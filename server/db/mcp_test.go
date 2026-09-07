package db

// mcp_test.go : les jetons MCP au niveau de la base.
//
// Ces tests vivent ICI et pas dans `server/api` pour une raison précise : ils
// ont besoin d'écrire en base une valeur qu'aucun chemin de production
// n'écrit. La première version exposait pour ça un `CorrompsNiveau…PourTest`
// dans le code de production - le seul crochet de test du dépôt entier, exporté,
// compilé dans le binaire. Depuis le paquet `db`, `d.sql` suffit.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func baseDeTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// Le plafond soustrait, et ne peut jamais étendre. C'est LA propriété qui rend
// défendable un jeton posé dans une routine cloud.
//
// Passe par le vrai chemin - émission puis résolution du jeton - et pas par une
// structure montée à la main : c'est la chaîne complète qui doit tenir.
func TestPlafondDuJetonSoustraitEtNEtendJamais(t *testing.T) {
	d := baseDeTest(t)

	// Un admin, écriture partout.
	colin, err := d.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	// Un compte fermé, lecture sur un seul dossier.
	achille, _ := d.CreateUser("achille", "motdepasse", perms.Invisible, false)
	d.SetPermission(achille.ID, "public", perms.Lecture)

	resout := func(jeton string) *PorteurMCP {
		p, err := d.PorteurParJetonMCP(jeton)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	niveau := func(p *PorteurMCP, chemin string) perms.Level {
		n, err := d.EffectifMCP(p, chemin)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Jeton `lecture` sur le compte admin : il RAMÈNE à la lecture.
	jLecture, _ := d.IssueMCPToken(colin.ID, "routine", perms.Lecture)
	bride := resout(jLecture)
	if n := niveau(bride, "prive/interne.md"); n != perms.Lecture {
		t.Errorf("jeton lecture sur compte écriture : %v, attendu lecture", n)
	}
	if peut, _ := d.PeutEcrireMCP(bride, "prive/interne.md"); peut {
		t.Error("un jeton lecture a obtenu l'écriture sur un compte admin")
	}

	// Le même compte, jeton `ecriture` : il retrouve son droit.
	jEcriture, _ := d.IssueMCPToken(colin.ID, "poste", perms.Ecriture)
	if peut, _ := d.PeutEcrireMCP(resout(jEcriture), "prive/interne.md"); !peut {
		t.Error("un jeton écriture n'a pas l'écriture sur un compte qui l'a")
	}

	// LE SENS INVERSE, et c'est l'invariant qui compte : un jeton `ecriture` ne
	// donne PAS l'écriture à un compte qui ne l'a pas. Un jeton MCP ne peut
	// rien qu'un humain ne pourrait, y compris quand son plafond est haut.
	jHaut, _ := d.IssueMCPToken(achille.ID, "routine", perms.Ecriture)
	monte := resout(jHaut)
	if peut, _ := d.PeutEcrireMCP(monte, "public/guide.md"); peut {
		t.Error("un jeton écriture a ÉTENDU les droits de son compte : le plafond n'est pas un plancher")
	}
	if peut, _ := d.PeutLireMCP(monte, "public/guide.md"); !peut {
		t.Error("le compte a la lecture, le jeton la lui retire")
	}
	// Et il ne rend pas visible ce que le compte ne voit pas.
	if peut, _ := d.PeutLireMCP(monte, "prive/interne.md"); peut {
		t.Error("un jeton a rendu visible un chemin invisible pour son compte")
	}
}

// Une ligne corrompue ne se replie JAMAIS sur un niveau permissif.
//
// La branche fail-closed n'était couverte par rien : le test d'origine
// interrogeait un jeton inexistant, qui sort en ErrNotFound par `sql.ErrNoRows`
// sans jamais atteindre `ParseLevel`. Une mutation du repli vers `ecriture`
// laissait toute la suite verte.
func TestNiveauMaxIllisibleNeDonneAucunDroit(t *testing.T) {
	d := baseDeTest(t)
	u, _ := d.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	jeton, err := d.IssueMCPToken(u.ID, "banc", perms.Ecriture)
	if err != nil {
		t.Fatal(err)
	}
	// Le VRAI jeton, sur une ligne devenue illisible : c'est le seul chemin qui
	// atteint la branche.
	if _, err := d.sql.Exec(`UPDATE mcp_jetons SET niveau_max = ?`, "n_importe_quoi"); err != nil {
		t.Fatal(err)
	}

	porteur, err := d.PorteurParJetonMCP(jeton)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("résolution d'un niveau illisible : err=%v porteur=%+v, attendu ErrNotFound", err, porteur)
	}

	// Et l'écran d'admin se plaint au lieu d'afficher « invisible » - ce serait
	// rassurant, faux, et justement la valeur qu'`IssueMCPToken` refuse.
	if _, err := d.ListJetonsMCP(u.ID); err == nil {
		t.Error("un niveau_max illisible est passé sans erreur dans la liste")
	}
}

// Révoquer un id inconnu n'est pas la même chose que re-révoquer : sinon
// l'écran d'admin dit « coupé » sur une faute de frappe.
func TestRevocationDistingueInconnuDeDejaRevoque(t *testing.T) {
	d := baseDeTest(t)
	u, _ := d.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	if _, err := d.IssueMCPToken(u.ID, "banc", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	liste, err := d.ListJetonsMCP(u.ID)
	if err != nil || len(liste) != 1 {
		t.Fatalf("liste : %v (%d)", err, len(liste))
	}

	if err := d.RevoqueJetonMCPDuCompte(u.ID, liste[0].ID); err != nil {
		t.Fatalf("première révocation : %v", err)
	}
	// Idempotent : re-révoquer ne se plaint pas, et ne rebouge pas la date.
	avant, _ := d.ListJetonsMCP(u.ID)
	if err := d.RevoqueJetonMCPDuCompte(u.ID, liste[0].ID); err != nil {
		t.Errorf("seconde révocation : %v, attendu nil (idempotent)", err)
	}
	apres, _ := d.ListJetonsMCP(u.ID)
	if avant[0].RevoqueLe != apres[0].RevoqueLe {
		t.Errorf("la date de révocation a bougé : %q -> %q", avant[0].RevoqueLe, apres[0].RevoqueLe)
	}

	if err := d.RevoqueJetonMCPDuCompte(u.ID, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("id inconnu : %v, attendu ErrNotFound", err)
	}

	// LE PÉRIMÈTRE : le jeton d'un AUTRE compte est introuvable, pas coupé.
	autre, _ := d.CreateUser("achille", "motdepasse", perms.Lecture, false)
	sien, _ := d.IssueMCPToken(autre.ID, "à achille", perms.Lecture)
	listeAutre, _ := d.ListJetonsMCP(autre.ID)
	if err := d.RevoqueJetonMCPDuCompte(u.ID, listeAutre[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("révocation du jeton d'un autre : %v, attendu ErrNotFound", err)
	}
	if _, err := d.PorteurParJetonMCP(sien); err != nil {
		t.Errorf("le jeton d'achille a été coupé par colin : %v", err)
	}
}

// Un plafond `invisible` serait un jeton qui ne peut rien : refusé à la
// création, plutôt qu'expliqué plus tard dans l'interface.
func TestPlafondInvisibleRefuseALaCreation(t *testing.T) {
	d := baseDeTest(t)
	u, _ := d.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	if _, err := d.IssueMCPToken(u.ID, "muet", perms.Invisible); err == nil {
		t.Error("un jeton au plafond invisible a été créé")
	}
}

// Le clair n'existe qu'une fois. La base ne porte que le SHA-256, et deux
// jetons émis coup sur coup sont distincts.
func TestLeJetonEnClairNeVitQuALaCreation(t *testing.T) {
	d := baseDeTest(t)
	u, _ := d.CreateUser("colin", "motdepasse", perms.Ecriture, true)

	a, _ := d.IssueMCPToken(u.ID, "un", perms.Lecture)
	b, _ := d.IssueMCPToken(u.ID, "deux", perms.Lecture)
	if a == b || a == "" {
		t.Fatalf("jetons non distincts ou vides : %q / %q", a, b)
	}
	var hashes []string
	rows, err := d.sql.Query(`SELECT hash FROM mcp_jetons`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		rows.Scan(&h)
		hashes = append(hashes, h)
	}
	for _, h := range hashes {
		if h == a || h == b {
			t.Error("le jeton en clair est stocké en base")
		}
	}
	// Et il reste résoluble : le hash correspond bien.
	if _, err := d.PorteurParJetonMCP(a); err != nil {
		t.Errorf("le jeton émis n'est pas résoluble : %v", err)
	}
}
