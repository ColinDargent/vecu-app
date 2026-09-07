// Package espaces : la notion d'espace de Vécu.
//
// Un espace est un dossier de premier niveau du dépôt. Il n'existe pas de table
// d'espaces : la règle de droit posée sur « shared » EST l'appartenance à
// l'espace « shared », et la liste des espaces se dérive des chemins du dépôt.
// C'est ce qui permet d'ajouter la notion d'espace sans toucher au stockage ni
// au modèle de droits.
//
// Règle unique, sans exception : un espace existe pour quelqu'un si et
// seulement si au moins un fichier ordinaire qu'il contient est lisible par
// cette personne. Il n'y a pas de fichier marqueur - un marqueur serait à la
// fois « pas du contenu » (donc non compté) et « du contenu » (donc suffisant à
// faire exister l'espace), et cette asymétrie révélerait le nom d'un espace à
// quelqu'un qui n'a le droit d'y lire aucun fichier. Un espace créé depuis
// l'interface reçoit donc un vrai fichier d'accueil, visible et modifiable.
package espaces

import (
	"fmt"
	"sort"
	"strings"

	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// maxNom borne le nom d'un espace (il devient un dossier sur tous les postes).
const maxNom = 64

// Info : un espace vu par un utilisateur donné.
type Info struct {
	Nom string
	// Niveau : droit effectif à la RACINE de l'espace. Ne suffit jamais à
	// décider de l'appartenance : voir List.
	Niveau perms.Level
	// Fichiers : nombre de fichiers lisibles par cet utilisateur.
	//
	// SAUF par ListAdmin, qui ne filtre pas : le compte porte alors tous les
	// fichiers de l'espace que l'appelant lui a passes, et Niveau comme Ecriture
	// peuvent dire « invisible » sur un espace dont le compte n'est pas zero.
	// C'est voulu - un administrateur doit voir l'espace qu'il s'est ferme.
	Fichiers int
	// Ecriture : au moins un chemin de l'espace est modifiable. Un espace
	// entièrement en lecture seule doit pouvoir être annoncé comme tel, sinon
	// l'utilisateur édite et ses écritures sont refusées en silence.
	Ecriture bool
}

// List dérive les espaces visibles par un utilisateur à partir de la liste
// complète des chemins du dépôt.
//
// Un espace est listé si et seulement si AU MOINS UN de ses fichiers est
// lisible par cet utilisateur. Trois conséquences voulues :
//
//   - Un espace dont la racine est invisible reste listé dès qu'un sous-dossier
//     est accessible. Les droits étant surchargeables dans les deux sens, ne pas
//     le lister priverait l'utilisateur de fichiers auxquels il a droit.
//   - Le droit sur le NOM du dossier ne suffit jamais. Sans cette règle, un
//     compte « lecture par défaut, exceptions invisibles » se verrait annoncer
//     l'existence d'espaces dont il ne peut lire aucun fichier.
//   - Un espace vidé de ses fichiers cesse d'exister, pour tout le monde.
//
// Sont ignorés : les chemins qui ne passent pas ValidChemin, c'est-à-dire les
// fichiers posés à la racine du dépôt et les dossiers de premier niveau au nom
// refusé. L'écriture de tels chemins est refusée par l'API (voir ValidChemin) ;
// le filtre reste ici en défense pour un dépôt importé hors du produit.
func List(paths []string, defaut perms.Level, rules []perms.Rule) []Info {
	return liste(paths, defaut, rules, true)
}

// ListAdmin liste les espaces SANS ecarter ceux que le lecteur s'est fermes,
// en gardant l'annotation de niveau REELLE.
//
// Pourquoi elle existe. `List` confondait deux questions : « quels chemins
// considerer » et « quel niveau afficher ». Pour un administrateur les deux
// divergent - il doit voir l'espace qu'il a ferme, annote « prive », sinon il
// ne peut plus le rouvrir depuis l'accueil et « administrable » n'est vrai que
// pour qui connait l'URL. C'est le meme defaut que l'ecran des dossiers portait
// (DAR-196), au meme endroit du raisonnement.
//
// A n'appeler que sur un lecteur administrateur : elle ne filtre rien.
func ListAdmin(paths []string, defaut perms.Level, rules []perms.Rule) []Info {
	return liste(paths, defaut, rules, false)
}

func liste(paths []string, defaut perms.Level, rules []perms.Rule, filtre bool) []Info {
	byNom := map[string]*Info{}
	for _, p := range paths {
		nom, _, ok := strings.Cut(p, "/")
		if !ok || ValidNom(nom) != nil {
			continue
		}
		if filtre && !perms.CanRead(p, defaut, rules) {
			continue
		}
		info := byNom[nom]
		if info == nil {
			info = &Info{Nom: nom, Niveau: perms.Effective(nom, defaut, rules)}
			byNom[nom] = info
		}
		info.Fichiers++
		if perms.CanWrite(p, defaut, rules) {
			info.Ecriture = true
		}
	}
	out := make([]Info, 0, len(byNom))
	for _, info := range byNom {
		out = append(out, *info)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Nom < out[b].Nom })
	return out
}

