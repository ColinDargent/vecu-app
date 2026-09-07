package db

// mcp.go : les jetons de la surface MCP.
//
// Même discipline que `device_tokens` : 32 octets aléatoires, SHA-256 stocké,
// le clair n'existe qu'une fois - au retour de `IssueMCPToken`. Ce qui change,
// c'est le PLAFOND : un jeton porte un niveau maximum, et le droit effectif
// d'un appel est le plus restrictif de ce plafond et du droit du compte. Un
// jeton ne peut donc que soustraire, jamais étendre.
//
// C'est cette propriété qui rend défendable un jeton posé dans une routine
// cloud : la fuite d'un jeton en lecture seule ne donne pas l'écriture, même
// si le compte porteur l'a.

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/colindargent/vecu/server/auth"
	"github.com/colindargent/vecu/server/perms"
)

// schemaMCP : migration PUREMENT ADDITIVE - une table neuve, aucune donnée
// transformée, aucune table existante touchée. C'est ce qui la distingue de la
// migration des cohortes du 25/08 et ce qui autorise à la pousser sans la
// cérémonie de vérification que DAR-182 exige.
const schemaMCP = `
CREATE TABLE IF NOT EXISTS mcp_jetons (
  -- AUTOINCREMENT comme users et groupes, et pas seulement par
  -- convention : sans lui, SQLite REUTILISE le rowid d'une ligne supprimee.
  -- La cascade sur la suppression d'un compte rendrait alors l'id d'un jeton
  -- mort a un jeton neuf, et toute trace d'audit confondrait les deux.
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  hash          TEXT NOT NULL UNIQUE,
  libelle       TEXT NOT NULL,
  niveau_max    TEXT NOT NULL,
  -- Sans DEFAULT, deliberement. Le datetime('now') de SQLite rend
  -- « 2026-08-31 06:38:59 » la ou ce paquet ecrit du RFC3339 : deux formats
  -- dans une seule colonne, donc un
  -- tri et une comparaison faux. Un seul ecrivain, un seul format, et tout
  -- autre inserteur echoue bruyamment au lieu d'ecrire de travers.
  cree_le       TEXT NOT NULL,
  dernier_usage TEXT,
  revoque_le    TEXT
);`

// JetonMCP : un jeton tel qu'il se lit dans l'écran d'admin. Le clair n'y est
// jamais - il n'existe qu'au retour de `IssueMCPToken`.
type JetonMCP struct {
	ID           int64
	UserID       int64
	Libelle      string
	NiveauMax    perms.Level
	CreeLe       string
	DernierUsage string // vide = jamais servi
	RevoqueLe    string // vide = actif
}

// PorteurMCP : ce qu'un jeton valide donne au moment d'un appel.
//
// LE COMPTE EST NON EXPORTÉ, et c'est tout l'intérêt du type. Tant qu'il était
// public, `perms.CanWrite(chemin, porteur.User.DefaultLevel, rules)` compilait -
// et c'est l'idiome de la maison, écrit à sept endroits d'`api.go`. Un auteur
// d'outil qui copie le style existant obtenait la réponse NON PLAFONNÉE, sans
// qu'aucun test ni aucune revue ne le signale. Le plafond vivait dans un
// commentaire, donc il ne s'appliquait pas.
//
// Fermé par la structure plutôt que par la consigne : le seul chemin vers un
// droit est `DB.EffectifMCP` et ses deux dérivées, parce qu'il n'y a plus rien
// d'autre à atteindre depuis l'extérieur du paquet.
type PorteurMCP struct {
	JetonID int64
	// Libelle : le nom que l'humain a donné au jeton (« meeting-processor »,
	// « claude code du mac »). Chargé ici parce que c'est la seule chose qui
	// rende une trace d'écriture lisible après coup : « le jeton 3 a écrit » ne
	// dit rien, « meeting-processor a écrit » dit tout.
	Libelle   string
	NiveauMax perms.Level
	user      *User
}

// UserID / Username : ce dont les outils ont réellement besoin du compte -
// l'identité pour l'auteur d'un commit, jamais le niveau de droit.
func (p *PorteurMCP) UserID() int64    { return p.user.ID }
func (p *PorteurMCP) Username() string { return p.user.Username }

// Compte rend le compte porteur, pour les handlers de l'API qui en ont besoin
// tel quel (identité, journal). Le NIVEAU de ce compte ne doit jamais servir
// directement : le droit passe par `DB.EffectifMCP` ou `DB.FiltreMCP`, seuls
// endroits où le plafond s'applique.
func (p *PorteurMCP) Compte() *User { return p.user }

