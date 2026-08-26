package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// TestCreerEspaceDepuisLApp : DT5 + DT3. Le parcours A - « je partage un dossier
// qui existe déjà chez moi » - enchaîne création, montage et premier envoi sans
// quitter l'app, donc la création doit exister en JSON.
//
// FIGE UNE DÉCISION D'AUTORISATION : tout compte authentifié peut créer un
// espace, pas seulement un admin. La réserver aux admins rendrait le parcours A
// impossible pour Achille, donc pour tout le monde sauf une personne. Ce qui la
// rend tenable, c'est que le créateur reçoit l'écriture sur SON espace et que
// personne d'autre ne reçoit rien - le modèle de F3 pour les skills, appliqué
// aux espaces.
func TestCreerEspaceDepuisLApp(t *testing.T) {
	srv, database, tok := serveurEspaces(t)

	// Achille n'est PAS admin, et il crée quand même.
	code, corps := postEspace(t, srv, tok["achille"], "Clients 2026")
	if code != http.StatusOK {
		t.Fatalf("création par un non-admin refusée : %d %v", code, corps)
	}
	nom, _ := corps["nom"].(string)
	if nom != "clients-2026" {
		t.Fatalf("nom technique = %q, attendu \"clients-2026\"", nom)
	}
	if corps["libelle"] != "Clients 2026" {
		t.Errorf("libellé rendu = %v", corps["libelle"])
	}

	// Le créateur peut y écrire, sinon il vient de créer un dossier invisible.
	rules, err := database.Rules(idDe(t, database, "achille"))
	if err != nil {
		t.Fatal(err)
	}
	if !perms.CanWrite(nom+"/note.md", perms.Invisible, rules) {
		t.Error("le créateur n'a pas l'écriture sur son propre espace")
	}

	// Et PERSONNE d'autre ne reçoit rien. C'est ce qui rend la décision tenable :
	// créer un espace ne donne accès à rien qu'on n'avait pas.
	rulesColin, err := database.Rules(idDe(t, database, "colin"))
	if err != nil {
		t.Fatal(err)
	}
	if perms.CanRead(nom+"/note.md", perms.Invisible, rulesColin) {
		t.Error("un espace créé par un tiers est lisible par un autre compte sans partage")
	}

	// Le libellé voyage dans GET /espaces : le client n'a pas à aller le chercher.
	_, liste := get(t, srv, tok["achille"], "/espaces")
	trouve := false
	for _, it := range liste["espaces"].([]any) {
		e := it.(map[string]any)
		if e["nom"] == nom {
			trouve = true
			if e["libelle"] != "Clients 2026" {
				t.Errorf("libellé dans la liste = %v", e["libelle"])
			}
		}
	}
	if !trouve {
		t.Errorf("l'espace créé n'apparaît pas dans /espaces : %v", liste["espaces"])
	}
}

// TestDeuxPersonnesPartagentUnDossierNotes : le cas que DT3 nomme. Le serveur
// désambiguïse ET le dit - « plutôt que de fusionner deux dossiers sans
// rapport », ce qui ferait apparaître le contenu de l'un chez l'autre.
func TestDeuxPersonnesPartagentUnDossierNotes(t *testing.T) {
	srv, _, tok := serveurEspaces(t)

	_, a := postEspace(t, srv, tok["colin"], "Notes")
	_, b := postEspace(t, srv, tok["achille"], "Notes")

	if a["nom"] == b["nom"] {
		t.Fatalf("les deux dossiers ont fusionné sous %q", a["nom"])
	}
	// Le nom retenu est RENDU : quelqu'un qui partage « Notes » et reçoit
	// « notes-2 » doit l'apprendre maintenant, pas le découvrir dans un chemin.
	if b["nom"] != "notes-2" {
		t.Errorf("second nom = %v", b["nom"])
	}
	// Mais les deux gardent le libellé qu'ils ont écrit.
	if a["libelle"] != "Notes" || b["libelle"] != "Notes" {
		t.Errorf("libellés = %v / %v", a["libelle"], b["libelle"])
	}
}

// TestCreerEspaceRefuseUnLibelleVide : un espace sans nom n'est pas un espace.
func TestCreerEspaceRefuseUnLibelleVide(t *testing.T) {
	srv, _, tok := serveurEspaces(t)
	for _, libelle := range []string{"", "   "} {
		if code, _ := postEspace(t, srv, tok["colin"], libelle); code != http.StatusBadRequest {
			t.Errorf("libellé %q : attendu 400, obtenu %d", libelle, code)
		}
	}
}

// --- outillage ---

func serveurEspaces(t *testing.T) (*httptest.Server, *db.DB, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	colin, _ := database.CreateUser("colin", "pw", perms.Invisible, true)      // admin
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false) // pas admin

	tok := map[string]string{}
	tok["colin"], _ = database.IssueToken(colin.ID, "t")
	tok["achille"], _ = database.IssueToken(achille.ID, "t")

	idsEspaces = map[string]int64{"colin": colin.ID, "achille": achille.ID}
	srv := httptest.NewServer((&Server{DB: database, Store: store}).Handler())
	t.Cleanup(srv.Close)
	return srv, database, tok
}

// idsEspaces : les comptes du serveur de test courant. Un test à la fois par
// paquet sur cette table, et elle est réécrite à chaque construction.
var idsEspaces map[string]int64

func postEspace(t *testing.T, srv *httptest.Server, token, libelle string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"libelle": libelle})
	req, _ := http.NewRequest("POST", srv.URL+"/espaces", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var corps map[string]any
	json.NewDecoder(resp.Body).Decode(&corps)
	return resp.StatusCode, corps
}

func idDe(t *testing.T, _ *db.DB, nom string) int64 {
	t.Helper()
	id, ok := idsEspaces[nom]
	if !ok {
		t.Fatalf("compte inconnu : %s", nom)
	}
	return id
}

var _ = espaces.FichierMeta // le paquet est bien celui qu'on teste indirectement
