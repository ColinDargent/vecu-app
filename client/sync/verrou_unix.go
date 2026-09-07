//go:build !windows

package sync

import (
	"os"
	"syscall"
)

// poseVerrouExclusif (unix) : `flock` exclusif et non bloquant.
//
// `LOCK_NB` fait échouer tout de suite si un autre processus tient déjà le
// fichier, au lieu d'attendre : l'appelant veut un refus lisible, pas un moteur
// qui semble démarrer et reste muet. Le verrou est relâché par le noyau quand le
// descripteur se ferme ou que le processus meurt - donc jamais d'orphelin après
// un crash.
// Source: https://pkg.go.dev/golang.org/x/sys/unix#Flock
func poseVerrouExclusif(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
