package main

// L'AUDIT DE J0.1 (DAR-182), joue sur une copie de la base de production.
//
// Le seul critere d'acceptation de DAR-182 n'a jamais ete joue : personne n'a
// diffuse le droit effectif de chaque compte avant et apres la migration des
// cohortes, poussee le 25/08. Six jours de fonctionnement sans plainte n'en
// sont pas une preuve - un droit trop OUVERT ne produit aucune plainte, par
// construction.
//
// Ce n'est pas un test de non-regression : il ne decide de rien, il DIFFUSE.
// La lecture est humaine, et le resultat va dans la spec. Il se saute sans les
// artefacts, comme le rejeu d'a cote.
//
//	VECU_PROD_REPLAY=/tmp/replay go test ./server/ -run TestAuditDroitsProd -v
//
// Mode d'emploi des artefacts : voir prodreplay_test.go.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
)

func TestAuditDroitsProd(t *testing.T) {
	dir := os.Getenv("VECU_PROD_REPLAY")
	if dir == "" {
		t.Skip("VECU_PROD_REPLAY non défini : audit de production sauté")
	}
	d := ouvrirCopie(t, filepath.Join(dir, "app.db"))
	chemins := cheminsDeLaCopie(t, filepath.Join(dir, "paths.txt"))

	users, err := d.ListUsers()
	if err != nil {
		t.Fatal(err)
	}

	// Les chemins PORTEURS d'une decision : la racine de chaque espace, plus
	// tout chemin sur lequel une regle est posee. Auditer les 1201 fichiers
	// noierait le signal ; ceux-ci sont ceux ou quelqu'un a voulu quelque chose.
	interessants := map[string]bool{}
	for _, p := range chemins {
		if i := strings.IndexByte(p, '/'); i > 0 {
			interessants[p[:i]] = true
		}
	}
	for i := range users {
		regles, err := d.Rules(users[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range regles {
			if c := perms.Canon(r.Path); c != "" {
				interessants[c] = true
			}
		}
	}
	cibles := make([]string, 0, len(interessants))
	for c := range interessants {
		cibles = append(cibles, c)
	}
	sort.Strings(cibles)

	t.Logf("audit sur %d chemins porteurs de décision, %d comptes", len(cibles), len(users))

	// Le droit effectif, calcule par LE code de production et pas par une
	// reimplementation : d.Rules porte l'echelle de precedence complete
	// (exception, cohorte, defaut individuel, defaut du dossier, defaut du
	// compte), et perms.Effective la resout.
	type ligne struct {
		chemin  string
		niveaux map[string]perms.Level
	}
	var table []ligne
	for _, c := range cibles {
		l := ligne{chemin: c, niveaux: map[string]perms.Level{}}
		for i := range users {
			u := users[i]
			regles, err := d.Rules(u.ID)
			if err != nil {
				t.Fatal(err)
			}
			l.niveaux[u.Username] = perms.Effective(c, u.DefaultLevel, regles)
		}
		table = append(table, l)
	}

	noms := make([]string, 0, len(users))
	for i := range users {
		noms = append(noms, users[i].Username)
	}
	sort.Strings(noms)

	t.Log("")
	t.Log("DROIT EFFECTIF PAR COMPTE ET PAR CHEMIN")
	t.Logf("  %-46s %s", "chemin", strings.Join(noms, " | "))
	for _, l := range table {
		vals := make([]string, 0, len(noms))
		for _, n := range noms {
			vals = append(vals, l.niveaux[n].String())
		}
		t.Logf("  %-46s %s", l.chemin, strings.Join(vals, " | "))
	}

	// CE QU'ON CHERCHE EN PRIORITE : l'ouverture non voulue. Une fermeture
	// excessive se signale toute seule le jour ou quelqu'un ne trouve plus un
	// fichier ; une ouverture excessive ne se signale jamais.
	t.Log("")
	t.Log("OUVERTURES : chemins ou un compte NON-ADMIN peut ECRIRE")
	for _, l := range table {
		for i := range users {
			u := users[i]
			if !u.IsAdmin && l.niveaux[u.Username] >= perms.Ecriture {
				t.Logf("  %-46s %s", l.chemin, u.Username)
			}
		}
	}
}

// cheminsDeLaCopie lit la liste des chemins telle que le depot de production la
// rend.
func cheminsDeLaCopie(t *testing.T, fichier string) []string {
	t.Helper()
	brut, err := os.ReadFile(fichier)
	if err != nil {
		t.Fatalf("lecture de %s : %v", fichier, err)
	}
	var out []string
	for _, l := range strings.Split(string(brut), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

var _ = db.User{}
