package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

func postJSON(t *testing.T, srv *httptest.Server, token, path string, body any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s : %v", path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func skillPresent(body map[string]any, slug string) (map[string]any, bool) {
	arr, _ := body["skills"].([]any)
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok && m["slug"] == slug {
			return m, true
		}
	}
	return nil, false
}

// TestSkillsAPI couvre la slice 4 : GET /skills (visible par l'appelant seul),
// gestion des accès réservée au créateur (404 pour les autres, admin compris),
// partage par le créateur rendant le skill visible au destinataire.
func TestSkillsAPI(t *testing.T) {
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

	// CreateUser pose « shared/skills=invisible » pour tous (privé par défaut).
	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true) // admin
	bob, _ := database.CreateUser("bob", "pw", perms.Invisible, false)
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false)

	tok := map[string]string{}
	tok["colin"], _ = database.IssueToken(colin.ID, "t")
	tok["bob"], _ = database.IssueToken(bob.ID, "t")
	tok["achille"], _ = database.IssueToken(achille.ID, "t")

	fixed := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)

	// bob crée son skill.
	if code, _ := put(t, srv, tok["bob"], "shared/skills/outil-bob/SKILL.md", "---\nname: OutilBob\ndescription: fait X\n---\n", ""); code != 200 {
		t.Fatalf("création skill bob : %d", code)
	}

	// GET /skills : bob voit le sien (créateur, écriture) ; achille et l'admin non.
	_, bobList := get(t, srv, tok["bob"], "/skills")
	if m, ok := skillPresent(bobList, "outil-bob"); !ok {
		t.Fatal("bob devrait voir son skill dans /skills")
	} else if m["creator"] != "bob" || m["niveau"] != "ecriture" {
		t.Fatalf("skill de bob mal renseigné : %v", m)
	}
	_, achilleList := get(t, srv, tok["achille"], "/skills")
	if _, ok := skillPresent(achilleList, "outil-bob"); ok {
		t.Fatal("achille ne doit pas voir le skill de bob (non partagé)")
	}
	_, colinList := get(t, srv, tok["colin"], "/skills")
	if _, ok := skillPresent(colinList, "outil-bob"); ok {
		t.Fatal("l'admin ne doit pas voir le skill d'un autre")
	}

	// Gestion des accès : réservée au créateur. Les autres (admin compris) → 404.
	if code, _ := get(t, srv, tok["achille"], "/skills/outil-bob/acces"); code != http.StatusNotFound {
		t.Fatalf("GET acces par un tiers : attendu 404, obtenu %d", code)
	}
	if code, _ := get(t, srv, tok["colin"], "/skills/outil-bob/acces"); code != http.StatusNotFound {
		t.Fatalf("GET acces par l'admin non-créateur : attendu 404, obtenu %d", code)
	}
	if code, _ := get(t, srv, tok["bob"], "/skills/outil-bob/acces"); code != http.StatusOK {
		t.Fatalf("GET acces par le créateur : attendu 200, obtenu %d", code)
	}

	// POST acces : un tiers et l'admin non-créateur ne peuvent pas partager.
	if code := postJSON(t, srv, tok["achille"], "/skills/outil-bob/acces", map[string]any{"user_id": achille.ID, "niveau": "lecture"}); code != http.StatusNotFound {
		t.Fatalf("POST acces par un tiers : attendu 404, obtenu %d", code)
	}
	if code := postJSON(t, srv, tok["colin"], "/skills/outil-bob/acces", map[string]any{"user_id": colin.ID, "niveau": "ecriture"}); code != http.StatusNotFound {
		t.Fatalf("POST acces par l'admin non-créateur : attendu 404, obtenu %d", code)
	}
	// Niveau invalide → 400.
	if code := postJSON(t, srv, tok["bob"], "/skills/outil-bob/acces", map[string]any{"user_id": achille.ID, "niveau": "root"}); code != http.StatusBadRequest {
		t.Fatalf("niveau invalide : attendu 400, obtenu %d", code)
	}

	// Le créateur partage avec achille : 200, puis achille voit le skill.
	if code := postJSON(t, srv, tok["bob"], "/skills/outil-bob/acces", map[string]any{"user_id": achille.ID, "niveau": "lecture"}); code != http.StatusOK {
		t.Fatalf("partage par le créateur : attendu 200, obtenu %d", code)
	}
	_, achilleList2 := get(t, srv, tok["achille"], "/skills")
	if _, ok := skillPresent(achilleList2, "outil-bob"); !ok {
		t.Fatal("après partage, achille devrait voir le skill de bob")
	}
}
