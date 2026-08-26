// Package skills : la notion de « skill » de Vécu.
//
// Un skill est un dossier `<root>/<slug>/` contenant au minimum `SKILL.md`
// (frontmatter `name` + `description`, convention Agent Skills, native à Claude
// Code). `root` est le dossier où vivent les skills - par défaut `shared/skills`,
// à l'intérieur de l'espace `shared` déjà synchronisé (cf. spec-skills.md,
// amendement 2026-07-26). Ce n'est donc PAS un espace de premier niveau : les
// skills héritent des droits de leur espace parent, et le contrôle par skill est
// une simple règle `perms` sur `<root>/<slug>`.
//
// Comme pour les espaces, un skill existe pour quelqu'un si et seulement si au
// moins un fichier ordinaire qu'il contient est lisible par cette personne. Le
// modèle ne consulte aucune table : il dérive tout de la liste des chemins du
// dépôt et des règles de l'utilisateur.
package skills

import (
	"sort"
	"strings"

	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
)

// DefaultRoot : dossier des skills par défaut. Configurable par l'appelant, mais
// une valeur unique suffit à la v1 (cf. amendement spec : shared/skills).
const DefaultRoot = "shared/skills"

// skillFile : le fichier qui porte le frontmatter d'un skill.
const skillFile = "SKILL.md"

// Info : un skill vu par un utilisateur donné.
type Info struct {
	Slug string
	// Name : libellé lu du frontmatter `SKILL.md`. Retombe sur le slug si le
	// SKILL.md est absent, illisible, ou sans champ `name`.
	Name string
	// Description : champ `description` du frontmatter (vide si absent).
	Description string
	// Fichiers : nombre de fichiers du skill lisibles par cet utilisateur.
	Fichiers int
	// Niveau : droit effectif à la racine du skill (`<root>/<slug>`).
	Niveau perms.Level
	// Ecriture : au moins un chemin du skill est modifiable.
	Ecriture bool
	// Projetable : le skill a un `SKILL.md` lisible et un frontmatter exploitable,
	// donc peut être projeté vers l'outil local. Sinon Raison dit pourquoi pas.
	Projetable bool
	// Raison : si non projetable, le motif (SKILL.md absent / illisible / slug
	// invalide). Vide si projetable.
	Raison string
	// Tags : thématiques du skill, lues dans `metadata.tags` du frontmatter.
	//
	// Elles vivent DANS le skill et pas dans une table du serveur : un tag
	// voyage alors avec son skill, il est versionné comme le reste, et il reste
	// lisible dans l'éditeur de son auteur. `metadata` est prévu pour ça -
	// « free-form YAML map for your own key-value data […] read by your own
	// tooling », que Claude Code accepte sans agir dessus.
	// Source: https://code.claude.com/docs/en/skills#frontmatter-reference
	//
	// Les tags ne portent AUCUN droit : le partage se règle ailleurs. Un tag est
	// une étiquette pour s'y retrouver, jamais une décision d'accès.
	Tags []string
}

// List dérive les skills visibles par un utilisateur sous `root`, à partir de la
// liste complète des chemins du dépôt, des règles de l'utilisateur et de son
// niveau par défaut.
//
// `read` lit le contenu d'un chemin du dépôt (pour parser le frontmatter du
// SKILL.md) ; il n'est appelé que sur des SKILL.md lisibles. Une erreur de `read`
// est traitée comme « SKILL.md illisible » (skill listé, non projetable).
//
// Règles (calquées sur espaces.List) :
//   - un skill est listé ssi au moins un de ses fichiers est lisible ;
//   - un slug invalide (mêmes contraintes que espaces.ValidNom) est ignoré : le
//     dossier de commande a un nom qui deviendra la commande côté Claude, il doit
//     être un segment de chemin sûr ;
//   - name retombe sur le slug si le frontmatter ne le donne pas.
func List(root string, paths []string, defaut perms.Level, rules []perms.Rule, read func(path string) (string, error)) []Info {
	root = perms.Canon(root)
	prefix := root + "/"
	byslug := map[string]*Info{}
	// mdLisible[slug] : le `SKILL.md` du skill est réellement présent dans le
	// dépôt (dans `paths`) ET lisible. On ne déduit jamais sa présence d'un
	// simple droit (perms.CanRead calcule un droit, pas une existence) : sinon on
	// appellerait read() sur un chemin absent, dont le comportement dépend de
	// l'implémentation du lecteur.
	mdLisible := map[string]bool{}
	for _, p := range paths {
		reste, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		slug, _, aFichier := strings.Cut(reste, "/")
		if !aFichier {
			// Fichier posé directement dans root (ex: root/README.md) : pas un skill.
			continue
		}
		if espaces.ValidNom(slug) != nil {
			continue
		}
		lisible := perms.CanRead(p, defaut, rules)
		if p == prefix+slug+"/"+skillFile && lisible {
			mdLisible[slug] = true
		}
		if !lisible {
			continue
		}
		info := byslug[slug]
		if info == nil {
			info = &Info{Slug: slug, Name: slug, Niveau: perms.Effective(prefix+slug, defaut, rules)}
			byslug[slug] = info
		}
		info.Fichiers++
		if perms.CanWrite(p, defaut, rules) {
			info.Ecriture = true
		}
	}

	out := make([]Info, 0, len(byslug))
	for slug, info := range byslug {
		remplirFrontmatter(info, prefix+slug+"/"+skillFile, mdLisible[slug], read)
		out = append(out, *info)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Slug < out[b].Slug })
	return out
}

