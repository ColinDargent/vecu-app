package web

// LE BANC D'ENFERMEMENT (DAR-196, slice 1).
//
// Un administrateur qui ferme un dossier doit pouvoir le rouvrir. Aujourd'hui
// il ne le peut pas : l'écran d'administration se construit depuis la vue du
// LECTEUR, donc ce que le lecteur ne voit plus, il ne peut plus administrer.
//
// Le garde anti-enfermement de `handleSetDefautDossier` (25/08) couvre UNE
// porte d'écriture sur SIX. Ce banc les exerce toutes les six par la vraie
// porte HTTP : cinq échouent avant la slice 2, la sixième passe déjà.
//
// Il est écrit ROUGE À DESSEIN, avant toute correction. Un banc qui ne
// reproduit pas invaliderait l'hypothèse, et ce serait un résultat.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
)

// Le dossier sur lequel l'admin se tire dans le pied, à chaque cas.
const cheminEnferme = "shared/projets"

type bancEnfermement struct {
	s     *Server
	h     http.Handler
	admin *db.User
	ck    *http.Cookie
}

func monteEnfermement(t *testing.T) *bancEnfermement {
	t.Helper()
	s := newServer(t)
	h := s.Handler()
	admin, err := s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write(cheminEnferme+"/note.md", "contenu", "colin"); err != nil {
		t.Fatal(err)
	}
	return &bancEnfermement{s: s, h: h, admin: admin, ck: login(t, h, "colin", "motdepasse")}
}

func (b *bancEnfermement) ficheAdmin() string {
	return "/admin/users/" + strconv.FormatInt(b.admin.ID, 10)
}

// fermeLeDossierEnBase pose le défaut `invisible` SANS passer par le handler.
//
// Passer par POST /admin/dossiers/defaut déclencherait le garde
// anti-enfermement, qui re-poserait une exception individuelle à l'admin et
// masquerait ce que les cas suivants cherchent à mesurer. La préparation d'un
// test ne doit pas emprunter le chemin que le test examine.
func (b *bancEnfermement) fermeLeDossierEnBase(t *testing.T) {
	t.Helper()
	if err := b.s.DB.SetDefautDossier(cheminEnferme, perms.Invisible); err != nil {
		t.Fatal(err)
	}
}

