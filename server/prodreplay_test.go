package main

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
	"github.com/colindargent/vecu/server/storage"
)

// Rejeu de la migration contre une copie de la PRODUCTION.
//
// L'incident du 28/07 a échappé à une suite verte parce que tous les tests
// tournaient sur des bases fabriquées à la main : aucune ne contenait le compte
// admin résiduel qui a détourné l'attribution. Ce test comble ce trou en
// rejouant la vraie base et les vrais chemins.
//
// Les artefacts NE SONT PAS versionnés (la base porte des empreintes de mots de
// passe et des jetons de poste ; la liste des chemins décrit le vault). Le test
// se saute donc en l'absence de VECU_PROD_REPLAY, qui doit pointer un dossier
// contenant :
//   - app.db     : copie de /data/app.db du serveur
//   - paths.txt  : sortie de `git -C /data/brain.git ls-tree -r --name-only HEAD`
//
// Mode d'emploi (depuis un dossier lié au projet Railway) :
//
//	railway ssh 'base64 /data/app.db' | tr -d '\n' | base64 -d > /tmp/replay/app.db
//	railway ssh 'git -C /data/brain.git ls-tree -r --name-only HEAD' > /tmp/replay/paths.txt
//	VECU_PROD_REPLAY=/tmp/replay go test ./server/ -run TestRejeuProd -v
func TestRejeuProd(t *testing.T) {
	dir := os.Getenv("VECU_PROD_REPLAY")
	if dir == "" {
		t.Skip("VECU_PROD_REPLAY non défini : rejeu de production sauté")
	}

	d := ouvrirCopie(t, filepath.Join(dir, "app.db"))
	store := depotDepuisChemins(t, filepath.Join(dir, "paths.txt"))

	users, err := d.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers : %v", err)
	}
	slugs, err := slugsDuDepot(store)
	if err != nil {
		t.Fatalf("slugsDuDepot : %v", err)
	}
	if len(slugs) == 0 {
		t.Fatal("aucun skill dans la copie de production : artefacts incohérents")
	}
	t.Logf("copie de production : %d comptes, %d skills", len(users), len(slugs))

	// Périmètre de chacun tel qu'il est en production aujourd'hui, séquelles de
	// l'incident comprises (le compte résiduel porte encore 24 droits d'écriture
	// que la revendication fautive lui avait accordés).
	brut := niveauxDeTous(t, d, users, slugs)

	// Séquence de démarrage réelle, en deux temps.
	if err := reparerIncidentSkills(d); err != nil {
		t.Fatalf("reparerIncidentSkills : %v", err)
	}

	// La réparation est la seule étape autorisée à retirer un droit. Elle ne
	// peut le faire que sur un compte sans poste appairé : sinon un client
	// supprimerait les fichiers en local, c'est-à-dire l'incident lui-même.
	avant := niveauxDeTous(t, d, users, slugs)
	for i := range users {
		u := users[i]
		var retires []string
		for _, s := range slugs {
			if avant[u.Username][s] < brut[u.Username][s] {
				retires = append(retires, s)
			}
		}
		if len(retires) == 0 {
			continue
		}
		postes, err := d.NbDevices(u.ID)
		if err != nil {
			t.Fatalf("NbDevices : %v", err)
		}
		if postes > 0 {
			t.Errorf("la réparation retire %d droit(s) à %q qui a %d poste(s) appairé(s) : suppression locale possible",
				len(retires), u.Username, postes)
		} else {
			t.Logf("réparation : %d droit(s) résiduel(s) retiré(s) à %q (aucun poste appairé, aucun fichier local en jeu)",
				len(retires), u.Username)
		}
	}

	if err := backfillSkills(d, store); err != nil {
		t.Fatalf("backfillSkills : %v", err)
	}

	// 1. L'invariant, sur les vraies données : personne ne perd un niveau.
	for i := range users {
		u := users[i]
		for _, s := range slugs {
			apres, err := d.Effective(&u, skills.DefaultRoot+"/"+s)
			if err != nil {
				t.Fatalf("Effective : %v", err)
			}
			if apres < avant[u.Username][s] {
				t.Errorf("%s a PERDU l'accès à %s : %v -> %v", u.Username, s, avant[u.Username][s], apres)
			}
		}
	}

	// 2. Tout compte qui pouvait écrire un skill le peut encore, et le voit.
	//    C'est la formulation « Colin retrouve ses skills » du plan de reprise.
	for i := range users {
		u := users[i]
		var perdus []string
		for _, s := range slugs {
			if avant[u.Username][s] < perms.Ecriture {
				continue
			}
			if lvl, _ := d.Effective(&u, skills.DefaultRoot+"/"+s); lvl < perms.Ecriture {
				perdus = append(perdus, s)
			}
		}
		if len(perdus) > 0 {
			t.Errorf("%s perd l'écriture sur %d skill(s) : %v", u.Username, len(perdus), perdus)
		}
	}

	// 3. Chaque skill a un propriétaire, et ce propriétaire pouvait déjà y écrire
	//    avant la migration (aucune élévation, aucun compte mort).
	orphelins := []string{}
	for _, s := range slugs {
		id, ok, err := d.CreatorOf(s)
		if err != nil {
			t.Fatalf("CreatorOf : %v", err)
		}
		if !ok {
			orphelins = append(orphelins, s)
			continue
		}
		var proprio *db.User
		for i := range users {
			if users[i].ID == id {
				proprio = &users[i]
			}
		}
		if proprio == nil {
			t.Errorf("%s attribué à un compte inexistant (id %d)", s, id)
			continue
		}
		if avant[proprio.Username][s] < perms.Ecriture {
			t.Errorf("%s attribué à %q qui n'y avait pas l'écriture avant (%v) - élévation de droit",
				s, proprio.Username, avant[proprio.Username][s])
		}
	}
	if len(orphelins) > 0 {
		t.Errorf("%d skill(s) sans propriétaire : %v", len(orphelins), orphelins)
	}

	// 4. Idempotence : un second démarrage ne change rien.
	empreinteA := empreinteDroits(t, d, users, slugs)
	if err := reparerIncidentSkills(d); err != nil {
		t.Fatalf("reparerIncidentSkills (2e passe) : %v", err)
	}
	if err := backfillSkills(d, store); err != nil {
		t.Fatalf("backfillSkills (2e passe) : %v", err)
	}
	if empreinteB := empreinteDroits(t, d, users, slugs); empreinteB != empreinteA {
		t.Error("un second démarrage modifie les droits : la migration n'est pas idempotente")
	}
}

