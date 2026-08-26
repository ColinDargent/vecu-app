package db

import (
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// Les groupes de skills sont une SECONDE source de droits, à côté de la table
// permissions. Ils sont injectés dans Rules plutôt qu'appris à perms.Effective :
// le résolveur, composant le plus sensible du produit, ne bouge pas d'une ligne.
//
// Ces tests figent la règle de précédence AVANT le code, parce qu'une erreur ici
// n'est pas un bug d'affichage : c'est un accès ouvert ou fermé à tort.

func groupeAvec(t *testing.T, d *DB, nom string, userID int64, niveau perms.Level, slugs ...string) int64 {
	t.Helper()
	id, err := d.CreerGroupe(nom)
	if err != nil {
		t.Fatalf("CreerGroupe : %v", err)
	}
	if err := d.SetMembreGroupe(id, userID, niveau); err != nil {
		t.Fatalf("SetMembreGroupe : %v", err)
	}
	for _, slug := range slugs {
		if err := d.RangeChemin(id, "shared/skills/"+slug, "skill", slug); err != nil {
			t.Fatalf("RangeChemin : %v", err)
		}
	}
	return id
}

func niveauDe(t *testing.T, d *DB, u *User, chemin string) perms.Level {
	t.Helper()
	rules, err := d.Rules(u.ID)
	if err != nil {
		t.Fatalf("Rules : %v", err)
	}
	return perms.Effective(chemin, u.DefaultLevel, rules)
}

func TestGroupeOuvreUnSkill(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	groupeAvec(t, d, "equipe-contenu", u.ID, perms.Lecture, "linkedin-post")

	if got := niveauDe(t, d, u, "shared/skills/linkedin-post/SKILL.md"); got != perms.Lecture {
		t.Errorf("skill du groupe = %v, attendu lecture", got)
	}
	// Ce qui n'est pas dans le groupe ne bouge pas.
	if got := niveauDe(t, d, u, "shared/skills/autre/SKILL.md"); got != perms.Invisible {
		t.Errorf("skill hors groupe = %v, attendu invisible", got)
	}
	if got := niveauDe(t, d, u, "shared/tasks.md"); got != perms.Invisible {
		t.Errorf("fichier ordinaire = %v : le groupe a débordé des skills", got)
	}
}

// TestRegleIndividuelleLEmporteSurLeGroupe : LA clause qui rend le modèle
// utilisable. Sans elle, la règle du groupe et la règle individuelle ont la même
// profondeur, et le départage fail-closed de perms.Effective prend toujours la
// plus restrictive - il deviendrait impossible d'OUVRIR un skill à quelqu'un que
// son groupe restreint.
func TestRegleIndividuelleLEmporteSurLeGroupe(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	groupeAvec(t, d, "equipe", u.ID, perms.Lecture, "ouvert", "ferme")

	// Vers le HAUT : le groupe donne lecture, l'individuel donne écriture.
	if err := d.SetPermission(u.ID, "shared/skills/ouvert", perms.Ecriture); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	if got := niveauDe(t, d, u, "shared/skills/ouvert/SKILL.md"); got != perms.Ecriture {
		t.Errorf("surcharge vers le haut = %v, attendu écriture", got)
	}

	// Vers le BAS : le groupe donne lecture, l'individuel ferme.
	if err := d.SetPermission(u.ID, "shared/skills/ferme", perms.Invisible); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	if got := niveauDe(t, d, u, "shared/skills/ferme/SKILL.md"); got != perms.Invisible {
		t.Errorf("surcharge vers le bas = %v, attendu invisible", got)
	}
}

// TestSortirDunGroupeNeLaissePasDeRegle : les groupes ne persistent RIEN dans
// permissions. Retirer un skill de son groupe lui rend son niveau d'avant, sans
// règle orpheline à réparer.
func TestSortirDunGroupeNeLaissePasDeRegle(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	id := groupeAvec(t, d, "equipe", u.ID, perms.Lecture, "skill-a")

	if got := niveauDe(t, d, u, "shared/skills/skill-a/SKILL.md"); got != perms.Lecture {
		t.Fatalf("prérequis : %v", got)
	}
	if err := d.SortChemin(id, "shared/skills/skill-a"); err != nil {
		t.Fatalf("SortChemin : %v", err)
	}
	if got := niveauDe(t, d, u, "shared/skills/skill-a/SKILL.md"); got != perms.Invisible {
		t.Errorf("après sortie du groupe = %v, attendu invisible", got)
	}
	rules, _ := d.Rules(u.ID)
	for _, r := range rules {
		if r.Path == "shared/skills/skill-a" {
			t.Errorf("une règle orpheline est restée dans permissions : %v", r)
		}
	}
	// Retirer le membre du groupe ferme aussi, sans toucher permissions.
	d.RangeChemin(id, "shared/skills/skill-a", "skill", "skill-a")
	if err := d.RetireMembreGroupe(id, u.ID); err != nil {
		t.Fatalf("RetireMembreGroupe : %v", err)
	}
	if got := niveauDe(t, d, u, "shared/skills/skill-a/SKILL.md"); got != perms.Invisible {
		t.Errorf("après retrait du membre = %v, attendu invisible", got)
	}
}

// TestSupprimerUnGroupeFermeSesSkills : la suppression en cascade doit fermer
// les accès, jamais les laisser pendre.
func TestSupprimerUnGroupeFermeSesSkills(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	id := groupeAvec(t, d, "equipe", u.ID, perms.Ecriture, "skill-a", "skill-b")

	if err := d.SupprimeGroupe(id); err != nil {
		t.Fatalf("SupprimeGroupe : %v", err)
	}
	for _, slug := range []string{"skill-a", "skill-b"} {
		if got := niveauDe(t, d, u, "shared/skills/"+slug+"/SKILL.md"); got != perms.Invisible {
			t.Errorf("%s après suppression du groupe = %v, attendu invisible", slug, got)
		}
	}
	if groupes, err := d.ListGroupes(); err != nil || len(groupes) != 0 {
		t.Errorf("le groupe survit à sa suppression (%d, %v)", len(groupes), err)
	}
}

// TestSkillDansPlusieursGroupes : LE RENVERSEMENT DU 25/08.
//
// Jusque-là, ranger un skill ailleurs le DÉPLAÇAIT - « un skill est dans au
// plus un groupe » (21/08), imposé par une clé primaire sur le slug seul. La
// lecture cohorte rend la règle fausse : un skill sert les opérations ET le
// marketing, comme un dossier sert les salariés ET les managers.
//
// Ce test remplace TestSkillDansUnSeulGroupe, qui figeait la règle d'avant.
func TestSkillDansPlusieursGroupes(t *testing.T) {
	d := newDB(t)
	lecteur, _ := d.CreateUser("lecteur", "mdp", perms.Invisible, false)
	ecrivain, _ := d.CreateUser("ecrivain", "mdp", perms.Invisible, false)
	premier := groupeAvec(t, d, "premier", lecteur.ID, perms.Lecture, "skill-a")
	second := groupeAvec(t, d, "second", ecrivain.ID, perms.Ecriture)

	if err := d.RangeChemin(second, "shared/skills/skill-a", "skill", "skill-a"); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	// Les DEUX groupes ouvrent, chacun au niveau de son membre.
	if got := niveauDe(t, d, lecteur, "shared/skills/skill-a/SKILL.md"); got != perms.Lecture {
		t.Errorf("le premier groupe a perdu son accès en rangeant ailleurs : %v", got)
	}
	if got := niveauDe(t, d, ecrivain, "shared/skills/skill-a/SKILL.md"); got != perms.Ecriture {
		t.Errorf("le second groupe n'ouvre pas : %v", got)
	}
	// Sortir d'UN groupe laisse dans l'autre.
	if err := d.SortChemin(second, "shared/skills/skill-a"); err != nil {
		t.Fatalf("SortChemin : %v", err)
	}
	if got := niveauDe(t, d, lecteur, "shared/skills/skill-a/SKILL.md"); got != perms.Lecture {
		t.Errorf("sortir du second groupe a fermé le premier : %v", got)
	}
	if got := niveauDe(t, d, ecrivain, "shared/skills/skill-a/SKILL.md"); got != perms.Invisible {
		t.Errorf("le second groupe ouvre encore après la sortie : %v", got)
	}
	_ = premier
}

// TestCohortesSAdditionnent : deux cohortes se contredisent sur le même chemin,
// le compte est dans les deux. LE PLUS PERMISSIF GAGNE.
//
// Sans la clause de db.Rules, les deux règles portent le même chemin donc la
// même profondeur, et le départage fail-closed de perms.Effective prend la plus
// restrictive : inscrire quelqu'un dans une deuxième cohorte lui FERMERAIT une
// porte. C'est un effet qui ne se voit nulle part à l'écran.
func TestCohortesSAdditionnent(t *testing.T) {
	d := newDB(t)
	caroline, _ := d.CreateUser("caroline", "mdp", perms.Invisible, false)
	salaries := groupeAvec(t, d, "Salariés", caroline.ID, perms.Invisible, "rh")
	managers := groupeAvec(t, d, "Managers", caroline.ID, perms.Lecture)
	if err := d.RangeChemin(managers, "shared/skills/rh", "skill", "rh"); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	if got := niveauDe(t, d, caroline, "shared/skills/rh/SKILL.md"); got != perms.Lecture {
		t.Errorf("membre de Salariés(invisible) + Managers(lecture) = %v, lecture attendue", got)
	}
	// Dans l'autre sens : l'ordre d'inscription ne décide de rien.
	if err := d.SetMembreGroupe(salaries, caroline.ID, perms.Ecriture); err != nil {
		t.Fatalf("SetMembreGroupe : %v", err)
	}
	if got := niveauDe(t, d, caroline, "shared/skills/rh/SKILL.md"); got != perms.Ecriture {
		t.Errorf("Salariés(ecriture) + Managers(lecture) = %v, ecriture attendue", got)
	}
}

// TestRegleIndividuelleLEmporteSurPlusieursCohortes : le barreau 1 de l'échelle
// tient au-dessus du barreau 2. Une exception individuelle referme ce que DEUX
// cohortes ouvrent - sinon on ne pourrait plus faire d'exception du tout.
func TestRegleIndividuelleLEmporteSurPlusieursCohortes(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	a := groupeAvec(t, d, "a", u.ID, perms.Lecture, "skill-a")
	b := groupeAvec(t, d, "b", u.ID, perms.Ecriture)
	if err := d.RangeChemin(b, "shared/skills/skill-a", "skill", "skill-a"); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	if got := niveauDe(t, d, u, "shared/skills/skill-a/SKILL.md"); got != perms.Ecriture {
		t.Fatalf("prérequis : %v", got)
	}
	if err := d.SetPermission(u.ID, "shared/skills/skill-a", perms.Invisible); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	if got := niveauDe(t, d, u, "shared/skills/skill-a/SKILL.md"); got != perms.Invisible {
		t.Errorf("l'exception individuelle ne referme plus : %v", got)
	}
	_ = a
}

// TestOrdreDesReglesDeterministe : db.Rules replie les cohortes dans une MAP, et
// l'ordre de parcours d'une map Go n'est pas spécifié. Sans le tri, deux appels
// rendraient les mêmes règles dans un ordre différent.
func TestOrdreDesReglesDeterministe(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	groupeAvec(t, d, "g", u.ID, perms.Lecture,
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel")

	premier, err := d.Rules(u.ID)
	if err != nil {
		t.Fatalf("Rules : %v", err)
	}
	for i := 0; i < 100; i++ {
		suivant, err := d.Rules(u.ID)
		if err != nil {
			t.Fatalf("Rules : %v", err)
		}
		if len(suivant) != len(premier) {
			t.Fatalf("appel %d : %d règles au lieu de %d", i, len(suivant), len(premier))
		}
		for j := range premier {
			if suivant[j] != premier[j] {
				t.Fatalf("appel %d : règle %d = %+v, attendu %+v", i, j, suivant[j], premier[j])
			}
		}
	}
}

// TestGroupeSansMembreNOuvreRien : un groupe est un contenant, pas un droit.
func TestGroupeSansMembreNOuvreRien(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	id, err := d.CreerGroupe("vide")
	if err != nil {
		t.Fatalf("CreerGroupe : %v", err)
	}
	if err := d.RangeChemin(id, "shared/skills/skill-a", "skill", "skill-a"); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	if got := niveauDe(t, d, u, "shared/skills/skill-a/SKILL.md"); got != perms.Invisible {
		t.Errorf("un groupe sans membre ouvre un accès : %v", got)
	}
}

// TestGroupesDuCheminFiltreLeGenre : les deux écrans lisent la même table.
//
// Sans le filtre, une ligne rangée comme DOSSIER remonterait sur la ligne du
// SKILL de même chemin : l'écran des skills la cocherait, la décocher
// supprimerait la ligne dossier, et le panneau du groupe - qui, lui, filtre -
// afficherait pendant ce temps un groupe qui ne contient pas ce skill. Un geste
// sur un écran détruisant l'état de l'autre.
func TestGroupesDuCheminFiltreLeGenre(t *testing.T) {
	d := newDB(t)
	ops, err := d.CreerGroupe("Ops")
	if err != nil {
		t.Fatalf("CreerGroupe : %v", err)
	}
	if err := d.RangeChemin(ops, "shared/skills/veille", "dossier", ""); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	if ids, err := d.GroupesDuChemin("shared/skills/veille", "skill"); err != nil || len(ids) != 0 {
		t.Errorf("une ligne dossier remonte comme skill : %v (err %v)", ids, err)
	}
	if ids, err := d.GroupesDuChemin("shared/skills/veille", "dossier"); err != nil || len(ids) != 1 {
		t.Errorf("la ligne dossier ne remonte pas comme dossier : %v (err %v)", ids, err)
	}
	// Elle ouvre bien l'accès malgré tout : le genre décide du rendu, jamais de
	// la résolution.
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	if err := d.SetMembreGroupe(ops, u.ID, perms.Lecture); err != nil {
		t.Fatalf("SetMembreGroupe : %v", err)
	}
	if got := niveauDe(t, d, u, "shared/skills/veille/SKILL.md"); got != perms.Lecture {
		t.Errorf("le genre a décidé de la résolution : %v", got)
	}
}

// TestCohortesSAdditionnentEntreProfondeurs : Q1, tranchée le 25/08.
//
// LE CAS QUI A BLOQUÉ L'INCRÉMENT 4. Le repli au plus permissif ne valait que
// pour deux chemins identiques ; entre profondeurs, perms.Effective reprenait la
// main et l'ancêtre le plus profond gagnait, quel que soit son niveau. Inscrire
// quelqu'un dans une deuxième cohorte lui RETIRAIT donc un accès.
func TestCohortesSAdditionnentEntreProfondeurs(t *testing.T) {
	d := newDB(t)
	caroline, _ := d.CreateUser("caroline", "mdp", perms.Invisible, false)

	salaries, err := d.CreerGroupe("Salariés")
	if err != nil {
		t.Fatalf("CreerGroupe : %v", err)
	}
	if err := d.SetMembreGroupe(salaries, caroline.ID, perms.Ecriture); err != nil {
		t.Fatalf("SetMembreGroupe : %v", err)
	}
	if err := d.RangeChemin(salaries, "clients", "dossier", ""); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	if got := niveauDe(t, d, caroline, "clients/acme/note.md"); got != perms.Ecriture {
		t.Fatalf("prérequis : %v", got)
	}

	// La deuxième cohorte, plus précise et plus basse.
	prestataires, _ := d.CreerGroupe("Prestataires")
	if err := d.SetMembreGroupe(prestataires, caroline.ID, perms.Lecture); err != nil {
		t.Fatalf("SetMembreGroupe : %v", err)
	}
	if err := d.RangeChemin(prestataires, "clients/acme", "dossier", ""); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	if got := niveauDe(t, d, caroline, "clients/acme/note.md"); got != perms.Ecriture {
		t.Errorf("la deuxième cohorte a RETIRÉ l'écriture : %v", got)
	}
	// Et dans l'autre sens : la cohorte basse ouvre bien ce que la haute ferme.
	if err := d.SetMembreGroupe(salaries, caroline.ID, perms.Invisible); err != nil {
		t.Fatalf("SetMembreGroupe : %v", err)
	}
	if got := niveauDe(t, d, caroline, "clients/acme/note.md"); got != perms.Lecture {
		t.Errorf("la cohorte basse n'ouvre pas ce que la haute ferme : %v", got)
	}
	// Le voisin, lui, n'est couvert que par la cohorte haute.
	if got := niveauDe(t, d, caroline, "clients/autre/note.md"); got != perms.Invisible {
		t.Errorf("l'ouverture de clients/acme a fui vers le voisin : %v", got)
	}
}

// TestExceptionIndividuelleNeVautQuePourSonNoeud : le masque individuel
// s'applique à l'émission, pas avant le repli. Une exception posée sur
// `clients` ne retire pas à la cohorte le droit d'ouvrir `clients/acme`.
func TestExceptionIndividuelleNeVautQuePourSonNoeud(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	g, _ := d.CreerGroupe("Salariés")
	d.SetMembreGroupe(g, u.ID, perms.Ecriture)
	d.RangeChemin(g, "clients", "dossier", "")
	d.RangeChemin(g, "clients/acme", "dossier", "")

	if err := d.SetPermission(u.ID, "clients", perms.Invisible); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	if got := niveauDe(t, d, u, "clients/autre/note.md"); got != perms.Invisible {
		t.Errorf("l'exception ne ferme pas son propre nœud : %v", got)
	}
	if got := niveauDe(t, d, u, "clients/acme/note.md"); got != perms.Ecriture {
		t.Errorf("l'exception sur clients a emporté clients/acme : %v", got)
	}
}

// TestCohorteOuvreLeDossierDesSkillsMalgreLeDefautAutomatique : Q2, tranchée le
// 25/08.
//
// CreateUser pose `shared/skills = invisible` sur CHAQUE compte, atomiquement.
// Sans la distinction d'origine, ce défaut masquait toute cohorte portant le
// DOSSIER des skills - le premier geste évident une fois les dossiers
// rangeables - et la cohorte était muette, sans un message nulle part.
func TestCohorteOuvreLeDossierDesSkillsMalgreLeDefautAutomatique(t *testing.T) {
	d := newDB(t)
	caroline, _ := d.CreateUser("caroline", "mdp", perms.Lecture, false)

	// Le défaut automatique est bien là, et il ferme.
	if got := niveauDe(t, d, caroline, "shared/skills/rh/SKILL.md"); got != perms.Invisible {
		t.Fatalf("prérequis : le défaut automatique ne ferme pas (%v)", got)
	}

	g, _ := d.CreerGroupe("Salariés")
	d.SetMembreGroupe(g, caroline.ID, perms.Lecture)
	if err := d.RangeChemin(g, "shared/skills", "dossier", ""); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	if got := niveauDe(t, d, caroline, "shared/skills/rh/SKILL.md"); got != perms.Lecture {
		t.Errorf("la cohorte est muette derrière le défaut automatique : %v", got)
	}

	// Une EXCEPTION, elle, reprend la main sur la cohorte.
	if err := d.SetPermission(caroline.ID, "shared/skills", perms.Invisible); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	if got := niveauDe(t, d, caroline, "shared/skills/rh/SKILL.md"); got != perms.Invisible {
		t.Errorf("l'exception ne reprend pas la main sur la cohorte : %v", got)
	}
}

// TestMigrationOrigineMarqueLesDefautsExistants : sur une base d'avant, les
// lignes qui portent la signature exacte du défaut automatique sont marquées, et
// elles seules.
func TestMigrationOrigineMarqueLesDefautsExistants(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Lecture, false)
	// Une exception délibérée, à un autre niveau, sur le même chemin.
	autre, _ := d.CreateUser("celia", "mdp", perms.Lecture, false)
	if err := d.SetPermission(autre.ID, "shared/skills", perms.Ecriture); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	// On efface la marque pour rejouer l'état d'avant la migration.
	if _, err := d.sql.Exec(`UPDATE permissions SET origine = ?`, OrigineException); err != nil {
		t.Fatalf("retour à l'état d'avant : %v", err)
	}
	if _, err := d.sql.Exec(`ALTER TABLE permissions DROP COLUMN origine`); err != nil {
		t.Fatalf("suppression de la colonne : %v", err)
	}
	if err := d.migreOrigine(); err != nil {
		t.Fatalf("migreOrigine : %v", err)
	}

	var origine string
	d.sql.QueryRow(`SELECT origine FROM permissions WHERE user_id = ? AND path = 'shared/skills'`,
		u.ID).Scan(&origine)
	if origine != OrigineDefaut {
		t.Errorf("le défaut automatique n'est pas marqué : %q", origine)
	}
	d.sql.QueryRow(`SELECT origine FROM permissions WHERE user_id = ? AND path = 'shared/skills'`,
		autre.ID).Scan(&origine)
	if origine != OrigineException {
		t.Errorf("une écriture délibérée a été marquée comme défaut : %q", origine)
	}
}
