package api

// ecriture.go : LA porte d'écriture, extraite pour être partagée.
//
// Pourquoi ce fichier existe. La surface MCP écrit, et la seule façon
// défendable de la brancher est qu'elle emprunte le chemin de l'API - mêmes
// droits, même fusion, mêmes copies de conflit serveur. (« Même journal » serait
// faux : il n'existe aucun journal d'écriture côté serveur. La porte MCP pose
// sa propre trace, qui nomme le jeton - voir `journaliseEcritureMCP`.)
// Dupliquer ferait dériver la porte MCP le jour où la porte HTTP change, et
// c'est exactement là que se cachent les trous d'accès.
//
// Ce qui change entre les deux portes, et RIEN D'AUTRE :
//   - comment on calcule le droit de l'appelant (le jeton MCP porte un plafond
//     en plus des droits de son compte) ;
//   - si l'appropriation d'un skill par première écriture est permise.
//
// Les deux différences sont portées par `ecrivain`, pas par une branche dans
// le corps de la fonction.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
)

// ecrivain : ce que la porte d'écriture a besoin de savoir de son appelant.
type ecrivain struct {
	userID   int64
	username string
	// niveau rend le droit effectif de cet appelant sur un chemin. C'est ici
	// que la porte MCP injecte le plafond de son jeton, sans que le corps de
	// l'écriture ait à savoir qu'un plafond existe.
	niveau func(chemin string) perms.Level
	// appropriationSkill : la première écriture sous `shared/skills/<slug>/`
	// peut-elle REVENDIQUER le skill pour ce compte ?
	//
	// Vrai par la porte HTTP, faux par la porte MCP. C'est le seul endroit du
	// jalon où les deux portes divergent, et le choix est tranché ici plutôt
	// que découvert plus tard.
	//
	// La revendication est la seule écriture qui CRÉE un droit au lieu d'en
	// exercer un : elle accorde l'écriture sur un chemin où le compte n'avait
	// rien. Un humain qui la déclenche voit ce qu'il fait ; une routine qui
	// tourne sans personne, non - et il suffirait qu'un agent écrive dans
	// `shared/skills/quelque-chose/` pour s'emparer d'un nom, en silence.
	//
	// Ce n'est PAS une seconde implémentation du chemin d'écriture : c'est une
	// autorisation plus étroite, du même genre que le plafond du jeton, sur un
	// chemin de code strictement partagé. Un skill qui EXISTE déjà s'écrit par
	// la porte MCP exactement comme par la porte HTTP, selon les droits
	// normaux.
	appropriationSkill bool
}

// ecrivainWeb : l'appelant d'une requête API authentifiée par jeton d'appareil.
//
// Les règles sont chargées PARESSEUSEMENT, à la première question de droit.
// C'est ce qui préserve l'ordre d'origine du handler : un chemin invalide était
// refusé sans toucher SQLite, et sur une base à `SetMaxOpenConns(1)` partagée
// avec le web admin, une requête base gratuite par écriture refusée n'est pas
// rien. Une seule charge par requête, mémorisée.
func (s *Server) ecrivainWeb(u *db.User) ecrivain {
	var rules []perms.Rule
	var charge bool
	var errCharge error
	return ecrivain{
		userID:   u.ID,
		username: u.Username,
		niveau: func(chemin string) perms.Level {
			if !charge {
				rules, errCharge = s.DB.Rules(u.ID)
				charge = true
			}
			if errCharge != nil {
				return perms.Invisible // fail-closed : on ne devine pas un droit
			}
			return perms.Effective(chemin, u.DefaultLevel, rules)
		},
		appropriationSkill: true,
	}
}

// ecrivainDe : LE point où l'on décide sous quelle contrainte cet appel écrit.
//
// Un seul endroit, traversé par les trois routes fichiers ET par les outils
// MCP. C'est ce qui garantit qu'un jeton plafonné est plafonné quelle que soit
// la porte par laquelle il arrive - un contrôle posé dans chaque handler
// finirait par manquer dans l'un d'eux.
func (s *Server) ecrivainDe(a appelant) (ecrivain, error) {
	if a.porteur == nil {
		return s.ecrivainWeb(a.user), nil
	}
	filtre, err := s.DB.FiltreMCP(a.porteur)
	if err != nil {
		return ecrivain{}, err
	}
	return ecrivainMCP(a.porteur, filtre), nil
}

// ecrivainMCP : l'appelant d'un outil MCP. Le plafond du jeton est déjà dans
// `filtre`, qui vient de `db.FiltreMCP`.
func ecrivainMCP(porteur *db.PorteurMCP, filtre func(string) perms.Level) ecrivain {
	return ecrivain{
		userID:             porteur.UserID(),
		username:           porteur.Username(),
		niveau:             filtre,
		appropriationSkill: false,
	}
}

// refusEcriture : le verdict d'une écriture refusée, avec le code HTTP que la
// porte API rendrait. La porte MCP le traduit en `isError`.
type refusEcriture struct {
	Status  int
	Message string
}

func (r refusEcriture) Error() string { return r.Message }

