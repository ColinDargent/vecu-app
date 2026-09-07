package sync

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// L'adoption range les skills créés hors de `shared/skills/`.
//
// Un skill est un dossier `shared/skills/<slug>/` : c'est de là que découlent la
// sync (il est dans l'espace `shared`), les droits par skill (une règle `perms`
// sur ce chemin) et la projection. Un skill créé ailleurs - typiquement
// directement dans `.claude/skills/`, le réflexe naturel de qui crée un skill
// pour Claude Code - n'est donc synchronisé pour personne, sans que rien ne le
// signale.
//
// La règle « range tes skills dans shared/skills/ » existait, mais elle vivait
// dans une note du vault. Une règle de discipline qui vit dans une note ne
// s'applique pas : celle-ci est ici, dans le défaut du client. L'adoption
// DÉPLACE le dossier vers `shared/skills/<slug>/` et pose un lien symbolique à
// la place, exactement là où l'auteur l'avait mis. Pour lui, rien ne change :
// Claude Code suit le lien (cf. projection.go). Pour tout le monde, le skill
// existe enfin.
//
// Deux invariants, dans cet ordre :
//   - le contenu n'est JAMAIS supprimé de sa source avant que la destination
//     soit vérifiée complète ;
//   - aucune garde n'est un paramètre. Tout ce qui protège se dérive de la
//     racine, comme le reste du moteur : les motifs du `.vecuignore`, les
//     espaces montés, le manifeste tiers, le slug. Une garde qu'on passe est
//     une garde qu'on peut éteindre sans que ça se voie à la lecture.

// claudePluginMarqueur : un dossier de skill qui contient ce fichier n'est pas
// un skill mais un plugin (il embarque agents, hooks, serveurs MCP). Il n'est
// pas portable et ne relève pas du stockage neutre de Vécu.
// Source: https://code.claude.com/docs/en/skills (note « Add a .claude-plugin/plugin.json »)
const claudePluginMarqueur = ".claude-plugin/plugin.json"

// skillFichier : le fichier qui fait d'un dossier un skill. Sans lui, Claude
// Code ne charge rien, et Vécu n'a rien à ranger.
const skillFichier = "SKILL.md"

// slugReserve : nom de dossier réservé par Claude Code dans les emplacements de
// skills, où il télécharge les skills activés sur claude.ai. Jamais un skill de
// la boîte, quelle que soit la casse.
// Source: https://code.claude.com/docs/en/skills (« The folder name `synced` is reserved »)
const slugReserve = "synced"

// verrouFichier : manifeste d'un gestionnaire de skills tiers, à côté du dossier
// `.claude/`. Ce qu'il verrouille est installé et versionné ailleurs : Vécu ne
// devient pas le miroir d'un package manager.
const verrouFichier = "skills-lock.json"

// Adoption : trace d'un skill que Vécu a déplacé. Cumulative dans l'état : c'est
// le registre des fichiers que l'outil a bougé sans qu'on le lui demande, il
// doit rester lisible longtemps après le cycle qui l'a produit.
type Adoption struct {
	Slug   string `json:"slug"`
	Source string `json:"source"` // d'où il a été déplacé (relatif à la racine, ou absolu hors racine)
	Date   string `json:"date"`   // RFC 3339, posé par l'appelant
	// Avertissement : ce qui reste à faire à la main. Il voyage avec l'adoption
	// et pas dans les refus, parce que le contenu EST rangé - et parce qu'il doit
	// survivre au cycle suivant : une consigne humaine qui vit quinze secondes
	// dans un daemon n'est pas une consigne.
	Avertissement string `json:"avertissement,omitempty"`
}

// candidatsChemins : de quoi comparer deux photos de refus, raison comprise.
func candidatsChemins(cs []Candidat) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		// Chemin ET raison : la raison est le contenu utile. Comparer les seuls
		// chemins fige le premier motif constaté, donc `status` continue de nommer
		// un fichier supprimé depuis et tait celui qui bloque maintenant.
		out = append(out, c.Chemin+"\x00"+c.Raison)
	}
	return out
}

// Candidat : un dossier qui EST un skill mais que l'adoption a refusé, avec la
// raison. Volontairement PAS une Laisse : une Laisse de genre fichier fait
// sortir `vecu sync` en erreur, et un dossier tiers ou un `synced/` ferait alors
// échouer tous les cycles, indéfiniment. Un rapport qui crie au loup n'est plus
// lu. Ces refus se lisent dans `vecu status`.
type Candidat struct {
	Chemin string `json:"chemin"`
	Raison string `json:"raison"`
}

// Resultat : ce qu'une adoption a produit. `Source` est le chemin normalisé d'où
// le skill vient (relatif à la racine s'il en venait), pour que l'appelant n'ait
// pas à le redériver - c'est exactement ce qui va dans `Adoption.Source`.
type Resultat struct {
	Slug          string
	Source        string
	Avertissement string
}

