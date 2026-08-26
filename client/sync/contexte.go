package sync

// contexte.go : F3 - le contexte suit les skills.
//
// Un skill est portable par le standard `SKILL.md`, et le lot 1 l'a rendu
// visible des trois outils. Le contexte, lui, ne l'est pas : Claude Code lit
// `CLAUDE.md`, Cursor et Codex lisent `AGENTS.md`. Vécu synchronise ces
// fichiers comme n'importe quel texte et ne fait rien pour qu'un autre outil
// les trouve - un dossier Vécu ouvert dans Cursor est muet.
//
// L'OPTION, tranchée par DT6 et vérifiée contre les trois docs officielles :
// `AGENTS.md` porte le contenu, à la racine de l'espace, et le `CLAUDE.md` de
// l'espace l'importe par `@AGENTS.md`. Le standard porte le contexte, Claude
// Code lit un fichier d'une ligne, et rien n'existe en double.
//
//   - Claude Code ne lit pas `AGENTS.md` et documente exactement ce motif
//     (« create a CLAUDE.md that imports it »), imports relatifs résolus par
//     rapport au fichier qui importe.
//     Source: https://code.claude.com/docs/en/memory
//   - Cursor lit `AGENTS.md` à la racine et dans les sous-dossiers.
//     Source: https://cursor.com/docs/context/rules
//   - Codex fusionne les `AGENTS.md` de la racine du dépôt jusqu'au dossier
//     courant.
//     Source: https://learn.chatgpt.com/docs/agent-configuration/agents-md
//
// LA BRANCHE QUE VÉCU NE PEUT PAS PRENDRE, et la doc la propose :
// `ln -s AGENTS.md CLAUDE.md`. Le lien serait vu par le scan, poussé au
// serveur, et se heurterait à la garde `sansLien` du moteur.
//
// LA RÈGLE QUI FERME LA QUESTION DU FICHIER FAIT MAIN : Vécu ne MODIFIE jamais
// un `CLAUDE.md`, il ne le CRÉE que s'il est absent. Et le prédicat n'est pas
// « ai-je écrit ce fichier » mais « ce fichier importe-t-il AGENTS.md ».
//
//   - Un registre « fichiers que j'ai posés » serait FAUX chez l'autre : le
//     pointeur est synchronisé, il arrive sur le second poste dont le registre
//     est vide, et ce poste-là le lirait « fait main ». Le contenu est le seul
//     juge que les deux postes partagent. Conséquence : F3 n'ajoute AUCUN champ
//     d'état, donc aucune migration et aucun champ de plus à ne pas oublier
//     dans `clone()`.
//   - Le prédicat par l'import répond à la vraie question. Un `CLAUDE.md`
//     riche, écrit à la main, qui porte déjà `@AGENTS.md`, est un état NOMINAL.
//     Un prédicat par égalité de contenu le classerait « fait main ».
//   - Modifier un fichier synchronisé qu'on n'a pas écrit ouvre le mode d'échec
//     d'août : deux postes qui ajoutent la ligne chacun de leur côté produisent
//     une copie de conflit sur le fichier de contexte de l'équipe. La création,
//     elle, est sûre parce que le contenu est identique des deux côtés - mesuré
//     et non déduit : `git merge-tree --allow-unrelated-histories` rend exit 0
//     quand le chemin porte le même contenu de part et d'autre, exit 1 s'il
//     diffère.
//
// D'où la contrainte de CORRECTION, et pas de style, sur `contenuPointeur` :
// aucune date, aucun nom de machine, aucun compte. Identique à l'octet près sur
// tous les postes, ou la création fabrique des copies de conflit.

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// FichierContexteAgents : le fichier que Cursor et Codex lisent tel quel.
const FichierContexteAgents = "AGENTS.md"

// FichierContexteClaude : le fichier que Claude Code lit, et lui seul.
const FichierContexteClaude = "CLAUDE.md"

