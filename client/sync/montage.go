package sync

// montage.go : inscrire une décision de montage dans la configuration locale.
//
// Le montage est le seul réglage vraiment local du produit : le même espace vit
// dans `~/second-brain/shared` chez l'un et dans `~/Documents/Équipe` chez
// l'autre, et le serveur n'en sait rien. Ce fichier est le seul endroit qui
// écrit ces décisions, pour qu'elles restent lisibles au même endroit.
//
// Écrire la config n'est pas en course avec le moteur : hors du démarrage
// (`NewEngine`) et des commandes de mise en service, rien d'autre ne l'écrit.

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// MonteEspace inscrit où un espace vit sur ce poste, et si le dossier qui s'y
// trouve déjà doit être adopté plutôt que refusé.
//
// N'agit QUE sur la configuration : c'est le démarrage suivant du moteur qui
// prend les verrous et monte. Le geste et son effet sont volontairement séparés
// - un moteur en cours de cycle ne doit pas voir sa table de montages changer
// sous lui, et reprendre un verrou demande de relâcher l'ancien.
func MonteEspace(dir, nom, chemin string, adopte bool) error {
	if nom == "" {
		return errors.New("nom d'espace vide")
	}
	if !filepath.IsAbs(chemin) {
		return fmt.Errorf("chemin de montage non absolu : %q", chemin)
	}
	chemin = filepath.Clean(chemin)

	cfg, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	// DÉPLACER un espace déjà monté est un autre geste, avec ses propres
	// questions (que deviennent les fichiers de l'ancien emplacement ?). Le
	// refuser explicitement vaut mieux que de le faire à moitié : la table
	// changerait, les fichiers resteraient, et le prochain cycle les verrait
	// comme des suppressions.
	if actuel, deja := cfg.Montages[nom]; deja && actuel != chemin {
		return fmt.Errorf("l'espace « %s » est déjà monté ici (%s) : le déplacer est un autre geste", nom, actuel)
	}
	if cfg.Montages == nil {
		cfg.Montages = map[string]string{}
	}
	cfg.Montages[nom] = chemin
	// Monter ce qu'on avait refusé est un changement d'avis parfaitement
	// ordinaire. Laisser l'exclusion en place ferait un montage inerte, et la
	// personne chercherait longtemps pourquoi son dossier ne descend pas.
	cfg.Exclus = slices.DeleteFunc(cfg.Exclus, func(e string) bool { return e == nom })
	// Symétrique, et ce n'est pas cosmétique : un `Detaches` résiduel rendrait
	// SILENCIEUSEMENT non destructeur un futur démontage par retrait d'accès,
	// c'est-à-dire qu'il laisserait sur le disque un contenu auquel la personne
	// n'a plus droit. Les deux listes se posent ensemble et se retirent ensemble.
	cfg.Detaches = slices.DeleteFunc(cfg.Detaches, func(e string) bool { return e == nom })
	if adopte && !slices.Contains(cfg.Adoptes, nom) {
		cfg.Adoptes = append(cfg.Adoptes, nom)
	}
	return SaveConfig(dir, cfg)
}

