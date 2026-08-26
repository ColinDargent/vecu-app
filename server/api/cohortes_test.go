package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// Les cohortes, mesurées sur l'API RÉELLE plutôt que sur un banc.
//
// C'est la discipline retenue le 21/08 pour les groupes de skills, et elle vaut
// double ici : ce que le web affiche et ce que le poste d'un membre télécharge
// sont deux chemins différents, et rien ne garantit qu'ils s'accordent sans le
// vérifier. `GET /tree` est ce que voit vraiment le client de sync.
func serveurCohortes(t *testing.T) (*httptest.Server, *db.DB, *db.User, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "vecu.db"))
	if err != nil {
		t.Fatalf("db.Open : %v", err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}
	store.Write("rh/salaires.md", "confidentiel", "colin")
	store.Write("clients/acme/note.md", "note", "colin")
	store.Write("clients/autre/note.md", "note", "colin")

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	caroline, _ := database.CreateUser("caroline", "pw", perms.Invisible, false)
	tokens := map[string]string{}
	tokens["colin"], _ = database.IssueToken(colin.ID, "test")
	tokens["caroline"], _ = database.IssueToken(caroline.ID, "test")

	fixed := time.Date(2026, 8, 25, 17, 30, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)
	return srv, database, caroline, tokens
}

func cheminsDuTree(t *testing.T, srv *httptest.Server, token string) map[string]bool {
	t.Helper()
	code, body := get(t, srv, token, "/tree")
	if code != http.StatusOK {
		t.Fatalf("GET /tree : %d", code)
	}
	out := map[string]bool{}
	// La réponse porte `paths`, une liste de chaînes. Vérifié en lisant la
	// réponse réelle, pas le modèle Go : c'est le format que le client de sync
	// consomme.
	brut, present := body["paths"]
	if !present {
		t.Fatalf("GET /tree : pas de clé `paths` dans %v", body)
	}
	// Un arbre vide sérialise `paths` à null, pas à []. C'est le format réel, on
	// le lit tel quel plutôt que de faire échouer un cas légitime.
	if brut == nil {
		return out
	}
	chemins, ok := brut.([]any)
	if !ok {
		t.Fatalf("GET /tree : `paths` n'est pas une liste : %T", brut)
	}
	for _, c := range chemins {
		if p, ok := c.(string); ok {
			out[p] = true
		}
	}
	return out
}

// TestCohorteOuvreUnDossierDansLaSync : un dossier rangé dans une cohorte arrive
// bien sur le poste de ses membres, et uniquement là.
func TestCohorteOuvreUnDossierDansLaSync(t *testing.T) {
	srv, database, caroline, tokens := serveurCohortes(t)

	if vus := cheminsDuTree(t, srv, tokens["caroline"]); len(vus) != 0 {
		t.Fatalf("prérequis : un compte invisible voit déjà %v", vus)
	}

	salaries, err := database.CreerGroupe("Salariés")
	if err != nil {
		t.Fatalf("CreerGroupe : %v", err)
	}
	if err := database.SetMembreGroupe(salaries, caroline.ID, perms.Lecture); err != nil {
		t.Fatalf("SetMembreGroupe : %v", err)
	}
	if err := database.RangeChemin(salaries, "clients", "dossier", ""); err != nil {
		t.Fatalf("RangeChemin : %v", err)
	}

	vus := cheminsDuTree(t, srv, tokens["caroline"])
	for _, attendu := range []string{"clients/acme/note.md", "clients/autre/note.md"} {
		if !vus[attendu] {
			t.Errorf("%s absent de la synchronisation du membre : %v", attendu, vus)
		}
	}
	if vus["rh/salaires.md"] {
		t.Error("la cohorte a ouvert un dossier qu'elle ne porte pas")
	}
}

// TestDeuxCohortesSurLeMemeArbreDansLaSync : le repli sur les ancêtres tient
// jusque dans ce que le poste télécharge, pas seulement dans le résolveur.
func TestDeuxCohortesSurLeMemeArbreDansLaSync(t *testing.T) {
	srv, database, caroline, tokens := serveurCohortes(t)

	large, _ := database.CreerGroupe("Salariés")
	database.SetMembreGroupe(large, caroline.ID, perms.Ecriture)
	database.RangeChemin(large, "clients", "dossier", "")

	// Une cohorte plus précise, plus bas, à un niveau plus faible.
	precise, _ := database.CreerGroupe("Prestataires")
	database.SetMembreGroupe(precise, caroline.ID, perms.Lecture)
	database.RangeChemin(precise, "clients/acme", "dossier", "")

	if vus := cheminsDuTree(t, srv, tokens["caroline"]); !vus["clients/acme/note.md"] {
		t.Errorf("la deuxième cohorte a fermé ce que la première ouvrait : %v", vus)
	}
	u, _ := database.UserByID(caroline.ID)
	if niveau, _ := database.Effective(u, "clients/acme/note.md"); niveau != perms.Ecriture {
		t.Errorf("niveau effectif = %v, ecriture attendue (le plus permissif)", niveau)
	}
}
