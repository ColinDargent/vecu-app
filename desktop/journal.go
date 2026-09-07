package main

// journal.go : le journal du moteur (vecu-sync.log), sa rotation, et la passe
// unique qui débarrasse vecu-app.log des 3,3 Go de bruit accumulés depuis le
// 26/07.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// seuilJournal : taille au-delà de laquelle vecu-sync.log est tourné.
	//
	// 785 lignes utiles en trois semaines : 10 Mio tient des années de marche
	// normale. Le plafond ne mord donc que quand quelque chose va mal - c'est-à-
	// dire exactement quand on veut les lignes RÉCENTES, pas les anciennes.
	seuilJournal = 10 << 20
	// journauxRetenus : nombre total de fichiers conservés, courant compris.
	// 10 Mio × 3 = 30 Mio au plus sur le disque.
	journauxRetenus = 3
)

// journalTournant : un écrivain à seuil de taille, et UN SEUL, sérialisé.
//
// `logf` est appelée depuis le cycle du moteur et depuis la boucle d'événements
// de la barre de menus. Deux écritures qui se croiseraient pendant une rotation
// écriraient dans le fichier déjà renommé, ou perdraient leur ligne.
// Source: https://pkg.go.dev/sync#Mutex
type journalTournant struct {
	chemin  string
	seuil   int64
	retenus int

	mu     sync.Mutex
	f      *os.File
	taille int64
}

func ouvreJournalTournant(chemin string, seuil int64, retenus int) (*journalTournant, error) {
	j := &journalTournant{chemin: chemin, seuil: seuil, retenus: retenus}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ouvre(); err != nil {
		return nil, err
	}
	return j, nil
}

// ouvre le fichier courant en ajout et relève sa taille. Appelée sous verrou.
//
// O_APPEND : chaque écriture se place à la fin du fichier, sans course sur
// l'offset. La taille est relue du disque et pas supposée à zéro - sinon un
// redémarrage repartirait d'un compteur nul sur un fichier déjà plein, et le
// plafond ne serait plus un plafond.
// Source: https://pkg.go.dev/os#OpenFile
func (j *journalTournant) ouvre() error {
	if err := os.MkdirAll(filepath.Dir(j.chemin), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(j.chemin, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	var taille int64
	if fi, err := f.Stat(); err == nil {
		taille = fi.Size()
	}
	j.f, j.taille = f, taille
	return nil
}

// Close relâche le fichier courant.
//
// AJOUTÉ PENDANT LE PORT WINDOWS, et le manque n'était pas anodin. Le journal
// gardait son descripteur ouvert pour la vie du processus, sans aucun moyen de
// le relâcher : invisible sur macOS, où un fichier ouvert se supprime et se
// renomme sans broncher, mais bloquant sur Windows, qui refuse les deux tant
// qu'un handle vit. Un retrait de l'app, ou tout simplement le ménage d'un
// test, butait dessus avec « The process cannot access the file because it is
// being used by another process ».
//
// Idempotent : appeler deux fois ne rend pas d'erreur. C'est ce qui permet de
// le poser en `defer` sans se demander qui ferme.
func (j *journalTournant) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

func (j *journalTournant) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		if err := j.ouvre(); err != nil {
			return 0, err
		}
	}
	// Tourner AVANT d'écrire, jamais après : une ligne ne doit pas se retrouver
	// coupée en deux entre un fichier et son archive, et le seuil est un plafond
	// et non une moyenne. La garde sur `taille > 0` évite de tourner un fichier
	// vide quand une seule écriture dépasse à elle seule le seuil.
	if j.taille > 0 && j.taille+int64(len(p)) > j.seuil {
		if err := j.tourne(); err != nil {
			return 0, err
		}
	}
	n, err := j.f.Write(p)
	j.taille += int64(n)
	return n, err
}

