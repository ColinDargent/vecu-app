//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// suffixeExecutable : rien sur unix, où un fichier exécutable se reconnaît à son
// bit de permission et non à son nom.
const suffixeExecutable = ""

// bascule remplace `cible` par `tmpNom` de façon atomique (os.Rename sur le même
// système de fichiers), après avoir conservé l'ancien binaire sous `cible.old`
// (lien dur = filet de recovery manuel, sans copie). fsync du dossier pour que
// l'entrée renommée survive à une coupure.
//
// L'ordre compte, et il est l'INVERSE de celui de Windows : ici on peut écraser
// un exécutable en cours d'exécution, parce que le noyau garde le contenu vivant
// tant qu'un processus le tient ouvert. Le rename est donc le dernier geste, et
// il est atomique.
func bascule(tmpNom, cible string) error {
	vieux := cible + ".old"
	_ = os.Remove(vieux)
	_ = os.Link(cible, vieux) // best-effort : recovery manuel si le neuf plante
	if err := os.Rename(tmpNom, cible); err != nil {
		os.Remove(tmpNom)
		return fmt.Errorf("remplacement : %w", err)
	}
	if d, err := os.Open(filepath.Dir(cible)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
