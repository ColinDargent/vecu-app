package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPartagerUnDossierQuiContientDejaUnReadme : le cas le plus banal du
// parcours A, et il était cassé.
//
// Trouvé le 21/08 au premier test réel, sur la machine. Un espace n'existe que
// s'il porte au moins un fichier lisible, donc sa création en déposait un :
// `README.md`. Le dossier adopté poussait ensuite le SIEN, sans marque-page,
// donc sur une histoire non apparentée - le serveur fusionnait, le fichier
// d'accueil prenait la place sur le disque, et le texte de la personne finissait
// dans une copie de conflit. Rien n'était perdu, mais le premier geste de la V1
// publique défigurait le dossier qu'on venait de lui confier, et ça vaut pour
// n'importe quel dossier de projet.
//
// Depuis, c'est le fichier de métadonnées qui fait exister l'espace. Son nom est
// réservé, il ne peut entrer en collision avec rien.
func TestPartagerUnDossierQuiContientDejaUnReadme(t *testing.T) {
	url, token, _, _ := serveurAvecMembre(t)
	racine := t.TempDir()
	if err := SaveConfig(racine, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	ailleurs := filepath.Join(t.TempDir(), "projet")
	if err := os.MkdirAll(ailleurs, 0o755); err != nil {
		t.Fatal(err)
	}
	mien := "# Mon projet\n\nCe que j'ai écrit, moi.\n"
	if err := os.WriteFile(filepath.Join(ailleurs, "README.md"), []byte(mien), 0o644); err != nil {
		t.Fatal(err)
	}

	cree, err := NewHTTPClient(url, token).CreerEspace("projet")
	if err != nil {
		t.Fatal(err)
	}
	if err := MonteEspace(racine, cree.Nom, ailleurs, true); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e.Close() })
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	// Un second cycle : la convergence a sa chance, et une copie qui n'arrive
	// qu'au cycle suivant est une copie quand même.
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	entrees, err := os.ReadDir(ailleurs)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range entrees {
		if strings.Contains(x.Name(), "conflit") {
			t.Errorf("copie de conflit dans un dossier qu'on vient de partager : %s", x.Name())
		}
	}
	b, err := os.ReadFile(filepath.Join(ailleurs, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != mien {
		t.Errorf("le README de la personne a été remplacé :\n%s", b)
	}
}
