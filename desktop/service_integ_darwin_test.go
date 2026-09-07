package main

import (
	"os"
	"testing"
)

// TestSynchroniseServiceLaunchd exerce la vraie mécanique launchd (bootout /
// bootstrap / idempotence) sur un LABEL DE TEST isolé, avec un binaire bidon.
// Gardé par VECU_INTEG=1 : il mute launchd, on ne le lance pas en CI ni dans
// un `go test ./...` ordinaire.
func TestSynchroniseServiceLaunchd(t *testing.T) {
	if os.Getenv("VECU_INTEG") != "1" {
		t.Skip("intégration launchd : lancer avec VECU_INTEG=1")
	}
	const label = "fr.vecu.test-migration"
	c := serviceCfg{
		label:      label,
		binaire:    "/bin/sleep",
		args:       []string{"3600"}, // reste en vie pour KeepAlive
		racine:     t.TempDir(),
		definition: cheminDefinition(label),
		journal:    "/tmp/vecu-test-migration.log",
	}
	// Nettoyage garanti quoi qu'il arrive.
	t.Cleanup(func() {
		_ = bootoutService(label)
		_ = os.Remove(c.definition)
		_ = os.Remove(c.definition + ".pre-vecu-app.bak")
	})

	// 1er appel : installe.
	installe, err := synchroniseService(c)
	if err != nil {
		t.Fatalf("install : %v", err)
	}
	if !installe {
		t.Fatalf("1er appel : doit installer")
	}
	if !serviceCharge(label) {
		t.Fatalf("service non chargé après install")
	}
	if _, err := os.Stat(c.definition); err != nil {
		t.Fatalf("plist absent : %v", err)
	}

	// 2e appel : idempotent (plist inchangé + chargé → no-op).
	installe, err = synchroniseService(c)
	if err != nil {
		t.Fatalf("2e appel : %v", err)
	}
	if installe {
		t.Errorf("2e appel : ne doit rien réinstaller (idempotence)")
	}

	// Changement de config (racine) → réinstalle.
	c.racine = t.TempDir()
	installe, err = synchroniseService(c)
	if err != nil {
		t.Fatalf("3e appel : %v", err)
	}
	if !installe {
		t.Errorf("changement de plist : doit réinstaller")
	}
}
