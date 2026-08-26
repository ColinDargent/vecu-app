package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// La surface de gestion des groupes. La résolution elle-même est figée côté
// base (server/db/groupes_test.go) ; ce qui se joue ici est QUI a le droit de
// quel geste, et ce que l'écran laisse voir.

func TestGroupeCreeEtRangeUnSkill(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.Store.Write("shared/skills/linkedin-post/SKILL.md",
		contenuSkill("linkedin-post", "Rédiger un post."), "colin")
	s.DB.ClaimSkill("linkedin-post", "shared/skills/linkedin-post", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	if rec := postForm(h, "/admin/skills/groupes", url.Values{"nom": {"Équipe contenu"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("création : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	groupes, _ := s.DB.ListGroupes()
	if len(groupes) != 1 || groupes[0].Nom != "Équipe contenu" {
		t.Fatalf("groupes = %v", groupes)
	}
	id := strconv.FormatInt(groupes[0].ID, 10)

	// Achille n'a rien tant que le groupe ne contient rien.
	if rec := postForm(h, "/admin/skills/groupes/membres", url.Values{
		"groupe_id": {id}, "niveau_" + strconv.FormatInt(achille.ID, 10): {"lecture"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("membre : %d", rec.Code)
	}
	rules, _ := s.DB.Rules(achille.ID)
	if perms.CanRead("shared/skills/linkedin-post/SKILL.md", perms.Invisible, rules) {
		t.Error("un groupe vide donne déjà accès")
	}

	// On y range le skill : l'accès s'ouvre, sans qu'aucune règle n'ait été
	// posée dans permissions.
	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug": {"linkedin-post"}, "groupe_id": {id},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("rangement : %d", rec.Code)
	}
	rules, _ = s.DB.Rules(achille.ID)
	if !perms.CanRead("shared/skills/linkedin-post/SKILL.md", perms.Invisible, rules) {
		t.Error("le skill rangé dans le groupe n'est pas devenu lisible")
	}

	// Sortir le skill referme, et ne laisse rien derrière.
	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug": {"linkedin-post"}, "groupe_id": {""},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("sortie : %d", rec.Code)
	}
	rules, _ = s.DB.Rules(achille.ID)
	if perms.CanRead("shared/skills/linkedin-post/SKILL.md", perms.Invisible, rules) {
		t.Error("le skill sorti du groupe reste lisible")
	}
}

// TestGroupesReservesAuxAdmins : un membre ne fabrique pas de périmètre.
func TestGroupesReservesAuxAdmins(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	routes := []string{
		"/admin/skills/groupes",
		"/admin/skills/groupes/supprimer",
		"/admin/skills/groupes/membres",
		"/admin/skills/groupes/modifier",
	}
	for _, route := range routes {
		rec := postForm(h, route, url.Values{
			"nom": {"pirate"}, "groupe_id": {"1"}, "niveau_1": {"ecriture"},
		}, c)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s par un membre : attendu 403, obtenu %d", route, rec.Code)
		}
	}
	if groupes, _ := s.DB.ListGroupes(); len(groupes) != 0 {
		t.Errorf("un membre a créé un groupe : %v", groupes)
	}
	// L'écran ne lui montre pas non plus la gouvernance.
	if body := get(h, "/admin/skills", c).Body.String(); strings.Contains(body, "Créer un groupe") {
		t.Error("le formulaire de création de groupe est servi à un membre")
	}
}

// TestRangerReserveAuCreateurOuAdmin : ranger son skill est le geste de partage
// de son auteur, donc il lui reste ouvert. Pour un tiers, 404.
func TestRangerReserveAuCreateurOuAdmin(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.DB.CreateUser("curieux", "mdp", perms.Lecture, false)
	s.Store.Write("shared/skills/sien/SKILL.md", contenuSkill("sien", "Skill d'achille."), "achille")
	s.DB.ClaimSkill("sien", "shared/skills/sien", achille.ID)
	id, _ := s.DB.CreerGroupe("equipe")
	// Depuis Q3 (25/08), un non-admin ne touche que les cohortes dont il est
	// membre : son geste de partage suppose qu'il connaisse le groupe de
	// l'intérieur.
	s.DB.SetMembreGroupe(id, achille.ID, perms.Lecture)
	h := s.Handler()

	// Le créateur, non-admin et membre du groupe : accepté.
	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug": {"sien"}, "groupe_id": {strconv.FormatInt(id, 10)},
	}, login(t, h, "achille", "mdp")); rec.Code != http.StatusSeeOther {
		t.Errorf("créateur : attendu 303, obtenu %d", rec.Code)
	}
	// Un tiers : 404, et rien n'a bougé.
	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug": {"sien"}, "groupe_id": {""},
	}, login(t, h, "curieux", "mdp")); rec.Code != http.StatusNotFound {
		t.Errorf("tiers : attendu 404, obtenu %d", rec.Code)
	}
	if ids, _ := s.DB.GroupesDuChemin(skillPath("sien"), "skill"); len(ids) != 1 || ids[0] != id {
		t.Errorf("un tiers a changé les groupes du skill : %v", ids)
	}
}

