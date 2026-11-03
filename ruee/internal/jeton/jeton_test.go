package jeton

import (
	"testing"
	"time"
)

// Les tests du jeton : la chaîne d'intégration continue les lance à chaque modification (go test ./...)

func cles(t *testing.T) (privee, publique string) {
	t.Helper()
	privee, publique, err := NouvellesCles()
	if err != nil {
		t.Fatal(err)
	}
	return privee, publique
}

func TestJetonValide(t *testing.T) {
	privee, publique := cles(t)
	cle, _ := ClePrivee(privee)
	verif, _ := ClePublique(publique)
	j, err := Signer(cle, Contenu{Rang: 42, Expire: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verifier(verif, j, time.Now())
	if err != nil || c.Rang != 42 {
		t.Fatalf("jeton refusé : %v (rang %d)", err, c.Rang)
	}
}

func TestJetonExpire(t *testing.T) {
	privee, publique := cles(t)
	cle, _ := ClePrivee(privee)
	verif, _ := ClePublique(publique)
	j, _ := Signer(cle, Contenu{Rang: 1, Expire: time.Now().Add(-time.Second).Unix()}) // déjà expiré
	if _, err := Verifier(verif, j, time.Now()); err != ErrExpire {
		t.Fatalf("attendu ErrExpire, obtenu %v", err)
	}
}

func TestJetonFalsifie(t *testing.T) {
	privee, publique := cles(t)
	cle, _ := ClePrivee(privee)
	verif, _ := ClePublique(publique)
	j, _ := Signer(cle, Contenu{Rang: 5000, Expire: time.Now().Add(time.Minute).Unix()})
	// Un robot remplace le contenu par celui d'un meilleur rang, en gardant la signature
	faux, _ := Signer(cle, Contenu{Rang: 1, Expire: time.Now().Add(time.Minute).Unix()})
	triche := faux[:len(faux)-len(j[len(j)-86:])] + j[len(j)-86:]
	if _, err := Verifier(verif, triche, time.Now()); err != ErrInvalide {
		t.Fatalf("attendu ErrInvalide, obtenu %v", err)
	}
}

func TestAutreCle(t *testing.T) {
	privee, _ := cles(t)
	_, autrePublique := cles(t) // la clé publique d'une autre paire
	cle, _ := ClePrivee(privee)
	verif, _ := ClePublique(autrePublique)
	j, _ := Signer(cle, Contenu{Rang: 1, Expire: time.Now().Add(time.Minute).Unix()})
	if _, err := Verifier(verif, j, time.Now()); err != ErrInvalide {
		t.Fatalf("attendu ErrInvalide, obtenu %v", err)
	}
}
