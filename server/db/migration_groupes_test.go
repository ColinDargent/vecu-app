package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// La migration du 25/08 porte des ACCÈS RÉELS : c'est le risque numéro un de la
// spec des cohortes. Ce test monte une base à l'ancien schéma, mesure le droit
// effectif de chaque compte sur chaque chemin AVANT en rejouant l'ancienne
// résolution sur les anciennes tables, applique la migration, et exige
// l'égalité stricte.
//
// La mesure d'avant n'est PAS écrite à la main : elle rejoue la vraie requête
// d'avant (la jointure des trois tables) puis perms.Effective. Une valeur
// attendue codée en dur ne prouverait que ma lecture du schéma.

// schemaAncien : le schéma des groupes tel qu'il vivait jusqu'au 25/08. Figé
// ici, en clair : c'est l'entrée du test, elle ne doit pas suivre le code.
const schemaAncien = `
CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  default_level TEXT NOT NULL DEFAULT 'invisible',
  is_admin      INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE permissions (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path    TEXT NOT NULL,
  level   TEXT NOT NULL,
  PRIMARY KEY (user_id, path)
);
CREATE TABLE skill_groupes (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  nom  TEXT NOT NULL UNIQUE
);
CREATE TABLE skill_groupe_membres (
  groupe_id INTEGER NOT NULL REFERENCES skill_groupes(id) ON DELETE CASCADE,
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  level     TEXT NOT NULL,
  PRIMARY KEY (groupe_id, user_id)
);
CREATE TABLE skill_groupe_skills (
  slug      TEXT PRIMARY KEY,
  groupe_id INTEGER NOT NULL REFERENCES skill_groupes(id) ON DELETE CASCADE,
  chemin    TEXT NOT NULL
);`

// jeuAncien : de quoi couvrir les quatre cas qui décident d'un accès - le
// défaut du compte, une règle individuelle, une règle de groupe, et la
// précédence de l'individuelle sur celle du groupe.
const jeuAncien = `
INSERT INTO users (id, username, password_hash, default_level, is_admin) VALUES
  (1, 'colin',   'x', 'ecriture',  1),
  (2, 'achille', 'x', 'invisible', 0),
  (3, 'celia',   'x', 'lecture',   0);
INSERT INTO permissions (user_id, path, level) VALUES
  (2, 'shared/clients', 'lecture'),
  (3, 'shared/skills/veille', 'invisible');
INSERT INTO skill_groupes (id, nom) VALUES (1, 'Opérations'), (2, 'Direction');
INSERT INTO skill_groupe_membres (groupe_id, user_id, level) VALUES
  (1, 2, 'lecture'),
  (1, 3, 'ecriture'),
  (2, 2, 'invisible');
INSERT INTO skill_groupe_skills (slug, groupe_id, chemin) VALUES
  ('veille',   1, 'shared/skills/veille'),
  ('daily',    1, 'shared/skills/daily'),
  ('strategie', 2, 'shared/skills/strategie');`

// ciblesMesurees : les chemins dont on compare le droit effectif. Un par cas du
// jeu, plus deux voisins pour attraper une règle qui aurait fui.
var ciblesMesurees = []string{
	"",
	"shared",
	"shared/clients",
	"shared/clients/vdf/note.md",
	"shared/skills/veille",
	"shared/skills/daily",
	"shared/skills/strategie",
	"shared/skills/autre",
}

