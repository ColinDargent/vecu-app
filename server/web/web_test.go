package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// newServer : serveur web sur base mémoire + dépôt git temporaire.
func newServer(t *testing.T) *Server {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open : %v", err)
	}
	t.Cleanup(func() { d.Close() })
	store, err := storage.Init(t.TempDir() + "/brain.git")
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}
	return &Server{DB: d, Store: store}
}

// login : joue le POST /admin/login et renvoie le cookie de session.
func login(t *testing.T, h http.Handler, username, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			// Les propriétés de sécurité revendiquées sont verrouillées ici.
			if !c.HttpOnly {
				t.Error("cookie de session non HttpOnly")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("cookie de session : attendu SameSite=Lax, obtenu %v", c.SameSite)
			}
			if c.Path != "/admin" {
				t.Errorf("cookie de session : attendu Path=/admin, obtenu %q", c.Path)
			}
			return c
		}
	}
	t.Fatal("cookie de session absent après login")
	return nil
}

func get(h http.Handler, path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAccueilSansSessionRedirige(t *testing.T) {
	s := newServer(t)
	rec := get(s.Handler(), "/admin/", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Errorf("attendu 303 → /admin/login, obtenu %d → %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLoginRefuseMauvaisIdentifiants(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "bon-mdp", perms.Ecriture, true)
	form := url.Values{"username": {"colin"}, "password": {"mauvais"}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("attendu 401, obtenu %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "identifiants invalides") {
		t.Error("message d'erreur absent de la page")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("aucun cookie ne doit être posé sur un échec")
	}
}

func TestLoginPuisAccueilPuisLogout(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	h := s.Handler()

	c := login(t, h, "colin", "mdp")

	rec := get(h, "/admin/", c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "colin") {
		t.Errorf("accueil connecté : attendu 200 avec le nom, obtenu %d", rec.Code)
	}

	// Logout : le jeton est révoqué côté serveur, pas seulement le cookie.
	req := httptest.NewRequest("POST", "/admin/logout", nil)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout : attendu 303, obtenu %d", rec.Code)
	}
	rec = get(h, "/admin/", c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("session révoquée : attendu redirection, obtenu %d", rec.Code)
	}
}
