package main

import (
	"strings"
	"testing"
)

// Ces tests tournent sur macOS et portent sur du Windows. Ce n'est pas une
// bizarrerie : ce sont les réglages dont un mauvais DÉFAUT casse le produit en
// silence, et aucun d'eux ne se voit à la lecture du code. Les vérifier ici est
// le seul moment où quelqu'un les regarde avant qu'une machine Windows existe.

func cfgTest() serviceCfg {
	return serviceCfg{
		label:      "fr.vecu.sync",
		binaire:    `C:\Users\x\AppData\Local\Vecu\vecu-app.exe`,
		racine:     `C:\Users\x\second-brain`,
		definition: `C:\ignoré.xml`,
		journal:    `C:\ignoré.log`,
	}
}

// TestTacheWindowsNeMeurtPasAuBoutDeTroisJours : le défaut d'ExecutionTimeLimit
// est P3D. Sans PT0S, le Planificateur tuerait l'app au bout de 72 heures - un
// défaut qui ne se manifeste qu'au troisième jour, donc jamais pendant une
// recette, et toujours chez l'utilisateur.
func TestTacheWindowsNeMeurtPasAuBoutDeTroisJours(t *testing.T) {
	xml := contenuTacheWindows(cfgTest())
	if !strings.Contains(xml, "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>") {
		t.Fatalf("ExecutionTimeLimit absent ou non nul : le service serait tué au bout de 3 jours\n%s", xml)
	}
}

// TestTacheWindowsSurviteALaBatterie : les deux réglages de batterie valent
// `true` par défaut. La synchronisation s'arrêterait dès qu'un portable est
// débranché - c'est-à-dire exactement quand quelqu'un travaille ailleurs qu'à
// son bureau.
func TestTacheWindowsSurviteALaBatterie(t *testing.T) {
	xml := contenuTacheWindows(cfgTest())
	for _, attendu := range []string{
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
	} {
		if !strings.Contains(xml, attendu) {
			t.Errorf("réglage de batterie non neutralisé : %s manquant", attendu)
		}
	}
}

// TestTacheWindowsRelanceEtDemarreALaSession : les deux moitiés de ce que
// launchd faisait avec RunAtLoad + KeepAlive{SuccessfulExit=false}.
func TestTacheWindowsRelanceEtDemarreALaSession(t *testing.T) {
	xml := contenuTacheWindows(cfgTest())
	if !strings.Contains(xml, "<LogonTrigger>") {
		t.Error("pas de LogonTrigger : le service ne démarrerait pas à l'ouverture de session")
	}
	if !strings.Contains(xml, "<RestartOnFailure>") {
		t.Error("pas de RestartOnFailure : un plantage laisserait le poste sans synchronisation")
	}
	if !strings.Contains(xml, "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>") {
		t.Error("deux instances pourraient se disputer le verrou du dossier")
	}
}

// TestTacheWindowsPasseLeDrapeauDeService : le Planificateur ne sait pas poser
// de variable d'environnement (aucun élément du schéma ne le permet), là où le
// plist le faisait avec VECU_SERVICE. Le drapeau est la seule voie, et sans lui
// l'instance lancée par le système se croirait manuelle : elle installerait le
// service au lieu de synchroniser, en boucle.
func TestTacheWindowsPasseLeDrapeauDeService(t *testing.T) {
	xml := contenuTacheWindows(cfgTest())
	if !strings.Contains(xml, "<Arguments>"+drapeauService) {
		t.Fatalf("le drapeau %q n'est pas passé à la tâche\n%s", drapeauService, xml)
	}
}

// TestTacheWindowsEchappeLeXML : un dossier peut s'appeler « R&D ». Un XML mal
// formé rend la tâche inenregistrable, et `schtasks` le dit mal.
func TestTacheWindowsEchappeLeXML(t *testing.T) {
	c := cfgTest()
	c.racine = `C:\Users\x\R&D <brouillon>`
	xml := contenuTacheWindows(c)
	if strings.Contains(xml, "R&D <brouillon>") {
		t.Error("le chemin n'est pas échappé : XML invalide")
	}
	if !strings.Contains(xml, "R&amp;D &lt;brouillon&gt;") {
		t.Errorf("échappement inattendu\n%s", xml)
	}
}

// TestTacheWindowsEstDeterministe : l'idempotence de `synchroniseService` en
// dépend. Une sérialisation qui varierait (ordre d'une map, horodatage)
// réinstallerait le service à chaque lancement de l'app, donc couperait la
// synchronisation à chaque fois.
func TestTacheWindowsEstDeterministe(t *testing.T) {
	c := cfgTest()
	if contenuTacheWindows(c) != contenuTacheWindows(c) {
		t.Fatal("sérialisation non déterministe : le service serait réinstallé à chaque lancement")
	}
}
