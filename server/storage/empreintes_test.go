package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEmpreintes : la primitive de DAR-198 contre un vrai dépôt.
func TestEmpreintes(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatalf("Init : %v", err)
	}
	if _, err := s.Write("shared/note.md", "v1", "colin"); err != nil {
		t.Fatalf("Write : %v", err)
	}
	// Un chemin ACCENTUE et un chemin A ESPACE : `git ls-tree` échappe les
	// accents par défaut (`core.quotePath`), et c'est précisément le piège qui
	// a fait passer deux notes pour un espace fantôme le 01/09.
	if _, err := s.Write("shared/été chaud.md", "soleil", "colin"); err != nil {
		t.Fatalf("Write accentué : %v", err)
	}
	tete1, err := s.Head()
	if err != nil {
		t.Fatalf("Head : %v", err)
	}

	emp, err := s.Empreintes("")
	if err != nil {
		t.Fatalf("Empreintes : %v", err)
	}
	if len(emp) != 2 {
		t.Fatalf("%d empreintes, attendu 2 : %v", len(emp), emp)
	}
	if _, ok := emp["shared/été chaud.md"]; !ok {
		t.Errorf("le chemin accentué manque ou est échappé : %v", emp)
	}
	for c, h := range emp {
		if len(h) < 7 || strings.ContainsAny(h, " \t") {
			t.Errorf("%s : empreinte %q ne ressemble pas à un OID", c, h)
		}
	}

	// L'empreinte CHANGE quand le contenu change, et pas avant. C'est ce qui
	// distingue « en retard » de « à jour ».
	avant := emp["shared/note.md"]
	if _, err := s.Write("shared/note.md", "v2", "colin"); err != nil {
		t.Fatalf("Write v2 : %v", err)
	}
	apres, err := s.Empreintes("")
	if err != nil {
		t.Fatalf("Empreintes après : %v", err)
	}
	if apres["shared/note.md"] == avant {
		t.Error("l'empreinte n'a pas bougé alors que le contenu a changé")
	}
	if apres["shared/été chaud.md"] != emp["shared/été chaud.md"] {
		t.Error("l'empreinte d'un fichier intact a bougé")
	}

	// L'arbre A UNE REVISION ANTERIEURE porte l'ancien contenu. C'est ce qui
	// permet de lire l'état d'un poste resté en arrière.
	vieux, err := s.Empreintes(tete1)
	if err != nil {
		t.Fatalf("Empreintes(tete1) : %v", err)
	}
	if vieux["shared/note.md"] != avant {
		t.Errorf("l'arbre à %s ne porte pas l'ancienne empreinte", tete1[:7])
	}
}

// TestEmpreintesTeteInconnue : un poste peut envoyer n'importe quoi comme tête.
// Le contrat est une ERREUR PROPRE, jamais une panique et jamais une carte vide
// silencieuse - une carte vide se lirait « ce poste n'a aucun fichier », donc
// tout en rouge, alors que la vérité est « je ne sais pas lire sa position ».
func TestEmpreintesTeteInconnue(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatalf("Init : %v", err)
	}
	s.Write("shared/note.md", "v1", "colin")

	for _, tete := range []string{
		"pas-un-oid",
		"0000000000000000000000000000000000000000",
		"'; rm -rf /",
	} {
		emp, err := s.Empreintes(tete)
		if err == nil {
			t.Errorf("Empreintes(%q) rend %d empreintes sans erreur, attendu une erreur", tete, len(emp))
		}
	}
}