// remplirFrontmatter renseigne Name/Description/Projetable/Raison d'un skill à
// partir de son SKILL.md.
//
// Un skill n'est projetable que si son SKILL.md est présent, lisible, et porte
// une `description` exploitable : c'est ce champ qui permet à Claude de savoir
// quand déclencher le skill. Sans lui, un symlink projeté pointerait vers un
// skill que l'agent n'utiliserait pas - on le signale plutôt que de le prétendre
// prêt. `name`, lui, retombe sur le slug (la commande vient du nom de dossier).
func remplirFrontmatter(info *Info, skillMDPath string, mdLisible bool, read func(path string) (string, error)) {
	if !mdLisible {
		info.Raison = "SKILL.md absent ou non lisible"
		return
	}
	contenu, err := read(skillMDPath)
	if err != nil {
		info.Raison = "SKILL.md illisible"
		return
	}
	fm := ParseFrontmatter(contenu)
	if fm.Name != "" {
		info.Name = fm.Name
	}
	info.Description = fm.Description
	info.Tags = fm.Tags
	// Les tags n'entrent PAS dans la projetabilité : un skill sans tag se projette
	// exactement comme avant. Ils ne servent qu'à retrouver un skill dans une
	// liste qui en compte trente.
	if fm.Description == "" {
		info.Raison = "SKILL.md sans description exploitable"
		return
	}
	info.Projetable = true
}

// Frontmatter : ce que Vécu lit d'un SKILL.md. Tout le reste du frontmatter est
// ignoré et laissé intact - c'est le contrat de l'outil de quelqu'un d'autre.
type Frontmatter struct {
	Name        string
	Description string
	Tags        []string
}

// ParseFrontmatter extrait `name`, `description` et `metadata.tags` du
// frontmatter d'un SKILL.md.
//
// Parseur maison volontaire (pas de dépendance YAML pour trois champs, cohérent
// avec le minimalisme de Vécu) : le frontmatter est le bloc entre la première
// ligne `---` et le `---` suivant. Tout écart (pas de frontmatter, champ absent)
// renvoie des valeurs vides : l'appelant décide du fallback.
func ParseFrontmatter(contenu string) Frontmatter {
	var fm Frontmatter
	name, description := "", ""
	contenu = strings.TrimPrefix(contenu, "\ufeff") // BOM éventuel
	lignes := strings.Split(contenu, "\n")
	i := 0
	// Sauter d'éventuelles lignes vides avant le frontmatter.
	for i < len(lignes) && strings.TrimSpace(lignes[i]) == "" {
		i++
	}
	if i >= len(lignes) || strings.TrimSpace(lignes[i]) != "---" {
		return fm // pas de frontmatter
	}
	i++
	for i < len(lignes) {
		ligne := lignes[i]
		if strings.TrimSpace(ligne) == "---" {
			break // fin du frontmatter
		}
		// Clés de premier niveau uniquement : une ligne indentée est une valeur
		// imbriquée (ex: « name: internal » sous « tools: »), pas le name/description
		// de tête. Ce parseur minimal ne traverse pas les mappings imbriqués.
		if ligne != strings.TrimLeft(ligne, " \t") {
			i++
			continue
		}
		cle, brut, ok := strings.Cut(ligne, ":")
		if !ok {
			i++
			continue
		}
		cle = strings.TrimSpace(cle)
		brut = strings.TrimSpace(brut)
		// `metadata` est le SEUL mapping imbriqué que ce parseur traverse, et il
		// n'y cherche que `tags`. Le reste de ce que quelqu'un y range est laissé
		// où il est - c'est sa clé, pas la nôtre.
		if cle == "metadata" && brut == "" {
			var tags []string
			tags, i = lisTagsImbriques(lignes, i+1)
			fm.Tags = tags
			continue
		}
		var valeur string
		if estBlocScalaire(brut) {
			// « > » plie (retours à la ligne -> espaces), « | » conserve. Le contenu
			// vit sur les lignes plus indentées qui suivent ; une ligne dé-indentée
			// (clé suivante ou « --- ») termine le bloc.
			plie := brut[0] == '>'
			i++
			var bloc []string
			for i < len(lignes) {
				suite := lignes[i]
				if strings.TrimSpace(suite) == "" {
					i++
					continue // ligne vide : dans le bloc, ignorée pour une description
				}
				if suite == strings.TrimLeft(suite, " \t") {
					break // dé-indentée : fin du bloc
				}
				bloc = append(bloc, strings.TrimSpace(suite))
				i++
			}
			sep := "\n"
			if plie {
				sep = " "
			}
			valeur = strings.TrimSpace(strings.Join(bloc, sep))
		} else {
			valeur = déquote(brut)
			i++
		}
		switch cle {
		case "name":
			name = valeur
		case "description":
			description = valeur
		}
	}
	fm.Name, fm.Description = name, description
	return fm
}

