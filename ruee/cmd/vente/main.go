// vente : le catalogue, la réservation et la commande, d'un seul bloc et avec une seule base PostgreSQL. C'est ce qui
// garantit qu'une place ne se vend jamais deux fois : le blocage d'une place et sa vente sont des transactions de la
// même base. La vente ne s'ouvre qu'avec un jeton de la salle d'attente.
//
// Routes : GET /api/concerts, POST /api/reservations (avec le jeton), POST /api/commandes, GET /api/version.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"         // le pilote PostgreSQL de référence en Go
	"github.com/jackc/pgx/v5/pgxpool" // un jeu de connexions réutilisées (ouvrir une connexion coûte cher)
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/valkey-io/valkey-go"

	"ruee/internal/commun"
	"ruee/internal/jeton"
)

type vente struct {
	base      *pgxpool.Pool     // la base PostgreSQL
	kv        valkey.Client     // la file « billets à émettre »
	cle       ed25519.PublicKey // pour vérifier les jetons (la vente ne peut pas en fabriquer)
	paiement  string            // adresse du service paiement
	blocage   time.Duration     // durée pendant laquelle une place réservée reste bloquée
	http      *http.Client      // pour appeler le service paiement
	catalogue cache             // le catalogue, recalculé au plus une fois par seconde
}

// Les mesures métier, lues par Prometheus : elles alimentent le tableau de bord « 10 h 00 »
var (
	reservations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ruee_reservations_total", Help: "Demandes de réservation, par résultat.",
	}, []string{"resultat"}) // acceptee, complet, refusee
	commandes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ruee_commandes_total", Help: "Commandes, par statut du paiement.",
	}, []string{"statut"}) // payee, refusee
	placesVendues = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ruee_places_vendues_total", Help: "Places vendues.",
	})
)

func main() {
	commun.Journal("vente")
	ctx, arreter := commun.Contexte()
	defer arreter()

	// La configuration : clé publique du jeton (ConfigMap), base et file (Secret, Service)
	cle, err := jeton.ClePublique(commun.EnvObligatoire("CLE_PUBLIQUE"))
	if err != nil {
		commun.Arreter("CLE_PUBLIQUE", err)
	}
	blocage, err := time.ParseDuration(commun.Env("DUREE_BLOCAGE", "10m"))
	if err != nil {
		commun.Arreter("DUREE_BLOCAGE", err)
	}
	base, err := pgxpool.New(ctx, commun.EnvObligatoire("BASE_URL")) // postgres://utilisateur:motdepasse@hote/base
	if err != nil {
		commun.Arreter("connexion à PostgreSQL", err)
	}
	defer base.Close()
	places, _ := strconv.Atoi(commun.Env("PLACES_PAR_CONCERT", "2000")) // 2 000 places par concert pour les démonstrations
	// La base peut démarrer après la vente (premier déploiement, bascule vers la copie) : on patiente
	commun.Attendre("préparation de la base", func() error { return preparerBase(ctx, base, places) })
	var kv valkey.Client
	commun.Attendre("connexion à Valkey", func() (err error) {
		kv, err = valkey.NewClient(valkey.ClientOption{
			InitAddress:  []string{commun.Env("VALKEY_ADRESSE", "localhost:6379")},
			DisableCache: true,
		})
		return err
	})
	defer kv.Close()

	v := &vente{base: base, kv: kv, cle: cle, blocage: blocage,
		paiement: commun.Env("PAIEMENT_URL", "http://localhost:8082"),
		http:     &http.Client{Timeout: 5 * time.Second}} // la banque ne répond pas ? on abandonne au bout de 5 s
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/concerts", commun.Mesurer("/api/concerts", v.concerts))
	mux.HandleFunc("POST /api/reservations", commun.Mesurer("/api/reservations", v.reserver))
	mux.HandleFunc("POST /api/commandes", commun.Mesurer("/api/commandes", v.commander))
	mux.HandleFunc("GET /api/version", v.version)
	commun.Servir(ctx, mux, base.Ping) // prêt si la base répond
}

