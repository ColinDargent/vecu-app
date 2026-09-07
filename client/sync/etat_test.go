package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLEtatResteOuIlEstTantQuOnNeMigrePas : le défaut ne bouge pas. Deux postes
// dogfoodent en ce moment, et le correctif de la perte silencieuse n'est même
// pas déployé - déplacer leur état comme effet de bord d'une mise à jour serait
// prendre un risque pour un gain nul.
func TestLEtatResteOuIlEstTantQuOnNeMigrePas(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, vecuDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := statePath(dir); got != filepath.Join(dir, vecuDir, "state.json") {
		t.Errorf("l'état a déménagé tout seul : %q", got)
	}
	// Y compris sur une racine neuve, qui n'a encore aucun état : c'est le cas de
	// tous les tests du paquet, et c'est ce qui les garde hermétiques - sans quoi
	// ils écriraient dans le vrai ~/Library/Application Support.
	if got := statePath(t.TempDir()); filepath.Base(filepath.Dir(got)) != vecuDir {
		t.Errorf("une racine neuve écrit hors d'elle-même : %q", got)
	}
}

// TestMigrationExpliciteEtReversible : la capacité existe, elle se déclenche par
// un geste, et elle laisse de quoi revenir en arrière.
func TestMigrationExpliciteEtReversible(t *testing.T) {
	maison := t.TempDir()
	t.Setenv("HOME", maison)
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", maison)
	t.Setenv("AppData", filepath.Join(maison, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(maison, "AppData", "Local")) // pour ne pas écrire dans le vrai dossier d'application
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, vecuDir), 0o700); err != nil {
		t.Fatal(err)
	}

	avant := State{Head: "abc123", Files: map[string]string{"shared/a.md": "sha-1"}}
	if err := avant.Save(dir); err != nil {
		t.Fatal(err)
	}
	ancien := filepath.Join(dir, vecuDir, "state.json")
	if !existeFichier(ancien) {
		t.Fatal("prérequis : l'état devait être écrit à l'ancien emplacement")
	}

	cible, err := MigreEtatVersDossierApplication(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Attendu DÉRIVÉ de `DossierApplication`, pas écrit en dur : ce chemin est
	// « ~/Library/Application Support/Vecu » sur macOS et « %AppData%\Vecu » sur
	// Windows, et un test qui grave la forme macOS ne teste plus la migration -
	// il teste sur quel système il tourne. L'égalité littérale qui verrouille la
	// forme macOS existe, et c'est sa place : chemins_app_darwin_test.go.
	if filepath.Dir(filepath.Dir(cible)) != DossierApplication() {
		t.Errorf("cible inattendue : %q, attendu sous %q", cible, DossierApplication())
	}

	// Le contenu a suivi, à l'identique.
	apres, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if apres.Head != "abc123" || apres.Files["shared/a.md"] != "sha-1" {
		t.Errorf("l'état n'a pas suivi : %+v", apres)
	}
	// Et c'est bien le NOUVEAU fichier qui fait autorité désormais.
	if got := statePath(dir); got != cible {
		t.Errorf("statePath = %q, attendu %q", got, cible)
	}

	// L'ancien est CONSERVÉ : un poste revenu à une version antérieure doit
	// encore démarrer. Le supprimer économiserait quelques kilo-octets et
	// coûterait un poste qui ne redémarre plus.
	if !existeFichier(ancien) {
		t.Error("l'ancien emplacement a été supprimé : un retour arrière ne démarre plus")
	}

	// Idempotente : la relancer ne réécrit rien et ne perd rien.
	nouveau := State{Head: "def456", Files: map[string]string{}}
	if err := nouveau.Save(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := MigreEtatVersDossierApplication(dir); err != nil {
		t.Fatal(err)
	}
	relu, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if relu.Head != "def456" {
		t.Errorf("une seconde migration a écrasé l'état courant avec l'ancien : head = %q", relu.Head)
	}
}

// TestDeuxRacinesGardentDeuxEtats : l'état est celui d'un moteur. Deux racines
// migrées ne doivent pas se retrouver à partager un fichier - elles écraseraient
// mutuellement leur `Files` en last-writer-wins, ce qui est exactement le mode
// de perte que le verrou existe pour empêcher.
func TestDeuxRacinesGardentDeuxEtats(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", t.TempDir())
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(d, vecuDir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if cheminEtatApplication(a) == cheminEtatApplication(b) {
		t.Fatal("deux racines partagent le même fichier d'état")
	}

	(State{Head: "aaa"}).Save(a)
	(State{Head: "bbb"}).Save(b)
	if _, err := MigreEtatVersDossierApplication(a); err != nil {
		t.Fatal(err)
	}
	if _, err := MigreEtatVersDossierApplication(b); err != nil {
		t.Fatal(err)
	}
	ea, _ := LoadState(a)
	eb, _ := LoadState(b)
	if ea.Head != "aaa" || eb.Head != "bbb" {
		t.Errorf("les états se sont mélangés : %q / %q", ea.Head, eb.Head)
	}
}

// TestMigrationDUnPosteNeuf : sans état à migrer, la migration doit quand même
// PRENDRE. Sinon `statePath` retomberait sur l'ancien chemin au prochain appel,
// et le poste écrirait indéfiniment là où on vient de décider qu'il n'écrit plus.
func TestMigrationDUnPosteNeuf(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", t.TempDir())
	dir := t.TempDir()
	cible, err := MigreEtatVersDossierApplication(dir)
	if err != nil {
		t.Fatal(err)
	}
	if statePath(dir) != cible {
		t.Errorf("la migration n'a pas pris sur un poste neuf : %q", statePath(dir))
	}
	// Et l'état vide posé est lisible, pas un fichier corrompu.
	var vide State
	b, err := os.ReadFile(cible)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &vide); err != nil {
		t.Errorf("l'état posé n'est pas du JSON valide : %v", err)
	}
}
