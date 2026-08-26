package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// contenuSkill : un SKILL.md minimal exploitable.
func contenuSkill(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# corps"
}

// TestSkillsLectureOuverteAuMembre : la page est ouverte à tout compte, et
// chacun n'y voit que son propre périmètre.
//
// Remplace TestSkillsRefuseNonAdmin, qui exigeait un 403 : contrat changé le
// 28/07 (décision Colin). L'ancien comportement rendait la délégation « le
// créateur gère son skill » (F3) inatteignable, faute de surface.
func TestSkillsLectureOuverteAuMembre(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	membre, _ := s.DB.CreateUser("membre", "mdp", perms.Invisible, false)
	// Compte sans lien avec ce que le membre voit : sa seule présence dans le
	// HTML signalerait une fuite de l'annuaire.
	s.DB.CreateUser("tiers-sans-rapport", "mdp", perms.Invisible, false)
	s.Store.Write("shared/skills/partage/SKILL.md", contenuSkill("partage", "Skill accessible."), "colin")
	s.Store.Write("shared/skills/prive/SKILL.md", contenuSkill("prive", "Skill hors périmètre."), "colin")
	s.DB.ClaimSkill("partage", "shared/skills/partage", colin.ID)
	s.DB.ClaimSkill("prive", "shared/skills/prive", colin.ID)
	s.DB.SetPermission(membre.ID, "shared/skills/partage", perms.Lecture)

	h := s.Handler()
	c := login(t, h, "membre", "mdp")

	rec := get(h, "/admin/skills", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/skills en membre : attendu 200, obtenu %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "partage") {
		t.Error("le membre ne voit pas le skill qui lui a été partagé")
	}
	// Fuite : un skill hors périmètre ne doit pas figurer dans le HTML servi,
	// même masqué visuellement.
	if strings.Contains(body, "prive") || strings.Contains(body, "Skill hors périmètre") {
		t.Error("un skill hors périmètre apparaît dans le HTML servi au membre")
	}
	// Le membre ne gère aucun skill : l'annuaire des comptes ne lui est pas servi.
	// Le créateur d'un skill qu'il voit reste affiché - c'est voulu (l'API
	// `GET /skills` expose déjà `creator` : savoir qui vous a partagé un skill
	// fait partie de l'information). Ce qui ne doit pas fuiter, c'est la liste
	// des comptes sans rapport.
	if strings.Contains(body, "tiers-sans-rapport") {
		t.Error("l'annuaire des comptes est servi à un membre qui ne gère aucun skill")
	}
	// Et aucun contrôle de partage ne lui est proposé.
	if strings.Contains(body, `action="/admin/skills/acces"`) {
		t.Error("des contrôles de partage sont rendus pour un non-créateur")
	}
}

// TestSkillsEcritureRefuseAuNonCreateur : l'ouverture en lecture ne desserre rien
// sur l'écriture. Un compte qui ne gère pas le skill est refusé en 404 - son
// existence n'est pas révélée à qui ne le gère pas.
func TestSkillsEcritureRefuseAuNonCreateur(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	membre, _ := s.DB.CreateUser("membre", "mdp", perms.Lecture, false)
	// Un skill bien réel, partagé au membre en lecture : il le voit, il ne le
	// gère pas. Sans ça le test passerait pour une raison sans rapport (slug
	// inexistant).
	s.Store.Write("shared/skills/dev-scope/SKILL.md", contenuSkill("dev-scope", "Scoper."), "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	s.DB.SetPermission(membre.ID, "shared/skills/dev-scope", perms.Lecture)

	h := s.Handler()
	c := login(t, h, "membre", "mdp")

	rec := postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"dev-scope"}, "user_id": {strconv.FormatInt(membre.ID, 10)}, "niveau": {"ecriture"},
	}, c)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /admin/skills/acces en non-créateur : attendu 404, obtenu %d", rec.Code)
	}
	rules, _ := s.DB.Rules(membre.ID)
	if perms.Effective("shared/skills/dev-scope", perms.Lecture, rules) == perms.Ecriture {
		t.Error("le membre s'est élevé en écriture sur un skill qu'il ne gère pas")
	}
}

