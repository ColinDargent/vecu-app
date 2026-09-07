package main

// service_tache_xml.go : le XML de la tâche planifiée Windows.
//
// CE FICHIER COMPILE SUR TOUS LES SYSTÈMES, et c'est délibéré. La génération du
// XML est une fonction pure - une config entre, une chaîne sort, aucun appel au
// système. La laisser dans service_windows.go la rendait invérifiable depuis la
// seule machine où les tests tournent vraiment, alors que c'est précisément là
// que vivent les réglages dont un mauvais défaut casse le produit en silence
// (ExecutionTimeLimit, les deux drapeaux de batterie).
//
// Ce qui reste dans service_windows.go est ce qui APPELLE le système : schtasks,
// le magasin de tâches, l'encodage de la sortie.

import (
	"fmt"
	"strings"
)

// contenuDefinition : le XML de la tâche, sérialisé de façon DÉTERMINISTE
// (Sprintf statique, aucune map) - deux appels à config égale rendent des octets
// identiques, ce dont dépend l'idempotence.
//
// Quatre réglages ne sont pas décoratifs, et leurs défauts casseraient le produit :
//
//   - ExecutionTimeLimit = PT0S : SANS LUI, le défaut est de TROIS JOURS, et le
//     Planificateur tuerait l'app au bout de 72 h. C'est le piège le plus cher de
//     ce fichier, parce qu'il ne se voit qu'au troisième jour.
//   - DisallowStartIfOnBatteries = false et StopIfGoingOnBatteries = false : les
//     deux défauts sont `true`. Sur un portable débranché, la synchronisation
//     s'arrêterait - c'est-à-dire précisément quand quelqu'un travaille ailleurs
//     qu'à son bureau.
//   - MultipleInstancesPolicy = IgnoreNew : deux instances prendraient le même
//     verrou et la seconde échouerait bruyamment. On refuse la seconde en amont.
//
// UN ÉLÉMENT ABSENT, ET LA RAISON DE SON ABSENCE. `UseUnifiedSchedulingEngine`
// figurait ici jusqu'au 06/09. Il est bien dans la liste des éléments du schéma,
// mais il exige la version **1.3**, et cette tâche déclare 1.2. Le vrai
// Planificateur a répondu :
//
//	ERROR: The task XML contains an unexpected node. (33,7):UseUnifiedSchedulingEngine:
//
// Retiré plutôt que de monter en 1.3 : il ne sert à rien ici (c'est une bascule
// de compatibilité héritée) et 1.2 marche partout. La leçon compte plus que la
// ligne - la doc listait l'élément, elle ne disait pas sa version minimale sur
// cette page, et AUCUNE relecture n'attrape ça. Il a fallu une machine.
func contenuTacheWindows(c serviceCfg) string {
	args := append([]string{drapeauService}, c.args...)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Vécu - synchronisation du dossier partagé</Description>
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
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
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
      <Arguments>%s</Arguments>
      <WorkingDirectory>%s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`,
		echappeXML(c.label),
		echappeXML(c.binaire),
		echappeXML(strings.Join(args, " ")),
		echappeXML(c.racine),
	)
}
