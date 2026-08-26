package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// entree : une entrée d'archive, décrite par son nom, son mode et son contenu.
// Typeflag vide = fichier ordinaire.
type entree struct {
	nom      string
	mode     int64
	contenu  string
	typeflag byte
	lien     string
}

// faitArchive écrit un tar.gz à la main.
//
// Volontairement indépendant du code de la release : ce que ces tests doivent
// figer est le CONTRAT DE FORMAT, pas la fidélité du client à son producteur.
// Réutiliser la fonction d'archivage rendrait les deux côtés faux ensemble.
func faitArchive(t *testing.T, entrees []entree) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entrees {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		h := &tar.Header{Name: e.nom, Mode: e.mode, Size: int64(len(e.contenu)), Typeflag: tf, Linkname: e.lien}
		if tf == tar.TypeDir {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("en-tête %q : %v", e.nom, err)
		}
		if tf == tar.TypeReg {
			if _, err := tw.Write([]byte(e.contenu)); err != nil {
				t.Fatalf("contenu %q : %v", e.nom, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bundleInstalle fabrique un bundle en place dont le binaire annonce `version`.
// Rend le chemin de l'exécutable, comme le rendrait os.Executable().
func bundleInstalle(t *testing.T, parent, version string) string {
	t.Helper()
	exe := filepath.Join(parent, "Vécu.app", "Contents", "MacOS", "vecu-app")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// archiveValide : un bundle complet dont le binaire annonce `version`.
func archiveValide(t *testing.T, version string) []byte {
	t.Helper()
	return faitArchive(t, []entree{
		{nom: "Contents/", mode: 0o755, typeflag: tar.TypeDir},
		{nom: "Contents/Info.plist", mode: 0o644, contenu: "<plist/>"},
		{nom: "Contents/MacOS/", mode: 0o755, typeflag: tar.TypeDir},
		{nom: "Contents/MacOS/vecu-app", mode: 0o755, contenu: "#!/bin/sh\necho " + version + "\n"},
	})
}

func versionAnnoncee(t *testing.T, exe string) string {
	t.Helper()
	out, err := exec.Command(exe, "--check").CombinedOutput()
	if err != nil {
		t.Fatalf("%s --check : %v (%s)", exe, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Le chemin nominal : le bundle est remplacé en entier, le binaire installé
// annonce la nouvelle version, et rien ne traîne derrière.
func TestRemplaceBundle_Nominal(t *testing.T) {
	dir := t.TempDir()
	exe := bundleInstalle(t, dir, "v0.2.0")

	if err := remplaceBundle(exe, archiveValide(t, "v0.3.0"), "v0.3.0"); err != nil {
		t.Fatalf("remplaceBundle : %v", err)
	}
	if got := versionAnnoncee(t, exe); got != "v0.3.0" {
		t.Fatalf("le binaire installé annonce %q, attendu v0.3.0", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "Vécu.app", "Contents", "Info.plist")); err != nil {
		t.Fatalf("l'Info.plist du nouveau bundle doit être là : %v", err)
	}
	for _, résidu := range []string{".vecu-maj-v0.3.0", "Vécu.app.ancien"} {
		if _, err := os.Stat(filepath.Join(dir, résidu)); err == nil {
			t.Fatalf("résidu laissé derrière : %s", résidu)
		}
	}
}

// Ce qui disparaît avec le remplacement par bundle : le vecu-app.old que
// l'ancien mécanisme accumulait à l'intérieur du bundle. Il est dans l'ANCIEN
// bundle, donc il part avec lui.
func TestRemplaceBundle_EmporteLeVieuxBinaireResiduel(t *testing.T) {
	dir := t.TempDir()
	exe := bundleInstalle(t, dir, "v0.2.0")
	vieux := exe + ".old"
	if err := os.WriteFile(vieux, []byte("binaire d'une version précédente"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := remplaceBundle(exe, archiveValide(t, "v0.3.0"), "v0.3.0"); err != nil {
		t.Fatalf("remplaceBundle : %v", err)
	}
	if _, err := os.Stat(vieux); err == nil {
		t.Fatal("le vecu-app.old hérité doit disparaître avec l'ancien bundle")
	}
}

// LE test qui compte : quand la sonde refuse, le bundle EN PLACE ne doit pas
// avoir bougé d'un octet. C'est le filet anti-brick.
func TestRemplaceBundle_SondeRefuse_RienNeBouge(t *testing.T) {
	cas := []struct {
		nom     string
		archive func(t *testing.T) []byte
	}{
		{"le binaire annonce une autre version", func(t *testing.T) []byte {
			return archiveValide(t, "v9.9.9") // on attendra v0.3.0
		}},
		{"le binaire sort en erreur", func(t *testing.T) []byte {
			return faitArchive(t, []entree{
				{nom: "Contents/MacOS/", mode: 0o755, typeflag: tar.TypeDir},
				{nom: "Contents/MacOS/vecu-app", mode: 0o755, contenu: "#!/bin/sh\nexit 1\n"},
			})
		}},
		{"le binaire n'est pas exécutable", func(t *testing.T) []byte {
			return faitArchive(t, []entree{
				{nom: "Contents/MacOS/", mode: 0o755, typeflag: tar.TypeDir},
				{nom: "Contents/MacOS/vecu-app", mode: 0o644, contenu: "#!/bin/sh\necho v0.3.0\n"},
			})
		}},
		{"l'archive ne contient aucun binaire", func(t *testing.T) []byte {
			return faitArchive(t, []entree{
				{nom: "Contents/", mode: 0o755, typeflag: tar.TypeDir},
				{nom: "Contents/Info.plist", mode: 0o644, contenu: "<plist/>"},
			})
		}},
		{"l'archive est corrompue", func(t *testing.T) []byte {
			return []byte("ceci n'est pas un gzip")
		}},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			dir := t.TempDir()
			exe := bundleInstalle(t, dir, "v0.2.0")

			if err := remplaceBundle(exe, c.archive(t), "v0.3.0"); err == nil {
				t.Fatal("la mise à jour aurait dû être refusée")
			}
			if got := versionAnnoncee(t, exe); got != "v0.2.0" {
				t.Fatalf("le bundle en place a bougé : il annonce %q au lieu de v0.2.0", got)
			}
			if _, err := os.Stat(filepath.Join(dir, ".vecu-maj-v0.3.0")); err == nil {
				t.Fatal("le temporaire d'extraction doit être nettoyé après un refus")
			}
		})
	}
}

// L'extraction ne doit rien pouvoir écrire hors du dossier de destination, ni
// par un chemin relatif, ni par un chemin absolu, ni par un lien symbolique.
func TestExtraitBundle_RefuseDeSortirDuDossier(t *testing.T) {
	cas := []struct {
		nom     string
		entrees []entree
	}{
		{"remontée par ..", []entree{{nom: "../evade", mode: 0o644, contenu: "x"}}},
		{"remontée profonde", []entree{{nom: "Contents/../../evade", mode: 0o644, contenu: "x"}}},
		{"chemin absolu", []entree{{nom: "/tmp/evade-vecu-test", mode: 0o644, contenu: "x"}}},
		{"lien symbolique", []entree{{nom: "lien", mode: 0o777, typeflag: tar.TypeSymlink, lien: "/etc/passwd"}}},
		{"lien matériel", []entree{{nom: "dur", mode: 0o644, typeflag: tar.TypeLink, lien: "/etc/passwd"}}},
		{"fichier spécial", []entree{{nom: "tube", mode: 0o644, typeflag: tar.TypeFifo}}},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "extraction")
			if err := extraitBundle(faitArchive(t, c.entrees), dest); err == nil {
				t.Fatal("l'entrée aurait dû être refusée")
			}
			// Et surtout : rien n'a été écrit à côté.
			if _, err := os.Stat(filepath.Join(dir, "evade")); err == nil {
				t.Fatal("un fichier a été écrit HORS du dossier d'extraction")
			}
		})
	}
}

// Un même chemin deux fois dans l'archive : on ne comprend pas, on n'installe pas.
func TestExtraitBundle_RefuseUnDoublon(t *testing.T) {
	dir := t.TempDir()
	arch := faitArchive(t, []entree{
		{nom: "Contents/MacOS/", mode: 0o755, typeflag: tar.TypeDir},
		{nom: "Contents/MacOS/vecu-app", mode: 0o755, contenu: "premier"},
		{nom: "Contents/MacOS/vecu-app", mode: 0o755, contenu: "second"},
	})
	if err := extraitBundle(arch, filepath.Join(dir, "e")); err == nil {
		t.Fatal("un chemin présent deux fois doit être refusé")
	}
}

// Un binaire qui n'est pas dans un bundle .app : refuser plutôt que d'échanger
// le dossier parent, qui serait n'importe lequel. Cas d'un build de dev lancé
// depuis /tmp.
func TestRacineBundle_RefuseHorsBundle(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quelque", "part", "vecu-app")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho v0.2.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := racineBundle(exe); err == nil {
		t.Fatal("un exécutable hors bundle doit être refusé")
	}
	if err := remplaceBundle(exe, archiveValide(t, "v0.3.0"), "v0.3.0"); err == nil {
		t.Fatal("remplaceBundle doit refuser hors bundle")
	}
}

// L'échange lui-même, isolé : après coup les deux chemins ont échangé leur
// contenu, et le nom du chemin installé n'a pas bougé — c'est ce qui fait que
// le plist launchd continue de résoudre.
func TestEchangeBundle_LesNomsNeBougentPas(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "Vécu.app")
	b := filepath.Join(dir, ".vecu-maj-v0.3.0")
	for chemin, marque := range map[string]string{a: "ancien", b: "nouveau"} {
		if err := os.MkdirAll(chemin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(chemin, "marque"), []byte(marque), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := echangeBundle(b, a); err != nil {
		t.Fatalf("echangeBundle : %v", err)
	}
	got, err := os.ReadFile(filepath.Join(a, "marque"))
	if err != nil || string(got) != "nouveau" {
		t.Fatalf("le chemin installé porte %q, attendu « nouveau » (err=%v)", got, err)
	}
}

// Le repli, exercé directement. Sur APFS l'échange atomique réussit toujours,
// donc ce chemin ne s'atteint jamais par echangeBundle sur nos machines : le
// tester exige de l'appeler.
func TestEchangeParDoubleRename(t *testing.T) {
	t.Run("nominal : les noms ne bougent pas non plus", func(t *testing.T) {
		dir := t.TempDir()
		a, b := filepath.Join(dir, "Vécu.app"), filepath.Join(dir, ".vecu-maj-v0.3.0")
		for chemin, marque := range map[string]string{a: "ancien", b: "nouveau"} {
			if err := os.MkdirAll(chemin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(chemin, "marque"), []byte(marque), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := echangeParDoubleRename(b, a); err != nil {
			t.Fatalf("echangeParDoubleRename : %v", err)
		}
		got, err := os.ReadFile(filepath.Join(a, "marque"))
		if err != nil || string(got) != "nouveau" {
			t.Fatalf("le chemin installé porte %q, attendu « nouveau » (err=%v)", got, err)
		}
		// Le repli nettoie derrière lui.
		if _, err := os.Stat(a + ".ancien"); err == nil {
			t.Fatal("le .ancien doit être supprimé après un repli réussi")
		}
	})

	// LE cas qui justifie la restauration : le second rename échoue. L'ancien
	// bundle doit revenir en place plutôt que de laisser un poste sans app.
	t.Run("second rename impossible : l'ancien revient en place", func(t *testing.T) {
		dir := t.TempDir()
		installe := filepath.Join(dir, "Vécu.app")
		if err := os.MkdirAll(installe, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(installe, "marque"), []byte("ancien"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Source inexistante : le premier rename réussit, le second échoue.
		absent := filepath.Join(dir, ".vecu-maj-inexistant")

		if err := echangeParDoubleRename(absent, installe); err == nil {
			t.Fatal("un nouveau bundle absent doit faire échouer l'échange")
		}
		got, err := os.ReadFile(filepath.Join(installe, "marque"))
		if err != nil || string(got) != "ancien" {
			t.Fatalf("l'ancien bundle doit être restauré à son emplacement ; lu %q (err=%v)", got, err)
		}
		if _, err := os.Stat(installe + ".ancien"); err == nil {
			t.Fatal("aucun .ancien ne doit subsister après restauration")
		}
	})
}