// TestSetAccesSkillOuvertALAdmin : LE TEST RETOURNÉ LE 21/08.
//
// Il figeait « pas de super-admin skills » (F3, livrée le 28/07 après un
// incident) et il a cassé quand l'exemption administrateur est revenue. Il est
// retourné plutôt que supprimé : ce qu'il garde est la trace de la décision, et
// il continue de vérifier que le refus tient pour tout le monde d'autre (voir
// TestSetAccesSkillRefuseUnTiersNonCreateur juste en dessous).
//
// La décision et son motif : `decisions.md`, 21/08 au soir.
func TestSetAccesSkillOuvertALAdmin(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	tiers, _ := s.DB.CreateUser("tiers", "mdp", perms.Invisible, false)
	// Le skill appartient à achille, pas à l'admin. Le claim est asserté : sans
	// ça, un claim raté ferait passer le test pour la mauvaise raison.
	s.Store.Write("shared/skills/sien/SKILL.md", contenuSkill("sien", "Skill d'achille."), "achille")
	if claimed, err := s.DB.ClaimSkill("sien", "shared/skills/sien", achille.ID); !claimed || err != nil {
		t.Fatalf("ClaimSkill : claimed=%v err=%v", claimed, err)
	}

	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"sien"}, "user_id": {strconv.FormatInt(tiers.ID, 10)}, "niveau": {"ecriture"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("admin non-créateur : attendu 303, obtenu %d", rec.Code)
	}
	rules, _ := s.DB.Rules(tiers.ID)
	if !perms.CanRead("shared/skills/sien/SKILL.md", perms.Invisible, rules) {
		t.Error("l'accès posé par l'administrateur n'a pas pris")
	}
}

// TestSetAccesSkillRefuseUnTiersNonCreateur : ce que l'exemption N'A PAS ouvert.
// Un compte ordinaire qui n'est pas le créateur reste refusé en 404, avant toute
// autre validation. L'exemption est réservée aux administrateurs et à eux seuls.
func TestSetAccesSkillRefuseUnTiersNonCreateur(t *testing.T) {
	s := newServer(t)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.DB.CreateUser("curieux", "mdp", perms.Lecture, false)
	tiers, _ := s.DB.CreateUser("tiers", "mdp", perms.Invisible, false)
	s.Store.Write("shared/skills/sien/SKILL.md", contenuSkill("sien", "Skill d'achille."), "achille")
	if claimed, err := s.DB.ClaimSkill("sien", "shared/skills/sien", achille.ID); !claimed || err != nil {
		t.Fatalf("ClaimSkill : claimed=%v err=%v", claimed, err)
	}

	h := s.Handler()
	c := login(t, h, "curieux", "mdp")

	rec := postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"sien"}, "user_id": {strconv.FormatInt(tiers.ID, 10)}, "niveau": {"ecriture"},
	}, c)
	if rec.Code != http.StatusNotFound {
		t.Errorf("tiers non-créateur : attendu 404, obtenu %d", rec.Code)
	}
	// Le refus doit être réel, pas cosmétique : aucune règle posée.
	rules, _ := s.DB.Rules(tiers.ID)
	if perms.CanRead("shared/skills/sien/SKILL.md", perms.Invisible, rules) {
		t.Error("un tiers non-créateur a tout de même posé l'accès")
	}
}

// TestSetAccesSkillOuvertAuCreateurNonAdmin : le pendant de la fermeture. La
// délégation livrée par F3 devient exerçable depuis le web - c'était tout
// l'objet du chantier.
func TestSetAccesSkillOuvertAuCreateurNonAdmin(t *testing.T) {
	s := newServer(t)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	tiers, _ := s.DB.CreateUser("tiers", "mdp", perms.Invisible, false)
	s.Store.Write("shared/skills/sien/SKILL.md", contenuSkill("sien", "Skill d'achille."), "achille")
	s.DB.ClaimSkill("sien", "shared/skills/sien", achille.ID)

	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	rec := postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"sien"}, "user_id": {strconv.FormatInt(tiers.ID, 10)}, "niveau": {"lecture"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("créateur non-admin : attendu 303, obtenu %d", rec.Code)
	}
	rules, _ := s.DB.Rules(tiers.ID)
	if !perms.CanRead("shared/skills/sien/SKILL.md", perms.Invisible, rules) {
		t.Error("le créateur non-admin n'a pas pu partager son propre skill")
	}

	// « Donner ET retirer » : le retour à invisible est l'autre moitié du geste.
	rec = postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"sien"}, "user_id": {strconv.FormatInt(tiers.ID, 10)}, "niveau": {"invisible"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait par le créateur : attendu 303, obtenu %d", rec.Code)
	}
	rules, _ = s.DB.Rules(tiers.ID)
	if perms.CanRead("shared/skills/sien/SKILL.md", perms.Invisible, rules) {
		t.Error("le créateur non-admin n'a pas pu retirer l'accès à son propre skill")
	}
}

