// billets : fabrique les billets et les envoie par courriel. Ce travail peut attendre quelques minutes : KEDA règle le
// nombre d'exemplaires sur la longueur de la file « billets à émettre », de 0 à N.
// La même image fait l'export de nuit vers le contrôle d'accès des stades : « billets export-stades » (un CronJob).
package main

import (
	"bytes"
	"context"
	"encoding/csv" // le fichier attendu par le contrôle d'accès des stades
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp" // l'envoi de courriel (protocole SMTP)
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pkg/sftp" // le dépôt de fichier sur le serveur des stades (SFTP, au-dessus de SSH)
	"github.com/valkey-io/valkey-go"
	"golang.org/x/crypto/ssh"

	"ruee/internal/commun"
)

func main() {
	// Une image, deux commandes : « billets » (par défaut) et « billets export-stades »
	if len(os.Args) > 1 && os.Args[1] == "export-stades" {
		commun.Journal("export-stades")
		if err := exporter(); err != nil {
			commun.Arreter("export vers les stades", err) // code de sortie 1 : Kubernetes marque la tâche en échec
		}
		return
	}
	commun.Journal("billets")
	emettre()
}

// commande : le message déposé par la vente dans la file
type commande struct {
	Commande int64  `json:"commande"`
	Email    string `json:"email"`
	Billets  []struct {
		Concert   int    `json:"concert"`
		Numero    int    `json:"numero"`
		Categorie string `json:"categorie"`
		Code      string `json:"code"`
	} `json:"billets"`
}

// emettre prend les commandes une par une dans la file. Sur SIGTERM (Kubernetes réduit le nombre d'exemplaires),
// il finit le billet en cours avant de s'arrêter : aucun billet n'est perdu.
func emettre() {
	ctx, arreter := commun.Contexte()
	defer arreter()
	var kv valkey.Client
	commun.Attendre("connexion à Valkey", func() (err error) {
		kv, err = valkey.NewClient(valkey.ClientOption{
			InitAddress:  []string{commun.Env("VALKEY_ADRESSE", "localhost:6379")},
			DisableCache: true,
		})
		return err
	})
	defer kv.Close()
	smtpAdresse := commun.Env("SMTP_ADRESSE", "localhost:1025") // le service de courriel
	slog.Info("démarrage", "file", "billets:a-emettre")

	for ctx.Err() == nil { // tant que l'arrêt n'est pas demandé
		// BRPOP : prend le plus ancien message de la file, ou attend qu'il en arrive un (5 secondes au plus)
		resultat, err := kv.Do(context.Background(), kv.B().Brpop().Key("billets:a-emettre").Timeout(5).Build()).AsStrSlice()
		if valkey.IsValkeyNil(err) { // rien en 5 secondes : on recommence (et on vérifie l'arrêt)
			continue
		}
		if err != nil {
			slog.Error("file indisponible", "erreur", err)
			time.Sleep(2 * time.Second)
			continue
		}
		var c commande
		if err := json.Unmarshal([]byte(resultat[1]), &c); err != nil { // resultat = [nom de la file, message]
			slog.Error("message illisible", "erreur", err)
			continue
		}
		if err := envoyer(smtpAdresse, c); err != nil {
			// Courriel impossible : le message retourne en tête de file, il sera repris
			slog.Error("courriel non envoyé, remis dans la file", "commande", c.Commande, "erreur", err)
			_ = kv.Do(context.Background(), kv.B().Rpush().Key("billets:a-emettre").Element(resultat[1]).Build()).Error()
			time.Sleep(2 * time.Second)
			continue
		}
		slog.Info("billets envoyés", "commande", c.Commande, "billets", len(c.Billets))
	}
	slog.Info("arrêt terminé")
}

