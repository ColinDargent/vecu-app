package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// Périmètre du test : achille lit tout sauf clients/vdf (invisible),
// avec une surcharge lecture sur un fichier précis de ce dossier invisible.
func newServerAvecContenu(t *testing.T) *Server {
	t.Helper()
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.DB.SetPermission(achille.ID, "clients/vdf", perms.Invisible)
	s.DB.SetPermission(achille.ID, "clients/vdf/public.md", perms.Lecture)
	s.Store.Write("notes/idees.md", "contenu idées", "colin")
	s.Store.Write("clients/vdf/secret.md", "top secret", "colin")
	s.Store.Write("clients/vdf/public.md", "visible malgré le dossier", "colin")
	s.Store.Write("clients/acme/note.md", "note acme", "colin")
	return s
}

func TestNavigationFiltreePerimetre(t *testing.T) {
	s := newServerAvecContenu(t)
	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	// Racine : notes et clients visibles.
	rec := get(h, "/admin/dossiers", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("racine : attendu 200, obtenu %d", rec.Code)
	}
	for _, attendu := range []string{"notes", "clients"} {
		if !strings.Contains(rec.Body.String(), attendu) {
			t.Errorf("racine : %q absent", attendu)
		}
	}

	// clients/vdf : navigable (surcharge lecture sur public.md) mais secret.md absent.
	rec = get(h, "/admin/dossiers/clients/vdf", c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "public.md") {
		t.Fatalf("dossier invisible avec surcharge : public.md doit rester navigable (%d)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret.md") {
		t.Error("secret.md hors périmètre listé")
	}

	// Lecture du fichier surchargé OK, du fichier invisible → 404.
	rec = get(h, "/admin/dossiers/clients/vdf/public.md", c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "visible malgré le dossier") {
		t.Errorf("fichier surchargé : attendu contenu, obtenu %d", rec.Code)
	}
	if rec := get(h, "/admin/dossiers/clients/vdf/secret.md", c); rec.Code != http.StatusNotFound {
		t.Errorf("fichier invisible : attendu 404, obtenu %d", rec.Code)
	}

	// L'admin, lui, voit tout.
	ca := login(t, h, "colin", "mdp")
	if rec := get(h, "/admin/dossiers/clients/vdf/secret.md", ca); rec.Code != http.StatusOK {
		t.Errorf("admin : attendu 200 sur secret.md, obtenu %d", rec.Code)
	}
}

func TestConflitsEnAttente(t *testing.T) {
	s := newServerAvecContenu(t)
	s.Store.Write("notes/idees (conflit 2026-07-23 10h00 - achille).md", "version conflictuelle", "colin")
	s.Store.Write("clients/vdf/secret (conflit 2026-07-23 10h01 - colin).md", "conflit hors périmètre", "colin")
	h := s.Handler()

	// achille : voit le conflit de notes/, pas celui du dossier invisible.
	c := login(t, h, "achille", "mdp")
	rec := get(h, "/admin/", c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "idees (conflit") {
		t.Errorf("conflit visible absent (%d)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret (conflit") {
		t.Error("conflit hors périmètre listé")
	}

	// colin (admin) : voit les deux.
	ca := login(t, h, "colin", "mdp")
	rec = get(h, "/admin/", ca)
	if !strings.Contains(rec.Body.String(), "idees (conflit") || !strings.Contains(rec.Body.String(), "secret (conflit") {
		t.Error("admin : les deux conflits doivent être listés")
	}
}

func TestLiensEchappes(t *testing.T) {
	s := newServerAvecContenu(t)
	s.Store.Write("notes/plan#1.md", "x", "colin")
	s.Store.Write("notes/quoi?.md", "y", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// « # » et « ? » doivent être échappés dans les href, sinon le lien casse.
	rec := get(h, "/admin/dossiers/notes", c)
	if !strings.Contains(rec.Body.String(), "plan%231.md") {
		t.Error("href non échappé pour « # »")
	}
	if !strings.Contains(rec.Body.String(), "quoi%3F.md") {
		t.Error("href non échappé pour « ? »")
	}
}

func TestEchappementHTML(t *testing.T) {
	s := newServerAvecContenu(t)
	s.Store.Write("notes/x<script>.md", "<b>gras</b>", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := get(h, "/admin/dossiers/notes", c)
	if strings.Contains(rec.Body.String(), "x<script>") {
		t.Error("nom de fichier non échappé dans le listing")
	}
	rec = get(h, "/admin/dossiers/notes/x%3Cscript%3E.md", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("lecture fichier au nom hostile : attendu 200, obtenu %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<b>gras</b>") {
		t.Error("contenu non échappé dans la page fichier")
	}
	// UNE DECISION A BOUGE ICI (03/09, le rendu markdown). Ce fichier est un
	// `.md` : sa vue par défaut n'est plus le texte échappé par html/template,
	// c'est le markdown rendu, où le HTML brut d'une note est JETE par goldmark
	// (jamais échappé, jamais exécuté - voir markdown_test.go, qui garde cette
	// propriété-là sur six vecteurs). L'assertion ci-dessus reste donc vraie,
	// mais pour une autre raison, et « &lt;b&gt; » n'apparaît plus.
	//
	// L'échappement, lui, n'a pas disparu : il garde la vue SOURCE, qui est
	// l'autre moitié de cet écran et le seul endroit où le contenu d'un fichier
	// est encore inséré tel quel. C'est là que le test le vérifie maintenant.
	rec = get(h, "/admin/dossiers/notes/x%3Cscript%3E.md?source", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("vue source : attendu 200, obtenu %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<b>gras</b>") {
		t.Error("contenu non échappé dans la vue source")
	}
	if !strings.Contains(rec.Body.String(), "&lt;b&gt;gras&lt;/b&gt;") {
		t.Error("contenu échappé absent")
	}
}

func TestDossierCacheIndistinguable(t *testing.T) {
	s := newServerAvecContenu(t)
	achille, _ := s.DB.Authenticate("achille", "mdp")
	s.DB.SetPermission(achille.ID, "prive", perms.Invisible)
	s.Store.Write("prive/note.md", "secret", "colin")
	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	cache := get(h, "/admin/dossiers/prive", c)
	inexistant := get(h, "/admin/dossiers/nexiste-pas", c)
	if cache.Code != http.StatusNotFound || inexistant.Code != http.StatusNotFound {
		t.Fatalf("attendu 404/404, obtenu %d/%d", cache.Code, inexistant.Code)
	}
	if cache.Body.String() != inexistant.Body.String() {
		t.Error("un dossier caché doit être indistinguable d'un dossier inexistant")
	}
	fCache := get(h, "/admin/dossiers/prive/note.md", c)
	if fCache.Code != http.StatusNotFound || fCache.Body.String() != inexistant.Body.String() {
		t.Error("un fichier caché doit être indistinguable d'un fichier inexistant")
	}
}

func TestConflitFauxPositifs(t *testing.T) {
	s := newServerAvecContenu(t)
	s.Store.Write("notes/reunion (conflit avec Marc).md", "note ordinaire", "colin")
	s.Store.Write("notes/vrai (conflit 2026-07-23 11h30 - achille).md", "copie", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := get(h, "/admin/", c)
	if strings.Contains(rec.Body.String(), "reunion (conflit avec Marc)") {
		t.Error("une note ordinaire matche le marqueur de conflit")
	}
	if !strings.Contains(rec.Body.String(), "vrai (conflit 2026-07-23 11h30 - achille)") {
		t.Error("une vraie copie de conflit n'est pas listée")
	}
}

// TestBandeauConflitsMuetQuandRienAArbitrer : le bandeau ne s'affiche que s'il
// y a quelque chose. C'est la raison pour laquelle l'onglet a disparu - vide en
// permanence depuis que les copies locales ne remontent plus, il avait appris à
// ne plus être regardé.
func TestBandeauConflitsMuetQuandRienAArbitrer(t *testing.T) {
	s := newServerAvecContenu(t)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	body := get(h, "/admin/", c).Body.String()
	if strings.Contains(body, "copie de conflit") {
		t.Error("le bandeau s'affiche alors qu'il n'y a aucune copie")
	}

	s.Store.Write("notes/idees (conflit 2026-07-23 10h00 - achille).md", "v", "colin")
	body = get(h, "/admin/", c).Body.String()
	if !strings.Contains(body, "copie de conflit") {
		t.Error("le bandeau ne s'affiche pas alors qu'une copie attend")
	}
}
