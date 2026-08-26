package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
	"github.com/colindargent/vecu/server/storage"
)

func newDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open : %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestBootstrapAdmin(t *testing.T) {
	d := newDB(t)

	// Sans variables : no-op.
	if err := bootstrapAdmin(d, "", ""); err != nil {
		t.Fatalf("sans variables : %v", err)
	}
	if ok, _ := d.HasUsers(); ok {
		t.Fatal("aucun compte ne devait être créé")
	}

	// Variable seule (dans les deux sens) : erreur explicite.
	if err := bootstrapAdmin(d, "colin", ""); err == nil {
		t.Error("user sans password : erreur attendue")
	}
	if err := bootstrapAdmin(d, "", "mdp-initial"); err == nil {
		t.Error("password sans user : erreur attendue")
	}

	// Mot de passe trop court : refusé, et le message ne contient jamais le
	// mot de passe (contrat : aucun secret dans logs/erreurs).
	if err := bootstrapAdmin(d, "colin", "court"); err == nil {
		t.Error("mot de passe < 8 : erreur attendue")
	} else if strings.Contains(err.Error(), "court") {
		t.Error("le message d'erreur contient le mot de passe")
	}

	// Base vide + variables : l'admin est créé, écriture à la racine.
	if err := bootstrapAdmin(d, "colin", "mdp-initial"); err != nil {
		t.Fatalf("bootstrap : %v", err)
	}
	u, err := d.Authenticate("colin", "mdp-initial")
	if err != nil || !u.IsAdmin || u.DefaultLevel != perms.Ecriture {
		t.Fatalf("admin bootstrappé incorrect : %+v (err %v)", u, err)
	}

	// Base peuplée : jamais de re-création ni de reset de mot de passe, même
	// avec de nouvelles valeurs.
	if err := bootstrapAdmin(d, "colin", "autre-mdp"); err != nil {
		t.Fatalf("re-bootstrap : %v", err)
	}
	if _, err := d.Authenticate("colin", "autre-mdp"); !errors.Is(err, db.ErrNotFound) {
		t.Error("le mot de passe ne doit pas être réinitialisé par le bootstrap")
	}
	if err := bootstrapAdmin(d, "intrus", "x"); err != nil {
		t.Fatalf("bootstrap sur base peuplée : %v", err)
	}
	if _, err := d.Authenticate("intrus", "x"); !errors.Is(err, db.ErrNotFound) {
		t.Error("aucun compte ne doit être créé sur une base peuplée")
	}

	// Username invalide : l'erreur remonte (le serveur refuse de démarrer
	// plutôt que de créer un compte cassé), sans fuiter le mot de passe.
	d2 := newDB(t)
	if err := bootstrapAdmin(d2, "a/b", "mot-de-passe-valide"); err == nil {
		t.Error("username invalide : erreur attendue")
	} else if strings.Contains(err.Error(), "mot-de-passe-valide") {
		t.Error("le message d'erreur contient le mot de passe")
	}
}

// compteAnterieur crée un compte tel qu'il existait AVANT F3 : sans la règle
// `shared/skills=invisible` que CreateUser pose désormais. C'est l'état que la
// migration doit savoir reprendre - et le seul où l'attribution de l'existant a
// un sens (après F3, un skill déposé est revendiqué à l'écriture).
func compteAnterieur(t *testing.T, d *db.DB, nom string, defaut perms.Level, admin bool) *db.User {
	t.Helper()
	u, err := d.CreateUser(nom, "motdepasse", defaut, admin)
	if err != nil {
		t.Fatalf("CreateUser %s : %v", nom, err)
	}
	if err := d.DeletePermission(u.ID, skills.DefaultRoot); err != nil {
		t.Fatalf("DeletePermission %s : %v", nom, err)
	}
	return u
}

