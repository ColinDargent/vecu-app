package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// L'équivalent Windows du test `osacompile` de dossier_darwin_test.go, et pour
// exactement la même raison, citée telle quelle : « un défaut ne se voit qu'en
// cliquant, et cliquer demande un humain devant l'écran ».
//
// Le parseur de PowerShell analyse un script SANS L'EXÉCUTER : il rend les
// erreurs de syntaxe et rien d'autre. C'est donc ce qui attrape la faute de
// frappe et l'échappement cassé - les deux seules classes qu'une relecture rate
// vraiment - sans qu'aucune fenêtre ne s'ouvre.
// Source: https://learn.microsoft.com/en-us/dotnet/api/system.management.automation.language.parser.parsefile
func TestLesSeptScriptsPowerShellSontSyntaxiquementValides(t *testing.T) {
	// Des valeurs HOSTILES, pas des valeurs de démonstration : c'est
	// l'échappement qu'on teste, et il ne casse que sur ces caractères-là.
	messageRetors := `L'apostrophe, le $(dollar), le "guillemet" & le <chevron>`
	cheminRetors := `C:\Users\o'brien\R&D "brouillon"`

	cas := []struct {
		nom    string
		script string
	}{
		{"choisirDossier", psScriptChoisirDossier(messageRetors)},
		{"informe", psScriptInforme(messageRetors)},
		{"boutons", psScriptBoutons(messageRetors, []string{"L'envoyer", "Annuler"})},
		{"demandeTexte", psScriptDemandeTexte(messageRetors, cheminRetors)},
		{"choisirDansListe", psScriptChoisirDansListe(messageRetors, []string{cheminRetors, "l'autre"})},
		{"demandeMotDePasse", psScriptDemandeMotDePasse(messageRetors)},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			if err := analysePowerShell(t, preambulePS+c.script); err != nil {
				t.Errorf("script %s invalide : %v", c.nom, err)
			}
		})
	}
}

// analysePowerShell écrit le script et le fait analyser par PowerShell.
//
// Par FICHIER et non par chaîne : passer le script en argument reposerait sur
// l'échappement de la ligne de commande, c'est-à-dire précisément la chose que
// ce test doit pouvoir déclarer cassée.
func analysePowerShell(t *testing.T, script string) error {
	t.Helper()
	chemin := filepath.Join(t.TempDir(), "script.ps1")
	if err := os.WriteFile(chemin, []byte(script), 0o644); err != nil {
		t.Fatalf("écriture du script : %v", err)
	}

	verif := `
$erreurs = $null
[System.Management.Automation.Language.Parser]::ParseFile('` + chemin + `', [ref]$null, [ref]$erreurs) | Out-Null
if ($erreurs -and $erreurs.Count -gt 0) {
	$erreurs | ForEach-Object { Write-Output $_.ToString() }
	exit 1
}
Write-Output 'OK'
`
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command", verif).CombinedOutput()
	if err != nil {
		return &erreurAnalyse{strings.TrimSpace(string(out))}
	}
	if !strings.Contains(string(out), "OK") {
		return &erreurAnalyse{strings.TrimSpace(string(out))}
	}
	return nil
}

type erreurAnalyse struct{ detail string }

func (e *erreurAnalyse) Error() string { return e.detail }

// TestLeMarqueurDAnnulationSurvitALEncodage : le marqueur traverse Go, l'encodage
// UTF-16LE, base64, PowerShell, puis revient. S'il était altéré au passage, une
// annulation se lirait comme une réponse - donc un accord, et le geste partirait.
func TestLeMarqueurDAnnulationSurvitALEncodage(t *testing.T) {
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"[Console]::Out.Write('"+marqueurAnnule+"')").CombinedOutput()
	if err != nil {
		t.Fatalf("powershell : %v (%s)", err, out)
	}
	if strings.TrimSpace(string(out)) != marqueurAnnule {
		t.Fatalf("marqueur altéré : %q", string(out))
	}
}
