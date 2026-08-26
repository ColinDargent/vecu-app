// Package sync : moteur de synchronisation du client vecu.
// Un cycle = push (scan local vs état connu) + pull (delta serveur) +
// réconciliation du périmètre. Les events fsnotify et le poll ne sont que des
// déclencheurs : chaque cycle refait un scan complet (la sync ne repose jamais
// sur les events seuls).
package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// vecuDir : sous-dossier d'état, à la racine du dossier synchronisé.
const vecuDir = ".vecu"

// Config : lien vers le serveur, écrit par `vecu login`.
type Config struct {
	Server   string `json:"server"` // URL de base (ex: https://vecu.example.com)
	Username string `json:"username"`
	Token    string `json:"token"` // jeton d'appareil (bearer)
	// Exclus : espaces que CE poste ne descend pas, malgré le droit d'y accéder.
	// C'est le seul réglage local du montage : tout le reste se décide dans
	// l'interface, et arrive ici sans qu'aucune commande soit tapée.
	Exclus []string `json:"exclus,omitempty"`
	// Detaches : espaces exclus par un geste de RETRAIT, dont les fichiers
	// doivent rester sur le disque.
	//
	// POURQUOI CE CHAMP EXISTE, et c'est le cœur du sujet. `Exclus` seul ne
	// distingue pas deux gestes que tout oppose :
	//
	//   - un accès RETIRÉ par le serveur : les fichiers partent du disque, et
	//     c'est voulu - ce contenu ne nous appartient plus ;
	//   - « retirer ce dossier de la synchronisation » : les fichiers RESTENT,
	//     et c'est la promesse écrite du CDC (F1, « sans perdre son contenu
	//     local »).
	//
	// `demonte` supprime les fichiers propres. Sans ce champ, le second geste
	// emprunterait le chemin du premier et effacerait le travail de la personne
	// au cycle suivant son propre clic.
	//
	// TOUJOURS accompagné d'une entrée dans `Exclus` : détaché sans exclu ferait
	// un espace qui redescend au cycle suivant. `RetireEspace` écrit les deux,
	// `MonteEspace` les retire tous les deux.
	Detaches []string `json:"detaches,omitempty"`
	// Montages : espace -> chemin absolu sur CE poste. La table qui remplace
	// « racine + nom d'espace » (DT2).
	//
	// Réglage local par nature : le même espace vit dans `~/second-brain/shared`
	// chez l'un et dans `~/Documents/Clients 2026` chez l'autre. Le serveur ne
	// sait rien de ces chemins et ne doit rien en savoir.
	//
	// **Un espace absent de la table retombe sur `racine + nom`**, c'est-à-dire
	// sur le modèle d'avant. C'est ce qui rend cette étape transparente : une
	// installation existante ne porte aucune entrée et ne change pas de
	// comportement. Le jour où la table devient l'autorité - « un espace sans
	// entrée n'est pas monté », DT2 - sera un changement séparé, et il aura sa
	// migration.
	Montages map[string]string `json:"montages,omitempty"`
	// Adoptes : espaces dont le dossier local préexistant doit être envoyé au
	// serveur plutôt que refusé au montage (voir `aligneMontage`).
	//
	// C'est l'équivalent PERSISTANT de `Engine.Importer`, qui ne vit qu'en
	// mémoire. Le parcours A - partager un dossier déjà rempli - écrit son
	// intention ici, puis le service redémarre pour reprendre les verrous : sans
	// persistance, l'intention mourrait entre les deux et le dossier arriverait
	// devant la garde « un dossier de ce nom existe déjà », c'est-à-dire devant
	// un refus, à la fin d'un geste que la personne vient de confirmer.
	//
	// Le drapeau reste après l'adoption, et c'est sans effet : la garde ne
	// s'applique qu'aux espaces jamais montés ici. S'il redevenait actif, c'est
	// que l'état local a été perdu - et réadopter le dossier que la personne a
	// choisi de partager est alors le bon comportement.
	Adoptes []string `json:"adoptes,omitempty"`
	// Projections : outils d'IA installés sur ce poste vers lesquels les skills
	// sont projetés (ex: ["claude"]). Vide = aucune projection. Réglage local,
	// posé par `vecu setup` selon l'agent détecté.
	Projections []string `json:"projections,omitempty"`
	// ProjectionsVues : outils que la détection a DÉJÀ proposés sur ce poste.
	//
	// Sans ce champ, `Projections` répond à deux questions qu'il faut séparer :
	// « vers quoi je projette » et « qu'est-ce qu'on m'a déjà proposé ». Une
	// liste vide était indiscernable entre « poste installé avant la feature »
	// et « l'utilisateur a dit non », donc la détection réactivait un outil
	// explicitement retiré, à chaque démarrage.
	//
	// La détection COMPLÈTE désormais la liste au lieu de la remplacer : un
	// outil jamais vu s'ajoute, un outil retiré à la main ne revient pas.
	ProjectionsVues []string `json:"projections_vues,omitempty"`
}

