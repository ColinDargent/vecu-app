package web

// jetons.go : l'écran des jetons MCP.
//
// CHAQUE COMPTE GÈRE LES SIENS, et c'est une décision de sécurité plutôt qu'un
// choix d'ergonomie. Un jeton agit AU NOM d'un compte : permettre d'en créer un
// pour le compte d'un autre reviendrait à donner à l'administrateur un moyen
// d'agir sous l'identité de quelqu'un, ce qu'aucune autre page du back office
// n'offre. En se limitant à soi-même, la propriété centrale du jalon tient sans
// exception : un jeton MCP ne peut rien que son porteur humain ne pourrait.
//
// LIMITE CONNUE, nommée plutôt que découverte : personne ne peut donc révoquer
// le jeton d'un autre depuis cet écran. Le levier qui reste est la suppression
// du compte, dont la cascade emporte ses jetons. Ça suffit au départ d'un
// collaborateur ; ça ne suffira pas le jour où on voudra couper un jeton précis
// sans toucher au compte.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
)

// quand : une date de base rendue lisible.
//
// La base stocke du RFC3339 en UTC, ce qui est le bon format pour trier et le
// mauvais pour lire - « 2026-08-31T08:42:34Z » ne dit rien à personne, et le
// serveur tourne en UTC alors que ceux qui lisent l'écran n'y sont pas.
//
// Pour `dernier_usage`, c'est l'ÉCART qui porte l'information : la colonne
// existe pour repérer un jeton oublié quelque part, et « il y a 3 mois »
// répond à cette question là où une date absolue demande un calcul. L'absolu
// reste dans l'infobulle, pour qui veut la précision.
//
// L'ABSOLU RESTE EN UTC, et c'est un choix, pas un oubli : ce back office est
// server-rendered sans une ligne de JavaScript, donc le serveur ne connaît pas
// le fuseau du lecteur. Afficher un fuseau serveur non étiqueté serait pire ;
// l'écart relatif, lui, est juste dans tous les fuseaux, et c'est la valeur que
// la colonne met en avant.
func quand(iso string, maintenant time.Time) (libelle, exact string) {
	if iso == "" {
		return "", ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso, iso // illisible : on rend le brut plutôt que de mentir
	}
	exact = t.UTC().Format("02/01/2006 à 15:04") + " UTC"
	d := maintenant.Sub(t)
	if d < 0 {
		// Date dans le futur : un ajustement d'horloge, ou une base bricolée.
		// Un écart négatif tomberait dans « à l'instant », ce qui serait faux
		// en silence. On rend l'absolu, qui est visiblement bizarre.
		return exact, exact
	}
	switch {
	case d < time.Minute:
		return "à l'instant", exact
	case d < time.Hour:
		return fmt.Sprintf("il y a %d min", int(d.Minutes())), exact
	case d < 24*time.Hour:
		return fmt.Sprintf("il y a %d h", int(d.Hours())), exact
	case d < 60*24*time.Hour:
		return fmt.Sprintf("il y a %d j", int(d.Hours()/24)), exact
	default:
		return exact, exact
	}
}

type jetonVM struct {
	ID          int64
	Libelle     string
	Plafond     string
	CreeLe      string
	CreeLeExact string
	// DernierUsage porte l'écart (« il y a 3 j »), DernierUsageExact la date
	// complète, en infobulle.
	DernierUsage      string
	DernierUsageExact string
	Revoque           bool
	RevoqueLe         string
}

type jetonsData struct {
	baseData
	Jetons []jetonVM
	// Clair : le jeton en clair, rendu UNE SEULE FOIS, sur la réponse à sa
	// création. Il n'est stocké nulle part - la base ne porte que son SHA-256 -
	// donc recharger la page le fait disparaître pour de bon. C'est voulu.
	Clair  string
	Erreur string
}

// maxLibelleJeton : le dépôt borne tous ses champs libres (`ValidUsername` à 64
// caractères, `validChemin` sur les caractères de contrôle). Celui-ci n'y
// échappe pas, et il a une raison de plus : le libellé part dans le journal
// d'écriture du serveur, où un nom de 100 ko ou truffé de retours à la ligne
// rendrait les traces illisibles.
const maxLibelleJeton = 64

// libelleValide rend le libellé nettoyé, ou le motif du refus.
func libelleValide(brut string) (libelle, motif string) {
	libelle = strings.TrimSpace(brut)
	if libelle == "" {
		return "", "donnez un nom au jeton : c'est la seule chose qui permettra de le reconnaître plus tard"
	}
	if utf8.RuneCountInString(libelle) > maxLibelleJeton {
		return "", fmt.Sprintf("le nom du jeton dépasse %d caractères", maxLibelleJeton)
	}
	for _, r := range libelle {
		// Les caractères de contrôle, pour la raison écrite dans `validChemin` :
		// ils rendent l'entrée difficile à reconnaître dans le rendu, donc
		// difficile à révoquer. Et ici, ils partent aussi dans le journal.
		if r < 0x20 || r == 0x7f {
			return "", "le nom du jeton contient un caractère de contrôle"
		}
	}
	return libelle, ""
}

