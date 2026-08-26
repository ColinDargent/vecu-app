package main

// accueil.go : le premier lancement, sans terminal.
//
// Ce que ce fichier remplace : `vecu setup`, tapé dans un terminal, qui était
// jusqu'ici le seul geste en ligne de commande du produit. Le CLI reste - c'est
// le chemin scriptable, et quelqu'un qui déploie un serveur Docker sait taper
// une commande - mais il cesse d'être le SEUL chemin.
//
// LA RÈGLE QUI COMMANDE TOUT LE FICHIER : rien n'est écrit tant que toutes les
// questions n'ont pas de réponse. Ni configuration, ni dossier créé, ni agent
// installé. Quelqu'un qui ferme le dialogue à la troisième question doit
// retrouver sa machine exactement comme avant - c'est pour ça que les questions
// remplissent une structure et que `appliqueAccueil` est le seul endroit qui
// écrit.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/colindargent/vecu/client/sync"
)

// reglagesAccueil : les réponses, avant qu'aucune n'ait d'effet.
//
// Les mêmes que celles de `cmdSetup`, et c'est un invariant, pas une
// coïncidence : deux postes installés par les deux chemins doivent avoir la
// même configuration de départ.
type reglagesAccueil struct {
	Serveur    string
	Compte     string
	MotDePasse string // jamais journalisé, jamais remis dans une erreur
	Racine     string
	Claude     bool
}

// appliqueAccueil : le seul endroit qui écrit, appelé une seule fois, après la
// dernière question.
//
// L'ordre est celui de `cmdSetup` : se connecter d'abord, créer ensuite. Un
// mot de passe refusé ne doit laisser aucun dossier derrière lui.
func appliqueAccueil(r reglagesAccueil) (sync.Config, error) {
	appareil, err := os.Hostname()
	if err != nil || appareil == "" {
		appareil = "Mac"
	}
	cfg, err := sync.Login(r.Serveur, r.Compte, r.MotDePasse, appareil)
	if err != nil {
		return sync.Config{}, err
	}
	if err := os.MkdirAll(r.Racine, 0o755); err != nil {
		return sync.Config{}, fmt.Errorf("dossier local : %w", err)
	}
	if r.Claude {
		cfg.Projections = []string{"claude"}
	}
	if err := sync.SaveConfig(r.Racine, cfg); err != nil {
		return sync.Config{}, fmt.Errorf("écriture de la configuration : %w", err)
	}
	// La racine mémorisée pour l'app : c'est elle que `resoudRacine` lira au
	// démarrage suivant, et c'est ce qui fait que `premierLancement` rendra faux.
	if err := ecritAppConfig(appConfig{Racine: r.Racine}); err != nil {
		return sync.Config{}, fmt.Errorf("mémorisation de la racine : %w", err)
	}
	return cfg, nil
}

// demandeReglages pose les questions, sans rien écrire.
//
// Rend `errAnnule` dès que la personne ferme un dialogue. L'appelant n'affiche
// alors rien : fermer une fenêtre est une réponse, pas une panne.
func demandeReglages() (reglagesAccueil, error) {
	var r reglagesAccueil
	var err error

	if err = informeAccueil(); err != nil {
		return r, err
	}
	if r.Serveur, err = demandeTexte(
		"Adresse du serveur Vécu.\n\nC'est l'adresse web de votre serveur, par exemple https://vecu.mon-entreprise.fr",
		"https://"); err != nil {
		return r, err
	}
	r.Serveur = strings.TrimSpace(r.Serveur)
	if r.Serveur == "" || r.Serveur == "https://" {
		_ = informe("Aucune adresse saisie. Rien n'a été changé.")
		return r, errAnnule
	}
	if r.Compte, err = demandeTexte("Votre nom d'utilisateur sur ce serveur.", ""); err != nil {
		return r, err
	}
	if r.Compte = strings.TrimSpace(r.Compte); r.Compte == "" {
		_ = informe("Aucun nom d'utilisateur saisi. Rien n'a été changé.")
		return r, errAnnule
	}
	if r.MotDePasse, err = demandeMotDePasse("Mot de passe de « " + r.Compte + " »."); err != nil {
		return r, err
	}
	if r.Racine, err = choisirDossier(
		"Choisir le dossier où Vécu posera les dossiers partagés. Un dossier vide convient très bien."); err != nil {
		return r, err
	}
	// La détection décide du DÉFAUT, pas de la réponse. Un poste peut avoir
	// Claude Code sans qu'on veuille y projeter quoi que ce soit.
	if sync.DetecteClaude() {
		r.Claude, err = confirme(
			"Claude Code est installé sur ce Mac.\n\nRendre les skills partagés utilisables dedans ?\n\nVécu posera des raccourcis vers les skills du dossier partagé. Aucun fichier n'est copié.",
			"Oui")
		if err != nil && !errors.Is(err, errAnnule) {
			return r, err
		}
	}
	return r, nil
}

func informeAccueil() error {
	return informe("Bienvenue dans Vécu.\n\n" +
		"Quatre questions pour connecter ce Mac : l'adresse de votre serveur, votre nom d'utilisateur, " +
		"votre mot de passe, et le dossier où poser les dossiers partagés.\n\n" +
		"Rien ne sera écrit sur ce Mac avant la dernière réponse.")
}

// accueil : le parcours complet. Rend la racine choisie, ou "" si la personne a
// renoncé ou si quelque chose a échoué.
//
// Le mot de passe est la seule question qu'on repose seule : se tromper de mot
// de passe est banal, et refaire les quatre questions pour ça ferait renoncer.
func accueil(logf func(string, ...any)) string {
	r, err := demandeReglages()
	if errors.Is(err, errAnnule) {
		logf("accueil : abandonné avant toute écriture")
		return ""
	}
	if err != nil {
		logf("accueil : %v", err)
		_ = informe("Les dialogues n'ont pas pu s'ouvrir.\n\n" + err.Error())
		return ""
	}

	for essai := 0; ; essai++ {
		cfg, err := appliqueAccueil(r)
		if err == nil {
			logf("accueil : connecté en tant que %s, racine %s", cfg.Username, r.Racine)
			return r.Racine
		}
		// Un identifiant refusé n'est pas une panne : on repose la question.
		// Toute autre erreur (réseau, disque) l'est, et on s'arrête.
		if !estRefusDIdentifiants(err) || essai >= 2 {
			logf("accueil : %v", err)
			_ = informe("La connexion a échoué.\n\n" + err.Error() + "\n\nRien n'a été écrit sur ce Mac.")
			return ""
		}
		r.MotDePasse, err = demandeMotDePasse("Mot de passe refusé.\n\nRéessayer pour « " + r.Compte + " » :")
		if err != nil {
			logf("accueil : abandonné après un refus")
			return ""
		}
	}
}

// estRefusDIdentifiants distingue « ce mot de passe est faux » de « le serveur
// est injoignable ». `sync.Login` rend « login refusé (HTTP 401) » dans le
// premier cas, une erreur réseau dans le second.
//
// Le message n'emporte JAMAIS le mot de passe : `Login` ne le remet pas dans son
// erreur, et c'est ce qui rend sûr de le journaliser.
func estRefusDIdentifiants(err error) bool {
	return err != nil && strings.Contains(err.Error(), "login refusé")
}