// State : état de synchronisation local, réécrit à chaque cycle.
type State struct {
	Head  string            `json:"head"`  // dernier head serveur connu (= base_oid des écritures)
	Files map[string]string `json:"files"` // chemin -> sha256 hex du contenu synchronisé
	// Versions : chemin -> commit auquel le contenu synchronisé de CE chemin
	// était courant. Le marque-page déclaré au serveur comme ancêtre commun
	// (`base_oid`) quand on pousse ce chemin.
	//
	// `Head` portait deux rôles qui divergent dès qu'une écriture est refusée :
	// curseur de lecture du delta (il doit AVANCER, sinon le même delta
	// redescend indéfiniment, pour tous les espaces, à cause d'un seul chemin)
	// et ancêtre commun de la fusion à trois voies côté serveur (il ne doit
	// décrire que ce que le disque a vraiment REÇU). Un lien ou un dossier en
	// travers d'un chemin fait diverger les deux : le curseur passe au-dessus de
	// la version non écrite, et le PUT suivant déclare un ancêtre que ce poste ne
	// contient pas. Le serveur lit la différence comme un retrait volontaire,
	// fusionne proprement, et la modification de l'autre membre disparaît - sans
	// conflit, sans copie, sans une ligne de journal.
	//
	// INVARIANT : une entrée n'est JAMAIS inférée. Elle est écrite au moment où
	// le contenu arrive sur le disque, avec le commit auquel il correspond, aux
	// mêmes quatre endroits que `Files`. Un chemin sans entrée pousse avec la
	// chaîne vide, qui n'est pas un sentinel : côté serveur elle produit un
	// commit orphelin, donc une fusion sans base commune, donc une copie de
	// conflit. C'est le repli sûr - du bruit, jamais une perte.
	//
	// `omitempty` : un état écrit par une version antérieure se charge sans
	// erreur (map nil), et un état écrit par cette version se charge sans erreur
	// sur la précédente (champ inconnu, ignoré par encoding/json).
	// Source: https://pkg.go.dev/encoding/json#Marshal
	Versions map[string]string `json:"versions,omitempty"`
	// Connus : espaces déjà montés au moins une fois sur ce poste. Sert à
	// distinguer « dossier personnel qui portait déjà ce nom » (à ne jamais
	// adopter) de « dossier que Vécu a lui-même créé ici » (résidus d'un
	// démontage : fichiers modifiés, binaires refusés, fichiers ignorés).
	Connus []string `json:"connus,omitempty"`
	// Espaces : espaces montés au dernier cycle, triés. Sert à détecter un
	// changement de montage (accès accordé ou retiré depuis l'interface).
	Espaces []string `json:"espaces"`
	// Cycle : horodatage RFC 3339 du dernier cycle terminé.
	Cycle string `json:"cycle,omitempty"`
	// Laisses : fichiers présents en local mais NON synchronisés au dernier
	// cycle, avec le motif. Le silence est le vrai danger d'un outil de sync :
	// tant que ces fichiers ne sont nulle part, il faut pouvoir le lire.
	Laisses []Laisse `json:"laisses,omitempty"`
	// Projetes : slugs de skills dont ce poste gère le symlink de projection vers
	// Claude (`.claude/skills/<slug>`). Sert à retirer proprement le lien d'un
	// skill disparu, sans jamais toucher un lien que Vécu n'a pas créé.
	Projetes []string `json:"projetes,omitempty"`
	// ProjetesAgents : la même chose pour `.agents/skills/<slug>`, la cible
	// commune à Cursor et à Codex.
	//
	// Un champ NOUVEAU, et non `Projetes` transformé en map par cible. Le type
	// d'un champ existant ne peut pas changer : `LoadState` propage l'erreur de
	// désérialisation et `NewEngine` la propage à son tour, donc un tableau lu
	// comme un objet empêcherait tout poste revenu à une version antérieure de
	// démarrer. Même motif que `Versions` en août : on ajoute, on ne mute pas.
	//
	// Ce que ça ne règle PAS, et qu'il vaut mieux savoir : une version
	// antérieure charge l'état sans erreur, mais `encoding/json` ne conserve pas
	// les champs qu'il ne connaît pas, et `Save` réécrit la structure entière.
	// Un seul cycle sur une version antérieure efface donc `projetes_agents` du
	// disque. Au retour, le registre est vide et les liens déjà posés dans
	// `.agents/skills/` ne seront plus jamais retirés par Vécu - ils restent,
	// cassés, jusqu'à ce que quelqu'un les enlève. Bruit, pas perte. La même
	// exposition vaut pour `Projetes` et `Versions` ; la fermer demanderait de
	// charger l'état en map et de re-sérialiser les champs inconnus, ce qui
	// coûte plus que le symptôme.
	// Source: https://pkg.go.dev/encoding/json#Unmarshal
	ProjetesAgents []string `json:"projetes_agents,omitempty"`
	// Reportes : chemins qu'un cycle a laissés intacts parce qu'une écriture
	// locale y est arrivée pendant ce cycle. Ce sont les chemins que le rattrapage
	// doit aller CHERCHER, parce que plus rien d'autre ne les reproposera.
	//
	// Sans ce registre, le report ouvre une perte. Le head avance au-dessus du
	// changement reporté, `Sync(head)` ne le re-listera donc jamais, et
	// `rattrape` saute le chemin comme « connu et présent » puisqu'il l'est. La
	// seule route de retour est le push différé du cycle suivant - et ce push
	// peut ne jamais partir : espace passé en lecture seule pour ce compte,
	// contenu devenu binaire (les deux branches de refus de `pushLocal` qui ne
	// touchent pas `Files`), ou simple no-op si l'édition de course est revenue
	// au contenu déjà connu. Dans tous ces cas la version de l'AUTRE membre
	// n'arrive jamais sur ce poste, et rien ne le dit. Mesuré en revue
	// adversariale contre `main`, qui lui la fait arriver.
	//
	// Un champ NOUVEAU, comme `Versions` en août et `ProjetesAgents` le 19/08 :
	// on ajoute, on ne mute jamais le type d'un champ existant, sinon un poste
	// revenu en arrière ne démarre plus. Même exposition connue en contrepartie -
	// un cycle sur une version antérieure efface `reportes` du disque, et les
	// chemins qui y étaient restent périmés jusqu'à leur prochaine édition
	// locale. Bruit, jamais perte.
	// Source: https://pkg.go.dev/encoding/json#Unmarshal
	Reportes []string `json:"reportes,omitempty"`
	// HorsPerimetre : chemin -> empreinte du contenu qui a été refusé parce
	// qu'il est hors du périmètre (binaire). Le registre de ce que Vécu a déjà
	// examiné et déjà écarté.
	//
	// Il existe pour une raison mesurée : sans lui, un binaire refusé n'entre
	// dans AUCUN registre. Il reste donc éternellement « présent sur le disque et
	// différent du connu », donc relu, re-refusé et re-journalisé à chaque cycle.
	// Sur le vault de Colin, 298 binaires × un cycle toutes les ~35 s ont produit
	// 17,6 millions de lignes et 3,3 Go de journal depuis le 26/07, soit 99,98 %
	// du fichier. L'instrument sur lequel on compterait si la perte du 10/08
	// revenait était devenu illisible.
	//
	// INDEXÉ PAR EMPREINTE, et pas un simple ensemble de chemins : un binaire
	// MODIFIÉ doit se redire une fois. C'est aussi ce qui fait basculer
	// proprement un fichier entre texte et binaire dans les deux sens - la
	// nouvelle empreinte manque au registre, donc le chemin est réévalué.
	//
	// JAMAIS `Files`, et c'est l'invariant central. `Files` signifie
	// « synchronisé à cette empreinte » : y écrire un binaire que le serveur n'a
	// pas serait un mensonge, et il se paierait sur la détection de suppression
	// (le chemin passerait pour un fichier connu disparu, donc une suppression
	// partirait chez tous les membres).
	//
	// Un champ NOUVEAU, comme `Versions`, `ProjetesAgents` et `Reportes` : on
	// ajoute, on ne mute jamais le type d'un champ existant, sinon un poste
	// revenu à une version antérieure ne démarre plus. Même exposition connue en
	// contrepartie - un cycle sur une version antérieure efface `hors_perimetre`
	// du disque, et les binaires se re-journalisent une fois au retour. Bruit,
	// jamais perte.
	// Source: https://pkg.go.dev/encoding/json#Unmarshal
	HorsPerimetre map[string]string `json:"hors_perimetre,omitempty"`
	// SkillsVus : slugs dont ce poste a DÉJÀ eu connaissance. Un slug rendu par
	// le serveur et absent d'ici est un skill dont on apprend l'existence.
	//
	// PAS d'`omitempty`, et c'est tout le sujet du champ. Le CDC nomme le piège,
	// déjà payé une fois par `Versions` en août : `omitempty` omet une liste
	// vide, le rechargement suivant la lit comme ABSENTE, donc indiscernable
	// d'un état écrit avant la feature - et l'amorce se rejoue. Ici le prix
	// serait un signal AVALÉ : un skill déposé entre les deux amorces entrerait
	// dans le registre sans avoir jamais été annoncé, et plus rien ne le dirait.
	//
	// Sans `omitempty`, une liste vide non nulle s'écrit `[]` et se relit non
	// nulle ; une absence reste `null`. La distinction porte l'information
	// « l'amorce a eu lieu », et elle s'écrit au lieu de se déduire.
	// Source: https://pkg.go.dev/encoding/json#Marshal
	SkillsVus []string `json:"skills_vus"`
	// Adoptes : skills que Vécu a DÉPLACÉS vers `shared/skills/`, cumulatif.
	// C'est le registre des fichiers que l'outil a bougé sans qu'on le lui
	// demande : il doit rester lisible longtemps après le cycle qui l'a produit.
	Adoptes []Adoption `json:"adoptes,omitempty"`
	// Candidats : dossiers qui SONT des skills et que l'adoption a refusés, avec
	// la raison. Photo du dernier cycle, pas un historique. Volontairement pas
	// des Laisses : un refus ne doit jamais faire sortir `vecu sync` en erreur.
	Candidats []Candidat `json:"candidats,omitempty"`
}

