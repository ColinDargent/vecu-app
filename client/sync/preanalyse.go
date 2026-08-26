package sync

// preanalyse.go : DT4 - adopter un dossier déjà rempli, sans surprise.
//
// « On annonce avant, on ne découvre pas après. » C'est le contrat de F6, et il
// vaut d'autant plus ici : adopter un dossier envoie son contenu à tout le monde
// et il n'y a pas de bouton d'annulation.
//
// Le principe de prudence est celui de `contientDesFichiers`, et DT4 le désigne
// nommément : une erreur de parcours répond « oui, il y a du contenu », jamais
// « non ». « Je n'ai pas pu regarder » n'est pas « il n'y a rien ».

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Garde : un obstacle constaté avant d'adopter un dossier.
//
// LA CLASSIFICATION EST ICI, et nulle part ailleurs. Le CDC laisse le choix
// ouvert - « à refuser ou à confirmer explicitement » - et la réponse n'est pas
// la même pour les quatre cas : un dossier imbriqué dans un espace monté est
// structurellement cassé, une détection de synchroniseur tiers est une
// heuristique qui peut se tromper. Refuser sur une heuristique bloque un usage
// légitime ; confirmer sur un cas cassé laisse construire sur du sable.
type Garde struct {
	// Bloquant : true = on refuse, l'adoption ne peut pas être bonne.
	// false = on prévient, la personne tranche en connaissance de cause.
	Bloquant bool
	Motif    string
}

// Analyse : ce que l'adoption de ce dossier ferait.
type Analyse struct {
	Chemin string

	// Fichiers / Octets : ce qui partirait vraiment.
	Fichiers int
	Octets   int64
	// Ignores : écartés par le `.vecuignore` et les règles par défaut.
	Ignores int
	// HorsPerimetre : binaires. La v2 ne transporte que du texte, et ils
	// resteront sur ce poste - c'est le genre posé par F6 le matin même.
	HorsPerimetre       int
	OctetsHorsPerimetre int64
	// Illisibles : rencontrés sans pouvoir être examinés (droits, E/S, lien).
	Illisibles int

	Gardes []Garde
	// Complete : le parcours est-il allé au bout ? Un « non » rend tous les
	// comptes ci-dessus des MINORANTS, et l'annonce doit le dire.
	Complete bool
}

// Refuse : au moins une garde bloquante.
func (a Analyse) Refuse() bool {
	for _, g := range a.Gardes {
		if g.Bloquant {
			return true
		}
	}
	return false
}

// AConfirmer : des avertissements, mais rien de bloquant.
func (a Analyse) AConfirmer() bool { return !a.Refuse() && len(a.Gardes) > 0 }

// PreAnalyse examine un dossier local et rend ce que son adoption ferait, sans
// rien modifier.
func (e *Engine) PreAnalyse(chemin string) Analyse {
	abs, err := filepath.Abs(chemin)
	if err != nil {
		abs = chemin
	}
	abs = filepath.Clean(abs)
	a := Analyse{Chemin: abs, Complete: true}

	info, err := os.Stat(abs)
	switch {
	case err != nil:
		a.Gardes = append(a.Gardes, Garde{true, "ce dossier n'est pas lisible : " + err.Error()})
		a.Complete = false
		return a
	case !info.IsDir():
		a.Gardes = append(a.Gardes, Garde{true, "ce n'est pas un dossier"})
		return a
	}

	a.Gardes = append(a.Gardes, e.gardesDe(abs)...)
	// Les règles du dossier lui-même, s'il en porte. Sans elles, l'annonce
	// compterait comme partant des fichiers que le dossier déclare exclure, et
	// « on annonce avant » deviendrait « on annonce autre chose ».
	motifs, err := LoadIgnores(abs)
	if err != nil {
		a.Complete = false
	}
	e.compte(&a, abs, motifs)
	return a
}

