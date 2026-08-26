package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func postForm(h http.Handler, path string, form url.Values, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUsersRefuseNonAdmin(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("membre", "mdp", perms.Lecture, false)
	h := s.Handler()
	c := login(t, h, "membre", "mdp")

	// Les 6 routes users/droits, GET comme POST, refusent un non-admin.
	routes := []struct{ methode, chemin string }{
		{"GET", "/admin/users"},
		{"POST", "/admin/users"},
		{"GET", "/admin/users/1"},
		{"POST", "/admin/users/1/defaut"},
		{"POST", "/admin/users/1/droits"},
		{"POST", "/admin/users/1/droits/supprimer"},
	}
	for _, rt := range routes {
		var rec *httptest.ResponseRecorder
		if rt.methode == "GET" {
			rec = get(h, rt.chemin, c)
		} else {
			rec = postForm(h, rt.chemin, url.Values{"username": {"pirate"}, "password": {"x"}, "niveau": {"ecriture"}, "chemin": {"x"}}, c)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s non-admin : attendu 403, obtenu %d", rt.methode, rt.chemin, rec.Code)
		}
	}
}

func TestCreationUtilisateur(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := postForm(h, "/admin/users", url.Values{
		"username": {"achille"}, "password": {"secret"}, "niveau": {"lecture"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("création : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	// Le compte existe et peut s'authentifier.
	u, err := s.DB.Authenticate("achille", "secret")
	if err != nil || u.DefaultLevel != perms.Lecture || u.IsAdmin {
		t.Errorf("compte créé incorrect : %+v (err %v)", u, err)
	}
	// La liste l'affiche.
	if rec := get(h, "/admin/users", c); !strings.Contains(rec.Body.String(), "achille") {
		t.Error("achille absent de la liste des utilisateurs")
	}
	// Doublon refusé, avec message.
	rec = postForm(h, "/admin/users", url.Values{
		"username": {"achille"}, "password": {"x"}, "niveau": {"lecture"},
	}, c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "déjà pris") {
		t.Errorf("doublon : attendu 400 avec message, obtenu %d", rec.Code)
	}
}

func TestCreationUsernameInvalide(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// Un « / » injecterait le nom dans les chemins de copie de conflit ; un
	// caractère de contrôle casserait l'auteur git : les deux sont refusés.
	for _, mauvais := range []string{"a/b", "a\x01b", "", strings.Repeat("x", 65)} {
		rec := postForm(h, "/admin/users", url.Values{
			"username": {mauvais}, "password": {"mdp"}, "niveau": {"lecture"},
		}, c)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("username %q : attendu 400, obtenu %d", mauvais, rec.Code)
		}
		if _, err := s.DB.Authenticate(mauvais, "mdp"); err == nil {
			t.Errorf("username %q : le compte n'aurait pas dû être créé", mauvais)
		}
	}
}

func TestGestionDroits(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	cible, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.Store.Write("clients/vdf/note.md", "x", "colin")
	s.Store.Write("projets/vecu/spec.md", "x", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	base := "/admin/users/" + itoa(cible.ID)

	// La page affiche l'arbre avec le droit effectif hérité du défaut.
	rec := get(h, base, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("page droits : attendu 200, obtenu %d", rec.Code)
	}
	for _, attendu := range []string{"clients", "vdf", "projets", "lecture"} {
		if !strings.Contains(rec.Body.String(), attendu) {
			t.Errorf("page droits : %q absent", attendu)
		}
	}

	// Poser une règle invisible sur clients/vdf.
	if rec := postForm(h, base+"/droits", url.Values{"chemin": {"clients/vdf"}, "niveau": {"invisible"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("pose de règle : attendu 303, obtenu %d", rec.Code)
	}
	lvl, _ := s.DB.Effective(cible, "clients/vdf/note.md")
	if lvl != perms.Invisible {
		t.Errorf("règle non appliquée : attendu invisible, obtenu %v", lvl)
	}
	// Le rendu reflète la règle : annotation « règle directe » + niveau.
	rec = get(h, base, c)
	if !strings.Contains(rec.Body.String(), "règle directe") || !strings.Contains(rec.Body.String(), "invisible") {
		t.Error("page droits : la règle posée n'apparaît pas dans l'arbre rendu")
	}

	// Chemin vide, échappement racine, caractère de contrôle : refusés.
	for _, mauvais := range []string{"", "../hors-depot", "a\x00b"} {
		if rec := postForm(h, base+"/droits", url.Values{"chemin": {mauvais}, "niveau": {"lecture"}}, c); rec.Code != http.StatusBadRequest {
			t.Errorf("chemin %q : attendu 400, obtenu %d", mauvais, rec.Code)
		}
	}

	// Une règle sur un chemin inexistant est signalée dans le rendu.
	if rec := postForm(h, base+"/droits", url.Values{"chemin": {"dossier/futur"}, "niveau": {"lecture"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("règle chemin futur : attendu 303, obtenu %d", rec.Code)
	}
	rec = get(h, base, c)
	if !strings.Contains(rec.Body.String(), "ne correspond à aucun chemin actuel") {
		t.Error("page droits : règle morte non signalée")
	}
	postForm(h, base+"/droits/supprimer", url.Values{"chemin": {"dossier/futur"}}, c)

	// Changer le niveau par défaut.
	if rec := postForm(h, base+"/defaut", url.Values{"niveau": {"ecriture"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("défaut : attendu 303, obtenu %d", rec.Code)
	}
	cible2, _ := s.DB.UserByID(cible.ID)
	if cible2.DefaultLevel != perms.Ecriture {
		t.Errorf("défaut non appliqué : %v", cible2.DefaultLevel)
	}

	// Retirer la règle : retombe sur le défaut (écriture).
	if rec := postForm(h, base+"/droits/supprimer", url.Values{"chemin": {"clients/vdf"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait : attendu 303, obtenu %d", rec.Code)
	}
	lvl, _ = s.DB.Effective(cible2, "clients/vdf/note.md")
	if lvl != perms.Ecriture {
		t.Errorf("après retrait : attendu écriture, obtenu %v", lvl)
	}

	// Utilisateur inconnu → 404.
	if rec := get(h, "/admin/users/9999", c); rec.Code != http.StatusNotFound {
		t.Errorf("id inconnu : attendu 404, obtenu %d", rec.Code)
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestDirsOfOrdreHierarchique(t *testing.T) {
	// « clients-old » ne doit pas s'intercaler entre « clients » et ses enfants
	// (« - » et « . » trient avant « / » en ordre lexical brut).
	got := dirsOf([]string{"clients/vdf/n.md", "clients-old/a.md", "clients/x.md", "clients.bak/y.md"})
	want := []string{"clients", "clients/vdf", "clients-old", "clients.bak"}
	if len(got) != len(want) {
		t.Fatalf("attendu %v, obtenu %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attendu %v, obtenu %v", want, got)
		}
	}
}

// TestRenderDroitsArbreScopeAdmin : la fiche d'un compte n'énumère que les
// dossiers que l'admin appelant a le droit de lire. Trou fermé le 24/08
// (DAR-164) : avant, l'arbre partait de Store.List("") sans filtre.
func TestRenderDroitsArbreScopeAdmin(t *testing.T) {
	s := newServer(t)
	// Admin B à périmètre restreint : défaut invisible, lecture sur clients/acme
	// seul. (Le déploiement réel n'a qu'un admin qui lit tout ; ce test fabrique
	// le second admin restreint qui rend le trou observable.)
	adminB, _ := s.DB.CreateUser("adminb", "mdp", perms.Invisible, true)
	if err := s.DB.SetPermission(adminB.ID, "clients/acme", perms.Lecture); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	cible, _ := s.DB.CreateUser("cible", "mdp", perms.Lecture, false)
	s.Store.Write("clients/vdf/secret.md", "top secret", "colin")
	s.Store.Write("clients/acme/note.md", "note", "colin")

	rec := httptest.NewRecorder()
	s.renderDroits(rec, http.StatusOK, adminB, cible, "")
	body := rec.Body.String()

	// L'arbre rend chaque dossier en cellule « nom/ » (droits.html). On cible
	// cette forme, pas le nom nu : le template porte un placeholder statique
	// « clients/vdf » ailleurs, sans rapport avec l'énumération.
	if !strings.Contains(body, ">acme/") {
		t.Errorf("l'arbre devrait montrer acme/ (lisible par l'admin appelant)")
	}
	if strings.Contains(body, ">vdf/") {
		t.Errorf("l'arbre expose vdf/, hors du périmètre de lecture de l'admin appelant")
	}
}