// preparerBase crée les tables et les places si elles n'existent pas. Le verrou (pg_advisory_xact_lock) évite que
// deux exemplaires qui démarrent ensemble le fassent en même temps ; il se libère à la fin de la transaction.
func preparerBase(ctx context.Context, base *pgxpool.Pool, placesParConcert int) error {
	tx, err := base.Begin(ctx) // tout ou rien : une transaction
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // annule tout si on sort sans Commit (sans effet après un Commit)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4242)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, places, placesParConcert); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Le schéma : trois tables. Une place est « libre », « bloquee » (réservée, en attente de paiement) ou « vendue ».
const schema = `
CREATE TABLE IF NOT EXISTS concerts (
  id int PRIMARY KEY, ville text NOT NULL, stade text NOT NULL, date date NOT NULL);
CREATE TABLE IF NOT EXISTS commandes (
  id bigserial PRIMARY KEY, reservation uuid UNIQUE NOT NULL, email text NOT NULL,
  montant_centimes int NOT NULL, autorisation text NOT NULL, creee_le timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS places (
  id bigserial PRIMARY KEY,
  concert int NOT NULL REFERENCES concerts(id),
  numero int NOT NULL,
  categorie text NOT NULL,
  prix_centimes int NOT NULL,
  etat text NOT NULL DEFAULT 'libre' CHECK (etat IN ('libre', 'bloquee', 'vendue')),
  reservation uuid,                        -- la réservation qui bloque la place
  bloquee_jusqua timestamptz,              -- après cette heure, la place redevient disponible
  commande bigint REFERENCES commandes(id),
  code_billet text UNIQUE,                 -- le code présenté à l'entrée du stade, unique
  UNIQUE (concert, numero));
CREATE INDEX IF NOT EXISTS places_a_vendre ON places (concert, id) WHERE etat <> 'vendue';
CREATE INDEX IF NOT EXISTS places_reservation ON places (reservation);
INSERT INTO concerts VALUES
  (1, 'Saint-Denis', 'Stade de France', '2027-06-04'), (2, 'Saint-Denis', 'Stade de France', '2027-06-05'),
  (3, 'Marseille', 'Orange Vélodrome', '2027-06-09'), (4, 'Lyon', 'Groupama Stadium', '2027-06-12'),
  (5, 'Lille', 'Stade Pierre-Mauroy', '2027-06-16'), (6, 'Bordeaux', 'Matmut Atlantique', '2027-06-19'),
  (7, 'Nantes', 'Stade de la Beaujoire', '2027-06-23'), (8, 'Toulouse', 'Stadium de Toulouse', '2027-06-26'),
  (9, 'Nice', 'Allianz Riviera', '2027-06-30'), (10, 'Montpellier', 'Stade de la Mosson', '2027-07-03'),
  (11, 'Lens', 'Stade Bollaert-Delelis', '2027-07-07'), (12, 'Strasbourg', 'Stade de la Meinau', '2027-07-10')
ON CONFLICT DO NOTHING;
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'lecture') THEN  -- le compte en lecture seule de l'export
    GRANT SELECT ON concerts, places TO lecture;
  END IF;
END $$;`

// Les places de chaque concert : 20 % en fosse, 40 % en tribune basse, 40 % en tribune haute
const places = `
INSERT INTO places (concert, numero, categorie, prix_centimes)
SELECT c.id, n,
  CASE WHEN n <= $1 / 5 THEN 'Fosse' WHEN n <= $1 * 3 / 5 THEN 'Tribune basse' ELSE 'Tribune haute' END,
  CASE WHEN n <= $1 / 5 THEN 9500 WHEN n <= $1 * 3 / 5 THEN 7500 ELSE 5500 END
FROM concerts c, generate_series(1, $1::int) AS n
ON CONFLICT DO NOTHING`

// cache : le catalogue est demandé par tous les fans ; on le recalcule au plus une fois par seconde et par exemplaire.
type cache struct {
	sync.Mutex           // une requête à la fois recalcule le catalogue
	valeur     []byte    // le JSON déjà prêt
	date       time.Time // l'heure du dernier calcul
}

// concerts : la liste des concerts, le prix le plus bas et le nombre de places libres
func (v *vente) concerts(w http.ResponseWriter, r *http.Request) {
	v.catalogue.Lock()
	defer v.catalogue.Unlock()
	if time.Since(v.catalogue.date) > time.Second { // le cache a plus d'une seconde : on recalcule
		lignes, err := v.base.Query(r.Context(), `
			SELECT c.id, c.ville, c.stade, to_char(c.date, 'YYYY-MM-DD'), min(p.prix_centimes),
			  count(*) FILTER (WHERE p.etat = 'libre' OR (p.etat = 'bloquee' AND p.bloquee_jusqua < now()))
			FROM concerts c JOIN places p ON p.concert = c.id
			GROUP BY c.id ORDER BY c.date`)
		if err != nil {
			commun.Erreur(w, http.StatusServiceUnavailable, "catalogue indisponible")
			return
		}
		type concert struct {
			ID         int    `json:"id"`
			Ville      string `json:"ville"`
			Stade      string `json:"stade"`
			Date       string `json:"date"`
			PrixMin    int    `json:"prix_min_centimes"`
			Disponible int    `json:"places_libres"`
		}
		// Chaque ligne du résultat devient un concert
		liste, err := pgx.CollectRows(lignes, func(l pgx.CollectableRow) (concert, error) {
			var c concert
			err := l.Scan(&c.ID, &c.Ville, &c.Stade, &c.Date, &c.PrixMin, &c.Disponible)
			return c, err
		})
		if err != nil {
			commun.Erreur(w, http.StatusServiceUnavailable, "catalogue indisponible")
			return
		}
		v.catalogue.valeur, _ = json.Marshal(liste)
		v.catalogue.date = time.Now()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(v.catalogue.valeur)
}

// place : une place telle que la voit le fan (l'identifiant interne n'est pas renvoyé : json:"-")
type place struct {
	ID        int64  `json:"-"`
	Concert   int    `json:"concert"`
	Numero    int    `json:"numero"`
	Categorie string `json:"categorie"`
	Prix      int    `json:"prix_centimes"`
}

// reserver bloque des places pendant DUREE_BLOCAGE. FOR UPDATE SKIP LOCKED : chaque transaction prend des places que
// personne d'autre n'est en train de prendre, sans attendre ; deux fans ne peuvent pas bloquer la même place.
func (v *vente) reserver(w http.ResponseWriter, r *http.Request) {
	// 1. Le jeton de la salle d'attente, dans l'en-tête « Authorization: Bearer … »
	if _, err := jeton.Verifier(v.cle, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), time.Now()); err != nil {
		reservations.WithLabelValues("refusee").Inc()
		commun.Erreur(w, http.StatusUnauthorized, "passez par la salle d'attente ("+err.Error()+")")
		return
	}
	// 2. La demande : un concert et de 1 à 4 places
	var demande struct {
		Concert int `json:"concert"`
		Nombre  int `json:"nombre"`
	}
	if err := commun.LireJSON(r, &demande); err != nil || demande.Nombre < 1 || demande.Nombre > 4 {
		commun.Erreur(w, http.StatusBadRequest, "demande : un concert et de 1 à 4 places")
		return
	}
	// 3. Le blocage, dans une transaction
	ctx := r.Context()
	tx, err := v.base.Begin(ctx)
	if err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "vente indisponible")
		return
	}
	defer tx.Rollback(ctx)
	reservation := nouvelUUID()
	// On choisit les premières places disponibles (libres, ou bloquées mais expirées) et on les bloque
	lignes, err := tx.Query(ctx, `
		WITH choisies AS (
		  SELECT id FROM places
		  WHERE concert = $1 AND (etat = 'libre' OR (etat = 'bloquee' AND bloquee_jusqua < now()))
		  ORDER BY id LIMIT $2
		  FOR UPDATE SKIP LOCKED)
		UPDATE places p SET etat = 'bloquee', reservation = $3, bloquee_jusqua = now() + $4 * interval '1 second'
		FROM choisies WHERE p.id = choisies.id
		RETURNING p.id, p.concert, p.numero, p.categorie, p.prix_centimes`,
		demande.Concert, demande.Nombre, reservation, int(v.blocage.Seconds()))
	if err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "vente indisponible")
		return
	}
	bloquees, err := pgx.CollectRows(lignes, scannerPlace)
	if err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "vente indisponible")
		return
	}
	if len(bloquees) < demande.Nombre { // pas assez de places : on annule tout (le Rollback différé s'en charge)
		reservations.WithLabelValues("complet").Inc()
		commun.Erreur(w, http.StatusConflict, "plus assez de places pour ce concert")
		return
	}
	if err := tx.Commit(ctx); err != nil { // les places sont à ce fan pendant DUREE_BLOCAGE
		commun.Erreur(w, http.StatusServiceUnavailable, "vente indisponible")
		return
	}
	reservations.WithLabelValues("acceptee").Inc()
	commun.EcrireJSON(w, http.StatusCreated, map[string]any{
		"reservation": reservation, "places": bloquees, "montant_centimes": total(bloquees),
		"expire": time.Now().Add(v.blocage).Format(time.RFC3339),
	})
}

