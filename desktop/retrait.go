package main

// retrait.go : « je retire ce dossier de la synchronisation, et je le garde ».
//
// La promesse tient en une phrase, comme celle du partage, et c'est elle qui
// décide de tout le reste : AUCUN fichier n'est supprimé. Ce que la personne
// voit dans son Finder après le geste est exactement ce qu'elle y voyait avant.
// Seul le lien avec le serveur est coupé.
//
// La confusion à ne jamais faire, et elle coûterait des fichiers : le démontage
// ordinaire, celui qui suit un accès retiré côté serveur, SUPPRIME les fichiers
// propres — et c'est voulu, ce contenu ne nous appartient plus. Ici c'est la
// personne qui retire, pas le serveur, et rien ne part. `cfg.Detaches` porte
// cette différence, et `RetireEspace` est le seul chemin qui l'écrit.

import (
	"errors"
	"fmt"

	"github.com/colindargent/vecu/client/sync"
)

// retireDossier enchaîne le parcours complet. Tourne dans sa propre goroutine :
// chaque dialogue attend un humain.
func (a *app) retireDossier(logf func(string, ...any)) {
	if !a.dialogueEnCours.CompareAndSwap(false, true) {
		return // un parcours est déjà ouvert, quelque part derrière une fenêtre
	}
	defer a.dialogueEnCours.Store(false)

	montes := a.engine.Espaces()
	if len(montes) == 0 {
		_ = informe("Aucun dossier n'est synchronisé sur ce poste.")
		return
	}

	nom, err := choisirDansListe("Quel dossier retirer de la synchronisation ?", montes)
	if errors.Is(err, errAnnule) {
		return
	}
	if err != nil {
		logf("retrait : %v", err)
		_ = informe("La liste des dossiers n'a pas pu s'ouvrir.\n\n" + err.Error())
		return
	}

	// ANNONCER avant d'agir, comme au partage. Ici l'annonce porte surtout sur
	// ce qui NE se passe PAS : quelqu'un qui clique « Retirer » s'attend
	// spontanément à perdre le dossier, et le dire lève exactement la crainte
	// qui ferait renoncer au geste.
	message := fmt.Sprintf(
		"Retirer « %s » de la synchronisation sur ce poste ?\n\n"+
			"Le dossier et tous ses fichiers RESTENT sur ce Mac, à leur place, tels quels.\n"+
			"Ce qui s'arrête : les modifications ne partiront plus vers le serveur, et celles des autres n'arriveront plus ici.\n\n"+
			"Rien n'est retiré aux autres membres : leur accès et leur copie ne changent pas.",
		nom)
	ok, err := confirme(message, "Retirer")
	if err != nil {
		logf("retrait : %v", err)
		return
	}
	if !ok {
		return
	}

	// Écrire la décision, puis redémarrer : le geste et son effet restent
	// séparés, comme pour le montage. Un moteur en cours de cycle ne doit pas
	// voir sa configuration changer sous lui, et c'est le démarrage suivant qui
	// applique le détachement.
	if err := sync.RetireEspace(a.racine, nom); err != nil {
		logf("retrait : %v", err)
		_ = informe(fmt.Sprintf("« %s » n'a pas pu être retiré.\n\n%s\n\nRien n'a changé.", nom, err.Error()))
		return
	}
	logf("retrait : espace %s détaché (contenu local conservé)", nom)

	_ = informe(fmt.Sprintf(
		"« %s » est retiré de la synchronisation.\n\n"+
			"Le dossier n'a pas bougé et son contenu est intact. Vécu redémarre pour en tenir compte.\n\n"+
			"Pour le remettre plus tard : « Rejoindre » réapparaîtra dans le menu.",
		nom))
	a.redemarre(logf, "retrait du dossier "+nom)
}
