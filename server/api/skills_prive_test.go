package api

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// TestSkillPriveMemeDesAdmins : un skill créé par un compte est invisible pour
// les autres - y compris un admin - dans /tree (donc dans la sync et sur le
// disque), tant qu'il n'est pas partagé. C'est le cœur de F3 : l'admin n'est pas
// exempté sur les skills. Après partage par le créateur, le destinataire le voit.
func TestSkillPriveMemeDesAdmins(t *testing.T) {
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

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true) // admin
	bob, _ := database.CreateUser("bob", "pw", perms.Invisible, false)
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false)
	// Privé par défaut pour tous (ce que posent CreateUser web + backfill).
	for _, id := range []int64{colin.ID, bob.ID, achille.ID} {
		if err := database.EnsurePermission(id, "shared/skills", perms.Invisible); err != nil {
			t.Fatal(err)
		}
	}

	tok := map[string]string{}
	tok["colin"], _ = database.IssueToken(colin.ID, "t")
	tok["bob"], _ = database.IssueToken(bob.ID, "t")
	tok["achille"], _ = database.IssueToken(achille.ID, "t")

	fixed := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)

	const skillFile = "shared/skills/secret-bob/SKILL.md"
	if code, _ := put(t, srv, tok["bob"], skillFile, "---\nname: S\ndescription: d\n---\n", ""); code != 200 {
		t.Fatalf("création skill par bob : %d", code)
	}

	// bob (créateur) le voit.
	_, bobBody := get(t, srv, tok["bob"], "/tree")
	if !toSet(bobBody["paths"])[skillFile] {
		t.Fatal("bob (créateur) devrait voir son skill")
	}
	// L'admin ne le voit PAS.
	_, colinBody := get(t, srv, tok["colin"], "/tree")
	if toSet(colinBody["paths"])[skillFile] {
		t.Fatal("l'admin ne doit PAS voir le skill d'un autre (privé même des admins)")
	}
	// Un autre non-admin non plus.
	_, achilleBody := get(t, srv, tok["achille"], "/tree")
	if toSet(achilleBody["paths"])[skillFile] {
		t.Fatal("un tiers ne doit pas voir le skill de bob")
	}

	// Après partage par le créateur, achille le voit.
	if err := database.SetPermission(achille.ID, "shared/skills/secret-bob", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	_, achilleBody2 := get(t, srv, tok["achille"], "/tree")
	if !toSet(achilleBody2["paths"])[skillFile] {
		t.Fatal("après partage par le créateur, achille devrait voir le skill")
	}
}

// TestExportZIPExclutSkillsDautrui (slice 3) : un admin qui exporte tout le dépôt
// n'obtient PAS le skill privé d'un autre - l'exemption admin ne couvre pas la
// zone skills - mais garde ses fichiers non-skills (exemption conservée ailleurs).
func TestExportZIPExclutSkillsDautrui(t *testing.T) {
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

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true) // admin
	bob, _ := database.CreateUser("bob", "pw", perms.Invisible, false)
	store.Write("public/note.md", "visible", "colin") // hors skills, visible de l'admin

	tokColin, _ := database.IssueToken(colin.ID, "t")
	tokBob, _ := database.IssueToken(bob.ID, "t")

	fixed := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)

	if code, _ := put(t, srv, tokBob, "shared/skills/secret-bob/SKILL.md", "---\nname: S\ndescription: d\n---\n", ""); code != 200 {
		t.Fatalf("création skill par bob : %d", code)
	}

	resp, body := apiGet(t, srv, tokColin, "/export")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export : %d", resp.StatusCode)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("ZIP invalide : %v", err)
	}
	noms := map[string]bool{}
	for _, f := range zr.File {
		noms[f.Name] = true
	}
	if noms["shared/skills/secret-bob/SKILL.md"] {
		t.Fatal("l'export admin ne doit pas contenir le skill privé d'un autre")
	}
	if !noms["public/note.md"] {
		t.Fatal("l'export admin doit contenir ses fichiers non-skills (exemption admin conservée hors skills)")
	}
}
