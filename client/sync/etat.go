package sync

// etat.go : où vit `state.json`.
//
// Aujourd'hui dans `<racine>/.vecu/`. DT2 le déplace vers le dossier
// d'application, pour une raison qui n'existe pas encore mais qui arrive : avec
// des espaces éparpillés, il n'y a plus de racine où le poser.
//
// « Un dossier d'état UNIQUE », dit le CDC, et il donne la raison de ne pas le
// découper par espace : « un état par espace multiplierait les états partiels et
// rendrait un cycle atomique impossible ». C'est l'état d'un moteur, pas d'un
// dossier.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// statePath : le fichier d'état de cette racine.
//
// Le nouvel emplacement ne gagne QUE s'il existe déjà, c'est-à-dire si la
// migration a été demandée explicitement. Le défaut reste l'ancien.
//
// C'est une déviation assumée du CDC, qui déclenche la migration « au premier
// lancement ». Migrer automatiquement déplacerait l'état des deux postes qui
// dogfoodent, en même temps, pour un gain nul tant qu'aucun espace ne vit hors
// de sa racine - et pendant que le correctif de la perte silencieuse n'est
// toujours pas déployé. La capacité est là et elle est testée ; son
// déclenchement est un geste, et il aura sa propre release.
func statePath(dir string) string {
	if nouveau := cheminEtatApplication(dir); existeFichier(nouveau) {
		return nouveau
	}
	return cheminEtatHistorique(dir)
}

func cheminEtatHistorique(dir string) string {
	return filepath.Join(dir, vecuDir, "state.json")
}

// cheminEtatApplication : `~/Library/Application Support/Vecu/etats/<empreinte>.json`.
//
// Indexé par la racine tant qu'il y en a une. Le jour où le montage libre
// supprime la notion de racine, la clé devient un nom fixe - et c'est le seul
// endroit à changer.
func cheminEtatApplication(dir string) string {
	somme := sha256.Sum256([]byte(filepath.Clean(dir)))
	return filepath.Join(dossierApplication(), "etats", hex.EncodeToString(somme[:16])+".json")
}

func dossierApplication() string {
	maison, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "vecu")
	}
	return filepath.Join(maison, "Library", "Application Support", "Vecu")
}

func existeFichier(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// MigreEtatVersDossierApplication déplace l'état de cette racine vers le dossier
// d'application. Idempotent.
//
// L'ancien fichier est CONSERVÉ, pas supprimé : « l'ancien emplacement reste
// lisible une version », dit le CDC, et c'est ce qui permet à un poste revenu en
// arrière de démarrer. Le laisser derrière coûte quelques kilo-octets ; le
// supprimer coûterait un poste qui ne démarre plus.
//
// Copie puis vérification, jamais un `os.Rename` : la racine et le dossier
// d'application peuvent être sur deux volumes, où `rename` échoue avec EXDEV.
// Source: https://pkg.go.dev/os#Rename
func MigreEtatVersDossierApplication(dir string) (string, error) {
	cible := cheminEtatApplication(dir)
	if existeFichier(cible) {
		return cible, nil // déjà fait
	}
	source := cheminEtatHistorique(dir)
	contenu, err := os.ReadFile(source)
	if os.IsNotExist(err) {
		// Rien à migrer : un poste neuf part directement du nouvel emplacement.
		// On pose un état vide plutôt que de ne rien faire, sinon `statePath`
		// retomberait sur l'ancien chemin au prochain appel et la migration ne
		// prendrait jamais.
		contenu = []byte("{}\n")
	} else if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(cible), 0o700); err != nil {
		return "", err
	}
	tmp := cible + ".tmp"
	if err := os.WriteFile(tmp, contenu, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, cible); err != nil {
		return "", err
	}
	// Relu depuis le disque : une écriture courte ou un volume plein rendrait un
	// état tronqué, et le moteur repartirait d'un état partiel en croyant avoir
	// migré. C'est le genre de perte qu'on ne voit qu'au cycle suivant.
	relu, err := os.ReadFile(cible)
	if err != nil {
		return "", err
	}
	if len(relu) != len(contenu) {
		os.Remove(cible)
		return "", fmt.Errorf("état migré incomplet (%d octets écrits, %d relus) : l'ancien emplacement reste l'autorité", len(contenu), len(relu))
	}
	return cible, nil
}