// TestRangerDansUnGroupeInexistant : sans ce contrôle, un id inventé rangerait
// le skill dans un groupe fantôme, invisible de l'écran et impossible à défaire
// autrement qu'en base.
func TestRangerDansUnGroupeInexistant(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("shared/skills/a/SKILL.md", contenuSkill("a", "Un skill."), "colin")
	s.DB.ClaimSkill("a", "shared/skills/a", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug": {"a"}, "groupe_id": {"9999"},
	}, c); rec.Code != http.StatusBadRequest {
		t.Errorf("groupe inexistant : attendu 400, obtenu %d", rec.Code)
	}
	if ids, _ := s.DB.GroupesDuChemin(skillPath("a"), "skill"); len(ids) != 0 {
		t.Errorf("le skill a été rangé dans un groupe fantôme : %v", ids)
	}
}

// TestNiveauInvisibleSortDuGroupe : un compte hors groupe et un compte au
// niveau invisible sont la même chose. Une seule des deux formes est stockée,
// pour qu'elles ne divergent pas.
func TestNiveauInvisibleSortDuGroupe(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	id, _ := s.DB.CreerGroupe("equipe")
	s.DB.SetMembreGroupe(id, achille.ID, perms.Ecriture)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	if rec := postForm(h, "/admin/skills/groupes/membres", url.Values{
		"groupe_id": {strconv.FormatInt(id, 10)},
		"niveau_" + strconv.FormatInt(achille.ID, 10): {"invisible"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait : %d", rec.Code)
	}
	membres, _ := s.DB.MembresGroupe(id)
	if _, dans := membres[achille.ID]; dans {
		t.Error("« invisible » a été stocké au lieu de sortir le compte du groupe")
	}
}

// TestGroupesVisiblesSansAucunSkill : sur un dépôt neuf, sans un seul skill,
// les groupes existent quand même.
//
// Ils vivaient à l'intérieur du `{{if .Skills}}` de la page : un tiers qui
// installe Vécu voyait « Aucun skill pour l'instant » et rien d'autre, donc
// aucun moyen de fabriquer le périmètre par lequel les skills arrivent.
func TestGroupesVisiblesSansAucunSkill(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	page := get(h, "/admin/skills", c).Body.String()
	if !strings.Contains(page, "Aucun skill pour l'instant") {
		t.Error("le message du dépôt vide a disparu")
	}
	if !strings.Contains(page, "Créer un groupe") {
		t.Fatal("sans skill, on ne peut pas créer de groupe")
	}

	if rec := postForm(h, "/admin/skills/groupes", url.Values{"nom": {"Équipe contenu"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("création : attendu 303, obtenu %d", rec.Code)
	}
	page = get(h, "/admin/skills", c).Body.String()
	if !strings.Contains(page, "Équipe contenu") {
		t.Error("le groupe créé sur un dépôt sans skill ne s'affiche pas")
	}
	// Et son panneau d'accès est réglable : les deux comptes y ont leur ligne,
	// trois niveaux chacun. Sans skill au dépôt, `Users` est vide - le panneau
	// des groupes ne doit donc rien lui devoir.
	if n := strings.Count(page, `name="niveau_`); n != 6 {
		t.Errorf("%d radios dans le panneau du groupe, attendu 6 (2 comptes x 3 niveaux)", n)
	}
}

// TestMembresGroupeAtomique : un niveau invalide n'en laisse aucun posé.
//
// Le compte valide est traité AVANT l'invalide dans l'ordre de la liste : une
// implémentation qui écrit au fil de la lecture laisserait le groupe à moitié
// réglé, sans que l'écran puisse dire laquelle des deux moitiés a pris.
func TestMembresGroupeAtomique(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	id, _ := s.DB.CreerGroupe("equipe")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := postForm(h, "/admin/skills/groupes/membres", url.Values{
		"groupe_id": {strconv.FormatInt(id, 10)},
		"niveau_" + strconv.FormatInt(colin.ID, 10):   {"ecriture"},
		"niveau_" + strconv.FormatInt(achille.ID, 10): {"root"},
	}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("niveau invalide : attendu 400, obtenu %d", rec.Code)
	}
	membres, _ := s.DB.MembresGroupe(id)
	if len(membres) != 0 {
		t.Errorf("le groupe a été réglé malgré le refus : %v", membres)
	}
}

// TestGroupeSeRenommeEtSeCompose : les deux gestes qui manquaient pour qu'un
// groupe soit modifiable après sa création (retour de Colin du 21/08).
func TestGroupeSeRenommeEtSeCompose(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	for _, slug := range []string{"linkedin-post", "dev-scope"} {
		s.Store.Write("shared/skills/"+slug+"/SKILL.md", contenuSkill(slug, "Un skill."), "colin")
		s.DB.ClaimSkill(slug, "shared/skills/"+slug, colin.ID)
	}
	id, _ := s.DB.CreerGroupe("equipe")
	s.DB.SetMembreGroupe(id, achille.ID, perms.Lecture)
	s.DB.RangeChemin(id, "shared/skills/linkedin-post", "skill", "linkedin-post")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// Le panneau est là, avec le nom modifiable et les deux skills cochables.
	page := get(h, "/admin/skills", c).Body.String()
	if !strings.Contains(page, "Modifier le groupe") {
		t.Fatal("la vue groupe ne propose pas de le modifier")
	}
	// Les cases de composition, pas les champs cachés des formulaires de la
	// ligne du skill, qui portent aussi un `name="slug"`.
	if n := strings.Count(page, `type="checkbox" name="slug"`); n != 2 {
		t.Errorf("%d skills proposés à la composition, attendu 2", n)
	}

	// Renommer, sortir linkedin-post, faire entrer dev-scope : une requête.
	rec := postForm(h, "/admin/skills/groupes/modifier", url.Values{
		"groupe_id": {strconv.FormatInt(id, 10)},
		"nom":       {"Équipe contenu"},
		"slug":      {"dev-scope"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("modification : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	groupes, _ := s.DB.ListGroupes()
	if len(groupes) != 1 || groupes[0].Nom != "Équipe contenu" {
		t.Fatalf("groupes = %v", groupes)
	}
	skills, _ := s.DB.SkillsDuGroupe(id)
	if len(skills) != 1 || skills[0] != "dev-scope" {
		t.Fatalf("composition = %v, attendu [dev-scope]", skills)
	}
	// Les accès suivent : achille lit ce qui est entré, plus ce qui est sorti.
	regles, _ := s.DB.Rules(achille.ID)
	if !perms.CanRead("shared/skills/dev-scope/SKILL.md", perms.Invisible, regles) {
		t.Error("le skill entré dans le groupe n'est pas devenu lisible")
	}
	if perms.CanRead("shared/skills/linkedin-post/SKILL.md", perms.Invisible, regles) {
		t.Error("le skill sorti du groupe reste lisible")
	}
}

// TestModifierGroupeRefuseUnSlugInconnu : RangeSkill insère par slug sans se
// demander si le skill existe. Un formulaire bricolé y créerait une
// appartenance fantôme, invisible de l'écran et jamais nettoyée.
func TestModifierGroupeRefuseUnSlugInconnu(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	id, _ := s.DB.CreerGroupe("equipe")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := postForm(h, "/admin/skills/groupes/modifier", url.Values{
		"groupe_id": {strconv.FormatInt(id, 10)},
		"nom":       {"equipe"},
		"slug":      {"skill-qui-nexiste-pas"},
	}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("slug inconnu : attendu 400, obtenu %d", rec.Code)
	}
	if skills, _ := s.DB.SkillsDuGroupe(id); len(skills) != 0 {
		t.Errorf("une appartenance fantôme a été créée : %v", skills)
	}
}

// TestModifierGroupeReserveAuxAdmins : un membre ne fabrique pas de périmètre,
// et ne renomme pas celui des autres.
func TestModifierGroupeReserveAuxAdmins(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	id, _ := s.DB.CreerGroupe("equipe")
	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	rec := postForm(h, "/admin/skills/groupes/modifier", url.Values{
		"groupe_id": {strconv.FormatInt(id, 10)}, "nom": {"pirate"},
	}, c)
	if rec.Code != http.StatusForbidden {
		t.Errorf("modification par un membre : attendu 403, obtenu %d", rec.Code)
	}
	if groupes, _ := s.DB.ListGroupes(); groupes[0].Nom != "equipe" {
		t.Error("un membre a renommé un groupe")
	}
}

// TestRangerUnSkillDansDeuxGroupes : le parcours complet, par HTTP.
//
// Le formulaire de la ligne du skill porte AUTANT de `groupe_id` que de cases
// cochées, et cet ensemble fait foi. C'est ce qui remplace le `<select>` : un
// choix unique ne sait pas dire « les opérations ET le marketing ».
func TestRangerUnSkillDansDeuxGroupes(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.Store.Write("shared/skills/veille/SKILL.md", contenuSkill("veille", "Un skill."), "colin")
	s.DB.ClaimSkill("veille", "shared/skills/veille", colin.ID)
	ops, _ := s.DB.CreerGroupe("Opérations")
	mkt, _ := s.DB.CreerGroupe("Marketing")
	s.DB.SetMembreGroupe(ops, achille.ID, perms.Lecture)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// L'écran propose bien des cases, une par groupe, et plus un select.
	page := get(h, "/admin/skills", c).Body.String()
	if n := strings.Count(page, `type="checkbox" name="groupe_id"`); n != 2 {
		t.Fatalf("%d case(s) de groupe rendue(s), 2 attendues", n)
	}
	if strings.Contains(page, `<select name="groupe_id">`) {
		t.Error("le select de groupe est encore rendu : un skill ne tient plus dans un seul")
	}

	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug":      {"veille"},
		"groupe_id": {strconv.FormatInt(ops, 10), strconv.FormatInt(mkt, 10)},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("rangement dans deux groupes : attendu 303, obtenu %d", rec.Code)
	}
	ids, err := s.DB.GroupesDuChemin(skillPath("veille"), "skill")
	if err != nil {
		t.Fatalf("GroupesDuChemin : %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("le skill est dans %d groupe(s), 2 attendus : %v", len(ids), ids)
	}

	// Le membre d'un seul des deux y accède quand même.
	a, _ := s.DB.UserByID(achille.ID)
	if niveau, _ := s.DB.Effective(a, "shared/skills/veille/SKILL.md"); niveau != perms.Lecture {
		t.Errorf("membre d'Opérations : %v, lecture attendue", niveau)
	}

	// Décocher le second le laisse dans le premier.
	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug": {"veille"}, "groupe_id": {strconv.FormatInt(ops, 10)},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait d'un groupe : attendu 303, obtenu %d", rec.Code)
	}
	ids, _ = s.DB.GroupesDuChemin(skillPath("veille"), "skill")
	if len(ids) != 1 || ids[0] != ops {
		t.Errorf("après décochage : %v, seul Opérations attendu", ids)
	}
	if niveau, _ := s.DB.Effective(a, "shared/skills/veille/SKILL.md"); niveau != perms.Lecture {
		t.Errorf("sortir du Marketing a fermé l'accès d'Opérations : %v", niveau)
	}
}

// TestCreateurNeDefaitPasUneCohorteQuIlNeConnaitPas : Q3, tranchée le 25/08.
//
// L'ensemble coché fait foi, donc sans bornage un créateur non-admin sortirait
// son skill de TOUTES les cohortes qu'un administrateur a réglées, en un POST -
// alors que l'écran ne lui en montre aucune. Un élargissement de pouvoir que
// personne n'avait décidé.
func TestCreateurNeDefaitPasUneCohorteQuIlNeConnaitPas(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.Store.Write("shared/skills/sien/SKILL.md", contenuSkill("sien", "Skill d'achille."), "achille")
	s.DB.ClaimSkill("sien", "shared/skills/sien", achille.ID)

	sienne, _ := s.DB.CreerGroupe("La sienne")
	s.DB.SetMembreGroupe(sienne, achille.ID, perms.Lecture)
	admin, _ := s.DB.CreerGroupe("Réglée par l'admin")
	s.DB.RangeChemin(sienne, skillPath("sien"), "skill", "sien")
	s.DB.RangeChemin(admin, skillPath("sien"), "skill", "sien")

	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	// Il décoche tout : seule SA cohorte doit partir.
	if rec := postForm(h, "/admin/skills/ranger", url.Values{"slug": {"sien"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("attendu 303, obtenu %d", rec.Code)
	}
	ids, _ := s.DB.GroupesDuChemin(skillPath("sien"), "skill")
	if len(ids) != 1 || ids[0] != admin {
		t.Errorf("le créateur a défait une cohorte qu'il ne connaît pas : %v", ids)
	}

	// Et il ne peut pas non plus y RANGER son skill.
	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug": {"sien"}, "groupe_id": {strconv.FormatInt(admin, 10)},
	}, c); rec.Code != http.StatusBadRequest {
		t.Errorf("rangement dans une cohorte étrangère : attendu 400, obtenu %d", rec.Code)
	}

	// Un administrateur, lui, garde la main sur tout.
	if rec := postForm(h, "/admin/skills/ranger", url.Values{
		"slug": {"sien"}, "groupe_id": {strconv.FormatInt(sienne, 10)},
	}, login(t, h, "colin", "mdp")); rec.Code != http.StatusSeeOther {
		t.Fatalf("admin : attendu 303, obtenu %d", rec.Code)
	}
	ids, _ = s.DB.GroupesDuChemin(skillPath("sien"), "skill")
	if len(ids) != 1 || ids[0] != sienne {
		t.Errorf("l'administrateur n'a pas la main sur toutes les cohortes : %v", ids)
	}
}
