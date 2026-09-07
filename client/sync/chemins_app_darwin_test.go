package sync

import (
	"os"
	"path/filepath"
	"testing"
)

// Le port Windows a remplacé onze chemins écrits en dur par `DossierApplication`
// et `DossierJournaux`. Sur macOS, le remplacement doit être une IDENTITÉ : le
// moindre écart déplacerait l'état, la config de l'app et les verrous des postes
// déjà en service, silencieusement, à la première mise à jour.
//
// Ces deux tests comparent aux chaînes littérales qui étaient dans le code avant
// le port. Ils ne vérifient pas que la fonction est « cohérente avec elle-même »
// (ce qu'une comparaison à `os.UserConfigDir()` ferait, et qui passerait même si
// la valeur changeait) : ils la comparent à ce que les postes ont sur le disque.

func TestDossierApplicationInchangeSurMacOS(t *testing.T) {
	maison, err := os.UserHomeDir()
	if err != nil {
		t.Skip("pas de dossier personnel")
	}
	attendu := filepath.Join(maison, "Library", "Application Support", "Vecu")
	if got := DossierApplication(); got != attendu {
		t.Fatalf("DossierApplication() = %q, attendu %q\n"+
			"Un écart ici déplace l'état de tous les postes macOS en service.", got, attendu)
	}
}

func TestDossierJournauxInchangeSurMacOS(t *testing.T) {
	maison, err := os.UserHomeDir()
	if err != nil {
		t.Skip("pas de dossier personnel")
	}
	// Pointé par StandardOutPath/StandardErrorPath des plists launchd déjà
	// installés : le changer exigerait de réinstaller le service sur chaque poste.
	attendu := filepath.Join(maison, "Library", "Logs")
	if got := DossierJournaux(); got != attendu {
		t.Fatalf("DossierJournaux() = %q, attendu %q\n"+
			"Les plists launchd installés pointent l'ancien chemin.", got, attendu)
	}
}

// Les verrous restent sous le dossier d'application, au même endroit qu'avant.
func TestDossierVerrousInchangeSurMacOS(t *testing.T) {
	maison, err := os.UserHomeDir()
	if err != nil {
		t.Skip("pas de dossier personnel")
	}
	attendu := filepath.Join(maison, "Library", "Application Support", "Vecu", "verrous")
	if got := dossierVerrous(); got != attendu {
		t.Fatalf("dossierVerrous() = %q, attendu %q", got, attendu)
	}
}
