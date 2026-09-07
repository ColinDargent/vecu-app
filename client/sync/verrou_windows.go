package sync

import (
	"os"

	"golang.org/x/sys/windows"
)

// poseVerrouExclusif (Windows) : `LockFileEx` exclusif et non bloquant.
//
// Les deux drapeaux reproduisent `flock(LOCK_EX|LOCK_NB)` terme à terme :
// LOCKFILE_EXCLUSIVE_LOCK (0x2) demande un verrou exclusif plutôt que partagé,
// LOCKFILE_FAIL_IMMEDIATELY (0x1) fait rendre la main tout de suite au lieu
// d'attendre. Sans le second, un deuxième moteur resterait bloqué là sans rien
// dire, ce qui est précisément le défaut que ce verrou existe pour éviter.
//
// On verrouille UN octet à l'offset 0, pas le fichier entier : Windows
// verrouille des plages, pas des fichiers, et « verrouiller une région au-delà
// de la fin du fichier n'est pas une erreur » - le fichier de verrou peut donc
// rester vide, comme sur unix.
//
// L'`Overlapped` porte l'offset de début de plage et doit être fourni même en
// synchrone ; ses champs à zéro valent offset 0, et `HEvent` nul est explicitement
// autorisé.
//
// Relâche : le système libère les verrous d'un processus qui meurt, comme flock.
// La doc note que ça peut prendre un instant selon les ressources disponibles -
// donc un redémarrage immédiat du moteur après un crash peut, en théorie, voir
// le verrou encore posé. C'est sans conséquence ici : le superviseur relance
// après un délai, et un refus est de toute façon lisible et non destructeur.
// Source: https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-lockfileex
func poseVerrouExclusif(f *os.File) error {
	var chevauchement windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, // réservé, doit être nul
		1, // un octet (poids faible)
		0, // poids fort
		&chevauchement,
	)
}
