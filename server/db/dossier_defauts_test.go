package db

import (
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// LE CAS DE RÉFÉRENCE (Colin, 25/08) : un second cerveau d'entreprise où tout le
// monde lit par défaut, sauf le dossier RH. Le salarié n° 36 doit être dehors
// AVANT d'avoir un compte - là où N règles individuelles se rejouent à chaque
// embauche et finissent par être oubliées.

func TestDefautDeDossierFermePourTousEtPourLesFuturs(t *testing.T) {
	d := newDB(t)
	ancien, _ := d.CreateUser("ancien", "mdp", perms.Lecture, false)
	if got := niveauDe(t, d, ancien, "rh/salaires.md"); got != perms.Lecture {
		t.Fatalf("prérequis : le défaut du compte n'ouvre pas (%v)", got)
	}

	if err := d.SetDefautDossier("rh", perms.Invisible); err != nil {
		t.Fatalf("SetDefautDossier : %v", err)
	}
	if got := niveauDe(t, d, ancien, "rh/salaires.md"); got != perms.Invisible {
		t.Errorf("le défaut du dossier ne ferme pas : %v", got)
	}
	// Le reste du cerveau n'a pas bougé.
	if got := niveauDe(t, d, ancien, "clients/note.md"); got != perms.Lecture {
		t.Errorf("le défaut du dossier a fui hors de son dossier : %v", got)
	}

	// LE SALARIÉ N° 36 : créé APRÈS la pose du défaut, sans qu'aucune règle
	// individuelle n'ait été écrite pour lui.
	nouveau, _ := d.CreateUser("nouveau", "mdp", perms.Lecture, false)
	if got := niveauDe(t, d, nouveau, "rh/salaires.md"); got != perms.Invisible {
		t.Errorf("le salarié n° 36 est entré dans les RH le jour de son embauche : %v", got)
	}
	regles, _ := d.Rules(nouveau.ID)
	for _, r := range regles {
		if r.Path == "rh" {
			// La règle vient du dossier, pas de `permissions` : rien n'a été
			// écrit pour ce compte.
			var n int
			d.sql.QueryRow(`SELECT count(*) FROM permissions WHERE user_id = ? AND path = 'rh'`,
				nouveau.ID).Scan(&n)
			if n != 0 {
				t.Error("une règle individuelle a été écrite pour un compte que le geste ne visait pas")
			}
		}
	}
}

// TestUneCohorteRouvreCeQueLeDossierFerme : les deux mécanismes ont un métier
// chacun. Le dossier ferme, la cohorte ouvre.
func TestUneCohorteRouvreCeQueLeDossierFerme(t *testing.T) {
	d := newDB(t)
	caroline, _ := d.CreateUser("caroline", "mdp", perms.Lecture, false)
	if err := d.SetDefautDossier("rh", perms.Invisible); err != nil {
		t.Fatalf("SetDefautDossier : %v", err)
	}
	if got := niveauDe(t, d, caroline, "rh/salaires.md"); got != perms.Invisible {
		t.Fatalf("prérequis : %v", got)
	}

	rh, _ := d.CreerGroupe("RH")
	d.SetMembreGroupe(rh, caroline.ID, perms.Ecriture)
	if err := d.RangeChemin(rh, "rh", "dossier", ""); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}
	if got := niveauDe(t, d, caroline, "rh/salaires.md"); got != perms.Ecriture {
		t.Errorf("la cohorte ne rouvre pas ce que le dossier ferme : %v", got)
	}

	// Et une exception individuelle reste au-dessus de tout.
	if err := d.SetPermission(caroline.ID, "rh", perms.Lecture); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	if got := niveauDe(t, d, caroline, "rh/salaires.md"); got != perms.Lecture {
		t.Errorf("l'exception ne reprend pas la main : %v", got)
	}
}

// TestRetirerLeDefautDeDossierRendLeNiveauDAvant : comme les cohortes, le défaut
// du dossier ne persiste rien dans `permissions` - rien à réparer quand on le
// retire.
func TestRetirerLeDefautDeDossierRendLeNiveauDAvant(t *testing.T) {
	d := newDB(t)
	u, _ := d.CreateUser("achille", "mdp", perms.Lecture, false)
	d.SetDefautDossier("rh", perms.Invisible)
	if got := niveauDe(t, d, u, "rh/salaires.md"); got != perms.Invisible {
		t.Fatalf("prérequis : %v", got)
	}
	if err := d.SupprimeDefautDossier("rh"); err != nil {
		t.Fatalf("SupprimeDefautDossier : %v", err)
	}
	if got := niveauDe(t, d, u, "rh/salaires.md"); got != perms.Lecture {
		t.Errorf("le niveau d'avant n'est pas rendu : %v", got)
	}
	var n int
	d.sql.QueryRow(`SELECT count(*) FROM permissions WHERE user_id = ?`, u.ID).Scan(&n)
	if n != 1 { // seulement le défaut automatique sur shared/skills
		t.Errorf("%d règle(s) dans permissions, seul le défaut automatique attendu", n)
	}
}
