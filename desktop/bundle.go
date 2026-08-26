package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// maxFichierBundle : borne par fichier extrait. Les octets ont déjà été liés à
// l'empreinte scellée avant d'arriver ici, donc ce n'est pas une défense contre
// un attaquant — c'est un garde-fou contre une archive mal construite qui
// remplirait le disque avant qu'on s'en aperçoive.
const maxFichierBundle = 300 << 20 // 300 Mio

// racineBundle remonte du binaire en cours d'exécution vers la racine du bundle.
//
// Disposition : <racine>.app/Contents/MacOS/<exécutable>. Trois niveaux.
//
// Le refus quand ça ne finit pas par « .app » n'est pas décoratif : un build de
// dev lancé depuis /tmp n'est pas dans un bundle, et échanger son dossier parent
// détruirait ce qui s'y trouve. Un build de dev ne s'auto-update jamais (version
// « dev »), donc ce chemin ne doit pas s'atteindre — la garde est là pour le cas
// où il s'atteindrait quand même.
func racineBundle(exe string) (string, error) {
	if resolu, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolu
	}
	racine := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(racine, ".app") {
		return "", fmt.Errorf("l'exécutable %q n'est pas dans un bundle .app (racine déduite : %q)", exe, racine)
	}
	if info, err := os.Stat(racine); err != nil || !info.IsDir() {
		return "", fmt.Errorf("racine de bundle %q inutilisable : %v", racine, err)
	}
	return racine, nil
}

// extraitBundle écrit le contenu de l'archive dans `dest`, qui est créé.
//
// L'extraction passe par os.Root : toute entrée dont le nom sortirait de `dest`
// est refusée par la bibliothèque, y compris via un lien symbolique. C'est ce
// qui rend la traversée de chemin impossible par construction plutôt que par
// une validation qu'on aurait pu écrire de travers.
// Source: https://pkg.go.dev/os#Root
//
// Tout ce qui n'est ni dossier ni fichier ordinaire est refusé, en miroir de ce
// que la release accepte d'archiver.
func extraitBundle(arch []byte, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("dossier d'extraction : %w", err)
	}
	r, err := os.OpenRoot(dest)
	if err != nil {
		return fmt.Errorf("ouverture du dossier d'extraction : %w", err)
	}
	defer r.Close()

	gz, err := gzip.NewReader(bytes.NewReader(arch))
	if err != nil {
		return fmt.Errorf("archive illisible : %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("archive tronquée ou corrompue : %w", err)
		}
		// Un dossier s'écrit « Contents/ » en tar, et os.Root refuse la barre
		// oblique finale (« mkdirat Contents/: no such file or directory »).
		// path.Clean la retire. Il ne NEUTRALISE rien : « ../evade » reste
		// « ../evade » après nettoyage, et c'est os.Root qui le refuse.
		nom := path.Clean(h.Name)
		if nom == "." || nom == "/" {
			continue // la racine de l'archive, rien à créer
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := r.MkdirAll(nom, 0o755); err != nil {
				return fmt.Errorf("création de %q : %w", h.Name, err)
			}
		case tar.TypeReg:
			if d := path.Dir(nom); d != "." && d != "/" {
				if err := r.MkdirAll(d, 0o755); err != nil {
					return fmt.Errorf("création de %q : %w", d, err)
				}
			}
			// O_EXCL : une archive qui contiendrait deux fois le même chemin est
			// une archive qu'on ne comprend pas, donc qu'on n'installe pas.
			f, err := r.OpenFile(nom, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(h.Mode).Perm())
			if err != nil {
				return fmt.Errorf("écriture de %q : %w", h.Name, err)
			}
			n, err := io.Copy(f, io.LimitReader(tr, maxFichierBundle+1))
			if err == nil && n > maxFichierBundle {
				err = fmt.Errorf("fichier %q au-delà de %d octets", h.Name, maxFichierBundle)
			}
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("entrée %q de type %q refusée (ni dossier ni fichier ordinaire)", h.Name, string(h.Typeflag))
		}
	}
	return nil
}

