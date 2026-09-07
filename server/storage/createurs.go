package storage

// createurs.go : qui a créé chaque chemin, en UNE passe sur l'historique.
//
// LE COUT EST LA SEULE RAISON POUR LAQUELLE CE FICHIER EXISTE. `Log(p)` sait
// répondre pour UN chemin ; la réponse par fichier coûte 56 s sur un dépôt de
// 1916 commits et 1880 fichiers (extrapolé le 02/09 depuis 596 ms sur 20
// fichiers) là où une passe unique coûte 212 ms. 260x d'écart, et c'est la même
// leçon que `ReadBatch` a payée le 31/08 : le coût n'est pas dans git, il est
// dans la répétition.
//
// LES DEUX FONCTIONS NE MARCHENT PAS SUR LE MEME GRAPHE, et il vaut mieux
// l'écrire que le laisser croire. `Log` suit `--first-parent` : la suite
// linéaire des états serveur, où un fichier arrivé par fusion apparaît AU
// commit de fusion. `Createurs` suit tous les parents : le même fichier
// apparaît alors au commit éphémère qui l'a vraiment ajouté. Les deux rendent
// aujourd'hui le même AUTEUR - `WriteMerge` fabrique la fusion avec
// `commitEnv(author)`, donc le merge et l'éphémère portent le même nom - mais
// c'est une coïncidence de construction, pas une garantie. Ne pas déduire de
// l'une ce que l'autre dirait.

import (
	"strings"
	"sync"
)

// Createur : qui a créé un chemin, et à quel rang dans l'historique.
//
// LE RANG N'EST PAS UNE DATE, et c'est ce qui le rend fiable. Il compte les
// créations dans l'ordre où le pli les rencontre, donc l'ordre topologique de
// l'historique. Comparer des dates de commit ne dirait pas la même chose : une
// écriture arrivée d'un poste dont l'horloge dérive porte une date qui ment,
// alors que sa place dans le graphe, elle, est un fait.
//
// Il sert à une seule question, celle du CREATEUR D'UN DOSSIER : un dossier
// n'existe que parce qu'un fichier y vit, donc son créateur est celui de son
// fichier le plus ancien. Sans un ordre, cette question n'a pas de réponse.
type Createur struct {
	Auteur string
	Rang   int // 0 = la plus ancienne création encore vivante du dépôt
}

// Createurs : chemin -> qui l'a créé, pour tous les chemins vivants de main.
//
// A n'appeler que quand on veut VRAIMENT tout le dépôt. Un écran qui n'a
// affaire qu'à un périmètre appelle `CreateursDe`, qui ne lui rend que ce
// qu'il a demandé.
func (s *Store) Createurs() (map[string]Createur, error) {
	s.creMu.Lock()
	defer s.creMu.Unlock()
	if err := s.rafraichitCreateurs(); err != nil {
		return nil, err
	}
	copie := make(map[string]Createur, len(s.creMap))
	for chemin, c := range s.creMap {
		copie[chemin] = c
	}
	return copie, nil
}

// CreateursDe : les créateurs des chemins demandés, et d'eux seuls.
//
// C'EST LA FORME QUE LES ECRANS UTILISENT, et le périmètre n'y est pas un
// confort. « Qui a créé ceci » est un renseignement SUR UN CHEMIN : le donner
// sur un chemin qu'on n'a pas le droit de voir est la même fuite que d'en
// donner le nom. Un appelant qui ne peut obtenir que ce qu'il a nommé ne peut
// pas fuiter par distraction - là où une carte complète qu'on fait circuler
// attend le premier qui la parcourra au lieu de l'indexer.
//
// Le coût suit le périmètre demandé, pas la taille du dépôt.
func (s *Store) CreateursDe(chemins []string) (map[string]Createur, error) {
	s.creMu.Lock()
	defer s.creMu.Unlock()
	if err := s.rafraichitCreateurs(); err != nil {
		return nil, err
	}
	out := make(map[string]Createur, len(chemins))
	for _, p := range chemins {
		if c, connu := s.creMap[p]; connu {
			out[p] = c
		}
	}
	return out, nil
}