// envoyer écrit le courriel (en-têtes, puis texte) et le confie au serveur SMTP
func envoyer(adresse string, c commande) error {
	var corps strings.Builder
	fmt.Fprintf(&corps, "From: Ruée <billets@ruee.example>\r\nTo: %s\r\nSubject: Vos billets, commande %d\r\n", c.Email, c.Commande)
	corps.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\nBonjour,\r\n\r\nVoici vos billets :\r\n\r\n")
	for _, b := range c.Billets {
		fmt.Fprintf(&corps, "- concert %d, %s, place %d : code %s\r\n", b.Concert, b.Categorie, b.Numero, b.Code)
	}
	corps.WriteString("\r\nPrésentez ce code à l'entrée du stade.\r\n")
	return smtp.SendMail(adresse, nil, "billets@ruee.example", []string{c.Email}, []byte(corps.String()))
}

// exporter envoie la liste des billets valides au contrôle d'accès des stades : un fichier CSV déposé en SFTP,
// comme ce système existant l'attend. Le compte de base de données n'a que le droit de lire.
func exporter() error {
	ctx, annuler := context.WithTimeout(context.Background(), 5*time.Minute) // l'export ne doit pas traîner la nuit
	defer annuler()

	// 1. Les billets vendus, lus dans la base
	base, err := pgx.Connect(ctx, commun.EnvObligatoire("BASE_URL"))
	if err != nil {
		return err
	}
	defer base.Close(ctx)
	lignes, err := base.Query(ctx, `
		SELECT p.code_billet, c.ville, c.stade, to_char(c.date, 'YYYY-MM-DD'), p.categorie, p.numero
		FROM places p JOIN concerts c ON c.id = p.concert
		WHERE p.etat = 'vendue' ORDER BY c.id, p.numero`)
	if err != nil {
		return err
	}

	// 2. Le fichier CSV, en mémoire (séparateur « ; », comme le veut le système des stades)
	var fichier bytes.Buffer
	ecrivain := csv.NewWriter(&fichier)
	ecrivain.Comma = ';'
	_ = ecrivain.Write([]string{"code", "ville", "stade", "date", "categorie", "place"})
	n := 0
	for lignes.Next() {
		var code, ville, stade, date, categorie string
		var numero int
		if err := lignes.Scan(&code, &ville, &stade, &date, &categorie, &numero); err != nil {
			return err
		}
		_ = ecrivain.Write([]string{code, ville, stade, date, categorie, strconv.Itoa(numero)})
		n++
	}
	if err := lignes.Err(); err != nil {
		return err
	}
	ecrivain.Flush()

	// 3. La connexion au serveur des stades. Il est reconnu à son empreinte : on ne dépose pas la liste des billets
	// chez un inconnu qui se ferait passer pour lui
	empreinte := commun.EnvObligatoire("STADES_EMPREINTE") // « SHA256:… », relevée une fois pour toutes
	config := &ssh.ClientConfig{
		User:              commun.Env("STADES_UTILISATEUR", "stades"),
		Auth:              []ssh.AuthMethod{ssh.Password(commun.EnvObligatoire("STADES_MOT_DE_PASSE"))}, // d'un Secret
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},                                                 // le type de clé dont on connaît l'empreinte
		HostKeyCallback: func(_ string, _ net.Addr, cle ssh.PublicKey) error {
			if ssh.FingerprintSHA256(cle) != empreinte {
				return errors.New("empreinte du serveur des stades inattendue : " + ssh.FingerprintSHA256(cle))
			}
			return nil
		},
		Timeout: 10 * time.Second,
	}
	connexion, err := ssh.Dial("tcp", commun.EnvObligatoire("STADES_ADRESSE"), config) // « hôte:port »
	if err != nil {
		return err
	}
	defer connexion.Close()
	client, err := sftp.NewClient(connexion)
	if err != nil {
		return err
	}
	defer client.Close()

	// 4. Le dépôt du fichier, avec la date et l'heure dans son nom
	dossier := commun.Env("STADES_DOSSIER", "depot")
	if err := client.MkdirAll(dossier); err != nil {
		return err
	}
	nom := path.Join(dossier, "billets-"+time.Now().Format("20060102-150405")+".csv")
	distant, err := client.Create(nom)
	if err != nil {
		return err
	}
	if _, err := distant.Write(fichier.Bytes()); err != nil {
		return err
	}
	if err := distant.Close(); err != nil {
		return err
	}
	slog.Info("export déposé", "fichier", nom, "billets", n)
	return nil
}
