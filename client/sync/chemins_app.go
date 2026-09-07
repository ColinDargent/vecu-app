package sync

import (
	"os"
	"path/filepath"
)

// chemins_app.go : où Vécu range ce qui lui appartient, sur chaque système.
//
// Ces chemins étaient écrits en dur, onze fois, sous la forme
// `filepath.Join(maison, "Library", "Application Support", "Vecu")`. Ils
// compilaient pour Windows sans broncher et y auraient créé un dossier
// « Library\Application Support » dans le profil de l'utilisateur - un défaut
// que le compilateur ne peut pas voir, et qu'aucun test ne rattrape tant qu'il
// tourne sur macOS.
//
// Le remplacement est SANS EFFET SUR MACOS, et c'est ce qui le rend sûr :
// `os.UserConfigDir` rend `$HOME/Library/Application Support` sur darwin, donc
// exactement les mêmes octets qu'avant. Aucun état à migrer sur les postes
// existants, aucune reprise de config. Un test d'égalité littérale le verrouille.
// Source: https://pkg.go.dev/os#UserConfigDir

// DossierApplication : le dossier de Vécu dans la configuration de l'utilisateur.
//
//   - macOS   : ~/Library/Application Support/Vecu
//   - Windows : %AppData%\Vecu
//
// Repli sur le temporaire si le système ne sait pas répondre, comme avant : un
// état perdu au redémarrage vaut mieux qu'un moteur qui refuse de démarrer.
func DossierApplication() string {
	conf, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "vecu")
	}
	return filepath.Join(conf, "Vecu")
}

// DossierJournaux : où atterrissent les journaux du service et du moteur.
//
// Pas dérivé de `DossierApplication` : macOS a un emplacement dédié aux
// journaux (`~/Library/Logs`) que `os.UserConfigDir` ne rend pas, et ce chemin
// DOIT rester inchangé - le plist launchd des postes en service le pointe, et
// le changer demanderait de réinstaller le service sur chaque poste. La couture
// est donc par système, dans journaux_darwin.go et journaux_autres.go.
func DossierJournaux() string { return dossierJournaux() }