// gardesDe : les quatre situations que DT4 nomme, chacune avec sa réponse.
func (e *Engine) gardesDe(abs string) []Garde {
	var out []Garde

	// (1) BLOQUANT - déjà dans un espace monté, ou en contient un.
	//
	// Structurellement cassé, pas discutable : le même fichier appartiendrait à
	// deux espaces. La résolution de montage choisit le plus profond, donc le
	// contenu serait synchronisé sous un nom et ignoré sous l'autre - et les
	// droits appliqués ne seraient pas ceux qu'on croit.
	for nom, racine := range e.montages {
		switch {
		case sousLaRacine(racine, abs):
			out = append(out, Garde{true, fmt.Sprintf("ce dossier est déjà dans l'espace « %s » (%s)", nom, racine)})
		case sousLaRacine(abs, racine):
			out = append(out, Garde{true, fmt.Sprintf("ce dossier contient l'espace « %s » (%s), qui est déjà monté", nom, racine)})
		}
	}
	if sousLaRacine(e.dir, abs) {
		out = append(out, Garde{true, "ce dossier est déjà dans la racine synchronisée (" + e.dir + ")"})
	}

	// (2) BLOQUANT - le home entier ou un dossier système.
	//
	// Jamais un geste intentionnel, et le rayon d'action est celui du poste
	// entier. Le confirmer serait offrir un bouton dont la bonne réponse est
	// toujours « non ».
	if maison, err := os.UserHomeDir(); err == nil && filepath.Clean(maison) == abs {
		out = append(out, Garde{true, "c'est le dossier personnel entier"})
	}
	for _, systeme := range []string{"/", "/System", "/Library", "/Applications", "/usr", "/etc", "/private", "/Volumes"} {
		if abs == systeme {
			out = append(out, Garde{true, "c'est un dossier système (" + systeme + ")"})
		}
	}

	// (3) À CONFIRMER - déjà synchronisé par un autre outil.
	//
	// Deux synchroniseurs sur les mêmes fichiers est un vrai danger. Mais la
	// détection est une HEURISTIQUE sur le chemin : quelqu'un peut avoir un
	// dossier nommé « Dropbox » qui n'en est pas un. Refuser sur une heuristique
	// bloquerait un usage légitime sans recours ; on prévient, la personne sait.
	if outil := synchroniseurTiers(abs); outil != "" {
		out = append(out, Garde{false, "ce dossier semble déjà synchronisé par " + outil +
			" : deux synchroniseurs sur les mêmes fichiers se disputent les écritures"})
	}

	// (4) INFORMATION - un dépôt git.
	//
	// CORRECTION DE DT4, vérifiée dans le code : le CDC justifie cette garde par
	// « le `.git` partirait fichier par fichier ». C'est déjà faux - `ignored()`
	// écarte le segment `.git` depuis toujours, donc rien de `.git/` ne part.
	//
	// Ce qui reste vrai est l'inverse, et mérite quand même d'être dit : c'est
	// justement parce que `.git` ne part PAS que l'historique ne suivra pas.
	// Quelqu'un qui partage un dépôt s'attend à partager ses commits.
	// (5) BLOQUANT - le dossier choisi est lui-même un lien symbolique.
	//
	// Mesurable, pas théorique : `scanLocal` marche le dossier avec `WalkDir`,
	// qui ne suit pas les liens. Un montage sur un lien ne parcourrait donc
	// RIEN, et l'espace se synchroniserait vide, en silence - le pire résultat
	// possible pour un geste dont la promesse est « ton dossier est partagé ».
	// `PreAnalyse` ne peut pas le voir seule : elle appelle `os.Stat`, qui suit
	// le lien et répond « c'est un dossier ».
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		out = append(out, Garde{true, "ce dossier est un lien symbolique : son contenu ne serait pas parcouru. Choisir le dossier réel vers lequel il pointe"})
	}

	if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
		out = append(out, Garde{false, "ce dossier est un dépôt git : le contenu partira, mais pas le `.git`, " +
			"donc ni l'historique ni les branches"})
	}
	return out
}

