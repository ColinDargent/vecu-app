package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/colindargent/vecu/client/sync"
)

// libelles : les textes affichés dans le menu, dérivés de l'état du moteur.
// Fonction pure (l'heure est injectée) pour être testable sans systray ni
// horloge réelle. La coquille systray se contente de poser ces chaînes.
type libelles struct {
	statut            string // ligne d'en-tête : synchronisé et depuis quand
	fichiers          string // nombre de fichiers suivis + nombre d'espaces
	espacesInfobul    string // détail par espace (infobulle de la ligne fichiers)
	laisses           string // avertissement fichiers seulement-ici
	laissesVisible    bool   // n'afficher l'avertissement que s'il y a lieu
	skills            string // skills projetés, et vers quels outils
	skillsVisible     bool   // rien à dire sur un poste sans skill monté
	nouveaux          string // skills qu'un collègue vient de déposer (F4)
	nouveauxInfo      string // le détail : un slug et son auteur par ligne
	nouveauxVisible   bool   // caché tant que personne n'a rien déposé
	copies            string // copies de conflit locales en attente d'arbitrage
	copiesInfo        string // le détail : un chemin par ligne
	copiesVisible     bool   // caché tant qu'il n'y en a aucune
	collisions        string // chemins que ce disque ne sait pas distinguer (casse)
	collisionsInfo    string // le détail : les chemins d'un groupe, et leur empreinte
	collisionsVisible bool   // caché tant qu'il n'y en a aucune
}

// libellePropositions : ce que la personne lit dans le menu pour chaque dossier
// qu'on lui a ouvert. Le libellé, le volume, et la lecture seule quand c'est le
// cas - un espace où l'on ne peut pas écrire doit être annoncé comme tel, sinon
// on l'édite et les écritures sont refusées en silence.
func libellePropositions(props []sync.Proposition) []string {
	out := make([]string, 0, len(props))
	for _, p := range props {
		libelle := fmt.Sprintf("Rejoindre « %s » (%s)", p.Libelle, pluriel(p.Fichiers, "fichier", "fichiers"))
		if !p.Ecriture {
			libelle += " · lecture seule"
		}
		out = append(out, libelle)
	}
	return out
}

