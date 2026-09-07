package main

// dossier_windows.go : l'EXÉCUTION des sept dialogues. Leur construction vit
// dans dialogues_ps.go, qui compile partout et se teste donc depuis macOS.
//
// POURQUOI PAS D'APPEL DIRECT À L'API WIN32. Les dialogues Windows Forms exigent
// un thread en apartment cloisonné (STA) et une boucle de messages. Ici,
// `systray.Run` possède le thread principal et `fyne.io/systray` n'expose aucun
// moyen public d'y déposer du travail - c'est mot pour mot la contrainte qui
// avait écarté `NSOpenPanel` sur macOS, et elle se résout de la même façon : un
// AUTRE processus. `powershell.exe` démarre en STA depuis la version 3.0.
// Source: https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_powershell_exe
//
// Zéro dépendance ajoutée, zéro CGO, et le même contrat de retour que
// l'implémentation macOS : un texte sur la sortie standard, ou `errAnnule`.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
)

// psAppel exécute un corps de script et rend ce qu'il a écrit, sans le marqueur.
//
// `-EncodedCommand` prend du base64 d'UTF-16LE. Ce n'est pas une précaution de
// style : c'est ce qui supprime ENTIÈREMENT la question de l'échappement au
// niveau de la ligne de commande. Un nom de dossier contenant `"`, `%`, `&` ou
// `^` traverserait cmd.exe en étant réinterprété ; encodé, il n'y a plus de
// ligne de commande à traverser.
//
// `HideWindow` évite le clignotement d'une console noire à chaque dialogue :
// l'app est graphique, elle ne doit jamais faire apparaître de terminal.
func psAppel(corps string) (string, error) {
	script := preambulePS + corps

	// UTF-16LE, en petit-boutiste explicite, tel que la doc l'exige.
	unites := utf16.Encode([]rune(script))
	octets := make([]byte, 0, len(unites)*2)
	for _, u := range unites {
		octets = append(octets, byte(u), byte(u>>8))
	}

	cmd := exec.Command("powershell.exe",
		"-NoProfile", // un profil cassé ne doit pas casser un dialogue
		"-Sta",       // les Windows Forms l'exigent ; défaut depuis PS 3.0, on ne s'y fie pas
		"-EncodedCommand", base64.StdEncoding.EncodeToString(octets),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	sortie, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("dialogue : %w (%s)", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("dialogue : %w", err)
	}

	rep := strings.TrimRight(string(sortie), "\r\n")
	if rep == marqueurAnnule {
		return "", errAnnule
	}
	return rep, nil
}

// choisirDossier ouvre le sélecteur de dossiers de Windows et rend le chemin
// absolu choisi.
func choisirDossier(prompt string) (string, error) {
	return psAppel(psScriptChoisirDossier(prompt))
}

// informe affiche un message. Le fermer n'est pas un échec.
func informe(message string) error {
	_, err := psAppel(psScriptInforme(message))
	if errors.Is(err, errAnnule) {
		return nil
	}
	return err
}

// confirme pose une question à deux issues et rend `true` si la personne a
// choisi d'agir.
//
// LE BOUTON CLIQUÉ SE LIT, il ne se déduit pas de l'absence d'erreur. C'est la
// leçon payée sur macOS, où un bouton nommé « Annuler » rendait un résultat sans
// erreur - donc un accord - et le dossier partait.
func confirme(message, action string) (bool, error) {
	rep, err := psAppel(psScriptBoutons(message, []string{action, "Annuler"}))
	if errors.Is(err, errAnnule) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return rep == action, nil
}

// choix pose une question à plusieurs issues et rend le bouton cliqué, ou la
// chaîne vide si la personne a fermé le dialogue.
//
// Trois boutons au maximum. Windows n'impose pas cette limite (c'était celle
// d'AppleScript), on la garde quand même : c'est la limite d'une décision qu'on
// prend en passant dans un menu, et laisser les deux systèmes diverger sur le
// nombre de boutons ferait diverger l'UX pour rien.
func choix(message string, boutons []string) (string, error) {
	if len(boutons) == 0 || len(boutons) > 3 {
		return "", fmt.Errorf("choix : %d boutons, il en faut de 1 à 3", len(boutons))
	}
	rep, err := psAppel(psScriptBoutons(message, boutons))
	if errors.Is(err, errAnnule) {
		return "", nil
	}
	return rep, err
}

// demandeTexte demande une saisie, avec une valeur proposée.
func demandeTexte(message, defaut string) (string, error) {
	return psAppel(psScriptDemandeTexte(message, defaut))
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
	return psAppel(psScriptChoisirDansListe(message, entrees))
}

// demandeMotDePasse demande une saisie MASQUÉE. Même contrat que demandeTexte,
// sans valeur proposée : proposer un mot de passe n'a aucun sens.
//
// Ce qui est masqué, et ce qui ne l'est pas. `UseSystemPasswordChar` empêche
// l'affichage, et la valeur saisie ne passe JAMAIS par la ligne de commande : le
// script encodé transporte l'invite, la réponse revient par la sortie standard.
// Elle n'est donc pas visible dans la liste des processus - même garantie que
// sur macOS, où c'était `osascript` qui la portait.
//
// En revanche elle est en clair dans la mémoire de ce processus. C'est le même
// niveau qu'ailleurs, et c'est pour ça que l'appelant ne doit jamais la
// journaliser ni la remettre dans un message d'erreur.
func demandeMotDePasse(message string) (string, error) {
	return psAppel(psScriptDemandeMotDePasse(message))
}