// Adopte range les skills créés hors de `shared/skills/`, AVANT que le cycle ne
// scanne le disque : les fichiers déplacés sont alors vus comme de nouveaux
// fichiers locaux et poussés dans le même cycle. Côté serveur, la première
// écriture sous `shared/skills/<slug>` est traitée comme une création et
// revendique le skill pour ce compte - il naît donc privé, propriété de celui
// qui l'a poussé.
//
// Étape distincte de SyncOnce, best-effort comme la projection : un accroc
// d'adoption ne fait jamais échouer un cycle. Sans projection active sur ce
// poste, aucun agent à servir : on ne touche pas à `.claude/`.
func (e *Engine) Adopte() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.projeteVers("claude") {
		// Vider les refus du dernier cycle où la projection était active : sans ça
		// ils resteraient affichés indéfiniment, sur un dossier qu'on ne regarde
		// plus.
		if e.state.Candidats != nil {
			e.state.Candidats = nil
			return e.state.Save(e.dir)
		}
		return nil
	}
	dir := racineDe(e.dir) // `--dir ./vault` : une source relative re-jointe sur une racine absolue serait doublée
	adoptables, refuses := candidats(dir)
	change := false
	for _, slug := range adoptables {
		chemin := claudeSkillsRel + "/" + slug
		res, err := adopte(dir, filepath.Join(dir, filepath.FromSlash(chemin)))
		if err != nil {
			// Refusé au moment du geste alors que le scan le disait adoptable : le
			// disque a bougé entre les deux. C'est un refus comme un autre.
			refuses = append(refuses, Candidat{Chemin: chemin, Raison: err.Error()})
			continue
		}
		// Journalisé au moment du geste, pas seulement dans `status` : un
		// déplacement de fichiers que personne n'a demandé ne doit jamais se
		// découvrir après coup.
		e.logf("skill adopté : %s déplacé de %s vers %s/%s", res.Slug, res.Source, skillsEspace, res.Slug)
		if res.Avertissement != "" {
			e.logf("adoption de %s : %s", res.Slug, res.Avertissement)
		}
		// L'avertissement voyage avec l'adoption, jamais dans les refus : le même
		// skill sortirait sinon à la fois sous « adoptés » et sous « non adoptés »,
		// avec un pied de page qui affirme qu'il n'a pas bougé. Et il doit
		// PERSISTER - `Candidats` est une photo du cycle, donc dans le daemon la
		// seule consigne humaine du dispositif vivrait quinze secondes.
		e.state.Adoptes = append(e.state.Adoptes, Adoption{
			Slug:          res.Slug,
			Source:        res.Source,
			Date:          time.Now().Format(time.RFC3339),
			Avertissement: res.Avertissement,
		})
		change = true
	}
	if change || !slices.Equal(candidatsChemins(e.state.Candidats), candidatsChemins(refuses)) {
		e.state.Candidats = refuses
		return e.state.Save(e.dir)
	}
	// Rien n'a bougé : ne pas réécrire state.json à chaque cycle pour rien.
	return nil
}

// Recense liste ce qui SERAIT adopté, sans rien déplacer. Appelée quand le
// cycle n'a pas abouti : l'adoption ne tourne pas dans ce cas (elle déciderait
// en regardant un dépôt périmé), mais se taire ferait croire qu'il n'y a rien en
// attente. Le silence est le vrai danger d'un outil de synchronisation.
func (e *Engine) Recense() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var refuses []Candidat
	if e.projeteVers("claude") {
		adoptables, r := candidats(racineDe(e.dir))
		refuses = r
		for _, slug := range adoptables {
			refuses = append(refuses, Candidat{
				Chemin: claudeSkillsRel + "/" + slug,
				Raison: "prêt à être rangé dans " + skillsEspace + "/, en attente d'un cycle qui aboutit",
			})
		}
	}
	// JAMAIS `e.state.Save` ici. Cette fonction n'est appelée que sur le chemin
	// d'erreur d'un cycle, et `SyncOnce` mute `e.state` au fil de l'eau : il ne
	// sauvegarde qu'une fois, à la fin, si tout a marché. Un `pull` interrompu
	// laisse `Files` avancé et `Head` en retard, et persister ce couple ferait
	// décrire à l'état un cycle qui n'a pas eu lieu. On relit donc le disque, on
	// n'y touche que les refus, et l'état en mémoire reste intact.
	st, err := LoadState(e.dir)
	if err != nil {
		return err
	}
	if slices.Equal(candidatsChemins(st.Candidats), candidatsChemins(refuses)) {
		return nil
	}
	st.Candidats = refuses
	return st.Save(e.dir)
}

// AdoptionManuelle range un skill situé n'importe où, y compris hors de la
// racine synchronisée. C'est le geste explicite qui couvre ce que le cycle ne
// voit pas : un skill créé dans le `~/.claude/skills/` personnel.
//
// Elle ne dépend pas de la projection (quelqu'un qui tape la commande sait ce
// qu'il demande), et elle applique exactement les mêmes gardes que l'adoption
// automatique - le geste est plus explicite, pas moins protégé.
//
// `sansTrace` est vide quand l'adoption a été inscrite dans l'état ; sinon il
// dit POURQUOI elle ne l'a pas été. Le verrou de la
// racine est pris s'il est libre ; s'il ne l'est pas (le service tourne), le
// déplacement se fait quand même - il est sûr, et le cycle suivant poussera le
// skill - mais l'état n'est pas réécrit : deux processus qui écrivent
// state.json se perdent en last-writer-wins. Faire dépendre le rangement de
// l'arrêt du service serait payer une trace au prix du service rendu.
func AdoptionManuelle(dir, source string) (res Resultat, sansTrace string, err error) {
	dir = racineDe(dir)
	lock, errLock := acquireLock(dir)
	if errLock == nil {
		defer lock.Close()
	}
	// La décision « ce slug est libre » se prend sur ce que ce poste a descendu.
	// Le cycle automatique a été déplacé après la sync pour cette raison exacte ;
	// ici on ne peut pas l'imposer (la commande sert justement quand la sync ne
	// tourne pas), donc on prévient.
	perime := vueDuDepotPerimee(dir)
	res, err = adopte(dir, source)
	if err != nil {
		return res, "", err
	}
	if perime != "" {
		res.Avertissement = joint(res.Avertissement, perime)
	}
	if errLock != nil {
		return res, "un autre processus vecu tient ce dossier (le service tourne ?)", nil
	}
	st, err := LoadState(dir)
	if err != nil {
		// Le skill EST rangé : ne pas transformer ça en échec. Mais dire la vraie
		// cause - annoncer un service qui tourne alors que l'état est illisible
		// envoie chercher au mauvais endroit.
		return res, "état local illisible (" + err.Error() + ")", nil
	}
	st.Adoptes = append(st.Adoptes, Adoption{Slug: res.Slug, Source: res.Source, Date: time.Now().Format(time.RFC3339), Avertissement: res.Avertissement})
	if err := st.Save(dir); err != nil {
		return res, "état local non enregistrable (" + err.Error() + ")", nil
	}
	return res, "", nil
}

