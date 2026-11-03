// Package commun regroupe ce que partagent les services de Ruée : configuration, journal, serveur HTTP, santé, mesures.
// Chaque équipe écrit son service ; la manière de démarrer, de s'arrêter et de rendre compte est la même pour tous.
package commun

import (
	"context"       // annulation d'un travail en cours (arrêt demandé, délai dépassé)
	"encoding/json" // lecture et écriture du JSON des requêtes et des réponses
	"errors"        // comparaison d'erreurs
	"log/slog"      // journal structuré : une ligne JSON par événement
	"net/http"      // le serveur HTTP
	"os"            // variables d'environnement, sortie du programme
	"os/signal"     // réception des signaux d'arrêt
	"strconv"       // conversions texte ↔ nombre
	"sync/atomic"   // un indicateur partagé sans verrou
	"syscall"       // le signal SIGTERM envoyé par Kubernetes
	"time"          // durées et délais

	"github.com/prometheus/client_golang/prometheus"          // les types de mesures (compteurs, histogrammes)
	"github.com/prometheus/client_golang/prometheus/promauto" // création et enregistrement des mesures en une fois
	"github.com/prometheus/client_golang/prometheus/promhttp" // la page /metrics que lit Prometheus
)

// Env lit une variable d'environnement, avec une valeur par défaut.
// La configuration vient de l'extérieur (ConfigMap, Secret, Compose) : la même image sert partout.
func Env(nom, defaut string) string {
	if v := os.Getenv(nom); v != "" { // la variable existe et n'est pas vide
		return v
	}
	return defaut
}

// EnvObligatoire arrête le service si la variable manque : mieux vaut ne pas démarrer que démarrer mal configuré.
func EnvObligatoire(nom string) string {
	v := os.Getenv(nom)
	if v == "" {
		slog.Error("variable d'environnement manquante", "nom", nom) // le journal dit exactement ce qui manque
		os.Exit(1)                                                   // Kubernetes voit le conteneur échouer et le signale
	}
	return v
}

// Version est fixée à la construction de l'image (variable VERSION du Dockerfile).
func Version() string { return Env("VERSION", "dev") }

// Journal écrit des lignes JSON sur la sortie standard : la plateforme les collecte, le service ne gère aucun fichier.
func Journal(service string) {
	sortie := slog.NewJSONHandler(os.Stdout, nil)                                    // format JSON, sur la sortie standard
	slog.SetDefault(slog.New(sortie).With("service", service, "version", Version())) // chaque ligne dit qui l'écrit
}

// Contexte est annulé quand Kubernetes (SIGTERM) ou le terminal (Ctrl+C) demande l'arrêt.
func Contexte() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Attendre essaie une connexion pendant 2 minutes, toutes les 5 secondes. Au démarrage du cluster, le DNS, la base ou
// la file peuvent répondre après le service : mieux vaut patienter que s'arrêter (Kubernetes espacerait ensuite les
// relances, jusqu'à 5 minutes).
func Attendre(quoi string, essayer func() error) {
	for essai := 1; ; essai++ {
		err := essayer()
		if err == nil {
			return
		}
		if essai == 24 {
			Arreter(quoi, err)
		}
		slog.Warn(quoi+" : pas encore prêt, nouvel essai dans 5 s", "erreur", err)
		time.Sleep(5 * time.Second)
	}
}

// Arreter journalise une erreur fatale et quitte.
func Arreter(message string, err error) {
	slog.Error(message, "erreur", err)
	os.Exit(1)
}

// Les deux mesures communes à tous les services, lues par Prometheus sur /metrics
var (
	requetes = promauto.NewCounterVec(prometheus.CounterOpts{ // un compteur qui ne fait qu'augmenter
		Name: "ruee_requetes_http_total", Help: "Requêtes HTTP reçues, par route et par code de réponse.",
	}, []string{"route", "code"}) // un compteur par couple (route, code) : /api/commandes 201, 402…
	durees = promauto.NewHistogramVec(prometheus.HistogramOpts{ // la répartition des durées, par tranches
		Name: "ruee_requetes_http_secondes", Help: "Durée de traitement des requêtes HTTP.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}, // les tranches, de 5 ms à 5 s
	}, []string{"route"})
)

