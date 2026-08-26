package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// construitBundle construit le bundle « Vécu.app » d'une cible et rend l'archive
// tar.gz de son CONTENU.
//
// Le bundle est construit par desktop/build-app.sh plutôt que réassemblé ici :
// la disposition du bundle, l'Info.plist et le sceau ad-hoc n'ont qu'une seule
// définition, et c'est ce script. Le dupliquer en Go garantirait qu'un jour les
// deux divergent.
//
// Ce que l'archive contient, et c'est délibéré : le contenu du bundle en chemins
// RELATIFS à sa racine (« Contents/Info.plist », « Contents/MacOS/vecu-app »,
// « Contents/_CodeSignature/CodeResources »), pas le dossier « Vécu.app »
// lui-même. Deux raisons. D'abord le nom du bundle est accentué, et le faire
// voyager dans une archive expose à un aller-retour NFC/NFD entre deux systèmes
// de fichiers. Ensuite c'est le client qui doit choisir où il extrait et sous
// quel nom, puisqu'il échange le résultat avec un bundle déjà installé.
func construitBundle(cible, version string) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "vecu-bundle-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	cmd := exec.Command("./desktop/build-app.sh", tmp, version, cible)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build-app.sh : %v : %s", err, strings.TrimSpace(string(out)))
	}

	racine, err := trouveBundle(tmp)
	if err != nil {
		return nil, err
	}
	return archive(racine)
}

// trouveBundle rend le chemin de l'unique dossier « *.app » de `dir`.
//
// On LIT le nom plutôt que de le construire. « Vécu.app » porte un accent, donc
// une composition Unicode, et une constante écrite en NFC dans ce fichier ne
// correspondrait pas forcément aux octets posés sur le disque. Lire supprime la
// question.
func trouveBundle(dir string) (string, error) {
	entrees, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var trouves []string
	for _, e := range entrees {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			trouves = append(trouves, filepath.Join(dir, e.Name()))
		}
	}
	if len(trouves) != 1 {
		return "", fmt.Errorf("attendu 1 bundle .app dans %s, trouvé %d", dir, len(trouves))
	}
	return trouves[0], nil
}

// archive rend le tar.gz du contenu de `racine`, chemins relatifs à `racine`.
//
// Refus explicite de tout ce qui n'est ni un dossier ni un fichier ordinaire.
// Le bundle n'a aujourd'hui aucun lien symbolique ; le jour où il en aurait un,
// il vaut mieux une release qui échoue qu'une archive qui le transporte à
// moitié. Le sceau n'en dépend pas : vérifié le 21/08, il survit au retrait
// complet des attributs étendus, donc une archive qui n'en porte aucun reste
// valide (« valid on disk », « satisfies its Designated Requirement »).
//
// Source: https://pkg.go.dev/archive/tar#FileInfoHeader
func archive(racine string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	err := filepath.WalkDir(racine, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(racine, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil // la racine elle-même ne voyage pas
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("%s n'est ni un dossier ni un fichier ordinaire (%s)", rel, d.Type())
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			h.Name += "/"
		}
		// Ni le compte ni la machine du constructeur n'ont à voyager dans une
		// archive publique. Les mettre à zéro rend aussi l'archive reproductible
		// d'un poste à l'autre, à binaire identique.
		h.Uid, h.Gid = 0, 0
		h.Uname, h.Gname = "", ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
