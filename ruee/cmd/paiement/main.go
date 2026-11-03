// paiement : la seule brique autorisée à parler à la banque (un système existant, hors du cluster). Elle est à part
// pour la conformité (PCI DSS, la norme des données de carte) : le périmètre qui voit des numéros de carte reste
// minuscule. Elle ne garde aucun numéro et n'en écrit jamais un en entier dans le journal.
//
// Route : POST /paiements {reference, montant_centimes, carte} → {statut, autorisation}
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"ruee/internal/commun"
)

var paiements = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "ruee_paiements_total", Help: "Demandes d'autorisation à la banque, par statut.",
}, []string{"statut"}) // accepte, refuse, invalide, banque_injoignable…

func main() {
	commun.Journal("paiement")
	ctx, arreter := commun.Contexte()
	defer arreter()

	banque := commun.Env("BANQUE_URL", "http://localhost:9000") // l'adresse de la banque, hors du cluster
	cle := commun.EnvObligatoire("BANQUE_CLE")                  // vient d'un Secret, jamais du code ni de l'image
	client := &http.Client{Timeout: 4 * time.Second}            // au-delà, on considère la banque injoignable

	mux := http.NewServeMux()
	mux.HandleFunc("POST /paiements", commun.Mesurer("/paiements", func(w http.ResponseWriter, r *http.Request) {
		// 1. La demande de la vente, contrôlée avant d'aller plus loin
		var demande struct {
			Reference string `json:"reference"`
			Montant   int    `json:"montant_centimes"`
			Carte     string `json:"carte"`
		}
		if err := commun.LireJSON(r, &demande); err != nil || demande.Montant <= 0 || !carteValide(demande.Carte) {
			paiements.WithLabelValues("invalide").Inc()
			commun.Erreur(w, http.StatusBadRequest, "demande de paiement invalide")
			return
		}
		// 2. La demande d'autorisation à la banque, avec la clé d'accès dans un en-tête
		corps, _ := json.Marshal(demande)
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, banque+"/v1/autorisations",
			strings.NewReader(string(corps)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cle-Api", cle)
		rep, err := client.Do(req)
		if err != nil {
			paiements.WithLabelValues("banque_injoignable").Inc()
			slog.Error("banque injoignable", "reference", demande.Reference, "erreur", err)
			commun.Erreur(w, http.StatusBadGateway, "banque injoignable")
			return
		}
		defer rep.Body.Close()
		// 3. La réponse de la banque, relayée à la vente
		var reponse struct {
			Statut       string `json:"statut"`
			Autorisation string `json:"autorisation"`
		}
		if rep.StatusCode != http.StatusOK || json.NewDecoder(rep.Body).Decode(&reponse) != nil {
			paiements.WithLabelValues("erreur_banque").Inc()
			slog.Error("réponse de la banque", "reference", demande.Reference, "code", rep.StatusCode)
			commun.Erreur(w, http.StatusBadGateway, "réponse de la banque illisible")
			return
		}
		paiements.WithLabelValues(reponse.Statut).Inc()
		// Le journal ne garde que les 4 derniers chiffres de la carte
		slog.Info("paiement", "reference", demande.Reference, "carte", masquer(demande.Carte),
			"montant_centimes", demande.Montant, "statut", reponse.Statut)
		commun.EcrireJSON(w, http.StatusOK, reponse)
	}))
	commun.Servir(ctx, mux, nil)
}

// carteValide : de 12 à 19 chiffres, rien d'autre
func carteValide(carte string) bool {
	if len(carte) < 12 || len(carte) > 19 {
		return false
	}
	for _, c := range carte {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// masquer ne garde que les quatre derniers chiffres : « ************4242 ».
func masquer(carte string) string {
	return strings.Repeat("*", len(carte)-4) + carte[len(carte)-4:]
}
