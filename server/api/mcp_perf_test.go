package api

// La mesure que la spec exige dès S2 : `chercher` n'a PAS d'index, il lit le
// contenu de chaque fichier visible. Il faut savoir ce que ça coûte, et à
// partir de quand ça ne tient plus.
//
// UN BENCHMARK, et pas un test. La première version était un test : elle
// coûtait 75 s à chaque `go test ./...`, et elle ne passait même pas par
// l'outil - elle réimplémentait la boucle, donc elle mesurait la primitive.
// Ici, le vrai chemin, par la vraie porte HTTP, et rien ne tourne sans
// `-bench`.
//
//	go test ./server/api/ -run '^$' -bench BenchmarkChercher -benchtime 3x
//
// MESURÉ le 31/08 sur 900 notes de ~2 Ko (la taille du vault de Colin) :
//
//	lecture un par un (Store.Read)   : ~9,0 s   -> inutilisable, au-delà du
//	                                              délai d'attente d'un client
//	lecture par lot  (Store.ReadBatch): ~0,17 s -> 50x, et c'est ce qui est livré
//
// C'est cette mesure qui a imposé `ReadBatch` : la spec supposait « acceptable
// à cette taille », l'intuition était fausse.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// vaultDeMesure monte un dépôt à l'échelle du vault réel.
func vaultDeMesure(b *testing.B, n int) (*httptest.Server, *storage.Store, []string, string) {
	b.Helper()
	dir := b.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		b.Fatal(err)
	}
	corps := strings.Repeat("une ligne de note ordinaire dans un second cerveau\n", 40)
	chemins := make([]string, 0, n)
	for i := range n {
		p := fmt.Sprintf("shared/notes/n%04d.md", i)
		if _, err := store.Write(p, corps, "colin"); err != nil {
			b.Fatal(err)
		}
		chemins = append(chemins, p)
	}
	u, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	jeton, _ := database.IssueMCPToken(u.ID, "mesure", perms.Lecture)
	srv := httptest.NewServer((&Server{DB: database, Store: store}).Handler())
	b.Cleanup(srv.Close)
	return srv, store, chemins, jeton
}

// chercheParLaPorte joue un vrai `tools/call` et rend le texte du résultat.
func chercheParLaPorte(b *testing.B, srv *httptest.Server, jeton, motif string) string {
	b.Helper()
	params := map[string]any{
		"name": "chercher", "arguments": map[string]any{"motif": motif},
		"_meta": map[string]any{champMetaVersion: revisionMCP},
	}
	brut, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(string(brut)))
	req.Header.Set("Authorization", "Bearer "+jeton)
	req.Header.Set("MCP-Protocol-Version", revisionMCP)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "chercher")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	res, _ := m["result"].(map[string]any)
	if res == nil {
		b.Fatalf("aucun résultat : %v", m)
	}
	blocs, _ := res["content"].([]any)
	if len(blocs) == 0 {
		return ""
	}
	txt, _ := blocs[0].(map[string]any)["text"].(string)
	return txt
}

// Le PIRE CAS : un motif absent de partout, donc aucun plafond de résultats
// n'interrompt le parcours et tout le périmètre visible est lu.
func BenchmarkChercherPireCas(b *testing.B) {
	srv, _, _, jeton := vaultDeMesure(b, 900)
	b.ResetTimer()
	for b.Loop() {
		if txt := chercheParLaPorte(b, srv, jeton, "motifquinexistenullepart"); !strings.Contains(txt, "Aucun résultat") {
			b.Fatalf("attendu aucun résultat, obtenu %q", txt)
		}
	}
}

// Le cas COURANT : un motif fréquent, où le plafond de résultats arrête la
// recherche après quelques tranches.
func BenchmarkChercherCasCourant(b *testing.B) {
	srv, _, _, jeton := vaultDeMesure(b, 900)
	b.ResetTimer()
	for b.Loop() {
		if txt := chercheParLaPorte(b, srv, jeton, "second cerveau"); !strings.Contains(txt, "shared/notes/") {
			b.Fatalf("attendu des résultats, obtenu %q", txt)
		}
	}
}

// Le témoin qui justifie `ReadBatch` : la même lecture, fichier par fichier.
func BenchmarkLectureUnParUnPourComparaison(b *testing.B) {
	_, store, chemins, _ := vaultDeMesure(b, 900)
	b.ResetTimer()
	for b.Loop() {
		for _, p := range chemins[:100] { // 100 seulement : le reste est extrapolable
			if _, err := store.Read(p, ""); err != nil {
				b.Fatal(err)
			}
		}
	}
}