// suffixesReserves : suffixes qui, sous macOS, font qu'un dossier cesse d'être
// un dossier ordinaire (bundles, documents composés, dossiers localisés). Un
// espace nommé « Notes.app » s'afficherait comme une application dans le Finder
// et ne s'ouvrirait pas au double-clic.
var suffixesReserves = []string{
	".app", ".bundle", ".framework", ".kext", ".plugin",
	".rtfd", ".lproj", ".localized", ".pkg", ".dsym",
}

// ValidNom valide un nom d'espace. Ce nom devient un dossier de premier niveau
// sur le poste de chaque membre : on refuse tout ce qui pourrait s'y comporter
// autrement qu'un dossier ordinaire, sur macOS comme sur Linux.
//
// Jeu de caractères volontairement restreint à l'ASCII (lettres, chiffres,
// « . », « _ », « - »). Ce n'est pas de la frilosité : macOS normalise les noms
// de fichiers, si bien qu'un « équipe » composé (NFD) et un « équipe » précomposé
// (NFC) sont deux espaces distincts côté serveur mais un seul dossier sur le
// disque - deux périmètres de droits différents fusionnés au même endroit. La
// restriction rend le cas impossible plutôt que de le gérer. Elle rend aussi
// inutiles les contrôles de caractères de contrôle, de « / » et d'espaces.
//
// La restriction ne vaut QUE pour le premier niveau : à l'intérieur d'un
// espace, les accents sont pleinement supportés (« notes/idées.md » est un
// chemin nominal, et le stockage a été réglé exprès pour).
func ValidNom(nom string) error {
	if nom == "" {
		return fmt.Errorf("nom d'espace vide")
	}
	for _, r := range nom {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("caractère interdit dans un nom d'espace : %q (lettres non accentuées, chiffres, « . _ - » uniquement)", r)
		}
	}
	// Le contrôle de longueur vient après celui des caractères : sur un nom
	// accentué, « caractère interdit » est le vrai motif du refus, et le nom
	// étant désormais ASCII, octets et caractères coïncident.
	if len(nom) > maxNom {
		return fmt.Errorf("nom d'espace trop long (%d caractères maximum)", maxNom)
	}
	switch {
	case strings.HasPrefix(nom, "."):
		// Un espace caché serait invisible dans le Finder et dans Obsidian, donc
		// indébuggable ; « . » et « .. » sont écartés par la même règle, comme
		// les dossiers de configuration (« .obsidian », « .git »).
		return fmt.Errorf("un nom d'espace ne peut pas commencer par « . »")
	case strings.HasSuffix(nom, "."):
		return fmt.Errorf("un nom d'espace ne peut pas se terminer par « . »")
	case strings.HasPrefix(nom, "-"):
		// « -rf » n'est pas un dossier ordinaire pour les outils en ligne de
		// commande du poste de l'utilisateur.
		return fmt.Errorf("un nom d'espace ne peut pas commencer par « - »")
	}
	bas := strings.ToLower(nom)
	for _, suffixe := range suffixesReserves {
		if strings.HasSuffix(bas, suffixe) {
			return fmt.Errorf("suffixe réservé par macOS : « %s »", suffixe)
		}
	}
	return nil
}

