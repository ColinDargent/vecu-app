package sync

// corbeille.go : le filet sous le retrait d'accès (DAR-199).
//
// Perdre l'accès à un dossier n'est pas une suppression. Le serveur garde tout,
// et rendre l'accès fait revenir les fichiers - c'est une dé-synchronisation.
// Mais sur le poste, `reconcilePerimeter` appelait `os.Remove`, et l'utilisateur
// n'avait aucun recours local pendant l'intervalle.
//
// Ce fichier n'ajoute qu'un geste : le fichier sort du dossier de travail vers
// `<racine>/.vecu/corbeille/`, au lieu de disparaître. Aucune garde de
// `reconcilePerimeter` ne bouge - c'est la destination du fichier qui change,
// pas la décision de le retirer.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dureeRetentionCorbeille : au-delà, une entrée est purgée par le cycle.
//
// La borne est dans le CODE et pas dans un runbook, et c'est délibéré. Ce dépôt
// a déjà payé la leçon : le journal du moteur a atteint 3,3 Go avant que
// quiconque ne le remarque, et ce qui l'a corrigé est une rotation, pas une
// consigne. Une règle de discipline qui vit dans une note ne s'applique pas.
const dureeRetentionCorbeille = 30 * 24 * time.Hour

// formatHorodatage : le nom d'un lot de corbeille.
//
// Lisible par un humain qui ouvre le dossier, et surtout PARSABLE : la purge lit
// l'âge sur ce nom, jamais sur le `mtime` du dossier. Un `mtime` bouge quand on
// inspecte, quand on restaure un fichier, quand une sauvegarde passe ; un nom
// ne bouge pas.
const formatHorodatage = "2006-01-02-15h04"

// racineCorbeille : `<racine>/.vecu/corbeille`.
//
// Sous `.vecu/`, qui est déjà écarté du scan par le moteur (avec `.git` et
// `.DS_Store`). La corbeille ne peut donc ni se repousser au serveur, ni compter
// comme un fichier de contenu, ni redescendre chez les autres membres.
func (e *Engine) racineCorbeille() string {
	return filepath.Join(e.dir, vecuDir, "corbeille")
}

// horloge : `time.Now`, sauf en test.
func (e *Engine) horloge() time.Time {
	if e.maintenant != nil {
		return e.maintenant()
	}
	return time.Now()
}

// alaCorbeille déplace le fichier de `rel` vers la corbeille et rend le chemin
// où il a atterri.
//
// Le chemin relatif est CONSERVÉ sous l'horodatage plutôt qu'aplati : la reprise
// est alors un `mv` que la ligne de journal donne en entier, et deux fichiers de
// même nom venus de deux dossiers ne se marchent pas dessus.
//
// Le mode d'échec de cette fonction ne peut pas être « le fichier a disparu ».
// Elle rend une erreur et laisse le fichier en place ; c'est l'appelant qui pose
// la laisse. Rien ici ne retombe sur `os.Remove`.
func (e *Engine) alaCorbeille(rel string) (string, error) {
	source := e.abs(rel)
	cible := filepath.Join(e.racineCorbeille(), e.horloge().Format(formatHorodatage), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(cible), 0o755); err != nil {
		return "", err
	}
	// Le même chemin retiré deux fois dans la même minute : on désambiguïse
	// plutôt que d'écraser. Une corbeille qui efface une entrée de corbeille
	// serait exactement le défaut qu'elle existe pour empêcher.
	cible = cheminLibre(cible)

	// Le cas ordinaire, et le seul qui compte : même volume, donc un rename, donc
	// aucune lecture du contenu.
	if err := os.Rename(source, cible); err == nil {
		return cible, nil
	}
	// Volume différent (un espace monté hors de la racine, DT2). Copier PUIS
	// retirer, et dans cet ordre : si la copie échoue, l'original est intact.
	if err := copieFichier(source, cible); err != nil {
		return "", err
	}
	if err := os.Remove(source); err != nil && !os.IsNotExist(err) {
		// La copie est faite, l'original résiste. Le fichier existe deux fois,
		// ce qui est sans danger ; le dire et ne pas conclure.
		return cible, fmt.Errorf("copié en corbeille mais non retiré de sa place : %w", err)
	}
	return cible, nil
}

// cheminLibre rend `chemin` s'il est libre, sinon `chemin (2)`, `chemin (3)`…
func cheminLibre(chemin string) string {
	if _, err := os.Lstat(chemin); os.IsNotExist(err) {
		return chemin
	}
	ext := filepath.Ext(chemin)
	base := strings.TrimSuffix(chemin, ext)
	for n := 2; n < 1000; n++ {
		essai := fmt.Sprintf("%s (%d)%s", base, n, ext)
		if _, err := os.Lstat(essai); os.IsNotExist(err) {
			return essai
		}
	}
	return chemin
}

// copieFichier copie `source` vers `cible`, contenu et rien d'autre.
func copieFichier(source, cible string) error {
	entree, err := os.Open(source)
	if err != nil {
		return err
	}
	defer entree.Close()
	sortie, err := os.OpenFile(cible, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(sortie, entree); err != nil {
		sortie.Close()
		os.Remove(cible) // une copie tronquée ne doit pas se faire passer pour une sauvegarde
		return err
	}
	return sortie.Close()
}

// purgeCorbeille retire les lots plus vieux que la rétention.
//
// Best-effort de bout en bout : la corbeille est un filet, pas un état. Une
// purge qui échoue ne doit jamais faire échouer un cycle de synchronisation -
// elle réessaiera au cycle suivant, et l'espace disque n'est pas une urgence de
// la seconde.
func (e *Engine) purgeCorbeille() {
	racine := e.racineCorbeille()
	lots, err := os.ReadDir(racine)
	if err != nil {
		return // pas de corbeille : rien à purger, et ce n'est pas une anomalie
	}
	limite := e.horloge().Add(-dureeRetentionCorbeille)
	for _, lot := range lots {
		if !lot.IsDir() {
			continue
		}
		// L'âge se lit sur le NOM. Un dossier dont le nom n'est pas un
		// horodatage n'a pas été posé par nous : on n'y touche pas.
		quand, err := time.ParseInLocation(formatHorodatage, lot.Name(), time.Local)
		if err != nil {
			continue
		}
		if quand.Before(limite) {
			os.RemoveAll(filepath.Join(racine, lot.Name()))
		}
	}
}