// importAgents : la ligne d'import, telle que la doc de Claude Code l'écrit.
const importAgents = "@" + FichierContexteAgents

// contenuPointeur : le `CLAUDE.md` que Vécu pose à côté d'un `AGENTS.md`.
//
// INVARIANT : aucune part variable. Voir l'en-tête du fichier - deux postes
// doivent produire le même octet, sinon leurs créations concurrentes se
// terminent en copie de conflit. Un test golden fige cette constante.
//
// Le commentaire HTML de bloc est retiré par Claude Code AVANT l'injection en
// contexte (documenté sur la page mémoire) : il ne coûte aucun jeton, et il
// évite qu'on trouve un jour un fichier d'une ligne sans savoir d'où il sort.
const contenuPointeur = importAgents + `

<!-- Posé par Vécu, une seule fois, à la création. Le contexte de cet espace
     vit dans AGENTS.md : Cursor et Codex le lisent tel quel, Claude Code
     l'importe par la ligne ci-dessus. Écrire le contexte dans AGENTS.md.
     Ce fichier s'édite librement : tant que la ligne d'import reste, Vécu
     n'y touche plus jamais. -->
`

// importeAgents : ce `CLAUDE.md` importe-t-il `AGENTS.md` ?
//
// APPROXIMATION ASSUMÉE, et elle doit l'être. La doc dit que l'analyse d'import
// saute les blocs de code et les `code spans` : « writing `@README` keeps the
// text literal, while @README outside backticks imports the file ». On
// reproduit la règle - retrait des blocs clôturés, puis des segments entre
// accents graves - sans réimplémenter un parseur Markdown contre une
// implémentation qu'on ne contrôle pas.
//
// Se tromper ici ne coûte JAMAIS un fichier : dans un sens une remarque de
// trop, dans l'autre une remarque en moins. C'est ce qui autorise
// l'approximation ; sur un chemin d'écriture elle serait inacceptable.
// Source: https://code.claude.com/docs/en/memory
func importeAgents(contenu string) bool {
	dansBloc := false
	for _, ligne := range strings.Split(contenu, "\n") {
		// Bloc clôturé : ``` ou ~~~ en tête de ligne (jusqu'à trois espaces
		// d'indentation, comme CommonMark). La même clôture ouvre et ferme ; on
		// ne suit pas la longueur de la clôture, ce qui suffit ici.
		if estCloture(ligne) {
			dansBloc = !dansBloc
			continue
		}
		if dansBloc {
			continue
		}
		if mentionneImport(horsAccentsGraves(ligne)) {
			return true
		}
	}
	return false
}

// estCloture : cette ligne ouvre ou ferme un bloc de code clôturé ?
func estCloture(ligne string) bool {
	l := strings.TrimLeft(ligne, " ")
	if len(ligne)-len(l) > 3 {
		return false // au-delà de trois espaces, c'est du code indenté, pas une clôture
	}
	return strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~")
}

// horsAccentsGraves retire les `code spans` d'une ligne.
//
// Découpage sur l'accent grave : les segments de rang PAIR sont hors span, les
// impairs dedans. Un accent grave non refermé laisse donc la fin de ligne hors
// analyse, ce qui est le repli prudent - on rate un import plutôt que d'en
// inventer un.
func horsAccentsGraves(ligne string) string {
	morceaux := strings.Split(ligne, "`")
	var b strings.Builder
	for i := 0; i < len(morceaux); i += 2 {
		b.WriteString(morceaux[i])
		b.WriteByte(' ') // séparateur : deux segments recollés ne doivent pas former un mot
	}
	return b.String()
}

