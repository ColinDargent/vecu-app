package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// Ces tests portaient sur la matrice /admin/espaces jusqu'au 21/08. La matrice
// a disparu, l'accès se règle dans le dossier lui-même - les garanties, elles,
// sont reprises telles quelles : refus des non-admins, redirection jamais
// reçue du formulaire, entrées invalides refusées, et l'avertissement sur les
// règles posées plus profond.

func TestDossiersRefuseNonAdmin(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("membre", "mdp", perms.Lecture, false)
	h := s.Handler()
	c := login(t, h, "membre", "mdp")

	routes := []struct{ methode, chemin string }{
		{"POST", "/admin/dossiers/acces"},
	}
	for _, rt := range routes {
		var rec *httptest.ResponseRecorder
		if rt.methode == "GET" {
			rec = get(h, rt.chemin, c)
		} else {
			rec = postForm(h, rt.chemin, url.Values{
				"nom": {"pirate"}, "chemin": {"pirate"}, "user_id": {"1"}, "niveau": {"ecriture"},
			}, c)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s non-admin : attendu 403, obtenu %d", rt.methode, rt.chemin, rec.Code)
		}
	}
	// Le refus doit être total : rien n'a été créé au passage.
	if list, _ := s.Store.List(""); len(list) != 0 {
		t.Errorf("un non-admin a écrit dans le dépôt : %v", list)
	}
}

// TestAccueilNeFuitPasLesMembres : l'accueil est ouvert à tout compte, mais
// « qui a accès à quoi » est une information de gouvernance. La donner à tout
// le monde apprendrait à un membre l'existence de comptes et de périmètres qui
// ne le regardent pas. C'est la règle qui valait pour la matrice (admin-only),
// conservée en changeant d'écran.
func TestAccueilNeFuitPasLesMembres(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.DB.CreateUser("achille", "mdp", perms.Lecture, false)
	s.Store.Write("shared/note.md", "bonjour", "colin")
	h := s.Handler()

	membre := login(t, h, "achille", "mdp")
	body := get(h, "/admin/", membre).Body.String()
	if !strings.Contains(body, "shared") {
		t.Fatalf("le membre ne voit pas son propre dossier")
	}
	if strings.Contains(body, "colin") {
		t.Error("l'accueil d'un non-admin nomme les autres comptes")
	}
	// Le panneau d'accès d'un dossier ne lui est pas servi non plus.
	if body := get(h, "/admin/dossiers/shared", membre).Body.String(); strings.Contains(body, "Accès à ce dossier") {
		t.Error("le panneau d'accès est servi à un non-admin")
	}

	// L'admin, lui, voit les deux.
	admin := login(t, h, "colin", "mdp")
	if body := get(h, "/admin/", admin).Body.String(); !strings.Contains(body, "achille") {
		t.Error("l'accueil d'un admin ne montre pas les membres")
	}
}

// TestLeWebNeCreePlusDeDossier : le geste a été retiré, route comprise.
//
// Un dossier créé depuis le web devenait un dossier à la RACINE Vécu sur le
// poste de chaque membre, jamais à l'endroit choisi par la personne. La ligne
// tranchée le 21/08 donne le local à l'app : on crée le dossier où on veut sur
// son Mac, puis « Partager un dossier… », qui ne le déplace pas.
//
// La validation des noms que ce test couvrait auparavant n'est pas perdue :
// elle est figée là où elle vit, sur espaces.Create (TestCreate, TestValidNom),
// et cette fonction reste - c'est le parcours A qui s'en sert désormais.
func TestLeWebNeCreePlusDeDossier(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.DB.CreateUser("membre", "mdp", perms.Lecture, false)
	h := s.Handler()

	// Même pour un administrateur : la route n'existe plus, elle n'est pas
	// seulement interdite.
	for _, qui := range []string{"colin", "membre"} {
		c := login(t, h, qui, "mdp")
		rec := postForm(h, "/admin/dossiers", url.Values{"nom": {"shared"}}, c)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST /admin/dossiers par %s : attendu 405, obtenu %d", qui, rec.Code)
		}
		if list, _ := s.Store.List(""); len(list) != 0 {
			t.Fatalf("un POST a quand même écrit dans le dépôt : %v", list)
		}
	}

	// L'écran n'offre plus le geste, et dit où il se trouve.
	c := login(t, h, "colin", "mdp")
	body := get(h, "/admin/", c).Body.String()
	if strings.Contains(body, `action="/admin/dossiers"`) {
		t.Error("le formulaire de création est encore rendu")
	}
	if !strings.Contains(body, "Partager un dossier") {
		t.Error("l'accueil ne dit pas où le geste se trouve désormais")
	}
}

