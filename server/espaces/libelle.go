package espaces

// libelle.go : DT3 - le nom technique et le libellé sont deux choses
// différentes.
//
// Le nom d'un espace est aujourd'hui l'identifiant technique ET ce que l'humain
// lit : ASCII strict, 64 caractères, sans accent, sans espace. Le parcours A
// rend cette confusion intenable - quelqu'un qui partage
// `~/Documents/Clients 2026` ne va pas renommer son dossier pour faire plaisir
// à l'outil.
//
// L'espace garde donc son nom technique (dérivé, normalisé, jamais montré) et
// gagne un libellé affiché. Le nom technique reste le premier segment du chemin
// du dépôt : rien ne bouge dans le stockage ni dans les droits.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/colindargent/vecu/server/storage"
)

// FichierMeta : les métadonnées d'un espace, à sa racine.
//
// Un fichier, pas une table : il se synchronise comme le reste, donc le libellé
// arrive chez tout le monde sans qu'aucune route ne le distribue, et il survit à
// une restauration du dépôt. Le nom ne commence pas par « .vecu » tout court -
// ce segment-là est ignoré par la synchronisation, et le libellé ne partirait
// jamais.
const FichierMeta = ".vecu-espace.json"

// Meta : ce que porte le fichier de métadonnées.
type Meta struct {
	Libelle string `json:"libelle"`
}

// replisAccent : le repli des lettres accentuées vers leur forme ASCII.
//
// Une table explicite plutôt que `golang.org/x/text/unicode/norm` : le projet
// tient une discipline zéro-dépendance, et importer un paquet de normalisation
// Unicode pour trente lettres la romprait. La table couvre le français et ses
// voisins immédiats ; tout ce qu'elle ne connaît pas devient un tiret, ce qui
// est un repli sûr - un nom technique n'a pas à être joli, il a à être stable.
var replisAccent = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a",
	'ç': "c",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i",
	'ñ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u",
	'ý': "y", 'ÿ': "y",
	'æ': "ae", 'œ': "oe", 'ß': "ss",
}

// NomTechnique dérive d'un libellé humain un nom que `ValidNom` accepte.
//
// Ne rend JAMAIS une chaîne vide ni un nom invalide : un libellé fait
// uniquement d'emojis, de ponctuation ou d'idéogrammes retombe sur « espace »,
// que l'appelant désambiguïsera comme n'importe quelle autre collision. Rendre
// une erreur ici obligerait l'humain à trouver lui-même un nom ASCII, ce qui est
// exactement le geste que DT3 supprime.
func NomTechnique(libelle string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(libelle)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == '.':
			b.WriteRune(r)
		default:
			if repli, connu := replisAccent[r]; connu {
				b.WriteString(repli)
			} else {
				b.WriteByte('-')
			}
		}
	}
	nom := b.String()
	// Les séparateurs se tassent : « Clients   2026 » ne doit pas donner
	// « clients---2026 ».
	for strings.Contains(nom, "--") {
		nom = strings.ReplaceAll(nom, "--", "-")
	}
	// Les bords, dans cet ordre et en boucle : `ValidNom` refuse un nom qui
	// commence par « . » ou « - » et un nom qui finit par « . », et un libellé
	// comme « ...à » les enchaîne.
	nom = strings.Trim(nom, "-.")
	if len(nom) > maxNom {
		nom = strings.Trim(nom[:maxNom], "-.")
	}
	if nom == "" || ValidNom(nom) != nil {
		return "espace"
	}
	return nom
}

// NomLibre rend le premier nom disponible à partir de `base` : `base`, puis
// `base-2`, `base-3`...
//
// C'est la désambiguïsation que DT3 exige : deux personnes qui partagent chacune
// un dossier `notes` produisent le même nom technique, et **fusionner deux
// dossiers sans rapport** serait la pire des réponses - le contenu de l'un
// apparaîtrait chez l'autre, avec les droits de l'autre.
func NomLibre(st *storage.Store, base string) (string, error) {
	for essai := 1; essai <= 999; essai++ {
		nom := base
		if essai > 1 {
			suffixe := fmt.Sprintf("-%d", essai)
			// Tronquer la BASE, pas le suffixe : un nom coupé à 64 dont on aurait
			// rogné le « -2 » retomberait sur le nom déjà pris, et la boucle
			// tournerait jusqu'à 999 sans jamais trouver.
			if len(base)+len(suffixe) > maxNom {
				nom = strings.Trim(base[:maxNom-len(suffixe)], "-.")
			}
			nom += suffixe
		}
		occupation, err := Occupation(st, nom)
		if err != nil {
			return "", err
		}
		if occupation == Libre {
			return nom, nil
		}
	}
	return "", fmt.Errorf("impossible de trouver un nom libre à partir de « %s »", base)
}

// CreateDepuisLibelle crée un espace à partir d'un libellé humain et rend le nom
// technique retenu.
//
// Le nom retenu est RENDU, et l'appelant doit le dire : c'est l'exigence de DT3.
// Quelqu'un qui partage « Notes » et reçoit `notes-2` doit l'apprendre au moment
// du partage, pas le découvrir un jour dans un chemin.
func CreateDepuisLibelle(st *storage.Store, libelle, auteur string) (nom string, err error) {
	libelle = strings.TrimSpace(libelle)
	if libelle == "" {
		return "", fmt.Errorf("libellé vide")
	}
	nom, err = NomLibre(st, NomTechnique(libelle))
	if err != nil {
		return "", err
	}
	// Une seule écriture, avec le bon libellé du premier coup. Depuis que le
	// fichier de métadonnées est CE QUI FAIT EXISTER l'espace, son échec n'est
	// plus un défaut d'apparence : il n'y aurait pas d'espace du tout. Il n'y a
	// donc plus de cas « créé, mais sans libellé » à rattraper.
	if err := creeAvecLibelle(st, nom, libelle, auteur); err != nil {
		return "", err
	}
	return nom, nil
}

// EcritLibelle pose (ou remplace) le libellé affiché d'un espace.
// creeAvecLibelle : refuse un nom déjà pris, puis écrit le fichier qui fait
// exister l'espace. Le point de passage unique des deux façons de créer un
// espace (l'app par `POST /espaces`, le web par son formulaire).
func creeAvecLibelle(st *storage.Store, nom, libelle, auteur string) error {
	b, err := json.MarshalIndent(Meta{Libelle: libelle}, "", "  ")
	if err != nil {
		return err
	}
	_, err = st.WriteWithMessage(nom+"/"+FichierMeta, string(b)+"\n", auteur, "create espace "+nom)
	return err
}

func EcritLibelle(st *storage.Store, nom, libelle, auteur string) error {
	b, err := json.MarshalIndent(Meta{Libelle: libelle}, "", "  ")
	if err != nil {
		return err
	}
	_, err = st.WriteWithMessage(nom+"/"+FichierMeta, string(b)+"\n", auteur, "libellé de "+nom)
	return err
}

// LitLibelle rend le libellé affiché d'un espace, ou "" s'il n'en a pas.
//
// Un fichier absent, illisible ou mal formé rend "" sans erreur : l'espace
// s'affiche alors sous son nom technique. C'est le repli que DT3 prévoit, et il
// vaut mieux qu'une liste d'espaces qui refuse de s'afficher parce que l'un
// d'eux a un fichier de métadonnées abîmé.
func LitLibelle(st *storage.Store, nom string) string {
	contenu, err := st.Read(nom+"/"+FichierMeta, "")
	if err != nil {
		return ""
	}
	var m Meta
	if json.Unmarshal([]byte(contenu), &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.Libelle)
}