// TestAccesSkillEnUnClic : le geste central. Un POST = une règle posée sur
// shared/skills/<slug> = le skill entre (ou sort) du périmètre de la personne,
// sans toucher au reste de shared/.
func TestAccesSkillEnUnClic(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.Store.Write("shared/skills/dev-scope/SKILL.md", contenuSkill("dev-scope", "Scoper un dev."), "colin")
	s.Store.Write("shared/skills/daily/SKILL.md", contenuSkill("daily", "Briefing matinal."), "colin")
	// La gestion des accès est réservée au créateur : colin possède les deux.
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	s.DB.ClaimSkill("daily", "shared/skills/daily", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	id := strconv.FormatInt(achille.ID, 10)

	voit := func(path string) bool {
		t.Helper()
		rules, _ := s.DB.Rules(achille.ID)
		a, _ := s.DB.UserByID(achille.ID)
		return perms.CanRead(path, a.DefaultLevel, rules)
	}

	if voit("shared/skills/dev-scope/SKILL.md") {
		t.Fatal("achille voit dev-scope avant tout accès")
	}
	rec := postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"dev-scope"}, "user_id": {id}, "niveau": {"lecture"},
	}, c)
	// La redirection revient SUR le panneau du skill : après un POST, le
	// navigateur repart en haut d'une page de 220 Ko, où le geste ne se voit pas.
	if rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") != "/admin/skills?ok=dev-scope#partage-dev-scope" {
		t.Fatalf("pose d'accès : code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	if !voit("shared/skills/dev-scope/SKILL.md") {
		t.Error("après l'accès, achille ne voit toujours pas dev-scope")
	}
	// Un seul skill accordé : le voisin daily reste invisible.
	if voit("shared/skills/daily/SKILL.md") {
		t.Error("accorder dev-scope a exposé daily (contrôle par skill non isolé)")
	}

	// Retrait : un clic, et le skill se referme.
	postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"dev-scope"}, "user_id": {id}, "niveau": {"invisible"},
	}, c)
	if voit("shared/skills/dev-scope/SKILL.md") {
		t.Error("après retrait, achille voit encore dev-scope")
	}
}

// TestAccesSkillRetourFiche : depuis la fiche d'un compte, le bouton renvoie sur
// la fiche ; une destination arbitraire du formulaire est ignorée.
func TestAccesSkillRetourFiche(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.Store.Write("shared/skills/dev-scope/SKILL.md", contenuSkill("dev-scope", "Scoper."), "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	id := strconv.FormatInt(achille.ID, 10)

	rec := postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"dev-scope"}, "user_id": {id}, "niveau": {"lecture"}, "retour": {"user"},
	}, c)
	if got := rec.Header().Get("Location"); got != "/admin/users/"+id {
		t.Errorf("redirection = %q, attendu /admin/users/%s", got, id)
	}
	rec = postForm(h, "/admin/skills/acces", url.Values{
		"slug": {"dev-scope"}, "user_id": {id}, "niveau": {"lecture"},
		"retour": {"https://exemple.invalide/phishing"},
	}, c)
	// La destination est fabriquée par le code, jamais reçue du formulaire.
	if got := rec.Header().Get("Location"); !strings.HasPrefix(got, "/admin/skills?") ||
		strings.Contains(got, "exemple.invalide") {
		t.Errorf("redirection ouverte : %q", got)
	}
	// La fiche affiche la vue miroir des skills (parité avec la section Espaces).
	body := get(h, "/admin/users/"+id, c).Body.String()
	if !strings.Contains(body, "<h2>Skills</h2>") || !strings.Contains(body, "dev-scope") {
		t.Error("la fiche utilisateur n'affiche pas la vue miroir des skills")
	}
}

func TestAccesSkillEntreesInvalides(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	// Créateur du slug testé : sans ça le refus d'autorisation (404) masquerait
	// la validation des entrées qu'on veut éprouver ici.
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	cas := []url.Values{
		{"slug": {"dev-scope"}, "user_id": {"9999"}, "niveau": {"lecture"}}, // compte inexistant
		{"slug": {"dev-scope"}, "user_id": {"abc"}, "niveau": {"lecture"}},  // id non numérique
		{"slug": {"dev-scope"}, "user_id": {"1"}, "niveau": {"admin"}},      // niveau inventé
		{"slug": {"../evasion"}, "user_id": {"1"}, "niveau": {"lecture"}},   // slug d'évasion
		{"slug": {"équipe"}, "user_id": {"1"}, "niveau": {"lecture"}},       // slug accentué
		{"slug": {""}, "user_id": {"1"}, "niveau": {"lecture"}},
	}
	for _, form := range cas {
		if rec := postForm(h, "/admin/skills/acces", form, c); rec.Code != http.StatusBadRequest {
			t.Errorf("%v : attendu 400, obtenu %d", form, rec.Code)
		}
	}
}