// mentionneImport cherche `@AGENTS.md` comme un chemin ENTIER.
//
// Les deux bornes comptent, et chacune ferme un faux positif réel :
//   - à droite, `@AGENTS.md.bak` et `@AGENTS.mdx` désignent un AUTRE fichier ;
//   - à gauche, un caractère de mot collé (`x@AGENTS.md`) n'est pas un import
//     à cet endroit.
func mentionneImport(ligne string) bool {
	reste := ligne
	for {
		i := strings.Index(reste, importAgents)
		if i < 0 {
			return false
		}
		avant := byte(' ')
		if i > 0 {
			avant = reste[i-1]
		}
		apres := byte(' ')
		if fin := i + len(importAgents); fin < len(reste) {
			apres = reste[fin]
		}
		if !caractereDeChemin(avant) && !caractereDeChemin(apres) {
			return true
		}
		reste = reste[i+len(importAgents):]
	}
}

// caractereDeChemin : ce caractère prolongerait-il un nom de fichier ?
func caractereDeChemin(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.' || c == '/' || c == '-' || c == '_':
		return true
	}
	return false
}

// --- l'étape d'après-cycle -------------------------------------------------

// Contexte rend le contexte de chaque espace monté lisible des trois outils.
//
// Étape d'après-cycle, comme `Adopte`, `Projette` et `Signale` : elle lit ce
// que le cycle vient d'écrire, ne touche jamais à la logique de sync, et ne
// peut pas faire échouer un cycle. Son écriture part au cycle suivant.
//
// L'ORDRE COMPTE, et il est sûr par construction. Tourner APRÈS `SyncOnce`
// laisse partir la suppression de l'ancien `CLAUDE.md` avant qu'un pointeur
// soit posé à sa place. Tourner avant recréerait le fichier dans le cycle même
// qui le supprime : le `mv CLAUDE.md AGENTS.md` ne partirait jamais.
func (e *Engine) Contexte() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	var laisses []Laisse
	for _, espace := range e.state.Espaces {
		// Rien du tout sur un espace en lecture seule pour ce compte, ni écriture
		// ni remarque.
		//
		// Écrire y serait une faute franche : le fichier serait créé localement,
		// le push renverrait 403, et `pushLocal` poserait une Laisse de genre
		// `fichier` - celle qui fait sortir `vecu sync` en erreur, à chaque cycle,
		// indéfiniment. Le déluge de F6 reconstruit ailleurs.
		//
		// Et signaler sans pouvoir agir ne vaut pas mieux : une remarque dont la
		// seule suite possible est « demander à quelqu'un d'autre » est du bruit
		// sur la surface qui doit rester lisible. La remarque va à qui peut agir.
		if !e.peutEcrire(espace) {
			delete(e.contexteDit, espace)
			continue
		}
		if l, aDire := e.contexteEspace(espace); aDire {
			laisses = append(laisses, l)
		}
	}
	if len(laisses) == 0 {
		return nil
	}
	// Mêmes gestes que `Projette` : `SyncOnce` a déjà figé `state.Laisses` avant
	// de rendre la main, donc une remarque posée ici doit être recopiée et
	// persistée par cette étape.
	e.laisses = append(e.laisses, laisses...)
	e.state.Laisses = e.laisses
	return e.state.Save(e.dir)
}