// joint assemble deux avertissements sans produire de « ; » orphelin.
func joint(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + " ; " + b
}

// vueDuDepotPerimee : ce poste a-t-il une vue assez fraîche de `shared/skills/`
// pour décider qu'un slug est libre ? Rend le motif s'il ne l'a pas.
func vueDuDepotPerimee(dir string) string {
	st, err := LoadState(dir)
	if err != nil {
		return "l'état local est illisible : impossible de savoir si ce slug est déjà pris par quelqu'un d'autre"
	}
	if st.Cycle == "" {
		return "ce poste n'a jamais synchronisé : impossible de savoir si ce slug est déjà pris par quelqu'un d'autre"
	}
	quand, err := time.Parse(time.RFC3339, st.Cycle)
	if err != nil || time.Since(quand) < time.Hour {
		return ""
	}
	return "dernier cycle il y a " + time.Since(quand).Truncate(time.Hour).String() +
		" : si quelqu'un a publié un skill de ce nom depuis, les deux versions entreront en conflit. Lancer `vecu sync` d'abord lève le doute"
}

// CandidatsPersonnels liste les skills du `~/.claude/skills/` de l'utilisateur
// qui pourraient être adoptés dans cette racine.
//
// LECTURE SEULE, et c'est une décision, pas une limite technique : le dossier
// personnel est le poste, pas la boîte, et c'est là que vivent les installs
// tierces. Rien n'en part sans une commande tapée à la main.
//
// Seuls les adoptables sont rendus. Un refus portant sur un skill personnel
// n'est pas actionnable pour la boîte : l'afficher ferait du bruit là où il n'y
// a rien à décider.
func CandidatsPersonnels(dir string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	racine := filepath.Join(home, filepath.FromSlash(claudeSkillsRel))
	if r, _ := relLexical(racineDe(dir), racine); r != "" {
		return nil // le home EST la racine synchronisée : le cycle s'en occupe déjà
	}
	entrees, err := os.ReadDir(racine)
	if err != nil {
		return nil
	}
	var prets []string
	for _, e := range entrees {
		// On propose le chemin RÉSOLU. Si le dossier personnel est atteint à travers
		// un lien (dotfiles, home déplacé), c'est celui-là qui sera accepté - et
		// c'est donc celui qu'on veut voir taper. Proposer le chemin apparent
		// donnerait une commande qui refuse, ou pire, ferait disparaître le skill de
		// la liste sans que personne sache pourquoi.
		chemin := filepath.Join(resolu(racine), e.Name())
		if raison, candidat := evalue(dir, chemin); candidat && raison == "" {
			prets = append(prets, chemin)
		}
	}
	sort.Strings(prets)
	return prets
}

// racineDe : la racine, absolue et nettoyée. Point d'entrée unique, pour que
// personne ne construise de chemin à partir d'un `dir` relatif (`vecu sync --dir
// ./vault`) : une source relative re-jointe sur une racine absolue donnerait un
// chemin doublé, donc inexistant, donc « rien à adopter » en silence.
func racineDe(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Clean(dir)
}

// souslaRacine : `source`, absolu et nettoyé. Un chemin relatif se résout contre
// la RACINE, jamais contre le dossier courant du processus : le daemon n'a
// aucune raison de tourner dans le vault.
func souslaRacine(dir, source string) string {
	if !filepath.IsAbs(source) {
		source = filepath.Join(dir, source)
	}
	return filepath.Clean(source)
}

// contexte : ce que la racine sait, DÉRIVÉ d'elle. Ni l'un ni l'autre n'est un
// paramètre : `LoadIgnores` et `LoadState` prennent déjà `dir`, exactement comme
// `verrousPour` lit le manifeste tiers à côté du `.claude/` dont vient la source.
type contexte struct {
	motifs  []string
	espaces []string
}

func contexteDe(dir string) contexte {
	motifs, _ := LoadIgnores(dir) // illisible = aucun motif, comme dans le moteur
	st, _ := LoadState(dir)
	return contexte{motifs: motifs, espaces: st.Espaces}
}

// ecarte : ce chemin, ou l'un de ses ancêtres, est-il écarté du scan ?
//
// Interroger le seul chemin complet ne suffit pas : un motif qui désigne un
// dossier (« shared/skills ») ne matche pas les fichiers qu'il contient, parce
// que path.Match ne traverse pas les « / ». C'est la même raison qui a fait
// naître engine.ignoreSurLeChemin. Sans ce parcours, on rangerait le skill dans
// un dossier que le scan saute entièrement.
func (c contexte) ecarte(rel string) bool {
	for p := rel; p != "." && p != "/"; p = path.Dir(p) {
		if ignored(p) {
			return true
		}
		if c.ecarteParMotif(p) {
			return true
		}
	}
	return false
}

// ecarteParMotif : écarté par un motif que QUELQU'UN A ÉCRIT, par opposition aux
// défauts intégrés du moteur (`.git`, `.vecu`, `.DS_Store`).
//
// La distinction porte tout le sens du refus sur le contenu d'un skill. Un
// `.DS_Store` que le Finder pose en ouvrant le dossier, ou le `.git/` d'un skill
// installé depuis un dépôt, ne sont pas du contenu : les laisser derrière n'ampute
// rien, et refuser le skill pour ça le rendrait inadoptable À VIE sur un Mac,
// avec un message qui envoie greper un `.vecuignore` vide. Un motif écrit à la
// main, lui, désigne du contenu que quelqu'un a décidé de ne pas synchroniser -
// et là, le skill arriverait vraiment amputé chez les autres.
func (c contexte) ecarteParMotif(rel string) bool {
	for p := rel; p != "." && p != "/"; p = path.Dir(p) {
		for _, motif := range c.motifs {
			if correspond(motif, p) {
				return true
			}
		}
	}
	return false
}