// TestMatriceSkills : la page affiche name/description, le compteur de fichiers,
// et signale un skill non projetable avec sa raison.
func TestMatriceSkills(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("shared/skills/dev-scope/SKILL.md", contenuSkill("dev-scope", "Scoper un dev technique."), "colin")
	s.Store.Write("shared/skills/dev-scope/reference.md", "ref", "colin")
	// Skill sans description : présent mais non projetable.
	s.Store.Write("shared/skills/brut/SKILL.md", "# pas de frontmatter", "colin")
	// Sous le modèle « privé par défaut », l'admin ne voit un skill que s'il le
	// possède : on l'attribue (comme le fait le backfill au démarrage).
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	s.DB.ClaimSkill("brut", "shared/skills/brut", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	body := get(h, "/admin/skills", c).Body.String()
	if !strings.Contains(body, "dev-scope") || !strings.Contains(body, "Scoper un dev technique.") {
		t.Error("la matrice n'affiche pas le slug/description du skill")
	}
	if !strings.Contains(body, "Non projeté") {
		t.Error("un skill non projetable n'est pas signalé (raison absente)")
	}
	// La colonne créateur est présente et affiche le propriétaire (colin a
	// revendiqué les deux skills ci-dessus).
	if !strings.Contains(body, "Créateur") {
		t.Error("la colonne Créateur est absente de la matrice")
	}
}

// TestSkillInvisibleAbsentDeLaMatrice : un skill rendu invisible pour l'admin
// courant (peu probable, mais le modèle doit tenir) n'apparaît pas dans SA vue.
// Surtout : un skill invisible pour un membre n'entre pas dans son périmètre.
func TestSkillInvisibleAbsentPourMembre(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.Store.Write("shared/skills/secret/SKILL.md", contenuSkill("secret", "Interne."), "colin")
	// achille lit shared par défaut, mais ce skill précis lui est masqué.
	s.DB.SetPermission(achille.ID, "shared/skills/secret", perms.Invisible)

	rules, _ := s.DB.Rules(achille.ID)
	if perms.CanRead("shared/skills/secret/SKILL.md", perms.Lecture, rules) {
		t.Error("un skill marqué invisible reste lisible pour le membre")
	}
}

// TestLeGesteJSONNexistePlus : `POST /admin/skills/acces.json` est retiré.
//
// Il servait la mise à jour sur place de la matrice des skills, remplacée le
// 22/08 par un panneau qui s'enregistre en une fois. Le script qui l'appelait
// est parti avec la matrice ; l'endpoint est resté sans appelant une journée,
// puis a été retiré sur décision de Colin. Une surface d'écriture qu'aucun
// écran n'utilise ne se protège que par le souvenir qu'elle existe.
//
// Ce qu'il tenait est tenu ailleurs : l'autorisation par valideBascule, que la
// bascule unitaire et le panneau appellent tous les deux.
func TestLeGesteJSONNexistePlus(t *testing.T) {
	s := newServer(t)
	ids, h, c := prepareSkill(t, s, map[string]perms.Level{"achille": perms.Invisible})
	chemin := skillPath("dev-scope")

	rec := postForm(h, "/admin/skills/acces.json", url.Values{
		"slug": {"dev-scope"}, "user_id": {strconv.FormatInt(ids["achille"], 10)},
		"niveau": {"ecriture"},
	}, c)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /admin/skills/acces.json : attendu 404, obtenu %d", rec.Code)
	}
	if n := regleSur(t, s, ids["achille"], chemin); n != "" {
		t.Errorf("l'endpoint retiré a quand même écrit : %q", n)
	}
}

