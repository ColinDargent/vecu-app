package sync

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
)

// La projection rend les skills synchronisés (dans `shared/skills/`) utilisables
// par l'agent local, dans SA convention, sans coupler le stockage à un outil.
//
// Principe : la source de vérité d'un skill reste `shared/skills/<slug>/`. La
// projection ne fait qu'y POINTER, par un lien symbolique relatif
// `<cible>/<slug>` → `../../shared/skills/<slug>`. Supprimer tous les liens
// laisse les skills intacts ; changer d'outil = changer d'adaptateur, jamais de
// stockage. La projection est idempotente, non destructive, et ne gère QUE les
// liens qu'elle a créés (suivis dans l'état, une liste par cible).
//
// Une CIBLE est un dossier où un outil découvre les skills du projet. Le format
// `SKILL.md` étant un standard ouvert, un adaptateur est un mapping de dossiers
// et jamais un traducteur : le même dossier source sert les trois outils.

// skillsEspace : le dossier des skills dans le dépôt (sous l'espace `shared`).
// Un skill = un sous-dossier contenant un `SKILL.md`.
const skillsEspace = "shared/skills"

// claudeSkillsRel : dossier où Claude Code découvre les skills du projet.
// Source: https://code.claude.com/docs/en/skills
const claudeSkillsRel = ".claude/skills"

// agentsSkillsRel : dossier commun à Cursor et à Codex. Une seule cible sert les
// deux, ce que les deux docs officielles confirment séparément :
//   - Cursor charge les skills depuis `.agents/skills/` et `.cursor/skills/`.
//     Source: https://cursor.com/docs/skills
//   - Codex scanne `.agents/skills` dans chaque dossier du répertoire courant
//     jusqu'à la racine du dépôt. Source: https://learn.chatgpt.com/docs/build-skills
//
// Les deux suivent les liens symboliques, donc la mécanique de Claude Code
// s'applique telle quelle. `.cursor/skills/` et `.codex/skills/` sont
// volontairement laissés de côté : ils feraient une cible de plus à créer, à
// retirer et à surveiller, pour aucun outil de plus.
//
// Limite à connaître, côté Codex : la remontée s'arrête à la racine du dépôt
// git. Une racine locale hors dépôt n'est vue que si Codex est lancé depuis
// elle ou son parent direct.
const agentsSkillsRel = ".agents/skills"

// dossierPresent : la cible `.agents/skills/` n'est peuplée que si son dossier
// existe DÉJÀ. C'est le geste qui porte l'intention (arbitrage du 19/08) : la
// présence de Cursor dans /Applications ne dit rien de ce que l'utilisateur veut
// dans CE dossier, alors qu'un `mkdir` le dit.
//
// TROIS réponses, pas deux, et l'appelant en fait trois choses différentes :
//
//   - présent : projeter ;
//   - absent : oublier le registre, il n'y a plus de liens à gérer ;
//   - erreur : ne rien projeter ET ne rien oublier. « Je n'ai pas pu regarder »
//     n'est ni « c'est là » ni « ce n'est pas là ». Écraser le registre sur un
//     dossier illisible orphelinerait des liens que plus rien ne retirerait.
//
// `Lstat` et non `Stat` : un lien symbolique compte comme présent, MÊME cassé.
// Sinon un `.agents/skills` géré par un gestionnaire de dotfiles, pas encore
// déployé, se lirait « absent » et effacerait le registre en silence. Rendu
// présent, il part dans projeteCible, dont `traverseUnLien` le refuse avec un
// motif lisible.
//
// ENOTDIR compte comme absent : un parent qui est un fichier ordinaire signifie
// qu'il n'y a pas de dossier ici, pas qu'on n'a pas pu regarder. C'est la
// convention du package, déjà tenue par `absentCommeFichier`.
func dossierPresent(dir, rel string) (bool, error) {
	fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	switch {
	case err == nil:
		return fi.IsDir() || fi.Mode()&os.ModeSymlink != 0, nil
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return false, nil
	default:
		return false, err
	}
}

// cibleSymlink : la cible RELATIVE du lien `<skillsRel>/<slug>`. Relative pour
// survivre à un déplacement de la racine. Le nombre de « .. » se dérive des
// segments de `skillsRel` (`.claude/skills/<slug>` est à deux niveaux sous la
// racine, d'où « ../../ ») plutôt que d'être écrit en dur : une cible d'une
// autre profondeur produirait sinon un lien qui pointe hors de la racine.
func cibleSymlink(skillsRel, slug string) string {
	remonte := make([]string, 0, 4)
	for range strings.Split(skillsRel, "/") {
		remonte = append(remonte, "..")
	}
	return filepath.Join(append(remonte, skillsEspace, slug)...)
}

