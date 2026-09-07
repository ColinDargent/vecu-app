package main

import (
	"fmt"
	"os"
)

// suffixeExecutable : « .exe » sur Windows.
//
// Le binaire temporaire de la mise à jour le porte parce qu'il est EXÉCUTÉ avant
// d'être installé (la sonde de santé lance `--check`). `CreateProcess` accepte
// en principe un PE quelle que soit son extension, mais on ne fait pas reposer
// le chemin critique de l'auto-update sur « en principe » : le fichier porte le
// nom que Windows attend.
const suffixeExecutable = ".exe"

// bascule (Windows) remplace `cible` par `tmpNom`, en DEUX renommages.
//
// C'est le point du port où la différence entre les deux systèmes n'est pas
// cosmétique. Windows refuse d'ÉCRASER un exécutable en cours d'exécution
// (`ERROR_ACCESS_DENIED`), mais il accepte de le RENOMMER : le fichier ouvert
// suit son nouveau nom, et le processus continue de tourner sans rien remarquer.
// Toute la mise à jour tient sur cette asymétrie.
//
// L'ordre est donc inversé par rapport à unix :
//
//	unix    : lien dur de sauvegarde, puis rename du neuf SUR l'ancien
//	windows : rename de l'ancien HORS du chemin, puis rename du neuf dedans
//
// Ce que ça coûte : entre les deux renommages, le chemin cible n'existe pas.
// La fenêtre est de l'ordre de la microseconde et rien ne la lit à ce
// moment-là - le processus en cours tient déjà son fichier par son descripteur,
// et le superviseur ne relance qu'après la sortie.
//
// L'ancien binaire est laissé sur place sous `.old` : le supprimer échouerait de
// toute façon tant qu'il est exécuté. Il est retiré au passage suivant, par le
// `os.Remove` du début - c'est-à-dire une fois qu'il ne tourne plus.
func bascule(tmpNom, cible string) error {
	vieux := cible + ".old"
	_ = os.Remove(vieux) // retire celui de la mise à jour précédente, s'il ne tourne plus

	if err := os.Rename(cible, vieux); err != nil {
		os.Remove(tmpNom)
		return fmt.Errorf("mise de côté de l'ancien binaire : %w", err)
	}
	if err := os.Rename(tmpNom, cible); err != nil {
		// Remettre l'ancien : mieux vaut la version d'avant qu'aucune application.
		if reerr := os.Rename(vieux, cible); reerr != nil {
			return fmt.Errorf("remplacement : %w ; RESTAURATION ÉCHOUÉE (%v) : le binaire est sous %s", err, reerr, vieux)
		}
		os.Remove(tmpNom)
		return fmt.Errorf("remplacement : %w", err)
	}
	return nil
}
