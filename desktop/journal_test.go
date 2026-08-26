package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestDeuxJournauxSepares : le moteur et launchd n'écrivent plus dans le même
// fichier, et ce test existe pour qu'ils ne s'y retrouvent jamais.
//
// Tant qu'ils partageaient un chemin, aucune rotation n'était possible : une
// rotation par renommage laisse le descripteur de launchd sur l'inode renommé,
// donc les panics continueraient d'aller dans un fichier tourné, invisible, et
// qui grossirait sans que personne le voie. Faire converger de nouveau les deux
// chemins rouvrirait ça en silence.
func TestDeuxJournauxSepares(t *testing.T) {
	app, moteur := cheminJournal(), cheminJournalSync()
	if app == moteur {
		t.Fatalf("le moteur et launchd écrivent de nouveau dans le même fichier : %s", app)
	}
	if !strings.HasSuffix(app, "vecu-app.log") {
		t.Errorf("la destination stdout/stderr du plist a changé de nom (%s) : le plist n'est pas réécrit après une mise à jour automatique, ce chemin doit rester tel quel", app)
	}
	if !strings.HasSuffix(moteur, "vecu-sync.log") {
		t.Errorf("journal du moteur = %s", moteur)
	}
	if filepath.Dir(app) != filepath.Dir(moteur) {
		t.Errorf("les deux journaux doivent rester côte à côte : %s vs %s", app, moteur)
	}
}

// TestLePlistPointeSurLeJournalApplicatif : le plist est le seul endroit qui
// décide où vont stdout/stderr. La slice 3 ne le touche pas - c'est tout son
// intérêt - et ce test le vérifie plutôt que de le supposer.
func TestLePlistPointeSurLeJournalApplicatif(t *testing.T) {
	c := serviceCfg{
		label:   "com.exemple.vecu",
		binaire: "/Applications/Vécu.app/Contents/MacOS/vecu",
		racine:  "/Users/x/vault",
		journal: cheminJournal(),
	}
	plist := c.contenuPlist()
	if !strings.Contains(plist, cheminJournal()) {
		t.Errorf("le plist ne redirige pas vers le journal applicatif :\n%s", plist)
	}
	if strings.Contains(plist, cheminJournalSync()) {
		t.Errorf("le plist redirige vers le journal du MOTEUR : launchd et la rotation se disputeraient le même fichier\n%s", plist)
	}
}

// TestOuvreJournalEcritDansLeJournalDuMoteur : preuve d'exécution, pas de
// lecture. HOME est déplacé le temps du test pour ne rien écrire dans le vrai
// ~/Library/Logs.
func TestOuvreJournalEcritDansLeJournalDuMoteur(t *testing.T) {
	maison := t.TempDir()
	t.Setenv("HOME", maison)

	w := ouvreJournal()
	if w == os.Stderr {
		t.Fatal("repli sur stderr : le journal n'a pas pu être ouvert")
	}
	if _, err := io.WriteString(w, "une ligne de moteur\n"); err != nil {
		t.Fatal(err)
	}

	attendu := filepath.Join(maison, "Library", "Logs", "vecu-sync.log")
	b, err := os.ReadFile(attendu)
	if err != nil {
		t.Fatalf("rien écrit dans %s : %v", attendu, err)
	}
	if !strings.Contains(string(b), "une ligne de moteur") {
		t.Errorf("contenu inattendu : %q", b)
	}
	// Et surtout : rien dans le journal applicatif, qui n'appartient plus qu'à
	// launchd.
	if _, err := os.Stat(filepath.Join(maison, "Library", "Logs", "vecu-app.log")); err == nil {
		t.Error("le moteur a écrit dans vecu-app.log : le déluge y retournerait")
	}
}

// TestRotationAuSeuil : le plafond est un plafond. Sans rotation, vecu-sync.log
// reprendrait exactement la trajectoire de vecu-app.log - 3,3 Go - la prochaine
// fois qu'une ligne se met à boucler.
func TestRotationAuSeuil(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu-sync.log")
	// Seuil minuscule et 3 fichiers retenus : les valeurs de prod (10 Mio × 3)
	// sont les mêmes règles à une autre échelle.
	j, err := ouvreJournalTournant(chemin, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	ligne := strings.Repeat("x", 30) + "\n" // 31 octets
	for i := 0; i < 40; i++ {
		if _, err := io.WriteString(j, ligne); err != nil {
			t.Fatalf("écriture %d : %v", i, err)
		}
	}

	// Le fichier courant + 2 archives, et rien au-delà : `retenus` compte le
	// courant.
	var total int64
	for _, p := range []string{chemin, chemin + ".1", chemin + ".2"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s manquant : %v", p, err)
		}
		if fi.Size() > 100 {
			t.Errorf("%s dépasse le seuil : %d octets", p, fi.Size())
		}
		total += fi.Size()
	}
	if _, err := os.Stat(chemin + ".3"); err == nil {
		t.Error("une 4e archive existe : la rétention ne borne rien")
	}
	if total > 300 {
		t.Errorf("total retenu %d octets, plafond annoncé 300", total)
	}

	// L'archive la plus récente doit porter les lignes les PLUS anciennes des
	// retenues : c'est le sens du décalage, et l'inverser passerait inaperçu
	// autrement.
	if b, err := os.ReadFile(chemin + ".1"); err != nil || !strings.Contains(string(b), "x") {
		t.Errorf("archive .1 vide ou illisible : %v", err)
	}
}

