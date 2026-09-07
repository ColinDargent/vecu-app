package main

// dossier.go : choisir un dossier sur ce poste, sans terminal.
//
// POURQUOI PAS `NSOpenPanel`. Le panneau natif doit tourner sur le thread
// principal et exige une `NSApplication` vivante. Ici, `systray.Run` possède ce
// thread et `fyne.io/systray` n'expose aucun moyen public d'y déposer du
// travail : l'appeler depuis une goroutine serait la faute qui marche neuf fois
// sur dix. Source: https://developer.apple.com/documentation/appkit/nsopenpanel
//
// `osascript` tourne dans un AUTRE processus : pas de contrainte de thread, pas
// de CGO ajouté, pas de dépendance nouvelle. Le dialogue est le même.
// Source: https://developer.apple.com/library/archive/documentation/LanguagesUtilities/Conceptual/MacAutomationScriptingGuide/PromptforaFileorFolder.html

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// activation : l'app est en barre de menus (pas de Dock, pas de fenêtre), donc
// rien ne la met au premier plan toute seule et un dialogue pourrait s'ouvrir
// derrière la fenêtre active.
const activation = "tell application \"System Events\" to activate\n"

// choisirDossier ouvre le sélecteur de dossiers du Finder et rend le chemin
// absolu choisi.
func choisirDossier(prompt string) (string, error) {
	// `activate` avant le dialogue : l'app est en barre de menus (pas de Dock,
	// pas de fenêtre), donc rien ne la met au premier plan toute seule et le
	// dialogue pourrait s'ouvrir derrière la fenêtre active.
	sortie, err := exec.Command("osascript", "-e", scriptChoisirDossier(prompt)).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", interpreteEchec(string(ee.Stderr))
		}
		return "", fmt.Errorf("sélecteur de dossiers : %w", err)
	}
	return nettoieChemin(string(sortie))
}

// interpreteEchec traduit ce qu'osascript a écrit sur sa sortie d'erreur.
//
// L'ANNULATION SE RECONNAÎT AU NUMÉRO, JAMAIS AU TEXTE. Mesuré le 21/08 sur ce
// poste : « 0:17: execution error: Annulé par l'utilisateur. (-128) », en
// français. Le message suit la langue du système, le numéro non - et ce poste
// est en français, donc un test écrit contre « User canceled » passerait au vert
// en laissant le vrai cas cassé.
func interpreteEchec(stderr string) error {
	if strings.Contains(stderr, "(-128)") {
		return errAnnule
	}
	return fmt.Errorf("sélecteur de dossiers : %s", strings.TrimSpace(stderr))
}

// nettoieChemin : `POSIX path of` rend un dossier AVEC un slash final (mesuré :
// « /Users/x/ »). Le laisser passerait un chemin qui ne se compare à
// aucun autre : `filepath.Rel`, les préfixes de montage et les clés de
// `Montages` travaillent tous sur la forme nettoyée.
func nettoieChemin(sortie string) (string, error) {
	chemin := filepath.Clean(strings.TrimRight(sortie, "\r\n"))
	if chemin == "" || chemin == "." || !filepath.IsAbs(chemin) {
		return "", fmt.Errorf("sélecteur de dossiers : chemin inattendu %q", sortie)
	}
	return chemin, nil
}