// GET /admin/jetons
func (s *Server) handleJetons(w http.ResponseWriter, r *http.Request, u *db.User) {
	s.renderJetons(w, http.StatusOK, u, "", "")
}

func (s *Server) renderJetons(w http.ResponseWriter, status int, u *db.User, clair, erreur string) {
	liste, err := s.DB.ListJetonsMCP(u.ID)
	if err != nil {
		// Un `niveau_max` illisible en base fait échouer la liste plutôt que de
		// s'afficher « invisible », qui serait rassurant et faux. On le DIT,
		// dans le journal : sans ça le 500 serait muet, et personne ne saurait
		// quelle ligne réparer.
		log.Printf("web: liste des jetons MCP de %q : %v", u.Username, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	data := jetonsData{baseData: base(u, "jetons"), Clair: clair, Erreur: erreur}
	maintenant := s.maintenant()
	for _, j := range liste {
		cree, creeExact := quand(j.CreeLe, maintenant)
		usage, usageExact := quand(j.DernierUsage, maintenant)
		revoque, _ := quand(j.RevoqueLe, maintenant)
		data.Jetons = append(data.Jetons, jetonVM{
			ID:                j.ID,
			Libelle:           j.Libelle,
			Plafond:           plafondFR(j.NiveauMax),
			CreeLe:            cree,
			CreeLeExact:       creeExact,
			DernierUsage:      usage,
			DernierUsageExact: usageExact,
			Revoque:           j.RevoqueLe != "",
			RevoqueLe:         revoque,
		})
	}
	render(w, status, "jetons", data)
}

// POST /admin/jetons : crée un jeton et affiche son clair, une fois.
//
// Rend la page directement au lieu de rediriger, et c'est le seul endroit du
// back office qui le fait : une redirection perdrait le clair, qui n'existe
// qu'en mémoire le temps de cette réponse.
func (s *Server) handleCreerJeton(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderJetons(w, http.StatusBadRequest, u, "", "formulaire invalide")
		return
	}
	libelle, motif := libelleValide(r.PostFormValue("libelle"))
	if motif != "" {
		s.renderJetons(w, http.StatusBadRequest, u, "", motif)
		return
	}
	plafond, ok := perms.ParseLevel(r.PostFormValue("plafond"))
	if !ok || (plafond != perms.Lecture && plafond != perms.Ecriture) {
		s.renderJetons(w, http.StatusBadRequest, u, "", "plafond invalide")
		return
	}
	clair, err := s.DB.IssueMCPToken(u.ID, libelle, plafond)
	if err != nil {
		s.renderJetons(w, http.StatusInternalServerError, u, "", "création impossible")
		return
	}
	s.renderJetons(w, http.StatusOK, u, clair, "")
}

// POST /admin/jetons/revoquer
func (s *Server) handleRevoquerJeton(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderJetons(w, http.StatusBadRequest, u, "", "formulaire invalide")
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err != nil {
		s.renderJetons(w, http.StatusBadRequest, u, "", "identifiant invalide")
		return
	}
	// LA GARDE DE PROPRIÉTÉ vit dans la REQUÊTE (`WHERE id = ? AND user_id = ?`),
	// pas dans un parcours de la liste.
	//
	// La première version vérifiait la propriété en relisant `ListJetonsMCP`,
	// au motif qu'« on ne peut pas révoquer ce qu'on ne voit pas ». Ça faisait
	// dépendre l'autorisation de la santé de l'affichage : une seule ligne
	// corrompue fait échouer la liste entière, et l'écran ne pouvait alors plus
	// couper AUCUN jeton du compte - pendant que les jetons sains continuaient
	// de fonctionner. Un coupe-circuit en panne à cause d'une ligne voisine.
	if err := s.DB.RevoqueJetonMCPDuCompte(u.ID, id); errors.Is(err, db.ErrNotFound) {
		// Inconnu et « appartient à quelqu'un d'autre » se disent PAREIL.
		s.renderJetons(w, http.StatusNotFound, u, "", "aucun jeton de cet identifiant sur ce compte")
		return
	} else if err != nil {
		log.Printf("web: révocation du jeton %d : %v", id, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/jetons", http.StatusSeeOther)
}

// maintenant : l'horloge du serveur, injectable pour les tests de rendu.
func (s *Server) maintenant() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// plafondFR : le plafond d'un jeton, qui n'est PAS la posture d'un coffre.
//
// La posture (privé / lecture seule / ouvert) decrit ce qu'un dossier EST pour
// quelqu'un. Le plafond d'un jeton decrit ce que ce jeton PEUT FAIRE, et
// « ouvert » n'y veut rien dire. Deux vocabulaires parce que ce sont deux
// questions ; les confondre rendrait l'ecran des jetons illisible pour gagner
// une fonction.
func plafondFR(l perms.Level) string {
	if l == perms.Ecriture {
		return "lecture et écriture"
	}
	return "lecture seule"
}
