package main

// partage.go : le parcours A du CDC - « je partage un dossier qui existe déjà
// chez moi ».
//
// La promesse tient en une phrase, et c'est elle qui décide de tout le reste :
// le dossier reste où il est. Aucun fichier n'est déplacé, aucun dossier n'est
// créé, et ce que la personne voit dans son Finder après le geste est
// exactement ce qu'elle y voyait avant.
//
// L'ordre des étapes n'est pas négociable : analyser, ANNONCER, puis agir. « On
// annonce avant, on ne découvre pas après » - adopter un dossier envoie son
// contenu à tout le monde, et il n'y a pas de bouton d'annulation.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/colindargent/vecu/client/sync"
)

// partageDossier enchaîne le parcours complet. Tourne dans sa propre goroutine :
// chaque dialogue attend un humain.
func (a *app) partageDossier(logf func(string, ...any)) {
	if !a.dialogueEnCours.CompareAndSwap(false, true) {
		return // un parcours est déjà ouvert, quelque part derrière une fenêtre
	}
	defer a.dialogueEnCours.Store(false)

	chemin, err := choisirDossier("Choisir le dossier à partager. Il restera où il est.")
	if errors.Is(err, errAnnule) {
		return
	}
	if err != nil {
		logf("partage : %v", err)
		_ = informe("Le sélecteur de dossiers n'a pas pu s'ouvrir.\n\n" + err.Error())
		return
	}

	analyse := a.engine.PreAnalyse(chemin)
	if analyse.Refuse() {
		_ = informe(refus(analyse))
		return
	}
	ok, err := confirme(annonce(analyse), "Partager")
	if err != nil {
		logf("partage : %v", err)
		return
	}
	if !ok {
		return
	}

	libelle, err := demandeTexte("Sous quel nom ce dossier apparaîtra-t-il pour les autres ?", filepath.Base(chemin))
	if errors.Is(err, errAnnule) {
		return
	}
	if err != nil {
		logf("partage : %v", err)
		return
	}
	if strings.TrimSpace(libelle) == "" {
		_ = informe("Un nom est nécessaire pour que les autres reconnaissent ce dossier.")
		return
	}

	cree, err := sync.NewHTTPClient(a.serveur, a.token).CreerEspace(libelle)
	if err != nil {
		logf("partage : création de l'espace : %v", err)
		_ = informe("Le dossier n'a pas pu être créé sur le serveur.\n\n" + err.Error())
		return
	}

	// L'espace existe côté serveur AVANT que la table locale ne le sache : son
	// nom technique est décidé là-bas (deux « notes » ne peuvent pas coexister,
	// DT3), donc il faut l'avoir reçu pour l'inscrire ici. Si l'inscription
	// échouait, l'espace resterait sur le serveur, vide : le dire avec son nom,
	// plutôt que laisser quelqu'un le retrouver un jour sans savoir d'où il sort.
	if err := sync.MonteEspace(a.racine, cree.Nom, chemin, true); err != nil {
		logf("partage : montage de %s : %v", cree.Nom, err)
		_ = informe(fmt.Sprintf("Le dossier « %s » a été créé sur le serveur, mais ce poste n'a pas pu l'inscrire.\n\n%s\n\nRien n'a été envoyé.", cree.Nom, err.Error()))
		return
	}
	logf("partage : espace %s monté sur %s (adoption)", cree.Nom, chemin)

	message := fmt.Sprintf("« %s » est partagé.\n\nLe dossier n'a pas bougé. Son contenu part maintenant, et Vécu redémarre pour le prendre en charge.", cree.Libelle)
	if cree.Avertissement != "" {
		message += "\n\nÀ noter : " + cree.Avertissement
	}
	_ = informe(message)
	a.redemarre(logf, "partage du dossier "+cree.Nom)
}

// refus : ce qu'on affiche quand une garde bloquante s'oppose au partage.
func refus(a sync.Analyse) string {
	var b strings.Builder
	b.WriteString("Ce dossier ne peut pas être partagé.\n")
	for _, g := range a.Gardes {
		if g.Bloquant {
			b.WriteString("\n• " + majuscule(g.Motif))
		}
	}
	return b.String()
}

// annonce : ce qui partira, ce qui ne partira pas, et ce qu'on n'a pas pu
// regarder. Fonction PURE, et c'est délibéré : c'est le texte qui engage le
// produit, il doit être testable sans afficher quoi que ce soit.
func annonce(a sync.Analyse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Partager « %s » ?\n\nLe dossier reste où il est. Aucun fichier n'est déplacé.\n", filepath.Base(a.Chemin))
	fmt.Fprintf(&b, "\n• %s (%s) seront envoyés", pluriel(a.Fichiers, "fichier", "fichiers"), octets(a.Octets))
	if a.Ignores > 0 {
		fmt.Fprintf(&b, "\n• %s ignorés par vos règles", pluriel(a.Ignores, "fichier", "fichiers"))
	}
	if a.HorsPerimetre > 0 {
		fmt.Fprintf(&b, "\n• %s non pris en charge (images, PDF, binaires) : ils restent sur ce poste, ils ne seront pas partagés",
			pluriel(a.HorsPerimetre, "fichier", "fichiers"))
	}
	if a.Illisibles > 0 {
		fmt.Fprintf(&b, "\n• %s n'ont pas pu être lus", pluriel(a.Illisibles, "fichier", "fichiers"))
	}
	for _, g := range a.Gardes {
		if !g.Bloquant {
			b.WriteString("\n\nAttention : " + g.Motif)
		}
	}
	// LE MINORANT SE DIT. Un « 12 fichiers partiront » sur un dossier dont la
	// moitié était illisible est un mensonge par omission, et c'est exactement
	// ce que le contrat de F6 interdit.
	if !a.Complete {
		b.WriteString("\n\nCes nombres sont des minimums : le dossier n'a pas pu être parcouru en entier.")
	}
	return b.String()
}

// octets : une taille lisible. Base 1000 et pas 1024, comme le Finder, parce
// que c'est à celui-là que la personne comparera.
func octets(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1f Go", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f Mo", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0f Ko", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d octets", n)
	}
}

func majuscule(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
