package sync

import (
	"os"
	"path/filepath"
)

// dossierJournaux (macOS) : ~/Library/Logs.
//
// L'emplacement système des journaux utilisateur, et surtout le chemin que
// pointent les plists launchd DÉJÀ INSTALLÉS sur les postes en service
// (StandardOutPath / StandardErrorPath). Le changer obligerait à réinstaller le
// service partout : il est figé, et c'est délibéré.
func dossierJournaux() string {
	maison, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(maison, "Library", "Logs")
}
