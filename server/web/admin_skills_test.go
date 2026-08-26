package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/perms"
)

// L'exemption administrateur sur les skills, décidée le 21/08. Elle renverse F3
// (livrée le 28/07 après incident) et ces tests figent ses trois bornes : elle
// vit dans le web et nulle part ailleurs, elle donne lecture et pas écriture,
// et elle ne franchit pas la racine des skills.

// prive : un skill déposé par achille, jamais partagé. F3 le garde invisible de
// tout le monde, administrateur compris - c'est ce qui change ici.
func prive(t *testing.T, s *Server, slug, desc string, auteurID int64, auteur string) {
	t.Helper()
	contenu := "---\nname: " + slug + "\ndescription: " + desc + "\n---\n\n# corps\n"
	if _, err := s.Store.Write("shared/skills/"+slug+"/SKILL.md", contenu, auteur); err != nil {
		t.Fatalf("Write : %v", err)
	}
	if _, err := s.DB.ClaimSkill(slug, "shared/skills/"+slug, auteurID); err != nil {
		t.Fatalf("ClaimSkill : %v", err)
	}
}

func TestAdminVoitLesSkillsPrivesDesAutres(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	prive(t, s, "blank-page", "Sortir de la page blanche.", achille.ID, "achille")
	h := s.Handler()

	// L'administrateur voit le skill ET sa description.
	body := get(h, "/admin/skills", login(t, h, "colin", "mdp")).Body.String()
	if !strings.Contains(body, "blank-page") {
		t.Error("l'administrateur ne voit pas le skill privé d'un autre")
	}
	if !strings.Contains(body, "Sortir de la page blanche.") {
		t.Error("l'administrateur voit la ligne mais pas le contenu du frontmatter")
	}
}

// TestNonAdminNeVoitToujoursPasLesSkillsPrives : l'exemption est réservée aux
// administrateurs. Le modèle privé tient pour tout le monde d'autre.
func TestNonAdminNeVoitToujoursPasLesSkillsPrives(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	prive(t, s, "strategie", "Confidentiel.", colin.ID, "colin")
	h := s.Handler()

	body := get(h, "/admin/skills", login(t, h, "achille", "mdp")).Body.String()
	if strings.Contains(body, "strategie") || strings.Contains(body, "Confidentiel.") {
		t.Error("un membre ordinaire voit un skill privé")
	}
}

// TestAdminNeSynchronisePasLesSkillsDesAutres : LA borne qui compte. Sans elle,
// le poste de l'administrateur téléchargerait les skills privés de tout le
// monde en permanence et les projetterait dans son `.claude/skills/`.
func TestAdminNeSynchronisePasLesSkillsDesAutres(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	prive(t, s, "blank-page", "Sortir de la page blanche.", achille.ID, "achille")

	// Le jeton d'appareil de l'administrateur, celui que porte son client de sync.
	jeton, err := s.DB.IssueToken(colin.ID, "poste")
	if err != nil {
		t.Fatalf("CreateDeviceToken : %v", err)
	}
	apiSrv := (&api.Server{DB: s.DB, Store: s.Store}).Handler()

	// L'arbre que le client reçoit ne contient pas le skill privé d'achille.
	req := httptest.NewRequest("GET", "/tree", nil)
	req.Header.Set("Authorization", "Bearer "+jeton)
	rec := httptest.NewRecorder()
	apiSrv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tree : %d", rec.Code)
	}
	var arbre struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &arbre); err != nil {
		t.Fatalf("json : %v", err)
	}
	for _, p := range arbre.Paths {
		if strings.Contains(p, "blank-page") {
			t.Fatalf("le client de l'administrateur reçoit un skill privé : %s", p)
		}
	}

	// Et la lecture directe du fichier lui est refusée, comme à n'importe qui.
	req = httptest.NewRequest("GET", "/files/shared/skills/blank-page/SKILL.md", nil)
	req.Header.Set("Authorization", "Bearer "+jeton)
	rec = httptest.NewRecorder()
	apiSrv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /files sur un skill privé : attendu 404, obtenu %d", rec.Code)
	}
}

