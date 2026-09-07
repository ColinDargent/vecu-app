package db

// postes.go : ce qu'un poste dit de lui-même.
//
// LE SERVEUR N'A AUCUNE POSITION PAR POSTE, et c'est le trou que ce fichier
// ferme. `device_tokens` porte trois colonnes - le hash, le compte, le libellé -
// et rien qui dise quand ce poste a parlé pour la dernière fois ni où il en est.
// Le mainteneur ne pouvait donc pas répondre à « est-ce que ce fichier est bien
// arrivé chez tout le monde », qui est le critère de DAR-198 mot pour mot.
//
// DEUX HORLOGES, ET UNE SEULE FAIT FOI. `cycle` est l'heure que le POSTE
// déclare ; `recu_le` est celle que le SERVEUR constate. Tout ce qui se compte
// en « sans nouvelles depuis » se compte sur `recu_le` : un poste dont l'horloge
// dérive rendrait la colonne absurde, et c'est un état qu'on ne contrôle pas.
// `cycle` reste stocké parce qu'un écart entre les deux est lui-même un
// renseignement - mais il ne décide de rien.
//
// LES EXCEPTIONS SONT UN BLOC JSON, délibérément. On charge toujours l'état
// complet d'un poste pour l'afficher, jamais une ligne isolée : trois tables
// normalisées coûteraient trois jointures pour zéro requête qu'on veuille
// écrire. Le jour où l'on voudra « tous les postes qui ont laissé ce chemin »,
// ce sera le moment de normaliser, pas avant.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// schemaPostes : migration PUREMENT ADDITIVE - une table neuve, aucune donnée
// transformée, aucune table existante touchée. Même forme que `schemaMCP`.
//
// LA CLE EST LE HASH DU JETON D'APPAREIL, PAS LE COMPTE : un humain a plusieurs
// postes (Colin en a trois en production), et c'est le poste qu'on interroge,
// pas la personne. La cascade suit donc `device_tokens` : révoquer un poste
// efface ce qu'il a dit, ce qui est la bonne sémantique - un poste retiré n'a
// plus d'état à montrer.
//
// ATTENTION : cette cascade ne s'exécute QUE si la connexion active
// `PRAGMA foreign_keys`. Elle est désactivée par défaut et le réglage est PAR
// CONNEXION - `db.go` ouvre bien avec `_pragma=foreign_keys(1)`, mais toute
// inspection faite avec un autre outil ne verra pas les cascades.
// Source: https://www.sqlite.org/foreignkeys.html
const schemaPostes = `
CREATE TABLE IF NOT EXISTS postes_etat (
  token_hash TEXT PRIMARY KEY REFERENCES device_tokens(token_hash) ON DELETE CASCADE,
  tete       TEXT NOT NULL,
  -- Sans DEFAULT, comme mcp_jetons et pour la même raison : le datetime('now')
  -- de SQLite rend « 2026-09-01 06:38:59 » là où ce paquet écrit du RFC3339.
  -- Deux formats dans une colonne donnent un tri et une comparaison faux.
  cycle      TEXT NOT NULL,
  recu_le    TEXT NOT NULL,
  exceptions TEXT NOT NULL
);`

// LabelSessionWeb : le libellé que le web pose sur le jeton d'une session de
// navigateur (`handleLogin` et `handleEntrer`).
//
// CE N'EST PAS UN POSTE, ET C'EST CE QUI FAIT QU'IL DOIT ETRE FILTRE ICI. Une
// session de navigateur ne synchronise rien, ne porte aucun fichier, et ne
// rapportera jamais son état. Sans ce filtre, l'écran du mainteneur affiche une
// colonne « sans nouvelles » de plus A CHAQUE CONNEXION - trouvé en regardant
// le rendu, jamais sorti par un test unitaire. En production, 10 jetons
// d'appareil existent pour deux humains, et une partie sont des sessions web.
//
// Une constante partagée plutôt qu'une chaîne recopiée : les deux sites
// d'émission et le filtre ne peuvent pas diverger sans que le compilateur le
// dise.
const LabelSessionWeb = "web"

// LaissePoste : un chemin que le poste n'a PAS sur son disque, et pourquoi.
// Miroir de `sync.Laisse` côté client, recopié plutôt qu'importé : le serveur
// ne dépend pas du paquet client, et le contrat entre les deux est le JSON.
type LaissePoste struct {
	Chemin string `json:"chemin"`
	Raison string `json:"raison"`
}

// ExceptionsPoste : les trois listes que la tête ne dit pas.
//
// LA TETE MENT PAR CONCEPTION, et ces listes sont ce qui la rattrape. Le
// curseur de lecture DOIT avancer même quand une écriture a été refusée, sinon
// le même delta redescend indéfiniment pour tous les espaces à cause d'un seul
// chemin (voir le commentaire de `Head` dans `client/sync/config.go`). Une tête
// à jour ne prouve donc pas qu'un fichier est sur le disque.
// LES BINAIRES N'Y FIGURENT PAS (Colin, 01/09). Le registre `HorsPerimetre` du
// poste porte des chemins que le serveur n'a jamais eus - un PDF, une photo
// posés dans un espace partagé - et les stocker apprendrait au mainteneur d'un
// client le nom de fichiers personnels. Les deux listes qui restent portent des
// chemins de fichiers que le serveur porte déjà.
type ExceptionsPoste struct {
	// Laisses : présents côté serveur, absents du disque, avec le motif.
	Laisses []LaissePoste `json:"laisses"`
	// ConflitsLocaux : les copies « (conflit local) », qui ne se synchronisent
	// JAMAIS par construction. Ne pas confondre avec les copies de conflit du
	// serveur, « (conflit 2026-09-01 14h30 - colin) », qui sont commitées donc
	// déjà visibles. Deux objets, un seul mot.
	ConflitsLocaux []string `json:"conflits_locaux"`
}

