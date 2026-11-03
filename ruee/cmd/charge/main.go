// charge : simule l'ouverture des ventes de 10 h 00. Des fans arrivent en quelques secondes, prennent un rang,
// attendent leur tour, réservent et paient, comme sur la vraie page. Avec -robot, un seul client mitraille l'entrée
// de la file, comme un robot de revente : la passerelle doit le freiner (réponses 429).
//
//	charge -url http://localhost:8080 -fans 2000 -arrivee 10s
//	charge -url http://localhost:8080 -robot -duree 15s
package main

import (
	"bytes"
	"encoding/json"
	"flag" // les options de la ligne de commande (-fans, -url…)
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Les options, avec leur valeur par défaut
var (
	url     = flag.String("url", "http://localhost:8080", "adresse de la plateforme (la passerelle)")
	fans    = flag.Int("fans", 1000, "nombre de fans")
	arrivee = flag.Duration("arrivee", 10*time.Second, "durée pendant laquelle les fans arrivent")
	achat   = flag.Float64("achat", 0.3, "part des fans admis qui achètent (les autres regardent et repartent)")
	robot   = flag.Bool("robot", false, "un seul client qui mitraille l'entrée de la file")
	duree   = flag.Duration("duree", 15*time.Second, "durée du mode robot")
	// 200 connexions au plus, réutilisées : assez pour la charge, sans épuiser les ports du poste ni le relais de
	// la machine virtuelle (sans plafond, chaque fan ouvrirait la sienne : des milliers à la fois)
	client = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 200, MaxConnsPerHost: 200}}
)

// compteurs : ce que le test mesure, mis à jour par tous les fans en même temps
type compteurs struct {
	entres, admis, commandes, places atomic.Int64 // additions sans verrou
	sync.Mutex
	codes   map[int]int     // réponses en erreur, par code HTTP
	latence []time.Duration // durée des entrées dans la file
}

func (c *compteurs) erreur(code int) {
	c.Lock()
	c.codes[code]++
	c.Unlock()
}

func main() {
	flag.Parse()
	c := &compteurs{codes: map[int]int{}}
	if *robot {
		lancerRobot(c)
		return
	}
	debut := time.Now()
	fini := make(chan struct{})
	go afficher(c, debut, fini) // une ligne d'avancement toutes les 2 secondes
	var groupe sync.WaitGroup
	for i := 0; i < *fans; i++ { // un fan = une goroutine (un fil d'exécution léger)
		groupe.Add(1)
		go func() {
			defer groupe.Done()
			time.Sleep(time.Duration(rand.Int64N(int64(*arrivee)))) // chacun arrive à un moment au hasard
			unFan(c)
		}()
	}
	groupe.Wait() // on attend le dernier fan
	close(fini)
	resume(c, time.Since(debut))
}

// unFan : entrer dans la file, attendre son tour, regarder les concerts, réserver, payer.
// Chaque fan a sa propre adresse IP, transmise comme le ferait l'équilibreur de charge du cloud (X-Forwarded-For).
func unFan(c *compteurs) {
	adresse := fmt.Sprintf("10.%d.%d.%d", rand.IntN(256), rand.IntN(256), 1+rand.IntN(254))
	// 1. Entrer dans la file
	t := time.Now()
	var entree struct {
		ID string `json:"id"`
	}
	code := appeler("POST", "/file/entrer", nil, "", adresse, &entree)
	if code != http.StatusCreated {
		c.erreur(code)
		return
	}
	c.Lock()
	c.latence = append(c.latence, time.Since(t))
	c.Unlock()
	c.entres.Add(1)

	// 2. Attendre son tour : on redemande toutes les secondes, comme la page
	var tour struct {
		Admis bool   `json:"admis"`
		Jeton string `json:"jeton"`
	}
	for limite := time.Now().Add(10 * time.Minute); ; time.Sleep(time.Second) {
		if code := appeler("GET", "/file/"+entree.ID, nil, "", adresse, &tour); code != http.StatusOK {
			c.erreur(code)
			if time.Now().After(limite) {
				return
			}
			continue
		}
		if tour.Admis {
			break
		}
	}
	c.admis.Add(1)
	if rand.Float64() > *achat { // beaucoup regardent sans acheter
		return
	}
	// 3. Choisir un concert au hasard et réserver de 1 à 4 places
	var concerts []struct {
		ID     int `json:"id"`
		Libres int `json:"places_libres"`
	}
	if code := appeler("GET", "/api/concerts", nil, "", adresse, &concerts); code != http.StatusOK || len(concerts) == 0 {
		c.erreur(code)
		return
	}
	var reservation struct {
		Reservation string `json:"reservation"`
	}
	choix := map[string]int{"concert": concerts[rand.IntN(len(concerts))].ID, "nombre": 1 + rand.IntN(4)}
	if code := appeler("POST", "/api/reservations", choix, tour.Jeton, adresse, &reservation); code != http.StatusCreated {
		c.erreur(code)
		return
	}
	// 4. Payer avec une carte de test (acceptée : elle ne finit pas par 0000)
	carte := "4970" + strconv.Itoa(10000000+rand.IntN(89999999)) + "4242"
	paiement := map[string]string{"reservation": reservation.Reservation, "email": "fan@exemple.fr", "carte": carte}
	var commande struct {
		Billets int64 `json:"billets"`
	}
	if code := appeler("POST", "/api/commandes", paiement, "", adresse, &commande); code != http.StatusCreated {
		c.erreur(code)
		return
	}
	c.commandes.Add(1)
	c.places.Add(commande.Billets)
}

