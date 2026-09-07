package db

// LA CAPACITE DE LA SLICE 4 (DAR-196) : une cohorte porte un niveau PAR CHEMIN,
// comme un utilisateur en porte un depuis toujours.
//
// Colin, le 01/09 : « aujourd'hui on peut deja choisir le niveau de visibilite
// d'un utilisateur sur un fichier ou meme un dossier. Ce que je veux, c'est
// qu'on monte ca au niveau de la cohorte. »
//
// Ce banc verrouille les DEUX invariants, qui tirent en sens contraire :
//   - DANS une cohorte, le chemin le plus PROFOND gagne (l'exclusion voulue) ;
//   - ENTRE cohortes, le plus PERMISSIF gagne (25/08, Q1 : une deuxieme cohorte
//     ne retire jamais un acces).

import (
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func baseAvecCohortes(t *testing.T) (*DB, *User) {
	t.Helper()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	u, err := d.CreateUser("marie", "mdp", perms.Lecture, false)
	if err != nil {
		t.Fatal(err)
	}
	return d, u
}

func effectif(t *testing.T, d *DB, u *User, chemin string) perms.Level {
	t.Helper()
	regles, err := d.Rules(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return perms.Effective(chemin, u.DefaultLevel, regles)
}

func TestUneCohorteExclutUnSousDossier(t *testing.T) {
	d, marie := baseAvecCohortes(t)
	// Le dossier est ferme a tout le monde par son defaut : seule la cohorte
	// ouvre, ce qui est la forme que Colin decrit.
	if err := d.SetDefautDossier("shared", perms.Invisible); err != nil {
		t.Fatal(err)
	}
	g, err := d.CreerGroupe("direction")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetMembreGroupe(g, marie.ID, perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := d.RangeChemin(g, "shared", "dossier", ""); err != nil {
		t.Fatal(err)
	}

	// Avant l'exclusion : la cohorte ouvre tout le dossier de base.
	if n := effectif(t, d, marie, "shared/direction/budget.md"); n != perms.Ecriture {
		t.Fatalf("la cohorte devait ouvrir tout son dossier de base, obtenu %v", n)
	}

	// LE GESTE : on retire un sous-dossier de la cohorte.
	if err := d.RangeChemin(g, "shared/direction", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.SetNiveauChemin(g, "shared/direction", perms.Invisible.String()); err != nil {
		t.Fatal(err)
	}

	if n := effectif(t, d, marie, "shared/direction/budget.md"); n != perms.Invisible {
		t.Errorf("le sous-dossier retiré reste ouvert : %v", n)
	}
	// ET LE RESTE DU DOSSIER DE BASE N'A PAS BOUGE. Sans ce contrôle, fermer
	// tout le dossier passerait pour une exclusion réussie.
	if n := effectif(t, d, marie, "shared/public/note.md"); n != perms.Ecriture {
		t.Errorf("l'exclusion a débordé sur le reste du dossier : %v", n)
	}
}

func TestUneDeuxiemeCohorteNeRetireJamaisRien(t *testing.T) {
	d, marie := baseAvecCohortes(t)
	if err := d.SetDefautDossier("shared", perms.Invisible); err != nil {
		t.Fatal(err)
	}
	// Cohorte A ouvre `shared` en écriture.
	a, _ := d.CreerGroupe("A")
	if err := d.SetMembreGroupe(a, marie.ID, perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := d.RangeChemin(a, "shared", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	// Cohorte B porte le MEME sous-dossier en invisible.
	b, _ := d.CreerGroupe("B")
	if err := d.SetMembreGroupe(b, marie.ID, perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := d.RangeChemin(b, "shared/direction", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.SetNiveauChemin(b, "shared/direction", perms.Invisible.String()); err != nil {
		t.Fatal(err)
	}

	// L'EXCLUSION DE B NE FERME PAS CE QUE A OUVRE. C'est le barreau du 25/08 :
	// entrer dans une deuxième cohorte ne retire jamais un accès.
	if n := effectif(t, d, marie, "shared/direction/budget.md"); n != perms.Ecriture {
		t.Errorf("la cohorte B a refermé ce que A ouvrait : %v", n)
	}
}

func TestLeNiveauDuMembreEstUnPlafond(t *testing.T) {
	d, marie := baseAvecCohortes(t)
	if err := d.SetDefautDossier("shared", perms.Invisible); err != nil {
		t.Fatal(err)
	}
	g, _ := d.CreerGroupe("direction")
	// Marie est en LECTURE dans cette cohorte...
	if err := d.SetMembreGroupe(g, marie.ID, perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := d.RangeChemin(g, "shared", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	// ...et le chemin porte l'ECRITURE.
	if err := d.SetNiveauChemin(g, "shared", perms.Ecriture.String()); err != nil {
		t.Fatal(err)
	}
	if n := effectif(t, d, marie, "shared/note.md"); n != perms.Lecture {
		t.Errorf("le niveau du membre doit plafonner celui du chemin, obtenu %v", n)
	}
}

// Le comportement d'AVANT la colonne, qui doit être exactement préservé : un
// chemin sans niveau prend celui du membre. C'est ce qui rend la migration
// incapable de changer le droit de quiconque.
func TestUnCheminSansNiveauPrendCeluiDuMembre(t *testing.T) {
	d, marie := baseAvecCohortes(t)
	if err := d.SetDefautDossier("shared", perms.Invisible); err != nil {
		t.Fatal(err)
	}
	g, _ := d.CreerGroupe("direction")
	if err := d.SetMembreGroupe(g, marie.ID, perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := d.RangeChemin(g, "shared", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	if n := effectif(t, d, marie, "shared/note.md"); n != perms.Lecture {
		t.Errorf("un chemin sans niveau doit prendre celui du membre, obtenu %v", n)
	}
}

func TestPoserUnNiveauSurUnCheminHorsCohorteEchoue(t *testing.T) {
	d, _ := baseAvecCohortes(t)
	g, _ := d.CreerGroupe("direction")
	if err := d.SetNiveauChemin(g, "shared/jamais-range", perms.Invisible.String()); err == nil {
		t.Error("poser un niveau sur un chemin que la cohorte ne porte pas devrait échouer")
	}
}

// L'ORIGINE DOIT CORRESPONDRE AU DROIT AFFICHE A COTE. Rendre « lecture, par la
// cohorte Direction » quand le resolveur dit « invisible » serait pire que ne
// rien dire : le mainteneur irait toucher la cohorte, et le droit ne bougerait
// pas.
//
// On croise les quatre barreaux sur les memes chemins, et on compare a
// perms.Effective, qui est ce que le reste du systeme applique.
func TestLOrigineCorrespondToujoursAuDroitEffectif(t *testing.T) {
	d, marie := baseAvecCohortes(t)
	colin, err := d.CreateUser("colin", "mdp", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	// Les quatre barreaux, tous poses.
	if err := d.SetDefautDossier("shared", perms.Invisible); err != nil { // dossier
		t.Fatal(err)
	}
	g, _ := d.CreerGroupe("direction")
	if err := d.SetMembreGroupe(g, marie.ID, perms.Ecriture); err != nil { // cohorte
		t.Fatal(err)
	}
	if err := d.RangeChemin(g, "shared", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.RangeChemin(g, "shared/direction", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.SetNiveauChemin(g, "shared/direction", perms.Invisible.String()); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPermission(marie.ID, "shared/rh", perms.Lecture); err != nil { // exception
		t.Fatal(err)
	}

	chemins := []string{
		"", "shared", "shared/note.md", "shared/direction", "shared/direction/budget.md",
		"shared/rh", "shared/rh/salaires.md", "shared/skills", "shared/skills/x/SKILL.md",
		"autre", "autre/fichier.md",
	}
	for _, u := range []*User{marie, colin} {
		regles, err := d.Rules(u.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range chemins {
			attendu := perms.Effective(c, u.DefaultLevel, regles)
			obtenu, origine, err := d.DroitAvecOrigine(u, c)
			if err != nil {
				t.Fatal(err)
			}
			if obtenu != attendu {
				t.Errorf("%s sur %q : DroitAvecOrigine dit %v, perms.Effective dit %v",
					u.Username, c, obtenu, attendu)
			}
			if origine.Barreau == "" {
				t.Errorf("%s sur %q : aucun barreau nommé", u.Username, c)
			}
			if origine.Barreau == "cohorte" && origine.Cohorte == "" {
				t.Errorf("%s sur %q : barreau cohorte sans nom de cohorte", u.Username, c)
			}
		}
	}
}
