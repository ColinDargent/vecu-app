package sync

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// casse.go : la collision de casse, sur un disque qui ne sait pas la distinguer.
//
// Git est sensible à la casse, APFS ne l'est pas. Le serveur peut donc porter
// `agents.md` ET `AGENTS.md` comme deux chemins distincts, là où le disque d'un
// poste macOS ne peut en héberger qu'un seul. Le second qui descend écrase le
// premier, et la clé d'état du premier décrit dès lors un contenu que le chemin
// ne porte pas - à jamais, puisque aucun cycle ne peut les départager.
//
// Mesuré le 23/08 sur le poste de Colin : `shared/process/agents.md` est suivi
// avec l'empreinte du registre des sub-agents, alors que ce chemin résout vers
// `AGENTS.md`, qui porte le contexte du dossier. Le défaut est connu depuis le
// 24/07 (« défaut 4 » de spec-suppression-constatee.md), il a été reporté
// explicitement, et il a produit sa première copie de conflit un mois plus tard.

// voisinDeCasse rend le chemin RÉELLEMENT présent sur le disque qui ne diffère
// de `rel` que par la casse, ou la chaîne vide s'il n'y en a pas.
//
// Les trois conditions sont réunies ensemble, et c'est ce qui rend la réponse
// exacte sur les trois configurations qui existent :
//
//   - `rel` n'est PAS parmi les noms réels du dossier. Sur un disque SENSIBLE à
//     la casse où les deux fichiers coexistent légitimement, cette condition
//     tombe : il n'y a pas de collision, les deux vivent côte à côte.
//   - un nom réel du dossier ne diffère de lui que par la casse.
//   - `os.Lstat` du chemin exact RÉUSSIT quand même. C'est la preuve que le
//     système de fichiers a résolu un nom qui n'existe pas à cette casse, donc
//     qu'il est insensible. Sur Linux avec le seul `agents.md`, il échoue et il
//     n'y a pas de collision non plus.
//
// L'insensibilité est ainsi CONSTATÉE sur le chemin concerné, et jamais déduite
// de `runtime.GOOS` : un volume monté ou un dossier APFS peuvent être créés
// sensibles à la casse, et sur ce sujet précis, déduire du système
// d'exploitation serait exactement l'erreur qu'on instrumente.
//
// Lecture du dossier et pas `os.Stat` : sur APFS, `os.Stat` résout la casse et
// ne peut donc pas répondre à « quel nom le disque porte-t-il vraiment ». C'est
// la même mécanique que `entreeSkillMd` dans adoption.go.
func (e *Engine) voisinDeCasse(rel string) string {
	// La garde du paquet, avant toute lecture de disque : sans elle, un espace
	// déplacé et remplacé par un lien ferait lire un dossier situé hors de la
	// racine locale, et nommer dans le journal un fichier personnel qui n'a
	// rien à faire là. Même convention que `changeDepuisLeScan`.
	if err := e.sansLien(rel); err != nil {
		return ""
	}
	// `os.Lstat` D'ABORD, et l'ordre n'est pas cosmétique. Au premier montage
	// d'un espace, TOUS les chemins entrants sont inconnus, donc tous portent le
	// motif qui appelle cette fonction : lire le dossier avant de savoir si le
	// fichier existe seulement en local ferait une lecture par fichier reçu, soit
	// un coût quadratique par dossier sur un vault de 900 fichiers. Un chemin que
	// le disque ne résout pas ne peut collisionner avec rien, et il sort ici pour
	// le prix d'un `Lstat`.
	if _, err := os.Lstat(e.abs(rel)); err != nil {
		return ""
	}
	base := path.Base(rel)
	entrees, err := os.ReadDir(filepath.Dir(e.abs(rel)))
	if err != nil {
		return ""
	}
	for _, ent := range entrees {
		nom := ent.Name()
		if nom == base {
			return "" // le chemin existe à sa casse exacte : rien à départager
		}
		if strings.EqualFold(nom, base) {
			// `os.ReadDir` rend les entrées triées, donc ce choix est
			// déterministe. Plusieurs voisins supposeraient d'ailleurs un disque
			// SENSIBLE à la casse, où le `Lstat` ci-dessus a déjà fait sortir.
			return path.Join(path.Dir(rel), nom)
		}
	}
	return ""
}

// motifCollisionDeCasse rend la raison à poser en laisse si `rel` ne peut pas
// être écrit sans écraser un voisin de casse, ou la chaîne vide.
//
// DEUX portes descendent du contenu, le pull et le rattrapage, et elles doivent
// refuser à l'identique. Ne fermer que le pull ne ferme rien : le chemin refusé
// manque alors au disque, le rattrapage va le chercher au cycle suivant, et la
// copie se fait là - une par cycle, indéfiniment. Mesuré sur le banc le 23/08.
func (e *Engine) motifCollisionDeCasse(rel string) string {
	voisin := e.voisinDeCasse(rel)
	if voisin == "" {
		return ""
	}
	return "collision de casse avec " + voisin + " : ce disque ne sait pas distinguer les deux noms. " +
		"Rien n'est écrit ici, et le contenu reste sur le serveur. " +
		"Pour trancher, renommer l'un des deux depuis un poste, ou retirer celui qui fait doublon."
}

// FantomesDeCasse rend les groupes de chemins SUIVIS qui ne diffèrent que par
// la casse. Chaque groupe est une collision : sur un disque insensible à la
// casse, un seul de ces chemins peut avoir un fichier, et les autres décrivent
// à jamais un contenu qu'ils ne portent pas.
//
// Dérivé de l'état SEUL, sans toucher au disque : c'est un diagnostic, il doit
// répondre sur un poste dont le volume est décroché, et `vecu status` le lit
// sans construire de moteur.
//
// Aucun champ ajouté à `State`, donc aucune migration : la collision est une
// propriété des clés déjà présentes.
//
// Ne supprime RIEN et n'est appelé par aucun chemin d'écriture. Trancher lequel
// des deux noms garder demande de savoir ce que chaque fichier contient et à
// qui il sert : c'est un geste humain, et sur un dépôt partagé il se fait à
// deux.
func FantomesDeCasse(files map[string]string) [][]string {
	parRepli := map[string][]string{}
	for chemin := range files {
		bas := strings.ToLower(chemin)
		parRepli[bas] = append(parRepli[bas], chemin)
	}
	var groupes [][]string
	for _, groupe := range parRepli {
		if len(groupe) < 2 {
			continue
		}
		sort.Strings(groupe)
		groupes = append(groupes, groupe)
	}
	// Trié sur le premier chemin de chaque groupe : une sortie de diagnostic
	// qui change d'ordre d'un appel à l'autre ne se compare pas.
	sort.Slice(groupes, func(i, j int) bool { return groupes[i][0] < groupes[j][0] })
	return groupes
}

// Court abrège une empreinte pour un rapport lisible. Exporté pour
// `vecu status`, qui affiche l'état sans construire de moteur et n'a donc pas
// accès à la version interne. Une seule règle de troncature pour le journal et
// pour la commande : deux longueurs différentes rendraient incomparables deux
// affichages du même état.
func Court(h string) string { return court(h) }