// ValidChemin valide un chemin destiné à être écrit dans le dépôt : il doit
// vivre DANS un espace au nom valide.
//
// C'est la garantie que le modèle tient de bout en bout. Un espace naît du
// premier fichier écrit dedans, pas d'un appel à Create : sans ce contrôle à
// l'écriture, n'importe quel client fabriquerait des dossiers de premier niveau
// que la validation refuse ensuite de reconnaître - le fichier serait accepté,
// stocké, relu, et pourtant synchronisé nulle part. Un refus visible vaut mieux
// qu'un contenu qui s'évapore.
func ValidChemin(p string) error {
	nom, reste, ok := strings.Cut(p, "/")
	if !ok || reste == "" {
		return fmt.Errorf("un fichier doit être rangé dans un espace (par exemple « shared/%s »)", p)
	}
	return ValidNom(nom)
}

// Existant décrit ce qui occupe déjà un nom de premier niveau.
type Existant int

const (
	Libre   Existant = iota // rien ne porte ce nom
	Dossier                 // un espace existe (à la casse près)
	Fichier                 // un fichier de la racine porte ce nom
)

// Occupation indique ce qui occupe le nom de premier niveau `nom`, **à la casse
// près**.
//
// La comparaison insensible à la casse n'est pas un confort : APFS et HFS+ sont
// insensibles à la casse par défaut, donc « clients » et « Clients » seraient
// deux espaces de périmètres différents déversés dans un seul dossier local.
// Pire, la sync verrait alors les fichiers de l'un comme supprimés et ceux de
// l'autre comme nouveaux, et déplacerait le contenu d'un périmètre à l'autre.
func Occupation(st *storage.Store, nom string) (Existant, error) {
	paths, err := st.List("")
	if err != nil {
		return Libre, err
	}
	for _, p := range paths {
		premier, _, dansDossier := strings.Cut(p, "/")
		if !dansDossier {
			premier = p // fichier posé à la racine du dépôt
		}
		if !strings.EqualFold(premier, nom) {
			continue
		}
		if dansDossier {
			return Dossier, nil
		}
		return Fichier, nil
	}
	return Libre, nil
}

// Create crée l'espace en y déposant son fichier d'accueil.
//
// **L'autorisation est à la charge de l'appelant** : cette fonction ne consulte
// aucun droit. Créer un espace depuis l'interface est une opération
// d'administration, réservée aux routes protégées par le middleware admin. À
// noter que ce n'est pas le seul chemin de création : un compte disposant du
// droit d'écriture à la racine crée un espace en y écrivant un fichier, ce qui
// est le sens même de « écriture à la racine » dans ce modèle.
//
// La vérification d'occupation n'est pas atomique avec l'écriture : deux
// créations simultanées du même nom écriraient le même fichier d'accueil deux
// fois, ce qui est sans effet de bord. Le seul coût d'une course est un message
// d'erreur manquant, jamais une perte.
func Create(st *storage.Store, nom, auteur string) error {
	if err := ValidNom(nom); err != nil {
		return err
	}
	occupation, err := Occupation(st, nom)
	if err != nil {
		return err
	}
	switch occupation {
	case Dossier:
		return fmt.Errorf("l'espace « %s » existe déjà (les majuscules ne distinguent pas deux espaces)", nom)
	case Fichier:
		return fmt.Errorf("un fichier de la racine du dépôt porte déjà le nom « %s »", nom)
	}
	// LE FICHIER QUI FAIT EXISTER L'ESPACE EST SON FICHIER DE MÉTADONNÉES, et
	// plus un `README.md` d'accueil.
	//
	// Mesuré le 21/08 au premier test réel du parcours A : partager un dossier
	// qui contient déjà un `README.md` - c'est-à-dire n'importe quel dossier de
	// projet - faisait entrer en collision le fichier d'accueil et celui de la
	// personne. Le dossier adopté pousse le sien sans marque-page, donc sur une
	// histoire non apparentée : le serveur fusionne, l'accueil prend la place sur
	// le disque et le texte de la personne finit dans une copie de conflit. Rien
	// n'est perdu, mais c'est le premier geste de la V1 publique et il défigure
	// le dossier qu'on vient de lui confier.
	//
	// Le fichier de métadonnées ne peut pas entrer en collision : son nom est
	// réservé, il porte le libellé, et il était de toute façon écrit à la
	// création. L'asymétrie que `List` refuse - un marqueur « pas du contenu »
	// ici et « du contenu » là - n'est pas introduite : ce fichier est compté
	// comme n'importe quel autre, partout, et il obéit aux mêmes droits.
	// Libellé VIDE, pas le nom technique : « pas de libellé, donc on affiche le
	// nom technique » (DT3) reste une situation qui existe, et `Affichage` la
	// traite déjà. Y mettre le nom la ferait disparaître au profit d'une copie
	// qui dit la même chose.
	return creeAvecLibelle(st, nom, "", auteur)
}

