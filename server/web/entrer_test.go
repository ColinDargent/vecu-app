package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/tickets"
)

// TestEntrerOuvreUneSessionPuisNeVautPlusRien : le parcours complet de la porte
// que l'app pousse. Un billet vaut une session, une seule fois.
func TestEntrerOuvreUneSessionPuisNeVautPlusRien(t *testing.T) {
	s := newServer(t)
	s.Tickets = tickets.New()
	u, err := s.DB.CreateUser("colin", "pw", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	jeton, err := s.Tickets.Emet(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/entrer?jeton="+jeton, nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("attendu 303, obtenu %d", rec.Code)
	}
	// La redirection n'emporte pas la requête : ce qui reste dans la barre
	// d'adresse et dans l'historique ne contient rien de secret.
	if dest := rec.Header().Get("Location"); dest != "/admin/" {
		t.Errorf("redirection vers %q, attendu /admin/", dest)
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("aucune session posée")
	}
	if !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Errorf("la session posée n'a pas les propriétés des autres : %+v", session)
	}
	if session.Value == jeton {
		t.Error("le billet lui-même a été posé comme session : il serait devenu permanent")
	}

	// La session marche vraiment.
	req := httptest.NewRequest("GET", "/admin/", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("la session ouverte par billet ne donne pas accès à l'admin : %d", rec.Code)
	}

	// Rejouer l'URL ne donne plus rien, et ne dit pas pourquoi.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/entrer?jeton="+jeton, nil))
	if dest := rec.Header().Get("Location"); dest != "/admin/login" {
		t.Errorf("un billet rejoué a rendu autre chose que le formulaire : %d %q", rec.Code, dest)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Error("un billet rejoué a posé une session")
		}
	}
}

// TestEntrerSansBilletValide : un jeton inventé, vide, ou un serveur sans
// magasin de billets renvoient tous au formulaire. Jamais une erreur qui
// renseigne, jamais un panic.
func TestEntrerSansBilletValide(t *testing.T) {
	s := newServer(t)
	s.Tickets = tickets.New()
	h := s.Handler()
	for _, url := range []string{"/admin/entrer", "/admin/entrer?jeton=", "/admin/entrer?jeton=invente"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if dest := rec.Header().Get("Location"); dest != "/admin/login" {
			t.Errorf("%s : redirection vers %q, attendu /admin/login", url, dest)
		}
	}
	// Serveur sans magasin câblé : la porte est fermée, pas cassée.
	sans := newServer(t)
	rec := httptest.NewRecorder()
	sans.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/admin/entrer?jeton=x", nil))
	if dest := rec.Header().Get("Location"); dest != "/admin/login" {
		t.Errorf("sans magasin : redirection vers %q", dest)
	}
}
