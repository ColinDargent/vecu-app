package sync

// verrous.go : un seul moteur à la fois sur un même contenu.
//
// Le verrou portait sur la RACINE - « un processus vecu par dossier ». Cette
// formulation a cessé d'être suffisante le jour où un espace a pu vivre hors de
// la racine (DT2, étape 2a) : deux moteurs lancés sur deux racines différentes
// peuvent monter le MÊME dossier externe. Chacun prend le verrou de sa racine,
// aucun ne voit l'autre, et les deux synchronisent les mêmes fichiers en
// parallèle.
//
// C'est exactement la condition qui a produit les copies de conflit en série en
// août, et le CDC la nomme comme le risque principal du lot : « un défaut ici
// produit deux cycles concurrents ».
//
// On verrouille donc CE QUI EST SYNCHRONISÉ, et plus le point de départ : la
// racine, plus chaque chemin monté hors d'elle.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// acquireLocks pose les verrous de ce moteur : celui de la racine, et un par
// montage explicite situé hors de la racine.
//
// Tout ou rien. Un moteur qui obtiendrait la racine mais pas l'un de ses espaces
// synchroniserait quand même le reste, en laissant l'espace contesté à deux
// processus - le pire des deux mondes, et silencieux.
func acquireLocks(dir string, montages map[string]string) ([]*os.File, error) {
	pris := []*os.File{}
	relache := func() {
		for _, f := range pris {
			f.Close()
		}
	}

	f, err := verrouilleFichier(filepath.Join(dir, vecuDir, "lock"))
	if err != nil {
		return nil, fmt.Errorf("un autre processus vecu synchronise déjà ce dossier (daemon en cours ?)")
	}
	pris = append(pris, f)

	for _, espace := range triees(montages) {
		chemin := montages[espace]
		if sousLaRacine(dir, chemin) {
			continue // déjà couvert par le verrou de la racine
		}
		f, err := verrouilleFichier(cheminVerrou(chemin))
		if err != nil {
			relache()
			return nil, fmt.Errorf("un autre processus vecu synchronise déjà l'espace « %s » (%s)", espace, chemin)
		}
		pris = append(pris, f)
	}
	return pris, nil
}

// VerrouExclusif : le verrou de fichier du produit, ouvert à l'app de bureau.
//
// Exporté pendant le port Windows parce que `desktop` en réécrivait un second à
// la main (le verrou de migration), avec son propre appel à flock - donc son
// propre défaut à corriger deux fois. Un seul endroit sait maintenant comment on
// verrouille un fichier, et c'est celui qui a la couture par système.
func VerrouExclusif(chemin string) (*os.File, error) { return verrouilleFichier(chemin) }

func verrouilleFichier(chemin string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(chemin), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(chemin, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	// Non bloquant : on veut un refus immédiat et lisible, pas un processus qui
	// attend sans rien dire. Relâché automatiquement à la mort du processus,
	// donc jamais de verrou orphelin. L'implémentation est par système
	// (verrou_unix.go / verrou_windows.go) ; la garantie, elle, est la même.
	if err := poseVerrouExclusif(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// cheminVerrou : le fichier de verrou d'un chemin monté hors de la racine.
//
// Dans le dossier d'application, JAMAIS dans le dossier monté lui-même : ce
// dossier est synchronisé, un fichier de verrou y partirait chez tout le monde.
// Nommé par l'empreinte du chemin, parce qu'un chemin absolu contient des
// séparateurs, des accents et des espaces.
func cheminVerrou(chemin string) string {
	somme := sha256.Sum256([]byte(filepath.Clean(chemin)))
	return filepath.Join(dossierVerrous(), hex.EncodeToString(somme[:16])+".lock")
}

func dossierVerrous() string { return filepath.Join(DossierApplication(), "verrous") }

// sousLaRacine : comparaison LEXICALE, cohérente avec `souLeMontage`. Résoudre
// les liens ici ferait diverger les deux réponses, et un montage compté deux
// fois se verrouillerait contre lui-même.
func sousLaRacine(dir, chemin string) bool {
	r, err := filepath.Rel(dir, chemin)
	if err != nil {
		return false
	}
	return r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// triees : ordre stable de prise des verrous. L'ordre d'itération d'une map est
// aléatoire en Go ; deux moteurs qui prendraient les mêmes verrous dans deux
// ordres différents pourraient se bloquer mutuellement. Le flock est non
// bloquant, donc ce serait un refus des deux côtés plutôt qu'un interblocage -
// mais un refus mutuel intermittent est déjà un défaut qu'on ne veut pas.
func triees(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// verrouilleFichierRacine : le verrou de la racine seule. Sert à
// `AdoptionManuelle`, qui travaille dans la racine et ne monte rien.
func verrouilleFichierRacine(dir string) (*os.File, error) {
	f, err := verrouilleFichier(filepath.Join(dir, vecuDir, "lock"))
	if err != nil {
		return nil, fmt.Errorf("un autre processus vecu synchronise déjà ce dossier (daemon en cours ?)")
	}
	return f, nil
}