// Contenu rend les chemins que l'espace porte EN DEHORS de son fichier de
// métadonnées, triés. C'est ce qu'on perdrait de vue en le supprimant.
func Contenu(st *storage.Store, nom string) ([]string, error) {
	paths, err := st.List("")
	if err != nil {
		return nil, err
	}
	prefixe := nom + "/"
	meta := prefixe + FichierMeta
	var reste []string
	for _, p := range paths {
		if !strings.HasPrefix(p, prefixe) || p == meta {
			continue
		}
		reste = append(reste, p)
	}
	sort.Strings(reste)
	return reste, nil
}

// Supprimer retire l'espace en retirant le fichier qui le fait exister.
//
// L'ESPACE DOIT ÊTRE VIDE, et ce refus est le cœur de la fonction plutôt qu'une
// précaution. Un espace est un dossier de premier niveau, et son existence tient
// à son fichier de métadonnées : retirer ce fichier alors que le dossier porte
// encore des fichiers ne les supprime pas, il les rend ORPHELINS. Le client sait
// déjà nommer cet état - « chemin hors espace côté serveur : aucun dossier local
// ne peut l'accueillir » - et c'est un cul-de-sac : plus aucun poste ne peut les
// redescendre, et aucun écran ne les montre.
//
// L'alternative aurait été de supprimer le contenu avec l'espace. On ne la prend
// pas : ce serait la seule suppression de masse du produit, déclenchée par un
// bouton d'administration, sur des fichiers que celui qui clique n'a pas
// forcément lus. Vider d'abord est un geste explicite, et il passe par les
// chemins de suppression qui existent déjà, avec leurs propres gardes.
func Supprimer(st *storage.Store, nom, auteur string) error {
	if err := ValidNom(nom); err != nil {
		return err
	}
	occupation, err := Occupation(st, nom)
	if err != nil {
		return err
	}
	if occupation != Dossier {
		return fmt.Errorf("l'espace « %s » n'existe pas", nom)
	}
	reste, err := Contenu(st, nom)
	if err != nil {
		return err
	}
	if len(reste) > 0 {
		exemple := reste[0]
		if len(reste) > 1 {
			exemple = fmt.Sprintf("%s (et %d autre%s)", reste[0], len(reste)-1, pluriel(len(reste)-1))
		}
		return fmt.Errorf("l'espace « %s » porte encore %d fichier%s : videz-le d'abord, sinon ils deviendraient "+
			"introuvables pour tous les postes. Premier restant : %s",
			nom, len(reste), pluriel(len(reste)), exemple)
	}
	_, err = st.Delete(nom+"/"+FichierMeta, auteur)
	return err
}

func pluriel(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}
