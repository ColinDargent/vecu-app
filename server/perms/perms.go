// Package perms : résolution du droit effectif d'un utilisateur sur un chemin.
//
// Modèle (design validé) : une règle est un couple (chemin, niveau). Le droit
// effectif d'un chemin cible est le niveau de la règle posée sur l'ancêtre le
// plus profond qui le couvre. À défaut de règle couvrante, le niveau par défaut
// de l'utilisateur (à la racine) s'applique. Une règle peut restreindre comme
// étendre, à n'importe quelle profondeur : l'écueil Drive (droits descendants
// uniquement) est évité par construction.
package perms

import (
	"path"
	"strings"
)

// Level : niveau d'accès, ordonné du plus faible au plus fort.
type Level int

const (
	Invisible Level = iota // le chemin n'existe pas pour l'utilisateur
	Lecture                // lecture seule
	Ecriture               // lecture + écriture
)

// Rule : un niveau posé sur un chemin (dossier ou fichier).
// Le chemin "" désigne la racine (porte le niveau par défaut de l'utilisateur).
type Rule struct {
	Path  string
	Level Level
}

// Effective renvoie le niveau effectif de `target` étant donné les règles de
// l'utilisateur et son niveau par défaut à la racine.
//
// La règle gagnante est celle dont le chemin est un ancêtre de `target` (ou
// target lui-même) et qui est le plus profond. `defaultLevel` agit comme la
// règle implicite à la racine ; une Rule{Path: ""} explicite la remplace.
func Effective(target string, defaultLevel Level, rules []Rule) Level {
	target = Canon(target)
	best := defaultLevel
	bestDepth := -1 // la racine implicite est moins profonde que toute règle "" explicite (depth 0)

	for _, r := range rules {
		rp := Canon(r.Path)
		if !covers(rp, target) {
			continue
		}
		d := depth(rp)
		switch {
		case d > bestDepth:
			// Ancêtre strictement plus profond : il gagne.
			best = r.Level
			bestDepth = d
		case d == bestDepth && r.Level < best:
			// Égalité de profondeur : ne devrait survenir que sur des chemins
			// canoniques identiques (donc dédupliqués en base). Départage
			// défensif fail-closed : le niveau le plus restrictif l'emporte.
			best = r.Level
		}
	}
	return best
}

// Canon normalise un chemin en forme canonique relative : nettoie les segments
// (., ..), retire le slash initial. La racine devient "". C'est la seule forme
// que covers/depth manipulent, et celle qui doit être stockée en base pour que
// la clé (user_id, path) déduplique les chemins logiquement identiques.
func Canon(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	c := path.Clean(p)
	if c == "." {
		return ""
	}
	return strings.TrimPrefix(c, "/")
}

// covers indique si `rule` (un chemin de règle) couvre `target` : soit le même
// chemin, soit un ancêtre strict au découpage par segment (pas un préfixe de
// chaîne : "a/b" ne couvre pas "a/bc").
func covers(rule, target string) bool {
	if rule == "" || rule == "." {
		return true // la racine couvre tout
	}
	if rule == target {
		return true
	}
	return strings.HasPrefix(target, rule+"/")
}

// depth : nombre de segments du chemin. La racine ("") vaut 0.
func depth(p string) int {
	if p == "" || p == "." {
		return 0
	}
	return strings.Count(p, "/") + 1
}

// CanRead : l'utilisateur voit-il ce chemin (lecture ou plus) ?
func CanRead(target string, defaultLevel Level, rules []Rule) bool {
	return Effective(target, defaultLevel, rules) >= Lecture
}

// CanWrite : l'utilisateur peut-il écrire ce chemin ?
func CanWrite(target string, defaultLevel Level, rules []Rule) bool {
	return Effective(target, defaultLevel, rules) >= Ecriture
}

// ParseLevel convertit un libellé stocké en base vers un Level.
func ParseLevel(s string) (Level, bool) {
	switch s {
	case "invisible":
		return Invisible, true
	case "lecture":
		return Lecture, true
	case "ecriture":
		return Ecriture, true
	}
	return Invisible, false
}

// String rend le libellé stocké en base.
func (l Level) String() string {
	switch l {
	case Invisible:
		return "invisible"
	case Lecture:
		return "lecture"
	case Ecriture:
		return "ecriture"
	}
	return "invisible"
}
