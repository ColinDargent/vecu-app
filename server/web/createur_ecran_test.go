package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func TestEcranFichierDitQuiLACree(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	if _, err := s.Store.Write("notes/décisions.md", "v1", "achille"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("notes/décisions.md", "v2", "colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	rec := get(h, "/admin/dossiers/notes/d%C3%A9cisions.md", login(t, h, "colin", "mdp"))
	if rec.Code != http.StatusOK {
		t.Fatalf("attendu 200, obtenu %d", rec.Code)
	}
	corps := rec.Body.String()
	if !strings.Contains(corps, "créé par") || !strings.Contains(corps, "achille") {
		t.Errorf("l'écran ne dit pas qui a créé le fichier :\n%s", corps)
	}
}

func TestListingDossierDitQuiACreeChaqueEntree(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	// `sous/` naît de son fichier le plus ancien, celui d'achille. Le second
	// est écrit par marie APRÈS : c'est achille qui doit s'afficher sur le
	// dossier.
	for _, e := range []struct{ chemin, auteur string }{
		{"notes/sous/premier.md", "achille"},
		{"notes/sous/second.md", "marie"},
		{"notes/seul.md", "marie"},
	} {
		if _, err := s.Store.Write(e.chemin, "x", e.auteur); err != nil {
			t.Fatal(err)
		}
	}
	h := s.Handler()
	rec := get(h, "/admin/dossiers/notes", login(t, h, "colin", "mdp"))
	if rec.Code != http.StatusOK {
		t.Fatalf("attendu 200, obtenu %d", rec.Code)
	}
	corps := rec.Body.String()
	ligneDossier := extraitEntree(t, corps, "sous/")
	if !strings.Contains(ligneDossier, "achille") {
		t.Errorf("le dossier « sous » devrait porter le créateur de son fichier le plus ancien :\n%s",
			ligneDossier)
	}
	if strings.Contains(ligneDossier, "marie") {
		t.Errorf("le dossier « sous » porte le créateur d'un fichier plus RÉCENT :\n%s", ligneDossier)
	}
	ligneFichier := extraitEntree(t, corps, "seul.md")
	if !strings.Contains(ligneFichier, "marie") {
		t.Errorf("le fichier « seul.md » devrait porter son créateur :\n%s", ligneFichier)
	}
}

// LA FUITE PAR LA PORTE DETOURNEE. Le dossier `notes` est visible ; le fichier
// le plus ancien qu'il contient ne l'est pas. Sans filtre sur la carte, le nom
// de son créateur s'afficherait sur un dossier parfaitement légitime - la
// personne n'apprend pas le chemin caché, elle apprend qui l'a posé.
func TestCreateurNeFuitPasParUnDossierVisible(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	marie, err := s.DB.CreateUser("marie", "mdp", perms.Lecture, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("notes/caché.md", "x", "achille"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("notes/ouvert.md", "y", "colin"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetPermission(marie.ID, "notes/caché.md", perms.Invisible); err != nil {
		t.Fatal(err)
	}

	h := s.Handler()
	c := login(t, h, "marie", "mdp")

	// Précondition : marie voit bien `notes`, et son fichier ouvert.
	rec := get(h, "/admin/dossiers", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("racine : attendu 200, obtenu %d", rec.Code)
	}
	racine := rec.Body.String()
	if !strings.Contains(racine, "notes/") {
		t.Fatalf("précondition : marie ne voit pas le dossier notes :\n%s", racine)
	}
	if strings.Contains(racine, "achille") {
		t.Errorf("le créateur du fichier caché fuit par le dossier qui le contient :\n%s",
			extraitEntree(t, racine, "notes/"))
	}
	if !strings.Contains(racine, "colin") {
		t.Errorf("précondition : le créateur visible devrait s'afficher :\n%s", racine)
	}

	// Et dans le dossier lui-même : ni le chemin caché, ni son créateur.
	rec = get(h, "/admin/dossiers/notes", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("dossier : attendu 200, obtenu %d", rec.Code)
	}
	dedans := rec.Body.String()
	if strings.Contains(dedans, "achille") {
		t.Errorf("le créateur d'un fichier invisible s'affiche dans le listing :\n%s", dedans)
	}
	if !strings.Contains(dedans, "ouvert.md") || !strings.Contains(dedans, "colin") {
		t.Errorf("le fichier visible et son créateur devraient être là :\n%s", dedans)
	}
}

// extraitEntree : le bloc <a>...</a> du listing qui porte `motif`. Les
// assertions portent sur UNE entrée, jamais sur la page : « achille apparaît
// quelque part » resterait vrai si le nom s'affichait sur la mauvaise ligne.
func extraitEntree(t *testing.T, corps, motif string) string {
	t.Helper()
	for _, bloc := range strings.Split(corps, "<a href=") {
		if i := strings.Index(bloc, "</a>"); i >= 0 && strings.Contains(bloc[:i], motif) {
			return bloc[:i]
		}
	}
	t.Fatalf("aucune entrée de listing ne porte %q dans :\n%s", motif, corps)
	return ""
}

// Le second verrou, testé là où il agit : les écrans ne l'exercent pas (ils
// n'indexent la carte qu'aux chemins qu'ils montrent déjà), donc seule une
// assertion sur la fonction elle-même peut le faire échouer.
func TestCarteDesCreateursNeSortPasDuPerimetre(t *testing.T) {
	s := newServer(t)
	if _, err := s.Store.Write("notes/ouvert.md", "x", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("notes/caché.md", "y", "achille"); err != nil {
		t.Fatal(err)
	}
	carte := s.createursDuPerimetre([]string{"notes/ouvert.md"})
	if got := carte["notes/ouvert.md"].Auteur; got != "colin" {
		t.Errorf("chemin du périmètre : créateur = %q, « colin » attendu", got)
	}
	if c, present := carte["notes/caché.md"]; present {
		t.Errorf("chemin hors périmètre présent dans la carte, attribué à %q", c.Auteur)
	}
	if len(carte) != 1 {
		t.Errorf("la carte porte %d clés pour un périmètre de 1 : %v", len(carte), carte)
	}
}

// UN DEPOT NEUF n'a rien à dire sur les créateurs : la légende qui explique la
// colonne ne doit pas s'imprimer, sinon la page promet une colonne absente et
// le vide se lit comme une anomalie.
func TestLegendeDuCreateurSeTaitQuandIlNYEnAAucun(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	h := s.Handler()
	rec := get(h, "/admin/dossiers", login(t, h, "colin", "mdp"))
	if rec.Code != http.StatusOK {
		t.Fatalf("attendu 200, obtenu %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "créé par") {
		t.Errorf("la légende s'imprime sur un dépôt sans aucune entrée :\n%s", rec.Body.String())
	}
}