// effectifsAncienSchema rejoue la résolution d'avant le 25/08 : les règles
// individuelles, puis celles des groupes que l'individuelle n'a pas déjà
// couvertes, puis perms.Effective.
func effectifsAncienSchema(t *testing.T, raw *sql.DB) map[string]perms.Level {
	t.Helper()
	out := map[string]perms.Level{}
	comptes, err := raw.Query(`SELECT id, username, default_level FROM users ORDER BY id`)
	if err != nil {
		t.Fatalf("lecture des comptes : %v", err)
	}
	defer comptes.Close()
	type compte struct {
		id     int64
		nom    string
		defaut perms.Level
	}
	var tous []compte
	for comptes.Next() {
		var c compte
		var lvl string
		if err := comptes.Scan(&c.id, &c.nom, &lvl); err != nil {
			t.Fatalf("scan compte : %v", err)
		}
		niveau, ok := perms.ParseLevel(lvl)
		if !ok {
			t.Fatalf("niveau par défaut invalide : %q", lvl)
		}
		c.defaut = niveau
		tous = append(tous, c)
	}

	for _, c := range tous {
		var regles []perms.Rule
		individuelles := map[string]bool{}
		rows, err := raw.Query(
			`SELECT path, level FROM permissions WHERE user_id = ? ORDER BY path`, c.id)
		if err != nil {
			t.Fatalf("règles individuelles : %v", err)
		}
		for rows.Next() {
			var p, l string
			if err := rows.Scan(&p, &l); err != nil {
				t.Fatalf("scan règle : %v", err)
			}
			niveau, _ := perms.ParseLevel(l)
			regles = append(regles, perms.Rule{Path: p, Level: niveau})
			individuelles[perms.Canon(p)] = true
		}
		rows.Close()

		// La requête d'avant, mot pour mot.
		gr, err := raw.Query(`
            SELECT s.chemin, m.level
              FROM skill_groupe_membres m
              JOIN skill_groupe_skills s ON s.groupe_id = m.groupe_id
             WHERE m.user_id = ?
             ORDER BY s.chemin`, c.id)
		if err != nil {
			t.Fatalf("règles de groupe : %v", err)
		}
		for gr.Next() {
			var p, l string
			if err := gr.Scan(&p, &l); err != nil {
				t.Fatalf("scan règle de groupe : %v", err)
			}
			if individuelles[perms.Canon(p)] {
				continue
			}
			niveau, _ := perms.ParseLevel(l)
			regles = append(regles, perms.Rule{Path: p, Level: niveau})
		}
		gr.Close()

		for _, cible := range ciblesMesurees {
			out[c.nom+" @ "+cible] = perms.Effective(cible, c.defaut, regles)
		}
	}
	return out
}

func TestMigrationGroupesPreserveLesAcces(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu.db")

	raw, err := sql.Open("sqlite", chemin+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("ouverture brute : %v", err)
	}
	if _, err := raw.Exec(schemaAncien); err != nil {
		t.Fatalf("schéma ancien : %v", err)
	}
	if _, err := raw.Exec(jeuAncien); err != nil {
		t.Fatalf("jeu de données : %v", err)
	}
	avant := effectifsAncienSchema(t, raw)
	raw.Close()

	// Open joue migrate(), donc migreGroupes().
	d, err := Open(chemin)
	if err != nil {
		t.Fatalf("Open après migration : %v", err)
	}
	defer d.Close()

	users, err := d.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers : %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("comptes perdus à la migration : %d au lieu de 3", len(users))
	}
	apres := map[string]perms.Level{}
	for _, u := range users {
		for _, cible := range ciblesMesurees {
			lvl, err := d.Effective(&u, cible)
			if err != nil {
				t.Fatalf("Effective : %v", err)
			}
			apres[u.Username+" @ "+cible] = lvl
		}
	}

	if len(avant) != len(apres) {
		t.Fatalf("mesures : %d avant, %d après", len(avant), len(apres))
	}
	for cle, attendu := range avant {
		if got := apres[cle]; got != attendu {
			t.Errorf("ACCÈS CHANGÉ PAR LA MIGRATION — %s : %s avant, %s après",
				cle, attendu, got)
		}
	}

	// Le schéma a bien basculé, et rien n'est resté en arrière.
	for _, mort := range []string{"skill_groupes", "skill_groupe_membres", "skill_groupe_skills"} {
		if existe, err := d.tableExiste(mort); err != nil || existe {
			t.Errorf("table %q toujours présente après migration (err=%v)", mort, err)
		}
	}
	for _, vivant := range []string{"groupes", "groupe_membres", "groupe_chemins"} {
		if existe, err := d.tableExiste(vivant); err != nil || !existe {
			t.Errorf("table %q absente après migration (err=%v)", vivant, err)
		}
	}

	// Les trois skills sont repris, tous en genre 'skill'.
	var n int
	if err := d.sql.QueryRow(
		`SELECT count(*) FROM groupe_chemins WHERE genre = 'skill'`).Scan(&n); err != nil {
		t.Fatalf("comptage : %v", err)
	}
	if n != 3 {
		t.Errorf("chemins repris : %d au lieu de 3", n)
	}
}