// IssueMCPToken crée un jeton pour ce compte et rend le CLAIR, une seule fois.
//
// `niveauMax` ne peut valoir que `lecture` ou `ecriture` : un jeton `invisible`
// serait un jeton qui ne peut rien, donc un réglage sans usage qu'il faudrait
// ensuite expliquer dans l'interface.
func (d *DB) IssueMCPToken(userID int64, libelle string, niveauMax perms.Level) (string, error) {
	if niveauMax != perms.Lecture && niveauMax != perms.Ecriture {
		return "", errors.New("plafond de jeton invalide : lecture ou ecriture")
	}
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if _, err := d.sql.Exec(
		`INSERT INTO mcp_jetons (user_id, hash, libelle, niveau_max, cree_le) VALUES (?, ?, ?, ?, ?)`,
		userID, auth.HashToken(token), libelle, niveauMax.String(),
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return "", err
	}
	return token, nil
}

// PorteurParJetonMCP résout un jeton porteur vers son compte et son plafond.
//
// Un jeton RÉVOQUÉ est introuvable, au même titre qu'un jeton inconnu : la
// ligne survit pour l'écran d'admin (on veut pouvoir dire « ce jeton a été
// révoqué le … »), mais elle ne donne plus rien. C'est le `revoque_le IS NULL`
// qui porte la révocation, et il vit dans la MÊME requête que la résolution -
// un contrôle posé ailleurs finirait par être oublié sur un chemin d'appel.
func (d *DB) PorteurParJetonMCP(token string) (*PorteurMCP, error) {
	var (
		id      int64
		uid     int64
		niveau  string
		libelle string
	)
	err := d.sql.QueryRow(
		`SELECT id, user_id, niveau_max, libelle FROM mcp_jetons WHERE hash = ? AND revoque_le IS NULL`,
		auth.HashToken(token)).Scan(&id, &uid, &niveau, &libelle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	max, ok := perms.ParseLevel(niveau)
	if !ok {
		// Une valeur que le code ne sait plus lire ne se replie pas sur un
		// niveau permissif. Fail-closed : le jeton ne donne rien.
		return nil, ErrNotFound
	}
	u, err := d.userByID(uid)
	if err != nil {
		return nil, err
	}
	return &PorteurMCP{JetonID: id, Libelle: libelle, NiveauMax: max, user: u}, nil
}

// ToucheJetonMCP avance `dernier_usage`.
//
// C'est la seule colonne qui sert APRÈS coup : elle rend repérable un jeton
// oublié dans un fichier de configuration. Sans elle, un jeton distribué puis
// abandonné est indistinguable d'un jeton qui travaille.
func (d *DB) ToucheJetonMCP(id int64) error {
	_, err := d.sql.Exec(`UPDATE mcp_jetons SET dernier_usage = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// ListJetonsMCP rend tous les jetons d'un compte, révoqués compris, du plus
// récent au plus ancien.
func (d *DB) ListJetonsMCP(userID int64) ([]JetonMCP, error) {
	rows, err := d.sql.Query(
		`SELECT id, user_id, libelle, niveau_max, cree_le,
		        COALESCE(dernier_usage, ''), COALESCE(revoque_le, '')
		   FROM mcp_jetons WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JetonMCP
	for rows.Next() {
		var j JetonMCP
		var niveau string
		if err := rows.Scan(&j.ID, &j.UserID, &j.Libelle, &niveau, &j.CreeLe,
			&j.DernierUsage, &j.RevoqueLe); err != nil {
			return nil, err
		}
		max, ok := perms.ParseLevel(niveau)
		if !ok {
			// Fail-LOUD, et c'est le pendant du fail-closed de
			// `PorteurParJetonMCP`. Se replier sur `invisible` afficherait
			// « ce jeton ne peut rien » - rassurant, faux, et justement la
			// valeur qu'`IssueMCPToken` refuse de creer. Deux surfaces qui
			// racontent deux histoires sur la meme ligne corrompue, c'est
			// l'ecran d'admin qui ment.
			return nil, fmt.Errorf("jeton MCP %d : niveau_max illisible en base (%q)", j.ID, niveau)
		}
		j.NiveauMax = max
		out = append(out, j)
	}
	return out, rows.Err()
}

// RevoqueJetonMCPDuCompte coupe un jeton APPARTENANT à ce compte.
//
// LE PÉRIMÈTRE EST DANS LA REQUÊTE, et c'est un correctif. La première version
// de l'écran vérifiait la propriété en parcourant `ListJetonsMCP`, au motif que
// « on ne peut pas révoquer ce qu'on ne voit pas ». Ça faisait dépendre
// l'autorisation de la santé de l'AFFICHAGE : un seul `niveau_max` illisible
// fait échouer la liste entière (fail-loud, voulu), et l'écran ne pouvait alors
// plus couper AUCUN jeton du compte - y compris les jetons sains, qui eux
// continuaient de fonctionner. Le coupe-circuit tombait en panne à cause d'une
// ligne voisine.
//
// Ici, la même propriété d'autorisation tient sans lire le reste : un `id` qui
// n'appartient pas à ce compte ne correspond simplement à aucune ligne.
func (d *DB) RevoqueJetonMCPDuCompte(userID, id int64) error {
	res, err := d.sql.Exec(
		`UPDATE mcp_jetons SET revoque_le = ? WHERE id = ? AND user_id = ? AND revoque_le IS NULL`,
		time.Now().UTC().Format(time.RFC3339), id, userID)
	if err != nil {
		return err
	}
	// Zero ligne touchee veut dire deux choses tres differentes : deja revoque
	// (idempotent, et c'est voulu) ou id inconnu (une faute de frappe dans
	// l'ecran d'admin, qui afficherait « coupe » sur un jeton qui ne l'est
	// pas). On les separe, comme `SetDefaultLevel` le fait deja sur le meme
	// motif.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var existe int
		if err := d.sql.QueryRow(
			`SELECT COUNT(*) FROM mcp_jetons WHERE id = ? AND user_id = ?`, id, userID).Scan(&existe); err != nil {
			return err
		}
		if existe == 0 {
			// Inconnu ET « appartient à quelqu'un d'autre » rendent le MÊME
			// refus : les distinguer dirait à qui essaie que le jeton existe
			// ailleurs.
			return ErrNotFound
		}
	}
	return nil
}

// FiltreMCP rend le calculateur de droit de ce jeton, ses règles chargées UNE
// SEULE FOIS.
//
// Existe pour l'arborescence et la recherche, qui posent la question sur des
// centaines de chemins : `EffectifMCP` recharge les règles à chaque appel, ce
// qui ferait une requête SQL par fichier du vault sur une base à
// `SetMaxOpenConns(1)`.
//
// La fermeture porte le MÊME calcul que `EffectifMCP` - c'est elle que
// `EffectifMCP` appelle - donc il n'y a toujours qu'un seul endroit où le
// plafond s'applique. Deux entrées, une seule règle.
func (d *DB) FiltreMCP(p *PorteurMCP) (func(chemin string) perms.Level, error) {
	rules, err := d.Rules(p.user.ID)
	if err != nil {
		return nil, err
	}
	defaut := p.user.DefaultLevel
	plafond := p.NiveauMax
	return func(chemin string) perms.Level {
		niveau := perms.Effective(chemin, defaut, rules)
		if plafond < niveau {
			return plafond
		}
		return niveau
	}, nil
}

// EffectifMCP : le droit de ce jeton sur ce chemin. LA fonction, pas une parmi
// d'autres.
//
// Elle charge SES PROPRES règles, comme `DB.Effective` le fait déjà pour un
// humain. La première version les recevait en paramètre : rien ne les
// rapprochait du compte porteur, donc `Effectif(chemin, reglesDUnAutreCompte)`
// rendait une réponse silencieusement fausse. Le dépôt avait déjà la forme
// sûre ; il n'y avait aucune raison d'en introduire une fragile.
//
// `min` des deux niveaux, donc un jeton ne peut QUE soustraire. Aucune valeur
// de `NiveauMax` n'étend les droits du compte porteur, y compris pour un
// compte admin : `IsAdmin` n'entre pas dans ce calcul, et c'est délibéré - un
// jeton MCP ne peut rien qu'un humain ne pourrait.
func (d *DB) EffectifMCP(p *PorteurMCP, chemin string) (perms.Level, error) {
	filtre, err := d.FiltreMCP(p)
	if err != nil {
		return perms.Invisible, err
	}
	return filtre(chemin), nil
}

// PeutLireMCP / PeutEcrireMCP : les deux seules questions que les outils posent.
func (d *DB) PeutLireMCP(p *PorteurMCP, chemin string) (bool, error) {
	n, err := d.EffectifMCP(p, chemin)
	return n >= perms.Lecture, err
}

func (d *DB) PeutEcrireMCP(p *PorteurMCP, chemin string) (bool, error) {
	n, err := d.EffectifMCP(p, chemin)
	return n >= perms.Ecriture, err
}
