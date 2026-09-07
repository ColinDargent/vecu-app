//go:build !darwin

package sync

import (
	"os"
	"path/filepath"
)

// dossierJournaux (Windows et le reste) : %LocalAppData%\Vecu\Logs.
//
// Windows n'a pas de dossier de journaux utilisateur normalisé. Le cache local
// est l'emplacement retenu par convention pour ce qui est volumineux, régénérable
// et propre à la machine - ce que sont exactement des journaux tournés par
// taille. Il ne suit pas un profil itinérant, contrairement à %AppData%, et
// c'est ce qu'on veut : un journal est local à la machine qui l'écrit.
// Source: https://pkg.go.dev/os#UserCacheDir
func dossierJournaux() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(cache, "Vecu", "Logs")
}
