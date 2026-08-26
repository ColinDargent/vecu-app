package skills

import (
	"strings"
	"testing"
)

// EcrisTags réécrit le frontmatter d'un SKILL.md qui appartient à quelqu'un
// d'autre. Tout ce qui n'est pas `metadata.tags` doit ressortir octet pour
// octet : le corps, les autres clés, l'ordre, les commentaires, les lignes
// vides. Ces tests figent cette promesse avant que le code existe.
func TestEcrisTags(t *testing.T) {
	cas := []struct {
		nom    string
		avant  string
		tags   []string
		apres  string
		erreur bool
	}{
		{
			nom:   "ajoute un bloc metadata quand il n'y en a pas",
			avant: "---\nname: a\ndescription: b\n---\n\n# corps\n",
			tags:  []string{"contenu", "client"},
			apres: "---\nname: a\ndescription: b\nmetadata:\n  tags: [contenu, client]\n---\n\n# corps\n",
		},
		{
			nom:   "remplace des tags inline existants",
			avant: "---\nname: a\nmetadata:\n  tags: [vieux]\n---\ncorps\n",
			tags:  []string{"neuf"},
			apres: "---\nname: a\nmetadata:\n  tags: [neuf]\n---\ncorps\n",
		},
		{
			nom:   "remplace une liste a tirets par la forme inline",
			avant: "---\nname: a\nmetadata:\n  tags:\n    - un\n    - deux\n---\ncorps\n",
			tags:  []string{"trois"},
			apres: "---\nname: a\nmetadata:\n  tags: [trois]\n---\ncorps\n",
		},
		{
			nom:   "preserve les autres cles de metadata",
			avant: "---\nname: a\nmetadata:\n  equipe: contenu\n  tags: [vieux]\n  budget: 3\n---\ncorps\n",
			tags:  []string{"neuf"},
			apres: "---\nname: a\nmetadata:\n  equipe: contenu\n  tags: [neuf]\n  budget: 3\n---\ncorps\n",
		},
		{
			nom:   "ajoute tags dans un metadata existant sans tags",
			avant: "---\nname: a\nmetadata:\n  equipe: contenu\n---\ncorps\n",
			tags:  []string{"neuf"},
			apres: "---\nname: a\nmetadata:\n  equipe: contenu\n  tags: [neuf]\n---\ncorps\n",
		},
		{
			nom:   "preserve une cle posee APRES le bloc metadata",
			avant: "---\nmetadata:\n  tags: [x]\ndescription: apres\n---\ncorps\n",
			tags:  []string{"y"},
			apres: "---\nmetadata:\n  tags: [y]\ndescription: apres\n---\ncorps\n",
		},
		{
			nom:   "liste vide retire la ligne tags",
			avant: "---\nname: a\nmetadata:\n  equipe: contenu\n  tags: [vieux]\n---\ncorps\n",
			tags:  nil,
			apres: "---\nname: a\nmetadata:\n  equipe: contenu\n---\ncorps\n",
		},
		{
			nom:   "liste vide retire aussi un metadata devenu vide",
			avant: "---\nname: a\nmetadata:\n  tags: [vieux]\n---\ncorps\n",
			tags:  nil,
			apres: "---\nname: a\n---\ncorps\n",
		},
		{
			nom:   "liste vide sur un fichier sans tags ne change rien",
			avant: "---\nname: a\n---\ncorps\n",
			tags:  nil,
			apres: "---\nname: a\n---\ncorps\n",
		},
		{
			nom:   "preserve l'indentation a quatre espaces",
			avant: "---\nname: a\nmetadata:\n    tags: [vieux]\n---\ncorps\n",
			tags:  []string{"neuf"},
			apres: "---\nname: a\nmetadata:\n    tags: [neuf]\n---\ncorps\n",
		},
		{
			nom:   "preserve un bloc scalaire de description",
			avant: "---\ndescription: >\n  ligne un\n  ligne deux\nname: a\n---\ncorps\n",
			tags:  []string{"t"},
			apres: "---\ndescription: >\n  ligne un\n  ligne deux\nname: a\nmetadata:\n  tags: [t]\n---\ncorps\n",
		},
		{
			nom:   "preserve les fins de ligne Windows",
			avant: "---\r\nname: a\r\n---\r\ncorps\r\n",
			tags:  []string{"t"},
			apres: "---\r\nname: a\r\nmetadata:\r\n  tags: [t]\r\n---\r\ncorps\r\n",
		},
		{
			nom:    "refuse un fichier sans frontmatter",
			avant:  "# titre\ntexte\n",
			tags:   []string{"t"},
			erreur: true,
		},
		{
			nom:    "refuse un frontmatter non terminé",
			avant:  "---\nname: a\n",
			tags:   []string{"t"},
			erreur: true,
		},
		{
			nom:    "refuse un tag qui contient une espace",
			avant:  "---\nname: a\n---\n",
			tags:   []string{"deux mots"},
			erreur: true,
		},
		{
			nom:    "refuse un tag qui contient une virgule",
			avant:  "---\nname: a\n---\n",
			tags:   []string{"a,b"},
			erreur: true,
		},
		{
			nom:    "refuse un tag qui contient un crochet",
			avant:  "---\nname: a\n---\n",
			tags:   []string{"a]b"},
			erreur: true,
		},
		{
			nom:    "refuse un tag qui contient un retour a la ligne",
			avant:  "---\nname: a\n---\n",
			tags:   []string{"a\nname: pirate"},
			erreur: true,
		},
	}

	for _, c := range cas {
		got, err := EcrisTags(c.avant, c.tags)
		if c.erreur {
			if err == nil {
				t.Errorf("%s : attendu une erreur, obtenu %q", c.nom, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s : erreur inattendue %v", c.nom, err)
			continue
		}
		if got != c.apres {
			t.Errorf("%s :\n obtenu %q\nattendu %q", c.nom, got, c.apres)
		}
	}
}

// Trois défauts trouvés en cherchant à infirmer EcrisTags plutôt qu'à la
// valider. Chacun est reproduit ici AVANT sa correction.
func TestEcrisTagsDefautsTrouvesEnRevue(t *testing.T) {
	// 1. Fins de ligne MIXTES. La détection « le fichier contient un \r\n donc
	// tout est CRLF » réécrivait le fichier entier dans l'autre convention :
	// un diff de toutes les lignes chez tous les membres, pour un tag.
	mixte := "---\r\nname: a\ndescription: b\r\n---\ncorps\n"
	got, err := EcrisTags(mixte, []string{"t"})
	if err != nil {
		t.Fatalf("mixte : %v", err)
	}
	if !strings.Contains(got, "name: a\ndescription: b\r\n") {
		t.Errorf("les fins de ligne d'origine ne sont pas préservées :\n%q", got)
	}
	// La ligne insérée suit la convention de son VOISIN IMMÉDIAT (ici
	// « description: b\r\n »), seule règle défendable sur un fichier mixte :
	// c'est ce qu'un humain verrait dans son éditeur.
	if !strings.Contains(got, "description: b\r\nmetadata:\r\n  tags: [t]\r\n") {
		t.Errorf("la ligne insérée ne suit pas la convention de son voisin :\n%q", got)
	}

	// 2. Une LIGNE VIDE entre la fin d'une liste de tags et la clé suivante
	// était avalée avec les tirets.
	avecVide := "---\nmetadata:\n  tags:\n    - un\n\ndescription: b\n---\ncorps\n"
	got, err = EcrisTags(avecVide, []string{"neuf"})
	if err != nil {
		t.Fatalf("ligne vide : %v", err)
	}
	if !strings.Contains(got, "\n\ndescription: b") {
		t.Errorf("la ligne vide du frontmatter a disparu :\n%q", got)
	}

	// 3. Caractères YAML de STRUCTURE. Le parseur maison de Vécu ne les
	// interprète pas, mais Claude Code lit ce même frontmatter avec un vrai
	// parseur : « *foo » y est un alias, « &foo » une ancre, « !foo » un tag
	// YAML. Un seul suffirait à rendre le SKILL.md illisible pour son auteur.
	for _, mauvais := range []string{"*alias", "&ancre", "!type", "{a", "}b", "|pipe", ">plie", "%directive", "@arobase"} {
		if _, err := EcrisTags("---\nname: a\n---\n", []string{mauvais}); err == nil {
			t.Errorf("tag « %s » accepté alors qu'il casse un parseur YAML", mauvais)
		}
	}
}

// TestEcrisTagsRelu : ce qu'on écrit doit être relu à l'identique par le
// parseur. Sans cet aller-retour, l'interface afficherait autre chose que ce
// que la personne vient d'enregistrer.
func TestEcrisTagsRelu(t *testing.T) {
	jeux := [][]string{
		{"contenu"},
		{"contenu", "client"},
		{"rédaction", "veille"},
		{"deux-mots"},
	}
	for _, tags := range jeux {
		out, err := EcrisTags("---\nname: a\ndescription: b\n---\ncorps\n", tags)
		if err != nil {
			t.Fatalf("%v : %v", tags, err)
		}
		fm := ParseFrontmatter(out)
		if fm.Name != "a" || fm.Description != "b" {
			t.Errorf("%v : name/description abîmés (%q, %q)", tags, fm.Name, fm.Description)
		}
		if len(fm.Tags) != len(tags) {
			t.Errorf("%v : relu %v", tags, fm.Tags)
			continue
		}
		for i := range tags {
			if fm.Tags[i] != tags[i] {
				t.Errorf("%v : relu %v", tags, fm.Tags)
				break
			}
		}
	}
}
