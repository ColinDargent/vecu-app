package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/colindargent/vecu/appupdate"
	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// TestAutoUpdate_BoutEnBout : la chaîne complète contre le VRAI serveur et la
// VRAIE release produite par `make release` — manifeste scellé sur disque, clé
// publique compilée dans l'app, binaire réel qui répond à --check. Prouve que
// chercheMaj + remplaceBinaire installent une release authentique de bout en bout.
//
// Se skip proprement là où la preuve n'a pas de sens : hors darwin (le binaire
// darwin ne s'exécute pas), ou si appdist/ n'a pas été construit.
func TestAutoUpdate_BoutEnBout(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("release darwin : exécutable seulement sur macOS")
	}
	cible := "darwin-" + runtime.GOARCH
	appdist, _ := filepath.Abs("../appdist")
	if _, err := os.Stat(filepath.Join(appdist, "vecu-app-"+cible)); err != nil {
		t.Skipf("appdist/ non construit pour %s (lancer `make release VERSION=…`)", cible)
	}

	// Vrai serveur, servant le vrai appdist.
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("db : %v", err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatalf("store : %v", err)
	}
	u, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	token, _ := database.IssueToken(u.ID, "test")
	srv := httptest.NewServer((&api.Server{DB: database, Store: store, AppDist: appdist}).Handler())
	t.Cleanup(srv.Close)

	// Clé publique RÉELLE compilée dans l'app (pas une clé de test).
	pub, err := clePublique()
	if err != nil {
		t.Fatalf("clé compilée : %v", err)
	}

	// La version attendue se LIT dans le manifeste scellé, elle ne s'écrit pas ici.
	// Codée en dur, elle devenait fausse a la release suivante : le test rougissait
	// en permanence, sur une barriere qui garde justement le chemin de release.
	attendue, err := versionDuManifeste(appdist, pub)
	if err != nil {
		t.Fatalf("manifeste de release : %v", err)
	}

	// Vu depuis un poste en v0.2.0, la release scellée doit être détectée, vérifiée
	// (signature + empreinte) et téléchargée.
	maj, err := chercheMaj(context.Background(), srv.Client(), srv.URL, token, pub, "v0.2.0", cible)
	if err != nil {
		t.Fatalf("chercheMaj : %v", err)
	}
	if maj.Version != attendue {
		t.Fatalf("version détectée = %q, attendu %q", maj.Version, attendue)
	}

	// Le format n'est PAS supposé : il dépend de ce que la release sur disque
	// publie. Une release antérieure à v0.9.0 n'a pas de bundle, une release
	// récente en a un, et le test doit rester juste dans les deux cas. Supposer
	// « bin » le rendrait faux à la première release qui publie des bundles.
	switch maj.Format {
	case formatBundle:
		// Installation réelle dans un faux bundle : la sonde exécute le binaire
		// EXTRAIT et n'échange les dossiers que s'il démarre sur la bonne version.
		racine := filepath.Join(dir, "Vécu.app")
		exe := filepath.Join(racine, "Contents", "MacOS", "vecu-app")
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, []byte("#!/bin/sh\necho v0.2.0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := remplaceBundle(exe, maj.Octets, attendue); err != nil {
			t.Fatalf("remplaceBundle (sonde incluse) : %v", err)
		}
		if err := sondeSante(exe, attendue); err != nil {
			t.Fatalf("le bundle installé doit être la %s : %v", attendue, err)
		}
		// Le bundle installé est un VRAI bundle scellé, pas seulement un binaire.
		if _, err := os.Stat(filepath.Join(racine, "Contents", "Info.plist")); err != nil {
			t.Fatalf("le bundle installé doit porter son Info.plist : %v", err)
		}
		// Aucun résidu : ni le temporaire d'extraction, ni le repli.
		for _, r := range []string{filepath.Join(dir, ".vecu-maj-"+attendue), racine + ".ancien"} {
			if _, err := os.Stat(r); err == nil {
				t.Fatalf("résidu laissé derrière : %s", r)
			}
		}
	default:
		// Chemin hérité : remplacement du binaire à l'intérieur du bundle.
		dest := filepath.Join(dir, "vecu-app")
		if err := os.WriteFile(dest, []byte("#!/bin/sh\necho v0.2.0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := remplaceBinaire(dest, maj.Octets, attendue); err != nil {
			t.Fatalf("remplaceBinaire (sonde incluse) : %v", err)
		}
		if err := sondeSante(dest, attendue); err != nil {
			t.Fatalf("le binaire installé doit être la %s : %v", attendue, err)
		}
		if _, err := os.Stat(dest + ".old"); err != nil {
			t.Fatalf("le binaire précédent doit être conservé en .old : %v", err)
		}
	}
}

// versionDuManifeste lit la version de la release scellée dans appdist/, en
// ouvrant le manifeste avec la clé publique compilée dans l'app. La source de
// vérité de ce que le test attend est donc la release elle-même.
func versionDuManifeste(appdist string, pub ed25519.PublicKey) (string, error) {
	brut, err := os.ReadFile(filepath.Join(appdist, "manifest.json"))
	if err != nil {
		return "", err
	}
	var sm appupdate.SignedManifest
	if err := json.Unmarshal(brut, &sm); err != nil {
		return "", err
	}
	m, err := appupdate.Open(pub, sm)
	if err != nil {
		return "", err
	}
	return m.Version, nil
}
