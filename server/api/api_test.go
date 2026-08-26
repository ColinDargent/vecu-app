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

// setup : serveur de test avec deux utilisateurs aux périmètres différents.
//   - colin : admin, écriture partout.
//   - achille : défaut invisible, lecture sur "public", lecture sur "clients"
//     mais "clients/vdf" re-masqué (surcharge restrictive).
func setup(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
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

	// Contenu.
	store.Write("public/guide.md", "# Guide", "colin")
	store.Write("clients/note.md", "note", "colin")
	store.Write("clients/vdf/secret.md", "secret", "colin")
	store.Write("prive/interne.md", "interne", "colin")

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false)
	database.SetPermission(achille.ID, "public", perms.Lecture) // lecture seule
	database.SetPermission(achille.ID, "clients", perms.Lecture)
	database.SetPermission(achille.ID, "clients/vdf", perms.Invisible)
	database.SetPermission(achille.ID, "equipe", perms.Ecriture) // écriture partagée

	tokens := map[string]string{}
	tokens["colin"], _ = database.IssueToken(colin.ID, "test")
	tokens["achille"], _ = database.IssueToken(achille.ID, "test")

	fixed := time.Date(2026, 7, 23, 17, 30, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)
	return srv, tokens
}

// put envoie un PUT /files/{path} et renvoie code + corps décodé.
func put(t *testing.T, srv *httptest.Server, token, path, content, baseOID string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"content": content, "base_oid": baseOID})
	req, _ := http.NewRequest("PUT", srv.URL+"/files/"+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s : %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// headCursor lit le head courant via /sync (curseur opaque).
func headCursor(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	_, body := get(t, srv, token, "/sync")
	h, _ := body["head"].(string)
	return h
}

func get(t *testing.T, srv *httptest.Server, token, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requête %s : %v", path, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestAuthRequired(t *testing.T) {
	srv, tokens := setup(t)
	if code, _ := get(t, srv, "", "/tree"); code != http.StatusUnauthorized {
		t.Errorf("sans jeton : attendu 401, obtenu %d", code)
	}
	if code, _ := get(t, srv, "jeton-bidon", "/tree"); code != http.StatusUnauthorized {
		t.Errorf("jeton invalide : attendu 401, obtenu %d", code)
	}
	if code, _ := get(t, srv, "", "/espaces"); code != http.StatusUnauthorized {
		t.Errorf("/espaces sans jeton : attendu 401, obtenu %d", code)
	}
	if code, _ := get(t, srv, tokens["colin"], "/health"); code != http.StatusOK {
		t.Errorf("/health devrait être public : %d", code)
	}
}

func TestTreeFilteredByPerimeter(t *testing.T) {
	srv, tokens := setup(t)

	_, colinBody := get(t, srv, tokens["colin"], "/tree")
	colinPaths := toStrings(colinBody["paths"])
	if len(colinPaths) != 4 {
		t.Errorf("colin (admin) devrait voir 4 fichiers, obtenu %v", colinPaths)
	}

	_, achilleBody := get(t, srv, tokens["achille"], "/tree")
	achillePaths := toSet(achilleBody["paths"])
	// Voit public + clients/note, PAS clients/vdf/secret ni prive.
	if !achillePaths["public/guide.md"] || !achillePaths["clients/note.md"] {
		t.Errorf("achille devrait voir public et clients/note : %v", achillePaths)
	}
	if achillePaths["clients/vdf/secret.md"] {
		t.Error("achille voit clients/vdf/secret.md (surcharge restrictive cassée)")
	}
	if achillePaths["prive/interne.md"] {
		t.Error("achille voit prive/interne.md (défaut invisible cassé)")
	}
}

func TestGetFileRespectsPerimeter(t *testing.T) {
	srv, tokens := setup(t)

	code, body := get(t, srv, tokens["achille"], "/files/public/guide.md")
	if code != http.StatusOK || body["content"] != "# Guide" {
		t.Errorf("achille devrait lire public/guide.md : %d %v", code, body)
	}
	// Hors périmètre : 404 (indistinguable d'un fichier absent).
	if code, _ := get(t, srv, tokens["achille"], "/files/clients/vdf/secret.md"); code != http.StatusNotFound {
		t.Errorf("achille ne devrait pas lire clients/vdf/secret.md : %d", code)
	}
	if code, _ := get(t, srv, tokens["achille"], "/files/prive/interne.md"); code != http.StatusNotFound {
		t.Errorf("achille ne devrait pas lire prive/interne.md : %d", code)
	}
}

func TestSyncFullThenIncremental(t *testing.T) {
	srv, tokens := setup(t)

	// Sync initial (sans since) : achille reçoit son périmètre complet.
	_, body := get(t, srv, tokens["achille"], "/sync")
	head1, _ := body["head"].(string)
	changes := toChangeSet(body["changes"])
	if _, ok := changes["clients/vdf/secret.md"]; ok {
		t.Error("sync initial expose un fichier hors périmètre")
	}
	if _, ok := changes["public/guide.md"]; !ok {
		t.Error("sync initial n'inclut pas public/guide.md")
	}

	// Delta depuis head1 : rien de neuf.
	_, body = get(t, srv, tokens["achille"], "/sync?since="+head1)
	if got := toChangeSet(body["changes"]); len(got) != 0 {
		t.Errorf("delta sans changement devrait être vide, obtenu %v", got)
	}
}

func TestPutRequiresWritePermission(t *testing.T) {
	srv, tokens := setup(t)
	head := headCursor(t, srv, tokens["achille"])
	// achille a public en lecture seule → 403.
	if code, _ := put(t, srv, tokens["achille"], "public/tentative.md", "x", head); code != http.StatusForbidden {
		t.Errorf("écriture en lecture seule : attendu 403, obtenu %d", code)
	}
	// Hors périmètre → 404 (existence non révélée).
	if code, _ := put(t, srv, tokens["achille"], "prive/x.md", "x", head); code != http.StatusNotFound {
		t.Errorf("écriture hors périmètre : attendu 404, obtenu %d", code)
	}
	// Dans son périmètre d'écriture → 200.
	if code, body := put(t, srv, tokens["achille"], "equipe/note.md", "bonjour", head); code != http.StatusOK {
		t.Errorf("écriture autorisée : attendu 200, obtenu %d (%v)", code, body)
	}
}

func TestPutConflictTwoClients(t *testing.T) {
	srv, tokens := setup(t)
	// État initial partagé.
	base := headCursor(t, srv, tokens["colin"])
	code, _ := put(t, srv, tokens["colin"], "equipe/plan.md", "ligne commune\n", base)
	if code != http.StatusOK {
		t.Fatalf("écriture initiale : %d", code)
	}
	shared := headCursor(t, srv, tokens["colin"]) // les deux clients partent d'ici

	// Client 1 (colin) modifie la ligne et pousse → main avance.
	if code, _ := put(t, srv, tokens["colin"], "equipe/plan.md", "version colin\n", shared); code != http.StatusOK {
		t.Fatalf("push colin : %d", code)
	}
	// Client 2 (achille) part du MÊME base `shared`, modifie la même ligne → conflit.
	code, body := put(t, srv, tokens["achille"], "equipe/plan.md", "version achille\n", shared)
	if code != http.StatusOK {
		t.Fatalf("push achille : %d (%v)", code, body)
	}
	if conflict, _ := body["conflict"].(bool); !conflict {
		t.Fatalf("attendu conflit, obtenu %v", body)
	}
	cp, _ := body["conflict_path"].(string)
	if cp != "equipe/plan (conflit 2026-07-23 17h30 - achille).md" {
		t.Errorf("nom de copie de conflit inattendu : %q", cp)
	}

	// main garde la version du premier écrivain.
	_, b := get(t, srv, tokens["colin"], "/files/equipe/plan.md")
	if b["content"] != "version colin\n" {
		t.Errorf("main devrait garder version colin, obtenu %q", b["content"])
	}
	// La copie de conflit est visible ET porte la version du second.
	code, b = get(t, srv, tokens["achille"], "/files/"+cp)
	if code != http.StatusOK || b["content"] != "version achille\n" {
		t.Errorf("copie de conflit illisible ou incorrecte : %d %q", code, b["content"])
	}
}

func TestDeleteViaAPI(t *testing.T) {
	srv, tokens := setup(t)
	base := headCursor(t, srv, tokens["achille"])
	put(t, srv, tokens["achille"], "equipe/tmp.md", "jetable\n", base)
	head := headCursor(t, srv, tokens["achille"])

	req, _ := http.NewRequest("DELETE", srv.URL+"/files/equipe/tmp.md?base_oid="+head, nil)
	req.Header.Set("Authorization", "Bearer "+tokens["achille"])
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE : %d", resp.StatusCode)
	}
	if code, _ := get(t, srv, tokens["achille"], "/files/equipe/tmp.md"); code != http.StatusNotFound {
		t.Errorf("fichier devrait être supprimé : %d", code)
	}
}

func toStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toSet(v any) map[string]bool {
	m := map[string]bool{}
	for _, s := range toStrings(v) {
		m[s] = true
	}
	return m
}

func toChangeSet(v any) map[string]bool {
	arr, _ := v.([]any)
	m := map[string]bool{}
	for _, x := range arr {
		obj, _ := x.(map[string]any)
		if p, ok := obj["path"].(string); ok {
			m[p] = true
		}
	}
	return m
}

// espacesDe : GET /espaces → nom -> nombre de fichiers visibles.
func espacesDe(t *testing.T, srv *httptest.Server, token string) map[string]float64 {
	t.Helper()
	code, body := get(t, srv, token, "/espaces")
	if code != http.StatusOK {
		t.Fatalf("GET /espaces : attendu 200, obtenu %d", code)
	}
	out := map[string]float64{}
	list, _ := body["espaces"].([]any)
	for _, e := range list {
		m, _ := e.(map[string]any)
		nom, _ := m["nom"].(string)
		n, _ := m["fichiers"].(float64)
		out[nom] = n
	}
	return out
}

func TestEspacesFiltresAuPerimetre(t *testing.T) {
	srv, tokens := setup(t)

	colin := espacesDe(t, srv, tokens["colin"])
	if len(colin) != 3 {
		t.Errorf("colin (admin) : %v, attendu exactement public/clients/prive", colin)
	}
	for nom, n := range map[string]float64{"public": 1, "clients": 2, "prive": 1} {
		if colin[nom] != n {
			t.Errorf("colin, espace %s : %v fichiers, attendu %v (tous : %v)", nom, colin[nom], n, colin)
		}
	}

	achille := espacesDe(t, srv, tokens["achille"])
	// Ensemble EXACT : un espace parasite (nom invalide devenu espace, fuite de
	// périmètre) doit faire échouer ce test, pas seulement les noms attendus.
	if len(achille) != 2 {
		t.Fatalf("achille : %v, attendu exactement public et clients", achille)
	}
	if achille["clients"] != 1 {
		t.Errorf("achille, espace clients : %v fichiers, attendu 1 (clients/vdf masqué)", achille["clients"])
	}
	if achille["public"] != 1 {
		t.Errorf("achille, espace public : %v fichiers, attendu 1", achille["public"])
	}
	// « equipe » a une règle d'écriture mais aucun fichier : un droit posé sur un
	// espace qui n'existe pas ne fabrique pas l'espace.
	if _, vu := achille["equipe"]; vu {
		t.Errorf("achille voit un espace « equipe » inexistant : %v", achille)
	}
}

