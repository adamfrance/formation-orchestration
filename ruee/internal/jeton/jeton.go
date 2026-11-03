// Package jeton : le jeton d'entrée que la salle d'attente remet au fan quand son tour arrive.
// La salle d'attente le signe avec sa clé privée ; la vente le vérifie avec la clé publique seulement :
// elle peut contrôler un jeton, pas en fabriquer. Même principe qu'un billet infalsifiable : tout le monde peut
// vérifier le tampon, seul le guichet possède le tampon.
package jeton

import (
	"crypto/ed25519"  // signature numérique Ed25519 : clés courtes, signature et vérification très rapides
	"crypto/rand"     // le hasard cryptographique, pour fabriquer les clés
	"encoding/base64" // les octets transformés en texte transportable dans une URL ou un en-tête
	"encoding/json"   // le contenu du jeton
	"errors"
	"strings"
	"time"
)

// Contenu : le rang du fan dans la file et la fin de validité du jeton (secondes Unix).
type Contenu struct {
	Rang   int64 `json:"rang"`
	Expire int64 `json:"exp"`
}

var (
	ErrInvalide = errors.New("jeton invalide") // signature fausse ou jeton abîmé
	ErrExpire   = errors.New("jeton expiré")   // signature juste, mais trop tard
)

var b64 = base64.RawURLEncoding // base64 sans « = » final, utilisable tel quel dans une URL

// Signer fabrique le jeton : « contenu.signature », les deux parties en base64.
func Signer(cle ed25519.PrivateKey, c Contenu) (string, error) {
	charge, err := json.Marshal(c) // {"rang":42,"exp":1793461684}
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(cle, charge) // 64 octets que seule la clé privée sait produire
	return b64.EncodeToString(charge) + "." + b64.EncodeToString(signature), nil
}

// Verifier contrôle la signature et la date de fin, puis rend le contenu.
func Verifier(cle ed25519.PublicKey, jeton string, maintenant time.Time) (Contenu, error) {
	partie, signature, ok := strings.Cut(jeton, ".") // on sépare le contenu de la signature
	if !ok {
		return Contenu{}, ErrInvalide
	}
	charge, err1 := b64.DecodeString(partie)
	sig, err2 := b64.DecodeString(signature)
	if err1 != nil || err2 != nil || !ed25519.Verify(cle, charge, sig) { // un seul octet changé et la signature est fausse
		return Contenu{}, ErrInvalide
	}
	var c Contenu
	if err := json.Unmarshal(charge, &c); err != nil {
		return Contenu{}, ErrInvalide
	}
	if maintenant.Unix() > c.Expire { // un jeton volé ne sert que quelques minutes
		return c, ErrExpire
	}
	return c, nil
}

// NouvellesCles fabrique une paire de clés, en base64 : la privée va dans un Secret, la publique dans un ConfigMap.
func NouvellesCles() (privee, publique string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	// La clé privée se résume à sa « graine » de 32 octets, dont tout le reste se recalcule
	return base64.StdEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(pub), nil
}

// ClePrivee relit la clé privée (sa graine de 32 octets, en base64).
func ClePrivee(b string) (ed25519.PrivateKey, error) {
	graine, err := base64.StdEncoding.DecodeString(b)
	if err != nil || len(graine) != ed25519.SeedSize {
		return nil, errors.New("clé privée illisible")
	}
	return ed25519.NewKeyFromSeed(graine), nil
}

// ClePublique relit la clé publique (32 octets, en base64).
func ClePublique(b string) (ed25519.PublicKey, error) {
	cle, err := base64.StdEncoding.DecodeString(b)
	if err != nil || len(cle) != ed25519.PublicKeySize {
		return nil, errors.New("clé publique illisible")
	}
	return ed25519.PublicKey(cle), nil
}
