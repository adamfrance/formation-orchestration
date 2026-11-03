// salle-attente : la brique qui encaisse le choc de 10 h 00. Chaque fan qui arrive reçoit un créneau d'entrée ; les
// créneaux s'enchaînent au débit fixé (DEBIT_PAR_SECONDE, dans un ConfigMap) ; à l'heure de son créneau, le fan reçoit
// un jeton signé qui lui ouvre la vente. Le service ne garde rien en mémoire : la file est dans Valkey, on peut donc
// lancer autant d'exemplaires qu'il faut.
//
// Routes : GET / (la page du fan), POST /file/entrer, GET /file/{id} (mon tour ?), GET /file (état de la file).
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"embed"        // la page HTML est rangée dans le programme : une seule image, rien à copier à côté
	"encoding/hex" // l'identifiant du fan, en hexadécimal
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/valkey-io/valkey-go" // le client officiel de Valkey (la base clé-valeur en mémoire)

	"ruee/internal/commun"
	"ruee/internal/jeton"
)

//go:embed page
var page embed.FS // le dossier page/ (index.html), inclus dans le programme à la compilation

// Réserve le prochain créneau d'entrée, en une seule opération dans Valkey : deux exemplaires du service ne peuvent
// pas donner le même créneau. Un créneau tous les « pas » microsecondes, jamais dans le passé.
// Ce petit programme (en Lua, le langage de script de Valkey) s'exécute dans Valkey, d'un seul tenant.
var reserverCreneau = valkey.NewLuaScript(`
local maintenant = tonumber(ARGV[1])                         -- l'heure actuelle, en microsecondes
local pas = tonumber(ARGV[2])                                -- l'écart entre deux entrées
local prochain = tonumber(redis.call('GET', KEYS[1]) or '0') -- le prochain créneau libre (0 au début)
local creneau = math.max(maintenant, prochain)               -- file vide : on entre tout de suite
redis.call('SET', KEYS[1], string.format('%.0f', creneau + pas)) -- le créneau suivant pour le prochain fan
local rang = redis.call('INCR', KEYS[2])                     -- le rang du fan : 1, 2, 3…
return {rang, string.format('%.0f', creneau)}
`)

type salle struct {
	kv         valkey.Client      // la connexion à Valkey, partagée par toutes les requêtes
	cle        ed25519.PrivateKey // la clé qui signe les jetons (dans un Secret)
	pas        float64            // microsecondes entre deux entrées : 1 000 000 / DEBIT_PAR_SECONDE
	dureeJeton time.Duration      // validité du jeton d'entrée
}

var jetonsEmis = promauto.NewCounter(prometheus.CounterOpts{
	Name: "ruee_jetons_emis_total", Help: "Jetons d'entrée remis aux fans dont le tour est venu.",
})

func main() {
	// « salle-attente cles » : fabrique la paire de clés du jeton, puis s'arrête (utilisé à l'installation)
	if len(os.Args) > 1 && os.Args[1] == "cles" {
		privee, publique, err := jeton.NouvellesCles()
		if err != nil {
			commun.Arreter("clés", err)
		}
		fmt.Printf("CLE_PRIVEE=%s\nCLE_PUBLIQUE=%s\n", privee, publique)
		return
	}

	commun.Journal("salle-attente")
	ctx, arreter := commun.Contexte() // annulé par SIGTERM
	defer arreter()

	// La configuration, lue dans l'environnement (ConfigMap et Secret en production)
	debit, err := strconv.ParseFloat(commun.Env("DEBIT_PAR_SECONDE", "50"), 64) // fans admis par seconde
	if err != nil || debit <= 0 {
		commun.Arreter("DEBIT_PAR_SECONDE", err)
	}
	dureeJeton, err := time.ParseDuration(commun.Env("DUREE_JETON", "10m")) // « 10m » = 10 minutes
	if err != nil {
		commun.Arreter("DUREE_JETON", err)
	}
	cle, err := jeton.ClePrivee(commun.EnvObligatoire("CLE_PRIVEE"))
	if err != nil {
		commun.Arreter("CLE_PRIVEE", err)
	}
	var kv valkey.Client
	commun.Attendre("connexion à Valkey", func() (err error) {
		kv, err = valkey.NewClient(valkey.ClientOption{
			InitAddress:  []string{commun.Env("VALKEY_ADRESSE", "localhost:6379")}, // le Service Kubernetes de Valkey
			DisableCache: true,                                                     // toujours la valeur à jour, pas de copie locale
		})
		return err
	})
	defer kv.Close()
	s := &salle{kv: kv, cle: cle, pas: 1e6 / debit, dureeJeton: dureeJeton}

	// L'état de la file pour Prometheus : le même pour tous les exemplaires, puisqu'il est dans Valkey
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "ruee_file_inscrits", Help: "Fans entrés dans la file."},
		func() float64 { i, _, _ := s.compteurs(context.Background()); return float64(i) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "ruee_file_en_attente", Help: "Fans qui attendent leur tour."},
		func() float64 { i, a, _ := s.compteurs(context.Background()); return float64(i - a) })

	// Les routes : « méthode chemin » → fonction qui répond
	mux := http.NewServeMux()
	contenu, _ := fs.Sub(page, "page")
	mux.Handle("GET /", http.FileServerFS(contenu)) // la page du fan
	mux.HandleFunc("POST /file/entrer", commun.Mesurer("/file/entrer", s.entrer))
	mux.HandleFunc("GET /file/{id}", commun.Mesurer("/file/{id}", s.etat))
	mux.HandleFunc("GET /file", commun.Mesurer("/file", s.resume))
	commun.Servir(ctx, mux, func(ctx context.Context) error { // prêt si Valkey répond
		return kv.Do(ctx, kv.B().Ping().Build()).Error()
	})
}

