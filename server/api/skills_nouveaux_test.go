package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// TestSignalDeNaissance : F4. Depuis le 28/07 un skill naît privé, et personne
// n'apprend jamais qu'il existe - donc personne ne demande à le voir, donc rien
// ne se partage. Mesuré le 19/08 : 25 skills, tous de `colin`, et aucune sonde
// ne peut dire depuis son compte si Achille en a déposé.
//
// Ce test fige les deux moitiés de la décision : ce que le signal révèle, et ce
// qu'il ne révèle pas.
func TestSignalDeNaissance(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatal(err)
	}

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true) // admin
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false)
	for _, id := range []int64{colin.ID, achille.ID} {
		if err := database.EnsurePermission(id, "shared/skills", perms.Invisible); err != nil {
			t.Fatal(err)
		}
	}
	tok := map[string]string{}
	tok["colin"], _ = database.IssueToken(colin.ID, "t")
	tok["achille"], _ = database.IssueToken(achille.ID, "t")

	fixed := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)

	// Achille dépose un skill. Il naît privé : colin ne peut pas le lire.
	const fichier = "shared/skills/revue-de-code/SKILL.md"
	if code, _ := put(t, srv, tok["achille"], fichier, "---\nname: Revue\ndescription: SECRET INDUSTRIEL\n---\ncontenu confidentiel\n", ""); code != 200 {
		t.Fatalf("dépôt par achille : %d", code)
	}

	// Prérequis : colin ne le voit VRAIMENT pas dans le périmètre. Sans ça le
	// reste du test ne prouverait rien.
	_, arbre := get(t, srv, tok["colin"], "/tree")
	if toSet(arbre["paths"])[fichier] {
		t.Fatal("prérequis : le skill devrait être privé et invisible de colin")
	}

	// ...et pourtant colin apprend qu'il existe. C'est LA décision de F4, et elle
	// contredit volontairement la règle « on ne révèle pas l'existence d'un skill
	// qu'on ne gère pas ». Si ce test casse un jour, c'est peut-être qu'on a
	// « corrigé » l'incohérence : relire la spec avant de le réparer.
	_, corps := get(t, srv, tok["colin"], "/skills/nouveaux")
	nouveaux, ok := corps["nouveaux"].([]any)
	if !ok || len(nouveaux) != 1 {
		t.Fatalf("colin devrait apprendre l'existence du skill d'achille : %#v", corps["nouveaux"])
	}
	n := nouveaux[0].(map[string]any)
	if n["slug"] != "revue-de-code" {
		t.Errorf("slug = %v", n["slug"])
	}
	if n["auteur"] != "achille" {
		t.Errorf("auteur = %v", n["auteur"])
	}
	if n["cree_le"] == "" || n["cree_le"] == nil {
		t.Errorf("aucune date de revendication : %#v", n)
	}
	// Et le signal s'arrête là. Le prix acté est le slug, pas le contenu.
	for _, interdit := range []string{"description", "contenu", "name", "niveau"} {
		if _, present := n[interdit]; present {
			t.Errorf("le signal divulgue %q, il ne doit porter que slug + auteur + date : %#v", interdit, n)
		}
	}

	// On n'a pas besoin d'un signal pour son propre dépôt.
	_, sien := get(t, srv, tok["achille"], "/skills/nouveaux")
	if l, _ := sien["nouveaux"].([]any); len(l) != 0 {
		t.Errorf("achille voit son propre skill dans le signal : %#v", sien["nouveaux"])
	}

	// Le partage reste un geste volontaire de l'auteur : le signal ne l'a pas
	// accordé tout seul.
	_, arbre2 := get(t, srv, tok["colin"], "/tree")
	if toSet(arbre2["paths"])[fichier] {
		t.Fatal("le signal a donné accès au contenu : il ne doit rendre visible qu'une existence")
	}
}
