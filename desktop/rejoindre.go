package main

// rejoindre.go : le parcours B du CDC - « je rejoins un dossier qu'on partage
// avec moi ».
//
// Ce que le parcours garantit, et c'est sa raison d'être : rien n'apparaît sur
// le disque sans qu'on l'ait demandé. Le moteur propose, la personne choisit où
// poser le dossier, et c'est seulement là qu'il descend.
//
// La différence avec le parcours A n'est pas cosmétique : là-bas, le dossier
// choisi DEVIENT l'espace ; ici, il en est le PARENT. Répondre « où poser ce
// dossier » par « dans Documents » ne doit pas déverser le contenu partagé dans
// Documents, mais créer « Documents/Équipe ».

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/colindargent/vecu/client/sync"
)

// rejoindreDossier ouvre le parcours pour une proposition. Tourne dans sa
// propre goroutine : chaque dialogue attend un humain.
func (a *app) rejoindreDossier(p sync.Proposition, logf func(string, ...any)) {
	if !a.dialogueEnCours.CompareAndSwap(false, true) {
		return // un parcours est déjà ouvert
	}
	defer a.dialogueEnCours.Store(false)

	acces := "vous pouvez y écrire"
	if !p.Ecriture {
		acces = "en lecture seule"
	}
	bouton, err := choix(fmt.Sprintf("« %s » a été partagé avec vous.\n\n%s, %s.\n\nOù voulez-vous le poser ? Vécu créera un dossier « %s » à l'endroit que vous choisirez.",
		p.Libelle, pluriel(p.Fichiers, "fichier", "fichiers"), acces, sync.DossierPropose(p)),
		[]string{"Plus tard", "Ne plus proposer", "Choisir un dossier…"})
	if err != nil {
		logf("rejoindre %s : %v", p.Nom, err)
		return
	}
	switch bouton {
	case "Ne plus proposer":
		// Le refus est DURABLE. Sans trace écrite, la proposition reviendrait au
		// cycle suivant, puis au démarrage suivant, jusqu'à ce que la personne
		// cède ou n'ouvre plus le menu.
		if err := sync.RefuseEspace(a.racine, p.Nom); err != nil {
			logf("rejoindre %s : refus : %v", p.Nom, err)
			_ = informe("Le refus n'a pas pu être enregistré.\n\n" + err.Error())
			return
		}
		logf("proposition refusée : %s", p.Nom)
		_ = informe(fmt.Sprintf("« %s » ne vous sera plus proposé.\n\nVous y avez toujours accès : il réapparaîtra si quelqu'un vous le repartage.", p.Libelle))
		return
	case "Choisir un dossier…":
	default:
		return // « Plus tard », ou dialogue fermé
	}

	parent, err := choisirDossier(fmt.Sprintf("Choisir où poser « %s »", p.Libelle))
	if errors.Is(err, errAnnule) {
		return
	}
	if err != nil {
		logf("rejoindre %s : %v", p.Nom, err)
		_ = informe("Le sélecteur de dossiers n'a pas pu s'ouvrir.\n\n" + err.Error())
		return
	}
	cible := filepath.Join(parent, sync.DossierPropose(p))

	// Le dossier de destination existe déjà et n'est pas vide : son contenu
	// rejoindrait l'espace partagé, donc partirait chez tout le monde. C'est
	// peut-être exactement ce qu'on veut (deux personnes qui rangent le même
	// dossier), mais ça ne se devine pas - et c'est irréversible.
	adopte := false
	if peuple, err := dossierPeuple(cible); err != nil {
		logf("rejoindre %s : %v", p.Nom, err)
		_ = informe("Ce dossier n'a pas pu être examiné.\n\n" + err.Error())
		return
	} else if peuple {
		analyse := a.engine.PreAnalyse(cible)
		if analyse.Refuse() {
			_ = informe(refus(analyse))
			return
		}
		ok, err := confirme(fmt.Sprintf("« %s » existe déjà et contient des fichiers.\n\n%s\n\nSes fichiers rejoindront l'espace partagé et seront envoyés à tous ceux qui y ont accès.",
			cible, annonce(analyse)), "Fusionner et rejoindre")
		if err != nil || !ok {
			return
		}
		adopte = true
	}

	if err := sync.MonteEspace(a.racine, p.Nom, cible, adopte); err != nil {
		logf("rejoindre %s : montage : %v", p.Nom, err)
		_ = informe("Ce dossier n'a pas pu être inscrit.\n\n" + err.Error())
		return
	}
	logf("proposition acceptée : %s monté sur %s (adoption : %v)", p.Nom, cible, adopte)
	_ = informe(fmt.Sprintf("« %s » va apparaître dans :\n%s\n\nVécu redémarre pour le mettre en place.", p.Libelle, cible))
	a.redemarre(logf, "montage de "+p.Nom)
}

// dossierPeuple : ce dossier existe-t-il avec quelque chose dedans ?
//
// Même prudence que `contientDesFichiers` côté moteur, et pour la même raison :
// une erreur de lecture répond « oui, il y a du contenu », jamais « non ». « Je
// n'ai pas pu regarder » n'est pas « il n'y a rien », et ce qui est en jeu ici
// est l'envoi de fichiers à tout le monde.
func dossierPeuple(chemin string) (bool, error) {
	entrees, err := os.ReadDir(chemin)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // il n'existe pas : Vécu le créera
	}
	if err != nil {
		return true, err
	}
	for _, e := range entrees {
		if e.Name() != ".DS_Store" {
			return true, nil
		}
	}
	return false, nil
}