// espaceMonte : cet espace est-il descendu sur ce poste ?
//
// Un état vide (premier démarrage, avant le premier cycle) répond non, et c'est
// le bon défaut : ranger un skill dans un espace non monté le rendrait invisible
// à la sync ET bloquerait le montage futur de cet espace - aligneMontage refuse
// d'adopter un dossier local préexistant non vide.
func (c contexte) espaceMonte(nom string) bool { return slices.Contains(c.espaces, nom) }

// etatSkillMd : ce que le dossier dit de son SKILL.md.
type etatSkillMd int

const (
	mdAbsent     etatSkillMd = iota // pas de SKILL.md : ce n'est pas un skill, rien à dire
	mdRegulier                      // SKILL.md ordinaire : adoptable
	mdIrregulier                    // SKILL.md présent mais lien/socket : c'est un skill, refusé
	mdCasse                         // seulement une variante de casse (skill.md) : c'est un skill ici, invisible ailleurs
	mdIllisible                     // dossier non parcourable : on ne peut rien affirmer, mais on le dit
)

// evalue répond « ce dossier peut-il être adopté ? ».
//
// `source` est le chemin du dossier de skill ; le slug est son nom de dossier,
// jamais un paramètre. C'est le nom de dossier qui devient la commande côté
// agent, et c'est lui que la projection attend : les laisser diverger poserait
// un deuxième lien, orphelin, que rien ne nettoierait jamais.
//
// Deux sorties distinctes, et la nuance porte tout le bruit de la commande :
//   - `candidat` faux : ce n'est pas un skill du tout (un lien, un dossier sans
//     SKILL.md). Rien à dire, personne n'attend quoi que ce soit.
//   - `candidat` vrai avec une `raison` : c'est bien un skill, et il n'a pas été
//     adopté. Ça, ça se dit.
func evalue(dir, source string) (raison string, candidat bool) {
	dir = racineDe(dir)
	source = souslaRacine(dir, source)
	slug := filepath.Base(source)
	// Lstat et pas Stat : un lien ne doit jamais être suivi ici. Les liens de
	// `.claude/skills/` sont soit ceux de la projection, soit ceux d'un autre
	// gestionnaire - dans les deux cas le contenu vit déjà ailleurs.
	// Source: https://pkg.go.dev/os#Lstat
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}

	// Les refus qui se lisent sur le seul nom passent AVANT l'ouverture du
	// dossier : un `synced/` illisible doit sortir sous « réservé », la vraie
	// règle, pas sous « illisible ».
	if refus := refusDuNom(slug); refus != "" {
		return refus, true
	}

	md, etat := entreeSkillMd(source)
	switch etat {
	case mdAbsent:
		return "", false
	case mdIllisible:
		return "dossier illisible : impossible de savoir si c'est un skill adoptable", true
	case mdIrregulier:
		return skillFichier + " n'est pas un fichier ordinaire (lien symbolique ?) : non adopté pour ne pas décider seul de son sort", true
	case mdCasse:
		return "porte un « " + md.Name() + " » et pas un « " + skillFichier + " » : il fonctionne ici parce que le disque ignore la casse, mais il serait invisible sur un poste Linux", true
	}

	// À partir d'ici c'est un skill : tout refus se dit.
	//
	// Le chemin de la source d'abord : écrire ou supprimer à travers un lien
	// sortirait de la racine. La comparaison est LEXICALE ici, volontairement -
	// on veut détecter le lien, pas le résoudre.
	if refus := refusDuChemin(dir, source); refus != "" {
		return refus, true
	}
	if _, err := os.Stat(filepath.Join(source, filepath.FromSlash(claudePluginMarqueur))); err == nil {
		return "ce dossier est un plugin (" + claudePluginMarqueur + "), pas un skill portable", true
	}
	// Le manifeste tiers est LU ICI, à côté du dossier `.claude/` dont vient la
	// source, et jamais reçu d'un appelant : une protection optionnelle n'en est
	// pas une.
	if verrousPour(dir, source)[slug] {
		return "installé et versionné par un gestionnaire tiers (" + verrouFichier + ") : il n'a pas à vivre dans le dépôt", true
	}
	// Destination. Les questions qui se règlent sur le disque d'abord : en régime
	// permanent le refus le plus fréquent est « existe déjà », et il ne doit pas
	// coûter un parse complet de l'état à chaque cycle.
	destRel := skillsEspace + "/" + slug
	dest := filepath.Join(dir, filepath.FromSlash(destRel))
	if imbriques(source, dest) {
		return "la source et " + destRel + " sont imbriqués : rien n'est déplacé dans son propre sous-dossier", true
	}
	if traverseUnLien(dir, skillsEspace) {
		return skillsEspace + " est (ou traverse) un lien symbolique : déplacer à travers sortirait de la racine", true
	}
	if raison := destinationAccueillante(dir, slug); raison != "" {
		return raison, true
	}
	ctx := contexteDe(dir)
	espace, _, _ := strings.Cut(skillsEspace, "/")
	if !ctx.espaceMonte(espace) {
		return "l'espace « " + espace + " » n'est pas monté sur ce poste : y ranger le skill le rendrait invisible à la sync", true
	}
	if ctx.ecarte(destRel) {
		return destRel + " est écarté par le " + ignoreFichier + " : l'adopter le rendrait invisible à la sync", true
	}
	if err := arbreTransportable(source, destRel, ctx); err != nil {
		return err.Error(), true
	}
	// Le lien de remplacement ne doit jamais atterrir DANS un espace synchronisé :
	// le scan y verrait un lien symbolique, donc une Laisse de genre fichier, donc
	// `vecu sync` sortirait en erreur à chaque cycle - sur un fichier que Vécu
	// aurait lui-même créé.
	if rel, dedans := relLexical(dir, source); dedans {
		if nom, _, _ := strings.Cut(rel, "/"); ctx.espaceMonte(nom) {
			return "la source est dans l'espace synchronisé « " + nom + " » : le lien de remplacement y ferait échouer chaque cycle", true
		}
	}
	return "", true
}

