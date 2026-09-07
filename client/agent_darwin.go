package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// installeAgentSysteme (macOS) écrit le LaunchAgent et le charge.
//
// Clés : Label et ProgramArguments sont requis ; KeepAlive à true garde le
// daemon en vie et implique déjà le lancement au chargement. RunAtLoad n'est
// délibérément PAS posé : la page de manuel dit « This key should be avoided,
// as speculative job launches have an adverse effect on system-boot and
// user-login scenarios », et KeepAlive suffit.
// Sources: man launchd.plist, man launchctl (vérifiés le 2026-07-24) ;
// https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html
func installeAgentSysteme(racine string) error {
	binaire, err := os.Executable()
	if err != nil {
		return err
	}
	if resolu, err := filepath.EvalSymlinks(binaire); err == nil {
		binaire = resolu
	}
	maison, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	agents := filepath.Join(maison, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		return err
	}
	journal := filepath.Join(maison, "Library", "Logs", "vecu.log")
	if err := os.MkdirAll(filepath.Dir(journal), 0o755); err != nil {
		return err
	}
	plist := filepath.Join(agents, labelAgent+".plist")
	contenu := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>start</string>
		<string>--dir</string>
		<string>%s</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, labelAgent, echappeXML(binaire), echappeXML(racine), echappeXML(racine), echappeXML(journal), echappeXML(journal))
	if err := os.WriteFile(plist, []byte(contenu), 0o644); err != nil {
		return err
	}

	cible := fmt.Sprintf("gui/%d", os.Getuid())
	// Retrait d'une éventuelle version précédente : sans bootout, bootstrap
	// échoue sur un service déjà chargé (« service already loaded »).
	// `load`/`unload` sont légués, bootstrap/bootout sont les formes courantes.
	_ = exec.Command("launchctl", "bootout", cible+"/"+labelAgent).Run()
	sortie, err := exec.Command("launchctl", "bootstrap", cible, plist).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap : %v (%s)", err, strings.TrimSpace(string(sortie)))
	}
	return nil
}