// microsecondes : une heure exprimée en microsecondes depuis 1970, le format des créneaux
func microsecondes(t time.Time) float64 { return float64(t.UnixMicro()) }

// entrer donne au fan son rang, son créneau et un identifiant impossible à deviner : c'est avec lui qu'il suivra
// son tour (avec un simple numéro, n'importe qui demanderait le numéro 1 et doublerait toute la file).
func (s *salle) entrer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resultat, err := reserverCreneau.Exec(ctx, s.kv, []string{"file:prochain", "file:inscrits"}, []string{
		strconv.FormatFloat(microsecondes(time.Now()), 'f', 0, 64), // maintenant
		strconv.FormatFloat(s.pas, 'f', 0, 64),                     // l'écart entre deux entrées
	}).ToArray()
	if err != nil || len(resultat) != 2 {
		commun.Erreur(w, http.StatusServiceUnavailable, "file indisponible")
		return
	}
	rang, _ := resultat[0].AsInt64()
	creneau, _ := resultat[1].ToString()

	octets := make([]byte, 16)
	_, _ = rand.Read(octets)         // 128 bits de hasard : impossible à deviner
	id := hex.EncodeToString(octets) // 32 caractères hexadécimaux
	// La fiche du fan, « rang:créneau », effacée d'elle-même au bout de 2 heures (Ex)
	err = s.kv.Do(ctx, s.kv.B().Set().Key("file:fan:"+id).Value(fmt.Sprintf("%d:%s", rang, creneau)).
		Ex(2*time.Hour).Build()).Error()
	if err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "file indisponible")
		return
	}
	commun.EcrireJSON(w, http.StatusCreated, map[string]any{"id": id, "rang": rang})
}

// etat dit au fan combien de personnes sont encore devant lui, ou lui remet son jeton si son tour est venu.
func (s *salle) etat(w http.ResponseWriter, r *http.Request) {
	fiche, err := s.kv.Do(r.Context(), s.kv.B().Get().Key("file:fan:"+r.PathValue("id")).Build()).ToString()
	if valkey.IsValkeyNil(err) { // identifiant inconnu ou fiche expirée
		commun.Erreur(w, http.StatusNotFound, "inconnu dans la file")
		return
	}
	if err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "file indisponible")
		return
	}
	texteRang, texteCreneau, _ := strings.Cut(fiche, ":")
	rang, _ := strconv.ParseInt(texteRang, 10, 64)
	creneau, _ := strconv.ParseFloat(texteCreneau, 64)

	if reste := creneau - microsecondes(time.Now()); reste > 0 { // pas encore son tour
		commun.EcrireJSON(w, http.StatusOK, map[string]any{
			"rang": rang, "admis": false,
			"devant":           int64(reste / s.pas),        // les fans dont le créneau vient avant le sien
			"attente_secondes": int(math.Ceil(reste / 1e6)), // arrondi à la seconde supérieure
		})
		return
	}
	// Son tour est venu : un jeton signé, valable DUREE_JETON
	j, err := jeton.Signer(s.cle, jeton.Contenu{Rang: rang, Expire: time.Now().Add(s.dureeJeton).Unix()})
	if err != nil {
		commun.Erreur(w, http.StatusInternalServerError, "jeton")
		return
	}
	jetonsEmis.Inc()
	commun.EcrireJSON(w, http.StatusOK, map[string]any{"rang": rang, "admis": true, "jeton": j})
}

// resume : le nombre de fans entrés dans la file et le nombre dont le tour est venu.
func (s *salle) resume(w http.ResponseWriter, r *http.Request) {
	inscrits, admis, err := s.compteurs(r.Context())
	if err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "file indisponible")
		return
	}
	commun.EcrireJSON(w, http.StatusOK, map[string]any{
		"inscrits": inscrits, "admis": admis, "en_attente": inscrits - admis, "debit_par_seconde": 1e6 / s.pas,
	})
}

// compteurs : les fans qui attendent encore sont ceux dont le créneau est dans le futur. Les créneaux se suivent à
// intervalle fixe jusqu'au prochain créneau libre : leur nombre se déduit de l'écart entre ce créneau et maintenant.
func (s *salle) compteurs(ctx context.Context) (inscrits, admis int64, err error) {
	// MGET : deux clés lues en un seul aller-retour
	valeurs, err := s.kv.Do(ctx, s.kv.B().Mget().Key("file:inscrits", "file:prochain").Build()).ToArray()
	if err != nil {
		return 0, 0, err
	}
	inscrits, _ = valeurs[0].AsInt64()
	texte, err := valeurs[1].ToString()
	if err != nil { // personne n'est encore entré
		return inscrits, 0, nil
	}
	prochain, _ := strconv.ParseFloat(texte, 64)
	enAttente := max(0, int64(math.Ceil((prochain-microsecondes(time.Now()))/s.pas))-1)
	return inscrits, max(0, inscrits-enAttente), nil
}
