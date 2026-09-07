package main

import (
	"os"
	"strings"
)

// service.go : ce que la supervision a de commun aux deux systèmes.
//
// L'app est elle-même le processus supervisé, et le même binaire tient deux
// rôles distincts :
//
//   - instance de SERVICE (lancée par le superviseur du système) : elle tient le
//     moteur et l'icône, et c'est la SEULE qui acquiert le verrou du dossier.
//     Elle ne touche JAMAIS au superviseur, sinon elle risquerait de se retirer
//     elle-même.
//   - instance MANUELLE (double-clic sur l'app) : seule responsable de
//     l'installation du service, puis elle sort. Elle ne lance jamais le moteur,
//     donc ne prend jamais le verrou : pas de course avec le service.
//
// L'implémentation vit dans service_darwin.go (launchd) et service_windows.go
// (Planificateur de tâches). Les deux tiennent le même contrat : `synchroniseService`
// est idempotente et atteste le JOB RÉELLEMENT CHARGÉ, pas la seule présence d'un
// fichier de définition.
const (
	labelService = "fr.vecu.sync" // même label que l'ancien daemon : l'installer le remplace
	envService   = "VECU_SERVICE"

	// drapeauService : comment le superviseur Windows dit « tu es le service ».
	//
	// launchd sait poser une variable d'environnement dans le plist ; le
	// Planificateur de tâches n'a aucun élément équivalent dans son schéma - il
	// sait passer des ARGUMENTS, et c'est tout. Plutôt que de faire diverger la
	// détection selon le système, les deux voies sont acceptées partout (voir
	// `estInstanceService`) : un seul comportement à comprendre, et il se teste
	// sur macOS.
	drapeauService = "--service"
)

// serviceCfg décrit le service désiré. `args` reste vide en usage réel (le
// binaire de l'app ne prend pas d'argument) ; il n'existe que pour tester la
// mécanique de supervision avec un binaire bidon.
//
// `definition` est le chemin du fichier qui décrit le service : un plist
// launchd sur macOS, un XML de tâche sur Windows. Le champ ne s'appelle plus
// `plist` depuis le port : `main.go` n'a pas à savoir lequel des deux il manipule.
type serviceCfg struct {
	label      string
	binaire    string
	racine     string
	definition string
	journal    string
	args       []string
}

// estInstanceService : ce processus a-t-il été lancé par le superviseur ?
//
// Les deux voies sont vraies partout, délibérément. La variable d'environnement
// est ce que launchd sait poser ; le drapeau est ce que le Planificateur de
// tâches sait passer. Accepter les deux sur les deux systèmes coûte trois lignes
// et évite qu'un défaut de démarrage ne se reproduise QUE sur le système où
// personne ne développe.
func estInstanceService() bool {
	if os.Getenv(envService) == "1" {
		return true
	}
	for _, a := range os.Args[1:] {
		if a == drapeauService {
			return true
		}
	}
	return false
}

// ecritFichierAtomique écrit via un temporaire + rename, pour qu'une écriture
// interrompue ne laisse jamais une définition tronquée (inchargeable).
//
// Portable sans réserve : `os.Rename` passe par `MoveFileEx` avec
// `MOVEFILE_REPLACE_EXISTING` sur Windows (os/file_windows.go), donc il écrase
// une cible existante comme le fait `rename(2)` sur unix. Vérifié dans la source
// de Go 1.26 plutôt que supposé - c'est exactement le genre de différence qu'on
// croit connaître et qui est fausse dans un sens ou dans l'autre.
func ecritFichierAtomique(path string, contenu []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, contenu, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// echappeXML protège les caractères XML d'un chemin.
//
// Partagé par les deux systèmes, et ce n'est pas un hasard : le plist launchd et
// le XML du Planificateur de tâches sont tous les deux du XML, et un dossier
// contenant « & » rendrait l'un comme l'autre inchargeable.
func echappeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
