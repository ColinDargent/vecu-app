package sync

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPreAnalyseAnnonceAvant : le contrat de DT4, qui est celui de F6 - « on
// annonce avant, on ne découvre pas après ». Adopter un dossier envoie son
// contenu à tout le monde, et il n'y a pas de bouton d'annulation.
func TestPreAnalyseAnnonceAvant(t *testing.T) {
	dossier := t.TempDir()
	ecris(t, dossier, "notes.md", "du texte\n")
	ecris(t, dossier, "sous/autre.md", "encore du texte accentué : é à ç\n")
	ecrisOctets(t, dossier, "image.png", []byte{0xff, 0xd8, 0x00, 0x9f, 0x10})
	ecris(t, dossier, ".DS_Store", "poubelle du Finder")
	ecris(t, dossier, ".git/config", "[core]\n")

	e := &Engine{dir: t.TempDir(), logf: func(string, ...any) {}}
	a := e.PreAnalyse(dossier)

	if !a.Complete {
		t.Errorf("parcours annoncé incomplet sans raison : %+v", a)
	}
	if a.Fichiers != 2 {
		t.Errorf("fichiers qui partiraient = %d, attendu 2 : %+v", a.Fichiers, a)
	}
	if a.HorsPerimetre != 1 {
		t.Errorf("hors périmètre = %d, attendu 1 (le .png)", a.HorsPerimetre)
	}
	if a.Ignores != 1 {
		t.Errorf("ignorés = %d, attendu 1 (le .DS_Store ; le .git est écarté en tant que dossier)", a.Ignores)
	}
	if a.Octets == 0 || a.OctetsHorsPerimetre != 5 {
		t.Errorf("volumes = %d / %d", a.Octets, a.OctetsHorsPerimetre)
	}
}

// TestUnDossierIllisibleNeSAnnoncePasVide : le principe que DT4 désigne
// nommément, repris de `contientDesFichiers` - « je n'ai pas pu regarder » n'est
// pas « il n'y a rien ». Annoncer « 2 fichiers partiront » sur un dossier dont
// la moitié était illisible serait un mensonge par omission.
func TestUnDossierIllisibleNeSAnnoncePasVide(t *testing.T) {
	// Précondition : un dossier illisible par bit de permission Unix, que Windows
	// ne porte pas. Ce que ça laisse découvert là-bas : l'annonce « analyse
	// incomplète » quand un parcours échoue. Noté dans spec-port-windows.md.
	if runtime.GOOS == "windows" {
		t.Skip("précondition = dossier illisible par permission Unix, sans effet sur Windows")
	}
	dossier := t.TempDir()
	ecris(t, dossier, "visible.md", "texte\n")
	interdit := filepath.Join(dossier, "ferme")
	if err := os.MkdirAll(filepath.Join(interdit, "dedans"), 0o755); err != nil {
		t.Fatal(err)
	}
	ecris(t, dossier, "ferme/dedans/cache.md", "texte\n")
	if err := os.Chmod(interdit, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(interdit, 0o755) })

	e := &Engine{dir: t.TempDir(), logf: func(string, ...any) {}}
	a := e.PreAnalyse(dossier)

	if a.Complete {
		t.Error("un dossier illisible a été annoncé comme entièrement examiné")
	}
	if a.Illisibles == 0 {
		t.Error("rien n'a été compté comme illisible")
	}
}

// TestGardeBloquanteSurUnEspaceDejaMonte : structurellement cassé, donc refusé
// et pas confirmé. Le même fichier appartiendrait à deux espaces : la résolution
// de montage choisit le plus profond, donc le contenu partirait sous un nom et
// serait ignoré sous l'autre, avec des droits qui ne sont pas ceux qu'on croit.
func TestGardeBloquanteSurUnEspaceDejaMonte(t *testing.T) {
	monte := t.TempDir()
	e := &Engine{dir: t.TempDir(), montages: map[string]string{"clients": monte}, logf: func(string, ...any) {}}

	dedans := filepath.Join(monte, "sous-dossier")
	if err := os.MkdirAll(dedans, 0o755); err != nil {
		t.Fatal(err)
	}
	if a := e.PreAnalyse(dedans); !a.Refuse() {
		t.Errorf("un dossier DANS un espace monté n'a pas été refusé : %+v", a.Gardes)
	}
	// Et le sens inverse : un dossier qui CONTIENT un espace monté.
	if a := e.PreAnalyse(filepath.Dir(monte)); !a.Refuse() {
		t.Errorf("un dossier qui contient un espace monté n'a pas été refusé : %+v", a.Gardes)
	}
	// Un dossier sans rapport passe.
	if a := e.PreAnalyse(t.TempDir()); a.Refuse() {
		t.Errorf("un dossier sans rapport a été refusé : %+v", a.Gardes)
	}
}

