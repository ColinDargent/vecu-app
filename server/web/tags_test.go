package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// contenuAvecTags : un SKILL.md complet, tags posés en tête.
func contenuAvecTags(slug, desc, tags string) string {
	s := "---\nname: " + slug + "\ndescription: " + desc + "\n"
	if tags != "" {
		s += "metadata:\n  tags: [" + tags + "]\n"
	}
	return s + "---\n\n# corps\n"
}

func TestTagsEcritsDansLeSkillMD(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("shared/skills/linkedin-post/SKILL.md",
		contenuAvecTags("linkedin-post", "Rédiger un post.", ""), "colin")
	s.DB.ClaimSkill("linkedin-post", "shared/skills/linkedin-post", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := postForm(h, "/admin/skills/tags", url.Values{
		"slug": {"linkedin-post"}, "tags": {"contenu, rédaction"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("pose de tags : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	apres, err := s.Store.Read("shared/skills/linkedin-post/SKILL.md", "")
	if err != nil {
		t.Fatalf("relecture : %v", err)
	}
	if !strings.Contains(apres, "tags: [contenu, rédaction]") {
		t.Errorf("les tags ne sont pas dans le SKILL.md :\n%s", apres)
	}
	// Le reste du fichier est intact : c'est la promesse qui compte.
	if !strings.Contains(apres, "description: Rédiger un post.") || !strings.Contains(apres, "# corps") {
		t.Errorf("le fichier a été abîmé :\n%s", apres)
	}
	// Une version est née, donc le geste est restaurable comme un autre.
	log, err := s.Store.Log("shared/skills/linkedin-post/SKILL.md")
	if err != nil || len(log) < 2 {
		t.Errorf("aucune version créée par l'écriture des tags (%d, %v)", len(log), err)
	}
	// Et la page les affiche.
	if body := get(h, "/admin/skills", c).Body.String(); !strings.Contains(body, `data-tag="rédaction"`) {
		t.Error("le tag posé n'apparaît pas dans le filtre")
	}
}

// TestTagsExigentLeDroitDEcriture : les tags s'écrivent DANS le fichier. Un
// compte en lecture seule ne peut pas les changer - et le formulaire ne lui est
// même pas servi, pour ne pas proposer un geste qui sera refusé.
func TestTagsExigentLeDroitDEcriture(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	avant := contenuAvecTags("dev-scope", "Scoper un dev.", "dev")
	s.Store.Write("shared/skills/dev-scope/SKILL.md", avant, "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	// Partagé en LECTURE : sans ce geste, F3 le garde privé et achille reçoit un
	// 404 avant même la question du droit d'écriture. C'est ici la lecture seule
	// qu'on veut éprouver, pas l'invisibilité.
	if err := s.DB.SetPermission(achille.ID, "shared/skills/dev-scope", perms.Lecture); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	rec := postForm(h, "/admin/skills/tags", url.Values{
		"slug": {"dev-scope"}, "tags": {"pirate"},
	}, c)
	if rec.Code != http.StatusForbidden {
		t.Errorf("lecture seule : attendu 403, obtenu %d", rec.Code)
	}
	if apres, _ := s.Store.Read("shared/skills/dev-scope/SKILL.md", ""); apres != avant {
		t.Errorf("le fichier a changé malgré le refus :\n%s", apres)
	}
	if body := get(h, "/admin/skills", c).Body.String(); strings.Contains(body, `action="/admin/skills/tags"`) {
		t.Error("le formulaire de tags est servi à un compte en lecture seule")
	}
}

// TestTagsSkillInvisibleEst404 : on ne révèle pas l'existence d'un skill hors
// périmètre, même en refusant.
func TestTagsSkillInvisibleEst404(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.Store.Write("shared/skills/secret/SKILL.md", contenuAvecTags("secret", "Confidentiel.", ""), "colin")
	s.DB.ClaimSkill("secret", "shared/skills/secret", colin.ID)
	if err := s.DB.SetPermission(achille.ID, "shared/skills/secret", perms.Invisible); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	if rec := postForm(h, "/admin/skills/tags", url.Values{
		"slug": {"secret"}, "tags": {"x"},
	}, c); rec.Code != http.StatusNotFound {
		t.Errorf("skill invisible : attendu 404, obtenu %d", rec.Code)
	}
}

func TestTagsEntreesInvalides(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	avant := contenuAvecTags("dev-scope", "Scoper un dev.", "dev")
	s.Store.Write("shared/skills/dev-scope/SKILL.md", avant, "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	cas := []url.Values{
		{"slug": {"dev-scope"}, "tags": {"*alias"}},                                                             // casserait un parseur YAML
		{"slug": {"dev-scope"}, "tags": {"a:b"}},                                                                // idem
		{"slug": {"dev-scope"}, "tags": {"un deux trois quatre cinq six sept huit neuf dix onze douze treize"}}, // trop
		{"slug": {"../evasion"}, "tags": {"x"}},
		{"slug": {""}, "tags": {"x"}},
	}
	for _, form := range cas {
		rec := postForm(h, "/admin/skills/tags", form, c)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%v : attendu 400, obtenu %d", form, rec.Code)
		}
	}
	if apres, _ := s.Store.Read("shared/skills/dev-scope/SKILL.md", ""); apres != avant {
		t.Errorf("un refus a quand même écrit :\n%s", apres)
	}
}

// TestTagsRetraitComplet : vider le champ retire les tags, et le bloc metadata
// avec eux s'il n'était là que pour ça.
func TestTagsRetraitComplet(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("shared/skills/dev-scope/SKILL.md",
		contenuAvecTags("dev-scope", "Scoper un dev.", "dev, interne"), "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	if rec := postForm(h, "/admin/skills/tags", url.Values{
		"slug": {"dev-scope"}, "tags": {"  "},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait : attendu 303, obtenu %d", rec.Code)
	}
	apres, _ := s.Store.Read("shared/skills/dev-scope/SKILL.md", "")
	if strings.Contains(apres, "tags:") || strings.Contains(apres, "metadata:") {
		t.Errorf("les tags ou le bloc metadata survivent :\n%s", apres)
	}
	if !strings.Contains(apres, "description: Scoper un dev.") {
		t.Errorf("le retrait a emporté autre chose :\n%s", apres)
	}
}

// TestTagsDoublonsIgnores : « contenu, Contenu » ne pose qu'un tag. Un doublon
// est une faute de frappe, pas une erreur à renvoyer au visage.
func TestTagsDoublonsIgnores(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("shared/skills/dev-scope/SKILL.md",
		contenuAvecTags("dev-scope", "Scoper un dev.", ""), "colin")
	s.DB.ClaimSkill("dev-scope", "shared/skills/dev-scope", colin.ID)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	postForm(h, "/admin/skills/tags", url.Values{
		"slug": {"dev-scope"}, "tags": {"contenu, Contenu, contenu"},
	}, c)
	apres, _ := s.Store.Read("shared/skills/dev-scope/SKILL.md", "")
	if !strings.Contains(apres, "tags: [contenu]") {
		t.Errorf("les doublons ne sont pas fondus :\n%s", apres)
	}
}
