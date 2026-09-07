// Package db : persistance SQLite (users, tokens d'appareil, permissions).
// SQLite pur Go via modernc.org/sqlite (zéro CGO).
// Source: https://pkg.go.dev/modernc.org/sqlite
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/colindargent/vecu/server/auth"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
)

// DB enveloppe la connexion SQLite et le schéma Vécu.
type DB struct {
	sql *sql.DB
}

// User : un compte du back office.
type User struct {
	ID           int64
	Username     string
	DefaultLevel perms.Level
	IsAdmin      bool
}

var ErrNotFound = errors.New("introuvable")

// Origine d'une règle de `permissions`. La table ne contient pas que des
// exceptions voulues : `CreateUser` y pose `shared/skills = invisible` sur
// chaque compte. Distinguer les deux est ce qui permet à une cohorte d'ouvrir un
// dossier qu'un défaut automatique ferme (Q2, tranchée le 25/08).
const (
	OrigineException = "exception" // quelqu'un a décidé ce niveau pour ce compte
	OrigineDefaut    = "defaut"    // posé automatiquement à la création du compte
)

// Open ouvre (ou crée) la base au chemin donné et applique le schéma.
// dsn "file:...?..." ou ":memory:" pour les tests.
func Open(path string) (*DB, error) {
	// _pragma=busy_timeout : évite les "database is locked" transitoires.
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite n'aime pas les écritures concurrentes : une seule connexion.
	sqlDB.SetMaxOpenConns(1)
	d := &DB{sql: sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() error { return d.sql.Close() }

func (d *DB) migrate() error {
	_, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  default_level TEXT NOT NULL DEFAULT 'invisible',
  is_admin      INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS device_tokens (
  token_hash TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  label      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS permissions (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path    TEXT NOT NULL,
  level   TEXT NOT NULL,
  origine TEXT NOT NULL DEFAULT 'exception',
  PRIMARY KEY (user_id, path)
);
CREATE TABLE IF NOT EXISTS skill_creators (
  slug       TEXT PRIMARY KEY,
  user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
-- Le defaut porte par le DOSSIER, et non par un compte. C'est lui qui ferme :
-- une cohorte ouvre et n'a pas a fermer, donc il fallait autre chose que N
-- regles individuelles rejouees a chaque embauche pour proteger un dossier
-- sensible. Une ligne ici vaut pour tout le monde, y compris pour les comptes
-- qui n'existent pas encore.
CREATE TABLE IF NOT EXISTS dossier_defauts (
  chemin TEXT PRIMARY KEY,
  level  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (
  cle    TEXT PRIMARY KEY,
  faitLe TEXT NOT NULL DEFAULT (datetime('now'))
);`)
	if err != nil {
		return err
	}
	// Les tables de groupes vivent à part parce qu'elles ont un renommage à
	// jouer AVANT leur création. Dans l'autre ordre, `CREATE TABLE IF NOT
	// EXISTS groupes` fabriquerait une table vide à côté de `skill_groupes`
	// sur une base d'avant le 25/08, et le renommage échouerait sur un nom
	// déjà pris - en laissant les droits dans l'ancienne table, invisibles.
	if err := d.migreOrigine(); err != nil {
		return err
	}
	if err := d.migreGroupes(); err != nil {
		return err
	}
	if _, err := d.sql.Exec(schemaGroupes); err != nil {
		return err
	}
	// APRES la creation du schema : `CREATE TABLE IF NOT EXISTS` ne touche pas
	// une table deja la, donc une base d'avant le 01/09 n'aurait jamais la
	// colonne ; et sur une base neuve la table n'existe qu'ici.
	if err := d.migreNiveauDeChemin(); err != nil {
		return err
	}
	// APRES `migreNiveauDeChemin` : la scission des cohortes mixtes déplace des
	// lignes de `groupe_chemins`, et elle doit les déplacer AVEC leur niveau.
	if err := d.migreGenreDeGroupe(); err != nil {
		return err
	}
	// Additive et sans dépendance : la table des jetons MCP ne référence que
	// `users`, déjà créée plus haut. Aucun renommage, aucune donnée transformée.
	if _, err := d.sql.Exec(schemaMCP); err != nil {
		return err
	}
	// Additive et sans dépendance elle aussi : `postes_etat` ne référence que
	// `device_tokens`, créée tout en haut. Voir `postes.go`.
	_, err = d.sql.Exec(schemaPostes)
	return err
}

// colonneExiste : la colonne `col` est-elle à la table `table` ?
func (d *DB) colonneExiste(table, col string) (bool, error) {
	rows, err := d.sql.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if n == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migreOrigine ajoute `permissions.origine` et marque l'existant (25/08, Q2).
//
// `CREATE TABLE IF NOT EXISTS` ne touche pas une table déjà là : sur une base
// d'avant, la colonne doit être ajoutée à part. Garde par l'existence de la
// colonne, comme migreGroupes se garde par l'existence d'une table.
//
// LE MARQUAGE EST ÉTROIT, ET C'EST DÉLIBÉRÉ. Seules les lignes qui portent la
// signature EXACTE du défaut automatique - le chemin des skills, au niveau
// invisible - passent en `defaut`. Un niveau lecture ou écriture posé là est
// forcément le geste de quelqu'un, il reste une exception. Un `invisible` posé
// à la main y est indiscernable du défaut, et referme de toute façon la même
// chose : le seul écart possible est qu'une cohorte puisse désormais le rouvrir,
// ce qui est précisément ce que Q2 a voulu.
// migreNiveauDeChemin ajoute `groupe_chemins.level` (01/09, DAR-196).
//
// CE QU'ELLE DEBLOQUE. Une cohorte portait des chemins SANS niveau, le niveau
// vivant sur le membre : elle appliquait donc un seul niveau a tous ses
// chemins, et ne savait pas dire « lecture sur shared/, invisible sur
// shared/direction ». Un utilisateur, lui, sait le dire depuis toujours. C'est
// cette asymetrie que la colonne supprime.
//
// LE DEFAUT VIDE EST LE COMPORTEMENT D'AVANT : un chemin sans niveau prend
// celui du membre, exactement comme avant la colonne. La migration ne peut donc
// changer le droit de personne, et c'est ce qui la rend jouable sur une base de
// production sans fenetre d'arret.
//
// Pas de contrainte CHECK sur la colonne, et c'est mesure : « When adding a
// column with a CHECK constraint [...] the added constraints are tested against
// all preexisting rows and the ADD COLUMN fails if any constraint fails. » Une
// valeur invalide en base est deja refusee a l'ecriture par ParseLevel.
// Source: https://www.sqlite.org/lang_altertable.html#altertabaddcol
func (d *DB) migreNiveauDeChemin() error {
	existe, err := d.colonneExiste("groupe_chemins", "level")
	if err != nil || existe {
		return err
	}
	if _, err := d.sql.Exec(
		`ALTER TABLE groupe_chemins ADD COLUMN level TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migration du niveau par chemin de cohorte : %w", err)
	}
	return nil
}

func (d *DB) migreOrigine() error {
	existe, err := d.colonneExiste("permissions", "origine")
	if err != nil || existe {
		return err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`ALTER TABLE permissions ADD COLUMN origine TEXT NOT NULL DEFAULT 'exception'`); err != nil {
		return fmt.Errorf("migration de l'origine des règles : %w", err)
	}
	res, err := tx.Exec(
		`UPDATE permissions SET origine = ? WHERE path = ? AND level = ?`,
		OrigineDefaut, perms.Canon(skills.DefaultRoot), perms.Invisible.String())
	if err != nil {
		return fmt.Errorf("marquage des défauts automatiques : %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	log.Printf("migration : colonne `origine` ajoutée à permissions, %d défaut(s) automatique(s) marqué(s)", n)
	return nil
}

// schemaGroupeChemins : le DDL de `groupe_chemins`, isolé parce qu'il sert à
// DEUX endroits - la création sur une base neuve et la migration d'une base
// d'avant. Deux copies divergeraient au premier changement de colonne.
//
// La clé primaire est (groupe_id, chemin), et c'est le cœur des cohortes : un
// chemin vit dans PLUSIEURS groupes. Jusqu'au 25/08 c'était `slug` seul, donc
// un skill dans au plus un groupe (décision du 21/08, renversée : un skill sert
// les opérations ET le marketing, comme un dossier sert les salariés ET les
// managers).
//
// `chemin` est stocké plutôt que recalculé, comme `skill_creators` le fait pour
// son grantPath : la base reste ignorante de l'endroit où vivent les skills, et
// déplacer cette racine un jour ne demanderait pas de la faire changer d'avis
// sur des droits déjà posés.
//
// `genre` commande le RENDU (quel écran affiche cette ligne) et jamais la
// résolution - `reglesDeGroupe` ne lit que (chemin, level). `slug` porte
// l'identité du skill pour l'écran, et le CHECK interdit en base que les deux
// colonnes se contredisent : sans lui, un dossier porteur d'un slug ou un skill
// sans slug seraient acceptés et casseraient le rendu bien plus loin.
// Source: https://www.sqlite.org/lang_createtable.html#check_constraints
const schemaGroupeChemins = `
CREATE TABLE IF NOT EXISTS groupe_chemins (
  groupe_id INTEGER NOT NULL REFERENCES groupes(id) ON DELETE CASCADE,
  chemin    TEXT NOT NULL,
  genre     TEXT NOT NULL,
  slug      TEXT NOT NULL DEFAULT '',
  -- level : le niveau que la cohorte accorde SUR CE CHEMIN. Vide = le chemin
  -- prend le niveau du membre, qui etait le seul comportement avant le 01/09.
  -- C'est ce qui permet a une cohorte de dire « lecture sur shared/, invisible
  -- sur shared/direction », ce qu'un utilisateur savait deja dire.
  level     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (groupe_id, chemin),
  CHECK ((genre = 'skill' AND slug <> '') OR (genre = 'dossier' AND slug = ''))
);
-- La cle primaire est (groupe_id, chemin), donc une recherche PAR CHEMIN ne
-- peut pas s'appuyer dessus : chemin n'en est pas la colonne de tete, et
-- SQLite balaie. Or GroupesDuChemin est appele UNE FOIS PAR SKILL dans la
-- boucle de rendu de l'ecran des skills - sans cet index, l'ecran est
-- quadratique. Mesure a l'EXPLAIN QUERY PLAN : SCAN sans lui, SEARCH avec.
CREATE INDEX IF NOT EXISTS idx_groupe_chemins_chemin ON groupe_chemins(chemin);`

// schemaGroupes : les trois tables d'une cohorte. Un groupe porte un paquet de
// chemins (`groupe_chemins`) et des comptes avec leur niveau (`groupe_membres`).
const schemaGroupes = `
CREATE TABLE IF NOT EXISTS groupes (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  nom  TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS groupe_membres (
  groupe_id INTEGER NOT NULL REFERENCES groupes(id) ON DELETE CASCADE,
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  level     TEXT NOT NULL,
  PRIMARY KEY (groupe_id, user_id)
);` + schemaGroupeChemins

// tableExiste : la table `nom` est-elle au schéma ?
func (d *DB) tableExiste(nom string) (bool, error) {
	var un int
	err := d.sql.QueryRow(
		`SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?`, nom).Scan(&un)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// migreGroupes : généralise les groupes de skills en cohortes (25/08).
//
// LE GARDE D'IDEMPOTENCE EST L'EXISTENCE DE `skill_groupes`, pas un marqueur
// dans `meta` : un marqueur peut être posé sur une migration qui a échoué à
// mi-parcours, l'état du schéma dit toujours la vérité.
//
// MAIS IL Y A TROIS ÉTATS, PAS DEUX, et le troisième est celui qu'un rollback
// de déploiement fabrique. Le binaire d'avant rejoue son `CREATE TABLE IF NOT
// EXISTS skill_groupes`, qui refabrique les trois anciennes tables VIDES à côté
// des nouvelles - et le tourne sur des droits de groupe à zéro, sans un message
// nulle part. Au redéploiement, `ALTER TABLE skill_groupes RENAME TO groupes`
// meurt sur « there is already another table named groupes », `Open` remonte,
// `main` fait log.Fatalf, et le serveur ne redémarre PLUS JAMAIS. C'est
// `resorbeAnciennesTables` qui referme ce piège.
//
// L'ORDRE COMPTE. Le parent d'abord : renommer `skill_groupes` réécrit les
// clauses REFERENCES de ses deux tables filles, automatiquement depuis SQLite
// 3.26 sauf PRAGMA legacy_alter_table=ON (par défaut : OFF).
// Source: https://www.sqlite.org/lang_altertable.html
//
// `groupe_chemins` n'est PAS un renommage : sa clé primaire change, ce qui
// touche le contenu sur disque, donc c'est une création suivie d'une copie et
// d'un DROP. Les noms source et cible diffèrent, ce qui évite le piège de la
// procédure en 12 étapes (renommer l'ancienne d'abord corrompt les références
// des vues et des triggers).
// Source: https://www.sqlite.org/lang_altertable.html#otheralter
func (d *DB) migreGroupes() error {
	ancien, err := d.tableExiste("skill_groupes")
	if err != nil || !ancien {
		return err
	}
	neuf, err := d.tableExiste("groupes")
	if err != nil {
		return err
	}
	if neuf {
		return d.resorbeAnciennesTables()
	}
	// Une base d'avant a forcément les trois tables : elles naissaient du même
	// Exec. Le vérifier plutôt que le supposer donne un message lisible au lieu
	// d'un « no such table » au milieu d'une transaction.
	for _, t := range []string{"skill_groupe_membres", "skill_groupe_skills"} {
		existe, err := d.tableExiste(t)
		if err != nil {
			return err
		}
		if !existe {
			return fmt.Errorf("migration des groupes : `skill_groupes` est là mais `%s` manque - schéma incohérent, migration interrompue plutôt que devinée", t)
		}
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	etapes := []string{
		`ALTER TABLE skill_groupes RENAME TO groupes`,
		`ALTER TABLE skill_groupe_membres RENAME TO groupe_membres`,
		schemaGroupeChemins,
		// LA COPIE EST TOTALE, ET C'EST DÉLIBÉRÉ. La table d'arrivée porte deux
		// contraintes que celle de départ n'avait pas - la clé (groupe_id,
		// chemin) et le CHECK genre/slug - donc un SELECT nu peut mourir sur
		// une ligne parfaitement légale sous l'ancien schéma. Sur une base de
		// droits en production, ce mode de défaillance est terminal : le
		// serveur ne boote plus.
		//
		// GROUP BY absorbe deux slugs différents posés sur le même chemin dans
		// le même groupe. C'est sans perte : `reglesDeGroupe` n'émet que
		// (chemin, niveau), donc les deux lignes produisaient déjà la MÊME
		// règle. max(slug) fait gagner un vrai slug sur une chaîne vide.
		//
		// Un slug vide devient un chemin de genre `dossier`, ce que le CHECK
		// accepte, plutôt que de faire échouer la migration. Là aussi la règle
		// émise est identique : seul le rendu change.
		`INSERT INTO groupe_chemins (groupe_id, chemin, genre, slug)
         SELECT groupe_id, chemin,
                CASE WHEN max(slug) = '' THEN 'dossier' ELSE 'skill' END,
                max(slug)
           FROM skill_groupe_skills
          GROUP BY groupe_id, chemin`,
		`DROP TABLE skill_groupe_skills`,
	}
	var avant, apres int
	if err := tx.QueryRow(`SELECT count(*) FROM skill_groupe_skills`).Scan(&avant); err != nil {
		return fmt.Errorf("migration des groupes : comptage de départ : %w", err)
	}
	for _, q := range etapes {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("migration des groupes : %w (sur %s)", err, resume(q))
		}
		if strings.HasPrefix(strings.TrimSpace(q), "INSERT") {
			if err := tx.QueryRow(`SELECT count(*) FROM groupe_chemins`).Scan(&apres); err != nil {
				return fmt.Errorf("migration des groupes : comptage d'arrivée : %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if apres != avant {
		log.Printf("migration des groupes : %d ligne(s) de départ repliée(s) en %d - des slugs distincts partageaient un chemin dans un même groupe, la règle émise est inchangée", avant, apres)
	}
	log.Print("migration : groupes de skills généralisés en cohortes (groupes, groupe_membres, groupe_chemins)")
	return nil
}

// resorbeAnciennesTables : le cas où les deux générations de tables coexistent.
//
// Un rollback de déploiement laisse le binaire d'avant recréer `skill_groupes`,
// `skill_groupe_membres` et `skill_groupe_skills` à VIDE, à côté des nouvelles.
// Les nouvelles portent la vérité : on efface les anciennes, et le
// redéploiement suivant retrouve un chemin dégagé.
//
// SI LES ANCIENNES NE SONT PAS VIDES, ON REFUSE DE DÉMARRER. Ça veut dire que
// quelqu'un a réglé des droits pendant la fenêtre de rollback, sur l'ancienne
// interface : il y a deux vérités et aucune façon honnête de choisir. Sur un
// système de contrôle d'accès, s'arrêter avec un message précis vaut mieux que
// deviner - un accès deviné ne se voit pas.
func (d *DB) resorbeAnciennesTables() error {
	// Enfants d'abord : leurs REFERENCES pointent sur skill_groupes.
	anciennes := []string{"skill_groupe_membres", "skill_groupe_skills", "skill_groupes"}
	for _, t := range anciennes {
		existe, err := d.tableExiste(t)
		if err != nil {
			return err
		}
		if !existe {
			continue
		}
		var n int
		if err := d.sql.QueryRow(`SELECT count(*) FROM ` + t).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("migration des groupes : `%s` et les tables de cohortes coexistent, et l'ancienne porte %d ligne(s). Des droits ont été posés pendant un retour arrière ; les reporter à la main dans `groupes`/`groupe_membres`/`groupe_chemins` puis supprimer les trois tables `skill_groupe*`", t, n)
		}
	}
	for _, t := range anciennes {
		if _, err := d.sql.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return err
		}
	}
	log.Print("migration des groupes : tables `skill_groupe*` vides retrouvées à côté des cohortes (trace d'un retour arrière), supprimées")
	return nil
}

// resume : un SQL multiligne tient sur une ligne dans un message d'erreur.
func resume(q string) string {
	court := strings.Join(strings.Fields(q), " ")
	if len(court) > 70 {
		court = court[:70] + "…"
	}
	return court
}

// DejaFait dit si l'opération `cle` a déjà été marquée comme faite. Sert aux
// gestes qui doivent tourner UNE seule fois sur la durée de vie d'une base (une
// réparation ponctuelle), par opposition aux migrations idempotentes rejouées à
// chaque démarrage.
func (d *DB) DejaFait(cle string) (bool, error) {
	var un int
	err := d.sql.QueryRow(`SELECT 1 FROM meta WHERE cle = ?`, cle).Scan(&un)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// MarquerFait enregistre `cle` comme faite. Idempotent.
func (d *DB) MarquerFait(cle string) error {
	_, err := d.sql.Exec(`INSERT OR IGNORE INTO meta (cle) VALUES (?)`, cle)
	return err
}

// NbDevices compte les postes appairés d'un compte. Un compte à zéro poste n'a
// jamais synchronisé : il ne peut pas être le propriétaire légitime de contenu
// existant. Cf. incident du 28/07, où l'attribution est partie sur un compte mort.
func (d *DB) NbDevices(userID int64) (int, error) {
	var n int
	err := d.sql.QueryRow(`SELECT count(*) FROM device_tokens WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// Revendication : un skill, son auteur, et quand il a été revendiqué.
type Revendication struct {
	Slug   string
	Auteur string // username, vide si le compte a été supprimé
	CreeLe string
}

// RevendicationsSauf liste les revendications de TOUS LES AUTRES comptes, la
// plus récente d'abord.
//
// Aucun filtre de droits, et c'est délibéré : c'est la seule requête du serveur
// qui rend une existence à quelqu'un qui n'a pas le droit de lire le contenu.
// Voir la note de `handleSkillsNouveaux` avant de « corriger » cette absence.
//
// Les revendications sans compte (créateur supprimé) sortent : un signal qui
// ne peut désigner personne n'est pas actionnable.
func (d *DB) RevendicationsSauf(userID int64) ([]Revendication, error) {
	rows, err := d.sql.Query(`
		SELECT sc.slug, u.username, sc.created_at
		FROM skill_creators sc
		JOIN users u ON u.id = sc.user_id
		WHERE sc.user_id != ?
		ORDER BY sc.created_at DESC, sc.slug`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revendication
	for rows.Next() {
		var r Revendication
		if err := rows.Scan(&r.Slug, &r.Auteur, &r.CreeLe); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Createurs liste les revendications enregistrées (slug -> créateur). user_id
// est nul si le compte créateur a été supprimé.
func (d *DB) Createurs() (map[string]sql.NullInt64, error) {
	rows, err := d.sql.Query(`SELECT slug, user_id FROM skill_creators`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sql.NullInt64{}
	for rows.Next() {
		var slug string
		var id sql.NullInt64
		if err := rows.Scan(&slug, &id); err != nil {
			return nil, err
		}
		out[slug] = id
	}
	return out, rows.Err()
}

// AnnulerRevendication défait exactement ce qu'a fait ClaimSkill : retire la
// ligne de revendication ET le droit d'écriture qu'elle avait accordé, dans la
// même transaction. Sert à réparer une attribution erronée (incident 28/07).
//
// Limite assumée : si le créateur avait AUSSI un droit posé à la main sur ce
// même skill avant la revendication, ce droit part avec. L'annulation ne sait
// pas distinguer les deux ; elle défait la forme, pas l'intention.
func (d *DB) AnnulerRevendication(slug, grantPath string, userID int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM permissions WHERE user_id = ? AND path = ?`,
		userID, perms.Canon(grantPath)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM skill_creators WHERE slug = ?`, slug); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimSkill revendique le skill `slug` pour `userID` s'il est libre, ET lui
// accorde l'écriture sur `grantPath`, dans la MÊME transaction. Renvoie true si
// la revendication a eu lieu. Atomicité voulue : revendication et don de droit
// vivent ou échouent ensemble - jamais un créateur enfermé hors de son propre
// skill. `INSERT OR IGNORE` sur la clé primaire `slug` rend l'opération sûre en
// cas de course : deux créateurs simultanés, un seul gagne, l'autre obtient
// false sans erreur (et ne recevra donc pas le grant).
//
// LE SKILL NAÎT PRIVÉ POUR TOUT LE MONDE. Avant d'accorder l'écriture au
// créateur, la transaction PURGE toute règle pré-positionnée à la racine du
// skill ou plus profond, pour TOUS les comptes. Sans ça, une règle posée sur un
// chemin interne (`shared/skills/<slug>/x`) AVANT la revendication survivrait :
// la gestion d'un skill n'écrit qu'à sa racine (handleSetAccesSkill), or une
// règle plus profonde l'emporte dans perms.Effective, donc le créateur croirait
// avoir coupé un accès resté ouvert. La revendication n'ayant lieu que sur un
// slug LIBRE (skill neuf ou fraîchement adopté), aucun partage légitime n'existe
// encore : la purge est sûre.
func (d *DB) ClaimSkill(slug, grantPath string, userID int64) (bool, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT OR IGNORE INTO skill_creators (slug, user_id) VALUES (?, ?)`,
		slug, userID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil // déjà revendiqué : rien à accorder
	}
	// Purge du pré-positionnement STRICTEMENT PLUS PROFOND que la racine, pour
	// tous les comptes. Pas la racine elle-même : une règle À la racine est un
	// partage légitime que le créateur peut couper (handleSetAccesSkill y écrit,
	// upsert), et que le backfill du 28/07 gèle pour ne rien faire perdre à
	// personne. Seules les règles internes échappent au contrôle du créateur et
	// doivent partir. `_` et `%` d'un slug sont des jokers LIKE - échappés,
	// sinon revendiquer `blank_page` purgerait `blankXpage/interne`.
	racine := perms.Canon(grantPath)
	if _, err := tx.Exec(
		`DELETE FROM permissions WHERE path LIKE ? ESCAPE '\'`,
		echapeLike(racine)+"/%"); err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`INSERT INTO permissions (user_id, path, level) VALUES (?, ?, ?)
         ON CONFLICT(user_id, path) DO UPDATE SET level = excluded.level`,
		userID, racine, perms.Ecriture.String()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// echapeLike neutralise les jokers LIKE de SQLite (`%`, `_`) et le caractère
// d'échappement lui-même, pour un motif utilisé avec ESCAPE '\'.
func echapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// CreatorOf renvoie l'ID du créateur d'un skill. ok=false si le slug n'est pas
// revendiqué, ou s'il l'est mais que le créateur a été supprimé (user_id NULL).
// Sert l'autorisation de partage et l'affichage, jamais le filtrage de lecture.
func (d *DB) CreatorOf(slug string) (userID int64, ok bool, err error) {
	var id sql.NullInt64
	err = d.sql.QueryRow(`SELECT user_id FROM skill_creators WHERE slug = ?`, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id.Int64, id.Valid, nil
}

// EstCreateur : `userID` est-il le créateur enregistré de ce skill ? Seule
// autorisation de gestion des accès d'un skill - aucune exemption admin (modèle
// F3). Vit ici pour que l'API (Bearer) et le web admin (session) posent la MÊME
// question : deux surfaces, une seule règle. Une erreur de lecture répond non
// (fail-closed) : on ne laisse pas gérer un skill sur une base illisible.
func (d *DB) EstCreateur(userID int64, slug string) bool {
	id, ok, err := d.CreatorOf(slug)
	if err != nil {
		// Fail-closed silencieux se présenterait comme « j'ai perdu mon skill » :
		// les boutons disparaissent et les POST passent en 404, sans trace.
		log.Printf("db: lecture du créateur de %q : %v", slug, err)
		return false
	}
	return ok && id == userID
}

// ValidUsername : identifiant sûr partout où il circule (auteur git, nom de
// copie de conflit, HTML) : lettres, chiffres, « . _ - », 1 à 64 caractères.
// Un « / » ou un caractère de contrôle casserait validateAuthor côté storage
// et l'invariant « la copie de conflit reste dans le dossier de l'original ».
func ValidUsername(username string) bool {
	if len(username) == 0 || len(username) > 64 {
		return false
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// CreateUser insère un compte. Le mot de passe est haché (argon2id), jamais stocké en clair.
func (d *DB) CreateUser(username, password string, defaultLevel perms.Level, isAdmin bool) (*User, error) {
	if !ValidUsername(username) {
		return nil, errors.New("nom d'utilisateur invalide (lettres, chiffres, . _ - uniquement, 64 max)")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO users (username, password_hash, default_level, is_admin) VALUES (?, ?, ?, ?)`,
		username, hash, defaultLevel.String(), boolToInt(isAdmin))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	// Skills privés par défaut, ATOMIQUEMENT avec la création du compte (admin
	// inclus) : aucun compte ne peut exister sans cette règle, quel que soit le
	// chemin de création (bootstrap, web, futur). Sans elle, un compte au défaut
	// « lecture »/« ecriture » (dont tout admin) verrait les skills de tous.
	// origine = 'defaut' : cette règle n'est l'exception de personne. Sans la
	// marque, elle masquerait toute cohorte portant le dossier des skills.
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO permissions (user_id, path, level, origine) VALUES (?, ?, ?, ?)`,
		id, perms.Canon(skills.DefaultRoot), perms.Invisible.String(), OrigineDefaut); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &User{ID: id, Username: username, DefaultLevel: defaultLevel, IsAdmin: isAdmin}, nil
}

// Authenticate vérifie un couple username/password et renvoie l'utilisateur.
// Un utilisateur inconnu déclenche quand même une vérification argon2 factice
// pour ne pas révéler l'existence du compte par le temps de réponse.
func (d *DB) Authenticate(username, password string) (*User, error) {
	var (
		u    User
		hash string
		lvl  string
		adm  int
	)
	err := d.sql.QueryRow(
		`SELECT id, username, password_hash, default_level, is_admin FROM users WHERE username = ?`,
		username).Scan(&u.ID, &u.Username, &hash, &lvl, &adm)
	if errors.Is(err, sql.ErrNoRows) {
		auth.VerifyPassword(password, dummyHash) // égalise le temps de réponse
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !auth.VerifyPassword(password, hash) {
		return nil, ErrNotFound
	}
	u.DefaultLevel, _ = perms.ParseLevel(lvl)
	u.IsAdmin = adm != 0
	return &u, nil
}

// IssueToken crée un jeton d'appareil pour un utilisateur et renvoie sa valeur
// en clair (à remettre au client). Seul le hash est stocké.
func (d *DB) IssueToken(userID int64, label string) (string, error) {
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if _, err := d.sql.Exec(
		`INSERT INTO device_tokens (token_hash, user_id, label) VALUES (?, ?, ?)`,
		auth.HashToken(token), userID, label); err != nil {
		return "", err
	}
	return token, nil
}

// UserByToken résout un bearer token vers son utilisateur (chargé avec ses droits).
func (d *DB) UserByToken(token string) (*User, error) {
	var uid int64
	err := d.sql.QueryRow(
		`SELECT user_id FROM device_tokens WHERE token_hash = ?`,
		auth.HashToken(token)).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return d.userByID(uid)
}

// HasUsers indique si au moins un compte existe (bootstrap du premier admin).
func (d *DB) HasUsers() (bool, error) {
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// UserByID charge un utilisateur par son identifiant.
func (d *DB) UserByID(id int64) (*User, error) { return d.userByID(id) }

// ListUsers renvoie tous les comptes, triés par nom (affichage web admin).
func (d *DB) ListUsers() ([]User, error) {
	rows, err := d.sql.Query(`SELECT id, username, default_level, is_admin FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var (
			u   User
			lvl string
			adm int
		)
		if err := rows.Scan(&u.ID, &u.Username, &lvl, &adm); err != nil {
			return nil, err
		}
		u.DefaultLevel, _ = perms.ParseLevel(lvl)
		u.IsAdmin = adm != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// RevokeToken supprime un jeton d'appareil (déconnexion). Idempotent :
// révoquer un jeton inconnu n'est pas une erreur.
func (d *DB) RevokeToken(token string) error {
	_, err := d.sql.Exec(`DELETE FROM device_tokens WHERE token_hash = ?`, auth.HashToken(token))
	return err
}

// SetDefaultLevel change le niveau par défaut (racine) d'un utilisateur.
func (d *DB) SetDefaultLevel(userID int64, level perms.Level) error {
	res, err := d.sql.Exec(`UPDATE users SET default_level = ? WHERE id = ?`, level.String(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePermission retire une règle de droit (le chemin retombe sur la règle
// ancêtre ou le default_level). Idempotent.
func (d *DB) DeletePermission(userID int64, path string) error {
	_, err := d.sql.Exec(`DELETE FROM permissions WHERE user_id = ? AND path = ?`, userID, perms.Canon(path))
	return err
}

func (d *DB) userByID(id int64) (*User, error) {
	var (
		u   User
		lvl string
		adm int
	)
	err := d.sql.QueryRow(
		`SELECT id, username, default_level, is_admin FROM users WHERE id = ?`,
		id).Scan(&u.ID, &u.Username, &lvl, &adm)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.DefaultLevel, _ = perms.ParseLevel(lvl)
	u.IsAdmin = adm != 0
	return &u, nil
}

// EnsurePermission pose une règle de droit UNIQUEMENT si aucune n'existe déjà à
// ce (user, chemin). `INSERT OR IGNORE` : un droit déjà présent (posé à la main,
// un partage) n'est jamais écrasé. Sert à établir un défaut idempotent (ex:
// « shared/skills = invisible » pour tous), rejouable à chaque démarrage.
func (d *DB) EnsurePermission(userID int64, path string, level perms.Level) error {
	// origine par défaut ('exception') : les appelants de main.go réparent des
	// droits que quelqu'un avait voulus, ce ne sont pas des défauts de compte.
	_, err := d.sql.Exec(
		`INSERT OR IGNORE INTO permissions (user_id, path, level) VALUES (?, ?, ?)`,
		userID, perms.Canon(path), level.String())
	return err
}

// SetPermission pose (ou remplace) une règle de droit pour un utilisateur.
// Le chemin est canonicalisé avant stockage : la clé (user_id, path) déduplique
// alors les chemins logiquement identiques ("clients" et "clients/"), ce qui
// évite deux règles contradictoires sur le même dossier.
func (d *DB) SetPermission(userID int64, path string, level perms.Level) error {
	// L'ORIGINE EST RÉÉCRITE, PAS SEULEMENT POSÉE. SetPermission est le geste de
	// quelqu'un, donc toujours une exception - y compris quand il tombe sur une
	// ligne que CreateUser avait posée en `defaut`. Sans le DO UPDATE sur la
	// colonne, régler `shared/skills` à la main laissait la règle marquée
	// `defaut`, donc incapable de reprendre la main sur une cohorte : le geste
	// était accepté, confirmé à l'écran, et sans effet.
	_, err := d.sql.Exec(
		`INSERT INTO permissions (user_id, path, level, origine) VALUES (?, ?, ?, ?)
         ON CONFLICT(user_id, path) DO UPDATE SET level = excluded.level, origine = excluded.origine`,
		userID, perms.Canon(path), level.String(), OrigineException)
	return err
}

// Rules charge toutes les règles d'un utilisateur (pour resolution perms).
func (d *DB) Rules(userID int64) ([]perms.Rule, error) {
	regles, _, err := d.reglesEtOrigines(userID)
	return regles, err
}

// ReglesEtOrigines : les mêmes règles, avec le barreau qui a produit chacune.
//
// Exposé le 02/09 pour la fiche d'un compte. `Rules` jetait les origines depuis
// toujours, alors que la résolution les calcule de toute façon : l'écran qui
// demande « pourquoi cette personne voit ça » n'avait donc pas de réponse, sur
// le seul écran où la question se pose vraiment.
//
// Les deux tranches sont PARALLÈLES, index par index. C'est le contrat que
// `reglesEtOrigines` tient déjà en émettant les deux dans la même boucle triée.
func (d *DB) ReglesEtOrigines(userID int64) ([]perms.Rule, []Origine, error) {
	return d.reglesEtOrigines(userID)
}

// Origine : le barreau de l'echelle qui a produit un droit, en clair.
//
// L'ECRAN DOIT DIRE D'OU VIENT UN DROIT, pas seulement lequel il est. Sans ca,
// le mainteneur voit « Marie : lecture » et ne peut pas savoir s'il doit
// toucher la regle de Marie, sa cohorte, le defaut du dossier ou le defaut de
// son compte - donc il tatonne, et il tatonne sur des droits.
type Origine struct {
	// Barreau : "exception", "cohorte", "defaut", "dossier", ou "compte" quand
	// aucune regle ne couvre le chemin et que le defaut du compte s'applique.
	Barreau string
	// Cohorte : le nom de celle qui a gagne, quand Barreau vaut "cohorte".
	Cohorte string
}

// reglesEtOrigines : les regles, ET le barreau qui a produit chacune.
func (d *DB) reglesEtOrigines(userID int64) ([]perms.Rule, []Origine, error) {
	// ORDER BY path : résolution déterministe indépendante de l'ordre des rowids.
	rows, err := d.sql.Query(
		`SELECT path, level, origine FROM permissions WHERE user_id = ? ORDER BY path`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	exceptions := map[string]perms.Level{}
	defauts := map[string]perms.Level{}
	for rows.Next() {
		var p, l, origine string
		if err := rows.Scan(&p, &l, &origine); err != nil {
			return nil, nil, err
		}
		lvl, ok := perms.ParseLevel(l)
		if !ok {
			return nil, nil, fmt.Errorf("niveau stocké invalide : %q", l)
		}
		// Canon À L'ÉMISSION, pas seulement dans la clé du masque. La clé
		// primaire de `permissions` porte la chaîne BRUTE : deux lignes
		// canoniquement égales mais textuellement distinctes (« a/b » et
		// « a/b/ ») y coexisteraient et sortiraient toutes les deux, ce qui
		// rendrait la promesse « une règle par chemin canonique » fausse. Tous
		// les écrivains d'aujourd'hui canonicalisent ; rien ne l'impose.
		c := perms.Canon(p)
		if origine == OrigineDefaut {
			defauts[c] = lvl
			continue
		}
		exceptions[c] = lvl
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// LES GROUPES SONT INJECTÉS ICI, et pas appris à perms.Effective. Le
	// résolveur ne bouge pas d'une ligne, et les groupes ne persistent rien
	// dans `permissions` : sortir un chemin d'une cohorte lui rend son niveau
	// d'avant, sans règle orpheline à réparer.
	//
	// L'ÉCHELLE DE PRÉCÉDENCE, un barreau par ligne :
	//
	//   exception individuelle       → gagne toujours
	//     sinon le plus PERMISSIF des cohortes du compte
	//       sinon le défaut individuel automatique (origine = 'defaut')
	//         sinon le défaut porté par le DOSSIER (dossier_defauts)
	//           sinon le défaut du compte (users.default_level, racine implicite)
	//
	// Elle n'émet QU'UNE règle par chemin canonique - c'est construit ainsi
	// plus bas, un seul `out = append` par chemin. Ça met le départage
	// fail-closed de perms.Effective hors d'atteinte de nos propres émissions ;
	// il reste pour ce qu'il protège vraiment, deux règles de même profondeur
	// venues d'ailleurs.
	//
	// BARREAU 1, la précédence de l'exception (21/08). Sans elle, les deux
	// règles portent le même chemin donc la même profondeur, le fail-closed
	// prend la plus restrictive, et il devient impossible d'ouvrir un chemin à
	// quelqu'un que sa cohorte restreint.
	//
	// BARREAU 2, LES COHORTES S'ADDITIONNENT (25/08, Q1). Le plus permissif
	// gagne, sur un même chemin comme entre profondeurs différentes : inscrire
	// quelqu'un dans une deuxième cohorte ne doit JAMAIS lui retirer un accès.
	// L'effet inverse ne se voit pas à l'écran et se découvre le jour où
	// quelqu'un ne trouve plus un fichier. Le prix, assumé : une cohorte ne
	// ferme rien qu'une autre ouvre - c'est le défaut du dossier qui ferme.
	//
	// BARREAU 4, LE DÉFAUT PORTÉ PAR LE DOSSIER (25/08). C'est LUI qui ferme.
	// Une cohorte ouvre et ne ferme rien qu'une autre ouvre, donc protéger un
	// dossier sensible aurait demandé une règle individuelle par compte - et le
	// salarié n° 36 serait entré dans les RH le jour de son embauche, jusqu'à ce
	// que quelqu'un y repense. Une ligne de `dossier_defauts` vaut pour tout le
	// monde, y compris pour les comptes qui n'existent pas encore.
	//
	// Il passe SOUS le défaut automatique du compte : celui-ci est posé par
	// chemin ET par compte, donc plus précis.
	//
	// BARREAU 3, LE DÉFAUT AUTOMATIQUE NE MASQUE PAS UNE COHORTE (25/08, Q2).
	// `permissions` ne contient pas que des exceptions voulues : CreateUser y
	// pose `shared/skills = invisible` sur CHAQUE compte. Sans ce barreau, une
	// cohorte portant ce dossier serait muette pour tout le monde, sans un
	// message nulle part - et le piège se reposerait à chaque défaut
	// automatique ajouté plus tard.
	groupes, err := d.reglesDeGroupe(userID)
	if err != nil {
		return nil, nil, err
	}
	// LA RÉSOLUTION SE FAIT EN DEUX TEMPS, ET L'ORDRE EST TOUT (01/09).
	//
	// Étape 1, DANS chaque cohorte : le chemin le plus PROFOND gagne. Une
	// cohorte qui porte `shared` en lecture et `shared/direction` en invisible
	// exclut `shared/direction`, parce que la ligne profonde est une exclusion
	// VOULUE par celui qui l'a posée. C'est ce que la colonne `level` a rendu
	// exprimable, et c'est le geste que l'écran des cohortes propose.
	//
	// Étape 2, ENTRE les cohortes : le plus PERMISSIF gagne. Inscrire quelqu'un
	// dans une deuxième cohorte ne doit jamais lui retirer un accès (25/08, Q1).
	//
	// L'ANCIENNE VERSION FONDAIT LES DEUX EN UNE, en repliant sur les ancêtres
	// après avoir mélangé toutes les cohortes dans une map par chemin. Elle
	// n'avait rien à perdre tant qu'une cohorte portait UN niveau pour tous ses
	// chemins ; depuis que le chemin porte le sien, elle annulait l'exclusion
	// profonde par le niveau de la racine de la même cohorte, et la fonction
	// serait née morte.
	parCohorte := map[int64][]perms.Rule{}
	interessants := map[string]bool{}
	for _, r := range groupes {
		c := perms.Canon(r.Path)
		parCohorte[r.GroupeID] = append(parCohorte[r.GroupeID], perms.Rule{Path: c, Level: r.Level})
		interessants[c] = true
	}
	cohortes := make(map[string]perms.Level, len(interessants))
	gagnante := make(map[string]int64, len(interessants))
	for c := range interessants {
		premier := true
		var meilleur perms.Level
		var quelle int64
		for gid, regles := range parCohorte {
			niveau, couvre := niveauDeCohorte(c, regles)
			if !couvre {
				// Une cohorte qui ne couvre pas ce chemin ne dit RIEN dessus.
				// La faire voter `invisible` lui ferait fermer ce qu'une autre
				// ouvre, ce que le barreau 2 interdit.
				continue
			}
			if premier || niveau > meilleur {
				meilleur, quelle, premier = niveau, gid, false
			}
		}
		if !premier {
			cohortes[c] = meilleur
			gagnante[c] = quelle
		}
	}

	// Émission : un passage, un seul append par chemin, dans l'ordre trié.
	// TRIER AVANT D'ÉMETTRE : « When iterating over a map with a range loop,
	// the iteration order is not specified and is not guaranteed to be the same
	// from one iteration to the next. » La résolution doit être déterministe.
	// Source: https://go.dev/blog/maps
	defautsDossier, err := d.DefautsDossiers()
	if err != nil {
		return nil, nil, err
	}
	tous := make(map[string]bool, len(exceptions)+len(defauts)+len(cohortes)+len(defautsDossier))
	for c := range exceptions {
		tous[c] = true
	}
	for c := range defauts {
		tous[c] = true
	}
	for c := range cohortes {
		tous[c] = true
	}
	for c := range defautsDossier {
		tous[c] = true
	}
	chemins := make([]string, 0, len(tous))
	for c := range tous {
		chemins = append(chemins, c)
	}
	sort.Strings(chemins)

	noms, err := d.nomsDesGroupes()
	if err != nil {
		return nil, nil, err
	}
	out := make([]perms.Rule, 0, len(chemins))
	origines := make([]Origine, 0, len(chemins))
	for _, c := range chemins {
		switch {
		case estDans(exceptions, c):
			out = append(out, perms.Rule{Path: c, Level: exceptions[c]})
			origines = append(origines, Origine{Barreau: "exception"})
		case estDans(cohortes, c):
			out = append(out, perms.Rule{Path: c, Level: cohortes[c]})
			origines = append(origines, Origine{Barreau: "cohorte", Cohorte: noms[gagnante[c]]})
		case estDans(defauts, c):
			out = append(out, perms.Rule{Path: c, Level: defauts[c]})
			origines = append(origines, Origine{Barreau: "defaut"})
		default:
			out = append(out, perms.Rule{Path: c, Level: defautsDossier[c]})
			origines = append(origines, Origine{Barreau: "dossier"})
		}
	}
	return out, origines, nil
}

func (d *DB) nomsDesGroupes() (map[int64]string, error) {
	groupes, err := d.ListGroupes()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(groupes))
	for _, g := range groupes {
		out[g.ID] = g.Nom
	}
	return out, nil
}

// DroitAvecOrigine : le droit effectif d'un compte sur un chemin, ET le barreau
// qui l'a produit.
//
// Meme depart que perms.Effective - la regle couvrante la plus PROFONDE gagne,
// le plus restrictif a profondeur egale - parce que rendre une origine qui ne
// correspond pas au droit affiche a cote serait pire que ne rien dire.
func (d *DB) DroitAvecOrigine(u *User, chemin string) (perms.Level, Origine, error) {
	r, err := d.ResolveurDe(u)
	if err != nil {
		return perms.Invisible, Origine{}, err
	}
	niveau, origine := r.Droit(chemin)
	return niveau, origine, nil
}

// Resolveur : les règles d'UN compte, chargées une fois, interrogeables autant
// de fois qu'on veut sans retoucher la base.
//
// POURQUOI IL EXISTE. `DroitAvecOrigine` lance une requête SQL à chaque appel,
// ce qui va très bien pour la fiche d'un compte - un chemin, un compte. L'écran
// du mainteneur (DAR-198), lui, demande le droit de CHAQUE compte sur CHAQUE
// fichier : 1186 x 12 = 14 232 requêtes, et 645 ms mesurés au banc de DAR-195
// contre un seuil de 140 ms. La requête n'était pas lente, elle était répétée.
type Resolveur struct {
	defaut   perms.Level
	regles   []perms.Rule
	origines []Origine
}

// ResolveurDe : charge les règles d'un compte, une fois.
func (d *DB) ResolveurDe(u *User) (*Resolveur, error) {
	regles, origines, err := d.reglesEtOrigines(u.ID)
	if err != nil {
		return nil, err
	}
	return &Resolveur{defaut: u.DefaultLevel, regles: regles, origines: origines}, nil
}

// Droit : le droit effectif sur un chemin, et d'où il vient. Aucune E/S.
//
// Corps repris tel quel de l'ancien `DroitAvecOrigine` : l'échelle de
// précédence ne change pas, seul l'endroit où les règles sont chargées change.
func (r *Resolveur) Droit(chemin string) (perms.Level, Origine) {
	cible := perms.Canon(chemin)
	meilleur, profondeur := r.defaut, -1
	origine := Origine{Barreau: "compte"}
	for i, regle := range r.regles {
		rp := perms.Canon(regle.Path)
		if !couvre(rp, cible) {
			continue
		}
		p := profondeurDe(rp)
		switch {
		case p > profondeur:
			meilleur, profondeur, origine = regle.Level, p, r.origines[i]
		case p == profondeur && regle.Level < meilleur:
			meilleur, origine = regle.Level, r.origines[i]
		}
	}
	return meilleur, origine
}

// estDans : la clé existe-t-elle ? perms.Invisible vaut 0, donc une simple
// lecture ne distingue pas « absent » de « posé à invisible » - et les deux ont
// des conséquences opposées sur l'échelle de précédence.
func estDans(m map[string]perms.Level, c string) bool {
	_, ok := m[c]
	return ok
}

// parentDe : le chemin du dossier parent, en forme canonique. La racine ("")
// est son propre parent, ce qui donne au repli sur les ancêtres sa condition
// d'arrêt.
func parentDe(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// Effective : droit effectif d'un utilisateur sur un chemin (charge ses règles).
func (d *DB) Effective(u *User, path string) (perms.Level, error) {
	rules, err := d.Rules(u.ID)
	if err != nil {
		return perms.Invisible, err
	}
	return perms.Effective(path, u.DefaultLevel, rules), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// dummyHash : hash argon2id valide d'un mot de passe arbitraire, utilisé pour
// égaliser le temps de réponse d'Authenticate sur un utilisateur inconnu.
// Généré une fois au chargement du package.
var dummyHash = mustDummy()

func mustDummy() string {
	h, err := auth.HashPassword("vecu-dummy-timing-equalizer")
	if err != nil {
		panic(err)
	}
	return h
}

// ---------------------------------------------------------------------------
// Groupes de skills
// ---------------------------------------------------------------------------
//
// Un groupe est un contenant de skills qui porte des droits : on y met des
// chemins, on y met des comptes avec un niveau, et le niveau s'applique à tous
// les chemins du groupe. UN CHEMIN VIT DANS PLUSIEURS GROUPES depuis le 25/08 :
// c'est ce qui fait la cohorte, et ce qui renverse la règle du 21/08.
//
// Les groupes ne persistent RIEN dans `permissions`. Ils sont injectés dans
// Rules à la lecture, donc sortir un skill d'un groupe lui rend son niveau
// d'avant sans laisser de règle orpheline à réparer. C'est aussi ce qui permet
// à `perms.Effective` de ne pas bouger d'une ligne : le résolveur, composant le
// plus sensible du produit, ignore jusqu'à l'existence des groupes.

// Groupe : un groupe de skills.
type Groupe struct {
	ID  int64
	Nom string
	// Genre : `dossier` ou `skill`. Une cohorte ne porte qu'un des deux, depuis
	// le 02/09 - voir `migreGenreDeGroupe` pour la raison.
	Genre string
}

// CreerGroupe crée une cohorte de DOSSIERS et rend son id.
//
// Le genre par défaut est `dossier` parce que c'est le geste courant. Les
// appelants qui créent une cohorte de skills passent par `CreerGroupeDeGenre`,
// et le nommer explicitement les oblige à savoir ce qu'ils créent.
func (d *DB) CreerGroupe(nom string) (int64, error) {
	return d.CreerGroupeDeGenre(nom, GenreDossier)
}

// CreerGroupeDeGenre crée une cohorte du genre demandé.
func (d *DB) CreerGroupeDeGenre(nom, genre string) (int64, error) {
	if genre != GenreDossier && genre != GenreSkill {
		return 0, fmt.Errorf("genre de cohorte inconnu : %q", genre)
	}
	res, err := d.sql.Exec(`INSERT INTO groupes (nom, genre) VALUES (?, ?)`, nom, genre)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RenommeGroupe change le nom d'un groupe. Le nom est UNIQUE en base : un
// doublon remonte ici comme erreur, et l'appelant en fait un message métier.
func (d *DB) RenommeGroupe(id int64, nom string) error {
	_, err := d.sql.Exec(`UPDATE groupes SET nom = ? WHERE id = ?`, nom, id)
	return err
}

// SupprimeGroupe efface un groupe. Les appartenances et les membres partent
// avec lui (ON DELETE CASCADE), donc les accès qu'il ouvrait se referment.
func (d *DB) SupprimeGroupe(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM groupes WHERE id = ?`, id)
	return err
}

// ListGroupes rend tous les groupes, triés par nom.
func (d *DB) ListGroupes() ([]Groupe, error) {
	rows, err := d.sql.Query(`SELECT id, nom, genre FROM groupes ORDER BY nom`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Groupe
	for rows.Next() {
		var g Groupe
		if err := rows.Scan(&g.ID, &g.Nom, &g.Genre); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetMembreGroupe donne à un compte un niveau sur tous les skills d'un groupe.
func (d *DB) SetMembreGroupe(groupeID, userID int64, level perms.Level) error {
	_, err := d.sql.Exec(
		`INSERT INTO groupe_membres (groupe_id, user_id, level) VALUES (?, ?, ?)
         ON CONFLICT(groupe_id, user_id) DO UPDATE SET level = excluded.level`,
		groupeID, userID, level.String())
	return err
}

// RetireMembreGroupe sort un compte du groupe.
func (d *DB) RetireMembreGroupe(groupeID, userID int64) error {
	_, err := d.sql.Exec(
		`DELETE FROM groupe_membres WHERE groupe_id = ? AND user_id = ?`, groupeID, userID)
	return err
}

// SetDefautDossier pose (ou remplace) le niveau par défaut d'un dossier.
func (d *DB) SetDefautDossier(chemin string, level perms.Level) error {
	_, err := d.sql.Exec(
		`INSERT INTO dossier_defauts (chemin, level) VALUES (?, ?)
         ON CONFLICT(chemin) DO UPDATE SET level = excluded.level`,
		perms.Canon(chemin), level.String())
	return err
}

// SupprimeDefautDossier retire le défaut d'un dossier : il retombe sur celui du
// compte, comme avant qu'on y touche.
func (d *DB) SupprimeDefautDossier(chemin string) error {
	_, err := d.sql.Exec(`DELETE FROM dossier_defauts WHERE chemin = ?`, perms.Canon(chemin))
	return err
}

// DefautDossier rend le défaut posé sur un dossier, et false s'il n'y en a pas.
func (d *DB) DefautDossier(chemin string) (perms.Level, bool, error) {
	var l string
	err := d.sql.QueryRow(
		`SELECT level FROM dossier_defauts WHERE chemin = ?`, perms.Canon(chemin)).Scan(&l)
	if errors.Is(err, sql.ErrNoRows) {
		return perms.Invisible, false, nil
	}
	if err != nil {
		return perms.Invisible, false, err
	}
	niveau, ok := perms.ParseLevel(l)
	if !ok {
		return perms.Invisible, false, fmt.Errorf("niveau de dossier invalide : %q", l)
	}
	return niveau, true, nil
}

// DefautsDossiers rend tous les défauts de dossier.
//
// Chargés en entier à chaque résolution : ils se comptent sur les doigts d'une
// main (un dossier RH, un plan stratégique), et les filtrer par chemin
// demanderait de connaître d'avance la cible, que Rules ignore.
func (d *DB) DefautsDossiers() (map[string]perms.Level, error) {
	rows, err := d.sql.Query(`SELECT chemin, level FROM dossier_defauts ORDER BY chemin`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]perms.Level{}
	for rows.Next() {
		var c, l string
		if err := rows.Scan(&c, &l); err != nil {
			return nil, err
		}
		niveau, ok := perms.ParseLevel(l)
		if !ok {
			return nil, fmt.Errorf("niveau de dossier invalide : %q", l)
		}
		out[c] = niveau
	}
	return out, rows.Err()
}

// GroupesDuMembre rend les ids des groupes dont un compte est membre, triés.
//
// Sert à borner ce qu'un non-administrateur peut toucher : son geste de partage
// ne doit pas pouvoir défaire une cohorte qu'il ne connaît pas.
func (d *DB) GroupesDuMembre(userID int64) ([]int64, error) {
	rows, err := d.sql.Query(
		`SELECT groupe_id FROM groupe_membres WHERE user_id = ? ORDER BY groupe_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MembresGroupe rend les couples (user_id, niveau) d'un groupe.
func (d *DB) MembresGroupe(groupeID int64) (map[int64]perms.Level, error) {
	rows, err := d.sql.Query(
		`SELECT user_id, level FROM groupe_membres WHERE groupe_id = ?`, groupeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]perms.Level{}
	for rows.Next() {
		var id int64
		var l string
		if err := rows.Scan(&id, &l); err != nil {
			return nil, err
		}
		lvl, ok := perms.ParseLevel(l)
		if !ok {
			return nil, fmt.Errorf("niveau stocké invalide : %q", l)
		}
		out[id] = lvl
	}
	return out, rows.Err()
}

// RangeChemin met un chemin dans un groupe.
//
// UN CHEMIN VIT DANS PLUSIEURS GROUPES depuis le 25/08 : c'est ce qui fait la
// cohorte. Ranger ailleurs AJOUTE, ça ne déplace plus (le 21/08 disait
// l'inverse, sous une clé primaire qui était le slug seul).
//
// `chemin` est calculé par l'appelant : la base reste ignorante de l'endroit où
// vivent les skills. `genre` et `slug` sont liés par un CHECK - un skill porte
// son slug, un dossier n'en porte pas.
// Source: https://www.sqlite.org/lang_upsert.html
//
// APPELANTS : ne pas appeler avec un *sql.Rows encore ouvert. Le pool n'a
// qu'une connexion (SetMaxOpenConns(1)).
func (d *DB) RangeChemin(groupeID int64, chemin, genre, slug string) error {
	// LE GENRE DU CHEMIN DOIT ÊTRE CELUI DE LA COHORTE (02/09).
	//
	// Sans cette garde, le typage ne serait qu'une étiquette : rien
	// n'empêcherait de ranger un skill dans une cohorte de dossiers, et il y
	// ouvrirait des accès depuis un écran qui ne le montre pas. Le refus vit
	// ici, au seul point d'écriture, plutôt que dans chacun des appelants - une
	// règle recopiée dans quatre handlers finit par manquer au cinquième.
	attendu, err := d.GenreDuGroupe(groupeID)
	if err != nil {
		return err
	}
	if genre != attendu {
		return fmt.Errorf("une cohorte de %ss ne peut pas porter un chemin de %s", attendu, genre)
	}
	_, err = d.sql.Exec(
		`INSERT INTO groupe_chemins (groupe_id, chemin, genre, slug) VALUES (?, ?, ?, ?)
         ON CONFLICT(groupe_id, chemin) DO UPDATE SET genre = excluded.genre, slug = excluded.slug`,
		groupeID, perms.Canon(chemin), genre, slug)
	return err
}

// SetNiveauChemin pose (ou retire) le niveau qu'une cohorte accorde SUR CE
// CHEMIN. Un niveau vide rend au chemin le niveau du membre, qui etait le seul
// comportement avant le 01/09.
//
// C'est le geste « ce sous-dossier n'est pas pour cette cohorte » : la cohorte
// porte `shared` en lecture, et `shared/direction` en invisible. Le chemin doit
// deja etre range dans la cohorte - poser un niveau sur un chemin qu'elle ne
// porte pas ne veut rien dire, et le silence le ferait croire fait.
func (d *DB) SetNiveauChemin(groupeID int64, chemin string, niveau string) error {
	res, err := d.sql.Exec(
		`UPDATE groupe_chemins SET level = ? WHERE groupe_id = ? AND chemin = ?`,
		niveau, groupeID, perms.Canon(chemin))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("le chemin %q n'est pas rangé dans la cohorte %d", chemin, groupeID)
	}
	return nil
}

// SortChemin retire un chemin d'UN groupe, et le laisse dans les autres.
//
// Le groupe fait partie de la clé : sans lui, sortir un skill d'une cohorte le
// sortirait de toutes, ce qui refermerait des accès que personne n'a demandé à
// refermer.
func (d *DB) SortChemin(groupeID int64, chemin string) error {
	_, err := d.sql.Exec(
		`DELETE FROM groupe_chemins WHERE groupe_id = ? AND chemin = ?`,
		groupeID, perms.Canon(chemin))
	return err
}

// GroupesDuChemin rend les ids des groupes qui portent ce chemin AU GENRE
// demandé, triés. Liste vide si le chemin n'est rangé nulle part.
//
// Le genre n'est pas décoratif ici : sans lui, l'écran des skills cocherait une
// ligne rangée comme dossier, et la décocher supprimerait cette ligne - un
// geste sur un écran détruisant l'état de l'autre, chacun affichant une vérité
// différente entre-temps.
func (d *DB) GroupesDuChemin(chemin, genre string) ([]int64, error) {
	rows, err := d.sql.Query(
		`SELECT groupe_id FROM groupe_chemins WHERE chemin = ? AND genre = ? ORDER BY groupe_id`,
		perms.Canon(chemin), genre)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CheminGroupe : une ligne de `groupe_chemins`, telle que les écrans la lisent.
type CheminGroupe struct {
	Chemin string
	Genre  string
	Slug   string
	// Niveau : ce que la cohorte accorde sur ce chemin. Vide = le chemin prend
	// le niveau du membre, qui etait le seul comportement avant le 01/09.
	Niveau string
}

// CheminsDuGroupe rend les chemins d'un groupe pour un genre donné, triés.
func (d *DB) CheminsDuGroupe(groupeID int64, genre string) ([]CheminGroupe, error) {
	rows, err := d.sql.Query(
		`SELECT chemin, genre, slug, level FROM groupe_chemins
          WHERE groupe_id = ? AND genre = ? ORDER BY chemin`, groupeID, genre)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheminGroupe
	for rows.Next() {
		var c CheminGroupe
		if err := rows.Scan(&c.Chemin, &c.Genre, &c.Slug, &c.Niveau); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SkillsDuGroupe rend les slugs des skills rangés dans un groupe, triés.
func (d *DB) SkillsDuGroupe(groupeID int64) ([]string, error) {
	lignes, err := d.CheminsDuGroupe(groupeID, "skill")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(lignes))
	for _, l := range lignes {
		out = append(out, l.Slug)
	}
	sort.Strings(out)
	return out, nil
}

// reglesDeGroupe : les règles qu'un compte tient de ses groupes.
//
// Une seule requête, jointure des trois tables : les groupes dont il est
// membre, croisés avec les skills qui y sont rangés.
// regleDeCohorte : une regle, ET la cohorte qui la porte.
//
// L'APPARTENANCE COMPTE POUR LA RESOLUTION, et c'est neuf du 01/09. Tant qu'une
// cohorte ne portait qu'un niveau pour tous ses chemins, les fondre toutes dans
// une map par chemin ne perdait rien. Depuis qu'un chemin porte son niveau, la
// profondeur doit etre arbitree DANS une cohorte (le plus profond gagne, c'est
// une exclusion voulue) avant que les cohortes ne soient fondues entre elles
// (le plus permissif gagne, pour qu'une deuxieme cohorte ne retire jamais rien).
type regleDeCohorte struct {
	GroupeID int64
	Path     string
	Level    perms.Level
}

func (d *DB) reglesDeGroupe(userID int64) ([]regleDeCohorte, error) {
	// COALESCE : le niveau du CHEMIN quand il est pose, celui du MEMBRE sinon.
	// Le vide est le comportement d'avant la colonne, donc une base non migree
	// et une base migree mais non reglee rendent exactement la meme chose.
	rows, err := d.sql.Query(`
        SELECT s.groupe_id, s.chemin, m.level,
               CASE WHEN s.level = '' THEN m.level ELSE s.level END
          FROM groupe_membres m
          JOIN groupe_chemins s ON s.groupe_id = m.groupe_id
         WHERE m.user_id = ?
         ORDER BY s.chemin`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []regleDeCohorte
	for rows.Next() {
		var gid int64
		var chemin, membre, effectif string
		if err := rows.Scan(&gid, &chemin, &membre, &effectif); err != nil {
			return nil, err
		}
		nMembre, ok := perms.ParseLevel(membre)
		if !ok {
			return nil, fmt.Errorf("niveau de membre invalide : %q", membre)
		}
		nChemin, ok := perms.ParseLevel(effectif)
		if !ok {
			return nil, fmt.Errorf("niveau de chemin de cohorte invalide : %q", effectif)
		}
		// LE NIVEAU DU MEMBRE EST UN PLAFOND, jamais un plancher. Un membre en
		// lecture ne gagne pas l'ecriture parce qu'un chemin la porte, et un
		// chemin `invisible` ferme pour tout le monde. Meme forme que le plafond
		// du jeton MCP (31/08) : un porteur ne depasse jamais ses propres droits.
		if nChemin > nMembre {
			nChemin = nMembre
		}
		out = append(out, regleDeCohorte{GroupeID: gid, Path: chemin, Level: nChemin})
	}
	return out, rows.Err()
}

// niveauDeCohorte : ce qu'UNE cohorte accorde sur `cible`, et si elle en dit
// quelque chose.
//
// Le plus PROFOND de ses chemins couvrants gagne, comme perms.Effective le fait
// pour les regles d'un compte. A profondeur egale - deux chemins canoniquement
// identiques, que la cle primaire de `groupe_chemins` interdit deja - le plus
// restrictif l'emporte, meme depart fail-closed que le resolveur.
func niveauDeCohorte(cible string, regles []perms.Rule) (perms.Level, bool) {
	cible = perms.Canon(cible)
	meilleur, profondeur := perms.Invisible, -1
	for _, r := range regles {
		rp := perms.Canon(r.Path)
		if !couvre(rp, cible) {
			continue
		}
		p := profondeurDe(rp)
		switch {
		case p > profondeur:
			meilleur, profondeur = r.Level, p
		case p == profondeur && r.Level < meilleur:
			meilleur = r.Level
		}
	}
	return meilleur, profondeur >= 0
}

// couvre : `chemin` est-il `cible` ou l'un de ses ancetres par segment ?
// Meme definition que `covers` dans perms, recopiee ici parce que le paquet ne
// l'exporte pas et que l'exporter elargirait sa surface pour un seul appelant.
func couvre(chemin, cible string) bool {
	if chemin == "" || chemin == cible {
		return true
	}
	return strings.HasPrefix(cible, chemin+"/")
}

func profondeurDe(chemin string) int {
	if chemin == "" {
		return 0
	}
	return strings.Count(chemin, "/") + 1
}

// CompteFermable dit si ce compte peut être fermé, et sinon POURQUOI.
//
// UN SEUL REFUS, et c'est une garde d'enfermement : fermer son PROPRE compte
// détruit la session avec laquelle on agit. Le geste se termine sur une page
// d'erreur, et si c'était le dernier administrateur, l'instance devient
// inadministrable dans le même clic - les comptes membres restent, les fichiers
// aussi, et plus personne ne peut créer d'administrateur puisque `CreateUser`
// est derrière le middleware admin.
//
// IL Y AVAIT UNE SECONDE GARDE ICI, « pas le dernier administrateur », et elle a
// été retirée après un contrôle de mutation : la neutraliser ne faisait tomber
// aucun test, parce qu'elle est INATTEIGNABLE. Le middleware admin garantit que
// celui qui agit est administrateur ; le refus d'auto-fermeture garantit qu'il
// n'est pas la cible. Il reste donc toujours au moins un administrateur - lui -
// après n'importe quelle fermeture que cette route accepte. Une garde qui ne
// peut pas tirer ne protège rien et fait croire le contraire.
//
// Le jour où une autre porte fermera un compte (une commande, un script), c'est
// ce raisonnement qu'il faudra reprendre, pas cette ligne qu'il aurait fallu
// garder.
func (d *DB) CompteFermable(id int64, parQui int64) error {
	if id == parQui {
		return errors.New("un compte ne peut pas se fermer lui-même : demandez à un autre administrateur")
	}
	if _, err := d.userByID(id); err != nil {
		return err
	}
	return nil
}

// DeleteUser ferme un compte.
//
// CE QUI PART AVEC LUI, et rien d'autre : ses règles de droits, ses jetons
// d'appareil, ses jetons MCP, ses appartenances de cohorte et l'état rapporté
// par ses postes. Toutes ces tables portent déjà `ON DELETE CASCADE` sur
// `users(id)` - la cascade n'est pas ajoutée ici, elle est seulement enfin
// atteignable.
//
// CE QUI RESTE : les fichiers. Le dépôt garde le contenu ET l'auteur de chaque
// commit. Fermer un compte retire un accès, ça n'efface pas un travail - c'est
// la même doctrine que la corbeille locale de DAR-199, à l'autre bout de la
// chaîne.
//
// PIÈGE MESURÉ, et il ne se voit pas depuis un autre outil : dans SQLite,
// `PRAGMA foreign_keys` est désactivé par défaut et se règle PAR CONNEXION
// (sqlite.org/foreignkeys.html). `db.Open` l'active (`_pragma=foreign_keys(1)`),
// donc les cascades tirent ici. Une suppression faite hors de l'application -
// un `sqlite3` sur le volume, un script d'inspection - laisserait des lignes
// orphelines qui rouvriraient des droits au prochain compte qui hériterait de
// l'identifiant. C'est la raison d'être de cette porte : il ne doit pas y avoir
// de second chemin.
func (d *DB) DeleteUser(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	touchees, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if touchees == 0 {
		return ErrNotFound
	}
	return nil
}

// GenreDossier / GenreSkill : les deux natures d'une cohorte.
//
// La valeur est la même que celle de `groupe_chemins.genre`, et ce n'est pas un
// hasard : le genre d'une cohorte EST celui des chemins qu'elle a le droit de
// porter. Deux vocabulaires pour la même distinction auraient fini par diverger.
const (
	GenreDossier = "dossier"
	GenreSkill   = "skill"
)

// migreGenreDeGroupe donne un genre à chaque cohorte (02/09, retour de Colin).
//
// POURQUOI LE GENRE MONTE SUR LA COHORTE. Il vivait sur le CHEMIN, ce qui
// laissait une cohorte porter des dossiers et des skills en même temps. Le
// modèle s'en accommodait - la résolution ne lit jamais le genre - mais
// l'écran, lui, ne pouvait pas être clair : le même panneau devait proposer un
// arbre de dossiers et une liste de skills, et une cohorte mixte apparaissait
// des deux côtés. « Les cohortes de skill et les cohortes de dossier doivent
// être séparées », dit Colin, et la seule façon de le tenir est de l'empêcher
// en base plutôt que de l'éviter à l'écran.
//
// LE GENRE SE DÉDUIT DU CONTENU, et le cas ambigu est le seul qui compte :
//
//   - une cohorte qui ne porte que des skills devient `skill` ;
//   - une cohorte qui ne porte que des dossiers devient `dossier` ;
//   - une cohorte VIDE devient `dossier`, parce que c'est le genre du geste
//     courant (« l'équipe contenu, voilà ses dossiers ») et qu'une cohorte vide
//     ne perd rien à être retypée à la main ;
//   - une cohorte MIXTE est SCINDÉE : elle garde ses dossiers, et une seconde
//     cohorte « <nom> (skills) » reçoit ses skills et les mêmes membres.
//
// La scission plutôt qu'un choix arbitraire : retyper une cohorte mixte en
// `dossier` ferait disparaître ses skills de tous les écrans SANS retirer les
// accès qu'ils ouvrent - des droits actifs et invisibles, exactement le mode
// d'échec que ce produit passe son temps à fermer.
//
// Garde d'idempotence : l'existence de la colonne, comme `migreOrigine`.
func (d *DB) migreGenreDeGroupe() error {
	deja, err := d.colonneExiste("groupes", "genre")
	if err != nil {
		return err
	}
	if deja {
		return nil
	}
	if _, err := d.sql.Exec(
		`ALTER TABLE groupes ADD COLUMN genre TEXT NOT NULL DEFAULT '` + GenreDossier + `'`); err != nil {
		return err
	}
	// Les cohortes qui ne portent QUE des skills.
	if _, err := d.sql.Exec(`
        UPDATE groupes SET genre = ?
         WHERE EXISTS (SELECT 1 FROM groupe_chemins c WHERE c.groupe_id = groupes.id AND c.genre = ?)
           AND NOT EXISTS (SELECT 1 FROM groupe_chemins c WHERE c.groupe_id = groupes.id AND c.genre = ?)`,
		GenreSkill, GenreSkill, GenreDossier); err != nil {
		return err
	}
	return d.scindeGroupesMixtes()
}

// scindeGroupesMixtes sort les skills des cohortes qui portent aussi des
// dossiers, dans une cohorte jumelle qui garde les mêmes membres.
func (d *DB) scindeGroupesMixtes() error {
	rows, err := d.sql.Query(`
        SELECT g.id, g.nom FROM groupes g
         WHERE EXISTS (SELECT 1 FROM groupe_chemins c WHERE c.groupe_id = g.id AND c.genre = ?)
           AND EXISTS (SELECT 1 FROM groupe_chemins c WHERE c.groupe_id = g.id AND c.genre = ?)
         ORDER BY g.id`, GenreSkill, GenreDossier)
	if err != nil {
		return err
	}
	type mixte struct {
		ID  int64
		Nom string
	}
	var mixtes []mixte
	for rows.Next() {
		var m mixte
		if err := rows.Scan(&m.ID, &m.Nom); err != nil {
			rows.Close()
			return err
		}
		mixtes = append(mixtes, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, m := range mixtes {
		// Le nom est UNIQUE : on désambiguïse plutôt que d'échouer la migration
		// entière sur une collision, ce qui laisserait la base à moitié typée.
		nom := m.Nom + " (skills)"
		for n := 2; ; n++ {
			var un int
			err := d.sql.QueryRow(`SELECT 1 FROM groupes WHERE nom = ?`, nom).Scan(&un)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				return err
			}
			nom = fmt.Sprintf("%s (skills %d)", m.Nom, n)
		}
		res, err := d.sql.Exec(`INSERT INTO groupes (nom, genre) VALUES (?, ?)`, nom, GenreSkill)
		if err != nil {
			return err
		}
		neuf, err := res.LastInsertId()
		if err != nil {
			return err
		}
		// Les MÊMES membres, aux mêmes niveaux : sans eux, la cohorte jumelle
		// n'ouvrirait rien et la scission serait un retrait d'accès déguisé.
		if _, err := d.sql.Exec(
			`INSERT INTO groupe_membres (groupe_id, user_id, level)
             SELECT ?, user_id, level FROM groupe_membres WHERE groupe_id = ?`, neuf, m.ID); err != nil {
			return err
		}
		if _, err := d.sql.Exec(
			`UPDATE groupe_chemins SET groupe_id = ? WHERE groupe_id = ? AND genre = ?`,
			neuf, m.ID, GenreSkill); err != nil {
			return err
		}
	}
	return nil
}

// GenreDuGroupe rend le genre d'une cohorte.
func (d *DB) GenreDuGroupe(id int64) (string, error) {
	var genre string
	err := d.sql.QueryRow(`SELECT genre FROM groupes WHERE id = ?`, id).Scan(&genre)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return genre, err
}