func TestUnAdminNeSEnfermePasHorsDUnDossier(t *testing.T) {
	cas := []struct {
		nom     string
		prepare func(t *testing.T, b *bancEnfermement)
		geste   func(t *testing.T, b *bancEnfermement) *http.Response
	}{
		{
			nom: "par le droit posé sur le dossier",
			geste: func(t *testing.T, b *bancEnfermement) *http.Response {
				return poste(t, b.h, b.ck, "/admin/dossiers/acces", url.Values{
					"chemin":  {cheminEnferme},
					"niveau":  {"invisible"},
					"user_id": {strconv.FormatInt(b.admin.ID, 10)},
				}).Result()
			},
		},
		{
			nom: "par le retrait du dossier de sa cohorte",
			prepare: func(t *testing.T, b *bancEnfermement) {
				// L'admin ne voit ce dossier QUE par sa cohorte : le dossier est
				// fermé par son défaut, la cohorte le rouvre.
				b.fermeLeDossierEnBase(t)
				id, err := b.s.DB.CreerGroupe("direction")
				if err != nil {
					t.Fatal(err)
				}
				if err := b.s.DB.SetMembreGroupe(id, b.admin.ID, perms.Ecriture); err != nil {
					t.Fatal(err)
				}
				if err := b.s.DB.RangeChemin(id, cheminEnferme, "dossier", ""); err != nil {
					t.Fatal(err)
				}
			},
			geste: func(t *testing.T, b *bancEnfermement) *http.Response {
				// LE GESTE A DÉMÉNAGÉ le 02/09 : le retrait d'un chemin se fait
				// sur la cohorte, plus depuis la page du dossier. L'invariant,
				// lui, est le même - retirer à sa propre cohorte le seul chemin
				// par lequel on voit un dossier ne doit pas l'enfermer.
				id, err := b.s.DB.ListGroupes()
				if err != nil || len(id) == 0 {
					t.Fatalf("cohorte de préparation introuvable : %v", err)
				}
				return poste(t, b.h, b.ck, "/admin/cohortes/retirer-chemin", url.Values{
					"groupe_id": {strconv.FormatInt(id[0].ID, 10)},
					"chemin":    {cheminEnferme},
				}).Result()
			},
		},
		{
			nom: "par le défaut du dossier (le seul garde existant)",
			geste: func(t *testing.T, b *bancEnfermement) *http.Response {
				return poste(t, b.h, b.ck, "/admin/dossiers/defaut", url.Values{
					"chemin": {cheminEnferme},
					"niveau": {"invisible"},
				}).Result()
			},
		},
		{
			nom: "par le droit posé sur sa propre fiche",
			geste: func(t *testing.T, b *bancEnfermement) *http.Response {
				return poste(t, b.h, b.ck, b.ficheAdmin()+"/droits", url.Values{
					"chemin": {cheminEnferme},
					"niveau": {"invisible"},
				}).Result()
			},
		},
		{
			nom: "par le retrait d'un droit sur sa propre fiche",
			prepare: func(t *testing.T, b *bancEnfermement) {
				// Le dossier est fermé par son défaut, une exception le rouvre
				// pour l'admin. Retirer l'exception l'enferme.
				b.fermeLeDossierEnBase(t)
				if err := b.s.DB.SetPermission(b.admin.ID, cheminEnferme, perms.Ecriture); err != nil {
					t.Fatal(err)
				}
			},
			geste: func(t *testing.T, b *bancEnfermement) *http.Response {
				return poste(t, b.h, b.ck, b.ficheAdmin()+"/droits/supprimer", url.Values{
					"chemin": {cheminEnferme},
				}).Result()
			},
		},
		{
			nom: "par le défaut de son propre compte",
			geste: func(t *testing.T, b *bancEnfermement) *http.Response {
				return poste(t, b.h, b.ck, b.ficheAdmin()+"/defaut", url.Values{
					"niveau": {"invisible"},
				}).Result()
			},
		},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			b := monteEnfermement(t)
			if c.prepare != nil {
				c.prepare(t, b)
			}

			// Le dossier s'administre AVANT le geste. Sans ce contrôle, un cas
			// dont la préparation échoue passerait pour un enfermement.
			if rec := get(b.h, hrefDossiers(cheminEnferme), b.ck); rec.Code != http.StatusOK {
				t.Fatalf("avant le geste : le dossier devait déjà s'administrer, obtenu %d", rec.Code)
			}

			if resp := c.geste(t, b); resp.StatusCode >= 400 {
				t.Fatalf("le geste lui-même a échoué : %d", resp.StatusCode)
			}

			// Le dossier reste administrable par son adresse.
			if rec := get(b.h, hrefDossiers(cheminEnferme), b.ck); rec.Code != http.StatusOK {
				t.Errorf("ENFERMÉ : après le geste, le dossier rend %d au lieu de 200. "+
					"Il n'est plus administrable depuis le siège qui l'a fermé", rec.Code)
			}

			// Et il reste ATTEIGNABLE depuis l'accueil. Sans ça, « administrable »
			// serait vrai pour qui connaît l'URL et faux pour tout le monde : un
			// mainteneur qui ne retrouve pas le dossier est enfermé en pratique.
			if body := get(b.h, "/admin/", b.ck).Body.String(); !strings.Contains(body, "shared") {
				t.Errorf("ENFERMÉ DEPUIS L'ACCUEIL : l'espace du dossier fermé n'y figure plus, " +
					"donc le mainteneur ne peut y arriver qu'en tapant l'adresse")
			}
		})
	}
}