// TestMigrationGroupesIdempotente : le serveur rejoue migrate() à CHAQUE
// démarrage. Un deuxième passage ne doit rien casser ni rien dupliquer.
//
// Attention à ce que ce test prouve : à partir de la passe 2, `skill_groupes`
// n'existe plus, donc migreGroupes sort à sa première ligne. Il fige le GARDE,
// pas l'innocuité du corps. C'est TestMigrationGroupesApresRetourArriere qui
// couvre le cas où le corps retrouve du travail à faire.
func TestMigrationGroupesIdempotente(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu.db")
	raw, err := sql.Open("sqlite", chemin)
	if err != nil {
		t.Fatalf("ouverture brute : %v", err)
	}
	if _, err := raw.Exec(schemaAncien + jeuAncien); err != nil {
		t.Fatalf("base ancienne : %v", err)
	}
	raw.Close()

	for passe := 1; passe <= 3; passe++ {
		d, err := Open(chemin)
		if err != nil {
			t.Fatalf("passe %d : Open : %v", passe, err)
		}
		var n int
		if err := d.sql.QueryRow(`SELECT count(*) FROM groupe_chemins`).Scan(&n); err != nil {
			t.Fatalf("passe %d : comptage : %v", passe, err)
		}
		if n != 3 {
			t.Errorf("passe %d : %d chemins au lieu de 3", passe, n)
		}
		groupes, err := d.ListGroupes()
		if err != nil {
			t.Fatalf("passe %d : ListGroupes : %v", passe, err)
		}
		if len(groupes) != 2 {
			t.Errorf("passe %d : %d groupes au lieu de 2", passe, len(groupes))
		}
		d.Close()
	}
}

// TestMigrationGroupesApresRetourArriere : LE cas qui tuait le serveur.
//
// Rollback de déploiement : le binaire d'avant rejoue son `CREATE TABLE IF NOT
// EXISTS skill_groupes`, qui refabrique les trois anciennes tables VIDES à côté
// des cohortes. Au redéploiement, le renommage trouve `groupes` déjà pris,
// migrate() remonte l'erreur et main fait log.Fatalf - à chaque boot, pour
// toujours, sur une base de droits en production.
func TestMigrationGroupesApresRetourArriere(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu.db")
	raw, err := sql.Open("sqlite", chemin)
	if err != nil {
		t.Fatalf("ouverture brute : %v", err)
	}
	if _, err := raw.Exec(schemaAncien + jeuAncien); err != nil {
		t.Fatalf("base ancienne : %v", err)
	}
	raw.Close()

	d, err := Open(chemin) // déploiement
	if err != nil {
		t.Fatalf("premier Open : %v", err)
	}
	d.Close()

	// Le binaire d'avant, revenu en arrière, recrée ses tables à vide.
	raw, err = sql.Open("sqlite", chemin)
	if err != nil {
		t.Fatalf("réouverture brute : %v", err)
	}
	if _, err := raw.Exec(`
CREATE TABLE IF NOT EXISTS skill_groupes (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  nom  TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS skill_groupe_membres (
  groupe_id INTEGER NOT NULL REFERENCES skill_groupes(id) ON DELETE CASCADE,
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  level     TEXT NOT NULL,
  PRIMARY KEY (groupe_id, user_id)
);
CREATE TABLE IF NOT EXISTS skill_groupe_skills (
  slug      TEXT PRIMARY KEY,
  groupe_id INTEGER NOT NULL REFERENCES skill_groupes(id) ON DELETE CASCADE,
  chemin    TEXT NOT NULL
);`); err != nil {
		t.Fatalf("recréation par le binaire d'avant : %v", err)
	}
	raw.Close()

	// Redéploiement. C'est ici que le serveur mourait.
	d2, err := Open(chemin)
	if err != nil {
		t.Fatalf("REDÉPLOIEMENT IMPOSSIBLE après retour arrière : %v", err)
	}
	defer d2.Close()

	var n int
	if err := d2.sql.QueryRow(`SELECT count(*) FROM groupe_chemins`).Scan(&n); err != nil {
		t.Fatalf("comptage : %v", err)
	}
	if n != 3 {
		t.Errorf("chemins perdus au passage : %d au lieu de 3", n)
	}
	for _, mort := range []string{"skill_groupes", "skill_groupe_membres", "skill_groupe_skills"} {
		if existe, _ := d2.tableExiste(mort); existe {
			t.Errorf("table %q laissée derrière : le prochain boot repassera par le même piège", mort)
		}
	}
}

