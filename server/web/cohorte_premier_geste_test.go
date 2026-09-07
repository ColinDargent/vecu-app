package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
)

// 02/09, retour de Colin. La cohorte est l'objet qui porte les droits : elle se
// COMPOSE - dossiers et skills - et les comptes en héritent. Régler les dossiers
// compte par compte était le geste inverse.
//
// Le modèle le permettait déjà : `groupe_chemins` porte un `genre` qui vaut
// `dossier` ou `skill`, et `reglesDeGroupe` joint sans filtrer dessus. C'était
// l'écran qui ne le laissait pas faire.
func TestUneCohorteSeComposeDeDossiersEtDeSkills(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	marie, err := s.DB.CreateUser("marie", "mdp", perms.Invisible, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("clients/acme/note.md", "note", "colin"); err != nil {
		t.Fatal(err)
	}
	equipe, err := s.DB.CreerGroupe("Équipe contenu")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetMembreGroupe(equipe, marie.ID, perms.Lecture); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// Précondition : sans cohorte, Marie (privée par défaut) ne voit rien.
	if niveau, _ := s.DB.Effective(marie, "clients/acme/note.md"); niveau != perms.Invisible {
		t.Fatalf("précondition : Marie voit déjà le dossier (%v), le test ne prouverait rien", niveau)
	}

	// LE GESTE : on part de la cohorte et on lui donne son dossier.
	if rec := postForm(h, "/admin/cohortes/modifier", url.Values{
		"groupe_id":      {strconv.FormatInt(equipe, 10)},
		"nom":            {"Équipe contenu"},
		"noeud":          {"clients"},
		"niveau_clients": {""},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("composition de la cohorte : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}

	marie, _ = s.DB.UserByID(marie.ID)
	if niveau, _ := s.DB.Effective(marie, "clients/acme/note.md"); niveau != perms.Lecture {
		t.Errorf("Marie n'hérite pas du dossier de sa cohorte : %v", niveau)
	}
}

// LA GARDE QUI REND LE PANNEAU SÛR : les cases ne portent que les ESPACES, donc
// enregistrer le panneau ne peut pas retirer un sous-dossier rangé ailleurs.
//
// Sans elle, ouvrir le panneau d'une cohorte et cliquer « Enregistrer » viderait
// en silence tous ses réglages fins - le mode d'échec le plus coûteux de cet
// écran, parce qu'il est invisible sur le moment.
func TestEnregistrerLePanneauNEmportePasLesSousDossiers(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("shared/direction/plan.md", "plan", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("clients/acme/note.md", "note", "colin"); err != nil {
		t.Fatal(err)
	}
	g, err := s.DB.CreerGroupe("Salariés")
	if err != nil {
		t.Fatal(err)
	}
	// Un sous-dossier réglé finement, qui n'a PAS de case dans le panneau.
	if err := s.DB.RangeChemin(g, "shared/direction", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetNiveauChemin(g, "shared/direction", "invisible"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// On enregistre le panneau en ne cochant QUE « clients ».
	if rec := postForm(h, "/admin/cohortes/modifier", url.Values{
		"groupe_id":      {strconv.FormatInt(g, 10)},
		"nom":            {"Salariés"},
		"noeud":          {"clients"},
		"niveau_clients": {""},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("composition : attendu 303, obtenu %d", rec.Code)
	}

	lignes, err := s.DB.CheminsDuGroupe(g, "dossier")
	if err != nil {
		t.Fatal(err)
	}
	var vuDirection, vuClients bool
	for _, l := range lignes {
		switch l.Chemin {
		case "shared/direction":
			vuDirection = true
			if l.Niveau != "invisible" {
				t.Errorf("le niveau du sous-dossier a été écrasé : %q", l.Niveau)
			}
		case "clients":
			vuClients = true
		}
	}
	if !vuDirection {
		t.Error("ENREGISTRER A EMPORTÉ le sous-dossier : les réglages fins d'une cohorte sont perdus au premier clic")
	}
	if !vuClients {
		t.Error("l'espace coché n'a pas été rangé")
	}
}

// Le pendant du même choix : puisque les cases ne portent que les espaces, un
// sous-dossier doit avoir sa propre porte d'ENTRÉE et de SORTIE, sinon il est
// piégé dans la cohorte pour toujours.
func TestUnSousDossierEntreEtSortDUneCohorte(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("shared/direction/plan.md", "plan", "colin"); err != nil {
		t.Fatal(err)
	}
	g, err := s.DB.CreerGroupe("Direction")
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// ENTRÉE : le formulaire des sous-dossiers range ET règle. Avant le 02/09
	// il ne faisait que régler, et renvoyait vers la page du dossier - qui n'est
	// plus une surface de rangement.
	if rec := postForm(h, "/admin/cohortes/niveau-chemin", url.Values{
		"groupe_id": {strconv.FormatInt(g, 10)},
		"chemin":    {"shared/direction"},
		"niveau":    {"lecture"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("entrée du sous-dossier : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	lignes, _ := s.DB.CheminsDuGroupe(g, "dossier")
	if len(lignes) != 1 || lignes[0].Chemin != "shared/direction" || lignes[0].Niveau != "lecture" {
		t.Fatalf("le sous-dossier n'est pas entré avec son niveau : %+v", lignes)
	}

	// SORTIE : le retrait par ligne.
	if rec := postForm(h, "/admin/cohortes/retirer-chemin", url.Values{
		"groupe_id": {strconv.FormatInt(g, 10)},
		"chemin":    {"shared/direction"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("sortie du sous-dossier : attendu 303, obtenu %d", rec.Code)
	}
	if lignes, _ := s.DB.CheminsDuGroupe(g, "dossier"); len(lignes) != 0 {
		t.Errorf("le sous-dossier est piégé dans la cohorte : %+v", lignes)
	}
}

// À l'arrivée de quelqu'un, on le met dans ses cohortes. C'est le geste que la
// création d'un compte ne proposait pas : on posait un niveau par défaut, puis
// on allait régler ses droits un par un sur sa fiche.
func TestLaCreationDUnCompteLeRattacheASesCohortes(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("clients/acme/note.md", "note", "colin"); err != nil {
		t.Fatal(err)
	}
	g, err := s.DB.CreerGroupe("Salariés")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.RangeChemin(g, "clients", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// Le compte est PRIVÉ par défaut, et c'est le cas qui compte : ses droits
	// doivent venir de la cohorte, pas de son défaut.
	if rec := postForm(h, "/admin/users", url.Values{
		"username": {"nouvelle"}, "password": {"motdepasse"}, "niveau": {"invisible"},
		"cohorte_id": {strconv.FormatInt(g, 10)},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("création : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}

	liste, _ := s.DB.ListUsers()
	var nouvelle int64
	for _, x := range liste {
		if x.Username == "nouvelle" {
			nouvelle = x.ID
		}
	}
	if nouvelle == 0 {
		t.Fatal("le compte n'a pas été créé")
	}
	u, _ := s.DB.UserByID(nouvelle)
	// « lecture seule » est le plafond par défaut, et c'est le sens prudent :
	// une ouverture non voulue ne se signale jamais toute seule.
	if niveau, _ := s.DB.Effective(u, "clients/acme/note.md"); niveau != perms.Lecture {
		t.Errorf("le compte n'hérite pas de sa cohorte à la création : %v", niveau)
	}
}

// La fiche d'un compte doit dire d'OÙ vient chaque droit. Sans ça, on lit
// « lecture » et on pose une exception qui double une cohorte - et le réglage
// par groupe se vide de son sens un compte à la fois.
func TestLaFicheDUnCompteDitDouVientChaqueDroit(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	marie, err := s.DB.CreateUser("marie", "mdp", perms.Invisible, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("clients/acme/note.md", "note", "colin"); err != nil {
		t.Fatal(err)
	}
	g, err := s.DB.CreerGroupe("Équipe contenu")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetMembreGroupe(g, marie.ID, perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.RangeChemin(g, "clients", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	page := get(h, "/admin/users/"+strconv.FormatInt(marie.ID, 10), c).Body.String()
	if !strings.Contains(page, "Équipe contenu") {
		t.Error("la fiche ne nomme pas la cohorte d'où vient le droit")
	}
	if !strings.Contains(page, "par la cohorte") {
		t.Errorf("la fiche ne dit pas que le droit vient d'une cohorte")
	}
}

// 02/09, Colin : « depuis la cohorte, je veux pouvoir enlever des dossiers dans
// un dossier. Sinon c'est juste je te donne accès à un dossier. »
//
// Une case à cocher ne sait dire que dedans ou dehors. Le geste réel est
// « `clients` oui, mais pas `clients/confidentiel` », et il demande un niveau
// par chemin - `privé` sur l'enfant étant l'exclusion.
func TestLArbreDUneCohorteExclutUnSousDossier(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	marie, err := s.DB.CreateUser("marie", "mdp", perms.Invisible, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"clients/acme/note.md", "clients/confidentiel/pacte.md"} {
		if _, err := s.Store.Write(f, "x", "colin"); err != nil {
			t.Fatal(err)
		}
	}
	g, err := s.DB.CreerGroupe("Salariés")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetMembreGroupe(g, marie.ID, perms.Lecture); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// TOUT `clients`, SAUF `clients/confidentiel`, en un seul enregistrement.
	if rec := postForm(h, "/admin/cohortes/modifier", url.Values{
		"groupe_id":                   {strconv.FormatInt(g, 10)},
		"nom":                         {"Salariés"},
		"noeud":                       {"clients", "clients/confidentiel"},
		"niveau_clients":              {"lecture"},
		"niveau_clients/confidentiel": {"invisible"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("réglage de l'arbre : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}

	marie, _ = s.DB.UserByID(marie.ID)
	if niveau, _ := s.DB.Effective(marie, "clients/acme/note.md"); niveau != perms.Lecture {
		t.Errorf("le dossier ouvert n'est pas ouvert : %v", niveau)
	}
	// LE POINT DU LOT : l'exclusion du sous-dossier tient, alors même que son
	// parent est ouvert par la même cohorte.
	if niveau, _ := s.DB.Effective(marie, "clients/confidentiel/pacte.md"); niveau != perms.Invisible {
		t.Errorf("le sous-dossier exclu reste visible : %v", niveau)
	}
}

// Les deux genres ne se mélangent pas à l'écran non plus : le panneau d'une
// cohorte de dossiers montre l'arbre, celui d'une cohorte de skills les cases.
func TestLEcranSepareLesDeuxGenresDeCohorte(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("clients/acme/note.md", "x", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("shared/skills/veille/SKILL.md", contenuSkill("veille", "Un skill."), "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.CreerGroupe("Salariés"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.CreerGroupeDeGenre("Lecteurs de skills", db.GenreSkill); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	page := get(h, "/admin/cohortes", c).Body.String()

	if !strings.Contains(page, `name="noeud"`) {
		t.Error("la cohorte de dossiers n'a pas son arbre")
	}
	if !strings.Contains(page, `name="slug"`) {
		t.Error("la cohorte de skills n'a pas ses cases")
	}
	// Et le genre est dit, sinon les deux tableaux se ressemblent.
	if !strings.Contains(page, "dossiers") || !strings.Contains(page, "skills") {
		t.Error("l'écran ne dit pas le genre de chaque cohorte")
	}
}
