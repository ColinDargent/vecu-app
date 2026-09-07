package sync

// Le critère central de la slice d'écriture MCP : une écriture faite par un
// agent, sans poste allumé, redescend sur les postes au cycle suivant.
//
// Le banc vit dans `client/sync` parce que c'est là que vit le moteur, et il
// n'ajoute AUCUNE ligne au client : il monte un serveur de test comme les
// bancs existants, écrit par la porte MCP, et fait tourner un vrai cycle.
//
// C'est ce qui rend crédible la bascule du `meeting-processor` : sans cette
// preuve, « le MCP écrit » ne dirait rien de ce que les postes voient.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// serveurAvecMCP : comme `testServer`, mais rend aussi un jeton MCP en écriture.
func serveurAvecMCP(t *testing.T) (url, jetonAppareil, jetonMCP string) {
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
	u, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	jetonAppareil, _ = database.IssueToken(u.ID, "poste")
	jetonMCP, _ = database.IssueMCPToken(u.ID, "routine cloud", perms.Ecriture)
	if _, err := store.Write("equipe/README.md", "# equipe\n", "colin"); err != nil {
		t.Fatal(err)
	}
	fixe := time.Date(2026, 8, 31, 17, 30, 0, 0, time.UTC)
	srv := httptest.NewServer((&api.Server{DB: database, Store: store, Now: func() time.Time { return fixe }}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, jetonAppareil, jetonMCP
}

// ecrisParMCP joue un vrai `tools/call` sur la porte MCP.
func ecrisParMCP(t *testing.T, url, jeton, chemin, contenu string) string {
	t.Helper()
	params := map[string]any{
		"name":      "ecrire_fichier",
		"arguments": map[string]any{"chemin": chemin, "contenu": contenu},
		"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
	}
	brut, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	req, _ := http.NewRequest("POST", url+"/mcp", bytes.NewReader(brut))
	req.Header.Set("Authorization", "Bearer "+jeton)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "ecrire_fichier")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("écriture MCP : HTTP %d", resp.StatusCode)
	}
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	res, _ := m["result"].(map[string]any)
	if res == nil {
		t.Fatalf("aucun résultat : %v", m)
	}
	if isErr, _ := res["isError"].(bool); isErr {
		blocs, _ := res["content"].([]any)
		t.Fatalf("écriture MCP refusée : %v", blocs)
	}
	blocs, _ := res["content"].([]any)
	txt, _ := blocs[0].(map[string]any)["text"].(string)
	return txt
}

// moteurSur : un moteur de sync branché sur ce serveur.
func moteurSur(t *testing.T, url, jetonAppareil string) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "colin", Token: jetonAppareil}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e.Close() })
	e.Importer([]string{"equipe"})
	return e, dir
}

// LE CRITÈRE : une écriture MCP redescend sur LES DEUX postes au cycle suivant.
func TestEcritureMCPRedescendSurLesPostes(t *testing.T) {
	url, jetonAppareil, jetonMCP := serveurAvecMCP(t)
	a, dirA := moteurSur(t, url, jetonAppareil)
	b, dirB := moteurSur(t, url, jetonAppareil)

	// Les deux postes partent alignés.
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A initial : %v", err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B initial : %v", err)
	}

	// L'agent écrit, sans qu'aucun poste soit impliqué.
	const chemin = "equipe/notes/compte-rendu.md"
	const contenu = "# Compte rendu\nÉcrit par un agent, sans poste allumé.\n"
	ecrisParMCP(t, url, jetonMCP, chemin, contenu)

	// Cycle suivant : les deux postes le reçoivent.
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B : %v", err)
	}
	for nom, dir := range map[string]string{"A": dirA, "B": dirB} {
		got, ok := readFile(t, dir, chemin)
		if !ok {
			t.Errorf("poste %s : le fichier n'est pas redescendu", nom)
			continue
		}
		if got != contenu {
			t.Errorf("poste %s : contenu %q, attendu %q", nom, got, contenu)
		}
	}
	// Et AUCUNE copie de conflit nulle part : c'est le piège du baseOID vide.
	for nom, dir := range map[string]string{"A": dirA, "B": dirB} {
		if copies := copiesDeConflit(t, dir, "equipe"); len(copies) != 0 {
			t.Errorf("poste %s : copies de conflit %v", nom, copies)
		}
	}
}

// Une écriture MCP sur un chemin qu'un poste porte déjà FUSIONNE, elle
// n'écrase pas - exactement comme entre deux postes.
func TestEcritureMCPFusionneAvecUnPoste(t *testing.T) {
	url, jetonAppareil, jetonMCP := serveurAvecMCP(t)
	a, dirA := moteurSur(t, url, jetonAppareil)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// L'agent modifie une AUTRE ligne que celle que le poste touchera.
	ecrisParMCP(t, url, jetonMCP, "equipe/plan.md", "L1-agent\nL2\nL3\n")

	// Le poste modifie sa ligne à lui, puis synchronise.
	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3-poste\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A après : %v", err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A convergence : %v", err)
	}

	final, _ := readFile(t, dirA, "equipe/plan.md")
	if !bytes.Contains([]byte(final), []byte("L1-agent")) {
		t.Errorf("l'écriture de l'agent a disparu : %q", final)
	}
	if !bytes.Contains([]byte(final), []byte("L3-poste")) {
		t.Errorf("l'écriture du poste a disparu : %q", final)
	}
}

// Une suppression MCP redescend elle aussi : le fichier disparaît du poste.
func TestSuppressionMCPRedescendSurLePoste(t *testing.T) {
	url, jetonAppareil, jetonMCP := serveurAvecMCP(t)
	a, dirA := moteurSur(t, url, jetonAppareil)

	const chemin = "equipe/ephemere.md"
	ecrisParMCP(t, url, jetonMCP, chemin, "à supprimer\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}
	if _, ok := readFile(t, dirA, chemin); !ok {
		t.Fatal("précondition : le fichier devrait être descendu")
	}

	// L'agent supprime.
	params := map[string]any{
		"name":      "supprimer_fichier",
		"arguments": map[string]any{"chemin": chemin},
		"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
	}
	brut, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	req, _ := http.NewRequest("POST", url+"/mcp", bytes.NewReader(brut))
	req.Header.Set("Authorization", "Bearer "+jetonMCP)
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "supprimer_fichier")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A après suppression : %v", err)
	}
	if _, ok := readFile(t, dirA, chemin); ok {
		t.Error("le fichier est toujours sur le poste après une suppression MCP")
	}
}