// TestMigrationGroupesRefuseDeDevinerDeuxVerites : si l'ancienne table porte des
// lignes, c'est que quelqu'un a réglé des droits pendant la fenêtre de retour
// arrière. Deux vérités, aucune façon honnête de choisir : on s'arrête avec un
// message qui dit quoi faire, plutôt que d'inventer un accès.
func TestMigrationGroupesRefuseDeDevinerDeuxVerites(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu.db")
	raw, err := sql.Open("sqlite", chemin)
	if err != nil {
		t.Fatalf("ouverture brute : %v", err)
	}
	if _, err := raw.Exec(schemaAncien + jeuAncien); err != nil {
		t.Fatalf("base ancienne : %v", err)
	}
	raw.Close()

	d, err := Open(chemin)
	if err != nil {
		t.Fatalf("premier Open : %v", err)
	}
	d.Close()

	raw, _ = sql.Open("sqlite", chemin)
	if _, err := raw.Exec(`
CREATE TABLE IF NOT EXISTS skill_groupes (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  nom  TEXT NOT NULL UNIQUE
);
INSERT INTO skill_groupes (nom) VALUES ('Posé pendant le retour arrière');`); err != nil {
		t.Fatalf("écriture pendant le retour arrière : %v", err)
	}
	raw.Close()

	if _, err := Open(chemin); err == nil {
		t.Fatal("la migration a avalé une ancienne table NON VIDE sans rien dire")
	} else if !strings.Contains(err.Error(), "coexistent") {
		t.Errorf("message peu actionnable : %v", err)
	}
}

// TestGroupeCheminCheckGenreSlug : le CHECK est la seule chose qui empêche un
// dossier de porter un slug (ou un skill de n'en pas porter). Sans lui, une
// ligne incohérente passe et ne casse qu'au rendu, bien plus loin.
func TestGroupeCheminCheckGenreSlug(t *testing.T) {
	d := newDB(t)
	id, err := d.CreerGroupe("Salariés")
	if err != nil {
		t.Fatalf("CreerGroupe : %v", err)
	}
	cas := []struct {
		nom            string
		genre, slug    string
		doitEtreRefuse bool
	}{
		{"skill avec slug", "skill", "veille", false},
		{"dossier sans slug", "dossier", "", false},
		{"skill sans slug", "skill", "", true},
		{"dossier avec slug", "dossier", "veille", true},
		{"genre inventé", "cohorte", "", true},
	}
	for i, c := range cas {
		_, err := d.sql.Exec(
			`INSERT INTO groupe_chemins (groupe_id, chemin, genre, slug) VALUES (?, ?, ?, ?)`,
			id, "shared/essai", c.genre, c.slug)
		if c.doitEtreRefuse && err == nil {
			t.Errorf("cas %q : accepté alors qu'il viole le CHECK", c.nom)
		}
		if !c.doitEtreRefuse && err != nil {
			t.Errorf("cas %q : refusé à tort : %v", c.nom, err)
		}
		if err == nil {
			d.sql.Exec(`DELETE FROM groupe_chemins WHERE groupe_id = ? AND chemin = ?`, id, "shared/essai")
		}
		_ = i
	}
}