// commander fait payer la réservation, puis vend les places et dépose « billets à émettre » dans la file.
func (v *vente) commander(w http.ResponseWriter, r *http.Request) {
	var demande struct {
		Reservation string `json:"reservation"`
		Email       string `json:"email"`
		Carte       string `json:"carte"`
	}
	if err := commun.LireJSON(r, &demande); err != nil || !strings.Contains(demande.Email, "@") {
		commun.Erreur(w, http.StatusBadRequest, "demande : réservation, courriel et carte")
		return
	}
	// 1. Les places de la réservation, encore bloquées et pas expirées
	ctx := r.Context()
	lignes, err := v.base.Query(ctx, `
		SELECT id, concert, numero, categorie, prix_centimes FROM places
		WHERE reservation = $1 AND etat = 'bloquee' AND bloquee_jusqua > now()`, demande.Reservation)
	if err != nil {
		commun.Erreur(w, http.StatusBadRequest, "réservation inconnue")
		return
	}
	bloquees, err := pgx.CollectRows(lignes, scannerPlace)
	if err != nil || len(bloquees) == 0 {
		commun.Erreur(w, http.StatusGone, "réservation inconnue ou expirée")
		return
	}

	// 2. Le paiement, par le service paiement (la vente ne voit jamais la banque)
	autorisation, err := v.payer(ctx, demande.Reservation, total(bloquees), demande.Carte)
	if err != nil {
		commandes.WithLabelValues("refusee").Inc()
		commun.Erreur(w, http.StatusPaymentRequired, "paiement refusé ("+err.Error()+")")
		return
	}

	// 3. La vente : la condition « reservation = $2 AND etat = 'bloquee' » garantit qu'aucun autre fan n'a repris
	// ces places entre-temps (si le blocage a expiré pendant le paiement, la vente est annulée)
	tx, err := v.base.Begin(ctx)
	if err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "vente indisponible")
		return
	}
	defer tx.Rollback(ctx)
	var commande int64
	err = tx.QueryRow(ctx, `INSERT INTO commandes (reservation, email, montant_centimes, autorisation)
		VALUES ($1, $2, $3, $4) RETURNING id`, demande.Reservation, demande.Email, total(bloquees), autorisation).
		Scan(&commande) // le numéro de la nouvelle commande
	if err != nil { // la réservation est déjà commandée (double clic, par exemple)
		commun.Erreur(w, http.StatusConflict, "commande déjà enregistrée")
		return
	}
	// Chaque place vendue reçoit un code de billet tiré au hasard (12 caractères, unique)
	lignes, err = tx.Query(ctx, `
		UPDATE places SET etat = 'vendue', commande = $1,
		  code_billet = upper(substr(replace(gen_random_uuid()::text, '-', ''), 1, 12))
		WHERE reservation = $2 AND etat = 'bloquee'
		RETURNING id, concert, numero, categorie, prix_centimes, code_billet`, commande, demande.Reservation)
	if err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "vente indisponible")
		return
	}
	type billet struct {
		place
		Code string `json:"code"`
	}
	billets, err := pgx.CollectRows(lignes, func(l pgx.CollectableRow) (billet, error) {
		var b billet
		err := l.Scan(&b.ID, &b.Concert, &b.Numero, &b.Categorie, &b.Prix, &b.Code)
		return b, err
	})
	if err != nil || len(billets) != len(bloquees) { // une place a changé de main : on annule tout
		slog.Warn("places reprises pendant le paiement : remboursement à faire", "reservation", demande.Reservation)
		commun.Erreur(w, http.StatusConflict, "places reprises pendant le paiement, vous serez remboursé")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		commun.Erreur(w, http.StatusServiceUnavailable, "vente indisponible")
		return
	}
	commandes.WithLabelValues("payee").Inc()
	placesVendues.Add(float64(len(billets)))

	// 4. Le billet peut attendre quelques minutes : on le confie à la file, le service billets s'en charge
	message, _ := json.Marshal(map[string]any{"commande": commande, "email": demande.Email, "billets": billets})
	if err := v.kv.Do(ctx, v.kv.B().Lpush().Key("billets:a-emettre").Element(string(message)).Build()).Error(); err != nil {
		slog.Error("file des billets indisponible", "commande", commande, "erreur", err)
	}
	commun.EcrireJSON(w, http.StatusCreated, map[string]any{
		"commande": commande, "montant_centimes": total(bloquees), "billets": len(billets),
		"message": "paiement accepté, vos billets arrivent par courriel",
	})
}

