package web

import (
	"fmt"
	"os"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// TestDumpEcran : rendre la page des skills dans un fichier, pour la REGARDER.
//
// Ne fait rien sans `DUMP=/chemin/page.html`. Il existe parce que les défauts
// qui comptent sur cet écran ne sont jamais sortis d'un test : le panneau qui
// ne se peignait plus, le `<button>` dans un `<a>`, les niveaux sans accent,
// la colonne qui poussait le tableau hors de la fenêtre. Une suite verte ne
// dit rien de ce qui se voit.
//
//	DUMP=/tmp/skills.html go test ./server/web/ -run TestDumpEcran -count=1
func TestDumpEcran(t *testing.T) {
	dest := os.Getenv("DUMP")
	if dest == "" {
		t.Skip("DUMP non défini")
	}
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	marie, _ := s.DB.CreateUser("marie", "mdp", perms.Invisible, false)
	noms := []string{"linkedin-post", "dev-scope", "dev-build", "daily", "weekly",
		"meeting", "propale", "ingest", "harvest", "brainstorming"}
	for _, slug := range noms {
		s.Store.Write("shared/skills/"+slug+"/SKILL.md",
			contenuSkillTags(slug, "Ce que fait "+slug+", en une phrase de longueur ordinaire.",
				[]string{"contenu", "client"}), "colin")
		s.DB.ClaimSkill(slug, "shared/skills/"+slug, colin.ID)
	}
	s.DB.SetPermission(achille.ID, skillPath("linkedin-post"), perms.Ecriture)
	s.DB.SetPermission(marie.ID, skillPath("linkedin-post"), perms.Lecture)
	g1, _ := s.DB.CreerGroupe("Équipe contenu")
	s.DB.SetMembreGroupe(g1, achille.ID, perms.Lecture)
	s.DB.RangeChemin(g1, skillPath("daily"), "skill", "daily")
	s.DB.RangeChemin(g1, skillPath("weekly"), "skill", "weekly")
	s.DB.CreerGroupe("Vécu")

	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	body := get(h, "/admin/skills?ok=linkedin-post", c).Body.String()
	if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fmt.Println("écrit :", dest, len(body), "octets")
}

func contenuSkillTags(name, desc string, tags []string) string {
	s := "---\nname: " + name + "\ndescription: " + desc + "\nmetadata:\n  tags:\n"
	for _, t := range tags {
		s += "    - " + t + "\n"
	}
	return s + "---\n\n# " + name + "\n\nCorps du skill.\n"
}
