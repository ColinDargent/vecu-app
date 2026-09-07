package main

// service_windows.go : la supervision par le Planificateur de tâches.
//
// La correspondance avec launchd est exacte sur les points qui comptent, et
// c'est ce qui permet de garder le même contrat des deux côtés :
//
//	launchd                          Planificateur de tâches
//	------------------------------   --------------------------------------
//	RunAtLoad                        <LogonTrigger>
//	KeepAlive{SuccessfulExit=false}  <RestartOnFailure>{Interval,Count}
//	launchctl bootstrap              schtasks /Create /XML ... /F
//	launchctl bootout                schtasks /Delete /F
//	launchctl print → « program = »  schtasks /Query /XML → <Command>
//	EnvironmentVariables             (absent du schéma) → <Arguments>--service</Arguments>
//
// Le point qui rend la correspondance juste et pas seulement plausible :
// `RestartOnFailure` relance sur une sortie EN ÉCHEC, pas sur une sortie propre.
// C'est exactement la sémantique de `SuccessfulExit=false`, donc « Quitter »
// depuis le menu quitte vraiment, au lieu d'être immédiatement rattrapé.
// Source: https://learn.microsoft.com/en-us/windows/win32/taskschd/task-scheduler-schema-elements
// Source: https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/schtasks-create

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/colindargent/vecu/client/sync"
)

// cheminDefinition : où l'on garde le XML de la tâche désirée.
//
// À la différence du plist, ce fichier n'est PAS ce que lit le système : le
// Planificateur tient sa propre copie dans son magasin, et c'est elle qui fait
// foi. Le fichier n'est qu'une entrée pour `schtasks /Create /XML` - raison pour
// laquelle l'idempotence se juge sur ce que le magasin rend, jamais sur ce
// fichier-ci.
func cheminDefinition(label string) string {
	return filepath.Join(sync.DossierApplication(), label+".xml")
}

// synchroniseService installe ou met à jour la tâche. Idempotent : ne fait rien
// si une tâche du bon nom est enregistrée ET pointe déjà le bon binaire.
//
// On atteste ce que le MAGASIN rend, pas ce que notre fichier contient : c'est
// la même exigence que sur macOS (« le job chargé, pas la présence du fichier »),
// et elle compte davantage ici, puisque le fichier n'est qu'une entrée.
// Appelée uniquement par l'instance manuelle.
func synchroniseService(c serviceCfg) (installe bool, err error) {
	if prog, enregistree := serviceProgramme(c.label); enregistree && prog == c.binaire {
		return false, nil
	}

	desire := contenuTacheWindows(c)
	if err := os.MkdirAll(filepath.Dir(c.definition), 0o755); err != nil {
		return false, err
	}
	// UTF-16LE avec marque d'ordre : l'en-tête XML annonce encoding="UTF-16",
	// et `schtasks` refuse le fichier si l'encodage réel ne correspond pas.
	if err := ecritFichierAtomique(c.definition, versUTF16LE(desire)); err != nil {
		return false, err
	}

	// Retirer l'ancienne tâche et attendre qu'elle ait disparu : l'ancien
	// processus doit avoir relâché son verrou avant que le nouveau ne tente de
	// l'acquérir. Même raison, et même attente, que le bootout de macOS.
	_ = bootoutService(c.label)
	attendServiceParti(c.label, 5*time.Second)

	if err := bootstrapService(c.label, c.definition); err != nil {
		return false, err
	}
	// `/Create` enregistre la tâche mais ne la démarre pas : le déclencheur est
	// l'ouverture de session, qui a déjà eu lieu. Sans ce `/Run`, l'icône
	// n'apparaîtrait qu'à la prochaine connexion - ce que personne ne
	// comprendrait après avoir double-cliqué sur l'application.
	_ = exec.Command("schtasks", "/Run", "/TN", c.label).Run()
	return true, nil
}