// TestMigrationGroupesAvaleLesLignesPathologiques : la table d'arrivée porte
// deux contraintes que celle de départ n'avait pas - la clé (groupe_id, chemin)
// et le CHECK genre/slug. Deux lignes parfaitement LÉGALES sous l'ancien schéma
// les violent, et un SELECT nu ferait mourir le serveur au boot, sur la base de
// droits de production.
//
// Aucune des deux ne devrait exister (skillPath est injectif, ValidNom refuse
// le vide) - mais le schéma décrit ce que le code écrit, pas ce que la table
// contient, et personne n'a regardé la base Railway.
func TestMigrationGroupesAvaleLesLignesPathologiques(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "vecu.db")
	raw, err := sql.Open("sqlite", chemin)
	if err != nil {
		t.Fatalf("ouverture brute : %v", err)
	}
	if _, err := raw.Exec(schemaAncien + `
INSERT INTO users (id, username, password_hash, default_level) VALUES
  (1, 'achille', 'x', 'invisible');
INSERT INTO skill_groupes (id, nom) VALUES (1, 'Opérations');
INSERT INTO skill_groupe_membres (groupe_id, user_id, level) VALUES (1, 1, 'lecture');
-- Deux slugs distincts sur le MÊME chemin dans le même groupe : légal avec
-- slug en clé primaire, interdit par la clé (groupe_id, chemin).
INSERT INTO skill_groupe_skills (slug, groupe_id, chemin) VALUES
  ('veille',     1, 'shared/skills/veille'),
  ('veille-bis', 1, 'shared/skills/veille'),
-- Un slug vide : légal avant, refusé par le CHECK genre='skill'.
  ('',           1, 'shared/skills/orphelin');`); err != nil {
		t.Fatalf("jeu pathologique : %v", err)
	}
	raw.Close()

	d, err := Open(chemin)
	if err != nil {
		t.Fatalf("MIGRATION MORTE SUR UNE LIGNE LÉGALE D'AVANT : %v", err)
	}
	defer d.Close()

	// Ce qui compte n'est pas le nombre de lignes, c'est que la RÈGLE émise
	// soit la même : le chemin reste ouvert en lecture à son membre.
	u, err := d.UserByID(1)
	if err != nil {
		t.Fatalf("UserByID : %v", err)
	}
	for _, cible := range []string{"shared/skills/veille", "shared/skills/orphelin"} {
		niveau, err := d.Effective(u, cible)
		if err != nil {
			t.Fatalf("Effective : %v", err)
		}
		if niveau != perms.Lecture {
			t.Errorf("%s : %s après migration, lecture attendue - l'accès a bougé", cible, niveau)
		}
	}

	// Le doublon est replié sur une ligne, l'orphelin est passé en dossier.
	var n int
	d.sql.QueryRow(`SELECT count(*) FROM groupe_chemins WHERE chemin = 'shared/skills/veille'`).Scan(&n)
	if n != 1 {
		t.Errorf("doublon non replié : %d lignes", n)
	}
	var genre, slug string
	d.sql.QueryRow(`SELECT genre, slug FROM groupe_chemins WHERE chemin = 'shared/skills/orphelin'`).Scan(&genre, &slug)
	if genre != "dossier" || slug != "" {
		t.Errorf("slug vide mal repris : genre=%q slug=%q", genre, slug)
	}
}