// TestEcritureHorsEspaceRefusee : un espace naît du premier fichier écrit
// dedans. Si l'écriture ne contrôle pas la forme du chemin, le fichier est
// accepté, stocké, relu... et synchronisé nulle part, sans la moindre erreur.
// C'est le mode de défaillance silencieux que ce test verrouille.
func TestEcritureHorsEspaceRefusee(t *testing.T) {
	srv, tokens := setup(t)
	head := headCursor(t, srv, tokens["colin"])

	refus := []string{
		"note.md",                   // à la racine du dépôt : aucun espace
		"idées/note.md",             // premier segment accentué (NFC/NFD sur macOS)
		".obsidian/appearance.json", // dossier technique d'un vault
		"Notes.app/note.md",         // bundle macOS
	}
	for _, p := range refus {
		if code, body := put(t, srv, tokens["colin"], p, "x", head); code != http.StatusBadRequest {
			t.Errorf("PUT %s : attendu 400, obtenu %d (%v)", p, code, body)
		}
	}
	// Rien n'a été écrit : le dépôt est intact.
	_, body := get(t, srv, tokens["colin"], "/tree")
	if n := len(toStrings(body["paths"])); n != 4 {
		t.Errorf("dépôt modifié par un refus : %d fichiers, attendu 4", n)
	}
	// À l'intérieur d'un espace, les accents restent pleinement supportés.
	if code, _ := put(t, srv, tokens["colin"], "public/idées.md", "des idées", head); code != http.StatusOK {
		t.Errorf("PUT public/idées.md : attendu 200, obtenu %d", code)
	}
}

// TestCollisionDeCasseRefusee : « clients » et « Clients » seraient deux
// espaces côté serveur et un seul dossier sur APFS. La sync verrait alors les
// fichiers de l'un comme supprimés et ceux de l'autre comme nouveaux, et
// déplacerait du contenu d'un périmètre de droits vers l'autre.
func TestCollisionDeCasseRefusee(t *testing.T) {
	srv, tokens := setup(t)
	head := headCursor(t, srv, tokens["colin"])

	if code, body := put(t, srv, tokens["colin"], "Clients/note.md", "x", head); code != http.StatusBadRequest {
		t.Errorf("PUT Clients/note.md avec « clients » existant : attendu 400, obtenu %d (%v)", code, body)
	}
	// Un espace réellement nouveau passe.
	if code, _ := put(t, srv, tokens["colin"], "archives/note.md", "x", head); code != http.StatusOK {
		t.Errorf("PUT dans un espace neuf : attendu 200, obtenu %d", code)
	}
	// Et une écriture supplémentaire dans un espace existant passe aussi.
	if code, _ := put(t, srv, tokens["colin"], "clients/autre.md", "x", headCursor(t, srv, tokens["colin"])); code != http.StatusOK {
		t.Errorf("PUT dans un espace existant : attendu 200, obtenu %d", code)
	}
}