// TestPanneauDePartage : le rendu du contrôle de partage, qui remplace la
// colonne par compte depuis le 22/08.
//
// Ce que ce test tient, et que la matrice tenait avant lui : les trois niveaux
// proposés, un seul retenu par compte, la forme qui porte l'état (donc lisible
// en niveaux de gris), le libellé accessible, et le geste faisable sans une
// ligne de JavaScript.
func TestPanneauDePartage(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.Store.Write("shared/skills/dev-scope/SKILL.md", contenuSkill("dev-scope", "Scoper."), "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	s.DB.SetPermission(achille.ID, "shared/skills/dev-scope", perms.Lecture)

	h := s.Handler()
	body := get(h, "/admin/skills", login(t, h, "colin", "mdp")).Body.String()

	// Les trois niveaux sont proposés, pour chacun des deux comptes.
	for _, niveau := range []string{"invisible", "lecture", "ecriture"} {
		if !strings.Contains(body, `value="`+niveau+`"`) {
			t.Errorf("le niveau %q n'est pas proposé", niveau)
		}
	}
	// Exactement un niveau retenu par compte : deux comptes, un skill.
	if n := strings.Count(body, "checked"); n != 2 {
		t.Errorf("%d niveaux cochés, attendu 2 (un par compte)", n)
	}
	// Le résumé se lit sans déplier, et il dit vrai.
	if !strings.Contains(body, `class="resume"`) {
		t.Error("le résumé par niveau n'est pas rendu")
	}
	// L'état se lit à la FORME : l'œil barré porte un trait que l'œil simple n'a
	// pas, et le crayon est une silhouette distincte. Sans ça, les trois états ne
	// se distinguent qu'à la couleur - illisible en niveaux de gris.
	if !strings.Contains(body, "M2.5 13.5l11-11") {
		t.Error("l'état invisible n'a pas sa barre : il ne se distingue plus de lecture par la forme")
	}
	if !strings.Contains(body, "M2 14l.9-3.4") {
		t.Error("l'état écriture n'a pas son crayon")
	}
	// Sans JavaScript : <details> s'ouvre et le formulaire part tout seul.
	if !strings.Contains(body, `<form method="post" action="/admin/skills/partage">`) {
		t.Error("le formulaire de partage a disparu : le geste ne marcherait plus sans script")
	}
	// Le niveau est nommé à côté du radio : le geste ne dépend pas de l'icône.
	if !strings.Contains(body, "<span>écriture</span>") {
		t.Error("les niveaux n'ont pas de libellé lisible")
	}
}

// TestLaLargeurNeSuitPasLeNombreDeComptes : le retour de Colin du 21/08.
//
// La matrice donnait une COLONNE par compte et trois boutons par cellule : à
// dix comptes, trente boutons par ligne de skill et un écran illisible. Le
// tableau ne doit plus rien devoir au nombre de comptes.
func TestLaLargeurNeSuitPasLeNombreDeComptes(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	for i := 0; i < 9; i++ {
		s.DB.CreateUser(fmt.Sprintf("compte%d", i), "mdp", perms.Invisible, false)
	}
	s.Store.Write("shared/skills/dev-scope/SKILL.md", contenuSkill("dev-scope", "Scoper."), "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)

	h := s.Handler()
	body := get(h, "/admin/skills", login(t, h, "colin", "mdp")).Body.String()

	if !strings.Contains(body, "<th>Partage</th>") {
		t.Fatal("la colonne Partage n'est pas rendue")
	}
	// Aucun en-tête de colonne ne porte un nom de compte : c'est ce qui faisait
	// grandir le tableau. Les comptes vivent dans le panneau, en lignes.
	for i := 0; i < 9; i++ {
		if strings.Contains(body, fmt.Sprintf("<th>compte%d</th>", i)) {
			t.Fatalf("compte%d a encore une colonne à lui", i)
		}
	}
	// Et les dix comptes sont bien réglables, en ligne dans le panneau.
	if n := strings.Count(body, `name="niveau_`); n != 30 {
		t.Errorf("%d radios de partage, attendu 30 (10 comptes x 3 niveaux)", n)
	}
}

// TestAvertissementReglesInternesSansLienAdmin : la fiche d'un compte est
// admin-only. Proposer le lien à un créateur non-admin l'enverrait sur un 403.
func TestAvertissementReglesInternesSansLienAdmin(t *testing.T) {
	s := newServer(t)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	tiers, _ := s.DB.CreateUser("tiers", "mdp", perms.Lecture, false)
	s.Store.Write("shared/skills/sien/SKILL.md", contenuSkill("sien", "Skill d'achille."), "achille")
	s.DB.ClaimSkill("sien", "shared/skills/sien", achille.ID)
	// Une règle interne, qui déclenche l'avertissement.
	s.DB.SetPermission(tiers.ID, "shared/skills/sien/SKILL.md", perms.Ecriture)

	h := s.Handler()
	body := get(h, "/admin/skills", login(t, h, "achille", "mdp")).Body.String()

	// L'avertissement est signalé mais REPLIÉ (voir dossiers.html) : on teste la
	// balise et un fragment que le singulier comme le pluriel contiennent, pas la
	// phrase entière, que le template coupe par des retours à la ligne.
	if !strings.Contains(body, `<details class="internes">`) {
		t.Fatal("l'avertissement sur les règles internes n'est pas rendu")
	}
	if !strings.Contains(body, "sur ce panneau") {
		t.Fatal("le résumé ne dit pas que les règles internes l'emportent")
	}
	if strings.Contains(body, `href="/admin/users/`) {
		t.Error("un créateur non-admin se voit proposer un lien vers une page qui lui répondra 403")
	}
	if !strings.Contains(body, "Demandez à un administrateur") {
		t.Error("aucune issue n'est proposée au créateur non-admin")
	}
}

// TestFiltreRecherche : le champ de filtre et son index. Le filtrage lui-même
// est côté client ; ce qui se teste ici, c'est le contrat que le JavaScript
// consomme - et le fait que le champ n'apparaisse pas sans lui.
func TestFiltreRecherche(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("shared/skills/veille-reglementaire/SKILL.md",
		contenuSkill("veille-reglementaire", "Suivre la veille du secteur agroalimentaire."), "colin")
	s.DB.ClaimSkill("veille-reglementaire", "shared/skills/veille-reglementaire", colin.ID)

	h := s.Handler()
	body := get(h, "/admin/skills", login(t, h, "colin", "mdp")).Body.String()

	// Cachée à l'arrivée : sans JavaScript, une barre qui ne filtre rien serait
	// un piège. Le script la révèle.
	if !strings.Contains(body, `id="filtres" hidden`) {
		t.Error("la barre de filtres n'est pas cachée par défaut : elle serait inerte sans JavaScript")
	}
	// L'index porte les trois champs, dont le nom qui n'est affiché nulle part.
	if !strings.Contains(body,
		`data-recherche="veille-reglementaire veille-reglementaire Suivre la veille du secteur agroalimentaire.`) {
		t.Error("l'index de recherche ne porte pas slug + nom + description")
	}
	// La description est repliée : elle vit dans un <details> fermé, donc elle
	// ne prend pas de place tant qu'on ne la demande pas.
	if !strings.Contains(body, `<details class="skill">`) {
		t.Error("la description n'est pas repliée")
	}
	if strings.Contains(body, `<details class="skill" open`) {
		t.Error("un skill est déplié à l'arrivée")
	}
	if !strings.Contains(body, `id="aucun-resultat"`) {
		t.Error("aucun message n'est prévu quand le filtre ne laisse rien")
	}
}

// TestSkillsTagsAffichesEtFiltrables : un tag écrit dans le SKILL.md remonte sur
// la ligne, entre dans l'index de recherche, et donne une puce de filtre.
func TestSkillsTagsAffichesEtFiltrables(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	contenu := "---\nname: linkedin-post\ndescription: Rédiger un post.\nmetadata:\n  tags: [contenu, rédaction]\n---\n\n# corps\n"
	s.Store.Write("shared/skills/linkedin-post/SKILL.md", contenu, "colin")
	s.DB.ClaimSkill("linkedin-post", "shared/skills/linkedin-post", colin.ID)
	// Un second skill sans tag : la puce ne doit pas le retenir.
	s.Store.Write("shared/skills/dev-scope/SKILL.md",
		contenuSkill("dev-scope", "Scoper un dev."), "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)

	h := s.Handler()
	body := get(h, "/admin/skills", login(t, h, "colin", "mdp")).Body.String()

	if !strings.Contains(body, `data-tag="contenu"`) || !strings.Contains(body, `data-tag="rédaction"`) {
		t.Error("les puces de filtre ne portent pas les tags du dépôt")
	}
	// L'attribut qui sert au filtre par tag, séparé par « | ».
	if !strings.Contains(body, `data-tags="contenu|rédaction|"`) {
		t.Error("la ligne ne porte pas ses tags pour le filtre")
	}
	// Le skill sans tag porte un attribut vide, pas l'attribut du voisin.
	if !strings.Contains(body, `data-tags=""`) {
		t.Error("un skill sans tag devrait porter un attribut de tags vide")
	}
	// Les tags entrent aussi dans la recherche texte : taper « rédaction »
	// trouve le skill sans qu'on ait à cliquer une puce.
	if !strings.Contains(body, `data-recherche="linkedin-post linkedin-post Rédiger un post. contenu rédaction `) {
		t.Error("les tags n'entrent pas dans l'index de recherche")
	}
}

// TestSkillsTagsHorsPerimetreNeFuitPas : le filtre est dérivé des skills
// AFFICHÉS. Un tag qui n'existe que sur un skill invisible révélerait son
// existence.
func TestSkillsTagsHorsPerimetreNeFuitPas(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	secret := "---\nname: secret\ndescription: Confidentiel.\nmetadata:\n  tags: [strategie-2027]\n---\n"
	s.Store.Write("shared/skills/secret/SKILL.md", secret, "colin")
	s.DB.ClaimSkill("secret", "shared/skills/secret", colin.ID)
	if err := s.DB.SetPermission(achille.ID, "shared/skills/secret", perms.Invisible); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}

	h := s.Handler()
	body := get(h, "/admin/skills", login(t, h, "achille", "mdp")).Body.String()
	if strings.Contains(body, "strategie-2027") {
		t.Error("un tag d'un skill hors périmètre apparaît dans le filtre")
	}
}

// --- Le partage complet, en une requête (POST /admin/skills/partage) ---------

// prepareSkill : un skill de colin, colin admin, plus les comptes demandés.
func prepareSkill(t *testing.T, s *Server, defauts map[string]perms.Level) (map[string]int64, http.Handler, *http.Cookie) {
	t.Helper()
	ids := map[string]int64{}
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	ids["colin"] = colin.ID
	for nom, def := range defauts {
		u, err := s.DB.CreateUser(nom, "mdp", def, false)
		if err != nil {
			t.Fatalf("CreateUser(%s) : %v", nom, err)
		}
		ids[nom] = u.ID
	}
	s.Store.Write("shared/skills/dev-scope/SKILL.md", contenuSkill("dev-scope", "Scoper."), "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	h := s.Handler()
	return ids, h, login(t, h, "colin", "mdp")
}

func TestPartageCompletPoseLesNiveaux(t *testing.T) {
	s := newServer(t)
	ids, h, c := prepareSkill(t, s, map[string]perms.Level{"achille": perms.Invisible})

	rec := postForm(h, "/admin/skills/partage", url.Values{
		"slug": {"dev-scope"},
		"niveau_" + strconv.FormatInt(ids["colin"], 10):   {"ecriture"},
		"niveau_" + strconv.FormatInt(ids["achille"], 10): {"lecture"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("partage : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	regles, _ := s.DB.Rules(ids["achille"])
	if !perms.CanRead("shared/skills/dev-scope/SKILL.md", perms.Invisible, regles) {
		t.Error("le skill n'est pas devenu lisible pour achille")
	}
	if perms.CanWrite("shared/skills/dev-scope/SKILL.md", perms.Invisible, regles) {
		t.Error("la lecture a donné l'écriture")
	}
}

// TestPartageNecritRienSiUnSeulNiveauEstInvalide : l'assignation est atomique.
//
// Le compte valide est traité AVANT l'invalide dans l'ordre de la liste : une
// implémentation qui écrit au fil de la lecture laisserait un partage à moitié
// posé, sans que l'écran puisse dire lequel.
func TestPartageNecritRienSiUnSeulNiveauEstInvalide(t *testing.T) {
	s := newServer(t)
	ids, h, c := prepareSkill(t, s, map[string]perms.Level{"achille": perms.Invisible})
	chemin := skillPath("dev-scope")

	// colin est traité AVANT achille dans l'ordre de la liste : une
	// implémentation qui écrit au fil de la lecture laisserait son niveau posé.
	rec := postForm(h, "/admin/skills/partage", url.Values{
		"slug": {"dev-scope"},
		"niveau_" + strconv.FormatInt(ids["colin"], 10):   {"invisible"},
		"niveau_" + strconv.FormatInt(ids["achille"], 10): {"root"},
	}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("niveau invalide : attendu 400, obtenu %d", rec.Code)
	}
	if n := regleSur(t, s, ids["colin"], chemin); n != "ecriture" {
		t.Errorf("le compte valide a été écrit malgré le refus : %q", n)
	}
}

// TestPartageNecritPasDeRegleInutile : un compte dont on ne change rien ne
// reçoit pas de règle.
//
// Enregistrer l'assignation complète écrirait naïvement une règle par compte,
// y compris pour ceux qu'on n'a pas touchés. Leur droit sur ce skill cesserait
// alors de suivre ce dont il dépendait - leur niveau par défaut, ou une règle
// posée plus haut. Invisible le jour même, faux pour toujours après.
func TestPartageNecritPasDeRegleInutile(t *testing.T) {
	s := newServer(t)
	ids, h, c := prepareSkill(t, s, map[string]perms.Level{"achille": perms.Lecture})
	chemin := skillPath("dev-scope")

	// Un compte neuf ne voit aucun skill : la règle « shared/skills invisible »
	// posée à sa création le lui ferme. C'est donc « invisible » que le
	// formulaire a rendu pour lui, et c'est ce qu'on lui renvoie.
	rec := postForm(h, "/admin/skills/partage", url.Values{
		"slug": {"dev-scope"},
		"niveau_" + strconv.FormatInt(ids["colin"], 10):   {"ecriture"},
		"niveau_" + strconv.FormatInt(ids["achille"], 10): {"invisible"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("partage inchangé : attendu 303, obtenu %d", rec.Code)
	}
	if n := regleSur(t, s, ids["achille"], chemin); n != "" {
		t.Fatalf("une règle a été posée sans qu'on change quoi que ce soit : %q", n)
	}
	// La preuve que rien n'a été figé : ouvrir la racine des skills à achille
	// lui ouvre celui-ci. Une règle « invisible » posée sur le skill, elle,
	// l'emporterait sur cette ouverture et le laisserait dehors.
	if err := s.DB.SetPermission(ids["achille"], "shared/skills", perms.Lecture); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	regles, _ := s.DB.Rules(ids["achille"])
	if !perms.CanRead("shared/skills/dev-scope/SKILL.md", perms.Lecture, regles) {
		t.Error("le skill est resté fermé : une règle a bien été figée sur son chemin")
	}
}

// TestPartageRefuseQuiNeGerePasLeSkill : même borne que la bascule unitaire, et
// pour la même raison - on ne révèle pas l'existence d'un skill qu'on ne gère
// pas, donc 404 et pas 403.
func TestPartageRefuseQuiNeGerePasLeSkill(t *testing.T) {
	s := newServer(t)
	ids, h, _ := prepareSkill(t, s, map[string]perms.Level{"achille": perms.Lecture})
	c := login(t, h, "achille", "mdp")
	chemin := skillPath("dev-scope")

	rec := postForm(h, "/admin/skills/partage", url.Values{
		"slug": {"dev-scope"},
		"niveau_" + strconv.FormatInt(ids["colin"], 10): {"invisible"},
	}, c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("partage par un tiers : attendu 404, obtenu %d", rec.Code)
	}
	if n := regleSur(t, s, ids["colin"], chemin); n != "ecriture" {
		t.Errorf("un tiers a réussi à écrire : %q", n)
	}
}

// regleSur : le niveau de la règle posée EXACTEMENT sur ce chemin, "" s'il n'y
// en a pas. Distinct du droit effectif, qui peut venir d'un chemin parent.
func regleSur(t *testing.T, s *Server, userID int64, chemin string) string {
	t.Helper()
	regles, err := s.DB.Rules(userID)
	if err != nil {
		t.Fatalf("Rules : %v", err)
	}
	for _, r := range regles {
		if r.Path == chemin {
			return r.Level.String()
		}
	}
	return ""
}

// TestLeRetourSeVoit : après un geste, la page revient à l'endroit du geste et
// dit que ça a marché.
//
// C'est le cœur du retour de Colin sur les groupes : le clic écrivait bien,
// mais rechargeait une page de 220 Ko en repartant du haut, sans un mot. Un
// geste réussi et un geste sans effet se ressemblaient exactement.
func TestLeRetourSeVoit(t *testing.T) {
	s := newServer(t)
	ids, h, c := prepareSkill(t, s, map[string]perms.Level{"achille": perms.Invisible})

	rec := postForm(h, "/admin/skills/partage", url.Values{
		"slug": {"dev-scope"},
		"niveau_" + strconv.FormatInt(ids["achille"], 10): {"lecture"},
	}, c)
	loc := rec.Header().Get("Location")
	if loc != "/admin/skills?ok=dev-scope#partage-dev-scope" {
		t.Fatalf("retour = %q", loc)
	}
	// Et la page servie à cette adresse ouvre le panneau, avec le message.
	// Un navigateur n'envoie JAMAIS le fragment au serveur : on demande donc
	// ce qu'il demanderait, chemin et requête seulement.
	page := get(h, strings.SplitN(loc, "#", 2)[0], c).Body.String()
	if !strings.Contains(page, `id="partage-dev-scope" open`) {
		t.Error("le panneau concerné ne s'ouvre pas au retour")
	}
	if !strings.Contains(page, `class="ok"`) {
		t.Error("rien ne dit que le geste a abouti")
	}
}

// TestConfirmationFabriqueeIgnoree : `ok` n'est pas réfléchi tel quel.
//
// Sans ce contrôle, un lien fabriqué ferait dire à l'écran qu'un partage a été
// enregistré alors que rien ne l'a été - et sur un slug inventé, il apprendrait
// au passage que ce skill n'existe pas.
func TestConfirmationFabriqueeIgnoree(t *testing.T) {
	s := newServer(t)
	_, h, c := prepareSkill(t, s, map[string]perms.Level{"achille": perms.Invisible})

	page := get(h, "/admin/skills?ok=skill-invente", c).Body.String()
	if strings.Contains(page, `class="ok"`) {
		t.Error("une confirmation s'affiche pour un skill qui n'existe pas")
	}
	if strings.Contains(page, "skill-invente") {
		t.Error("le paramètre est réfléchi dans la page")
	}
}
