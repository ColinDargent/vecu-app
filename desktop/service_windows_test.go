package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLeXMLDeTacheEstAccepteParLePlanificateur : LE test que la lecture de la
// documentation ne remplace pas.
//
// On peut lire le schéma dix fois et produire un XML que `schtasks` refuse -
// pour un élément dans le mauvais ordre, un espace de noms absent, un encodage
// qui ne correspond pas à l'en-tête. Ici le Planificateur de tâches réel
// enregistre la tâche, on la relit depuis SON magasin, et on vérifie qu'il en
// rend bien le binaire et le dossier de travail.
//
// C'est aussi le seul endroit qui exerce `depuisSortieWindows` sur une vraie
// sortie `schtasks /XML` : si le décodage UTF-16 était faux, la relecture
// rendrait une chaîne pleine d'octets nuls et l'expression régulière ne
// trouverait rien - un « tâche absente » silencieux, suivi d'une réinstallation
// à chaque lancement de l'app.
func TestLeXMLDeTacheEstAccepteParLePlanificateur(t *testing.T) {
	label := "vecu-test-port-windows"
	binaire := `C:\Windows\System32\cmd.exe`
	racine := t.TempDir()

	c := serviceCfg{
		label:      label,
		binaire:    binaire,
		racine:     racine,
		definition: filepath.Join(t.TempDir(), label+".xml"),
		journal:    filepath.Join(t.TempDir(), "j.log"),
	}

	if err := os.WriteFile(c.definition, versUTF16LE(contenuTacheWindows(c)), 0o644); err != nil {
		t.Fatalf("écriture de la définition : %v", err)
	}
	t.Cleanup(func() { _ = bootoutService(label) })

	if err := bootstrapService(label, c.definition); err != nil {
		t.Fatalf("LE PLANIFICATEUR REFUSE NOTRE XML : %v", err)
	}

	prog, enregistree := serviceProgramme(label)
	if !enregistree {
		t.Fatal("tâche créée mais introuvable à la relecture : le décodage de la sortie schtasks est suspect")
	}
	if !strings.EqualFold(prog, binaire) {
		t.Errorf("binaire relu = %q, attendu %q", prog, binaire)
	}

	if d, ok := racineDeclaree(""); ok && !strings.EqualFold(d, racine) {
		// racineDeclaree interroge labelService, pas notre label de test : on ne
		// vérifie donc que la cohérence si une tâche de production existe.
		t.Logf("racine de la tâche de production : %q", d)
	}

	if !serviceCharge(label) {
		t.Error("serviceCharge ne voit pas une tâche qui vient d'être créée")
	}

	// Le retrait doit marcher, sinon une réinstallation resterait bloquée.
	if err := bootoutService(label); err != nil {
		t.Errorf("retrait de la tâche : %v", err)
	}
	if serviceCharge(label) {
		t.Error("la tâche est encore là après retrait")
	}
}

// TestSynchroniseServiceEstIdempotente : le second appel ne doit RIEN faire.
// Sinon l'app réinstallerait le service à chaque lancement, coupant la
// synchronisation à chaque fois.
func TestSynchroniseServiceEstIdempotente(t *testing.T) {
	label := "vecu-test-idempotence"
	c := serviceCfg{
		label:      label,
		binaire:    `C:\Windows\System32\cmd.exe`,
		racine:     t.TempDir(),
		definition: filepath.Join(t.TempDir(), label+".xml"),
		journal:    filepath.Join(t.TempDir(), "j.log"),
	}
	// `/End` AVANT `/Delete`, et c'est une leçon du runner : `synchroniseService`
	// démarre vraiment la tâche, et Windows refuse de supprimer le dossier de
	// travail d'un processus vivant. Sans cet arrêt, le test réussit puis échoue
	// au ménage - un rouge qui ne dit rien du produit.
	t.Cleanup(func() {
		_, _ = schtasks("/End", "/TN", label)
		_ = bootoutService(label)
	})

	installe, err := synchroniseService(c)
	if err != nil {
		t.Fatalf("première installation : %v", err)
	}
	if !installe {
		t.Fatal("première installation annoncée comme déjà en place")
	}

	installe, err = synchroniseService(c)
	if err != nil {
		t.Fatalf("second appel : %v", err)
	}
	if installe {
		t.Error("SECOND APPEL RÉINSTALLE : le service serait coupé à chaque lancement de l'app")
	}
}

// TestLaSondeDeSanteLitUnBinaireSansConsole : la question qui décide si
// l'auto-update peut fonctionner sur Windows.
//
// `-H windowsgui` marque le binaire comme application du sous-système graphique,
// pour qu'aucune console noire ne s'ouvre au lancement. Un tel processus n'a PAS
// de console attachée - et `sondeSante` lance justement `<binaire> --check` et
// compare sa sortie standard à la version attendue. Si cette sortie est perdue,
// aucune mise à jour ne s'installe jamais, en silence.
//
// La CI a d'abord mesuré une sortie VIDE, mais avec PowerShell : or PowerShell
// n'attend pas un processus du sous-système graphique, il rend la main tout de
// suite. La mesure ne disait donc peut-être rien du binaire. Ce test refait
// l'appel comme le PRODUIT le fait - depuis Go, avec CombinedOutput, qui attend
// la fin du processus - parce que c'est le seul appel dont la réponse compte.
func TestLaSondeDeSanteLitUnBinaireSansConsole(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sonde.exe")

	build := exec.Command("go", "build",
		"-ldflags", "-X main.version=sonde-test -H windowsgui",
		"-o", exe, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("construction du binaire de sonde : %v (%s)", err, out)
	}

	if err := sondeSante(exe, "sonde-test"); err != nil {
		t.Fatalf("LA SONDE NE LIT RIEN D'UN BINAIRE -H windowsgui : %v\n"+
			"Conséquence : aucune mise à jour ne s'installerait jamais sur Windows.", err)
	}
}