// TestAdminArbitreLesAccesDunSkillQuilNaPasCree : voir sans pouvoir arbitrer
// n'aurait servi à rien - c'est l'autre moitié de la décision.
func TestAdminArbitreLesAccesDunSkillQuilNaPasCree(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	marie, _ := s.DB.CreateUser("marie", "mdp", perms.Lecture, false)
	prive(t, s, "blank-page", "Sortir de la page blanche.", achille.ID, "achille")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"blank-page"}, "user_id": {strconv.FormatInt(marie.ID, 10)}, "niveau": {"lecture"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("arbitrage par l'admin : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	rules, _ := s.DB.Rules(marie.ID)
	m, _ := s.DB.UserByID(marie.ID)
	if !perms.CanRead("shared/skills/blank-page/SKILL.md", m.DefaultLevel, rules) {
		t.Error("l'accès posé par l'administrateur n'a pas pris")
	}
}

// TestAdminNeModifiePasLeSkillDunAutre : l'exemption donne LECTURE. Gouverner
// n'est pas modifier le travail de quelqu'un.
func TestAdminNeModifiePasLeSkillDunAutre(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	prive(t, s, "blank-page", "Sortir de la page blanche.", achille.ID, "achille")
	avant, _ := s.Store.Read("shared/skills/blank-page/SKILL.md", "")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	if rec := postForm(h, "/admin/skills/tags", url.Values{
		"slug": {"blank-page"}, "tags": {"impose"},
	}, c); rec.Code != http.StatusForbidden {
		t.Errorf("écriture des tags par l'admin : attendu 403, obtenu %d", rec.Code)
	}
	if apres, _ := s.Store.Read("shared/skills/blank-page/SKILL.md", ""); apres != avant {
		t.Errorf("l'administrateur a modifié le skill d'un autre :\n%s", apres)
	}
}

// TestExemptionNeFranchitPasLaRacineDesSkills : l'exemption est calculée dans
// skillsDuDepot, qui ne regarde que `shared/skills/`. Le reste du dépôt suit
// les règles ordinaires, y compris pour un administrateur.
func TestExemptionNeFranchitPasLaRacineDesSkills(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("clients/vdf/secret.md", "confidentiel", "achille")
	if err := s.DB.SetPermission(colin.ID, "clients/vdf", perms.Invisible); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	if rec := get(h, "/admin/dossiers/clients/vdf", c); rec.Code != http.StatusNotFound {
		t.Errorf("un dossier rendu invisible à l'admin reste accessible : %d", rec.Code)
	}
	if body := get(h, "/admin/", c).Body.String(); strings.Contains(body, "vdf") {
		t.Error("un dossier invisible apparaît sur l'accueil de l'admin")
	}
}

// TestAdminGardeSesDroitsReelsSurSesSkills : régression trouvée à l'écran, le
// 21/08. L'exemption REMPLAÇAIT les droits de l'administrateur au lieu de s'y
// ajouter : il voyait tous les skills, mais il perdait l'écriture sur les
// siens, et l'interface lui annonçait « vous êtes en lecture seule sur ce
// skill » à propos d'un skill qu'il venait d'écrire.
//
// La règle : pour un administrateur, le niveau effectif sur un skill est le
// maximum entre son niveau réel et « lecture ». L'exemption est un plancher,
// jamais un plafond.
func TestAdminGardeSesDroitsReelsSurSesSkills(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	prive(t, s, "a-moi", "Mon skill.", colin.ID, "colin")
	prive(t, s, "a-lui", "Son skill.", achille.ID, "achille")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// Sur SON skill : le formulaire de tags est servi, et il écrit.
	if rec := postForm(h, "/admin/skills/tags", url.Values{
		"slug": {"a-moi"}, "tags": {"interne"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Errorf("tags sur son propre skill : attendu 303, obtenu %d", rec.Code)
	}
	// Sur celui d'un autre : lecture seulement, l'exemption ne donne pas plus.
	if rec := postForm(h, "/admin/skills/tags", url.Values{
		"slug": {"a-lui"}, "tags": {"impose"},
	}, c); rec.Code != http.StatusForbidden {
		t.Errorf("tags sur le skill d'un autre : attendu 403, obtenu %d", rec.Code)
	}
}
