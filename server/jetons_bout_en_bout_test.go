package main

// Le cycle de vie d'un jeton MCP, DE BOUT EN BOUT et sur le montage réel : le
// même mux racine que `main`, l'écran d'admin d'un côté, la surface MCP de
// l'autre.
//
// Pourquoi ici. Les tests de `server/web` mesurent la révocation au niveau de
// la base, ce qui est plus faible que le critère : « révoquer coupe l'accès au
// PREMIER APPEL SUIVANT ». Ce qu'il faut mesurer, c'est un appel MCP réel qui
// bascule de 200 à 401 après un clic sur « Révoquer ». Les deux serveurs sont
// dans deux paquets, et seul le binaire les monte ensemble - donc le banc vit
// dans le paquet du binaire.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
	"github.com/colindargent/vecu/server/tickets"
	"github.com/colindargent/vecu/server/web"
)

// montageReel : le mux racine de `main`, à l'identique.
func montageReel(t *testing.T) (*httptest.Server, *db.DB) {
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
	store.Write("equipe/note.md", "une note\n", "colin")

	billets := tickets.New()
	apiSrv := &api.Server{DB: database, Store: store, Tickets: billets}
	webSrv := &web.Server{DB: database, Store: store, Tickets: billets}
	root := http.NewServeMux()
	root.Handle("/admin/", webSrv.Handler())
	root.Handle("/", apiSrv.Handler())
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, database
}

// appelMCPBrut : un `tools/call` conforme, et rend le code HTTP AVEC le corps.
//
// Le corps compte : la première version n'assertait que le statut, et un
// `tools/call` sur un outil inexistant rend 200 avec une erreur JSON-RPC
// dedans. Le test passait donc en mesurant une frontière (le bearer) et rien
// de ce qu'il annonçait mesurer.
func appelMCPBrut(t *testing.T, url, jeton, outil string, args map[string]any) (int, string) {
	t.Helper()
	params := map[string]any{
		"name": outil, "arguments": args,
		"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
	}
	brut, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	req, _ := http.NewRequest("POST", url+"/mcp", bytes.NewReader(brut))
	req.Header.Set("Authorization", "Bearer "+jeton)
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", outil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	corps, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(corps)
}

func TestJetonCreeParLEcranPuisRevoqueCoupeAuPremierAppel(t *testing.T) {
	srv, database := montageReel(t)
	if _, err := database.CreateUser("colin", "motdepasse", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// 1. Connexion à l'admin web.
	form := url.Values{"username": {"colin"}, "password": {"motdepasse"}}
	req, _ := http.NewRequest("POST", srv.URL+"/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "vecu_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("aucun cookie de session")
	}

	// 2. Création du jeton PAR L'ÉCRAN.
	form = url.Values{"libelle": {"meeting-processor"}, "plafond": {"ecriture"}}
	req, _ = http.NewRequest("POST", srv.URL+"/admin/jetons", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	corps, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`<pre>([A-Za-z0-9_-]{40,})</pre>`).FindStringSubmatch(string(corps))
	if m == nil {
		t.Fatalf("aucun jeton dans la page de création")
	}
	jeton := m[1]

	// 3. IL MARCHE, par la surface MCP.
	code, corpsAppel := appelMCPBrut(t, srv.URL, jeton, "lire_fichier", map[string]any{"chemin": "equipe/note.md"})
	if code != http.StatusOK {
		t.Fatalf("avant révocation : HTTP %d, attendu 200", code)
	}
	// LE CONTENU, pas seulement le statut : c'est ce qui distingue une lecture
	// qui marche d'une erreur emballée dans un 200.
	if !strings.Contains(corpsAppel, "une note") {
		t.Fatalf("le jeton ne lit pas réellement le fichier : %s", corpsAppel)
	}
	if strings.Contains(corpsAppel, `"isError":true`) {
		t.Fatalf("l'appel est en isError : %s", corpsAppel)
	}

	// 4. Révocation PAR L'ÉCRAN.
	liste, _ := database.ListJetonsMCP(1)
	if len(liste) != 1 {
		t.Fatalf("%d jeton(s)", len(liste))
	}
	form = url.Values{"id": {strconv.FormatInt(liste[0].ID, 10)}}
	req, _ = http.NewRequest("POST", srv.URL+"/admin/jetons/revoquer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("révocation : HTTP %d", resp.StatusCode)
	}

	// 5. AU PREMIER APPEL SUIVANT : coupé. Pas au second, pas après un délai.
	if code, _ := appelMCPBrut(t, srv.URL, jeton, "lire_fichier", map[string]any{"chemin": "equipe/note.md"}); code != http.StatusUnauthorized {
		t.Errorf("après révocation, PREMIER appel : HTTP %d, attendu 401", code)
	}

	// 6. Et l'écran garde la trace, plutôt que de faire disparaître la ligne.
	req, _ = http.NewRequest("GET", srv.URL+"/admin/jetons", nil)
	req.AddCookie(session)
	resp, _ = client.Do(req)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "meeting-processor") {
		t.Error("le jeton révoqué a disparu de l'écran : on ne peut plus savoir qu'il a existé")
	}
	if !strings.Contains(string(page), "révoqué le") {
		t.Error("l'écran ne dit pas que le jeton est révoqué")
	}
}