// refusDuNom : ce que le seul nom de dossier interdit. Chaque refus nomme la
// règle qui a tiré - « nom invalide » sans dire laquelle envoie chercher au
// mauvais endroit.
func refusDuNom(slug string) string {
	if strings.EqualFold(slug, slugReserve) {
		return "« " + slugReserve + " » est un nom réservé par Claude Code (skills téléchargés depuis claude.ai) : jamais adopté"
	}
	if espaceValide(slug) {
		return ""
	}
	switch {
	case slug == "" || len(slug) > 64:
		return "nom de dossier trop long (64 caractères au plus) : c'est lui qui devient un chemin synchronisé"
	case strings.HasPrefix(slug, "."), strings.HasPrefix(slug, "-"), strings.HasSuffix(slug, "."):
		return "nom de dossier invalide (ni point ni tiret en tête, ni point final) : c'est lui qui devient la commande côté agent"
	}
	return "nom de dossier invalide (caractères non ASCII, ou suffixe de paquet macOS) : c'est lui qui devient un chemin synchronisé sur tous les postes"
}

// refusDuChemin : le chemin de la source interdit-il d'y toucher ?
//
// Écrire ou supprimer à travers un lien symbolique sortirait du périmètre qu'on
// croit manipuler. La comparaison est LEXICALE : on cherche à détecter le lien,
// pas à le résoudre.
//
// Hors de la racine (adoption manuelle), le plafond du parcours est le dossier
// personnel. Le cas réel est un `~/.claude` géré par un dépôt de dotfiles : le
// contenu vit ailleurs, et Vécu ne doit pas le sortir d'un dépôt tiers sur la
// foi d'un chemin qui ne dit pas où il mène. Refuser en NOMMANT le chemin réel
// laisse la décision à qui la comprend : relancer dessus est un accord explicite.
func refusDuChemin(dir, source string) string {
	if rel, dedans := relLexical(dir, source); dedans {
		if traverseUnLien(dir, path.Dir(rel)) {
			return "un composant de " + path.Dir(rel) + " est un lien symbolique : rien n'est déplacé à travers"
		}
		return ""
	}
	// Hors racine, on ne cherche pas un plafond de parcours : on compare le chemin
	// donné à sa résolution. Toute différence veut dire qu'un composant est un
	// lien, sans avoir à savoir lequel ni jusqu'où remonter.
	//
	// Le défaut sûr est le REFUS. La version précédente s'appuyait sur
	// os.UserHomeDir comme plafond, donc ne gardait rien quand HOME n'était pas
	// exporté (launchd, systemd, cron, `env -i`) ni quand le dépôt vivait hors du
	// dossier personnel (/opt, /Volumes, un second compte). L'asymétrie allait
	// dans le sens destructif : sortir du contenu d'un dépôt tiers sans un mot.
	parent := filepath.Dir(source)
	reel := resolu(parent)
	if reel == parent {
		return ""
	}
	return "ce chemin passe par un lien symbolique : le contenu vit en réalité dans " +
		filepath.Join(reel, filepath.Base(source)) +
		" - relancer sur ce chemin-là exactement, pour le sortir de son dépôt en connaissance de cause"
}

// destinationAccueillante : le chemin de destination peut-il recevoir le skill ?
//
// Trois questions, pas une. Le dossier parent doit exister comme dossier (un
// `shared` qui est un fichier ferait échouer le MkdirAll après coup, sur un
// candidat annoncé adoptable). La destination exacte ne doit pas exister : on ne
// devine jamais une fusion. Et aucun homonyme de casse ne doit s'y trouver :
// `Daily` et `daily` cohabitent sur ext4 et collisionnent sur le Mac du binôme -
// le même raisonnement que pour le SKILL.md, appliqué au nom qui devient un
// chemin synchronisé partout.
func destinationAccueillante(dir, slug string) string {
	destRel := skillsEspace + "/" + slug
	parent := filepath.Join(dir, filepath.FromSlash(skillsEspace))
	// De la racine vers la destination : chaque ancêtre qui existe doit être un
	// dossier. Descendre plutôt que remonter, parce qu'un ancêtre qui est un
	// fichier fait échouer le Lstat des chemins EN DESSOUS sur ENOTDIR, pas sur
	// ENOENT - une remontée prendrait ça pour « n'existe pas encore ».
	p := dir
	for _, seg := range strings.Split(skillsEspace, "/") {
		p = filepath.Join(p, seg)
		info, err := os.Lstat(p)
		if err != nil {
			break // n'existe pas encore : MkdirAll s'en chargera
		}
		if !info.IsDir() {
			rel, _ := relLexical(dir, p)
			return rel + " existe et n'est pas un dossier : la destination ne peut pas être créée"
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(destRel))); err == nil {
		return destRel + " existe déjà : renommer le dossier local, ou le supprimer si c'est un doublon"
	}
	entrees, err := os.ReadDir(parent)
	if err != nil {
		return "" // parent absent : rien ne peut collisionner
	}
	for _, e := range entrees {
		if e.Name() != slug && strings.EqualFold(e.Name(), slug) {
			return skillsEspace + "/" + e.Name() + " existe déjà et ne diffère que par la casse : les deux collisionneraient sur un poste macOS"
		}
	}
	return ""
}