// TestGardeBloquanteSurLeHome : jamais un geste intentionnel, et le rayon
// d'action est le poste entier. Le confirmer serait offrir un bouton dont la
// bonne réponse est toujours « non ».
func TestGardeBloquanteSurLeHome(t *testing.T) {
	maison := t.TempDir()
	t.Setenv("HOME", maison)
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", maison)
	t.Setenv("AppData", filepath.Join(maison, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(maison, "AppData", "Local"))
	e := &Engine{dir: t.TempDir(), logf: func(string, ...any) {}}
	if a := e.PreAnalyse(maison); !a.Refuse() {
		t.Errorf("le dossier personnel entier n'a pas été refusé : %+v", a.Gardes)
	}
}

// TestSynchroniseurTiersSeConfirmeEtNeSeRefusePas : deux synchroniseurs sur les
// mêmes fichiers est un vrai danger, mais la détection est une HEURISTIQUE sur
// le chemin. Quelqu'un peut avoir un dossier nommé « Dropbox » qui n'en est pas
// un ; refuser sur une heuristique bloque un usage légitime sans recours.
func TestSynchroniseurTiersSeConfirmeEtNeSeRefusePas(t *testing.T) {
	base := t.TempDir()
	dossier := filepath.Join(base, "Dropbox", "Equipe")
	if err := os.MkdirAll(dossier, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{dir: t.TempDir(), logf: func(string, ...any) {}}
	a := e.PreAnalyse(dossier)

	if a.Refuse() {
		t.Errorf("refusé alors qu'une heuristique ne devrait que prévenir : %+v", a.Gardes)
	}
	if !a.AConfirmer() {
		t.Errorf("aucun avertissement sur un dossier Dropbox : %+v", a.Gardes)
	}
	if !strings.Contains(a.Gardes[0].Motif, "Dropbox") {
		t.Errorf("l'avertissement ne nomme pas l'outil : %q", a.Gardes[0].Motif)
	}
}

// TestDepotGitPrevientSurLHistoire : correction de DT4, vérifiée dans le code.
// Le CDC justifie cette garde par « le .git partirait fichier par fichier » -
// c'est déjà faux, `ignored()` écarte ce segment. Ce qui reste vrai est
// l'inverse, et vaut d'être dit : c'est parce que .git ne part PAS que
// l'historique ne suivra pas, et quelqu'un qui partage un dépôt s'y attend.
func TestDepotGitPrevientSurLHistoire(t *testing.T) {
	dossier := t.TempDir()
	ecris(t, dossier, ".git/config", "[core]\n")
	ecris(t, dossier, "main.go", "package main\n")

	e := &Engine{dir: t.TempDir(), logf: func(string, ...any) {}}
	a := e.PreAnalyse(dossier)

	if a.Refuse() {
		t.Errorf("un dépôt git a été refusé : %+v", a.Gardes)
	}
	var dit bool
	for _, g := range a.Gardes {
		if strings.Contains(g.Motif, "git") {
			dit = true
			if !strings.Contains(g.Motif, "historique") {
				t.Errorf("l'avertissement ne dit pas ce qui est réellement en jeu : %q", g.Motif)
			}
		}
	}
	if !dit {
		t.Errorf("rien n'a été dit sur le dépôt git : %+v", a.Gardes)
	}
	// Et rien de .git/ n'est compté comme partant.
	if a.Fichiers != 1 {
		t.Errorf("fichiers = %d, attendu 1 (le .git ne part pas)", a.Fichiers)
	}
}

// TestUnGrosFichierTexteNestPasPrisPourUnBinaire : le jugement se fait sur les
// premiers kilo-octets. Une rune multi-octets coupée en deux à la frontière
// ferait passer un gros fichier français pour un binaire - et il serait annoncé
// comme « ne partira pas », ce que personne ne vérifierait avant d'y perdre du
// travail.
func TestUnGrosFichierTexteNestPasPrisPourUnBinaire(t *testing.T) {
	dossier := t.TempDir()
	// « é » fait deux octets : en répéter assez pour qu'une frontière à 8 KiB
	// tombe au milieu de l'un d'eux.
	gros := strings.Repeat("éàçù ", 4000)
	ecris(t, dossier, "long.md", gros)

	e := &Engine{dir: t.TempDir(), logf: func(string, ...any) {}}
	a := e.PreAnalyse(dossier)
	if a.HorsPerimetre != 0 {
		t.Errorf("un fichier texte accentué a été classé binaire : %+v", a)
	}
	if a.Fichiers != 1 {
		t.Errorf("fichiers = %d", a.Fichiers)
	}
}

// TestFichierVideEstDuTexte : un fichier vide n'est pas un binaire.
func TestFichierVideEstDuTexte(t *testing.T) {
	dossier := t.TempDir()
	ecris(t, dossier, "vide.md", "")
	e := &Engine{dir: t.TempDir(), logf: func(string, ...any) {}}
	if a := e.PreAnalyse(dossier); a.Fichiers != 1 || a.HorsPerimetre != 0 {
		t.Errorf("fichier vide mal classé : %+v", a)
	}
}

func ecris(t *testing.T, dir, rel, contenu string) {
	t.Helper()
	ecrisOctets(t, dir, rel, []byte(contenu))
}

func ecrisOctets(t *testing.T, dir, rel string, b []byte) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSynchroniseurTiersReconnaitUnCheminWindows : la garde doit parler quel que
// soit le séparateur.
//
// Elle était MUETTE sur Windows jusqu'au 06/09 : les motifs sont écrits avec des
// barres obliques et un chemin Windows n'en contient aucune. Or OneDrive est
// présent par défaut sur presque toutes les machines Windows, et le dossier
// « Documents » y est souvent redirigé sans que la personne le sache - c'est
// exactement le cas où deux synchroniseurs se disputent les mêmes fichiers.
//
// Le test tourne sur les DEUX systèmes : il passe des chemins Windows littéraux,
// que `ToSlash` ramène à la forme des motifs indépendamment de l'OS hôte.
func TestSynchroniseurTiersReconnaitUnCheminWindows(t *testing.T) {
	cas := []struct {
		chemin string
		veut   string
	}{
		{`C:\Users\colin\OneDrive\vault`, "OneDrive"},
		{`C:\Users\colin\Dropbox\second-brain`, "Dropbox"},
		{`C:\Users\colin\Google Drive\x`, "Google Drive"},
		{`C:\Users\colin\Documents\vault`, ""},
	}
	for _, c := range cas {
		if got := synchroniseurTiers(c.chemin); got != c.veut {
			t.Errorf("synchroniseurTiers(%q) = %q, veut %q", c.chemin, got, c.veut)
		}
	}
}