// rafraichitCreateurs : le pli, si et seulement si le head a bougé. Verrou déjà
// tenu.
//
// Le résultat est mémorisé et invalidé par le head. Le head change à chaque
// écriture ; tant qu'il ne bouge pas, la carte est vraie. C'est l'invariant que
// le reste du serveur utilise déjà.
//
// CE QUE CETTE LIGNE COUTE, mesuré le 03/09 sur ce Mac : `Head()` est un fork
// de `git rev-parse`, soit 9 ms - et c'est la TOTALITE du coût d'un appel à
// chaud, la copie de la carte se comptant en microsecondes. Un cache dont la
// clé vit dans git se paie un lancement de process, et il n'y a pas moyen d'y
// couper sans faire porter le head par le Store, ce qui échangerait 9 ms
// contre un affichage périmé le jour où quelqu'un écrit dans le dépôt par une
// autre porte.
//
// Le head est lu SOUS LE VERROU. Lu avant, une écriture glissée entre la
// lecture et le pli ferait enregistrer sous l'ancienne clé une carte calculée
// sur la nouvelle : jamais un résultat faux (la carte n'est jamais plus vieille
// que sa clé), mais un cache qui rate tous ses appels suivants.
func (s *Store) rafraichitCreateurs() error {
	head, err := s.Head()
	if err != nil {
		return err
	}
	if s.creHead == head && s.creMap != nil {
		return nil
	}
	carte, err := s.plieCreateurs()
	if err != nil {
		return err
	}
	s.creHead, s.creMap = head, carte
	return nil
}

