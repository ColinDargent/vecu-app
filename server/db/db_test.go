package db

import (
	"errors"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func newDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestCreateAndAuthenticate(t *testing.T) {
	d := newDB(t)
	if _, err := d.CreateUser("colin", "motdepasse", perms.Ecriture, true); err != nil {
		t.Fatalf("CreateUser : %v", err)
	}
	u, err := d.Authenticate("colin", "motdepasse")
	if err != nil {
		t.Fatalf("Authenticate : %v", err)
	}
	if u.Username != "colin" || !u.IsAdmin || u.DefaultLevel != perms.Ecriture {
		t.Errorf("utilisateur mal chargé : %+v", u)
	}
	if _, err := d.Authenticate("colin", "faux"); !errors.Is(err, ErrNotFound) {
		t.Errorf("mauvais mdp devrait échouer, obtenu %v", err)
	}
	if _, err := d.Authenticate("inconnu", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("utilisateur inconnu devrait échouer, obtenu %v", err)
	}
}

func TestDuplicateUsername(t *testing.T) {
	d := newDB(t)
	d.CreateUser("colin", "x", perms.Lecture, false)
	if _, err := d.CreateUser("colin", "y", perms.Lecture, false); err == nil {
		t.Error("username en double accepté")
	}
}

func TestTokenLifecycle(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "pw", perms.Lecture, false)
	tok, err := d.IssueToken(u.ID, "macbook")
	if err != nil {
		t.Fatalf("IssueToken : %v", err)
	}
	got, err := d.UserByToken(tok)
	if err != nil {
		t.Fatalf("UserByToken : %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("token résout vers le mauvais user : %d != %d", got.ID, u.ID)
	}
	if _, err := d.UserByToken("jeton-bidon"); !errors.Is(err, ErrNotFound) {
		t.Errorf("jeton inconnu devrait échouer, obtenu %v", err)
	}
}

func TestPermissionsPersistAndResolve(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "pw", perms.Invisible, false)
	// Étend clients en lecture, restreint un sous-dossier.
	if err := d.SetPermission(u.ID, "clients", perms.Lecture); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	d.SetPermission(u.ID, "clients/vdf", perms.Invisible)

	lvl, err := d.Effective(u, "clients/note.md")
	if err != nil {
		t.Fatalf("Effective : %v", err)
	}
	if lvl != perms.Lecture {
		t.Errorf("clients/note.md : attendu Lecture, obtenu %v", lvl)
	}
	lvl, _ = d.Effective(u, "clients/vdf/secret.md")
	if lvl != perms.Invisible {
		t.Errorf("clients/vdf/secret.md : attendu Invisible, obtenu %v", lvl)
	}
	lvl, _ = d.Effective(u, "hors-scope.md")
	if lvl != perms.Invisible {
		t.Errorf("hors périmètre : attendu Invisible (défaut), obtenu %v", lvl)
	}
}

func TestSetPermissionUpsert(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("colin", "pw", perms.Invisible, false)
	d.SetPermission(u.ID, "projets", perms.Lecture)
	d.SetPermission(u.ID, "projets", perms.Ecriture) // remplace, pas de doublon
	rules, _ := d.Rules(u.ID)
	// CreateUser pose aussi « shared/skills=invisible » (privé par défaut) : on ne
	// vérifie ici que la règle « projets », qui doit être unique et à ecriture.
	var projets []perms.Rule
	for _, r := range rules {
		if r.Path == "projets" {
			projets = append(projets, r)
		}
	}
	if len(projets) != 1 || projets[0].Level != perms.Ecriture {
		t.Errorf("upsert cassé : %+v", rules)
	}
}

func TestListUsersEtUserByID(t *testing.T) {
	d := newDB(t)
	d.CreateUser("colin", "pw", perms.Ecriture, true)
	achille, _ := d.CreateUser("achille", "pw", perms.Lecture, false)

	users, err := d.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers : %v", err)
	}
	if len(users) != 2 || users[0].Username != "achille" || users[1].Username != "colin" {
		t.Errorf("attendu [achille colin] triés, obtenu %+v", users)
	}
	if !users[1].IsAdmin || users[1].DefaultLevel != perms.Ecriture {
		t.Errorf("colin : admin+écriture attendus, obtenu %+v", users[1])
	}

	u, err := d.UserByID(achille.ID)
	if err != nil || u.Username != "achille" {
		t.Errorf("UserByID : attendu achille, obtenu %+v (err %v)", u, err)
	}
	if _, err := d.UserByID(9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("UserByID inconnu : attendu ErrNotFound, obtenu %v", err)
	}
}

func TestRevokeToken(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("colin", "pw", perms.Ecriture, false)
	token, _ := d.IssueToken(u.ID, "web")
	if _, err := d.UserByToken(token); err != nil {
		t.Fatalf("jeton frais invalide : %v", err)
	}
	if err := d.RevokeToken(token); err != nil {
		t.Fatalf("RevokeToken : %v", err)
	}
	if _, err := d.UserByToken(token); !errors.Is(err, ErrNotFound) {
		t.Errorf("jeton révoqué : attendu ErrNotFound, obtenu %v", err)
	}
	if err := d.RevokeToken(token); err != nil { // idempotent
		t.Errorf("double révocation : %v", err)
	}
}

func TestDeletePermission(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("colin", "pw", perms.Lecture, false)
	d.SetPermission(u.ID, "clients/vdf/", perms.Invisible)
	if err := d.DeletePermission(u.ID, "clients/vdf"); err != nil { // canon : slash final équivalent
		t.Fatalf("DeletePermission : %v", err)
	}
	lvl, _ := d.Effective(u, "clients/vdf/secret.md")
	if lvl != perms.Lecture {
		t.Errorf("règle retirée : attendu retour à Lecture, obtenu %v", lvl)
	}
	if err := d.DeletePermission(u.ID, "inconnu"); err != nil { // idempotent
		t.Errorf("suppression règle inconnue : %v", err)
	}
}

func TestCreateUserUsernameInvalide(t *testing.T) {
	d := newDB(t)
	for _, mauvais := range []string{"", "a/b", "a b", "a\x01b", "colin!", "é"} {
		if _, err := d.CreateUser(mauvais, "mdp", perms.Lecture, false); err == nil {
			t.Errorf("username %q : création acceptée à tort", mauvais)
		}
	}
	if _, err := d.CreateUser("colin.d_2-ok", "mdp", perms.Lecture, false); err != nil {
		t.Errorf("username valide refusé : %v", err)
	}
}