// LE PLAFOND, DE BOUT EN BOUT. Le template promet noir sur blanc « un jeton en
// lecture seule ne peut rien écrire, même si votre compte le peut ». Rien ne
// testait cette phrase sur l'assemblage réel : la couverture existait au
// niveau de la base seulement.
func TestUnJetonCreeEnLectureSeuleNePeutPasEcrire(t *testing.T) {
	srv, database := montageReel(t)
	if _, err := database.CreateUser("colin", "motdepasse", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	session := connecte(t, client, srv.URL)

	// Un jeton EN LECTURE SEULE, créé par l'écran.
	form := url.Values{"libelle": {"lecture seule"}, "plafond": {"lecture"}}
	req, _ := http.NewRequest("POST", srv.URL+"/admin/jetons", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`<pre>([A-Za-z0-9_-]{40,})</pre>`).FindStringSubmatch(string(page))
	if m == nil {
		t.Fatal("aucun jeton dans la page")
	}
	jeton := m[1]

	// IL LIT.
	code, corps := appelMCPBrut(t, srv.URL, jeton, "lire_fichier", map[string]any{"chemin": "equipe/note.md"})
	if code != http.StatusOK || !strings.Contains(corps, "une note") {
		t.Fatalf("le jeton lecture ne lit pas : HTTP %d %s", code, corps)
	}

	// IL N'ÉCRIT PAS, alors que le compte est admin en écriture partout.
	_, corps = appelMCPBrut(t, srv.URL, jeton, "ecrire_fichier",
		map[string]any{"chemin": "equipe/tentative.md", "contenu": "non\n"})
	if !strings.Contains(corps, `"isError":true`) {
		t.Errorf("un jeton en lecture seule a écrit : %s", corps)
	}
	if !strings.Contains(corps, "lecture seule") {
		t.Errorf("le refus ne dit pas que c'est le plafond : %s", corps)
	}

	// Et il ne voit même pas les outils d'écriture dans son catalogue.
	brut, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
		"params": map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"}},
	})
	req, _ = http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(brut))
	req.Header.Set("Authorization", "Bearer "+jeton)
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/list")
	resp, _ = client.Do(req)
	liste, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(liste), "ecrire_fichier") {
		t.Errorf("le catalogue d'un jeton lecture propose l'écriture : %s", liste)
	}
}

// connecte ouvre une session admin et rend le cookie.
func connecte(t *testing.T, client *http.Client, url string) *http.Cookie {
	t.Helper()
	form := "username=colin&password=motdepasse"
	req, _ := http.NewRequest("POST", url+"/admin/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "vecu_session" {
			return c
		}
	}
	t.Fatal("aucun cookie de session")
	return nil
}
