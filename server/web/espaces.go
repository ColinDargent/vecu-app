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
func reglesInternes(nom string, rules []perms.Rule) []string {
	var out []string
	for _, ru := range rules {
		if canon := perms.Canon(ru.Path); strings.HasPrefix(canon, nom+"/") {
			out = append(out, canon+" = "+niveauFR(ru.Level))
		}
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
	return espaces.List(files, u.DefaultLevel, rules), nil
}