func TestBackfillSkills(t *testing.T) {
	d := newDB(t)
	colin := compteAnterieur(t, d, "colin", perms.Ecriture, true)
	achille := compteAnterieur(t, d, "achille", perms.Invisible, false)

	store, err := storage.Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}
	// Skills pré-existants dans le dépôt, sans créateur (comme un import).
	store.Write("shared/skills/alpha/SKILL.md", "---\nname: A\ndescription: d\n---\n", "colin")
	store.Write("shared/skills/beta/SKILL.md", "---\nname: B\ndescription: d\n---\n", "colin")
	store.Write("shared/note.md", "hors skills", "colin") // ne doit pas être traité

	if err := backfillSkills(d, store); err != nil {
		t.Fatalf("backfillSkills : %v", err)
	}

	// 1. La racine des skills est masquée : un skill créé APRÈS la migration est
	// privé par défaut pour tout le monde, admin compris.
	for _, u := range []*db.User{colin, achille} {
		if lvl, _ := d.Effective(u, "shared/skills"); lvl != perms.Invisible {
			t.Fatalf("%s devrait avoir shared/skills=invisible, a %v", u.Username, lvl)
		}
		if lvl, _ := d.Effective(u, "shared/skills/nouveau-venu"); lvl != perms.Invisible {
			t.Fatalf("%s ne devrait pas voir un skill créé après la migration, a %v", u.Username, lvl)
		}
	}

	// 2. L'existant est attribué à l'admin qui pouvait déjà y écrire, et il le
	// garde en écriture.
	for _, slug := range []string{"alpha", "beta"} {
		if id, ok, _ := d.CreatorOf(slug); !ok || id != colin.ID {
			t.Fatalf("créateur %s = (%d,%v), attendu colin=%d", slug, id, ok, colin.ID)
		}
		if lvl, _ := d.Effective(colin, "shared/skills/"+slug); lvl != perms.Ecriture {
			t.Fatalf("colin devrait avoir écriture sur %s (créateur), a %v", slug, lvl)
		}
		// 3. Achille n'avait accès à rien : la migration ne lui donne rien.
		if lvl, _ := d.Effective(achille, "shared/skills/"+slug); lvl != perms.Invisible {
			t.Fatalf("achille ne devrait pas voir %s, a %v", slug, lvl)
		}
	}

	// 4. Idempotence + non-écrasement d'un partage : on partage alpha avec achille,
	// puis on rejoue la migration ; le partage doit survivre.
	if err := d.SetPermission(achille.ID, "shared/skills/alpha", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := backfillSkills(d, store); err != nil {
		t.Fatalf("backfillSkills (2e passe) : %v", err)
	}
	if lvl, _ := d.Effective(achille, "shared/skills/alpha"); lvl != perms.Lecture {
		t.Fatalf("le partage alpha->achille ne doit pas être écrasé par la migration, a %v", lvl)
	}
}