// Genre d'une Laisse : ce qui est EN JEU, donc ce que la personne doit faire.
//
// Deux genres ne suffisaient pas. Un nom d'espace refusé sortait sous
// « ces fichiers n'existent que sur ce poste », ce qui est faux ; et le
// rattrapage classait en fichier perdu un chemin qui est sur le serveur et
// seulement absent d'ici. Un rapport qui crie au loup n'est plus lu.
const (
	// GenreFichier : ce contenu n'existe QUE sur ce poste. Le seul genre qui
	// signale un risque de perte, et le seul qui fait sortir `vecu sync` en
	// erreur.
	GenreFichier = "fichier"
	// GenreEspace : la remarque porte sur un espace entier, pas sur un fichier.
	GenreEspace = "espace"
	// GenreServeur : ce chemin est sur le serveur, il n'a simplement pas pu
	// arriver ou être appliqué ici. Rien n'est perdu - mais le dire quand même,
	// le silence est le vrai danger d'un outil de synchronisation.
	GenreServeur = "serveur"
	// GenreHorsPerimetre : ce contenu est hors du périmètre de Vécu - la v2 ne
	// synchronise que du texte - et il le restera. Comme un `GenreFichier`, il
	// n'existe que sur ce poste ; contrairement à lui, ce n'est pas un
	// incident : rien ne peut être tenté, ni par le cycle suivant ni par la
	// personne. Le classer en risque de perte faisait sortir `vecu sync` en
	// erreur à CHAQUE exécution, ce qui apprend à ignorer le code de sortie -
	// donc à ne plus le voir le jour où il signale vraiment quelque chose.
	GenreHorsPerimetre = "hors-perimetre"
)