// niveauxDeTous relève le niveau effectif de chaque compte sur chaque skill.
func niveauxDeTous(t *testing.T, d *db.DB, users []db.User, slugs []string) map[string]map[string]perms.Level {
	t.Helper()
	out := map[string]map[string]perms.Level{}
	for i := range users {
		u := users[i]
		out[u.Username] = map[string]perms.Level{}
		for _, s := range slugs {
			lvl, err := d.Effective(&u, skills.DefaultRoot+"/"+s)
			if err != nil {
				t.Fatalf("Effective : %v", err)
			}
			out[u.Username][s] = lvl
		}
	}
	return out
}

// ouvrirCopie duplique la base dans un dossier temporaire : le rejeu ne doit
// jamais écrire dans l'artefact fourni.
func ouvrirCopie(t *testing.T, src string) *db.DB {
	t.Helper()
	octets, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("lecture %s : %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), "app.db")
	if err := os.WriteFile(dst, octets, 0o600); err != nil {
		t.Fatalf("copie base : %v", err)
	}
	d, err := db.Open(dst)
	if err != nil {
		t.Fatalf("db.Open : %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// depotDepuisChemins reconstruit un dépôt portant exactement les chemins de la
// production. La migration ne lit du dépôt que la liste des chemins à HEAD : le
// contenu des fichiers n'entre dans aucune de ses décisions.
func depotDepuisChemins(t *testing.T, src string) *storage.Store {
	t.Helper()
	f, err := os.Open(src)
	if err != nil {
		t.Fatalf("lecture %s : %v", src, err)
	}
	defer f.Close()

	store, err := storage.Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		p := strings.TrimSpace(sc.Text())
		if p == "" {
			continue
		}
		if _, err := store.Write(p, "contenu sans importance pour la migration\n", "rejeu"); err != nil {
			t.Fatalf("écriture %s : %v", p, err)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("parcours %s : %v", src, err)
	}
	t.Logf("dépôt reconstruit : %d chemins", n)
	return store
}

// empreinteDroits sérialise les droits effectifs de tous les comptes sur tous les
// skills, pour comparer deux états.
func empreinteDroits(t *testing.T, d *db.DB, users []db.User, slugs []string) string {
	t.Helper()
	var lignes []string
	for i := range users {
		u := users[i]
		for _, s := range slugs {
			lvl, err := d.Effective(&u, skills.DefaultRoot+"/"+s)
			if err != nil {
				t.Fatalf("Effective : %v", err)
			}
			lignes = append(lignes, u.Username+" "+s+" "+lvl.String())
		}
	}
	sort.Strings(lignes)
	return strings.Join(lignes, "\n")
}