// appeler envoie une requête JSON et rend le code de la réponse (0 si le serveur n'a pas répondu)
func appeler(methode, chemin string, corps any, jeton, adresse string, reponse any) int {
	var contenu bytes.Buffer
	if corps != nil {
		_ = json.NewEncoder(&contenu).Encode(corps)
	}
	req, _ := http.NewRequest(methode, *url+chemin, &contenu)
	req.Header.Set("Content-Type", "application/json")
	if jeton != "" {
		req.Header.Set("Authorization", "Bearer "+jeton)
	}
	req.Header.Set("X-Forwarded-For", adresse) // l'adresse du client, vue par la passerelle
	rep, err := client.Do(req)
	if err != nil {
		return 0 // pas de réponse du tout (délai dépassé, connexion refusée)
	}
	defer rep.Body.Close()
	if rep.StatusCode < 300 && reponse != nil {
		_ = json.NewDecoder(rep.Body).Decode(reponse)
	}
	_, _ = io.Copy(io.Discard, rep.Body) // lire la réponse jusqu'au bout : sinon la connexion ne resservira pas
	return rep.StatusCode
}

// afficher : l'avancement toutes les 2 secondes, jusqu'à la fin du test
func afficher(c *compteurs, debut time.Time, fini chan struct{}) {
	for {
		select {
		case <-fini:
			return
		case <-time.After(2 * time.Second):
			c.Lock()
			erreurs := fmt.Sprint(c.codes)
			c.Unlock()
			fmt.Printf("%3.0fs  entrés %5d  admis %5d  commandes %4d  erreurs %s\n", time.Since(debut).Seconds(),
				c.entres.Load(), c.admis.Load(), c.commandes.Load(), erreurs)
		}
	}
}

// resume : le bilan, avec la médiane et le 95e centile du temps d'entrée dans la file
func resume(c *compteurs, total time.Duration) {
	slices.Sort(c.latence)
	quantile := func(q float64) time.Duration {
		if len(c.latence) == 0 {
			return 0
		}
		return c.latence[int(q*float64(len(c.latence)-1))].Round(time.Millisecond)
	}
	fmt.Printf("\nTerminé en %s : %d fans entrés, %d admis, %d commandes, %d places vendues\n",
		total.Round(time.Second), c.entres.Load(), c.admis.Load(), c.commandes.Load(), c.places.Load())
	fmt.Printf("Entrée dans la file : médiane %s, 95 %% en moins de %s ; erreurs par code : %v\n",
		quantile(0.5), quantile(0.95), c.codes)
}

// lancerRobot : 20 connexions en parallèle, sans aucune pause, pendant -duree.
func lancerRobot(c *compteurs) {
	fin := time.Now().Add(*duree)
	var ok, freine atomic.Int64
	var groupe sync.WaitGroup
	for i := 0; i < 20; i++ {
		groupe.Add(1)
		go func() {
			defer groupe.Done()
			for time.Now().Before(fin) {
				switch code := appeler("POST", "/file/entrer", nil, "", "203.0.113.66", nil); code { // toujours la même adresse
				case http.StatusCreated:
					ok.Add(1)
				case http.StatusTooManyRequests: // 429 : la passerelle a freiné le robot
					freine.Add(1)
				default:
					c.erreur(code)
				}
			}
		}()
	}
	groupe.Wait()
	secondes := duree.Seconds()
	fmt.Printf("Robot, %s : %d entrées acceptées (%.0f par seconde), %d freinées par la passerelle (429), autres %v\n",
		*duree, ok.Load(), float64(ok.Load())/secondes, freine.Load(), c.codes)
}
