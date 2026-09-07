package api

// CE QUE COLIN A DEMANDE LE 01/09 : « il faut que le MCP soit accessible aux
// utilisateurs, et que le jeton herite des memes droits que la cohorte dont
// fait partie la personne. »
//
// Ce banc verrouille les deux moities, parce que les deux tenaient deja et que
// rien ne les tenait explicitement. Une propriete qui marche par accident se
// perd au premier refactor.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

func TestLeJetonMCPHeriteDesDroitsDeLaCohorte(t *testing.T) {
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
	if _, err := store.Write("shared/direction/budget.md", "chiffres", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write("shared/public/note.md", "ordinaire", "colin"); err != nil {
		t.Fatal(err)
	}

	// Un compte ORDINAIRE, pas un administrateur : c'est le sujet de la demande.
	membre, err := database.CreateUser("marie", "mdp", perms.Lecture, false)
	if err != nil {
		t.Fatal(err)
	}
	// Le dossier est ferme a tout le monde par son defaut...
	if err := database.SetDefautDossier("shared/direction", perms.Invisible); err != nil {
		t.Fatal(err)
	}
	cohorte, err := database.CreerGroupe("direction")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetMembreGroupe(cohorte, membre.ID, perms.Lecture); err != nil {
		t.Fatal(err)
	}

	jeton, err := database.IssueMCPToken(membre.ID, "poste de marie", perms.Lecture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{DB: database, Store: store}).Handler())
	t.Cleanup(srv.Close)

	// AVANT : la cohorte ne porte pas encore le chemin, le jeton ne lit rien.
	// Sans ce controle, l'assertion suivante ne distingue pas « la cohorte a
	// ouvert » de « aucune regle ne fermait », et elle passerait meme si les
	// cohortes n'etaient plus lues du tout.
	if txt := litParMCP(t, srv, jeton, "shared/direction/budget.md"); strings.Contains(txt, "chiffres") {
		t.Fatalf("le dossier devait être fermé avant l'entrée en cohorte : %q", txt)
	}

	// SEULE la cohorte le rouvre, et le jeton suit sans etre retouche.
	if err := database.RangeChemin(cohorte, "shared/direction", "dossier", ""); err != nil {
		t.Fatal(err)
	}
	if txt := litParMCP(t, srv, jeton, "shared/direction/budget.md"); !strings.Contains(txt, "chiffres") {
		t.Errorf("le jeton n'hérite pas du droit ouvert par la cohorte : %q", txt)
	}

	// Sortir le chemin de la cohorte le referme, sans qu'aucun jeton ne bouge.
	// C'est la propriété qui rend la demande de Colin utile : on gouverne au
	// niveau de la cohorte, et tous ses membres suivent.
	if err := database.SortChemin(cohorte, "shared/direction"); err != nil {
		t.Fatal(err)
	}
	if txt := litParMCP(t, srv, jeton, "shared/direction/budget.md"); strings.Contains(txt, "chiffres") {
		t.Error("le retrait du chemin de la cohorte ne referme pas le jeton")
	}

	// Et ce qui ne dependait pas de la cohorte n'a pas bouge.
	if txt := litParMCP(t, srv, jeton, "shared/public/note.md"); !strings.Contains(txt, "ordinaire") {
		t.Errorf("un chemin hors cohorte a été refermé par erreur : %q", txt)
	}
}

// litParMCP joue un vrai `tools/call` et rend le texte du resultat.
func litParMCP(t *testing.T, srv *httptest.Server, jeton, chemin string) string {
	t.Helper()
	params := map[string]any{
		"name":      "lire_fichier",
		"arguments": map[string]any{"chemin": chemin},
		"_meta":     map[string]any{champMetaVersion: revisionMCP},
	}
	brut, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params,
	})
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(string(brut)))
	req.Header.Set("Authorization", "Bearer "+jeton)
	req.Header.Set("MCP-Protocol-Version", revisionMCP)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "lire_fichier")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	res, _ := m["result"].(map[string]any)
	if res == nil {
		return "<aucun résultat>"
	}
	blocs, _ := res["content"].([]any)
	if len(blocs) == 0 {
		return ""
	}
	txt, _ := blocs[0].(map[string]any)["text"].(string)
	return txt
}
