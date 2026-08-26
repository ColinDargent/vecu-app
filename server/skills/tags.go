package skills

import (
	"fmt"
	"strings"
)

// EcrisTags rend le contenu d'un SKILL.md avec `metadata.tags` remplacé par
// `tags`. Une liste vide retire la ligne, et retire aussi `metadata` s'il
// n'était là que pour elle.
//
// **Ce fichier appartient à quelqu'un d'autre.** Tout ce qui n'est pas la ligne
// `tags` ressort octet pour octet : le corps, les autres clés, leur ordre, les
// blocs scalaires, les lignes vides, les fins de ligne. La réécriture est donc
// faite ligne à ligne sur le texte d'origine, jamais par un aller-retour à
// travers une structure - un encodeur YAML normaliserait la mise en forme du
// frontmatter entier, et rendrait un diff illisible chez tous les membres au
// cycle suivant.
//
// Refuse plutôt que de deviner : un fichier sans frontmatter, ou dont le
// frontmatter n'est pas terminé, n'est pas modifié. Vécu ne fabrique pas un
// frontmatter dans le fichier d'un autre.
func EcrisTags(contenu string, tags []string) (string, error) {
	for _, t := range tags {
		if err := ValidTag(t); err != nil {
			return "", err
		}
	}

	// Découpage sur « \n » SEUL : une ligne CRLF garde son « \r » final, qui
	// ressort tel quel au Join. Normaliser le fichier en une convention unique
	// réécrirait toutes les lignes d'un fichier aux fins de ligne mixtes - un
	// diff de tout le fichier chez tous les membres, pour un tag. Partout où on
	// compare, c'est TrimSpace qui parle, donc le « \r » ne gêne pas.
	lignes := strings.Split(contenu, "\n")

	debut, finFM, ok := bornesFrontmatter(lignes)
	if !ok {
		return "", fmt.Errorf("ce SKILL.md n'a pas de frontmatter : ajoutez un bloc « --- » en tête avant de poser des tags")
	}

	iMeta, iTags, finTags, indent := reperes(lignes, debut, finFM)
	var out []string
	switch {
	case iTags >= 0:
		// La ligne existe : on la remplace, ou on l'enlève.
		out = append(out, lignes[:iTags]...)
		if len(tags) > 0 {
			out = append(out, indent+ligneTags(tags)+eolDe(lignes[iTags]))
		}
		reste := lignes[finTags:]
		// `metadata` vidé de sa seule clé n'a plus de sens en YAML : on l'enlève
		// avec elle plutôt que de laisser une clé sans valeur.
		if len(tags) == 0 && metaDevientVide(lignes, iMeta, iTags, finTags, finFM) {
			out = out[:iMeta]
		}
		out = append(out, reste...)
	case len(tags) == 0:
		return contenu, nil // rien à retirer, rien à écrire
	case iMeta >= 0:
		// `metadata` existe sans `tags` : on ajoute la ligne À LA FIN du bloc, à
		// l'indentation qu'il utilise déjà. En tête, elle se glisserait devant
		// des clés que la personne a rangées dans un ordre voulu.
		finBloc := finDuBloc(lignes, iMeta, finFM)
		out = append(out, lignes[:finBloc]...)
		out = append(out, indent+ligneTags(tags)+eolDe(lignes[finBloc-1]))
		out = append(out, lignes[finBloc:]...)
	default:
		// Ni l'un ni l'autre : le bloc entier s'ajoute juste avant le « --- »
		// de fin, donc après tout ce que la personne a écrit.
		out = append(out, lignes[:finFM]...)
		voisin := eolDe(lignes[finFM-1])
		out = append(out, "metadata:"+voisin, "  "+ligneTags(tags)+voisin)
		out = append(out, lignes[finFM:]...)
	}
	return strings.Join(out, "\n"), nil
}

// ValidTag refuse ce qui casserait la relecture ou permettrait d'injecter une
// clé de frontmatter. Un tag est une étiquette, pas une syntaxe.
func ValidTag(t string) error {
	if strings.TrimSpace(t) == "" {
		return fmt.Errorf("un tag vide")
	}
	// Le parseur maison de Vécu n'interprète aucun de ces caractères, mais ce
	// frontmatter est AUSSI lu par Claude Code, avec un vrai parseur YAML :
	// « *x » y est un alias, « &x » une ancre, « !x » un tag YAML, « %x » une
	// directive. Un seul suffirait à rendre le SKILL.md illisible pour son
	// auteur, et Vécu l'aurait écrit à sa place.
	if strings.ContainsAny(t, ",[]{}\n\r\"'#:*&!|>%@`") {
		return fmt.Errorf("caractère interdit dans le tag « %s » : lettres, chiffres, « - » et « _ »", t)
	}
	// Pas d'espace dans un tag, et c'est un choix de lecture avant d'être un
	// choix d'écriture : `découpeTags` traite l'espace comme un séparateur de
	// repli, pour qu'un « tags: contenu client » tapé à la main soit compris.
	// Autoriser « deux mots » rendrait cette tolérance ambiguë, et un tag
	// enregistré ne se relirait pas comme il a été écrit.
	if strings.ContainsAny(t, " \t") {
		return fmt.Errorf("un tag ne contient pas d'espace : « %s ». Utilisez un tiret", t)
	}
	if len(t) > 40 {
		return fmt.Errorf("tag trop long (40 caractères maximum) : « %s »", t)
	}
	return nil
}