// TestBackfillTopologieProd rejoue la base de production telle qu'elle était le
// 28/07 au matin, avec le compte admin résiduel qui a fait dérailler la première
// version : `Colin` (id le plus petit, admin, aucun poste appairé, aucun droit)
// à côté de `colin` (le compte réellement utilisé).
//
// L'ancienne version attribuait « à l'admin de plus petit id » : tout partait sur
// `Colin`, et `colin` perdait ses 24 skills. Ce test échoue sur ce code-là.
func TestBackfillTopologieProd(t *testing.T) {
	d := newDB(t)
	// id 1 : compte résiduel du 24/07. Admin, mais invisible partout et jamais
	// appairé - il n'a jamais rien synchronisé.
	residuel := compteAnterieur(t, d, "Colin", perms.Invisible, true)
	// id 2 : Achille, accès en écriture à `shared`, niveaux réglés à la main
	// par skill le 26/07.
	achille := compteAnterieur(t, d, "achille", perms.Invisible, false)
	// id 3 : le compte réellement utilisé.
	colin := compteAnterieur(t, d, "colin", perms.Ecriture, true)

	if err := d.SetPermission(achille.ID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	for _, u := range []*db.User{achille, colin} {
		if _, err := d.IssueToken(u.ID, "poste-"+u.Username); err != nil {
			t.Fatalf("IssueToken %s : %v", u.Username, err)
		}
	}

	store, err := storage.Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}
	slugs := []string{"daily", "weekly", "meeting", "compile-knowledge", "dev-scope"}
	for _, s := range slugs {
		store.Write("shared/skills/"+s+"/SKILL.md", "---\nname: "+s+"\ndescription: d\n---\n", "colin")
	}
	// Réglages manuels d'Achille du 26/07 : deux skills masqués, le reste hérité
	// de `shared` en écriture.
	for _, s := range []string{"daily", "compile-knowledge"} {
		if err := d.SetPermission(achille.ID, "shared/skills/"+s, perms.Invisible); err != nil {
			t.Fatal(err)
		}
	}

	// Photo du périmètre de chacun avant migration.
	avant := map[string]map[string]perms.Level{}
	for _, u := range []*db.User{residuel, achille, colin} {
		avant[u.Username] = map[string]perms.Level{}
		for _, s := range slugs {
			lvl, _ := d.Effective(u, "shared/skills/"+s)
			avant[u.Username][s] = lvl
		}
	}

	if err := backfillSkills(d, store); err != nil {
		t.Fatalf("backfillSkills : %v", err)
	}

	// 1. L'invariant : personne ne perd un seul niveau. C'est l'incident du 28/07
	// qui est verrouillé ici.
	for _, u := range []*db.User{residuel, achille, colin} {
		for _, s := range slugs {
			apres, _ := d.Effective(u, "shared/skills/"+s)
			if apres < avant[u.Username][s] {
				t.Fatalf("%s a PERDU l'accès à %s : %v -> %v", u.Username, s, avant[u.Username][s], apres)
			}
		}
	}
	// 2. Concrètement : colin garde ses 5 skills en écriture.
	for _, s := range slugs {
		if lvl, _ := d.Effective(colin, "shared/skills/"+s); lvl != perms.Ecriture {
			t.Fatalf("colin devrait garder l'écriture sur %s, a %v", s, lvl)
		}
	}
	// 3. Achille garde exactement ce qu'il avait, masquages manuels compris.
	for _, s := range slugs {
		attendu := perms.Ecriture
		if s == "daily" || s == "compile-knowledge" {
			attendu = perms.Invisible
		}
		if lvl, _ := d.Effective(achille, "shared/skills/"+s); lvl != attendu {
			t.Fatalf("achille sur %s : attendu %v, a %v", s, attendu, lvl)
		}
	}
	// 4. L'attribution va au compte vivant, jamais au résiduel.
	for _, s := range slugs {
		id, ok, _ := d.CreatorOf(s)
		if !ok {
			t.Fatalf("%s devrait avoir un propriétaire", s)
		}
		if id == residuel.ID {
			t.Fatalf("%s attribué au compte résiduel %q - c'est l'incident du 28/07", s, residuel.Username)
		}
		if id != colin.ID {
			t.Fatalf("%s attribué à l'id %d, attendu colin=%d", s, id, colin.ID)
		}
	}
}

// TestChercherPerteDetecteUneRegression teste le filet lui-même, sur un plan que
// le code de production ne sait pas fabriquer : sans ça, on ne vérifierait que
// l'absence de bug, jamais la présence du garde-fou.
func TestChercherPerteDetecteUneRegression(t *testing.T) {
	users := []db.User{{ID: 1, Username: "colin", DefaultLevel: perms.Ecriture, IsAdmin: true}}
	slugs := []string{"daily"}
	avant := map[int64]map[string]perms.Level{1: {"daily": perms.Ecriture}}

	// Plan sain : le niveau est gelé explicitement, le masque ne l'atteint pas.
	sain := map[int64][]perms.Rule{1: {
		{Path: "shared/skills/daily", Level: perms.Ecriture},
		{Path: "shared/skills", Level: perms.Invisible},
	}}
	if p, ok := chercherPerte(users, slugs, avant, sain); ok {
		t.Fatalf("plan sain signalé en perte : %+v", p)
	}

	// Plan fautif : le masque est posé sans gel - exactement la forme de
	// l'incident du 28/07.
	fautif := map[int64][]perms.Rule{1: {{Path: "shared/skills", Level: perms.Invisible}}}
	p, ok := chercherPerte(users, slugs, avant, fautif)
	if !ok {
		t.Fatal("une perte d'accès non détectée : le filet ne sert à rien")
	}
	if p.compte != "colin" || p.slug != "daily" || p.avant != perms.Ecriture || p.apres != perms.Invisible {
		t.Fatalf("perte mal décrite : %+v", p)
	}
}