// lisTagsImbriques parcourt le bloc indenté qui suit une clé `metadata:` et en
// extrait `tags`. Rend les tags trouvés et l'index de la première ligne QUI NE
// FAIT PAS partie du bloc, pour que l'appelant reprenne exactement là.
//
// Trois écritures acceptées, parce que Vécu écrit la première mais qu'un humain
// écrit les deux autres à la main dans son éditeur :
//
//	metadata:
//	  tags: [contenu, client]
//	  tags: contenu, client
//	  tags:
//	    - contenu
//	    - client
func lisTagsImbriques(lignes []string, i int) ([]string, int) {
	var tags []string
	for i < len(lignes) {
		ligne := lignes[i]
		if strings.TrimSpace(ligne) == "" {
			i++
			continue
		}
		// Dé-indentée : le bloc `metadata` est fini. Le « --- » de fin de
		// frontmatter tombe dans ce cas, et c'est voulu.
		if ligne == strings.TrimLeft(ligne, " \t") {
			return tags, i
		}
		cle, brut, ok := strings.Cut(strings.TrimSpace(ligne), ":")
		i++
		if !ok || strings.TrimSpace(cle) != "tags" {
			continue // une autre clé de metadata : on la laisse tranquille
		}
		brut = strings.TrimSpace(brut)
		if brut != "" {
			tags = découpeTags(strings.TrimSuffix(strings.TrimPrefix(brut, "["), "]"))
			continue
		}
		// Forme liste : les « - x » plus indentés qui suivent.
		for i < len(lignes) {
			suite := strings.TrimSpace(lignes[i])
			if suite == "" {
				i++
				continue
			}
			if lignes[i] == strings.TrimLeft(lignes[i], " \t") || !strings.HasPrefix(suite, "-") {
				break
			}
			if t := nettoieTag(strings.TrimPrefix(suite, "-")); t != "" {
				tags = append(tags, t)
			}
			i++
		}
	}
	return tags, i
}

// découpeTags : « a, b , c » ou « a b » -> [a b c]. La virgule est le séparateur
// attendu ; l'espace sert de repli pour ce qui est tapé à la main.
func découpeTags(brut string) []string {
	champs := strings.FieldsFunc(brut, func(r rune) bool { return r == ',' })
	var out []string
	for _, c := range champs {
		for _, t := range strings.Fields(c) {
			if t := nettoieTag(t); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// nettoieTag : espaces et guillemets retirés. Un tag reste ce que la personne a
// écrit - pas de mise en minuscules, pas de translittération : il est affiché
// tel quel, et le filtre, lui, compare sans casse ni accents.
func nettoieTag(s string) string { return déquote(strings.TrimSpace(s)) }

// estBlocScalaire indique si une valeur de frontmatter est un indicateur de bloc
// YAML (« > » plié ou « | » littéral), avec un indicateur de chomping optionnel
// (« - » ou « + »). Le contenu réel vit alors sur les lignes indentées suivantes.
func estBlocScalaire(valeur string) bool {
	switch valeur {
	case ">", "|", ">-", "|-", ">+", "|+":
		return true
	}
	return false
}

// déquote retire une paire de guillemets simples ou doubles encadrants.
func déquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
