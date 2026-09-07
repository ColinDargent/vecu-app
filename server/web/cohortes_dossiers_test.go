package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// TestPanneauCohortesSurUnDossier : le parcours complet, par HTTP.
//
// Réservé aux administrateurs, comme le panneau d'accès d'à côté : ranger un
// dossier dans une cohorte, c'est fabriquer un périmètre.
func TestPanneauCohortesSurUnDossier(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	caroline, _ := s.DB.CreateUser("caroline", "mdp", perms.Invisible, false)
	s.Store.Write("clients/acme/note.md", "note", "colin")
	s.Store.Write("clients/autre/note.md", "note", "colin")
	salaries, _ := s.DB.CreerGroupe("Salariés")
	s.DB.SetMembreGroupe(salaries, caroline.ID, perms.Lecture)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	page := get(h, "/admin/dossiers/clients", c).Body.String()
	if !strings.Contains(page, "Cohortes") {
		t.Fatal("le panneau des cohortes n'est pas rendu sur un dossier")
	}
	// 02/09 : la page du dossier LIT ses cohortes, elle ne les range plus. Le
	// geste a déménagé sur la cohorte, qui est l'objet dont on part.
	if strings.Contains(page, `action="/admin/dossiers/cohortes"`) {
		t.Fatal("la page du dossier range encore des cohortes : deuxième surface autoritaire")
	}

	if rec := postForm(h, "/admin/cohortes/modifier", url.Values{
		"groupe_id":      {strconv.FormatInt(salaries, 10)},
		"nom":            {"Salariés"},
		"noeud":          {"clients"},
		"niveau_clients": {""},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("rangement du dossier depuis la cohorte : attendu 303, obtenu %d", rec.Code)
	}

	u, _ := s.DB.UserByID(caroline.ID)
	if niveau, _ := s.DB.Effective(u, "clients/acme/note.md"); niveau != perms.Lecture {
		t.Errorf("la cohorte n'ouvre pas le dossier : %v", niveau)
	}

	// Décocher referme, sans laisser de règle orpheline dans `permissions`.
	// Le formulaire de la cohorte sans aucun `dossier` = plus aucun espace.
	if rec := postForm(h, "/admin/cohortes/modifier", url.Values{
		"groupe_id":      {strconv.FormatInt(salaries, 10)},
		"nom":            {"Salariés"},
		"noeud":          {"clients"},
		"niveau_clients": {"hors"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait : attendu 303, obtenu %d", rec.Code)
	}
	if niveau, _ := s.DB.Effective(u, "clients/acme/note.md"); niveau != perms.Invisible {
		t.Errorf("le dossier reste ouvert après retrait de la cohorte : %v", niveau)
	}
	regles, _ := s.DB.Rules(caroline.ID)
	for _, r := range regles {
		if strings.HasPrefix(r.Path, "clients") {
			t.Errorf("règle orpheline laissée dans permissions : %+v", r)
		}
	}
}

// TestCohortesDossierReserveAuxAdmins : un membre ne fabrique pas de périmètre
// (21/08). Ni le panneau, ni la route.
func TestCohortesDossierReserveAuxAdmins(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.Store.Write("clients/acme/note.md", "note", "colin")
	g, _ := s.DB.CreerGroupe("Salariés")
	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	if page := get(h, "/admin/dossiers/clients", c).Body.String(); strings.Contains(page, "Cohortes") {
		t.Error("le panneau des cohortes est servi à un membre")
	}
	if rec := postForm(h, "/admin/dossiers/cohortes", url.Values{
		"chemin": {"clients"}, "groupe_id": {strconv.FormatInt(g, 10)},
	}, c); rec.Code == http.StatusSeeOther {
		t.Errorf("un membre a rangé un dossier dans une cohorte (%d)", rec.Code)
	}
	if ids, _ := s.DB.GroupesDuChemin("clients", "dossier"); len(ids) != 0 {
		t.Errorf("le dossier a été rangé quand même : %v", ids)
	}
}

// TestUneSeuleSurfaceAutoritaire : UNE SEULE surface range un dossier dans une
// cohorte. Deux écrans qui font tous les deux foi s'écrasent l'un l'autre dès
// que l'un est resté ouvert pendant que l'autre enregistrait.
//
// 02/09 : cette surface est la COHORTE, plus la page du dossier. L'invariant
// est le même ; c'est le côté qui écrit qui a changé, sur retour de Colin.
func TestUneSeuleSurfaceAutoritaire(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("clients/acme/note.md", "note", "colin")
	s.Store.Write("shared/skills/veille/SKILL.md", contenuSkill("veille", "Un skill."), "colin")
	g, _ := s.DB.CreerGroupe("Salariés")
	s.DB.RangeChemin(g, "clients", "dossier", "")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	page := get(h, "/admin/cohortes", c).Body.String()
	if !strings.Contains(page, `href="/admin/dossiers/clients"`) {
		t.Error("le dossier porté par la cohorte ne renvoie pas vers sa page")
	}
	// L'INVARIANT NE BOUGE PAS, LE CÔTÉ QUI ÉCRIT A DÉMÉNAGÉ (02/09, retour de
	// Colin). Une seule surface range un dossier dans une cohorte, sans quoi
	// deux écrans laissés ouverts s'écrasent l'un l'autre. Cette surface est
	// désormais la COHORTE : on part de « l'équipe contenu, voilà ses
	// dossiers », pas de chaque dossier pour se demander qui y entre.
	//
	// CE QU'ON VÉRIFIE ICI A CHANGÉ LE 04/09, et il fallait le changer. La
	// ligne d'avant cherchait `name="dossier"` - les cases à cocher du panneau.
	// Or RIEN NE LES LISAIT : `handleModifierGroupe` ne regarde que `noeud` et
	// `niveau_<chemin>`, et décocher `clients` puis enregistrer laissait
	// `clients` dans la cohorte (constaté au navigateur). L'assertion tenait
	// donc sur une commande morte : elle ne pouvait pas tomber, et elle aurait
	// laissé retirer la vraie surface sans rien dire. C'est l'arbre qu'on
	// vérifie, parce que c'est lui qui écrit.
	if !strings.Contains(page, `name="noeud"`) {
		t.Error("l'écran des cohortes ne compose pas ses dossiers : le geste n'a nulle part où se faire")
	}
	if !strings.Contains(page, `name="niveau_clients"`) {
		t.Error("le dossier porté n'a pas de réglage de niveau dans l'arbre de sa cohorte")
	}
	// UN CHEMIN, UN SEUL ENDROIT (04/09). `clients` est dans l'arbre, donc il
	// ne doit PAS reparaître dans « autres chemins » : la page d'avant le
	// montrait quatre fois, sous quatre vocabulaires, dont deux inertes.
	if n := strings.Count(page, `name="niveau_clients"`); n != 1 {
		t.Errorf("`clients` est réglable à %d endroits de la page, attendu 1", n)
	}
	if strings.Contains(page, `id="niv-`+strconv.FormatInt(g, 10)+`-clients"`) {
		t.Error("`clients` est dans l'arbre ET dans les autres chemins : deux surfaces pour un dossier")
	}
	// Ce qu'il regle, c'est le NIVEAU que la cohorte accorde sur un dossier
	// qu'elle porte deja. Ce reglage n'a de sens que rapporte a la cohorte,
	// donc il vit ici et pas sur la page du dossier.
	if !strings.Contains(page, `action="/admin/cohortes/niveau-chemin"`) {
		t.Error("l'écran des cohortes ne permet pas de régler le niveau d'un dossier qu'il porte")
	}

	// UN CHEMIN PLUS PROFOND QUE L'ARBRE. Il n'y a pas de ligne d'arbre pour
	// lui - l'arbre s'arrête à deux niveaux - donc c'est « autres chemins » qui
	// doit porter et son réglage et son retrait. Sans ce retrait, un
	// sous-dossier rangé dans une cohorte y serait piégé.
	s.DB.RangeChemin(g, "clients/acme/prive", "dossier", "")
	page = get(h, "/admin/cohortes", c).Body.String()
	if !strings.Contains(page, `action="/admin/cohortes/retirer-chemin"`) {
		t.Error("aucun retrait par chemin : un sous-dossier rangé dans une cohorte y serait piégé")
	}
	if !strings.Contains(page, `id="niv-`+strconv.FormatInt(g, 10)+`-clients/acme/prive"`) {
		t.Error("le chemin profond n'a pas de réglage de niveau : il est entré sans pouvoir être réglé")
	}
	if strings.Contains(page, `name="niveau_clients/acme/prive"`) {
		t.Error("le chemin profond apparaît dans l'arbre, qui ne le couvre pas")
	}
}

// TestDefautDeDossierDepuisLEcran : le parcours complet, par HTTP.
func TestDefautDeDossierDepuisLEcran(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	salarie, _ := s.DB.CreateUser("salarie", "mdp", perms.Lecture, false)
	s.Store.Write("rh/salaires.md", "confidentiel", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	page := get(h, "/admin/dossiers/rh", c).Body.String()
	if !strings.Contains(page, "Par défaut, ce dossier est") {
		t.Fatal("le réglage du défaut n'est pas rendu")
	}

	if rec := postForm(h, "/admin/dossiers/defaut", url.Values{
		"chemin": {"rh"}, "niveau": {"invisible"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("pose du défaut : attendu 303, obtenu %d", rec.Code)
	}
	u, _ := s.DB.UserByID(salarie.ID)
	if niveau, _ := s.DB.Effective(u, "rh/salaires.md"); niveau != perms.Invisible {
		t.Errorf("le défaut ne ferme pas pour un compte existant : %v", niveau)
	}
	// Le salarié n° 36.
	futur, _ := s.DB.CreateUser("futur", "mdp", perms.Lecture, false)
	f, _ := s.DB.UserByID(futur.ID)
	if niveau, _ := s.DB.Effective(f, "rh/salaires.md"); niveau != perms.Invisible {
		t.Errorf("un compte créé après la pose entre dans les RH : %v", niveau)
	}

	// Retrait : tout retombe sur le défaut du compte.
	if rec := postForm(h, "/admin/dossiers/defaut", url.Values{
		"chemin": {"rh"}, "retirer": {"1"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait : attendu 303, obtenu %d", rec.Code)
	}
	if niveau, _ := s.DB.Effective(u, "rh/salaires.md"); niveau != perms.Lecture {
		t.Errorf("le retrait ne rend pas le niveau d'avant : %v", niveau)
	}
}

// TestFermerUnDossierNeFermePasSonAuteur : incrément 6.
//
// `visibleFiles` s'appuie sur les règles de l'administrateur lui-même : fermer
// un dossier le ferme AUSSI pour lui, et il perd du même coup l'écran depuis
// lequel il pourrait revenir en arrière. Une exception lui est donc posée - à
// LUI seul, écrire pour des comptes que le geste ne visait pas les détacherait
// de leur défaut (leçon du 21/08).
func TestFermerUnDossierNeFermePasSonAuteur(t *testing.T) {
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	autre, _ := s.DB.CreateUser("autre", "mdp", perms.Lecture, false)
	s.Store.Write("rh/salaires.md", "confidentiel", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := postForm(h, "/admin/dossiers/defaut", url.Values{
		"chemin": {"rh"}, "niveau": {"invisible"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("attendu 303, obtenu %d", rec.Code)
	}
	// PLUS D'EXCEPTION POSEE, et plus de deverrouillage a annoncer (DAR-196,
	// 01/09). Le garde rendait a l'auteur son niveau d'AVANT, donc l'ecriture,
	// sur un dossier qu'il venait de declarer invisible : il preservait
	// l'administrabilite en lui rendant la LECTURE des fichiers, ce qu'aucune
	// des cinq autres portes ne faisait. L'administrabilite est desormais portee
	// par la vue, pour les six portes et sans rendre aucun droit.
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "okd=1") {
		t.Errorf("un deverrouillage est encore annonce alors que le garde a ete retire : %q", loc)
	}

	// L'auteur est bien ferme, comme tout le monde : le geste dit ce qu'il fait.
	u, _ := s.DB.UserByID(colin.ID)
	if niveau, _ := s.DB.Effective(u, "rh/salaires.md"); niveau != perms.Invisible {
		t.Errorf("le defaut du dossier devait fermer aussi son auteur, obtenu %v", niveau)
	}
	// Mais il ADMINISTRE toujours le dossier : c'est le critere de DAR-196.
	if code := get(h, "/admin/dossiers/rh", c).Code; code != http.StatusOK {
		t.Errorf("la page du dossier est devenue inatteignable : %d", code)
	}
	// Et il n'en LIT plus le contenu : administrer n'est pas lire.
	if code := get(h, "/admin/dossiers/rh/salaires.md", c).Code; code != http.StatusNotFound {
		t.Errorf("le contenu du fichier ferme reste servi a son auteur : %d", code)
	}

	// PERSONNE D'AUTRE n'a gagné de règle. Prouvé sans lire la base : le dossier
	// est bien fermé pour l'autre compte, et RETIRER le défaut lui rend son
	// niveau d'avant. Une règle individuelle écrite à son insu survivrait au
	// retrait et le laisserait fermé - c'est le « invisible le jour même, faux
	// pour toujours » du 21/08.
	a, _ := s.DB.UserByID(autre.ID)
	if niveau, _ := s.DB.Effective(a, "rh/salaires.md"); niveau != perms.Invisible {
		t.Errorf("le dossier n'est pas fermé pour les autres : %v", niveau)
	}
	if rec := postForm(h, "/admin/dossiers/defaut", url.Values{
		"chemin": {"rh"}, "retirer": {"1"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("retrait : attendu 303, obtenu %d", rec.Code)
	}
	if niveau, _ := s.DB.Effective(a, "rh/salaires.md"); niveau != perms.Lecture {
		t.Errorf("un compte non visé garde une règle après le retrait du défaut : %v", niveau)
	}
}

// TestDefautReglablSansAucuneCohorte : le panneau ne doit PAS vivre dans un
// `{{if .Cohortes}}`. Sur un dépôt neuf, il disparaîtrait avec le réglage du
// défaut qu'il contient, et fermer un dossier serait impossible tant qu'aucun
// groupe n'existe. C'est le piège trouvé le 21/08 sur la section Groupes, qui
// vivait dans un `{{if .Skills}}` et devenait invisible sur un dépôt sans skill.
func TestDefautReglableSansAucuneCohorte(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("rh/salaires.md", "confidentiel", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	page := get(h, "/admin/dossiers/rh", c).Body.String()
	if !strings.Contains(page, "Par défaut, ce dossier est") {
		t.Error("sur un dépôt sans cohorte, le réglage du défaut disparaît")
	}
	if !strings.Contains(page, "Aucune cohorte pour l'instant") {
		t.Error("l'écran ne dit pas pourquoi il n'y a aucune case à cocher")
	}
	if rec := postForm(h, "/admin/dossiers/defaut", url.Values{
		"chemin": {"rh"}, "niveau": {"invisible"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Errorf("pose du défaut sans cohorte : attendu 303, obtenu %d", rec.Code)
	}
}

// TestSelecteurDeDefautNAffirmeRienSansDefaut : trouvé au rendu dans un vrai
// navigateur, pas par un test écrit d'avance.
//
// `DefautDossier` rend `perms.Invisible` (le zéro du type) quand AUCUN défaut
// n'est posé. L'option « invisible » sortait donc `selected` en même temps que
// l'option « aucun », et la dernière l'emporte en HTML : l'écran affirmait que
// le dossier était fermé par défaut alors que rien n'était posé.
func TestSelecteurDeDefautNAffirmeRienSansDefaut(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("rh/salaires.md", "confidentiel", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	page := get(h, "/admin/dossiers/rh", c).Body.String()
	debut := strings.Index(page, `<select name="niveau">`)
	if debut < 0 {
		t.Fatal("sélecteur absent")
	}
	bloc := page[debut : debut+strings.Index(page[debut:], "</select>")]
	if n := strings.Count(bloc, "selected"); n != 1 {
		t.Errorf("%d option(s) sélectionnée(s), une seule attendue :\n%s", n, bloc)
	}
	if !strings.Contains(bloc, `value="" selected`) {
		t.Error("sans défaut posé, l'option sélectionnée doit être « aucun »")
	}

	// Une fois un défaut posé, c'est lui qui est sélectionné, et lui seul.
	if err := s.DB.SetDefautDossier("rh", perms.Lecture); err != nil {
		t.Fatalf("SetDefautDossier : %v", err)
	}
	page = get(h, "/admin/dossiers/rh", c).Body.String()
	debut = strings.Index(page, `<select name="niveau">`)
	bloc = page[debut : debut+strings.Index(page[debut:], "</select>")]
	if n := strings.Count(bloc, "selected"); n != 1 {
		t.Errorf("%d option(s) sélectionnée(s) avec un défaut posé", n)
	}
	if !strings.Contains(bloc, `value="lecture" selected`) {
		t.Errorf("le défaut posé n'est pas celui qui s'affiche :\n%s", bloc)
	}
}

// TestOptionAucunRetireLeDefaut : l'écran propose « aucun », le handler doit en
// faire un retrait. Sans ce cas, il refuserait un geste qu'il propose.
func TestOptionAucunRetireLeDefaut(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	u, _ := s.DB.CreateUser("salarie", "mdp", perms.Lecture, false)
	s.Store.Write("rh/salaires.md", "confidentiel", "colin")
	s.DB.SetDefautDossier("rh", perms.Invisible)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	if rec := postForm(h, "/admin/dossiers/defaut", url.Values{
		"chemin": {"rh"}, "niveau": {""},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("option « aucun » : attendu 303, obtenu %d", rec.Code)
	}
	if _, pose, _ := s.DB.DefautDossier("rh"); pose {
		t.Error("le défaut est toujours posé")
	}
	a, _ := s.DB.UserByID(u.ID)
	if niveau, _ := s.DB.Effective(a, "rh/salaires.md"); niveau != perms.Lecture {
		t.Errorf("le niveau d'avant n'est pas rendu : %v", niveau)
	}
}
