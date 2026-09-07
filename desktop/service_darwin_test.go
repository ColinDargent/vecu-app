package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContenuPlist(t *testing.T) {
	c := serviceCfg{
		label:      "fr.vecu.sync",
		binaire:    "/Users/x/Applications/Vécu.app/Contents/MacOS/vecu-app",
		racine:     "/Users/x/second-brain",
		definition: "/ignoré",
		journal:    "/Users/x/Library/Logs/vecu-app.log",
	}
	p := c.contenuPlist()

	must := []string{
		"<string>fr.vecu.sync</string>",
		"<string>/Users/x/Applications/Vécu.app/Contents/MacOS/vecu-app</string>",
		"<key>VECU_SERVICE</key>", // le marqueur qui distingue l'instance de service
		"<key>KeepAlive</key>",
		"<key>SuccessfulExit</key>", // KeepAlive crash-only : Quitter reste possible
		"<false/>",
		"<key>RunAtLoad</key>",                   // démarrage initial + au login
		"<string>/Users/x/second-brain</string>", // WorkingDirectory
	}
	for _, m := range must {
		if !strings.Contains(p, m) {
			t.Errorf("plist ne contient pas %q\n---\n%s", m, p)
		}
	}
}

func TestContenuPlistEchappeXML(t *testing.T) {
	c := serviceCfg{label: "l", binaire: "/b", racine: "/Users/a & b/vault", journal: "/j"}
	if !strings.Contains(c.contenuPlist(), "/Users/a &amp; b/vault") {
		t.Errorf("le & du chemin n'est pas échappé")
	}
}

func TestRacineDepuisPlist(t *testing.T) {
	// Un plist façon ancien daemon CLI : « vecu start --dir <racine> ».
	dir := t.TempDir()
	p := filepath.Join(dir, "old.plist")
	contenu := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>fr.vecu.sync</string>
	<key>ProgramArguments</key>
	<array>
		<string>/opt/homebrew/bin/vecu</string>
		<string>start</string>
		<string>--dir</string>
		<string>/Users/x/second-brain</string>
	</array>
</dict>
</plist>`
	if err := os.WriteFile(p, []byte(contenu), 0o644); err != nil {
		t.Fatal(err)
	}
	racine, ok := racineDeclaree(p)
	if !ok || racine != "/Users/x/second-brain" {
		t.Errorf("racineDeclaree = %q, %v ; veut /Users/x/second-brain, true", racine, ok)
	}

	// Un plist sans --dir (l'app migrée) : pas de racine à apprendre.
	p2 := filepath.Join(dir, "app.plist")
	os.WriteFile(p2, []byte(`<?xml version="1.0"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>ProgramArguments</key><array><string>/x/vecu-app</string></array></dict></plist>`), 0o644)
	if _, ok := racineDeclaree(p2); ok {
		t.Errorf("un plist sans --dir ne doit pas rendre de racine")
	}
}