// payer demande l'autorisation au service paiement, le seul qui parle à la banque.
func (v *vente) payer(ctx context.Context, reference string, montant int, carte string) (string, error) {
	corps, _ := json.Marshal(map[string]any{"reference": reference, "montant_centimes": montant, "carte": carte})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, v.paiement+"/paiements", strings.NewReader(string(corps)))
	req.Header.Set("Content-Type", "application/json")
	rep, err := v.http.Do(req)
	if err != nil { // délai dépassé, service arrêté…
		return "", errors.New("service de paiement injoignable")
	}
	defer rep.Body.Close()
	var reponse struct {
		Statut       string `json:"statut"`
		Autorisation string `json:"autorisation"`
	}
	if err := json.NewDecoder(rep.Body).Decode(&reponse); err != nil || reponse.Statut != "accepte" {
		return "", errors.New("refusé par la banque")
	}
	return reponse.Autorisation, nil // le numéro d'autorisation, gardé avec la commande
}

// version : la version et la couleur de cet exemplaire (bleu ou vert), pour suivre une bascule.
func (v *vente) version(w http.ResponseWriter, r *http.Request) {
	hote, _ := os.Hostname() // dans Kubernetes : le nom du pod
	commun.EcrireJSON(w, http.StatusOK, map[string]string{
		"version": commun.Version(), "couleur": commun.Env("COULEUR", "bleu"), "exemplaire": hote,
	})
}

// scannerPlace : une ligne de la table places → une place
func scannerPlace(l pgx.CollectableRow) (place, error) {
	var p place
	err := l.Scan(&p.ID, &p.Concert, &p.Numero, &p.Categorie, &p.Prix)
	return p, err
}

// total : le prix des places, en centimes (jamais de nombres à virgule pour de l'argent)
func total(places []place) int {
	somme := 0
	for _, p := range places {
		somme += p.Prix
	}
	return somme
}

// nouvelUUID : un identifiant aléatoire au format standard (UUID version 4), pour la réservation
func nouvelUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40 // version 4 (aléatoire)
	b[8] = b[8]&0x3f | 0x80 // variante standard
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