// litteralAppleScript rend un littéral de chaîne AppleScript.
//
// Les invites d'aujourd'hui sont des constantes de ce dépôt, mais un libellé
// d'espace venu du serveur finira par y passer : une chaîne échappée à la main
// au moment où ça arrivera est une injection de script, et le processus tourne
// avec les droits de la personne.
func litteralAppleScript(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// informe affiche un message avec un seul bouton. Rend une erreur seulement si
// le dialogue n'a pas pu s'afficher : ce que la personne clique n'a aucune
// conséquence, il n'y a rien à décider.
func informe(message string) error {
	_, err := dialogue(scriptInforme(message))
	if errors.Is(err, errAnnule) {
		return nil // fermer une information n'est pas un échec
	}
	return err
}

// confirme pose une question à deux issues et rend `true` si la personne a
// choisi d'agir.
//
// LE BOUTON CLIQUÉ SE LIT, il ne se déduit pas de l'absence d'erreur. Un bouton
// personnalisé nommé « Annuler » n'est pas le bouton d'annulation d'AppleScript :
// la doc d'Apple documente `button returned` et ne promet AUCUN traitement
// particulier pour un nom de bouton. Sans cette lecture, « Annuler » rendait un
// résultat sans erreur, donc un accord - et le dossier partait.
// Source: https://developer.apple.com/library/archive/documentation/LanguagesUtilities/Conceptual/MacAutomationScriptingGuide/PromptforText.html
//
// Le refus est converti en erreur -128 côté AppleScript plutôt que comparé côté
// Go : c'est le même chemin que la touche Échap et que la fermeture du
// dialogue, donc un seul cas à traiter au lieu de trois. Le comportement d'un
// « error number -128 » a été mesuré sur ce poste le 21/08 (sortie 1, « (-128) »
// sur la sortie d'erreur).
func confirme(message, action string) (bool, error) {
	_, err := dialogue(scriptConfirme(message, action))
	if errors.Is(err, errAnnule) {
		return false, nil
	}
	return err == nil, err
}

// choix pose une question à plusieurs issues et rend le bouton cliqué, ou la
// chaîne vide si la personne a fermé le dialogue.
//
// Trois boutons au maximum : c'est la limite d'AppleScript, et c'est aussi la
// limite d'une décision qu'on prend en passant dans un menu. Le premier bouton
// est celui par défaut, donc celui de l'inaction.
func choix(message string, boutons []string) (string, error) {
	if len(boutons) == 0 || len(boutons) > 3 {
		return "", fmt.Errorf("choix : %d boutons, il en faut de 1 à 3", len(boutons))
	}
	rep, err := dialogue(scriptChoix(message, boutons))
	if errors.Is(err, errAnnule) {
		return "", nil
	}
	return rep, err
}

// demandeTexte demande une saisie, avec une valeur proposée. Même raison qu'à
// `confirme` de vérifier le bouton : sans ça, « Annuler » rendrait le texte
// saisi et le geste continuerait comme si la personne avait dit oui.
func demandeTexte(message, defaut string) (string, error) {
	return dialogue(scriptDemandeTexte(message, defaut))
}

// choisirDansListe fait choisir UNE entrée parmi n, quel que soit n.
//
// `choix` ne va pas au-delà de trois boutons, ce qui est la bonne limite pour
// une décision qu'on prend en passant. Choisir parmi ses propres dossiers n'est
// pas cette décision-là : leur nombre appartient à la personne, pas à nous.
func choisirDansListe(message string, entrees []string) (string, error) {
	if len(entrees) == 0 {
		return "", errors.New("liste vide")
	}
	return dialogue(scriptChoisirDansListe(message, entrees))
}

// demandeMotDePasse demande une saisie MASQUÉE. Même contrat que demandeTexte,
// sans valeur proposée : proposer un mot de passe n'a aucun sens.
//
// Ce qui est masqué, et ce qui ne l'est pas. AppleScript n'affiche pas les
// caractères à l'écran, et la valeur saisie ne passe JAMAIS par la ligne de
// commande : le script transporte l'invite, la réponse revient par la sortie
// standard d'`osascript`. Elle n'est donc pas visible dans `ps`.
//
// En revanche elle est en clair dans la mémoire de ce processus, exactement
// comme l'était `promptPassword()` du CLI. C'est le même niveau de garantie, et
// c'est pour ça que l'appelant ne doit jamais la journaliser ni la remettre dans
// un message d'erreur.
func demandeMotDePasse(message string) (string, error) {
	return dialogue(scriptDemandeMotDePasse(message))
}

// dialogue exécute un fragment AppleScript qui affiche quelque chose, et rend
// ce qu'il a écrit sur sa sortie standard.
//
// L'`activate` a la même raison qu'au sélecteur de dossiers : l'app est en barre
// de menus, rien ne la met au premier plan, et un dialogue ouvert derrière la
// fenêtre active est un dialogue que personne ne voit.
func dialogue(fragment string) (string, error) {
	script := activation + fragment
	sortie, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", interpreteEchec(string(ee.Stderr))
		}
		return "", fmt.Errorf("dialogue : %w", err)
	}
	return strings.TrimRight(string(sortie), "\r\n"), nil
}

// Les quatre scripts, construits à part de leur exécution.
//
// Séparés pour une raison précise : un défaut d'AppleScript ne se voit qu'en
// cliquant, et cliquer demande un humain devant l'écran. Isolés ici, ils se
// compilent (`osacompile`) dans un test, sans rien afficher - ce qui attrape la
// faute de syntaxe et l'échappement cassé, les deux seules classes qu'une
// relecture rate vraiment.

func scriptChoisirDossier(prompt string) string {
	return activation + fmt.Sprintf("POSIX path of (choose folder with prompt %s)", litteralAppleScript(prompt))
}

func scriptInforme(message string) string {
	return fmt.Sprintf(`display dialog %s buttons {"OK"} default button "OK" with title "Vécu"`,
		litteralAppleScript(message))
}

func scriptConfirme(message, action string) string {
	return fmt.Sprintf(`set r to (display dialog %s buttons {"Annuler", %s} default button "Annuler" with title "Vécu")
if button returned of r is not %s then error number -128`,
		litteralAppleScript(message), litteralAppleScript(action), litteralAppleScript(action))
}

func scriptChoix(message string, boutons []string) string {
	litteraux := make([]string, 0, len(boutons))
	for _, b := range boutons {
		litteraux = append(litteraux, litteralAppleScript(b))
	}
	return fmt.Sprintf(`button returned of (display dialog %s buttons {%s} default button %s with title "Vécu")`,
		litteralAppleScript(message), strings.Join(litteraux, ", "), litteraux[0])
}

func scriptDemandeTexte(message, defaut string) string {
	return fmt.Sprintf(`set r to (display dialog %s default answer %s buttons {"Annuler", "Continuer"} default button "Continuer" with title "Vécu")
if button returned of r is not "Continuer" then error number -128
text returned of r`, litteralAppleScript(message), litteralAppleScript(defaut))
}

// `choose from list` rend une LISTE, et `false` si la personne annule — pas une
// erreur, d'où le `error number -128` qui la traduit en `errAnnule` comme
// partout ailleurs.
func scriptChoisirDansListe(message string, entrees []string) string {
	litteraux := make([]string, 0, len(entrees))
	for _, e := range entrees {
		litteraux = append(litteraux, litteralAppleScript(e))
	}
	return fmt.Sprintf(`set r to (choose from list {%s} with prompt %s with title "Vécu")
if r is false then error number -128
item 1 of r`, strings.Join(litteraux, ", "), litteralAppleScript(message))
}

// `with hidden answer` masque la saisie. `default answer ""` reste obligatoire :
// c'est lui qui fait apparaître le champ, `with hidden answer` ne fait que le
// masquer.
func scriptDemandeMotDePasse(message string) string {
	return fmt.Sprintf(`set r to (display dialog %s default answer "" with hidden answer buttons {"Annuler", "Continuer"} default button "Continuer" with title "Vécu")
if button returned of r is not "Continuer" then error number -128
text returned of r`, litteralAppleScript(message))
}