// TestRotationSousEcrituresConcurrentes : `logf` est appelée depuis le cycle du
// moteur ET depuis la boucle d'événements de la barre de menus. Une rotation
// pendant une écriture concurrente perdrait des lignes, ou en écrirait dans le
// fichier déjà renommé. À lancer avec -race.
func TestRotationSousEcrituresConcurrentes(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu-sync.log")
	j, err := ouvreJournalTournant(chemin, 2000, 4)
	if err != nil {
		t.Fatal(err)
	}
	const ecrivains, parEcrivain = 8, 60
	var wg sync.WaitGroup
	for e := 0; e < ecrivains; e++ {
		wg.Add(1)
		go func(e int) {
			defer wg.Done()
			for i := 0; i < parEcrivain; i++ {
				if _, err := fmt.Fprintf(j, "ecrivain %d ligne %d\n", e, i); err != nil {
					t.Errorf("écriture : %v", err)
					return
				}
			}
		}(e)
	}
	wg.Wait()

	// Aucune ligne perdue et aucune ligne coupée : on recompte sur le courant et
	// toutes les archives encore retenues.
	vues := 0
	for _, p := range []string{chemin, chemin + ".1", chemin + ".2", chemin + ".3"} {
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			if l == "" {
				continue
			}
			if !strings.HasPrefix(l, "ecrivain ") {
				t.Fatalf("ligne coupée par une rotation : %q", l)
			}
			vues++
		}
	}
	// La rétention peut avoir jeté les plus anciennes : on vérifie qu'il n'y a ni
	// perte au-delà de ce que la rotation a légitimement écarté, ni duplication.
	if vues > ecrivains*parEcrivain {
		t.Errorf("%d lignes pour %d écrites : des lignes ont été dupliquées", vues, ecrivains*parEcrivain)
	}
	if vues == 0 {
		t.Error("tout a disparu")
	}
}

