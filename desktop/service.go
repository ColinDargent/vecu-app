package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Supervision par launchd. L'app remplace l'ancien LaunchAgent `fr.vecu.sync`
// (qui lançait « vecu start ») : elle devient elle-même le process supervisé.
//
// Deux rôles pour un même binaire, distingués par la variable VECU_SERVICE
// posée dans le plist :
//   - instance de SERVICE (VECU_SERVICE=1, lancée par launchd) : tient le moteur
//     et l'icône menu-bar, et c'est la SEULE qui acquiert le verrou du dossier.
//     Elle ne touche JAMAIS launchd (sinon elle risquerait un bootout d'elle-même).
//   - instance MANUELLE (double-clic sur l'app) : seule responsable de la
//     migration/installation du service, puis elle sort. Elle ne lance jamais le
//     moteur, donc ne prend jamais le verrou : pas de course avec le service.
const (
	labelService = "fr.vecu.sync" // même label que l'ancien daemon : l'installer le remplace
	envService   = "VECU_SERVICE"
)

// serviceCfg décrit le LaunchAgent désiré. `args` reste vide en usage réel (le
// binaire de l'app ne prend pas d'argument) ; il n'existe que pour tester la
// mécanique launchd avec un binaire bidon.
type serviceCfg struct {
	label   string
	binaire string
	racine  string
	plist   string
	journal string
	args    []string
}

func cibleLaunchd() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func cheminPlist(label string) string {
	maison, _ := os.UserHomeDir()
	return filepath.Join(maison, "Library", "LaunchAgents", label+".plist")
}

// contenuPlist : sérialisation DÉTERMINISTE (Sprintf statique, aucune map) — deux
// appels à config égale rendent des octets identiques, ce dont dépend
// l'idempotence de synchroniseService.
//
// KeepAlive en dictionnaire {SuccessfulExit=false} : relancer sur sortie en
// échec (crash), PAS sur une sortie propre — sinon « Quitter » depuis le menu
// serait immédiatement rattrapé par launchd. RunAtLoad posé explicitement pour
// le démarrage initial et au login.
// Source: man launchd.plist (KeepAlive/SuccessfulExit, RunAtLoad), vérifié le 2026-07-26.
func (c serviceCfg) contenuPlist() string {
	var prog strings.Builder
	for _, a := range append([]string{c.binaire}, c.args...) {
		prog.WriteString("\n\t\t<string>" + echappeXML(a) + "</string>")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>%s
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>%s</key>
		<string>1</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, echappeXML(c.label), prog.String(), envService, echappeXML(c.racine), echappeXML(c.journal), echappeXML(c.journal))
}

// synchroniseService installe ou met à jour le LaunchAgent. Idempotent : ne fait
// rien si le plist sur disque est déjà le désiré ET qu'un service tournant sous
// ce label pointe bien sur le bon binaire (on atteste le JOB chargé, pas juste
// la présence du fichier). Appelée uniquement par l'instance manuelle.
// Retourne true si une (ré)installation a eu lieu.
func synchroniseService(c serviceCfg) (installe bool, err error) {
	desire := c.contenuPlist()
	actuel, _ := os.ReadFile(c.plist)
	if prog, charge := serviceProgramme(c.label); string(actuel) == desire && charge && prog == c.binaire {
		return false, nil // déjà en place et le bon job tourne
	}

	// Sauvegarde de l'ANCIEN plist, une seule fois : ne jamais écraser la
	// sauvegarde du vrai plist d'origine (daemon CLI) par une version déjà migrée.
	bak := c.plist + ".pre-vecu-app.bak"
	if len(actuel) > 0 {
		if _, e := os.Stat(bak); os.IsNotExist(e) {
			_ = os.WriteFile(bak, actuel, 0o644)
		}
	}

	// Retirer l'ancien job (daemon CLI ou version précédente) et ATTENDRE qu'il
	// ait disparu : bootout est asynchrone, et l'ancien daemon doit avoir relâché
	// son verrou flock avant que le nouveau service ne tente de l'acquérir.
	_ = bootoutService(c.label)
	attendServiceParti(c.label, 5*time.Second)

	if err := os.MkdirAll(filepath.Dir(c.plist), 0o755); err != nil {
		return false, err
	}
	if err := ecritFichierAtomique(c.plist, []byte(desire)); err != nil {
		return false, err
	}
	if err := bootstrapService(c.plist); err != nil {
		// Bootstrap échoué APRÈS un bootout destructif : le poste se retrouverait
		// sans aucune sync. Best-effort, restaurer l'ancien service (le plist
		// d'origine sauvegardé) plutôt que de laisser un trou.
		if b, e := os.ReadFile(bak); e == nil && len(b) > 0 {
			if ecritFichierAtomique(c.plist, b) == nil {
				_ = bootstrapService(c.plist)
			}
		}
		return false, err
	}
	return true, nil
}