// reponse retient le code HTTP renvoyé, pour le compter ensuite
type reponse struct {
	http.ResponseWriter
	code int
}

func (r *reponse) WriteHeader(code int) { r.code = code; r.ResponseWriter.WriteHeader(code) }

// Mesurer compte chaque requête d'une route et mesure sa durée.
func Mesurer(route string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		debut := time.Now()                                                // l'heure d'arrivée de la requête
		rep := &reponse{ResponseWriter: w, code: http.StatusOK}            // 200 si le service ne dit rien d'autre
		h(rep, r)                                                          // le vrai travail de la route
		requetes.WithLabelValues(route, strconv.Itoa(rep.code)).Inc()      // +1 pour cette route et ce code
		durees.WithLabelValues(route).Observe(time.Since(debut).Seconds()) // la durée, rangée dans sa tranche
	}
}

// Servir lance le serveur HTTP : les routes du service, la santé et les mesures. Sur SIGTERM, la sonde de
// disponibilité passe en erreur (Kubernetes cesse d'envoyer du trafic), puis les requêtes en cours se terminent.
func Servir(ctx context.Context, mux *http.ServeMux, pret func(context.Context) error) {
	var arret atomic.Bool // devient vrai quand l'arrêt est demandé

	// Sonde de vie : « le programme répond » (sinon Kubernetes le redémarre)
	mux.HandleFunc("GET /sante/vivant", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// Sonde de disponibilité : « je peux servir » (sinon Kubernetes ne m'envoie plus de trafic, sans me redémarrer)
	mux.HandleFunc("GET /sante/pret", func(w http.ResponseWriter, r *http.Request) {
		if arret.Load() { // arrêt en cours : on refuse le nouveau trafic
			http.Error(w, "arrêt en cours", http.StatusServiceUnavailable)
			return
		}
		if pret != nil {
			if err := pret(r.Context()); err != nil { // la base ou la file ne répond pas
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /metrics", promhttp.Handler()) // les mesures, au format texte de Prometheus

	srv := &http.Server{
		Addr:              ":" + Env("PORT", "8080"), // le port d'écoute, 8080 par défaut
		Handler:           mux,                       // les routes déclarées par le service
		ReadHeaderTimeout: 5 * time.Second,           // un client trop lent est coupé (protection contre l'épuisement)
	}
	fini := make(chan struct{}) // fermé quand l'arrêt propre est terminé
	go func() {
		defer close(fini)
		<-ctx.Done()      // on attend la demande d'arrêt
		arret.Store(true) // la sonde de disponibilité échoue désormais
		// On laisse au Service de Kubernetes le temps de retirer ce pod de sa liste
		time.Sleep(time.Duration(envEntier("DELAI_ARRET_SECONDES", 3)) * time.Second)
		c, annuler := context.WithTimeout(context.Background(), 20*time.Second) // 20 s au plus pour finir
		defer annuler()
		_ = srv.Shutdown(c) // ne prend plus de connexions et termine les requêtes en cours
	}()
	slog.Info("démarrage", "adresse", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		Arreter("serveur HTTP", err) // port déjà pris, par exemple
	}
	<-fini // on ne quitte qu'une fois les requêtes en cours terminées
	slog.Info("arrêt terminé")
}

// envEntier lit une variable d'environnement entière, avec une valeur par défaut
func envEntier(nom string, defaut int) int {
	if n, err := strconv.Atoi(os.Getenv(nom)); err == nil {
		return n
	}
	return defaut
}

// EcrireJSON répond en JSON avec le code donné.
func EcrireJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Erreur répond {"erreur": "..."} avec le code donné.
func Erreur(w http.ResponseWriter, code int, message string) {
	EcrireJSON(w, code, map[string]string{"erreur": message})
}

// LireJSON lit le corps d'une requête dans v, 64 Kio au plus : un corps énorme est refusé, il ne sature pas la mémoire.
func LireJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(v)
}