// plieCreateurs : l'unique parcours d'historique.
//
// POURQUOI `AD` ET PAS `A` SEUL, et pourquoi un pli qui regarde la présence.
// Trois situations veulent trois réponses, et un pli « le dernier ajout gagne »
// (ou « le premier ») se trompe sur l'une des trois :
//
//   - créé par A puis modifié par B : un seul ajout, le créateur est A ;
//   - supprimé puis recréé : c'est le chemin VIVANT qu'on décrit, pas son
//     homonyme mort, donc le créateur est celui de la dernière création ;
//   - un commit éphémère client parti d'une base périmée « ajoute » un chemin
//     qui existe déjà sur main (voir `ephemeralWrite` : il part du tree du
//     client, où le chemin manque). L'ajout est réel pour git et faux pour le
//     produit : le fichier n'a jamais cessé d'exister.
//
// D'où la règle unique qui couvre les trois : un `A` n'écrit le créateur que si
// le chemin est ABSENT à ce moment du pli, un `D` le rend absent. Aucune date
// n'est comparée.
//
// LES QUATRE DRAPEAUX, ET CE QUE CHACUN FERME :
//
// `-z` : les chemins sortent en octets bruts, terminés par NUL. Sans lui, git
// CITE tout chemin porteur d'un guillemet ou d'un antislash - « notes/réunion
// "kickoff".md » devient « "notes/réunion \"kickoff\".md" », que
// `validatePath` accepte pourtant. La carte porterait alors une clé fantôme
// et rendrait "" pour le vrai chemin, sans erreur. C'est le même choix
// qu'`Empreintes`, `Diff` et `List`, où le commentaire dit déjà que c'est
// « la seule forme sûre ».
//
// `--no-renames` : sans lui, un commit qui supprime a.md et ajoute b.md sort
// en `R`, que `--diff-filter=AD` écarte - donc AUCUN événement, b.md sans
// créateur et a.md vivant à tort. Le renommage v1 est modélisé delete+add
// partout ailleurs ; `Diff` porte le même drapeau pour la même raison.
//
// `--diff-filter=AD` : les seuls événements qui changent la présence.
//
// `--topo-order --reverse` : les ancêtres sortent avant leurs descendants, donc
// le long d'UNE ligne d'historique une suppression précède bien la recréation
// qui la suit - c'est tout ce dont la règle de présence a besoin.
//
// L'ORDRE ENTRE DEUX LIGNES SŒURS N'EST PAS DECIDE, et `Rang` s'en accommode
// sans le cacher. Sur le MEME chemin, deux branches ne peuvent ajouter et
// fusionner proprement qu'avec le même blob : les deux auteurs sont alors
// également créateurs. Sur DEUX chemins différents - ce que `Rang` compare
// pour désigner le créateur d'un dossier - deux postes hors ligne qui créent
// chacun un fichier sont départagés par l'ordre de parcours de git, pas par
// l'horloge du monde. Déterministe pour un historique donné, arbitraire vis-à-
// vis du temps réel. C'est le prix de ne pas se fier aux dates de commit, qui
// mentent dès qu'une horloge de poste dérive ; et l'enjeu se réduit à
// « laquelle de deux créations quasi simultanées nomme le dossier ».
//
// Les commits de fusion ne sortent RIEN : sans `--diff-merges`, `git log`
// n'affiche pas le diff d'un merge. Le chemin apparaît donc une seule fois,
// dans le commit (éphémère ou non) qui l'a vraiment ajouté, avec le bon auteur.
//
// Format : `%x01%an` préfixe chaque en-tête d'un \x01, qu'un chemin ne peut pas
// porter (`hasControlChar` refuse tout octet sous 0x20). C'est ce qui
// distingue un champ d'en-tête d'un champ de statut, même idiome que `Log`
// avec `%x1f`.
// Source: https://git-scm.com/docs/git-log#Documentation/git-log.txt---diff-filterACDMRTUXB82308203
// Source: https://git-scm.com/docs/git-log#Documentation/git-log.txt---topo-order
// Source: https://git-scm.com/docs/git-diff#Documentation/git-diff.txt---no-renames
func (s *Store) plieCreateurs() (map[string]Createur, error) {
	out, err := s.gitRaw("", nil, "log", "--topo-order", "--reverse",
		"--format=%x01%an", "--name-status", "--diff-filter=AD", "--no-renames",
		"-z", "refs/heads/main")
	if err != nil {
		return nil, err
	}
	// Sortie : « \x01<auteur>\0 » puis, par fichier, « <statut>\0<chemin>\0 ».
	// Le statut du PREMIER fichier d'un commit traîne le saut de ligne qui
	// terminait la ligne de format, d'où le TrimPrefix.
	champs := strings.Split(out, "\x00")
	carte := map[string]Createur{}
	auteur := ""
	rang := 0
	for i := 0; i < len(champs); i++ {
		f := champs[i]
		if f == "" {
			continue
		}
		if f[0] == 0x01 {
			auteur = f[1:]
			continue
		}
		statut := strings.TrimPrefix(f, "\n")
		if statut == "" || i+1 >= len(champs) {
			continue
		}
		i++
		chemin := champs[i]
		switch statut[0] {
		case 'D':
			delete(carte, chemin)
		case 'A':
			if _, vivant := carte[chemin]; !vivant {
				carte[chemin] = Createur{Auteur: auteur, Rang: rang}
				rang++
			}
		}
	}
	return carte, nil
}

// creCache : le cache de Createurs, séparé du mutex d'écriture.
//
// Deux verrous distincts et jamais imbriqués : `mu` sérialise les écritures,
// celui-ci protège la carte. Les prendre ensemble ferait attendre un lecteur
// derrière un commit. Le parcours, lui, se fait BIEN sous ce verrou-là : sans
// ça, N requêtes concurrentes sur un head neuf lanceraient N `git log`
// identiques - la file d'attente est moins chère que la répétition.
type creCache struct {
	creMu   sync.Mutex
	creHead string
	creMap  map[string]Createur
}