// tourne décale les archives et rouvre un fichier neuf. Appelée sous verrou.
//
// Décalage du PLUS ANCIEN vers le plus récent (.2 -> .3 avant .1 -> .2) : dans
// l'autre sens, chaque renommage écraserait l'archive suivante et il ne
// resterait qu'un seul fichier, silencieusement.
func (j *journalTournant) tourne() error {
	if err := j.f.Close(); err != nil {
		return err
	}
	j.f = nil

	archives := j.retenus - 1 // `retenus` compte le fichier courant
	if archives < 1 {
		if err := os.Remove(j.chemin); err != nil && !os.IsNotExist(err) {
			return err
		}
		return j.ouvre()
	}
	// La plus ancienne sort du monde.
	if err := os.Remove(fmt.Sprintf("%s.%d", j.chemin, archives)); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := archives - 1; i >= 1; i-- {
		// Une archive manquante n'est pas une erreur : au premier tour, il n'y en
		// a aucune.
		if err := os.Rename(fmt.Sprintf("%s.%d", j.chemin, i), fmt.Sprintf("%s.%d", j.chemin, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(j.chemin, j.chemin+".1"); err != nil {
		return err
	}
	return j.ouvre()
}

// motifBruitJournal : la ligne qui a produit 99,98 % des 3,3 Go de vecu-app.log.
// Un binaire refusé se re-journalisait à chaque cycle, pour les 298 fichiers,
// toutes les ~35 secondes depuis le 26/07. La cause est fermée par le registre
// des hors-périmètre ; ce qui est déjà écrit, lui, reste à nettoyer.
const motifBruitJournal = "fichier binaire, non pris en charge en v2"

// cheminMarqueurFiltre : la passe ne doit tourner qu'UNE fois, et son marqueur
// est un FICHIER dont l'existence est un fait.
//
// Pas un champ dans app.json : c'est exactement le mode d'échec de la migration
// `Versions` en août, où une valeur vide était indiscernable d'une valeur
// absente, et la migration se rejouait. Un fichier existe ou n'existe pas.
func cheminMarqueurFiltre() string {
	p := cheminAppConfig()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "filtre-journal-fait")
}

// filtreJournalApplicatif écarte de vecu-app.log la seule ligne de bruit, une
// fois pour toutes, et rend un compte rendu à journaliser (vide si rien à
// faire).
//
// Les ~785 lignes restantes sont les SEULES traces qu'on ait des écritures, des
// conflits et des incidents depuis le 26/07, dont la fenêtre de la perte du
// 10/08. Une troncature les emporterait avec le bruit, et on le découvrirait le
// jour où on en a besoin.
func filtreJournalApplicatif() string {
	marqueur := cheminMarqueurFiltre()
	if marqueur == "" {
		return ""
	}
	if _, err := os.Stat(marqueur); err == nil {
		return "" // déjà passé
	}
	chemin := cheminJournal()
	avant := tailleFichier(chemin)
	garde, jete, err := filtreEnPlace(chemin, motifBruitJournal)
	if err != nil {
		// Pas fatal, et surtout PAS marqué comme fait : un journal encombré est un
		// inconfort, un marqueur posé à tort rendrait l'échec définitif.
		return fmt.Sprintf("filtre du journal applicatif : %v (réessai au prochain démarrage)", err)
	}
	if err := os.MkdirAll(filepath.Dir(marqueur), 0o755); err == nil {
		_ = os.WriteFile(marqueur, []byte("fait\n"), 0o644)
	}
	return fmt.Sprintf("journal applicatif filtré : %d lignes de bruit écartées, %d lignes conservées, %s -> %s",
		jete, garde, tailleLisible(avant), tailleLisible(tailleFichier(chemin)))
}

// filtreEnPlace réécrit `chemin` sans les lignes contenant `motif`.
//
// Deux temps, et le second ne part que si le premier est vérifié : on écrit
// d'abord une COPIE filtrée à côté, on recompte ses lignes indépendamment, et
// l'original n'est remplacé qu'ensuite. Sans cette relecture, une écriture
// courte ou un disque plein emporterait les 785 lignes utiles en silence.
func filtreEnPlace(chemin, motif string) (garde, jete int, err error) {
	src, err := os.Open(chemin)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil // rien à filtrer : ce n'est pas un échec
		}
		return 0, 0, err
	}
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(chemin), "vecu-filtre-*")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	// bufio.Reader, JAMAIS bufio.Scanner. Le Scanner plafonne son jeton à 64 KiB
	// par défaut et rend ErrTooLong au-delà : une trace de panic dépasse ce
	// seuil, et ce sont exactement les lignes qu'on veut CONSERVER. ReadString
	// n'a pas cette limite, et lit en flux - les 3,3 Go ne passent jamais en
	// mémoire.
	// Source: https://pkg.go.dev/bufio#Scanner.Buffer
	// Source: https://pkg.go.dev/bufio#Reader.ReadString
	r := bufio.NewReaderSize(src, 1<<20)
	w := bufio.NewWriterSize(tmp, 1<<20)
	for {
		ligne, errLire := r.ReadString('\n')
		if ligne != "" {
			if strings.Contains(ligne, motif) {
				jete++
			} else {
				garde++
				if _, e := w.WriteString(ligne); e != nil {
					return garde, jete, e
				}
			}
		}
		if errLire == io.EOF {
			break
		}
		if errLire != nil {
			return garde, jete, errLire
		}
	}
	if e := w.Flush(); e != nil {
		return garde, jete, e
	}
	if e := tmp.Sync(); e != nil {
		return garde, jete, e
	}

	// Vérification INDÉPENDANTE, sur la copie, avant de toucher à l'original.
	relu, e := compteLignes(tmp.Name())
	if e != nil {
		return garde, jete, e
	}
	if relu != garde {
		return garde, jete, fmt.Errorf("copie filtrée incomplète (%d lignes écrites, %d relues) : l'original n'est pas remplacé", garde, relu)
	}

	// Remplacement EN PLACE, et surtout PAS par renommage. launchd tient ce
	// fichier ouvert : c'est sa destination stdout/stderr, et le plist n'est pas
	// réécrit. Un renommage le laisserait écrire dans l'inode détaché - les
	// panics iraient dans un fichier invisible, et les 3,3 Go ne seraient même
	// pas rendus au disque tant que le service tourne. On réécrit donc le MÊME
	// inode depuis l'offset 0, puis on le tronque.
	//
	// Les quelques lignes que launchd écrirait pendant la passe sont perdues.
	// C'est le prix assumé, et il est presque théorique : depuis que le moteur a
	// son propre journal, ce fichier ne reçoit plus que des panics.
	if _, e := tmp.Seek(0, io.SeekStart); e != nil {
		return garde, jete, e
	}
	dst, e := os.OpenFile(chemin, os.O_WRONLY, 0o644)
	if e != nil {
		return garde, jete, e
	}
	defer dst.Close()
	n, e := io.Copy(dst, tmp)
	if e != nil {
		return garde, jete, e
	}
	if e := dst.Truncate(n); e != nil {
		return garde, jete, e
	}
	return garde, jete, dst.Sync()
}

func compteLignes(chemin string) (int, error) {
	f, err := os.Open(chemin)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	n := 0
	for {
		ligne, err := r.ReadString('\n')
		if ligne != "" {
			n++
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

func tailleFichier(chemin string) int64 {
	fi, err := os.Stat(chemin)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func tailleLisible(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f Go", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f Mo", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f Ko", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d o", n)
}