// Laisse : ce que le cycle n'a pas synchronisé, et pourquoi.
type Laisse struct {
	Chemin string `json:"chemin"`
	Raison string `json:"raison"`
	// Genre : voir les constantes ci-dessus. Vide = GenreFichier, la lecture la
	// plus prudente pour un état écrit par une version antérieure.
	Genre string `json:"genre,omitempty"`
}

// Perdu : cette remarque signale-t-elle un contenu qui n'existe que sur ce
// poste ? C'est la seule question qui doit faire échouer une commande.
func (l Laisse) Perdu() bool { return l.Genre == "" || l.Genre == GenreFichier }

// FichierMetaEspace : les métadonnées d'un espace (son libellé), à sa racine.
// Doit rester identique à `espaces.FichierMeta` côté serveur. Le client le
// synchronise comme n'importe quel texte - c'est voulu, le libellé doit arriver
// chez tout le monde - mais il le reconnaît pour ne pas le compter comme un
// fichier de contenu.
const FichierMetaEspace = ".vecu-espace.json"

// ignoreFichier : liste d'ignore propre à cette racine, en plus des règles par
// défaut. Une ligne = un motif ; « # » commente ; les lignes vides sont ignorées.
const ignoreFichier = ".vecuignore"

// LoadIgnores lit les motifs du `.vecuignore` de la racine. Absent = aucun
// motif (ce n'est pas une erreur).
func LoadIgnores(dir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, ignoreFichier))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var motifs []string
	for _, ligne := range strings.Split(string(b), "\n") {
		ligne = strings.TrimSpace(ligne)
		if ligne == "" || strings.HasPrefix(ligne, "#") {
			continue
		}
		motifs = append(motifs, strings.TrimSuffix(ligne, "/"))
	}
	return motifs, nil
}