// entreeSkillMd cherche le SKILL.md du dossier, à la casse EXACTE.
//
// os.Lstat ne suffit pas : APFS est insensible à la casse, donc un `skill.md`
// passerait ici et ne serait jamais projeté sur un poste Linux (SkillsMontes
// compare la chaîne exacte). Une adoption invisible chez l'autre est le pire
// mode de défaillance pour un outil de synchronisation - donc on le dit, plutôt
// que d'écarter en silence un dossier qui, pour son auteur, fonctionne.
// ReadDir rend les noms tels qu'ils sont sur le disque.
// Source: https://pkg.go.dev/os#ReadDir
func entreeSkillMd(source string) (fs.DirEntry, etatSkillMd) {
	entrees, err := os.ReadDir(source)
	if err != nil {
		return nil, mdIllisible
	}
	var voisin fs.DirEntry
	for _, e := range entrees {
		switch {
		case e.Name() == skillFichier:
			if e.Type().IsRegular() {
				return e, mdRegulier
			}
			return e, mdIrregulier
		case strings.EqualFold(e.Name(), skillFichier):
			voisin = e
		}
	}
	if voisin != nil {
		return voisin, mdCasse
	}
	return nil, mdAbsent
}

// verrousPour : le manifeste tiers qui protège CE dossier de skills. Il vit à
// côté du dossier `.claude/` dont la source dépend - la racine du dépôt pour un
// skill du projet, le home pour un skill personnel. Sans ancêtre `.claude`, on
// retombe sur celui de la racine plutôt que sur aucune protection.
func verrousPour(dir, source string) map[string]bool {
	for p := filepath.Dir(source); p != filepath.Dir(p); p = filepath.Dir(p) {
		if filepath.Base(p) == ".claude" {
			return verrousSkills(filepath.Dir(p))
		}
	}
	return verrousSkills(dir)
}

// verrousSkills lit les slugs verrouillés par un gestionnaire tiers dans
// `<parent>/skills-lock.json`. Absent, illisible ou malformé : aucun verrou. Un
// manifeste cassé ne doit pas empêcher une adoption, il ne fait que retirer une
// protection.
func verrousSkills(parent string) map[string]bool {
	brut, err := os.ReadFile(filepath.Join(parent, verrouFichier))
	if err != nil {
		return nil
	}
	var manifeste struct {
		Skills map[string]json.RawMessage `json:"skills"`
	}
	if err := json.Unmarshal(brut, &manifeste); err != nil {
		return nil
	}
	verrous := make(map[string]bool, len(manifeste.Skills))
	for slug := range manifeste.Skills {
		verrous[slug] = true
	}
	return verrous
}

// imbriques : l'un de ces deux chemins est-il sous l'autre ? Copier un dossier
// dans son propre sous-dossier part en récursion et remplit le disque avant de
// s'arrêter sur un nom trop long.
//
// Comparaison insensible à la casse : sur APFS, `<dir>/SHARED` EST
// `<dir>/shared`, et une garde purement lexicale se ferait contourner par le cas
// le plus banal du disque de destination.
func imbriques(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a == b || sousChemin(a, b) || sousChemin(b, a)
}

func sousChemin(parent, enfant string) bool {
	rel, err := filepath.Rel(parent, enfant)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// arbreTransportable refuse un skill que le dépôt ne pourrait pas porter tel quel.
//
// Ce n'est pas la copie qui pose problème - os.CopyFS reporte les liens tels
// quels, y compris un lien absolu ou qui s'évade de l'arbre. C'est justement ça :
// un skill qui embarque un lien vers `/etc/hosts` ou vers un dossier hors du
// vault serait rangé dans le dépôt avec, et partirait chez tout le monde en
// promettant un contenu qui n'existe que sur ce poste. Refuser est plus honnête
// que trancher en silence. WalkDir ne suit pas les liens, il les rapporte.
// Source: https://pkg.go.dev/path/filepath#WalkDir
func arbreTransportable(source, destRel string, ctx contexte) error {
	return filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("dossier illisible (%s) : %v", filepath.Base(p), err)
		}
		rel, _ := filepath.Rel(source, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("contient une entrée qui n'est ni un fichier ni un dossier (%s) : non adopté pour ne pas décider seul de son sort", rel)
		}
		// Ce que la sync ne porterait pas. Tant que le skill vit dans `.claude/`,
		// rien ne le scanne et son contenu est indifférent ; une fois rangé dans
		// `shared/`, un fichier que le transport refuse devient une Laisse de genre
		// fichier À CHAQUE CYCLE, donc `vecu sync` en code d'erreur pour toujours -
		// à cause d'un geste que personne n'a demandé. Et un fichier ignoré part en
		// silence : le skill arriverait amputé chez les autres.
		if ctx.ecarteParMotif(path.Join(destRel, rel)) {
			return fmt.Errorf("contient %s, que le %s écarte : rangé dans le dépôt, ce skill arriverait amputé chez les autres", rel, ignoreFichier)
		}
		contenu, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("fichier illisible (%s) : %v", rel, err)
		}
		if !utf8.Valid(contenu) {
			return fmt.Errorf("contient un fichier binaire (%s), que la sync ne transporte pas : rangé dans le dépôt, il ferait échouer chaque cycle", rel)
		}
		return nil
	})
}