// TestFiltreConserveToutSaufLeBruit : les ~785 lignes restantes sont les SEULES
// traces qu'on ait des écritures, conflits et incidents depuis le 26/07, dont
// la fenêtre de la perte du 10/08. Une troncature les emporterait avec le bruit,
// et on le découvrirait le jour où on en a besoin.
func TestFiltreConserveToutSaufLeBruit(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu-app.log")
	bruit := "2026/08/01 10:00:00 non synchronisé : shared/a.png (fichier binaire, non pris en charge en v2 : il reste sur ce poste)\n"
	utiles := []string{
		"2026/07/26 18:13:38 espaces montés : [shared]\n",
		"2026/08/10 17:20:00 conflit sur shared/tasks.md : copie créée (tasks.conflit.md)\n",
		"2026/08/12 09:00:00 écrit (pull) shared/note.md\n",
	}
	var contenu strings.Builder
	contenu.WriteString(utiles[0])
	for i := 0; i < 5000; i++ {
		contenu.WriteString(bruit)
	}
	contenu.WriteString(utiles[1])
	for i := 0; i < 5000; i++ {
		contenu.WriteString(bruit)
	}
	contenu.WriteString(utiles[2])
	if err := os.WriteFile(chemin, []byte(contenu.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	garde, jete, err := filtreEnPlace(chemin, motifBruitJournal)
	if err != nil {
		t.Fatal(err)
	}
	if jete != 10000 {
		t.Errorf("bruit écarté = %d, attendu 10000", jete)
	}
	if garde != len(utiles) {
		t.Errorf("lignes conservées = %d, attendu %d", garde, len(utiles))
	}
	b, err := os.ReadFile(chemin)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != strings.Join(utiles, "") {
		t.Errorf("le contenu conservé n'est pas exactement les lignes utiles, dans l'ordre :\n%q", b)
	}
}

// TestFiltreConserveUneLigneTresLongue : le piège documenté, et il vise
// précisément les lignes qu'on veut garder. `bufio.Scanner` plafonne son jeton à
// 64 KiB par défaut et rend ErrTooLong au-delà - or une trace de panic dépasse
// ce seuil. Utiliser un Scanner ici aurait fait échouer la passe exactement sur
// l'incident qu'on cherche à conserver.
// Source: https://pkg.go.dev/bufio#Scanner.Buffer
func TestFiltreConserveUneLigneTresLongue(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu-app.log")
	panique := "panic: runtime error: " + strings.Repeat("goroutine stack trace ", 20000) + "\n"
	if len(panique) <= 64*1024 {
		t.Fatalf("prérequis : la ligne doit dépasser 64 KiB, elle fait %d octets", len(panique))
	}
	contenu := "2026/08/01 10:00:00 non synchronisé : a.png (fichier binaire, non pris en charge en v2)\n" +
		panique +
		"2026/08/01 10:00:01 non synchronisé : b.png (fichier binaire, non pris en charge en v2)\n"
	if err := os.WriteFile(chemin, []byte(contenu), 0o644); err != nil {
		t.Fatal(err)
	}

	garde, jete, err := filtreEnPlace(chemin, motifBruitJournal)
	if err != nil {
		t.Fatalf("la passe a échoué sur une ligne longue : %v", err)
	}
	if garde != 1 || jete != 2 {
		t.Errorf("garde=%d jete=%d, attendu 1 et 2", garde, jete)
	}
	b, err := os.ReadFile(chemin)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != panique {
		t.Errorf("la trace de panic n'a pas survécu intacte (%d octets conservés sur %d)", len(b), len(panique))
	}
}

// TestFiltreNeTournePasDeuxFois : le mode d'échec de la migration `Versions` en
// août - une map vidée relisait nil et rejouait la migration. Le marqueur est un
// fichier : son existence est un fait, pas une valeur à interpréter.
func TestFiltreNeTournePasDeuxFois(t *testing.T) {
	maison := t.TempDir()
	t.Setenv("HOME", maison)

	journal := filepath.Join(maison, "Library", "Logs", "vecu-app.log")
	if err := os.MkdirAll(filepath.Dir(journal), 0o755); err != nil {
		t.Fatal(err)
	}
	contenu := "2026/08/01 10:00:00 non synchronisé : a.png (fichier binaire, non pris en charge en v2)\n" +
		"2026/08/10 17:20:00 conflit sur shared/tasks.md : copie créée\n"
	if err := os.WriteFile(journal, []byte(contenu), 0o644); err != nil {
		t.Fatal(err)
	}

	premier := filtreJournalApplicatif()
	if premier == "" {
		t.Fatal("la passe n'a rien fait au premier démarrage")
	}
	apres, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}

	// Le journal se remet à recevoir des lignes de bruit (par exemple un poste
	// revenu un cycle sur une version antérieure). La passe ne doit PAS repartir.
	f, err := os.OpenFile(journal, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("2026/08/21 09:00:00 non synchronisé : z.png (fichier binaire, non pris en charge en v2)\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if second := filtreJournalApplicatif(); second != "" {
		t.Errorf("la passe a tourné une seconde fois : %s", second)
	}
	final, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) <= len(apres) {
		t.Errorf("la seconde passe a bien retouché le fichier (%d -> %d octets)", len(apres), len(final))
	}
}

// TestFiltreEcritDansLeMemeInode : launchd tient ce fichier ouvert - c'est sa
// destination stdout/stderr, et le plist n'est pas réécrit. Remplacer par
// renommage laisserait launchd écrire dans l'inode détaché : les panics
// iraient dans un fichier invisible, et les 3,3 Go ne seraient même pas rendus
// au disque tant que le service tourne.
func TestFiltreEcritDansLeMemeInode(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu-app.log")
	contenu := "2026/08/01 10:00:00 non synchronisé : a.png (fichier binaire, non pris en charge en v2)\n" +
		"2026/08/10 17:20:00 une ligne utile\n"
	if err := os.WriteFile(chemin, []byte(contenu), 0o644); err != nil {
		t.Fatal(err)
	}
	avant, err := os.Stat(chemin)
	if err != nil {
		t.Fatal(err)
	}

	// Un écrivain qui tient le fichier ouvert, comme launchd.
	tenu, err := os.OpenFile(chemin, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer tenu.Close()

	if _, _, err := filtreEnPlace(chemin, motifBruitJournal); err != nil {
		t.Fatal(err)
	}

	apres, err := os.Stat(chemin)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(avant, apres) {
		t.Fatal("le fichier a changé d'inode : le descripteur de launchd écrirait dans le vide")
	}
	// Et le descripteur déjà ouvert écrit toujours dans le fichier qu'on lit.
	if _, err := tenu.WriteString("panic: après la passe\n"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(chemin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "panic: après la passe") {
		t.Errorf("l'écrivain qui tenait le fichier n'y écrit plus :\n%q", b)
	}
	if !strings.Contains(string(b), "une ligne utile") {
		t.Errorf("la ligne utile a disparu :\n%q", b)
	}
}
