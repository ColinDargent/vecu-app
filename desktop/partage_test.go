package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/client/sync"
)

// TestAnnonceDitCeQuiPartEtCeQuiNePartPas : le texte qui engage le produit.
// « On annonce avant, on ne découvre pas après » : ce que la personne lit ici
// est le seul moment où elle peut encore dire non.
func TestAnnonceDitCeQuiPartEtCeQuiNePartPas(t *testing.T) {
	a := sync.Analyse{
		Chemin: "/Users/colin/Documents/Clients 2026", Fichiers: 128, Octets: 2_400_000,
		Ignores: 12, HorsPerimetre: 3, Complete: true,
	}
	got := annonce(a)
	for _, attendu := range []string{"Clients 2026", "reste où il est", "128 fichiers", "2.4 Mo", "12 fichiers", "3 fichiers", "non pris en charge"} {
		if !strings.Contains(got, attendu) {
			t.Errorf("l'annonce ne dit pas %q :\n%s", attendu, got)
		}
	}
	// Rien à signaler : pas de mention de minorant, qui inquiéterait pour rien.
	if strings.Contains(got, "minimums") {
		t.Errorf("annonce complète présentée comme un minorant :\n%s", got)
	}
}

// TestAnnonceDitLeMinorant : un parcours incomplet rend tous les comptes des
// minorants, et le taire serait un mensonge par omission.
func TestAnnonceDitLeMinorant(t *testing.T) {
	a := sync.Analyse{Chemin: "/tmp/x", Fichiers: 12, Illisibles: 4, Complete: false}
	got := annonce(a)
	if !strings.Contains(got, "minimums") {
		t.Errorf("le minorant n'est pas annoncé :\n%s", got)
	}
	if !strings.Contains(got, "4 fichiers n'ont pas pu être lus") {
		t.Errorf("les illisibles ne sont pas comptés :\n%s", got)
	}
}

// TestAnnonceRelaieLesGardesNonBloquantes : deux synchroniseurs sur les mêmes
// fichiers est le risque que le CDC nomme, et la garde est non bloquante par
// choix. Elle ne vaut donc que si elle est LUE.
func TestAnnonceRelaieLesGardesNonBloquantes(t *testing.T) {
	a := sync.Analyse{
		Chemin: "/Users/colin/Dropbox/Equipe", Fichiers: 3, Complete: true,
		Gardes: []sync.Garde{{Bloquant: false, Motif: "ce dossier semble déjà synchronisé par Dropbox"}},
	}
	got := annonce(a)
	if !strings.Contains(got, "Dropbox") {
		t.Errorf("garde non bloquante absente de l'annonce :\n%s", got)
	}
}

// TestRefusListeLesMotifsBloquants : et rien d'autre. Mélanger un avertissement
// à un refus laisse croire qu'on peut passer outre.
func TestRefusListeLesMotifsBloquants(t *testing.T) {
	a := sync.Analyse{
		Chemin: "/Users/colin", Gardes: []sync.Garde{
			{Bloquant: true, Motif: "c'est le dossier personnel entier"},
			{Bloquant: false, Motif: "ce dossier est un dépôt git"},
		},
	}
	got := refus(a)
	if !strings.Contains(got, "C'est le dossier personnel entier") {
		t.Errorf("motif bloquant absent :\n%s", got)
	}
	if strings.Contains(got, "dépôt git") {
		t.Errorf("un avertissement est présenté comme un refus :\n%s", got)
	}
}

func TestOctetsLisibles(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{{512, "512 octets"}, {2_400, "2 Ko"}, {2_400_000, "2.4 Mo"}, {3_100_000_000, "3.1 Go"}} {
		if got := octets(c.n); got != c.want {
			t.Errorf("octets(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestLibellePropositions : ce que la personne lit avant de décider. Un espace
// en lecture seule doit être annoncé comme tel : sans ça on l'édite, et les
// écritures sont refusées en silence.
func TestLibellePropositions(t *testing.T) {
	got := libellePropositions([]sync.Proposition{
		{Nom: "clients-2026", Libelle: "Clients 2026", Ecriture: true, Fichiers: 42},
		{Nom: "archives", Libelle: "Archives", Ecriture: false, Fichiers: 1},
	})
	if len(got) != 2 {
		t.Fatalf("deux propositions attendues : %v", got)
	}
	if !strings.Contains(got[0], "Clients 2026") || !strings.Contains(got[0], "42 fichiers") {
		t.Errorf("libellé incomplet : %q", got[0])
	}
	if strings.Contains(got[0], "lecture seule") {
		t.Errorf("un espace modifiable annoncé en lecture seule : %q", got[0])
	}
	if !strings.Contains(got[1], "lecture seule") || !strings.Contains(got[1], "1 fichier") {
		t.Errorf("lecture seule ou singulier manquant : %q", got[1])
	}
	if len(libellePropositions(nil)) != 0 {
		t.Error("une liste vide doit rester vide")
	}
}

// TestDossierPeuple : la prudence de `contientDesFichiers`, côté app. Ce qui est
// en jeu est l'envoi de fichiers à tout le monde, donc « je n'ai pas pu
// regarder » doit répondre « oui, il y a du contenu ».
func TestDossierPeuple(t *testing.T) {
	vide := t.TempDir()
	if peuple, err := dossierPeuple(vide); err != nil || peuple {
		t.Errorf("dossier vide : peuple=%v err=%v", peuple, err)
	}
	if peuple, err := dossierPeuple(filepath.Join(vide, "absent")); err != nil || peuple {
		t.Errorf("dossier absent : peuple=%v err=%v", peuple, err)
	}
	// Un .DS_Store posé par le Finder ne fait pas d'un dossier un dossier plein :
	// c'est la règle déjà retenue par l'adoption, après qu'elle a rendu tout
	// skill ordinaire inadoptable à vie sur un Mac.
	if err := os.WriteFile(filepath.Join(vide, ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if peuple, err := dossierPeuple(vide); err != nil || peuple {
		t.Errorf("un .DS_Store suffit à faire un dossier plein : peuple=%v err=%v", peuple, err)
	}
	if err := os.WriteFile(filepath.Join(vide, "note.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if peuple, err := dossierPeuple(vide); err != nil || !peuple {
		t.Errorf("dossier plein non détecté : peuple=%v err=%v", peuple, err)
	}
}

// TestPropositionAuNeSuitPasUnRangPerime : entre l'affichage et le clic, un
// cycle a pu retirer une proposition. Rejoindre celle qui a pris sa place
// serait monter un dossier que la personne n'a jamais choisi.
func TestPropositionAuNeSuitPasUnRangPerime(t *testing.T) {
	a := &app{}
	if _, ok := a.propositionAu(0); ok {
		t.Error("un rang lu avant tout rafraîchissement devrait être vide")
	}
	a.propositions.Store([]sync.Proposition{{Nom: "equipe"}})
	p, ok := a.propositionAu(0)
	if !ok || p.Nom != "equipe" {
		t.Errorf("rang 0 : %v %v", p, ok)
	}
	if _, ok := a.propositionAu(1); ok {
		t.Error("un rang au-delà de la liste a rendu une proposition")
	}
	if _, ok := a.propositionAu(-1); ok {
		t.Error("un rang négatif a rendu une proposition")
	}
}
