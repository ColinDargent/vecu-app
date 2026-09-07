// Ce qui reste de la notion d'espace côté web : la lecture du périmètre et la
// vue miroir sur la fiche d'un compte.
//
// La matrice espaces × comptes a disparu le 21/08 : l'accès se règle désormais
// dans le dossier lui-même (voir dossiers.go), à l'endroit où il agit. Un
// espace n'est plus qu'un dossier de premier niveau.
package web

import (
	"sort"
	"strings"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
)

// accesVM : le droit d'un utilisateur à la racine d'un objet (un skill), tel
// qu'affiché dans une cellule.
type accesVM struct {
	UserID   int64
	Username string
	Niveau   string // forme stockage : compare directement aux valeurs des boutons
	// Profondes : règles posées À L'INTÉRIEUR de l'objet pour cet utilisateur.
	// Sans cette information, la cellule ment : les boutons agissent sur la
	// racine, mais une règle plus profonde l'emporte dans le calcul du droit
	// effectif. Cliquer « invisible » serait alors un geste sans effet, et
	// l'administrateur croirait avoir coupé un accès resté ouvert.
	Profondes []string
}

// espaceAccesVM : un espace et le droit qu'y a l'utilisateur affiché. Sert la
// vue miroir sur la fiche d'un compte.
type espaceAccesVM struct {
	Nom       string
	Niveau    string // forme stockage
	Profondes []string
}

// reglesInternes : les règles posées strictement à l'intérieur de l'objet.
// Ce sont elles qui peuvent rendre inopérant un clic sur la racine.
//
// LA ZONE DES SKILLS EN EST EXCLUE, et c'est ce qui rend l'avertissement
// lisible (02/09, retour de Colin). `CreateUser` pose `shared/skills = privé`
// sur CHAQUE compte, plus une règle par skill : sur `shared`, cette fonction
// rendait donc trente-cinq lignes identiques d'un compte à l'autre, affichées
// en rouge sous les boutons. L'avertissement disparaissait dans son propre
// bruit, et le bruit disait « alerte » là où il n'y a qu'un défaut du système.
//
// Ces règles-là ne peuvent pas non plus rendre un clic inopérant au sens visé :
// les skills ont leur propre écran, et leur propre section sur cette page. Le
// bouton d'un espace ne prétend rien sur eux.
func reglesInternes(nom string, rules []perms.Rule) []string {
	dansZoneSkills := skills.SousRacine(nom) || perms.Canon(nom) == skills.DefaultRoot
	var out []string
	for _, ru := range rules {
		canon := perms.Canon(ru.Path)
		if !strings.HasPrefix(canon, nom+"/") {
			continue
		}
		// L'exclusion ne vaut QUE quand l'objet affiché est hors de la zone des
		// skills. Sur le panneau d'un skill, une règle posée à l'intérieur de ce
		// skill est exactement l'avertissement qu'on veut : elle rend bien le
		// bouton de sa racine inopérant.
		if !dansZoneSkills && (skills.SousRacine(canon) || canon == skills.DefaultRoot) {
			continue
		}
		out = append(out, canon+" = "+niveauFR(ru.Level))
	}
	sort.Strings(out)
	return out
}

// espacesDuDepot : les espaces tels que les voit l'administrateur courant.
//
// C'est volontairement SON périmètre qui sert de référence, comme partout
// ailleurs dans le web admin : un administrateur ne distribue pas un accès à un
// espace qu'il ne voit pas lui-même.
func (s *Server) espacesDuDepot(u *db.User) ([]espaces.Info, error) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		return nil, err
	}
	files, err := s.Store.List("")
	if err != nil {
		return nil, err
	}
	// Meme vue que l'accueil et l'ecran des dossiers : un administrateur voit
	// les espaces qu'il s'est fermes, annotes de leur niveau reel (DAR-196).
	if u.IsAdmin {
		return espaces.ListAdmin(files, u.DefaultLevel, rules), nil
	}
	return espaces.List(files, u.DefaultLevel, rules), nil
}
