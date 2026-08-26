package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
	"github.com/colindargent/vecu/server/tickets"
)

func setupSessionWeb(t *testing.T, billets *tickets.Store) (*httptest.Server, string) {
	t.Helper()
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
	u, err := database.CreateUser("colin", "pw", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	jeton, _ := database.IssueToken(u.ID, "test")
	srv := httptest.NewServer((&Server{DB: database, Store: store, Tickets: billets}).Handler())
	t.Cleanup(srv.Close)
	return srv, jeton
}

func postSessionWeb(t *testing.T, srv *httptest.Server, bearer string) (int, map[string]string) {
	t.Helper()
	req, err := http.NewRequest("POST", srv.URL+"/session-web", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]string{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestSessionWebRendUnCheminUtilisableUneFois : ce que l'app reçoit quand on
// clique « Ouvrir l'admin web ».
func TestSessionWebRendUnCheminUtilisableUneFois(t *testing.T) {
	billets := tickets.New()
	srv, bearer := setupSessionWeb(t, billets)

	code, out := postSessionWeb(t, srv, bearer)
	if code != http.StatusOK {
		t.Fatalf("attendu 200, obtenu %d (%v)", code, out)
	}
	chemin := out["chemin"]
	if !strings.HasPrefix(chemin, "/admin/entrer?jeton=") {
		t.Fatalf("chemin inattendu : %q", chemin)
	}
	// UN CHEMIN, PAS UNE URL ABSOLUE : le serveur ne connaît son adresse
	// publique que par l'en-tête Host, que le client contrôle.
	if strings.Contains(chemin, "http") {
		t.Errorf("le serveur a construit une URL absolue depuis l'en-tête Host : %q", chemin)
	}

	u, err := url.Parse(chemin)
	if err != nil {
		t.Fatal(err)
	}
	jeton := u.Query().Get("jeton")
	if jeton == "" {
		t.Fatal("aucun billet dans le chemin")
	}
	if jeton == bearer {
		t.Fatal("le jeton d'appareil lui-même a été mis dans une URL")
	}
	if _, ok := billets.Consomme(jeton); !ok {
		t.Error("le billet rendu n'est pas valide")
	}
	if _, ok := billets.Consomme(jeton); ok {
		t.Error("le billet a servi deux fois")
	}

	// Deux appels rendent deux billets différents.
	_, a := postSessionWeb(t, srv, bearer)
	_, b := postSessionWeb(t, srv, bearer)
	if a["chemin"] == b["chemin"] {
		t.Error("deux demandes ont rendu le même billet")
	}
}

// TestSessionWebExigeUnJeton : la route est authentifiée comme les autres. Sans
// ça, n'importe qui ouvrirait une session sur le compte de quelqu'un d'autre.
func TestSessionWebExigeUnJeton(t *testing.T) {
	srv, _ := setupSessionWeb(t, tickets.New())
	if code, _ := postSessionWeb(t, srv, ""); code != http.StatusUnauthorized {
		t.Errorf("sans jeton : attendu 401, obtenu %d", code)
	}
	if code, _ := postSessionWeb(t, srv, "pas-un-jeton"); code != http.StatusUnauthorized {
		t.Errorf("jeton inventé : attendu 401, obtenu %d", code)
	}
}

// TestSessionWebSansMagasin : un déploiement qui n'a pas câblé les billets
// répond « pas disponible », il ne tombe pas sur un pointeur nul.
func TestSessionWebSansMagasin(t *testing.T) {
	srv, bearer := setupSessionWeb(t, nil)
	if code, _ := postSessionWeb(t, srv, bearer); code != http.StatusNotImplemented {
		t.Errorf("sans magasin : attendu 501, obtenu %d", code)
	}
}