// ecrisFichier : LE corps de l'écriture, pour les deux portes.
//
// Rend le résultat de la fusion, ou un `refusEcriture` portant le code que
// l'API rendrait. L'ordre des contrôles est celui de l'ancien `handlePutFile`,
// à la ligne près - il a ses raisons, écrites au fil du code.
func (s *Server) ecrisFichier(e ecrivain, p, contenu, baseOID, empreinteAttendue string) (writeResponse, error) {
	// Tout fichier vit dans un espace au nom valide. Contrôlé AVANT les droits :
	// c'est la forme du chemin qui est en cause, pas le périmètre, et un chemin
	// accepté ici mais orphelin serait stocké puis synchronisé nulle part.
	if err := espaces.ValidChemin(p); err != nil {
		return writeResponse{}, refusEcriture{http.StatusBadRequest, err.Error()}
	}

	// Appropriation d'un skill : la PREMIÈRE écriture sous shared/skills/<slug>/
	// (aucun fichier du skill n'existe encore dans le dépôt) est une CRÉATION,
	// autorisée pour tout compte authentifié malgré le « shared/skills = invisible »
	// par défaut. La revendication est atomique (claim + grant écriture dans une
	// transaction) et se fait AVANT l'écriture : le gagnant écrit en propriétaire,
	// un créateur concurrent perdant retombe sur les droits normaux (donc refusé,
	// il n'écrit pas). Un skill déjà existant (fichiers présents dans git) n'est
	// jamais « créable » : on ne le revendique pas, ses écritures suivent les
	// droits normaux - un compte tiers ne peut pas s'en emparer.
	slug := skillSlug(p)
	creation := false
	if e.appropriationSkill && slug != "" && !s.Store.Exists(skills.DefaultRoot+"/"+slug) {
		claimed, err := s.DB.ClaimSkill(slug, skills.DefaultRoot+"/"+slug, e.userID)
		if err != nil {
			return writeResponse{}, refusEcriture{http.StatusInternalServerError, "erreur interne"}
		}
		creation = claimed
	}
	if !creation {
		if e.niveau(p) < perms.Lecture {
			return writeResponse{}, refusEcriture{http.StatusNotFound, "introuvable"}
		}
		if e.niveau(p) < perms.Ecriture {
			return writeResponse{}, refusEcriture{http.StatusForbidden, "écriture non autorisée"}
		}
	}

	// Création d'un espace par écriture : refuser un homonyme de casse
	// différente. APFS et HFS+ étant insensibles à la casse, « clients » et
	// « Clients » atterriraient dans un seul dossier local ; la sync verrait
	// alors les fichiers de l'un comme supprimés et ceux de l'autre comme
	// nouveaux, et déplacerait le contenu d'un périmètre de droits vers l'autre.
	// Le parcours ne coûte que sur le premier fichier d'un espace.
	if nom, _, _ := strings.Cut(p, "/"); !s.Store.Exists(nom) {
		occupation, err := espaces.Occupation(s.Store, nom)
		if err != nil {
			return writeResponse{}, refusEcriture{http.StatusInternalServerError, "erreur interne"}
		}
		if occupation != espaces.Libre {
			return writeResponse{}, refusEcriture{http.StatusBadRequest,
				"un espace ou un fichier porte déjà ce nom avec une casse différente : " + nom}
		}
	}

	res, err := s.Store.WriteMergeGardee(p, contenu, e.username, baseOID,
		conflictName(p, e.username, s.now()), empreinteAttendue)
	if err != nil {
		return writeResponse{}, refusEcriture{http.StatusBadRequest, "écriture refusée"}
	}
	// La copie de conflit naît dans le dossier de l'original : couverte par la
	// même règle quand le droit vient d'un dossier ancêtre. Limite connue : si
	// le droit du client vient d'une règle posée sur le fichier précis, la
	// copie n'est pas couverte et peut sortir de son périmètre (elle reste
	// visible de l'admin, rien n'est perdu).
	return writeResponse{
		Head: res.Head, Merged: res.Merged,
		Conflict: res.Conflict, ConflictPath: res.ConflictPath,
	}, nil
}

// supprimeFichier : LE corps de la suppression, pour les deux portes.
func (s *Server) supprimeFichier(e ecrivain, p, baseOID string) (writeResponse, error) {
	// La même garde que l'écriture, et pour la même raison : depuis que la
	// suppression range la version de main dans une copie, ce chemin ÉCRIT. Sans
	// ce contrôle il pourrait déposer une copie sur un chemin que l'écriture
	// refuse - hors espace, donc hors de tout dossier local, donc une copie que
	// la sync ne peut ni descendre ni effacer.
	if err := espaces.ValidChemin(p); err != nil {
		return writeResponse{}, refusEcriture{http.StatusBadRequest, err.Error()}
	}
	if e.niveau(p) < perms.Lecture {
		return writeResponse{}, refusEcriture{http.StatusNotFound, "introuvable"}
	}
	if e.niveau(p) < perms.Ecriture {
		return writeResponse{}, refusEcriture{http.StatusForbidden, "écriture non autorisée"}
	}
	res, err := s.Store.DeleteMerge(p, e.username, baseOID, conflictName(p, e.username, s.now()))
	if err != nil {
		return writeResponse{}, refusEcriture{http.StatusBadRequest, "suppression refusée"}
	}
	return writeResponse{
		Head: res.Head, Merged: res.Merged,
		Conflict: res.Conflict, ConflictPath: res.ConflictPath,
	}, nil
}

// statutDe rend le code HTTP d'un refus d'écriture, ou 500 si l'erreur n'en est
// pas un (ce qui ne devrait pas arriver, et qu'on ne veut pas taire).
func statutDe(err error) (int, string) {
	var refus refusEcriture
	if errors.As(err, &refus) {
		return refus.Status, refus.Message
	}
	return http.StatusInternalServerError, "erreur interne"
}
