package main

import "os/exec"

// ouvreDossier (macOS) : révèle un dossier dans le Finder.
func ouvreDossier(chemin string) error { return exec.Command("open", chemin).Start() }

// ouvreURL (macOS) : ouvre une adresse dans le navigateur par défaut.
func ouvreURL(url string) error { return exec.Command("open", url).Start() }