// TestAccesEnUnClic : le geste central. Un POST = une règle posée = le dossier
// entre (ou sort) du périmètre de la personne.
func TestAccesEnUnClic(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.Store.Write("shared/note.md", "bonjour", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	id := strconv.FormatInt(achille.ID, 10)
	perimetre := func() int {
		t.Helper()
		rules, err := s.DB.Rules(achille.ID)
		if err != nil {
			t.Fatalf("Rules : %v", err)
		}
		a, _ := s.DB.UserByID(achille.ID)
		files, _ := s.Store.List("")
		n := 0
		for _, f := range files {
			if perms.CanRead(f, a.DefaultLevel, rules) {
				n++
			}
		}
		return n
	}

	if perimetre() != 0 {
		t.Fatalf("achille voit déjà des fichiers avant tout accès")
	}
	rec := postForm(h, "/admin/dossiers/acces", url.Values{
		"chemin": {"shared"}, "user_id": {id}, "niveau": {"ecriture"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("pose d'accès : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	// On revient dans le dossier où le clic a eu lieu, pas sur un écran à part.
	if got := rec.Header().Get("Location"); got != "/admin/dossiers/shared" {
		t.Errorf("redirection = %q, attendu /admin/dossiers/shared", got)
	}
	if perimetre() != 1 {
		t.Errorf("après l'accès : %d fichiers visibles, attendu 1", perimetre())
	}

	// Retrait : un clic aussi, et le périmètre se referme.
	postForm(h, "/admin/dossiers/acces", url.Values{
		"chemin": {"shared"}, "user_id": {id}, "niveau": {"invisible"},
	}, c)
	if perimetre() != 0 {
		t.Errorf("après retrait : %d fichiers visibles, attendu 0", perimetre())
	}
}

// TestAccesEnProfondeur : ce que la matrice ne savait pas faire. Le modèle
// autorise une règle à n'importe quelle profondeur, dans les deux sens ; il
// fallait jusqu'ici taper le chemin à la main dans la fiche d'un compte.
func TestAccesEnProfondeur(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.Store.Write("clients/vdf/note.md", "n", "colin")
	s.Store.Write("clients/autre/note.md", "n", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	id := strconv.FormatInt(achille.ID, 10)

	rec := postForm(h, "/admin/dossiers/acces", url.Values{
		"chemin": {"clients/vdf"}, "user_id": {id}, "niveau": {"lecture"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("accès en profondeur : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/admin/dossiers/clients/vdf" {
		t.Errorf("redirection = %q", got)
	}
	// UN dossier ouvert, pas son voisin : la règle est bien posée en profondeur.
	rules, _ := s.DB.Rules(achille.ID)
	a, _ := s.DB.UserByID(achille.ID)
	if !perms.CanRead("clients/vdf/note.md", a.DefaultLevel, rules) {
		t.Error("clients/vdf/note.md reste invisible après l'ouverture")
	}
	if perms.CanRead("clients/autre/note.md", a.DefaultLevel, rules) {
		t.Error("clients/autre/note.md est devenu visible : la règle a fuité vers le voisin")
	}
}

// TestAccesRetourFiche : depuis la fiche d'un compte, le même bouton renvoie
// sur la fiche. La destination ne vient jamais du formulaire.
func TestAccesRetourFiche(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.Store.Write("shared/note.md", "bonjour", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	id := strconv.FormatInt(achille.ID, 10)

	rec := postForm(h, "/admin/dossiers/acces", url.Values{
		"chemin": {"shared"}, "user_id": {id}, "niveau": {"lecture"}, "retour": {"user"},
	}, c)
	if got := rec.Header().Get("Location"); got != "/admin/users/"+id {
		t.Errorf("redirection = %q, attendu /admin/users/%s", got, id)
	}
	// Une destination arbitraire est ignorée, pas suivie.
	rec = postForm(h, "/admin/dossiers/acces", url.Values{
		"chemin": {"shared"}, "user_id": {id}, "niveau": {"lecture"},
		"retour": {"https://exemple.invalide/phishing"},
	}, c)
	if got := rec.Header().Get("Location"); got != "/admin/dossiers/shared" {
		t.Errorf("redirection ouverte : %q", got)
	}
	// La fiche affiche bien la vue miroir.
	if body := get(h, "/admin/users/"+id, c).Body.String(); !strings.Contains(body, "Espaces") {
		t.Errorf("la fiche utilisateur n'affiche pas la section Espaces")
	}
}

func TestAccesEntreesInvalides(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("shared/note.md", "bonjour", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	cas := []url.Values{
		{"chemin": {"shared"}, "user_id": {"9999"}, "niveau": {"lecture"}}, // compte inexistant
		{"chemin": {"shared"}, "user_id": {"abc"}, "niveau": {"lecture"}},  // id non numérique
		{"chemin": {"shared"}, "user_id": {"1"}, "niveau": {"admin"}},      // niveau inventé
		{"chemin": {"../evasion"}, "user_id": {"1"}, "niveau": {"lecture"}},
		{"chemin": {".obsidian"}, "user_id": {"1"}, "niveau": {"lecture"}},
		{"chemin": {".obsidian/plugins"}, "user_id": {"1"}, "niveau": {"lecture"}},
		{"chemin": {""}, "user_id": {"1"}, "niveau": {"lecture"}}, // la racine : c'est le niveau par défaut du compte
	}
	for _, form := range cas {
		if rec := postForm(h, "/admin/dossiers/acces", form, c); rec.Code != http.StatusBadRequest {
			t.Errorf("%v : attendu 400, obtenu %d", form, rec.Code)
		}
	}
}

// TestPanneauSignaleLesReglesInternes : les boutons agissent sur le dossier
// affiché, mais une règle plus profonde l'emporte dans le droit effectif. Sans
// avertissement, cliquer « invisible » est un geste sans effet et l'admin croit
// avoir coupé un accès resté ouvert.
func TestPanneauSignaleLesReglesInternes(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	if _, err := s.Store.Write("clients/vdf/note.md", "n", "colin"); err != nil {
		t.Fatalf("Write : %v", err)
	}
	// Accès accordé en profondeur seulement : la racine reste invisible.
	if err := s.DB.SetPermission(achille.ID, "clients/vdf", perms.Ecriture); err != nil {
		t.Fatalf("SetPermission : %v", err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	body := get(h, "/admin/dossiers/clients", c).Body.String()
	if !strings.Contains(body, "clients/vdf") {
		t.Errorf("le panneau ne signale pas la règle interne qui l'emporte")
	}
	// Signalé, mais REPLIÉ : un groupe de trente skills produisait autant de
	// lignes par compte, et le panneau chassait de l'écran les boutons qu'il
	// commente. Le compte se lit sans déplier, le détail se demande.
	if !strings.Contains(body, `<details class="internes">`) {
		t.Errorf("l'avertissement n'est pas replié dans un <details>")
	}
	if strings.Contains(body, `<details class="internes" open`) {
		t.Errorf("l'avertissement est déplié d'office")
	}
	if !strings.Contains(body, "1 règle") {
		t.Errorf("le résumé ne porte pas le nombre de règles internes")
	}
	// Et la fiche du compte porte le même avertissement.
	fiche := get(h, "/admin/users/"+strconv.FormatInt(achille.ID, 10), c).Body.String()
	if !strings.Contains(fiche, "clients/vdf") {
		t.Errorf("la fiche ne signale pas la règle interne")
	}
}

// TestAnciennesAdressesRedirigent : les adresses d'avant le 21/08 sont dans des
// favoris, dans l'historique des navigateurs et dans le menu de l'app de
// bureau. Aucune ne doit rendre un 404.
func TestAnciennesAdressesRedirigent(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	s.Store.Write("shared/note.md", "bonjour", "colin")
	s.Store.Write("notes/plan#1.md", "x", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	cas := []struct{ de, vers string }{
		{"/admin/fichiers", "/admin/dossiers"},
		{"/admin/fichiers/shared", "/admin/dossiers/shared"},
		{"/admin/fichiers/shared/note.md", "/admin/dossiers/shared/note.md"},
		// Le chemin est re-fabriqué segment par segment, jamais recollé depuis
		// l'URL brute : le « # » ressort échappé.
		{"/admin/fichiers/notes/plan%231.md", "/admin/dossiers/notes/plan%231.md"},
		{"/admin/espaces", "/admin/"},
		{"/admin/conflits", "/admin/"},
	}
	for _, k := range cas {
		rec := get(h, k.de, c)
		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("GET %s : attendu 301, obtenu %d", k.de, rec.Code)
			continue
		}
		if got := rec.Header().Get("Location"); got != k.vers {
			t.Errorf("GET %s → %q, attendu %q", k.de, got, k.vers)
		}
	}
}
