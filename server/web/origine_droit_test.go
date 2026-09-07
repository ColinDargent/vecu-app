package web

// SLICE 6 (DAR-196) : l'ecran dit D'OU vient un droit, pas seulement lequel.
//
// Le piege que ca rend visible : une cohorte OUVRE et ne ferme rien. Reserver un
// dossier a une cohorte demande donc deux gestes - fermer le dossier par son
// defaut, puis l'ouvrir a la cohorte. Un mainteneur qui ne fait que le second
// croit avoir restreint et n'a rien fait, parce que tout le monde y avait deja
// acces. L'ecran affichait « Direction a acces », ce qui est vrai, et taisait
// que les onze autres aussi.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func TestLEcranDitDouVientLeDroit(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	marie, err := s.DB.CreateUser("marie", "mdp", perms.Lecture, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("shared/direction/budget.md", "chiffres", "colin"); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "colin", "mdp")

	// 1. AU DEPART : marie voit le dossier par le defaut de son compte, et
	//    l'ecran le dit. C'est le cas ou un mainteneur croit avoir restreint.
	page := get(h, hrefDossiers("shared/direction"), c).Body.String()
	if !strings.Contains(page, "défaut du compte") {
		t.Errorf("l'origine « défaut du compte » n'est pas affichée :\n%s", extraitMembre(page))
	}

	// 2. On ferme le dossier par son defaut, et on l'ouvre a une cohorte.
	g, err := s.DB.CreerGroupe("Direction")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetMembreGroupe(g, marie.ID, perms.Lecture); err != nil {
		t.Fatal(err)
	}
	// 02/09 : le rangement d'un sous-dossier se fait sur la COHORTE. Le même
	// formulaire range et règle, parce que la page du dossier a cessé d'être une
	// surface de rangement le même jour.
	if rec := poste(t, h, c, "/admin/cohortes/niveau-chemin", url.Values{
		"groupe_id": {strconv.FormatInt(g, 10)}, "chemin": {"shared/direction"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("rangement dans la cohorte : %d", rec.Code)
	}
	if rec := poste(t, h, c, "/admin/dossiers/defaut", url.Values{
		"chemin": {"shared/direction"}, "niveau": {"invisible"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("fermeture du dossier : %d", rec.Code)
	}

	// 3. MAINTENANT le droit de marie vient de sa cohorte, et l'ecran la NOMME.
	page = get(h, hrefDossiers("shared/direction"), c).Body.String()
	if !strings.Contains(page, "par la cohorte Direction") {
		t.Errorf("l'écran ne nomme pas la cohorte d'où vient le droit :\n%s", extraitMembre(page))
	}
}

func TestLeNiveauDUnDossierDeCohorteSeRegleDepuisLEcran(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	marie, err := s.DB.CreateUser("marie", "mdp", perms.Lecture, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("shared/direction/budget.md", "chiffres", "colin"); err != nil {
		t.Fatal(err)
	}
	g, err := s.DB.CreerGroupe("Salariés")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetMembreGroupe(g, marie.ID, perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.RangeChemin(g, "shared", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.RangeChemin(g, "shared/direction", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "colin", "mdp")

	niveau := func() perms.Level {
		t.Helper()
		u, err := s.DB.UserByID(marie.ID)
		if err != nil {
			t.Fatal(err)
		}
		n, _, err := s.DB.DroitAvecOrigine(u, "shared/direction/budget.md")
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if n := niveau(); n != perms.Ecriture {
		t.Fatalf("la cohorte devait ouvrir tout son dossier, obtenu %v", n)
	}

	// LE GESTE : réserver le sous-dossier en le rendant invisible à la cohorte.
	if rec := poste(t, h, c, "/admin/cohortes/niveau-chemin", url.Values{
		"groupe_id": {strconv.FormatInt(g, 10)},
		"chemin":    {"shared/direction"},
		"niveau":    {"invisible"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("réglage du niveau : %d", rec.Code)
	}
	if n := niveau(); n != perms.Invisible {
		t.Errorf("le sous-dossier reste ouvert après le geste : %v", n)
	}

	// ET LE RETOUR EN ARRIERE : un niveau vide rend au chemin celui du membre.
	if rec := poste(t, h, c, "/admin/cohortes/niveau-chemin", url.Values{
		"groupe_id": {strconv.FormatInt(g, 10)},
		"chemin":    {"shared/direction"},
		"niveau":    {""},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait du niveau : %d", rec.Code)
	}
	if n := niveau(); n != perms.Ecriture {
		t.Errorf("le retrait du niveau ne rend pas le droit du membre : %v", n)
	}
}

// extraitMembre : le bloc des membres, pour un message d'erreur lisible plutot
// qu'une page de 40 Ko.
func extraitMembre(page string) string {
	i := strings.Index(page, `class="membre"`)
	if i < 0 {
		return "(aucun bloc membre dans la page)"
	}
	fin := i + 600
	if fin > len(page) {
		fin = len(page)
	}
	return page[i:fin]
}