// echangeBundle met `nouveau` à la place de `installe`.
//
// Chemin nominal : renamex_np avec RENAME_SWAP, qui échange atomiquement les
// deux chemins. Les NOMS ne bougent pas, seuls les contenus s'échangent — c'est
// pour ça que le plist launchd, qui pointe un chemin à l'intérieur du bundle,
// continue de résoudre après coup. Après un échange réussi, `nouveau` porte
// l'ANCIEN bundle, que l'appelant supprime.
// Source: man 2 renamex_np — « it will cause the source and target to be
// atomically swapped », valable entre dossiers, sous réserve que le volume
// annonce VOL_CAP_INT_RENAME_SWAP (vrai sur APFS).
//
// Repli quand le volume ne le supporte pas : deux renames. Il existe alors une
// fenêtre, courte, où le chemin installé n'existe pas. On la referme en
// remettant l'ancien en place si le second rename échoue.
// Source: https://pkg.go.dev/golang.org/x/sys/unix#RenamexNp
func echangeBundle(nouveau, installe string) error {
	err := unix.RenamexNp(nouveau, installe, unix.RENAME_SWAP)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return fmt.Errorf("échange atomique : %w", err)
	}
	return echangeParDoubleRename(nouveau, installe)
}

// echangeParDoubleRename : le repli, quand le volume n'annonce pas
// VOL_CAP_INT_RENAME_SWAP.
//
// Fonction à part pour une raison de test, pas de style : sur APFS l'échange
// atomique réussit toujours, donc ce chemin ne s'atteint jamais depuis
// echangeBundle sur les machines qu'on a. Le sortir est le seul moyen de
// l'exercer plutôt que de se contenter de l'avoir écrit.
func echangeParDoubleRename(nouveau, installe string) error {
	ancien := installe + ".ancien"
	_ = os.RemoveAll(ancien)
	if err := os.Rename(installe, ancien); err != nil {
		return fmt.Errorf("mise de côté de l'ancien bundle : %w", err)
	}
	if err := os.Rename(nouveau, installe); err != nil {
		// Remettre l'ancien : mieux vaut la version d'avant qu'aucune application.
		if reerr := os.Rename(ancien, installe); reerr != nil {
			return fmt.Errorf("installation échouée (%v) ET restauration échouée (%v) : le bundle est à %s", err, reerr, ancien)
		}
		return fmt.Errorf("installation du nouveau bundle : %w", err)
	}
	_ = os.RemoveAll(ancien)
	return nil
}

// remplaceBundle enchaîne extraction → sonde de santé → échange.
//
// L'ordre porte toute la sûreté : la sonde tourne sur le binaire EXTRAIT, donc
// avant que quoi que ce soit d'installé ne bouge. Un bundle d'une mauvaise
// architecture, corrompu, ou qui plante au démarrage échoue ici, et le bundle
// en place n'a pas été touché. C'est le même contrat que remplaceBinaire, porté
// du fichier au dossier.
//
// L'authenticité, elle, a déjà été établie avant d'arriver ici : les octets sont
// liés à l'empreinte scellée dans le manifeste signé.
func remplaceBundle(exe string, arch []byte, versionAttendue string) error {
	racine, err := racineBundle(exe)
	if err != nil {
		return err
	}
	// À CÔTÉ du bundle : même volume, donc rename et échange possibles. Un
	// dossier temporaire système ne le garantirait pas.
	tmp := filepath.Join(filepath.Dir(racine), ".vecu-maj-"+versionAttendue)
	_ = os.RemoveAll(tmp) // reliquat d'une tentative interrompue

	if err := extraitBundle(arch, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("extraction du bundle : %w", err)
	}
	binNeuf := filepath.Join(tmp, "Contents", "MacOS", filepath.Base(exe))
	if err := sondeSante(binNeuf, versionAttendue); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("bundle non validé, mise à jour abandonnée : %w", err)
	}
	if err := echangeBundle(tmp, racine); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	// Après un échange réussi, `tmp` porte l'ancien bundle. C'est ici que
	// disparaît le vecu-app.old que l'ancien mécanisme laissait s'accumuler.
	_ = os.RemoveAll(tmp)
	return nil
}