// serviceProgramme rend le chemin du binaire de la tâche enregistrée sous ce
// nom, et si une tâche existe.
func serviceProgramme(label string) (programme string, enregistree bool) {
	out, err := schtasks("/Query", "/TN", label, "/XML", "ONE")
	if err != nil {
		return "", false
	}
	if m := motifCommande.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	return "", true // enregistrée, mais commande non lue
}

var (
	motifCommande = regexp.MustCompile(`(?s)<Command>(.*?)</Command>`)
	motifDossier  = regexp.MustCompile(`(?s)<WorkingDirectory>(.*?)</WorkingDirectory>`)
)

func serviceCharge(label string) bool {
	_, err := schtasks("/Query", "/TN", label)
	return err == nil
}

// attendServiceParti attend que la tâche ne soit plus enregistrée, jusqu'à `max`.
func attendServiceParti(label string, max time.Duration) {
	fin := time.Now().Add(max)
	for time.Now().Before(fin) {
		if !serviceCharge(label) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func bootoutService(label string) error {
	_, err := schtasks("/Delete", "/TN", label, "/F")
	return err
}

// bootstrapService enregistre la tâche depuis son XML.
//
// `/F` écrase une tâche existante sans poser de question - nécessaire parce que
// le `/Delete` qui précède peut n'avoir rien trouvé à supprimer, ou avoir été
// devancé par une réinstallation concurrente.
func bootstrapService(label, definition string) error {
	sortie, err := schtasks("/Create", "/TN", label, "/XML", definition, "/F")
	if err != nil {
		return fmt.Errorf("schtasks /Create : %v (%s)", err, strings.TrimSpace(sortie))
	}
	return nil
}

// racineDeclaree extrait le dossier supervisé par la tâche enregistrée.
//
// Lit le MAGASIN, pas le fichier passé en argument : sur Windows le fichier peut
// avoir été supprimé sans que la tâche disparaisse. Le paramètre est ignoré, il
// n'existe que pour tenir la même signature que la version macOS.
func racineDeclaree(string) (string, bool) {
	out, err := schtasks("/Query", "/TN", labelService, "/XML", "ONE")
	if err != nil {
		return "", false
	}
	if m := motifDossier.FindStringSubmatch(out); m != nil {
		if d := strings.TrimSpace(m[1]); d != "" {
			return d, true
		}
	}
	return "", false
}

// schtasks lance la commande et rend sa sortie décodée.
//
// `HideWindow` évite qu'une console noire ne clignote à chaque appel : l'app est
// graphique, et `resoudRacine` appelle ceci au démarrage.
func schtasks(args ...string) (string, error) {
	cmd := exec.Command("schtasks", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return depuisSortieWindows(out), err
}

// versUTF16LE encode en UTF-16 petit-boutiste, précédé de sa marque d'ordre.
func versUTF16LE(s string) []byte {
	unites := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2+len(unites)*2)
	b = append(b, 0xFF, 0xFE) // BOM UTF-16LE
	for _, u := range unites {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

// depuisSortieWindows décode ce qu'un outil Windows a écrit.
//
// `schtasks /XML` rend de l'UTF-16LE avec marque d'ordre, là où ses autres
// sous-commandes rendent du texte de console. Se tromper ici ne produit pas une
// erreur : ça produit une chaîne pleine d'octets nuls dans laquelle aucune
// expression régulière ne trouve rien - donc un « tâche absente » silencieux,
// suivi d'une réinstallation à chaque lancement. On regarde la marque d'ordre
// plutôt que de supposer l'une ou l'autre.
func depuisSortieWindows(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		unites := make([]uint16, 0, (len(b)-2)/2)
		for i := 2; i+1 < len(b); i += 2 {
			unites = append(unites, binary.LittleEndian.Uint16(b[i:]))
		}
		return string(utf16.Decode(unites))
	case len(b) >= 3 && bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:]) // UTF-8 avec marque d'ordre
	default:
		return string(b)
	}
}
