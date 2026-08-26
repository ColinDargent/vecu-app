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

// TestAppropriationSkill couvre la slice 1 : écrire dans un slug de skill libre
// est une création autorisée pour tout compte (même défaut invisible), qui en
// fait le propriétaire ; écrire dans le skill revendiqué d'un autre suit les
// droits normaux (refusé sans partage).
func TestAppropriationSkill(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("db.Open : %v", err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false)
	bob, _ := database.CreateUser("bob", "pw", perms.Invisible, false)

	tok := map[string]string{}
	tok["colin"], _ = database.IssueToken(colin.ID, "t")
	tok["achille"], _ = database.IssueToken(achille.ID, "t")
	tok["bob"], _ = database.IssueToken(bob.ID, "t")

	fixed := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)

	const skillMD = "---\nname: A\ndescription: fait X\n---\n"

	// 1. achille (défaut invisible) crée un skill : la création est autorisée.
	if code, _ := put(t, srv, tok["achille"], "shared/skills/outil-a/SKILL.md", skillMD, ""); code != 200 {
		t.Fatalf("création skill par achille : code %d (attendu 200)", code)
	}
	// Créateur enregistré + grant écriture posé.
	if id, ok, _ := database.CreatorOf("outil-a"); !ok || id != achille.ID {
		t.Fatalf("créateur outil-a = (%d,%v), attendu achille=%d", id, ok, achille.ID)
	}
	if lvl, _ := database.Effective(achille, "shared/skills/outil-a"); lvl != perms.Ecriture {
		t.Fatalf("achille devrait avoir écriture sur son skill, a %v", lvl)
	}

	// 2. Le créateur ré-écrit dans son skill (déjà revendiqué, il le possède) : OK.
	if code, _ := put(t, srv, tok["achille"], "shared/skills/outil-a/ref.md", "détail", ""); code != 200 {
		t.Fatalf("maj par le créateur : code %d (attendu 200)", code)
	}

	// 3. bob (non-admin invisible, non partagé) écrit dans le skill d'achille : refusé.
	if code, _ := put(t, srv, tok["bob"], "shared/skills/outil-a/hack.md", "x", ""); code == 200 {
		t.Fatalf("bob ne devrait pas pouvoir écrire dans le skill d'achille (code %d)", code)
	}

	// 4. bob crée le sien : OK, il en est le créateur.
	if code, _ := put(t, srv, tok["bob"], "shared/skills/outil-b/SKILL.md", skillMD, ""); code != 200 {
		t.Fatalf("création skill par bob : code %d (attendu 200)", code)
	}
	if id, ok, _ := database.CreatorOf("outil-b"); !ok || id != bob.ID {
		t.Fatalf("créateur outil-b = (%d,%v), attendu bob=%d", id, ok, bob.ID)
	}
}

// TestSkillExistantNonHijackable : un skill dont les fichiers existent déjà dans
// le dépôt mais SANS ligne créateur (cas des skills pré-existants, avant le
// backfill de la slice 2) n'est pas une « création » : un compte tiers ne peut
// pas s'en emparer par une simple écriture. C'est la garde d'existence git.
func TestSkillExistantNonHijackable(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("db.Open : %v", err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}

	// Skill pré-existant posé directement dans le dépôt (comme un import), sans
	// aucune revendication en base.
	store.Write("shared/skills/legacy/SKILL.md", "---\nname: L\ndescription: d\n---\n", "colin")

	bob, _ := database.CreateUser("bob", "pw", perms.Invisible, false)
	tokBob, _ := database.IssueToken(bob.ID, "t")

	fixed := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)

	// bob tente d'écrire dans le skill existant : refusé (pas une création, et il
	// n'a aucun droit dessus).
	if code, _ := put(t, srv, tokBob, "shared/skills/legacy/hack.md", "x", ""); code == 200 {
		t.Fatalf("un skill existant ne doit pas être revendicable par un tiers (code %d)", code)
	}
	// Personne n'a été enregistré comme créateur par cette tentative.
	if _, ok, _ := database.CreatorOf("legacy"); ok {
		t.Fatal("la tentative d'écriture ne doit pas revendiquer un skill existant")
	}
}
