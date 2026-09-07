package main

import (
	"os/exec"
	"syscall"
)

// ouvrir_windows.go : les deux gestes que `open` rendait sur macOS.
//
// Windows n'a pas de commande unique équivalente. `start` en est une, mais c'est
// une commande INTERNE à cmd.exe : elle obligerait à passer par `cmd /c`, donc à
// traverser les règles de découpage de cmd - et un chemin contenant `&`, `^` ou
// `%` y serait réinterprété. On appelle donc les deux exécutables qui font
// vraiment le travail, chacun recevant son argument sans intermédiaire.

// ouvreDossier (Windows) : révèle un dossier dans l'Explorateur.
//
// `explorer.exe` rend un code de sortie non nul même quand il a réussi, ce qui
// est un comportement connu et documenté depuis longtemps. Sans importance ici :
// on utilise `Start` et non `Run`, donc on n'attend pas le processus et on ne
// lit pas son code.
func ouvreDossier(chemin string) error {
	cmd := exec.Command("explorer.exe", chemin)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

// ouvreURL (Windows) : ouvre une adresse dans le navigateur par défaut.
//
// `FileProtocolHandler` est le point d'entrée que Windows expose pour « ouvre ça
// avec ce qui est associé au protocole ». Il reçoit l'URL comme un argument
// unique, sans passer par un interpréteur de commandes.
func ouvreURL(url string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