// candidats parcourt `.claude/skills/` de la racine et classe ce qu'il y trouve.
// Rend les slugs adoptables et les refus à afficher. Les entrées qui ne sont pas
// des skills sont écartées en silence.
func candidats(dir string) (adoptables []string, refuses []Candidat) {
	dir = racineDe(dir)
	// Même garde que la projection : si `.claude` ou `.claude/skills` est un lien,
	// on n'écrit ni ne supprime à travers - ça sortirait de la racine.
	if traverseUnLien(dir, claudeSkillsRel) {
		return nil, []Candidat{{
			Chemin: claudeSkillsRel,
			Raison: ".claude/ ou .claude/skills/ est un lien symbolique : rien n'est adopté depuis ce dossier",
		}}
	}
	racine := filepath.Join(dir, filepath.FromSlash(claudeSkillsRel))
	entrees, err := os.ReadDir(racine)
	switch {
	case os.IsNotExist(err):
		return nil, nil // pas de dossier de skills sur ce poste : rien à dire
	case err != nil:
		// Illisible n'est pas absent : droits, volume décroché. Le taire ferait
		// croire qu'il n'y a rien à adopter.
		return nil, []Candidat{{Chemin: claudeSkillsRel, Raison: "dossier illisible (" + err.Error() + ") : rien n'a pu être examiné"}}
	}
	for _, e := range entrees {
		slug := e.Name()
		raison, candidat := evalue(dir, filepath.Join(racine, slug))
		switch {
		case !candidat:
		case raison != "":
			refuses = append(refuses, Candidat{Chemin: claudeSkillsRel + "/" + slug, Raison: raison})
		default:
			adoptables = append(adoptables, slug)
		}
	}
	sort.Strings(adoptables)
	sort.Slice(refuses, func(i, j int) bool { return refuses[i].Chemin < refuses[j].Chemin })
	return adoptables, refuses
}

// adopte déplace le dossier de skill `source` vers `<dir>/shared/skills/<slug>`
// et pose un lien de remplacement à la place. Le slug est le nom de dossier de
// la source, jamais un paramètre.
//
// Le lien est posé ICI, dans le même geste que le déplacement, et pas laissé à
// la projection de fin de cycle : si le push échoue (serveur injoignable), le
// skill n'entre pas dans l'état, la projection ne tourne même pas (Cycle
// n'appelle Projette qu'après un SyncOnce réussi), et le dossier aurait disparu
// de sous les pieds de son auteur le temps de la panne.
//
// Un avertissement n'est jamais une erreur : le contenu est rangé, et le dire
// comme une erreur enverrait relancer une adoption que le passage suivant
// refuserait.
func adopte(dir, source string) (Resultat, error) {
	dir = racineDe(dir)
	source = souslaRacine(dir, source)
	res := Resultat{Slug: filepath.Base(source), Source: source}
	if rel, dedans := relLexical(dir, source); dedans {
		res.Source = rel
	}
	if raison, candidat := evalue(dir, source); !candidat || raison != "" {
		if !candidat {
			raison = "ce n'est pas un dossier de skill (lien symbolique, ou " + skillFichier + " absent)"
		}
		return res, fmt.Errorf("%s", raison)
	}
	dest := filepath.Join(dir, filepath.FromSlash(skillsEspace), res.Slug)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return res, fmt.Errorf("dossier %s non créable : %w", skillsEspace, err)
	}
	reste, err := deplace(source, dest)
	if err != nil {
		return res, err
	}
	if reste != "" {
		// La source n'a pas pu être entièrement retirée. os.RemoveAll supprime les
		// enfants d'abord : selon ce qui a bloqué, il reste un dossier vide (droits
		// sur le parent) ou une partie du contenu. Dans les deux cas ce résidu
		// occupe le chemin du lien : ne pas tenter le Symlink (il échouerait sur
		// « file exists ») et ne rien promettre. Tant qu'il est là, la projection le
		// verra comme un dossier fait main et refusera de le remplacer, à chaque
		// cycle. C'est une action humaine, pas une attente.
		res.Avertissement = reste + " - tant qu'il est là, l'agent local ne verra pas le skill : le retirer à la main"
		return res, nil
	}
	if err := os.Symlink(cibleRemplacement(dir, source, res.Slug), source); err != nil {
		// Pas de retour en arrière : le contenu est au bon endroit, c'est ce qui
		// compte. Le lien sera posé par la projection, mais seulement au prochain
		// cycle qui aboutit.
		res.Avertissement = "skill rangé dans " + skillsEspace + "/" + res.Slug + ", mais le lien de remplacement n'a pas pu être posé (" + err.Error() + ") : l'agent local ne le verra qu'au prochain cycle qui aboutit"
	}
	return res, nil
}

// cibleRemplacement : où pointe le lien laissé à la place du skill déplacé.
//
// Source dans la racine : cible RELATIVE, qui survit à un déplacement de la
// racine. Depuis `.claude/skills/`, elle vaut exactement `../../shared/skills/
// <slug>`, donc la projection reconnaît le lien comme le sien et l'adopte sans
// le recréer (cf. projeterClaude).
//
// Source hors racine (adoption manuelle depuis `~/.claude/skills/`) : cible
// ABSOLUE. Un chemin relatif remonterait le disque de l'utilisateur, et ne
// voudrait plus rien dire si l'un des deux bougeait.
//
// La décision se prend sur les chemins RÉSOLUS, pas sur les chaînes : `/var` et
// `/private/var` désignent le même dossier sur macOS, et APFS ignore la casse.
// Une source jugée « hors racine » pour une différence d'orthographe basculerait
// le lien en absolu, que la projection signalerait à chaque cycle sans jamais
// l'adopter.
func cibleRemplacement(dir, source, slug string) string {
	dest := filepath.Join(dir, filepath.FromSlash(skillsEspace), slug)
	dr, sr := resolu(dir), resolu(filepath.Dir(source))
	rel, err := filepath.Rel(dr, sr)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return dest // hors racine : absolu
	}
	depuis, err := filepath.Rel(sr, filepath.Join(dr, filepath.FromSlash(skillsEspace), slug))
	if err != nil {
		return dest
	}
	// `ToSlash`, et ce n'est pas cosmétique : cette chaîne doit être EXACTEMENT
	// celle que rend `cibleSymlink`, sinon la projection prend le lien pour un
	// lien étranger et refuse d'y toucher. `filepath.Rel` rend des antislashs sur
	// Windows là où `cibleSymlink` rend des barres obliques - les deux
	// divergeaient donc sur ce système, et le même dossier se synchronisant entre
	// un Mac et un PC, chacun aurait boudé les liens de l'autre en silence.
	//
	// Le repli absolu ci-dessus garde, lui, les séparateurs du système : il ne
	// voyage pas d'une machine à l'autre, puisqu'il désigne un chemin hors racine.
	return filepath.ToSlash(depuis)
}

