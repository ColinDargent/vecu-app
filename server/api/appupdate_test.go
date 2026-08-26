package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// setupApp : serveur de test avec un dossier de release peuplé (manifeste + un
// binaire arm64), un utilisateur, et son jeton.
func setupApp(t *testing.T, appdist string) (*httptest.Server, string) {
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
	u, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	token, _ := database.IssueToken(u.ID, "test")

	srv := httptest.NewServer((&Server{DB: database, Store: store, AppDist: appdist}).Handler())
	t.Cleanup(srv.Close)
	return srv, token
}

func getApp(t *testing.T, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s : %v", url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func TestAppEndpoints_Nominal(t *testing.T) {
	appdist := t.TempDir()
	if err := os.WriteFile(filepath.Join(appdist, "manifest.json"), []byte(`{"version":"v0.3.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	binaire := []byte("\x7fELF-ish faux binaire arm64")
	if err := os.WriteFile(filepath.Join(appdist, "vecu-app-darwin-arm64"), binaire, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, token := setupApp(t, appdist)

	resp, body := getApp(t, srv.URL+"/app/manifest", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest : code %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("manifest content-type = %q", ct)
	}
	if string(body) != `{"version":"v0.3.0"}` {
		t.Fatalf("manifest corps = %q", body)
	}

	resp, body = getApp(t, srv.URL+"/app/download?target=darwin-arm64", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download : code %d", resp.StatusCode)
	}
	if string(body) != string(binaire) {
		t.Fatalf("download rend un corps inattendu (%d octets)", len(body))
	}
}

func TestAppEndpoints_AuthRequise(t *testing.T) {
	appdist := t.TempDir()
	os.WriteFile(filepath.Join(appdist, "manifest.json"), []byte(`{}`), 0o644)
	srv, _ := setupApp(t, appdist)

	if resp, _ := getApp(t, srv.URL+"/app/manifest", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sans jeton, manifest doit être 401, or %d", resp.StatusCode)
	}
	if resp, _ := getApp(t, srv.URL+"/app/download?target=darwin-arm64", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sans jeton, download doit être 401, or %d", resp.StatusCode)
	}
}

func TestAppDownload_CibleInvalide(t *testing.T) {
	appdist := t.TempDir()
	srv, token := setupApp(t, appdist)

	// Cible hors allowlist.
	if resp, _ := getApp(t, srv.URL+"/app/download?target=linux-amd64", token); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cible inconnue doit être 400, or %d", resp.StatusCode)
	}
	// Tentative de traversée de chemin : rejetée par l'allowlist, jamais jointe.
	if resp, _ := getApp(t, srv.URL+"/app/download?target=../../etc/passwd", token); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversée de chemin doit être 400, or %d", resp.StatusCode)
	}
}

func TestAppEndpoints_SansRelease(t *testing.T) {
	// AppDist vide : aucune release publiée → 404 propre.
	srv, token := setupApp(t, "")
	if resp, _ := getApp(t, srv.URL+"/app/manifest", token); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("sans AppDist, manifest doit être 404, or %d", resp.StatusCode)
	}

	// AppDist configuré mais binaire absent → 404 (allowlist OK, fichier manquant).
	srv2, token2 := setupApp(t, t.TempDir())
	if resp, _ := getApp(t, srv2.URL+"/app/download?target=darwin-amd64", token2); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("binaire absent doit être 404, or %d", resp.StatusCode)
	}
}

// Le paramètre `format` : les deux valeurs connues servent deux fichiers
// différents, l'absence vaut « bin », et l'inconnu est refusé AVANT toute
// construction de chemin.
func TestAppDownload_Format(t *testing.T) {
	appdist := t.TempDir()
	binaire := []byte("binaire nu arm64")
	bundle := []byte("archive tar.gz du bundle arm64")
	if err := os.WriteFile(filepath.Join(appdist, "vecu-app-darwin-arm64"), binaire, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appdist, "vecu-app-darwin-arm64.tgz"), bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, token := setupApp(t, appdist)

	cas := []struct {
		nom     string
		url     string
		statut  int
		attendu []byte
	}{
		{"format absent = le binaire, comme avant v0.9.0",
			"/app/download?target=darwin-arm64", 200, binaire},
		{"format=bin explicite",
			"/app/download?target=darwin-arm64&format=bin", 200, binaire},
		{"format=bundle sert l'archive",
			"/app/download?target=darwin-arm64&format=bundle", 200, bundle},
		{"format inconnu refusé",
			"/app/download?target=darwin-arm64&format=zip", 400, nil},
		{"format ne peut pas servir de chemin",
			"/app/download?target=darwin-arm64&format=../../etc/passwd", 400, nil},
		{"cible inconnue refusée même avec un format valide",
			"/app/download?target=linux-amd64&format=bundle", 400, nil},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			resp, body := getApp(t, srv.URL+c.url, token)
			if resp.StatusCode != c.statut {
				t.Fatalf("statut %d, attendu %d (corps : %s)", resp.StatusCode, c.statut, body)
			}
			if c.attendu != nil && string(body) != string(c.attendu) {
				t.Fatalf("corps servi %q, attendu %q", body, c.attendu)
			}
		})
	}
}

// Une release antérieure à v0.9.0 ne publie aucun bundle. Un client récent qui
// en demande un doit recevoir un 404 net, pas une erreur trouble : c'est ce
// 404 qui déclenchera son repli vers le binaire.
func TestAppDownload_BundleAbsentDUneVieilleRelease(t *testing.T) {
	appdist := t.TempDir()
	if err := os.WriteFile(filepath.Join(appdist, "vecu-app-darwin-arm64"), []byte("binaire"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, token := setupApp(t, appdist)

	resp, _ := getApp(t, srv.URL+"/app/download?target=darwin-arm64&format=bundle", token)
	if resp.StatusCode != 404 {
		t.Fatalf("statut %d, attendu 404 sur un bundle non publié", resp.StatusCode)
	}
	resp, body := getApp(t, srv.URL+"/app/download?target=darwin-arm64", token)
	if resp.StatusCode != 200 || string(body) != "binaire" {
		t.Fatalf("le chemin binaire doit rester servi : statut %d, corps %q", resp.StatusCode, body)
	}
}