// EtatPoste : l'état d'un poste tel qu'il se lit à l'écran.
type EtatPoste struct {
	TokenHash string
	UserID    int64
	Libelle   string
	Tete      string
	// Cycle : l'heure déclarée par le poste. Indicative, ne décide de rien.
	Cycle string
	// RecuLe : l'heure constatée par le serveur. C'est elle qui fait foi.
	RecuLe string
	Exceptions ExceptionsPoste
}

// EnregistreEtatPoste : le poste a parlé. Un seul état courant par poste, donc
// un remplacement et pas un historique - « où en est ce poste » est une
// question au présent, et garder les cycles passés ferait grossir la base d'une
// ligne par poste et par cycle sans que rien ne les lise.
//
// `recu` est passé plutôt que pris ici pour que le test puisse figer l'horloge.
func (d *DB) EnregistreEtatPoste(tokenHash, tete, cycle string, exc ExceptionsPoste, recu time.Time) error {
	blob, err := json.Marshal(exc)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`
		INSERT INTO postes_etat (token_hash, tete, cycle, recu_le, exceptions)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET
		  tete = excluded.tete, cycle = excluded.cycle,
		  recu_le = excluded.recu_le, exceptions = excluded.exceptions`,
		tokenHash, tete, cycle, recu.UTC().Format(time.RFC3339), string(blob))
	return err
}

// EtatsPostes : tous les postes CONNUS, qu'ils aient parlé ou non.
//
// LE JOINTURE PART DE `device_tokens`, PAS DE `postes_etat`, et c'est le coeur
// du contrat de l'écran. Un poste qui n'a jamais parlé DOIT ressortir, avec un
// `RecuLe` vide - sinon il disparaît de la liste et son silence se lit comme
// une absence de problème. Un poste muet et un poste sain rendraient la même
// page, ce qui est exactement le défaut que DAR-198 doit fermer.
func (d *DB) EtatsPostes() ([]EtatPoste, error) {
	rows, err := d.sql.Query(`
		SELECT t.token_hash, t.user_id, t.label,
		       COALESCE(e.tete, ''), COALESCE(e.cycle, ''),
		       COALESCE(e.recu_le, ''), COALESCE(e.exceptions, '')
		FROM device_tokens t
		LEFT JOIN postes_etat e ON e.token_hash = t.token_hash
		WHERE t.label <> ?
		ORDER BY t.user_id, t.label, t.token_hash`, LabelSessionWeb)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EtatPoste
	for rows.Next() {
		var p EtatPoste
		var blob string
		if err := rows.Scan(&p.TokenHash, &p.UserID, &p.Libelle,
			&p.Tete, &p.Cycle, &p.RecuLe, &blob); err != nil {
			return nil, err
		}
		// Un blob illisible ne fait pas tomber la page : le poste retombe sur
		// « sans nouvelles », qui est la lecture prudente. Perdre l'écran entier
		// parce qu'un poste a envoyé du JSON abîmé serait le mauvais échange.
		if blob != "" {
			if err := json.Unmarshal([]byte(blob), &p.Exceptions); err != nil {
				p.RecuLe = ""
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AParle : ce poste a-t-il déjà dit quelque chose ?
//
// LA QUESTION QUE L'ECRAN DOIT POSER AVANT D'AFFICHER QUOI QUE CE SOIT DE VERT.
// Sans elle, un poste muet rend zéro exception, ce qui se lit « tout est
// arrivé » alors que la lecture juste est « je n'en sais rien ».
func (p EtatPoste) AParle() bool { return p.RecuLe != "" }

// ErrPosteInconnu : le hash présenté n'est pas un jeton d'appareil.
var ErrPosteInconnu = errors.New("poste inconnu")

// EtatPosteParHash : l'état d'un seul poste. `sql.ErrNoRows` devient
// ErrPosteInconnu pour que l'appelant distingue « pas de jeton » de « erreur ».
func (d *DB) EtatPosteParHash(tokenHash string) (*EtatPoste, error) {
	var p EtatPoste
	var blob string
	err := d.sql.QueryRow(`
		SELECT t.token_hash, t.user_id, t.label,
		       COALESCE(e.tete, ''), COALESCE(e.cycle, ''),
		       COALESCE(e.recu_le, ''), COALESCE(e.exceptions, '')
		FROM device_tokens t
		LEFT JOIN postes_etat e ON e.token_hash = t.token_hash
		WHERE t.token_hash = ?`, tokenHash).
		Scan(&p.TokenHash, &p.UserID, &p.Libelle, &p.Tete, &p.Cycle, &p.RecuLe, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPosteInconnu
	}
	if err != nil {
		return nil, err
	}
	if blob != "" {
		if err := json.Unmarshal([]byte(blob), &p.Exceptions); err != nil {
			p.RecuLe = ""
		}
	}
	return &p, nil
}