// serviceProgramme rend le chemin du binaire du job chargé sous ce label, et si
// un job est chargé. Parse `launchctl print` (ligne « program = <chemin> »).
func serviceProgramme(label string) (programme string, charge bool) {
	out, err := exec.Command("launchctl", "print", cibleLaunchd()+"/"+label).CombinedOutput()
	if err != nil {
		return "", false
	}
	for _, ligne := range strings.Split(string(out), "\n") {
		ligne = strings.TrimSpace(ligne)
		if p, ok := strings.CutPrefix(ligne, "program = "); ok {
			return strings.TrimSpace(p), true
		}
	}
	return "", true // chargé, mais programme non lu (ex: décrit par ProgramArguments)
}

func serviceCharge(label string) bool {
	return exec.Command("launchctl", "print", cibleLaunchd()+"/"+label).Run() == nil
}

// attendServiceParti attend que le label ne soit plus chargé, jusqu'à `max`.
// Sans ça, un bootstrap qui suit un bootout se voit répondre « already loaded »,
// et le nouveau service pourrait courir sur un verrou pas encore relâché.
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
	return exec.Command("launchctl", "bootout", cibleLaunchd()+"/"+label).Run()
}

// bootstrapService charge et démarre le service, avec quelques tentatives : sur
// une transition récente, launchd peut répondre « already loaded » le temps que
// le démontage précédent s'achève.
func bootstrapService(plist string) error {
	var derniere error
	for i := 0; i < 10; i++ {
		sortie, err := exec.Command("launchctl", "bootstrap", cibleLaunchd(), plist).CombinedOutput()
		if err == nil {
			return nil
		}
		derniere = fmt.Errorf("launchctl bootstrap : %v (%s)", err, strings.TrimSpace(string(sortie)))
		time.Sleep(200 * time.Millisecond)
	}
	return derniere
}

// racineDepuisPlist extrait le dossier supervisé par un plist. Deux formes :
// l'ancien daemon CLI le passe en argument « --dir <racine> » ; l'app migrée le
// pose en WorkingDirectory. On lit les deux, pour que la racine reste toujours
// récupérable depuis le plist seul.
func racineDepuisPlist(path string) (string, bool) {
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", path).Output()
	if err != nil {
		return "", false
	}
	var p struct {
		ProgramArguments []string `json:"ProgramArguments"`
		WorkingDirectory string   `json:"WorkingDirectory"`
	}
	if json.Unmarshal(out, &p) != nil {
		return "", false
	}
	for i, a := range p.ProgramArguments {
		if a == "--dir" && i+1 < len(p.ProgramArguments) {
			return p.ProgramArguments[i+1], true
		}
	}
	if p.WorkingDirectory != "" {
		return p.WorkingDirectory, true
	}
	return "", false
}

// ecritFichierAtomique écrit via un temporaire + rename, pour qu'une écriture
// interrompue ne laisse jamais un plist tronqué (inchargeable).
func ecritFichierAtomique(path string, contenu []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, contenu, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// echappeXML protège les caractères XML d'un chemin dans un plist (un dossier
// peut contenir « & » ; un plist mal formé rend le service inchargeable).
func echappeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
