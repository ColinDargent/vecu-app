package skills

import (
	"fmt"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

const root = "shared/skills"

// dépôt de référence : deux skills complets, un sans SKILL.md, un au slug
// invalide, un fichier posé à la racine des skills, un fichier hors root.
var paths = []string{
	"shared/skills/dev-scope/SKILL.md",
	"shared/skills/dev-scope/reference.md",
	"shared/skills/linkedin-post/SKILL.md",
	"shared/skills/sans-md/autre.md",
	"shared/skills/équipe/SKILL.md", // slug invalide (accent) : ignoré
	"shared/skills/README.md",       // fichier direct dans root : pas un skill
	"shared/note.md",                // hors root
}

var contenus = map[string]string{
	"shared/skills/dev-scope/SKILL.md":     "---\nname: dev-scope\ndescription: Scoper un dev technique.\n---\n\n# corps",
	"shared/skills/linkedin-post/SKILL.md": "---\nname: linkedin-post\ndescription: \"Rediger un post LinkedIn.\"\n---\n",
}

func lire(p string) (string, error) {
	c, ok := contenus[p]
	if !ok {
		return "", fmt.Errorf("introuvable : %s", p)
	}
	return c, nil
}

func parSlug(list []Info) map[string]Info {
	out := map[string]Info{}
	for _, s := range list {
		out[s.Slug] = s
	}
	return out
}

func TestListAdminVoitLesSkillsValides(t *testing.T) {
	got := parSlug(List(root, paths, perms.Ecriture, nil, lire))
	// dev-scope, linkedin-post, sans-md. Pas équipe (slug invalide), pas README
	// (fichier direct), pas note.md (hors root).
	if len(got) != 3 {
		t.Fatalf("skills = %v, attendu 3 (dev-scope, linkedin-post, sans-md)", keys(got))
	}
	if _, vu := got["équipe"]; vu {
		t.Error("slug invalide « équipe » listé")
	}
	if _, vu := got["README"]; vu {
		t.Error("fichier README.md posé dans root compté comme skill")
	}
}

func TestListFrontmatterParse(t *testing.T) {
	got := parSlug(List(root, paths, perms.Ecriture, nil, lire))
	ds := got["dev-scope"]
	if ds.Name != "dev-scope" || ds.Description != "Scoper un dev technique." {
		t.Errorf("dev-scope frontmatter = (%q, %q)", ds.Name, ds.Description)
	}
	if !ds.Projetable || ds.Raison != "" {
		t.Errorf("dev-scope devrait être projetable, got projetable=%v raison=%q", ds.Projetable, ds.Raison)
	}
	if ds.Fichiers != 2 {
		t.Errorf("dev-scope fichiers = %d, attendu 2 (SKILL.md + reference.md)", ds.Fichiers)
	}
	if !ds.Ecriture {
		t.Error("dev-scope devrait être en écriture (défaut Ecriture)")
	}
	// Description entre guillemets doubles : dequote.
	if lp := got["linkedin-post"]; lp.Description != "Rediger un post LinkedIn." {
		t.Errorf("linkedin-post description = %q (guillemets non retirés ?)", lp.Description)
	}
}

func TestListSkillSansSkillMD(t *testing.T) {
	got := parSlug(List(root, paths, perms.Ecriture, nil, lire))
	s := got["sans-md"]
	if s.Name != "sans-md" {
		t.Errorf("name = %q, attendu fallback sur le slug", s.Name)
	}
	if s.Projetable {
		t.Error("un skill sans SKILL.md ne doit pas être projetable")
	}
	if s.Raison == "" {
		t.Error("un skill non projetable doit dire pourquoi (Raison)")
	}
}

// TestListFrontmatterMalforme : un SKILL.md présent mais sans frontmatter
// exploitable (donc sans description). name retombe sur le slug, et le skill est
// NON projetable avec une raison : sans description, Claude ne saurait pas quand
// le déclencher, un symlink projeté serait mensonger.
func TestListFrontmatterMalforme(t *testing.T) {
	p := []string{"shared/skills/brut/SKILL.md"}
	c := map[string]string{"shared/skills/brut/SKILL.md": "# Pas de frontmatter\n\ndu texte"}
	read := func(x string) (string, error) { return c[x], nil }
	got := parSlug(List(root, p, perms.Ecriture, nil, read))
	s := got["brut"]
	if s.Name != "brut" {
		t.Errorf("name = %q, attendu fallback slug", s.Name)
	}
	if s.Description != "" {
		t.Errorf("description = %q, attendu vide", s.Description)
	}
	if s.Projetable {
		t.Error("un SKILL.md sans description ne doit pas être projetable")
	}
	if s.Raison == "" {
		t.Error("un skill non projetable doit dire pourquoi (Raison)")
	}
}

// TestListSkillMDAbsentLectureInerte : un dossier de skill sans SKILL.md, mais
// avec un autre fichier lisible, ne doit PAS être déclaré projetable - même si le
// lecteur `read` renvoie ("", nil) pour un chemin absent (au lieu d'une erreur).
// L'existence du SKILL.md se juge sur la liste des chemins, jamais sur un droit
// ni sur la valeur de retour du lecteur. Verrou du bug P2 (doubt gate slice 1).
func TestListSkillMDAbsentLectureInerte(t *testing.T) {
	p := []string{"shared/skills/foo/guide.md"} // pas de SKILL.md dans le dépôt
	appels := 0
	read := func(string) (string, error) { appels++; return "", nil } // lecteur « inerte »
	got := parSlug(List(root, p, perms.Ecriture, nil, read))
	s := got["foo"]
	if s.Fichiers != 1 {
		t.Fatalf("foo devrait être listé (guide.md lisible), fichiers=%d", s.Fichiers)
	}
	if s.Projetable {
		t.Error("skill sans SKILL.md déclaré projetable via un read inerte")
	}
	if appels != 0 {
		t.Errorf("read() appelé %d fois sur un SKILL.md absent, attendu 0", appels)
	}
}

// TestListSkillInvisibleAbsent : un skill rendu invisible pour un utilisateur
// (règle perms sur son dossier) ne doit apparaître nulle part pour lui.
func TestListSkillInvisibleAbsent(t *testing.T) {
	rules := []perms.Rule{
		{Path: "shared", Level: perms.Lecture},
		{Path: "shared/skills/dev-scope", Level: perms.Invisible},
	}
	got := parSlug(List(root, paths, perms.Lecture, rules, lire))
	if _, vu := got["dev-scope"]; vu {
		t.Errorf("skill invisible « dev-scope » listé : %v", keys(got))
	}
	if _, vu := got["linkedin-post"]; !vu {
		t.Error("linkedin-post (lisible) devrait rester listé")
	}
}

// TestListNiveauRacineSkill : le niveau annoncé est le droit effectif à la
// racine du skill, pas à la racine de l'espace.
func TestListNiveauRacineSkill(t *testing.T) {
	rules := []perms.Rule{
		{Path: "shared", Level: perms.Lecture},
		{Path: "shared/skills/dev-scope", Level: perms.Ecriture},
	}
	got := parSlug(List(root, paths, perms.Lecture, rules, lire))
	if got["dev-scope"].Niveau != perms.Ecriture {
		t.Errorf("dev-scope niveau = %v, attendu ecriture", got["dev-scope"].Niveau)
	}
	if got["linkedin-post"].Niveau != perms.Lecture {
		t.Errorf("linkedin-post niveau = %v, attendu lecture", got["linkedin-post"].Niveau)
	}
}

func TestListTri(t *testing.T) {
	list := List(root, paths, perms.Ecriture, nil, lire)
	for i := 1; i < len(list); i++ {
		if list[i-1].Slug > list[i].Slug {
			t.Fatalf("liste non triée : %s avant %s", list[i-1].Slug, list[i].Slug)
		}
	}
}

func TestParseFrontmatter(t *testing.T) {
	cas := []struct {
		nom, contenu, name, desc string
	}{
		{"nominal", "---\nname: a\ndescription: b\n---\n", "a", "b"},
		{"guillemets doubles", "---\nname: \"a b\"\ndescription: \"c d\"\n---", "a b", "c d"},
		{"guillemets simples", "---\ndescription: 'x'\n---", "", "x"},
		{"pas de frontmatter", "# titre\ntexte", "", ""},
		{"champ absent", "---\nname: seul\n---", "seul", ""},
		{"lignes vides avant", "\n\n---\nname: a\n---", "a", ""},
		{"description avec deux-points", "---\ndescription: fait X: puis Y\n---", "", "fait X: puis Y"},
		{"clé indentée ignorée", "---\nname: real\ntools:\n  name: internal\n---", "real", ""},
		{"block scalar vide reste vide", "---\nname: a\ndescription: |\n---", "a", ""},
		{"bloc plié une ligne", "---\nname: a\ndescription: >\n  Fait X puis Y.\n---", "a", "Fait X puis Y."},
		{"bloc plié multi-lignes -> espaces", "---\ndescription: >\n  ligne un\n  ligne deux\n---", "", "ligne un ligne deux"},
		{"bloc littéral -> retours conservés", "---\ndescription: |\n  a\n  b\n---", "", "a\nb"},
		{"bloc avec chomping", "---\ndescription: >-\n  texte\n---", "", "texte"},
		{"clé après un bloc encore lue", "---\ndescription: >\n  desc\nname: apres\n---", "apres", "desc"},
	}
	for _, c := range cas {
		fm := ParseFrontmatter(c.contenu)
		if fm.Name != c.name || fm.Description != c.desc {
			t.Errorf("%s : ParseFrontmatter = (%q, %q), attendu (%q, %q)", c.nom, fm.Name, fm.Description, c.name, c.desc)
		}
	}
}

// TestParseTags : les trois écritures que le frontmatter peut porter. Vécu écrit
// la première ; les deux autres sont ce qu'un humain tape dans son éditeur.
func TestParseTags(t *testing.T) {
	cas := []struct {
		nom, contenu string
		tags         []string
	}{
		{"aucun metadata", "---\nname: a\ndescription: b\n---", nil},
		{"metadata sans tags", "---\nname: a\nmetadata:\n  equipe: contenu\n---", nil},
		{"liste inline", "---\nname: a\nmetadata:\n  tags: [contenu, client]\n---", []string{"contenu", "client"}},
		{"virgules", "---\nname: a\nmetadata:\n  tags: contenu, client\n---", []string{"contenu", "client"}},
		{"liste a tirets", "---\nname: a\nmetadata:\n  tags:\n    - contenu\n    - client\n---", []string{"contenu", "client"}},
		{"guillemets", "---\nmetadata:\n  tags: [\"contenu\", 'client']\n---", []string{"contenu", "client"}},
		{"accents preserves", "---\nmetadata:\n  tags: [rédaction]\n---", []string{"rédaction"}},
		{"tags vide", "---\nmetadata:\n  tags: []\n---", nil},
	}
	for _, c := range cas {
		got := ParseFrontmatter(c.contenu).Tags
		if len(got) != len(c.tags) {
			t.Errorf("%s : tags = %v, attendu %v", c.nom, got, c.tags)
			continue
		}
		for i := range got {
			if got[i] != c.tags[i] {
				t.Errorf("%s : tags = %v, attendu %v", c.nom, got, c.tags)
				break
			}
		}
	}
}

// TestMetadataNeMangePasLesClesSuivantes : `metadata` est le seul mapping que le
// parseur traverse. En sortir au bon endroit n'est pas cosmétique - une clé
// avalée, c'est une description perdue.
func TestMetadataNeMangePasLesClesSuivantes(t *testing.T) {
	contenu := "---\nmetadata:\n  tags: [x]\n  autre: valeur\ndescription: apres le bloc\nname: nom\n---\ncorps"
	fm := ParseFrontmatter(contenu)
	if fm.Description != "apres le bloc" {
		t.Errorf("description = %q : le bloc metadata a mangé la clé suivante", fm.Description)
	}
	if fm.Name != "nom" {
		t.Errorf("name = %q", fm.Name)
	}
	if len(fm.Tags) != 1 || fm.Tags[0] != "x" {
		t.Errorf("tags = %v", fm.Tags)
	}
}

func keys(m map[string]Info) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
