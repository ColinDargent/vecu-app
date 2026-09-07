package sync

import (
	"path/filepath"
	"testing"
)

// TestVerrouRefuseLeSecond : deux moteurs ne synchronisent jamais le même
// contenu. C'est LA garantie du fichier, celle qui a fait les copies de conflit
// en série en août quand elle manquait.
//
// Le test est PORTABLE, et c'est ce qui lui donne sa valeur pour le port : il
// s'exécute sur macOS avec `flock` et sur Windows avec `LockFileEx`, sans une
// ligne de différence. Deux prises successives ouvrent deux descripteurs
// distincts, ce que les deux systèmes traitent comme deux prétendants - la
// seconde doit échouer des deux côtés.
func TestVerrouRefuseLeSecond(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sous-dossier", "test.lock")

	premier, err := VerrouExclusif(p)
	if err != nil {
		t.Fatalf("première prise refusée alors que rien ne tient le verrou : %v", err)
	}
	defer premier.Close()

	second, err := VerrouExclusif(p)
	if err == nil {
		second.Close()
		t.Fatal("SECONDE PRISE ACCEPTÉE : deux moteurs pourraient synchroniser le même dossier")
	}
}

// TestVerrouLibereALaFermeture : sans ça, un moteur qui s'arrête proprement
// laisserait le poste incapable de redémarrer jusqu'au prochain redémarrage.
func TestVerrouLibereALaFermeture(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.lock")

	premier, err := VerrouExclusif(p)
	if err != nil {
		t.Fatalf("première prise : %v", err)
	}
	premier.Close()

	second, err := VerrouExclusif(p)
	if err != nil {
		t.Fatalf("verrou non relâché après fermeture : %v", err)
	}
	second.Close()
}
