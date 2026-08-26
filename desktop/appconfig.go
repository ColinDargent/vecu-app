package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// appConfig : réglages propres à l'app de bureau, distincts de la config du
// moteur (qui vit dans <racine>/.vecu/config.json). Sert surtout à mémoriser
// quelle racine superviser, apprise de l'ancien LaunchAgent à la migration.
type appConfig struct {
	Racine string `json:"racine"`
}

// cheminAppConfig : ~/Library/Application Support/Vecu/app.json.
func cheminAppConfig() string {
	maison, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(maison, "Library", "Application Support", "Vecu", "app.json")
}

// racineAppConfig lit la racine mémorisée, ou "" si le fichier est absent ou
// illisible (cas nominal avant la première migration).
func racineAppConfig() string {
	p := cheminAppConfig()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var c appConfig
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	return c.Racine
}

// ecritAppConfig mémorise la racine à superviser (créée à la migration, relue
// ensuite par le service à chaque démarrage).
func ecritAppConfig(c appConfig) error {
	p := cheminAppConfig()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}
