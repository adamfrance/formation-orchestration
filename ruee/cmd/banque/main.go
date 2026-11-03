// banque : simule la passerelle de paiement de la banque, un système existant qui tourne hors du cluster et qu'on
// ne remplace pas. Elle répond en 150 à 400 ms, demande une clé d'accès et refuse les cartes qui finissent par 0000.
//
// Route : POST /v1/autorisations (en-tête X-Cle-Api) → {statut, autorisation}
package main

import (
	"crypto/rand"
	"crypto/subtle" // comparaison en temps constant : la durée ne trahit pas les bons caractères de la clé
	"encoding/hex"
	"math/big"
	"net/http"
	"strings"
	"time"

	"ruee/internal/commun"
)

func main() {
	commun.Journal("banque")
	ctx, arreter := commun.Contexte()
	defer arreter()
	cle := commun.EnvObligatoire("CLE_API") // la clé que la banque a remise à Ruée

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/autorisations", func(w http.ResponseWriter, r *http.Request) {
		// 1. La clé d'accès
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Cle-Api")), []byte(cle)) != 1 {
			commun.Erreur(w, http.StatusUnauthorized, "clé d'accès refusée")
			return
		}
		var demande struct {
			Reference string `json:"reference"`
			Montant   int    `json:"montant_centimes"`
			Carte     string `json:"carte"`
		}
		if err := commun.LireJSON(r, &demande); err != nil || demande.Montant <= 0 {
			commun.Erreur(w, http.StatusBadRequest, "demande illisible")
			return
		}
		// 2. Le temps de réponse d'une vraie banque : de 150 à 400 ms
		attente, _ := rand.Int(rand.Reader, big.NewInt(250))
		time.Sleep(time.Duration(150+attente.Int64()) * time.Millisecond)
		// 3. La décision
		if strings.HasSuffix(demande.Carte, "0000") {
			commun.EcrireJSON(w, http.StatusOK, map[string]string{"statut": "refuse"})
			return
		}
		numero := make([]byte, 6)
		_, _ = rand.Read(numero)
		commun.EcrireJSON(w, http.StatusOK, map[string]string{
			"statut": "accepte", "autorisation": "AUT-" + strings.ToUpper(hex.EncodeToString(numero)),
		})
	})
	commun.Servir(ctx, mux, nil)
}