// RefuseEspace inscrit qu'on ne descend pas cet espace sur ce poste.
//
// L'inverse du montage, et le refus doit être DURABLE : sans trace écrite, la
// proposition reviendrait au cycle suivant, puis au démarrage suivant, jusqu'à
// ce que la personne cède ou n'ouvre plus le menu.
func RefuseEspace(dir, nom string) error {
	if nom == "" {
		return errors.New("nom d'espace vide")
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	if !slices.Contains(cfg.Exclus, nom) {
		cfg.Exclus = append(cfg.Exclus, nom)
	}
	return SaveConfig(dir, cfg)
}

// RetireEspace inscrit qu'on retire cet espace de la synchronisation sur ce
// poste, EN GARDANT son contenu local.
//
// À ne pas confondre avec `RefuseEspace`, et la confusion coûterait des
// fichiers. `RefuseEspace` décline une PROPOSITION : l'espace n'a jamais été
// monté ici, il n'y a rien sur le disque, et son exclusion ne peut rien
// supprimer. `RetireEspace` s'applique à un espace MONTÉ, dont le dossier est
// plein - et le démontage ordinaire supprime les fichiers propres.
//
// C'est `Detaches` qui porte la différence, et c'est le démarrage suivant du
// moteur qui l'applique. Le geste et son effet restent séparés, comme pour
// `MonteEspace` : un moteur en cours de cycle ne doit pas voir sa table changer
// sous lui.
func RetireEspace(dir, nom string) error {
	if nom == "" {
		return errors.New("nom d'espace vide")
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	// Les deux ensemble, toujours. `Exclus` empêche qu'il redescende, `Detaches`
	// dit que son contenu reste. L'un sans l'autre est un demi-geste : exclu
	// seul supprime les fichiers, détaché seul les garde puis les redescend.
	if !slices.Contains(cfg.Exclus, nom) {
		cfg.Exclus = append(cfg.Exclus, nom)
	}
	if !slices.Contains(cfg.Detaches, nom) {
		cfg.Detaches = append(cfg.Detaches, nom)
	}
	return SaveConfig(dir, cfg)
}

// Proposition : un espace auquel ce compte a droit, jamais monté sur ce poste,
// et qui attend un geste.
//
// C'est le parcours B du CDC. Ce qu'elle porte est ce qu'il faut pour décider
// et rien de plus : un libellé lisible, si on pourra y écrire, et le volume.
type Proposition struct {
	// Nom : le nom technique, qui sert de clé partout (droits, montages).
	Nom string
	// Libelle : ce que la personne lit. Repli sur le nom technique quand
	// l'espace n'a pas de libellé, comme partout ailleurs (DT3).
	Libelle string
	// Ecriture : un espace entièrement en lecture seule doit être annoncé comme
	// tel, sinon on l'édite et les écritures sont refusées en silence.
	Ecriture bool
	Fichiers int
}

// Propositions : les espaces ouverts à ce compte qui ne sont pas descendus.
//
// Recalculées à chaque cycle. Une liste vide est le cas nominal : tout ce à
// quoi on a droit est déjà là.
func (e *Engine) Propositions() []Proposition {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Proposition(nil), e.propositions...)
}

// DossierPropose : le nom de dossier à créer pour rejoindre un espace.
//
// Le parcours B fait choisir un dossier PARENT, pas la racine de l'espace : la
// personne répond à « où poser ce dossier », et poser le contenu partagé
// directement dans « Documents » y déverserait des dizaines de fichiers.
// L'espace vit donc dans un sous-dossier, et c'est ce nom-là.
//
// Le libellé quand il fait un nom de dossier acceptable, le nom technique
// sinon. Un libellé peut contenir n'importe quoi - il vient du serveur, donc de
// quelqu'un d'autre : une barre oblique en ferait deux dossiers, un suffixe
// « .app » en ferait un paquet aux yeux du Finder, un nom commençant par un
// point le rendrait invisible.
func DossierPropose(p Proposition) string {
	nom := strings.TrimSpace(p.Libelle)
	if nom == "" || len(nom) > 64 || strings.ContainsAny(nom, `/\:`) || strings.Contains(nom, "\x00") {
		return p.Nom
	}
	if strings.HasPrefix(nom, ".") || strings.HasPrefix(nom, "-") || strings.HasSuffix(nom, ".") {
		return p.Nom
	}
	bas := strings.ToLower(nom)
	for _, suffixe := range []string{".app", ".bundle", ".framework", ".kext", ".plugin", ".rtfd", ".lproj", ".localized", ".pkg", ".dsym"} {
		if strings.HasSuffix(bas, suffixe) {
			return p.Nom
		}
	}
	return nom
}
