package main

// agent_windows.go : l'équivalent Windows du LaunchAgent de `vecu setup`.
//
// Même service que sur macOS - le daemon CLI, lancé en « vecu start --dir
// <racine> » - installé dans le Planificateur de tâches au lieu de launchd.
//
// Le XML ressemble à celui de l'app de bureau sans en être le même, et ils sont
// délibérément séparés : ils décrivent deux services différents (celui-ci lance
// le binaire CLI avec des arguments, l'autre lance l'app), dans deux paquets
// différents. Les fusionner demanderait une abstraction dont aucun des deux
// n'a besoin.
// Source: https://learn.microsoft.com/en-us/windows/win32/taskschd/task-scheduler-schema-elements

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"

	"github.com/colindargent/vecu/client/sync"
)

// installeAgentSysteme (Windows) enregistre la tâche planifiée et la démarre.
//
// `KeepAlive` de launchd devient `RestartOnFailure`. `ExecutionTimeLimit` à
// PT0S est indispensable : son défaut de trois jours ferait tuer le daemon au
// bout de 72 heures, un défaut qui ne se manifesterait qu'au troisième jour
// d'utilisation. Les deux réglages « sur batterie » sont remis à false, sans
// quoi la synchronisation s'arrêterait dès qu'un portable est débranché.
func installeAgentSysteme(racine string) error {
	binaire, err := os.Executable()
	if err != nil {
		return err
	}
	if resolu, err := filepath.EvalSymlinks(binaire); err == nil {
		binaire = resolu
	}

	journal := filepath.Join(sync.DossierJournaux(), "vecu.log")
	if err := os.MkdirAll(filepath.Dir(journal), 0o755); err != nil {
		return err
	}

	definition := filepath.Join(sync.DossierApplication(), labelAgent+".xml")
	if err := os.MkdirAll(filepath.Dir(definition), 0o755); err != nil {
		return err
	}

	contenu := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Vécu - synchronisation (daemon)</Description>
    <URI>\%s</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>start --dir "%s"</Arguments>
      <WorkingDirectory>%s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`, echappeXML(labelAgent), echappeXML(binaire), echappeXML(racine), echappeXML(racine))

	// UTF-16LE avec marque d'ordre : l'en-tête annonce encoding="UTF-16" et
	// `schtasks` refuse le fichier si l'encodage réel ne correspond pas.
	unites := utf16.Encode([]rune(contenu))
	octets := []byte{0xFF, 0xFE}
	for _, u := range unites {
		octets = binary.LittleEndian.AppendUint16(octets, u)
	}
	if err := os.WriteFile(definition, octets, 0o644); err != nil {
		return err
	}

	// Retrait d'une éventuelle version précédente, puis pose. `/F` écrase sans
	// poser de question - le `/Delete` qui précède peut n'avoir rien trouvé.
	_ = tacheSetup("/Delete", "/TN", labelAgent, "/F")
	if sortie, err := exec.Command("schtasks", "/Create", "/TN", labelAgent, "/XML", definition, "/F").CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create : %v (%s)", err, strings.TrimSpace(string(sortie)))
	}
	// `/Create` enregistre sans démarrer : le déclencheur est l'ouverture de
	// session, déjà passée. Sans ce `/Run`, rien ne synchroniserait avant la
	// prochaine connexion.
	_ = tacheSetup("/Run", "/TN", labelAgent)
	return nil
}

func tacheSetup(args ...string) error {
	cmd := exec.Command("schtasks", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}