func ligneTags(tags []string) string { return "tags: [" + strings.Join(tags, ", ") + "]" }

// eolDe : le terminateur d'une ligne existante, à recopier sur celle qu'on
// insère juste à côté. Sur un fichier aux fins de ligne mixtes, la seule règle
// défendable est de suivre le voisin immédiat - c'est ce qu'un humain verrait
// dans son éditeur.
func eolDe(ligne string) string {
	if strings.HasSuffix(ligne, "\r") {
		return "\r"
	}
	return ""
}

// bornesFrontmatter : index de la ligne « --- » d'ouverture et de celle de
// fermeture. `ok` est faux s'il n'y a pas de frontmatter délimité des deux
// côtés - le cas d'un fichier tronqué, qu'on ne touche pas.
func bornesFrontmatter(lignes []string) (debut, fin int, ok bool) {
	i := 0
	for i < len(lignes) && strings.TrimSpace(lignes[i]) == "" {
		i++
	}
	if i >= len(lignes) || strings.TrimSpace(lignes[i]) != "---" {
		return 0, 0, false
	}
	debut = i
	for j := i + 1; j < len(lignes); j++ {
		if strings.TrimSpace(lignes[j]) == "---" {
			return debut, j, true
		}
	}
	return 0, 0, false
}

// reperes localise, dans le frontmatter, la clé `metadata` de premier niveau et
// la ligne `tags` qui vit dedans. `finTags` est la première ligne APRÈS la
// valeur de tags (une forme liste occupe plusieurs lignes). `indent` est
// l'indentation à utiliser pour la ligne tags, reprise du bloc quand il existe.
func reperes(lignes []string, debut, finFM int) (iMeta, iTags, finTags int, indent string) {
	iMeta, iTags, finTags, indent = -1, -1, -1, "  "
	for i := debut + 1; i < finFM; i++ {
		ligne := lignes[i]
		if ligne != strings.TrimLeft(ligne, " \t") {
			continue // ligne indentée : traitée à l'intérieur du bloc metadata
		}
		cle, brut, ok := strings.Cut(ligne, ":")
		if !ok || strings.TrimSpace(cle) != "metadata" || strings.TrimSpace(brut) != "" {
			continue
		}
		iMeta = i
		for j := i + 1; j < finFM; j++ {
			suite := lignes[j]
			if strings.TrimSpace(suite) == "" {
				continue
			}
			if suite == strings.TrimLeft(suite, " \t") {
				break // dé-indentée : fin du bloc metadata
			}
			c, _, ok := strings.Cut(strings.TrimSpace(suite), ":")
			if !ok {
				continue
			}
			// L'indentation du bloc est celle de sa première clé, quelle qu'elle
			// soit : deux espaces, quatre, une tabulation.
			indent = suite[:len(suite)-len(strings.TrimLeft(suite, " \t"))]
			if strings.TrimSpace(c) != "tags" {
				continue
			}
			iTags = j
			finTags = j + 1
			// Forme liste : les « - x » plus indentés appartiennent à tags.
			for k := j + 1; k < finFM; k++ {
				s := strings.TrimSpace(lignes[k])
				// Une ligne vide se traverse SANS entrer dans finTags : si un
				// tiret la suit elle sera reprise, sinon elle reste au fichier.
				// L'absorber effaçait une ligne vide du frontmatter.
				if s == "" {
					continue
				}
				if lignes[k] != strings.TrimLeft(lignes[k], " \t") && strings.HasPrefix(s, "-") {
					finTags = k + 1
					continue
				}
				break
			}
			return iMeta, iTags, finTags, indent
		}
		return iMeta, iTags, finTags, indent
	}
	return iMeta, iTags, finTags, indent
}

// finDuBloc : index de la première ligne qui ne fait plus partie du bloc
// indenté ouvert par la clé de la ligne `iMeta`.
func finDuBloc(lignes []string, iMeta, finFM int) int {
	fin := iMeta + 1
	for i := iMeta + 1; i < finFM; i++ {
		if strings.TrimSpace(lignes[i]) == "" {
			continue
		}
		if lignes[i] == strings.TrimLeft(lignes[i], " \t") {
			break
		}
		fin = i + 1
	}
	return fin
}

// metaDevientVide : après retrait de la ligne tags, le bloc `metadata` ne
// porterait plus aucune clé.
func metaDevientVide(lignes []string, iMeta, iTags, finTags, finFM int) bool {
	if iMeta < 0 {
		return false
	}
	for i := iMeta + 1; i < finFM; i++ {
		if i >= iTags && i < finTags {
			continue // la ligne qu'on retire
		}
		ligne := lignes[i]
		if strings.TrimSpace(ligne) == "" {
			continue
		}
		if ligne == strings.TrimLeft(ligne, " \t") {
			break // fin du bloc
		}
		return false // une autre clé y vit
	}
	return true
}
