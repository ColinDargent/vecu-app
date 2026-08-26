package db

import (
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func TestClaimSkill(t *testing.T) {
	d := newDB(t)
	colin, err := d.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	if err != nil {
		t.Fatalf("CreateUser : %v", err)
	}

	// Slug libre : aucun créateur.
	if _, ok, err := d.CreatorOf("outil"); err != nil || ok {
		t.Fatalf("CreatorOf avant revendication = (%v,%v), attendu (false,nil)", ok, err)
	}

	// Première revendication : gagne, et le grant écriture est posé dans la même
	// transaction (le créateur n'est jamais enfermé hors de son skill).
	gagne, err := d.ClaimSkill("outil", "shared/skills/outil", colin.ID)
	if err != nil || !gagne {
		t.Fatalf("ClaimSkill 1re = (%v,%v), attendu (true,nil)", gagne, err)
	}
	if id, ok, _ := d.CreatorOf("outil"); !ok || id != colin.ID {
		t.Fatalf("CreatorOf = (%d,%v), attendu (colin=%d,true)", id, ok, colin.ID)
	}
	lvl, _ := d.Effective(colin, "shared/skills/outil")
	if lvl != perms.Ecriture {
		t.Fatalf("le créateur devrait avoir écriture sur son skill, a %v", lvl)
	}

	// Seconde revendication du même slug : perd, sans erreur (atomicité PK).
	if gagne, err := d.ClaimSkill("outil", "shared/skills/outil", colin.ID); err != nil || gagne {
		t.Fatalf("ClaimSkill 2e = (%v,%v), attendu (false,nil)", gagne, err)
	}

	// ON DELETE SET NULL : créateur supprimé -> créateur inconnu, sans casser la ligne.
	if _, err := d.sql.Exec(`DELETE FROM users WHERE id = ?`, colin.ID); err != nil {
		t.Fatalf("DELETE user : %v", err)
	}
	if _, ok, _ := d.CreatorOf("outil"); ok {
		t.Fatal("CreatorOf après suppression du créateur devrait être ok=false (user_id NULL)")
	}
}

// TestClaimSkillPurgeLePrePositionnement : un skill naît privé pour tout le
// monde. Une règle posée à sa racine ou plus profond AVANT la revendication ne
// survit pas - sinon le créateur, qui ne gère qu'à la racine, croirait avoir
// coupé un accès resté ouvert par une règle interne. (DAR-164, purge conservée
// après le renversement de F3 du 21/08 : indépendante de l'exemption admin.)
func TestClaimSkillPurgeLePrePositionnement(t *testing.T) {
	d := newDB(t)
	createur, _ := d.CreateUser("colin", "mdp", perms.Ecriture, true)
	tiers, _ := d.CreateUser("achille", "mdp", perms.Invisible, false) // masque shared/skills=invisible posé par CreateUser

	// Règle INTERNE pré-positionnée (plus profonde que la racine), sans partage à
	// la racine : c'est celle que le créateur ne peut PAS recouvrir, puisqu'il
	// n'écrit qu'à la racine.
	d.SetPermission(tiers.ID, "shared/skills/blank-page/interne.md", perms.Ecriture)

	claimed, err := d.ClaimSkill("blank-page", "shared/skills/blank-page", createur.ID)
	if err != nil || !claimed {
		t.Fatalf("ClaimSkill : claimed=%v err=%v", claimed, err)
	}

	tiersRules, _ := d.Rules(tiers.ID)
	// La règle interne ne survit pas : le masque bootstrap reprend la main.
	if lvl := perms.Effective("shared/skills/blank-page/interne.md", tiers.DefaultLevel, tiersRules); lvl != perms.Invisible {
		t.Errorf("la règle interne pré-positionnée a survécu à la revendication : %v", lvl)
	}
	// Le créateur a bien l'écriture à la racine.
	crRules, _ := d.Rules(createur.ID)
	if lvl := perms.Effective("shared/skills/blank-page", createur.DefaultLevel, crRules); lvl != perms.Ecriture {
		t.Errorf("le créateur n'a pas l'écriture sur son skill : %v", lvl)
	}
}

// TestClaimSkillPreserveUnPartageRacine : grandfathering. Un partage légitime
// posé À la racine avant la revendication survit - le créateur pourra le couper
// (handleSetAccesSkill écrit au même chemin), et le backfill du 28/07 le gèle
// pour ne rien faire perdre. C'est ce que la purge ne doit PAS toucher.
func TestClaimSkillPreserveUnPartageRacine(t *testing.T) {
	d := newDB(t)
	createur, _ := d.CreateUser("colin", "mdp", perms.Ecriture, true)
	tiers, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)
	d.SetPermission(tiers.ID, "shared/skills/blank-page", perms.Lecture)

	if _, err := d.ClaimSkill("blank-page", "shared/skills/blank-page", createur.ID); err != nil {
		t.Fatalf("ClaimSkill : %v", err)
	}

	tiersRules, _ := d.Rules(tiers.ID)
	if lvl := perms.Effective("shared/skills/blank-page", tiers.DefaultLevel, tiersRules); lvl != perms.Lecture {
		t.Errorf("la purge a effacé un partage légitime posé à la racine : %v", lvl)
	}
}

// TestClaimSkillNEcrasePasUnVoisinAvecUnderscore : le slug peut contenir « _ »,
// un joker LIKE. Revendiquer « blank_page » ne doit pas purger « blankXpage ».
func TestClaimSkillNEcrasePasUnVoisinAvecUnderscore(t *testing.T) {
	d := newDB(t)
	c, _ := d.CreateUser("colin", "mdp", perms.Ecriture, true)
	tiers, _ := d.CreateUser("achille", "mdp", perms.Invisible, false)

	// Un accès légitime sur un skill VOISIN dont le nom ne diffère que par le
	// caractère à la position du souligné.
	d.SetPermission(tiers.ID, "shared/skills/blankXpage", perms.Lecture)

	if _, err := d.ClaimSkill("blank_page", "shared/skills/blank_page", c.ID); err != nil {
		t.Fatalf("ClaimSkill : %v", err)
	}

	tiersRules, _ := d.Rules(tiers.ID)
	if lvl := perms.Effective("shared/skills/blankXpage", tiers.DefaultLevel, tiersRules); lvl != perms.Lecture {
		t.Errorf("la purge a mordu sur un voisin (underscore non échappé) : %v", lvl)
	}
}