// resolu : le chemin avec ses liens résolus, ou le chemin tel quel si la
// résolution échoue (chemin absent, boucle de liens).
// Source: https://pkg.go.dev/path/filepath#EvalSymlinks
func resolu(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// relLexical : le chemin de `source` relatif à la racine, en séparateurs slash,
// et s'il est bien sous la racine - au sens des chaînes, sans résoudre les liens.
// C'est la bonne question quand on cherche à DÉTECTER un lien sur le chemin ;
// pour décider où pointe un lien de remplacement, voir cibleRemplacement.
func relLexical(dir, source string) (string, bool) {
	rel, err := filepath.Rel(dir, source)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// deplace déplace un dossier, en préservant l'invariant « rien n'est supprimé
// avant que la destination soit vérifiée complète ».
//
// Chemin nominal : os.Rename, atomique et instantané sur un même volume.
// « OS-specific restrictions may apply when oldpath and newpath are in different
// directories » - un déplacement entre volumes échoue, ce qui est le cas normal
// d'une adoption manuelle depuis `~/`.
// Source: https://pkg.go.dev/os#Rename
//
// La destination est refusée ICI si elle existe, et pas seulement dans `evalue` :
// POSIX autorise Rename à remplacer un répertoire vide (darwin renvoie EEXIST,
// Linux non), donc laisser la garde chez l'appelant la ferait dépendre du noyau.
//
// `reste` non vide : le contenu est bien arrivé, mais la source n'a pas pu être
// entièrement retirée.
func deplace(source, dest string) (reste string, err error) {
	if _, err := os.Lstat(dest); err == nil {
		return "", fmt.Errorf("%s existe déjà : rien n'est déplacé par-dessus", filepath.Base(dest))
	}
	if err := os.Rename(source, dest); err == nil {
		return "", nil
	}
	return copieVerifiee(source, dest)
}

// copieVerifiee : le repli de `deplace`, isolé pour être testable seul (un test
// ne peut pas provoquer un échec de Rename sur un même volume).
//
// Ce repli n'est PAS équivalent au chemin nominal. CopyFS crée les fichiers en
// 0o666 plus les bits d'exécution de la source, ne reporte pas les dates, et
// dédouble les liens durs. Un fichier privé (0600) devient donc lisible par tous
// une fois rangé dans le dépôt. Assumé : le contenu part de toute façon sur le
// serveur, qui ne transporte ni modes ni dates - le mode local n'a jamais été ce
// qui protège quoi que ce soit dans Vécu.
// Source: https://pkg.go.dev/os#CopyFS
func copieVerifiee(source, dest string) (reste string, err error) {
	// Refuser une destination existante AVANT d'écrire quoi que ce soit. CopyFS
	// copie ce qui ne collisionne pas puis échoue sur le premier conflit, en
	// laissant en place ce qui était déjà là : le nettoyage de la destination
	// partielle effacerait alors du contenu du dépôt. On ne nettoie que ce qu'on a
	// soi-même créé, et pour le savoir il faut avoir constaté l'absence.
	if _, err := os.Lstat(dest); err == nil {
		return "", fmt.Errorf("%s existe déjà : rien n'est copié par-dessus", filepath.Base(dest))
	}
	if err := os.CopyFS(dest, os.DirFS(source)); err != nil {
		os.RemoveAll(dest) // destination que NOUS venons de créer : ne pas la laisser derrière
		return "", fmt.Errorf("copie vers %s impossible : %w", filepath.Base(dest), err)
	}
	if err := memeArbre(source, dest); err != nil {
		os.RemoveAll(dest)
		return "", err
	}
	if err := os.RemoveAll(source); err != nil {
		return "contenu bien rangé, mais le dossier d'origine " + source + " n'a pas pu être retiré (" + err.Error() + ")", nil
	}
	return "", nil
}

// memeArbre vérifie que la copie a tout emporté : mêmes chemins relatifs, mêmes
// tailles. C'est la condition qui autorise à supprimer la source, donc elle ne
// se déduit pas de « CopyFS n'a pas renvoyé d'erreur ».
//
// Taille et pas empreinte : le contenu vient d'être écrit à partir de la source,
// dans le même processus. Ce qu'on cherche à attraper est une copie tronquée ou
// incomplète (disque plein, volume décroché), pas une corruption silencieuse.
//
// Lstat des deux côtés, jamais Stat : WalkDir rend un lstat côté source (la
// taille du lien), et un Stat côté destination rendrait celle de sa cible. La
// vérification qui autorise à supprimer la source ne doit pas comparer deux
// grandeurs différentes et crier « tronqué » sur une copie parfaite.
// Source: https://pkg.go.dev/os#Lstat
func memeArbre(source, dest string) error {
	return filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("source illisible pendant la vérification (%s) : %v", filepath.Base(p), err)
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		cp, errDest := os.Lstat(filepath.Join(dest, rel))
		if d.IsDir() {
			// Les dossiers comptent aussi : un sous-dossier vide perdu par la copie
			// est du contenu perdu, et la suppression de la source le rendrait
			// définitif.
			if errDest != nil {
				return fmt.Errorf("copie incomplète (dossier %s manquant) : la source est laissée intacte", rel)
			}
			return nil
		}
		src, err := d.Info()
		if err != nil {
			return fmt.Errorf("source illisible pendant la vérification (%s) : %v", rel, err)
		}
		if errDest != nil {
			return fmt.Errorf("copie incomplète (%s manquant) : la source est laissée intacte", rel)
		}
		if cp.Size() != src.Size() {
			return fmt.Errorf("copie incomplète (%s tronqué : %d octets sur %d) : la source est laissée intacte", rel, cp.Size(), src.Size())
		}
		return nil
	})
}