// synchroniseurTiers : reconnaît les emplacements des services grand public.
// Heuristique par le chemin, jamais une certitude - c'est pour ça que la garde
// correspondante n'est pas bloquante.
func synchroniseurTiers(abs string) string {
	bas := strings.ToLower(abs)
	for motif, nom := range map[string]string{
		"/library/mobile documents": "iCloud Drive",
		"/library/cloudstorage":     "un service connecté au Finder (iCloud, Drive, Dropbox…)",
		"/dropbox":                  "Dropbox",
		"/google drive":             "Google Drive",
		"/googledrive":              "Google Drive",
		"/onedrive":                 "OneDrive",
	} {
		if strings.Contains(bas+"/", motif+"/") || strings.Contains(bas, motif+"/") {
			return nom
		}
	}
	return ""
}

// compte parcourt le dossier et remplit les volumes.
//
// Prudence : une erreur de parcours ne fait jamais conclure au vide. Elle marque
// l'analyse INCOMPLÈTE, ce qui rend tous les comptes des minorants - et l'annonce
// doit le dire, sans quoi « 12 fichiers partiront » serait un mensonge par
// omission sur un dossier dont la moitié était illisible.
func (e *Engine) compte(a *Analyse, racine string, motifsLocaux []string) {
	// Le dossier n'est pas encore un espace : ses chemins n'ont pas de préfixe,
	// donc `e.ignore` ne peut pas retrouver ses règles tout seul. On les lui
	// ajoute ici, appliquées telles quelles.
	ignore := func(rel string) bool {
		if e.ignore(rel) {
			return true
		}
		for _, motif := range motifsLocaux {
			if correspond(motif, rel) {
				return true
			}
		}
		return false
	}
	filepath.WalkDir(racine, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if abs == racine && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			a.Complete = false
			a.Illisibles++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, errRel := filepath.Rel(racine, abs)
		if errRel != nil {
			a.Complete = false
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if ignore(rel) {
			if d.IsDir() {
				return fs.SkipDir // ne pas compter l'intérieur d'un dossier écarté
			}
			a.Ignores++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Un lien symbolique n'est ni du texte ni un binaire : c'est un chemin,
		// et le suivre sortirait du dossier. La sync ne les transporte pas.
		if d.Type()&fs.ModeSymlink != 0 {
			a.Illisibles++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			a.Complete = false
			a.Illisibles++
			return nil
		}
		if estTexte(abs, info.Size()) {
			a.Fichiers++
			a.Octets += info.Size()
		} else {
			a.HorsPerimetre++
			a.OctetsHorsPerimetre += info.Size()
		}
		return nil
	})
}

// tetePourJuger : on ne lit pas des giga-octets pour dire « binaire ». Le début
// suffit, et c'est ce que fait tout outil qui pose cette question.
const tetePourJuger = 8 << 10

// estTexte juge sur le début du fichier.
//
// Un fichier tronqué en plein milieu d'un caractère multi-octets aurait l'air
// invalide : la rune incomplète de la fin est retirée avant le test, sinon un
// gros fichier français sur trois serait annoncé comme binaire.
// Source: https://pkg.go.dev/unicode/utf8#Valid
func estTexte(abs string, taille int64) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false // illisible : surtout ne pas l'annoncer comme partant
	}
	defer f.Close()
	buf := make([]byte, tetePourJuger)
	// ReadFull plutôt que Read : `Read` peut rendre moins que demandé sans que
	// le fichier soit fini, et un fichier jugé sur ses 512 premiers octets parce
	// que le noyau en a rendu 512 serait une décision prise au hasard.
	// Source: https://pkg.go.dev/io#ReadFull
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false // lecture en échec : ne pas l'annoncer comme partant
	}
	if n == 0 {
		return true // fichier vide : du texte, trivialement
	}
	tete := buf[:n]
	if int64(n) < taille {
		tete = sansRuneTronquee(tete)
	}
	return utf8.Valid(tete)
}

// sansRuneTronquee retire de la fin une séquence UTF-8 incomplète.
func sansRuneTronquee(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && i < len(b); i++ {
		coupe := b[:len(b)-i]
		// Une vraie U+FFFD présente dans le fichier fait 3 octets ; une séquence
		// invalide en fait 1. C'est ce qui distingue « le fichier contient un
		// caractère de remplacement » de « la lecture s'est arrêtée au milieu ».
		if r, taille := utf8.DecodeLastRune(coupe); r != utf8.RuneError || taille > 1 {
			return coupe
		}
	}
	return b
}