func configPath(dir string) string { return filepath.Join(dir, vecuDir, "config.json") }

// SaveConfig écrit la config (crée .vecu/ au besoin).
func SaveConfig(dir string, c Config) error {
	if err := os.MkdirAll(filepath.Join(dir, vecuDir), 0o700); err != nil {
		return err
	}
	return writeJSONFile(configPath(dir), c, 0o600) // 0600 : le token est un secret
}

// LoadConfig lit la config du dossier.
func LoadConfig(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath(dir))
	if err != nil {
		return c, fmt.Errorf("dossier non initialisé (lancer `vecu login`) : %w", err)
	}
	return c, json.Unmarshal(b, &c)
}

// LoadState lit l'état, ou renvoie un état vide si absent (premier démarrage).
func LoadState(dir string) (State, error) {
	s := State{Files: map[string]string{}}
	b, err := os.ReadFile(statePath(dir))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.Files == nil {
		s.Files = map[string]string{}
	}
	return s, nil
}

// Save persiste l'état de façon atomique (écriture temp + rename).
func (s State) Save(dir string) error {
	return writeJSONFile(statePath(dir), s, 0o600)
}

// clone rend une copie profonde : ni la map ni les slices ne sont partagées
// avec l'original. C'est ce qui rend `Engine.State()` lisible de façon
// concurrente - un lecteur itère la copie détachée pendant qu'un cycle réécrit
// l'état interne sous verrou.
func (s State) clone() State {
	c := s
	if s.Files != nil {
		c.Files = make(map[string]string, len(s.Files))
		for k, v := range s.Files {
			c.Files[k] = v
		}
	}
	if s.Versions != nil {
		c.Versions = make(map[string]string, len(s.Versions))
		for k, v := range s.Versions {
			c.Versions[k] = v
		}
	}
	if s.HorsPerimetre != nil {
		c.HorsPerimetre = make(map[string]string, len(s.HorsPerimetre))
		for k, v := range s.HorsPerimetre {
			c.HorsPerimetre[k] = v
		}
	}
	if s.SkillsVus != nil { // nil et vide ne veulent pas dire la même chose ici
		c.SkillsVus = append([]string{}, s.SkillsVus...)
	}
	c.Connus = append([]string(nil), s.Connus...)
	c.Espaces = append([]string(nil), s.Espaces...)
	c.Projetes = append([]string(nil), s.Projetes...)
	c.ProjetesAgents = append([]string(nil), s.ProjetesAgents...)
	c.Reportes = append([]string(nil), s.Reportes...)
	c.Adoptes = append([]Adoption(nil), s.Adoptes...)
	c.Candidats = append([]Candidat(nil), s.Candidats...)
	c.Laisses = append([]Laisse(nil), s.Laisses...)
	return c
}

// writeJSONFile écrit du JSON indenté de façon atomique.
func writeJSONFile(path string, v any, perm os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
