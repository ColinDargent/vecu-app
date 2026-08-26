package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAnnulationReconnueAuNumeroPasAuTexte : le poste de Colin est en français,
// et osascript localise ses messages d'erreur. Une reconnaissance par le texte
// anglais passerait au vert sur un test écrit en anglais tout en laissant le
// vrai cas cassé, ce qui est le pire des deux mondes.
func TestAnnulationReconnueAuNumeroPasAuTexte(t *testing.T) {
	// Chaîne relevée telle quelle sur le poste le 21/08.
	fr := "0:17: execution error: Annulé par l’utilisateur. (-128)\n"
	if err := interpreteEchec(fr); !errors.Is(err, errAnnule) {
		t.Errorf("annulation en français non reconnue : %v", err)
	}
	en := "0:17: execution error: User canceled. (-128)\n"
	if err := interpreteEchec(en); !errors.Is(err, errAnnule) {
		t.Errorf("annulation en anglais non reconnue : %v", err)
	}
	// Une vraie panne ne doit pas être avalée comme une annulation : elle se
	// voit, avec le message d'osascript.
	panne := "0:12: execution error: Application isn’t running. (-600)\n"
	err := interpreteEchec(panne)
	if errors.Is(err, errAnnule) {
		t.Error("une panne a été prise pour une annulation")
	}
	if !strings.Contains(err.Error(), "-600") {
		t.Errorf("le motif de la panne est perdu : %v", err)
	}
}

// TestCheminNettoyeDuSlashFinal : « POSIX path of » rend « /Users/x/ ». Un
// chemin à slash final ne se compare à aucun autre.
func TestCheminNettoyeDuSlashFinal(t *testing.T) {
	got, err := nettoieChemin("/Users/x/Documents/Equipe/\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/x/Documents/Equipe" {
		t.Errorf("chemin mal nettoyé : %q", got)
	}
	for _, mauvais := range []string{"", "\n", "relatif/chemin\n"} {
		if _, err := nettoieChemin(mauvais); err == nil {
			t.Errorf("chemin inattendu accepté : %q", mauvais)
		}
	}
}

// TestLitteralAppleScriptEchappe : un libellé d'espace venu du serveur finira
// dans cette invite. Une chaîne mal échappée est une injection de script, dans
// un processus qui tourne avec les droits de la personne.
func TestLitteralAppleScriptEchappe(t *testing.T) {
	got := litteralAppleScript(`dossier "Clients" \ 2026`)
	want := `"dossier \"Clients\" \\ 2026"`
	if got != want {
		t.Errorf("échappement :\n got %s\nwant %s", got, want)
	}
}

// TestScriptsAppleScriptCompilent : la seule vérification possible sans un
// humain devant l'écran.
//
// `osacompile` analyse et compile le script SANS l'exécuter, donc sans rien
// afficher. Ça attrape les deux fautes qu'une relecture rate : la syntaxe, et un
// échappement cassé par un libellé qui contient un guillemet - et un libellé
// d'espace vient du serveur, donc de quelqu'un d'autre.
func TestScriptsAppleScriptCompilent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osacompile n'existe que sur macOS")
	}
	// Un libellé hostile dans chaque emplacement où du texte entre.
	hostile := `Clients "2026" \ fin`
	for nom, script := range map[string]string{
		"choisirDossier":    scriptChoisirDossier("Choisir le dossier " + hostile),
		"informe":           scriptInforme("Message " + hostile + "\navec un retour à la ligne"),
		"confirme":          scriptConfirme("Partager "+hostile+" ?", "Partager"),
		"demandeTexte":      scriptDemandeTexte("Sous quel nom ?", hostile),
		"demandeMotDePasse": scriptDemandeMotDePasse("Mot de passe pour " + hostile),
		"choix":             scriptChoix("Rejoindre "+hostile+" ?", []string{"Plus tard", "Ne plus proposer", "Choisir un dossier…"}),
	} {
		cmd := exec.Command("osacompile", "-o", filepath.Join(t.TempDir(), nom+".scpt"), "-e", script)
		if sortie, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("le script %s ne compile pas : %v\n%s\n--- script ---\n%s", nom, err, sortie, script)
		}
	}
}

// Le champ du mot de passe doit rester masqué et vide. Ce test garde deux
// régressions qu'une relecture laisserait passer, parce que le script est une
// chaîne : quelqu'un qui retire « with hidden answer » affiche le mot de passe
// à l'écran, et quelqu'un qui met une valeur dans « default answer » la
// pré-remplit. Les deux compilent parfaitement sous osacompile.
func TestScriptMotDePasse_MasqueEtVide(t *testing.T) {
	s := scriptDemandeMotDePasse("Mot de passe")
	if !strings.Contains(s, "with hidden answer") {
		t.Fatal("le champ n'est plus masqué : la saisie s'afficherait à l'écran")
	}
	if !strings.Contains(s, `default answer ""`) {
		t.Fatalf("le champ doit partir VIDE, jamais pré-rempli ; script :\n%s", s)
	}
	// L'invite entre dans le script, la réponse n'en sort jamais : rien de ce
	// que la personne tape ne peut se retrouver dans une chaîne qu'on
	// journalise, puisque le script est construit AVANT la saisie.
	// « log » se cherche en DÉBUT DE LIGNE : chercher la sous-chaîne « log »
	// tout court rend un faux positif sur « display dialog ». L'instrument doit
	// être plus précis que l'objet qu'il mesure.
	for _, ligne := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ligne), "log ") {
			t.Fatalf("le script du mot de passe journalise : %q", ligne)
		}
	}
	if strings.Contains(s, "do shell script") {
		t.Fatal("le script du mot de passe ne doit pas passer par un shell")
	}
}

// `choose from list` doit compiler avec un nombre quelconque d'entrées et des
// libellés hostiles : un nom d'espace vient du serveur, donc de quelqu'un
// d'autre.
func TestScriptChoisirDansListe_Compile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osacompile n'existe que sur macOS")
	}
	cas := map[string][]string{
		"une entrée":   {`Clients "2026" \ fin`},
		"deux entrées": {"shared", "clients"},
		"au-delà de 3": {"a", "b", "c", "d", "e", "f", "g"},
	}
	for nom, entrees := range cas {
		s := scriptChoisirDansListe("Quel dossier retirer ?", entrees)
		cmd := exec.Command("osacompile", "-o", filepath.Join(t.TempDir(), "l.scpt"), "-e", s)
		if sortie, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s : %v\n%s\n--- script ---\n%s", nom, err, sortie, s)
		}
	}
}