// contexteEspace applique la table de décision à un espace, et rend la remarque
// à poser s'il y en a une.
func (e *Engine) contexteEspace(espace string) (Laisse, bool) {
	racine := e.racineEspace(espace)

	// La racine de l'espace doit être un VRAI dossier. `Lstat` et non `Stat` :
	// un lien qui pointe vers un dossier ferait écrire hors du périmètre, ce que
	// tout le moteur refuse (`sansLien`). Un dossier absent - espace monté mais
	// jamais hydraté - tombe ici aussi, et il n'y a effectivement rien à faire.
	if info, err := os.Lstat(racine); err != nil || !info.IsDir() {
		return e.rienADire(espace)
	}

	agents, obstacleAgents := etatContexte(racine, FichierContexteAgents)
	claude, obstacleClaude := etatContexte(racine, FichierContexteClaude)
	if motif := premierNonVide(obstacleAgents, obstacleClaude); motif != "" {
		return e.remarque(espace, motif+" : Vécu n'a rien écrit ici.")
	}

	relClaude := espace + "/" + FichierContexteClaude
	switch {
	case agents == presenceFichier && claude == presenceAbsente:
		// UNE ABSENCE SUR LE DISQUE N'EST PAS UNE ABSENCE TOUT COURT. Si le
		// chemin est connu de `state.Files`, le serveur porte un `CLAUDE.md` -
		// probablement fait main - qui n'est simplement pas (ou plus) ici : espace
		// fraîchement monté dont le rattrapage a échoué sur ce fichier, ou
		// suppression locale pas encore poussée. Poser le pointeur là-dessus
		// pousserait un ajout sans ancêtre commun contre un contenu DIFFÉRENT,
		// c'est-à-dire une copie de conflit fabriquée par Vécu sur le fichier de
		// contexte de l'équipe. Le rattrapage ramènera le vrai fichier ; s'il
		// s'agissait d'une suppression, elle partira, et le cycle d'après posera
		// le pointeur en toute connaissance de cause.
		if _, connuDuServeur := e.state.Files[relClaude]; connuDuServeur {
			return e.rienADire(espace)
		}
		// Un `.vecuignore` qui écarte ce chemin est une consigne explicite : ne
		// pas gérer ce fichier. Le créer quand même poserait un fichier que la
		// synchronisation ne portera jamais, sans que personne l'ait demandé.
		if e.ignore(relClaude) || e.ignoreSurLeChemin(relClaude) {
			return e.rienADire(espace)
		}
		// Le seul cas où Vécu écrit. Création, jamais modification.
		if err := os.WriteFile(filepath.Join(racine, FichierContexteClaude), []byte(contenuPointeur), 0o644); err != nil {
			return e.remarque(espace, FichierContexteClaude+" non posé ("+err.Error()+
				") : le contexte de "+FichierContexteAgents+" reste illisible de Claude Code.")
		}
		// Écriture directe, et non `e.ecris()` : le refus d'`ecris` pose une
		// Laisse de genre `serveur`, dont le sens est « ce chemin est sur le
		// serveur et n'a pas pu arriver ici ». C'est faux d'un fichier que ce
		// poste crée. Une dizaine de lignes en double contre un message qui ment.
		e.logf("contexte : %s posé dans %s, le contenu de %s devient lisible de Claude Code",
			FichierContexteClaude, espace, FichierContexteAgents)
		return e.rienADire(espace)

	case agents == presenceFichier && claude == presenceFichier:
		contenu, err := os.ReadFile(filepath.Join(racine, FichierContexteClaude))
		if err != nil {
			return e.remarque(espace, FichierContexteClaude+" illisible ("+err.Error()+") : Vécu n'a rien écrit ici.")
		}
		if importeAgents(string(contenu)) {
			return e.rienADire(espace) // état nominal, qu'il vienne de Vécu ou d'une main
		}
		return e.remarque(espace, FichierContexteAgents+" et "+FichierContexteClaude+
			" portent chacun leur contexte et peuvent diverger. Vécu ne modifie jamais un "+
			FichierContexteClaude+" fait main : ajouter « "+importAgents+" » en tête de "+
			espace+"/"+FichierContexteClaude+" fait lire le même contexte aux trois outils.")

	case agents == presenceAbsente && claude == presenceFichier:
		return e.remarque(espace, "le contexte de cet espace n'est lisible que de Claude Code. Cursor et Codex lisent "+
			FichierContexteAgents+", qui n'existe pas ici. Le geste, une fois et d'un seul côté : « mv "+
			espace+"/"+FichierContexteClaude+" "+espace+"/"+FichierContexteAgents+
			" » - Vécu posera le "+FichierContexteClaude+" d'import au cycle suivant.")
	}

	// Les deux absents : pas de contexte dans cet espace, rien à rendre portable.
	return e.rienADire(espace)
}