// detecteClaude : Claude Code est-il utilisé sur ce poste ? Signal suffisant
// pour activer la projection par défaut (cf. NewEngine). On teste, dans l'ordre :
// un dossier `.claude/` à la racine synchronisée (le vault est piloté par Claude
// Code ici), puis `~/.claude/` (installation utilisateur), puis le binaire
// `claude` sur le PATH. Toute présence suffit ; l'absence des trois laisse la
// projection éteinte (aucun agent à servir).
func detecteClaude(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, ".claude")); err == nil && fi.IsDir() {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		if fi, err := os.Stat(filepath.Join(home, ".claude")); err == nil && fi.IsDir() {
			return true
		}
	}
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	return false
}

// projeteVers indique si ce poste projette vers l'outil `outil`.
func (e *Engine) projeteVers(outil string) bool {
	for _, o := range e.projections {
		if o == outil {
			return true
		}
	}
	return false
}

// skillsMontes : les slugs de skills réellement descendus sur ce poste.
func (e *Engine) skillsMontes() []string { return SkillsMontes(e.state) }

// SkillsMontes dérive de l'état les slugs de skills descendus sur ce poste. Un
// skill compte s'il a un `SKILL.md` suivi dans `shared/skills/<slug>/` (sans
// SKILL.md, Claude Code ne le chargerait pas). Le slug est revalidé : `state.
// Files` est relu du disque sans garantie de forme. Exporté pour `vecu status`,
// qui lit l'état sans construire de moteur.
func SkillsMontes(st State) []string {
	prefix := skillsEspace + "/"
	set := map[string]bool{}
	for p := range st.Files {
		reste, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		slug, sous, aFichier := strings.Cut(reste, "/")
		if !aFichier || sous != "SKILL.md" {
			continue
		}
		if !espaceValide(slug) {
			continue
		}
		set[slug] = true
	}
	out := make([]string, 0, len(set))
	for slug := range set {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

// Projette met à jour les liens de projection après un cycle. Étape distincte de
// SyncOnce : elle lit l'état que le cycle vient d'écrire, ne touche jamais à la
// logique de sync, et ne peut pas faire échouer un cycle (ses accrocs sont des
// Laisses, comme le reste). Sans projection activée sur ce poste : no-op.
func (e *Engine) Projette() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	montes := e.skillsMontes()
	var laisses []Laisse
	change := false

	// Claude Code : activé par détection d'outil, inscrit dans la config.
	if e.projeteVers("claude") {
		projetes, l, refuse := projeteCible(e.dir, claudeSkillsRel, montes, e.state.Projetes)
		if !refuse {
			change = change || !slices.Equal(e.state.Projetes, projetes)
			e.state.Projetes = projetes
		}
		laisses = append(laisses, l...)
	}

	// Cursor et Codex : la condition se lit sur le DISQUE à chaque cycle, pas
	// dans la config. Rien à stocker, rien à migrer, et retirer le dossier
	// suffit à retirer la cible.
	// Un lien symbolique en travers du chemin est l'affaire de projeteCible, qui
	// sait le refuser avec un motif lisible. Il faut l'y envoyer explicitement :
	// `dossierPresent` s'appuie sur Lstat, qui ne refuse de suivre que le DERNIER
	// composant. Un `.agents` lui-même symlinké, dont la cible ne contient pas
	// encore `skills`, se lirait donc « absent » et passerait en silence - le cas
	// même que la garde prétend couvrir.
	lien := traverseUnLien(e.dir, agentsSkillsRel)
	present, err := dossierPresent(e.dir, agentsSkillsRel)
	switch {
	case err != nil:
		// Illisible. Ne rien projeter, et le dire : le registre décrit des liens
		// qui sont probablement toujours là.
		laisses = append(laisses, Laisse{
			Chemin: agentsSkillsRel,
			Raison: "dossier illisible (" + err.Error() + ") : projection vers Cursor et Codex laissée en l'état",
			Genre:  GenreServeur,
		})
	case lien || present:
		projetes, l, refuse := projeteCible(e.dir, agentsSkillsRel, montes, e.state.ProjetesAgents)
		if !refuse {
			change = change || !slices.Equal(e.state.ProjetesAgents, projetes)
			e.state.ProjetesAgents = projetes
		}
		laisses = append(laisses, l...)
	}
	// Dossier absent : le registre est CONSERVÉ, jamais vidé. Il décrit ce que
	// Vécu a posé, pas ce qui est monté à l'instant. L'effacer n'achèterait rien
	// (la pose d'un lien ne consulte jamais le registre, seuls le retrait et la
	// reprise après erreur le lisent) et coûterait cher sur une absence
	// TRANSITOIRE - rename, volume démonté, bascule de dotfiles : les liens, eux,
	// sont toujours là. Un skill qui sortirait du périmètre pendant cette fenêtre
	// verrait son lien orphelin à jamais, faute d'un registre pour le retirer.
	// Un registre qui mentionne un lien absent est inoffensif : la boucle de
	// retrait fait un Lstat et passe son chemin.

	if len(laisses) > 0 {
		e.laisses = append(e.laisses, laisses...)
		e.state.Laisses = e.laisses
		change = true
	}
	// Rien n'a bougé : ne pas réécrire state.json pour rien. Même règle que
	// `Adopte`.
	//
	// À ne pas surestimer : `SyncOnce` sauvegarde l'état à chaque cycle de toute
	// façon, donc ce garde évite une écriture sur deux, pas toutes. Et un accroc
	// PERMANENT (un dossier fait main qui bloque un slug à chaque passage)
	// réémet sa Laisse à chaque cycle, donc `change` reste vrai. Le garde sert
	// le cas nominal, pas le cas dégradé.
	if !change {
		return nil
	}
	return e.state.Save(e.dir)
}

// projeterClaude : la cible de Claude Code, sans le drapeau de refus.
// `Projette` appelle `projeteCible` directement ; ce point d'entrée n'existe
// plus que pour les tests, qui l'appellent en neuf endroits et dont la
// signature à deux valeurs de retour est la preuve que le refactor de cible
// n'a rien changé au comportement.
func projeterClaude(dir string, desired, previous []string) (projetes []string, laisses []Laisse) {
	projetes, laisses, _ = projeteCible(dir, claudeSkillsRel, desired, previous)
	return projetes, laisses
}

// projeteCible aligne le dossier `skillsRel` sur l'ensemble `desired` des skills
// montés, en ne gérant QUE ses propres liens (ceux dont la cible est
// `../../shared/skills/<slug>`). `previous` est l'ensemble géré au cycle
// précédent, pour CETTE cible. Renvoie le nouvel ensemble géré et les accrocs.
//
// Invariants :
//   - jamais d'écrasement d'un chemin `<skillsRel>/<x>` qui n'est pas un lien
//     géré par Vécu (dossier réel fait main, lien étranger vers ailleurs) : on
//     le laisse et on le signale ;
//   - adoption d'un lien pré-existant déjà correct (même cible) : il est inscrit
//     comme géré sans être recréé ;
//   - un skill disparu voit SON lien retiré, à condition qu'il soit encore le
//     lien géré (cible correcte). S'il a été remplacé entre-temps, on n'y touche pas.
//
// `refuse` distingue « la cible entière a été refusée, rien n'a été inspecté »
// de « rien n'est géré ». Les deux rendent une liste vide, et l'appelant doit
// les traiter à l'opposé : sur un refus il CONSERVE le registre, sinon il
// orphelinerait des liens que plus rien ne viendrait retirer. C'est la prudence
// que la boucle applique déjà par slug (cf. `case err != nil`), portée au
// niveau de la cible.
func projeteCible(dir, skillsRel string, desired, previous []string) (projetes []string, laisses []Laisse, refuse bool) {
	// Invariant du moteur (cf. engine.sansLien) : ne jamais écrire ni supprimer à
	// travers un lien symbolique - ça sortirait de la racine. Si `.claude` ou
	// `.claude/skills` est LUI-MÊME un lien (gestionnaire de dotfiles, skills
	// centralisés ailleurs), toute la projection est refusée et rien n'est touché.
	if traverseUnLien(dir, skillsRel) {
		return nil, []Laisse{{
			Chemin: skillsRel,
			Raison: skillsRel + " (ou un de ses parents) est un lien symbolique : projection refusée pour ne rien écrire hors de la racine. Les skills restent dans shared/skills/.",
			Genre:  GenreServeur,
		}}, true
	}
	skillsDir := filepath.Join(dir, filepath.FromSlash(skillsRel))
	veut := make(map[string]bool, len(desired))
	for _, slug := range desired {
		veut[slug] = true
	}
	// `previous` vient de state.Projetes, relu de state.json : validé comme
	// partout ailleurs (le client ne confie pas la sûreté de son disque à un état
	// qui a pu être corrompu ou édité). Un slug avec « .. » ferait sinon un Remove
	// hors de .claude/skills/.
	prev := make(map[string]bool, len(previous))
	for _, slug := range previous {
		if espaceValide(slug) {
			prev[slug] = true
		}
	}
	gere := make(map[string]bool, len(desired))

	for _, slug := range desired {
		lien := filepath.Join(skillsDir, slug)
		cible := cibleSymlink(skillsRel, slug)
		info, err := os.Lstat(lien)
		switch {
		case os.IsNotExist(err):
			if err := os.MkdirAll(skillsDir, 0o755); err != nil {
				laisses = append(laisses, laisseProjection(slug, "dossier "+skillsRel+"/ non créable : "+err.Error()))
				continue
			}
			if err := os.Symlink(cible, lien); err != nil {
				laisses = append(laisses, laisseProjection(slug, "lien non créé : "+err.Error()))
				continue
			}
			gere[slug] = true
		case err != nil:
			laisses = append(laisses, laisseProjection(slug, "impossible d'inspecter "+skillsRel+"/"+slug+" : "+err.Error()))
			// Accroc transitoire (volume décroché, droits) : si ce slug était déjà
			// géré, conserver le suivi. Le lâcher orphelinerait un lien géré, que la
			// boucle de retrait ne nettoierait plus jamais faute de le voir dans prev.
			if prev[slug] {
				gere[slug] = true
			}
		case info.Mode()&os.ModeSymlink != 0:
			// Un lien existe déjà : l'adopter s'il pointe au bon endroit (Clean pour
			// tolérer un slash final ou un « ./ »), sinon c'est un lien étranger (vers
			// un autre store, un autre outil) qu'on ne touche pas.
			if dest, _ := os.Readlink(lien); filepath.Clean(dest) == cible {
				gere[slug] = true // adoption d'un lien déjà correct
			} else {
				laisses = append(laisses, laisseProjection(slug, skillsRel+"/"+slug+" est un lien géré ailleurs (non créé par Vécu) : skill non projeté, lien laissé intact"))
			}
		default:
			// Un vrai fichier ou dossier occupe le chemin (un skill fait main) :
			// jamais d'écrasement.
			laisses = append(laisses, laisseProjection(slug, skillsRel+"/"+slug+" existe déjà (dossier fait main, non géré par Vécu) : skill non projeté, contenu laissé intact"))
		}
	}

	// Retrait des liens gérés dont le skill a disparu, et eux seuls. On itère le
	// set VALIDÉ (prev), jamais la liste brute.
	for slug := range prev {
		if veut[slug] {
			continue
		}
		lien := filepath.Join(skillsDir, slug)
		info, err := os.Lstat(lien)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue // déjà parti, ou remplacé par un vrai fichier/dossier : ne pas toucher
		}
		if dest, _ := os.Readlink(lien); filepath.Clean(dest) != cibleSymlink(skillsRel, slug) {
			continue // devenu un lien étranger : ne pas toucher
		}
		// TOCTOU accepté : entre ce Readlink et le Remove, un tiers pourrait
		// remplacer le lien. La fenêtre est étroite, le flock exclut un autre
		// processus vecu, et os.Remove ne suit pas un lien - le pire cas est la
		// suppression d'un fichier fraîchement posté à cet instant précis.
		os.Remove(lien) // notre lien devenu inutile : le retirer (échec sans conséquence)
	}

	projetes = make([]string, 0, len(gere))
	for slug := range gere {
		projetes = append(projetes, slug)
	}
	sort.Strings(projetes)
	return projetes, laisses, false
}

// traverseUnLien indique si un composant du chemin relatif `rel` (sous `dir`) est
// un lien symbolique. Même garde que engine.sansLien : écrire ou supprimer à
// travers un tel lien sortirait de la racine locale. Un composant absent n'est
// pas un lien (il n'y a rien à traverser).
func traverseUnLien(dir, rel string) bool {
	courant := dir
	for _, seg := range strings.Split(rel, "/") {
		courant = filepath.Join(courant, seg)
		info, err := os.Lstat(courant)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// laisseProjection : un accroc de projection. Genre serveur : rien n'est perdu -
// le skill est bien sur le disque dans `shared/skills/`, seul son raccourci vers
// l'agent manque.
func laisseProjection(slug, raison string) Laisse {
	return Laisse{Chemin: skillsEspace + "/" + slug, Raison: raison, Genre: GenreServeur}
}