// menuLabels calcule les libellés du menu à partir de l'état et des laisses.
// `now` est injecté pour rendre le formatage de « dernier cycle » déterministe.
func menuLabels(st sync.State, laisses []sync.Laisse, nouveaux []sync.NouveauSkill, copies []string, now time.Time) libelles {
	l := libelles{}

	if st.Head == "" {
		l.statut = "Jamais synchronisé"
	} else {
		l.statut = "Synchronisé · " + depuis(st.Cycle, now)
	}

	l.fichiers = fmt.Sprintf("%d fichiers · %s", len(st.Files), pluriel(len(st.Espaces), "espace", "espaces"))

	// Détail par espace en infobulle : un compte global ne dit pas si un dossier
	// attendu est vide. Même calcul que `vecu status`.
	var lignes []string
	for _, espace := range st.Espaces {
		n := 0
		for p := range st.Files {
			if strings.HasPrefix(p, espace+"/") {
				n++
			}
		}
		lignes = append(lignes, fmt.Sprintf("%s : %d fichiers", espace, n))
	}
	sort.Strings(lignes)
	l.espacesInfobul = strings.Join(lignes, "\n")

	// Seul le genre « fichier » (contenu qui n'existe QUE sur ce poste) est un
	// vrai risque de perte : c'est le seul qu'on remonte en avertissement.
	perdus := 0
	for _, la := range laisses {
		if la.Perdu() {
			perdus++
		}
	}
	if perdus > 0 {
		l.laisses = fmt.Sprintf("⚠ %s seulement sur ce poste", pluriel(perdus, "fichier", "fichiers"))
		l.laissesVisible = true
	}

	// Skills : combien sont utilisables, et par quels outils. Un skill qui
	// descend sans être projeté est invisible pour l'agent, donc inutile - et
	// c'est exactement ce qui s'est produit chez Achille en juillet, sans que
	// rien ne le dise.
	if montes := sync.SkillsMontes(st); len(montes) > 0 {
		vers := make([]string, 0, 2)
		if len(st.Projetes) > 0 {
			vers = append(vers, "Claude Code")
		}
		if len(st.ProjetesAgents) > 0 {
			vers = append(vers, "Cursor et Codex")
		}
		l.skillsVisible = true
		if len(vers) == 0 {
			l.skills = fmt.Sprintf("%s, aucun projeté", pluriel(len(montes), "skill", "skills"))
		} else {
			projetes := len(st.Projetes)
			if n := len(st.ProjetesAgents); n > projetes {
				projetes = n
			}
			l.skills = fmt.Sprintf("%d/%d skills → %s", projetes, len(montes), strings.Join(vers, ", "))
		}
	}

	// F4 : un skill déposé par quelqu'un d'autre. Le CDC l'exige ici et pas
	// ailleurs - « le signal se lit dans l'app, pas dans une commande que
	// personne ne lance ». Le détail va en infobulle : le menu porte un compte,
	// pas une liste, la leçon de F6 le matin même.
	if n := len(nouveaux); n > 0 {
		l.nouveauxVisible = true
		l.nouveaux = fmt.Sprintf("%s déposé%s - cliquer pour marquer comme vu",
			pluriel(n, "nouveau skill", "nouveaux skills"), pluriel2(n))
		lignes := make([]string, 0, n)
		for _, s := range nouveaux {
			lignes = append(lignes, fmt.Sprintf("%s - par %s", s.Slug, s.Auteur))
		}
		sort.Strings(lignes)
		l.nouveauxInfo = strings.Join(lignes, "\n")
	}

	// Les copies de conflit locales. Elles n'existent que sur ce poste depuis
	// qu'elles ne se synchronisent plus, donc AUCUNE autre surface ne peut les
	// montrer : sans cette ligne, on ne les découvre qu'en tombant dessus dans
	// un dossier. Un compte dans le menu, la liste en infobulle - le menu porte
	// un compte et pas une liste, la leçon de F6.
	//
	// Jamais un avertissement : rien n'est perdu, c'est l'inverse. Un arbitrage
	// attend, et un rapport qui crie au loup n'est plus lu.
	if n := len(copies); n > 0 {
		l.copiesVisible = true
		l.copies = fmt.Sprintf("%s à arbitrer", pluriel(n, "copie de conflit", "copies de conflit"))
		l.copiesInfo = strings.Join(copies, "\n")
	}

	// Les collisions de casse déjà dans l'état. Vécu n'en crée plus depuis le
	// 23/08, il n'efface pas celles qui existent : elles portent du contenu, et
	// trancher lequel des deux noms garder est un geste humain.
	//
	// La barre de menus et pas la ligne de commande : Colin ne tape jamais
	// `vecu`, et le CLI installé date du 24/07 puisque aucune release ne le
	// remplace. Une surface utilisateur qui ne vit que dans le CLI ne serait lue
	// par personne. Arbitrage du 23/08.
	//
	// Dérivé de `st.Files`, déjà en paramètre : aucun argument de plus à cette
	// fonction, donc aucun de ses dix-huit sites d'appel à toucher.
	//
	// Jamais un avertissement, pour la même raison que les copies : rien n'est
	// perdu, tout est sur le serveur. Un arbitrage attend.
	if groupes := sync.FantomesDeCasse(st.Files); len(groupes) > 0 {
		l.collisionsVisible = true
		l.collisions = fmt.Sprintf("%s à arbitrer", pluriel(len(groupes), "collision de casse", "collisions de casse"))
		var lignes []string
		for _, groupe := range groupes {
			for _, chemin := range groupe {
				lignes = append(lignes, fmt.Sprintf("%s (état : %s)", chemin, sync.Court(st.Files[chemin])))
			}
		}
		l.collisionsInfo = strings.Join(lignes, "\n")
	}

	return l
}

// pluriel2 : la marque du pluriel seule, pour un participe accordé.
func pluriel2(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}

func pluriel(n int, singulier, pluriel string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singulier)
	}
	return fmt.Sprintf("%d %s", n, pluriel)
}

// depuis rend un horodatage RFC 3339 en durée lisible, relative à `now`.
// Repris de `client` (cmdStatus) : 15 lignes de formatage pur, dupliquées
// plutôt qu'extraites dans une lib partagée pour un seul appelant de plus.
func depuis(iso string, now time.Time) string {
	if iso == "" {
		return "jamais"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	d := now.Sub(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return "il y a moins d'une minute"
	case d < time.Hour:
		return fmt.Sprintf("il y a %d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("il y a %d heures", int(d.Hours()))
	}
	return "le " + t.Local().Format("02/01/2006 à 15h04")
}