// remarque : une Laisse de genre `espace`, journalisée UNE FOIS.
//
// Le genre `espace` ne fait sortir aucune commande en erreur (`Perdu()` ne rend
// vrai que sur `""` et `fichier`), ce qui est juste : rien n'est perdu, un
// geste est simplement à faire.
//
// La Laisse est réémise à chaque cycle - c'est son contrat, une photo du
// dernier cycle - mais PAS la ligne de journal. Une situation stable
// journalisée à chaque cycle écrirait environ 5 700 lignes par jour et par
// espace : c'est DT8 en miniature, et il vaut mieux ne pas repayer la leçon.
// Le registre est en mémoire, jamais persisté : le redire une fois au
// redémarrage est le bon prix pour zéro champ d'état.
func (e *Engine) remarque(espace, raison string) (Laisse, bool) {
	if e.contexteDit[espace] != raison {
		if e.contexteDit == nil {
			e.contexteDit = map[string]string{}
		}
		e.contexteDit[espace] = raison
		e.logf("contexte, espace %s : %s", espace, raison)
	}
	return Laisse{Chemin: espace, Raison: raison, Genre: GenreEspace}, true
}

// rienADire : aucune remarque, et le registre de journal oublie cet espace - la
// prochaine remarque, si la situation se dégrade à nouveau, se redira.
func (e *Engine) rienADire(espace string) (Laisse, bool) {
	delete(e.contexteDit, espace)
	return Laisse{}, false
}

// presenceContexte : ce que `Lstat` dit d'un des deux fichiers, dans les seuls
// termes qui décident ici.
type presenceContexte int

const (
	// presenceAbsente : absence CONFIRMÉE. La seule qui autorise à créer.
	presenceAbsente presenceContexte = iota
	// presenceFichier : un fichier ordinaire, qu'on peut lire comme du texte.
	presenceFichier
	// presenceObstacle : lien, dossier, ou `Lstat` en erreur. On ne touche à
	// rien - « je n'ai pas pu regarder » n'est pas « il n'y a rien ».
	presenceObstacle
)

// etatContexte inspecte un fichier à la racine d'un espace. Rend le motif
// lisible quand c'est un obstacle, la chaîne vide sinon.
func etatContexte(racine, nom string) (presenceContexte, string) {
	info, err := os.Lstat(filepath.Join(racine, nom))
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return presenceAbsente, ""
	case err != nil:
		return presenceObstacle, nom + " illisible (" + err.Error() + ")"
	case info.Mode()&os.ModeSymlink != 0:
		// Un lien serait poussé au serveur puis refusé par `sansLien` ; et écrire
		// à travers sortirait du périmètre. La garde de la projection, appliquée
		// ici au contexte.
		return presenceObstacle, nom + " est un lien symbolique"
	case info.IsDir():
		return presenceObstacle, "un dossier occupe " + nom
	case !info.Mode().IsRegular():
		return presenceObstacle, nom + " n'est pas un fichier ordinaire"
	}
	return presenceFichier, ""
}

func premierNonVide(valeurs ...string) string {
	for _, v := range valeurs {
		if v != "" {
			return v
		}
	}
	return ""
}

// DetecteClaude : Claude Code est-il installé sur ce poste ?
//
// Vit ici plutôt que dans le CLI depuis que l'app pose la même question au
// premier lancement. Deux détections auraient dérivé, et deux postes installés
// par les deux chemins n'auraient pas eu la même configuration de départ.
//
// Deux signaux, l'un ou l'autre suffit : la commande dans le PATH, ou le
// dossier de configuration dans le home. Le second attrape une installation
// dont le binaire n'est pas exposé au shell de ce processus - ce qui est le cas
// courant sous launchd, dont le PATH est minimal.
func DetecteClaude() bool {
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	if maison, err := os.UserHomeDir(); err == nil {
		if fi, err := os.Stat(filepath.Join(maison, ".claude")); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}
