package api

import (
	"bytes"
	"encoding/base64"
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

// setupMCP : un serveur avec un jeton MCP en écriture pour `colin`.
//
// Son propre harnais plutôt qu'un élargissement de `setup` : celui-ci ne rend
// pas la base, et le faire changer obligerait à toucher tous les tests qui
// s'en servent. Discipline de périmètre - la spec MCP ne demande aucune
// modification de l'existant.
func setupMCP(t *testing.T) (*httptest.Server, *db.DB, map[string]int64, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("db.Open : %v", err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}
	store.Write("public/guide.md", "# Guide\n", "colin")
	store.Write("prive/interne.md", "interne\n", "colin")

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false)
	database.SetPermission(achille.ID, "public", perms.Lecture)

	ids := map[string]int64{"colin": colin.ID, "achille": achille.ID}
	jetons := map[string]string{}
	jetons["colin"], _ = database.IssueMCPToken(colin.ID, "banc", perms.Ecriture)
	jetons["achille"], _ = database.IssueMCPToken(achille.ID, "banc", perms.Lecture)

	srv := httptest.NewServer((&Server{DB: database, Store: store}).Handler())
	t.Cleanup(srv.Close)
	return srv, database, ids, jetons
}

// appelMCP monte un POST /mcp complet et conforme, puis laisse `retouche`
// l'abîmer. Les en-têtes sont dérivés du corps, comme un vrai client les
// dérive : un banc qui les écrirait à la main ne testerait pas la même chose.
func appelMCP(t *testing.T, srv *httptest.Server, jeton, methode string, params map[string]any, retouche func(*http.Request)) *http.Response {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{champMetaVersion: revisionMCP}
	corps := map[string]any{"jsonrpc": "2.0", "id": 1, "method": methode, "params": params}
	brut, _ := json.Marshal(corps)
	req, err := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(brut))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", revisionMCP)
	req.Header.Set("Mcp-Method", methode)
	if nom, _ := params["name"].(string); nom != "" {
		req.Header.Set("Mcp-Name", nom)
	}
	if jeton != "" {
		req.Header.Set("Authorization", "Bearer "+jeton)
	}
	if retouche != nil {
		retouche(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func corpsMCP(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("corps illisible : %v", err)
	}
	return m
}

// codeErreurMCP extrait `error.code`, ou 0 s'il n'y a pas d'erreur.
func codeErreurMCP(t *testing.T, resp *http.Response) int {
	t.Helper()
	m := corpsMCP(t, resp)
	e, ok := m["error"].(map[string]any)
	if !ok {
		return 0
	}
	c, _ := e["code"].(float64)
	return int(c)
}

func TestMCPPostRepondEtLesAutresMethodesRendent405(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	resp := appelMCP(t, srv, jetons["colin"], "tools/list", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST /mcp : %d, attendu 200", resp.StatusCode)
	}

	// GET et DELETE : ce que la révision 2026-07-28 demande de répondre à un
	// client de l'ère des sessions, qui ouvrirait un stream GET ou fermerait
	// une session par DELETE.
	for _, m := range []string{"GET", "DELETE"} {
		req, _ := http.NewRequest(m, srv.URL+"/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+jetons["colin"])
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp : %d, attendu 405", m, r.StatusCode)
		}
	}
}

// Toute Origin PRÉSENTE est refusée, et c'est le point de la garde.
//
// Les deux premières versions ne protégeaient de rien : comparer l'Origin au
// `Host` réussit toujours dans un DNS rebinding (le navigateur dérive les deux
// du même nom attaquant), et une liste blanche configurable ne pouvait admettre
// que des clients NON navigateurs, puisque `/mcp` n'émet aucun en-tête CORS et
// qu'un navigateur n'atteint donc jamais le POST.
func TestMCPToutOriginPresentEstRefuse(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	// Le cas du rebinding : Origin et Host portent le MÊME nom étranger. Une
	// garde qui compare l'un à l'autre laisse passer.
	resp := appelMCP(t, srv, jetons["colin"], "tools/list", nil, func(r *http.Request) {
		r.Host = "evil.example"
		r.Header.Set("Origin", "http://evil.example")
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rebinding (Origin == Host, tous deux étrangers) : %d, attendu 403", resp.StatusCode)
	}

	resp = appelMCP(t, srv, jetons["colin"], "tools/list", nil, func(r *http.Request) {
		r.Header.Set("Origin", "https://attaquant.example")
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("origine étrangère : %d, attendu 403", resp.StatusCode)
	}

	// Même l'hôte réellement servi est refusé : sans CORS, aucun navigateur ne
	// peut de toute façon arriver jusqu'ici, et un réglage qui prétendrait le
	// contraire ferait croire à une capacité inexistante.
	resp = appelMCP(t, srv, jetons["colin"], "tools/list", nil, func(r *http.Request) {
		r.Header.Set("Origin", "http://"+r.Host)
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Origin == hôte servi : %d, attendu 403", resp.StatusCode)
	}

	// Absente, la requête passe. C'est le chemin de TOUS nos clients.
	if resp := appelMCP(t, srv, jetons["colin"], "tools/list", nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("sans Origin : %d, attendu 200", resp.StatusCode)
	}
}

func TestMCPJetonAbsentInconnuOuRevoque(t *testing.T) {
	srv, database, ids, jetons := setupMCP(t)

	if resp := appelMCP(t, srv, "", "tools/list", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("jeton absent : %d, attendu 401", resp.StatusCode)
	}
	if resp := appelMCP(t, srv, "jeton-invente", "tools/list", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("jeton inconnu : %d, attendu 401", resp.StatusCode)
	}

	// Un jeton d'APPAREIL n'ouvre pas la porte MCP : les deux magasins sont
	// distincts, et c'est ce qui permet au plafond d'exister.
	appareil, _ := database.IssueToken(ids["colin"], "poste")
	if resp := appelMCP(t, srv, appareil, "tools/list", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("jeton d'appareil sur /mcp : %d, attendu 401", resp.StatusCode)
	}

	// Révocation : coupe AU PREMIER APPEL SUIVANT, pas au suivant du suivant.
	if resp := appelMCP(t, srv, jetons["achille"], "tools/list", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("avant révocation : %d, attendu 200", resp.StatusCode)
	}
	liste, err := database.ListJetonsMCP(ids["achille"])
	if err != nil || len(liste) != 1 {
		t.Fatalf("liste des jetons : %v (%d)", err, len(liste))
	}
	if err := database.RevoqueJetonMCPDuCompte(ids["achille"], liste[0].ID); err != nil {
		t.Fatal(err)
	}
	if resp := appelMCP(t, srv, jetons["achille"], "tools/list", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("après révocation : %d, attendu 401", resp.StatusCode)
	}
}

func TestMCPEntetesValideesContreLeCorps(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	cas := []struct {
		nom      string
		retouche func(*http.Request)
	}{
		{"MCP-Protocol-Version manquante", func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") }},
		{"MCP-Protocol-Version != corps", func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "2025-11-25") }},
		{"Mcp-Method manquante", func(r *http.Request) { r.Header.Del("Mcp-Method") }},
		{"Mcp-Method != corps", func(r *http.Request) { r.Header.Set("Mcp-Method", "tools/call") }},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			resp := appelMCP(t, srv, jetons["colin"], "tools/list", nil, c.retouche)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s : %d, attendu 400", c.nom, resp.StatusCode)
			}
			if code := codeErreurMCP(t, resp); code != codeHeaderMismatch {
				t.Errorf("%s : code %d, attendu %d", c.nom, code, codeHeaderMismatch)
			}
		})
	}

	// Mcp-Name est exigé sur `tools/call`, et sur lui seul.
	t.Run("Mcp-Name manquante sur tools/call", func(t *testing.T) {
		resp := appelMCP(t, srv, jetons["colin"], "tools/call",
			map[string]any{"name": "arborescence"},
			func(r *http.Request) { r.Header.Del("Mcp-Name") })
		if resp.StatusCode != http.StatusBadRequest || codeErreurMCP(t, resp) != codeHeaderMismatch {
			t.Errorf("Mcp-Name manquante : %d / %d", resp.StatusCode, codeErreurMCP(t, resp))
		}
	})

	t.Run("Mcp-Name != corps", func(t *testing.T) {
		resp := appelMCP(t, srv, jetons["colin"], "tools/call",
			map[string]any{"name": "arborescence"},
			func(r *http.Request) { r.Header.Set("Mcp-Name", "autre_chose") })
		if resp.StatusCode != http.StatusBadRequest || codeErreurMCP(t, resp) != codeHeaderMismatch {
			t.Errorf("Mcp-Name divergente : %d / %d", resp.StatusCode, codeErreurMCP(t, resp))
		}
	})
}

// La sentinelle base64 DOIT être décodée avant comparaison. Sans ça, tout nom
// qu'un client choisit d'encoder serait refusé à tort - et un client a le droit
// d'encoder même une valeur ASCII.
func TestMCPSentinelleBase64DecodeeAvantComparaison(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	encode := func(v string) string {
		return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
	}

	// Encodée et concordante : elle passe la validation d'en-têtes (l'outil
	// n'existe pas encore en S1, donc on vérifie qu'on est allé PLUS LOIN que
	// le rejet d'en-tête).
	resp := appelMCP(t, srv, jetons["colin"], "tools/call",
		map[string]any{"name": "arborescence"},
		func(r *http.Request) { r.Header.Set("Mcp-Name", encode("arborescence")) })
	if resp.StatusCode == http.StatusBadRequest {
		t.Fatalf("sentinelle concordante refusée en 400 : %v", corpsMCP(t, resp))
	}
	if code := codeErreurMCP(t, resp); code == codeHeaderMismatch {
		t.Errorf("sentinelle concordante rejetée en HeaderMismatch")
	}

	// Encodée et DIVERGENTE : elle doit être refusée, donc le décodage sert
	// bien à comparer et pas seulement à ne pas planter.
	resp = appelMCP(t, srv, jetons["colin"], "tools/call",
		map[string]any{"name": "arborescence"},
		func(r *http.Request) { r.Header.Set("Mcp-Name", encode("autre_chose")) })
	if resp.StatusCode != http.StatusBadRequest || codeErreurMCP(t, resp) != codeHeaderMismatch {
		t.Errorf("sentinelle divergente : %d / %d", resp.StatusCode, codeErreurMCP(t, resp))
	}

	// Base64 illisible : refusée, jamais acceptée « dans le doute ».
	resp = appelMCP(t, srv, jetons["colin"], "tools/call",
		map[string]any{"name": "arborescence"},
		func(r *http.Request) { r.Header.Set("Mcp-Name", "=?base64?ceci n'est pas du base64?=") })
	if resp.StatusCode != http.StatusBadRequest || codeErreurMCP(t, resp) != codeHeaderMismatch {
		t.Errorf("sentinelle illisible : %d / %d", resp.StatusCode, codeErreurMCP(t, resp))
	}
}

func TestMCPRevisionNonSupporteeAnnonceCeQuOnSert(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	// En-tête ET corps sur une révision ancienne : les deux concordent, donc on
	// passe la validation d'en-têtes et on tombe bien sur le refus de révision.
	corps := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
		"params": map[string]any{"_meta": map[string]any{champMetaVersion: "2025-06-18"}},
	}
	brut, _ := json.Marshal(corps)
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(brut))
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	req.Header.Set("Mcp-Method", "tools/list")
	req.Header.Set("Authorization", "Bearer "+jetons["colin"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("révision ancienne : %d, attendu 400", resp.StatusCode)
	}
	m := corpsMCP(t, resp)
	e, _ := m["error"].(map[string]any)
	if e == nil {
		t.Fatalf("aucune erreur JSON-RPC dans %v", m)
	}
	if c, _ := e["code"].(float64); int(c) != codeVersionNonSupportee {
		t.Errorf("code %v, attendu %d", e["code"], codeVersionNonSupportee)
	}
	data, _ := e["data"].(map[string]any)
	if data == nil {
		t.Fatalf("aucun champ data : %v", e)
	}
	sup, _ := data["supported"].([]any)
	if len(sup) != 1 || sup[0] != revisionMCP {
		t.Errorf("supported = %v, attendu [%s]", data["supported"], revisionMCP)
	}
	if data["requested"] != "2025-06-18" {
		t.Errorf("requested = %v, attendu 2025-06-18", data["requested"])
	}
}

func TestMCPMethodeInconnueRend404(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	// `initialize` en est une : cette révision l'a supprimé, et c'est
	// exactement le report assumé de la direction « toutes les actions via
	// Claude ». Le corps JSON-RPC distingue ce 404 de celui d'un serveur qui
	// n'hébergerait pas ce chemin du tout.
	for _, m := range []string{"initialize", "resources/list", "prompts/get", "n_importe_quoi"} {
		resp := appelMCP(t, srv, jetons["colin"], m, nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s : %d, attendu 404", m, resp.StatusCode)
		}
		if code := codeErreurMCP(t, resp); code != codeMethodeInconnue {
			t.Errorf("%s : code %d, attendu %d", m, code, codeMethodeInconnue)
		}
	}
}

func TestMCPNotificationRend202SansCorps(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	corps := map[string]any{
		"jsonrpc": "2.0", "method": "notifications/quelque_chose",
		"params": map[string]any{"_meta": map[string]any{champMetaVersion: revisionMCP}},
	}
	brut, _ := json.Marshal(corps)
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(brut))
	req.Header.Set("Authorization", "Bearer "+jetons["colin"])
	// Les en-têtes miroirs sont exigées ici AUSSI. Les faire sauter pour une
	// notification laissait passer un `Mcp-Method` menteur, acquitté en 202.
	req.Header.Set("MCP-Protocol-Version", revisionMCP)
	req.Header.Set("Mcp-Method", "notifications/quelque_chose")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification : %d, attendu 202", resp.StatusCode)
	}
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if buf.Len() != 0 {
		t.Errorf("202 avec un corps : %q", buf.String())
	}
}

func TestMCPToolsListRepondEnJSON(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	resp := appelMCP(t, srv, jetons["colin"], "tools/list", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list : %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !bytes.Contains([]byte(ct), []byte("application/json")) {
		t.Errorf("Content-Type = %q, attendu application/json", ct)
	}
	m := corpsMCP(t, resp)
	res, _ := m["result"].(map[string]any)
	if res == nil {
		t.Fatalf("aucun result : %v", m)
	}
	if res["resultType"] != "complete" {
		t.Errorf("resultType = %v, attendu \"complete\"", res["resultType"])
	}
	if _, ok := res["tools"].([]any); !ok {
		t.Fatalf("tools absent ou mal typé : %v", res["tools"])
	}
}

func TestMCPDernierUsageAvance(t *testing.T) {
	srv, database, ids, jetons := setupMCP(t)

	avant, _ := database.ListJetonsMCP(ids["colin"])
	if len(avant) != 1 {
		t.Fatalf("%d jeton(s), attendu 1", len(avant))
	}
	if avant[0].DernierUsage != "" {
		t.Errorf("dernier_usage renseigné avant tout appel : %q", avant[0].DernierUsage)
	}

	appelMCP(t, srv, jetons["colin"], "tools/list", nil, nil)

	apres, _ := database.ListJetonsMCP(ids["colin"])
	if apres[0].DernierUsage == "" {
		t.Error("dernier_usage n'a pas avancé après un appel accepté")
	}
}

// Le plafond est PORTÉ par le jeton dès sa création. Il ne mord que sur
// l'écriture (S3) : les trois outils de lecture n'exigent que `>= Lecture`, que
// le plafond ne peut pas descendre en dessous puisque `invisible` est refusé à
// la création.
func TestMCPPlafondDuJetonEstPorteDesLaCreation(t *testing.T) {
	_, database, ids, _ := setupMCP(t)

	colin, _ := database.ListJetonsMCP(ids["colin"])
	if colin[0].NiveauMax != perms.Ecriture {
		t.Errorf("plafond de colin = %v, attendu ecriture", colin[0].NiveauMax)
	}
	achille, _ := database.ListJetonsMCP(ids["achille"])
	if achille[0].NiveauMax != perms.Lecture {
		t.Errorf("plafond d'achille = %v, attendu lecture", achille[0].NiveauMax)
	}

	// Un plafond `invisible` serait un jeton qui ne peut rien : refusé à la
	// création plutôt qu'expliqué plus tard dans l'interface.
	if _, err := database.IssueMCPToken(ids["colin"], "muet", perms.Invisible); err == nil {
		t.Error("un jeton au plafond invisible a été créé")
	}
}

// server/discover est un MUST de la révision, et c'est le branchement d'un vrai
// client qui l'a sorti : la spec du vault n'énumérait que `tools/list`.
//
// Mesuré le 31/08 sur claude-code/2.1.227 : le client sonde `server/discover`
// EN PREMIER. Tant qu'on répondait 404, il retombait sur l'ère `initialize` et
// la connexion échouait.
func TestMCPServerDiscoverRepond(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	resp := appelMCP(t, srv, jetons["colin"], "server/discover", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("server/discover : %d, attendu 200", resp.StatusCode)
	}
	res, _ := corpsMCP(t, resp)["result"].(map[string]any)
	if res == nil {
		t.Fatalf("aucun result")
	}
	if res["resultType"] != "complete" {
		t.Errorf("resultType = %v", res["resultType"])
	}
	sup, _ := res["supportedVersions"].([]any)
	if len(sup) != 1 || sup[0] != revisionMCP {
		t.Errorf("supportedVersions = %v, attendu [%s]", res["supportedVersions"], revisionMCP)
	}
	caps, _ := res["capabilities"].(map[string]any)
	if caps == nil {
		t.Fatalf("aucune capabilities")
	}
	if _, ok := caps["tools"]; !ok {
		t.Error("la capacité tools n'est pas déclarée")
	}
	// Ni resources ni prompts : ils ajouteraient une seconde façon de lire la
	// même chose, et ils sont hors périmètre du jalon. Les annoncer ferait
	// sonder un client pour rien.
	for _, absent := range []string{"resources", "prompts"} {
		if _, ok := caps[absent]; ok {
			t.Errorf("capacité %q annoncée alors qu'elle est hors périmètre", absent)
		}
	}
	meta, _ := res["_meta"].(map[string]any)
	info, _ := meta["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if info == nil || info["name"] != "vecu" {
		t.Errorf("serverInfo = %v", meta)
	}
}

// Les indices de cache sont un MUST sur tout résultat `complete` de
// `server/discover` et `tools/list`. Un client conforme REFUSE le résultat sans
// eux : mesuré au branchement, Claude Code rejetait `tools/list` en nommant
// précisément `ttlMs` et `cacheScope`.
func TestMCPIndicesDeCacheObligatoires(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	cas := []struct {
		methode string
		scope   string
	}{
		// « private » sur les DEUX, et c'est une décision de sécurité. La spec
		// avertit qu'un résultat « public » peut être servi à un autre porteur
		// de jeton depuis un endpoint pourtant authentifié.
		//
		// `server/discover` ne porte aujourd'hui que l'identité du serveur,
		// donc « public » serait exact - mais le jour où les capacités
		// annoncées dépendront du plafond, « public » deviendrait faux par
		// omission et personne ne reviendrait le relire.
		{"server/discover", "private"},
		// `tools/list` dépend déjà du plafond du jeton.
		{"tools/list", "private"},
	}
	for _, c := range cas {
		t.Run(c.methode, func(t *testing.T) {
			resp := appelMCP(t, srv, jetons["colin"], c.methode, nil, nil)
			res, _ := corpsMCP(t, resp)["result"].(map[string]any)
			if res == nil {
				t.Fatalf("aucun result")
			}
			ttl, ok := res["ttlMs"].(float64)
			if !ok {
				t.Errorf("ttlMs absent ou mal typé : %v", res["ttlMs"])
			}
			if ttl < 0 {
				t.Errorf("ttlMs = %v, la spec exige >= 0", ttl)
			}
			if res["cacheScope"] != c.scope {
				t.Errorf("cacheScope = %v, attendu %q", res["cacheScope"], c.scope)
			}
		})
	}
}

// `initialize` est refusé EN NOMMANT ce qu'on sert. On ne l'implémente pas -
// la révision l'a supprimé - mais un client trop ancien n'a aucun mécanisme de
// rattrapage, et ce message est le seul diagnostic qu'il puisse afficher.
func TestMCPInitializeRefuseEnNommantLaRevision(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	// Tel qu'un client de l'ère des sessions l'envoie : sans aucun en-tête de
	// métadonnées. C'est ce qui rendait auparavant un « en-tête manquant »
	// parfaitement vrai et parfaitement inutile.
	corps := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}
	brut, _ := json.Marshal(corps)
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(brut))
	req.Header.Set("Authorization", "Bearer "+jetons["colin"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("initialize : %d, attendu 404", resp.StatusCode)
	}
	e, _ := corpsMCP(t, resp)["error"].(map[string]any)
	if e == nil {
		t.Fatalf("aucune erreur JSON-RPC")
	}
	if c, _ := e["code"].(float64); int(c) != codeMethodeInconnue {
		t.Errorf("code %v, attendu %d", e["code"], codeMethodeInconnue)
	}
	msg, _ := e["message"].(string)
	if !strings.Contains(msg, revisionMCP) {
		t.Errorf("le message ne nomme pas la révision servie : %q", msg)
	}
	data, _ := e["data"].(map[string]any)
	sup, _ := data["supported"].([]any)
	if len(sup) != 1 || sup[0] != revisionMCP {
		t.Errorf("supported = %v, attendu [%s]", data["supported"], revisionMCP)
	}
}

// Une notification ne peut porter qu'une méthode de notification. Un
// `tools/call` sans id acquitté en 202 serait une écriture silencieusement
// perdue dès que S3 branchera les outils.
func TestMCPRequeteSansIdRefuseeAuLieuDEtreAcquittee(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	envoie := func(methode string, params map[string]any) *http.Response {
		if params == nil {
			params = map[string]any{}
		}
		params["_meta"] = map[string]any{champMetaVersion: revisionMCP}
		brut, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": methode, "params": params})
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(brut))
		req.Header.Set("Authorization", "Bearer "+jetons["colin"])
		req.Header.Set("MCP-Protocol-Version", revisionMCP)
		req.Header.Set("Mcp-Method", methode)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	for _, m := range []string{"tools/call", "tools/list", "server/discover"} {
		resp := envoie(m, map[string]any{"name": "arborescence"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s sans id : %d, attendu 400 (et surtout pas 202)", m, resp.StatusCode)
		}
	}
	// Une vraie notification reste acquittée.
	if resp := envoie("notifications/quelque_chose", nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("notifications/* : %d, attendu 202", resp.StatusCode)
	}
}

// Toute réponse d'erreur porte un `id`, valant `null` quand la requête n'a pas
// permis de le lire. JSON-RPC 2.0 §5 : un client à parseur strict rejette une
// enveloppe sans ce champ.
func TestMCPToutesLesErreursPortentUnId(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	brutJSON := func(corps string, entetes map[string]string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader([]byte(corps)))
		for k, v := range entetes {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	avecJeton := map[string]string{"Authorization": "Bearer " + jetons["colin"]}

	cas := []struct {
		nom     string
		resp    *http.Response
		attendu any // l'id attendu : nil pour `null`, float64(7) pour un id récupéré
	}{
		{"jeton manquant", brutJSON(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil), nil},
		{"corps illisible", brutJSON(`{ pas du json`, avecJeton), nil},
		{"lot JSON-RPC", brutJSON(`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`, avecJeton), nil},
		{"objet en trop après le premier", brutJSON(`{"jsonrpc":"2.0","id":1,"method":"tools/list"} n_importe_quoi`, avecJeton), nil},
		// L'id est RÉCUPÉRÉ même quand params a la mauvaise forme : sans ça, le
		// client ne peut pas rattacher le refus à son appel.
		{"params mal typé", brutJSON(`{"jsonrpc":"2.0","id":7,"method":"tools/list","params":[]}`, avecJeton), float64(7)},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			m := corpsMCP(t, c.resp)
			if _, present := m["id"]; !present {
				t.Fatalf("aucun champ id dans la réponse d'erreur : %v", m)
			}
			if m["id"] != c.attendu {
				t.Errorf("id = %v (%T), attendu %v", m["id"], m["id"], c.attendu)
			}
			if _, ok := m["error"].(map[string]any); !ok {
				t.Errorf("aucune erreur JSON-RPC : %v", m)
			}
		})
	}
}

// Un en-tête miroir non validé est très exactement le risque que la validation
// existe pour fermer : un intermédiaire routerait sur une valeur que le
// serveur n'a jamais vérifiée.
func TestMCPMcpNamePresentSurUneMethodeQuiNEnPortePasEstRefuse(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	resp := appelMCP(t, srv, jetons["colin"], "tools/list", nil, func(r *http.Request) {
		r.Header.Set("Mcp-Name", "mensonge-total")
	})
	if resp.StatusCode != http.StatusBadRequest || codeErreurMCP(t, resp) != codeHeaderMismatch {
		t.Errorf("Mcp-Name sur tools/list : %d / %d, attendu 400 / %d",
			resp.StatusCode, codeErreurMCP(t, resp), codeHeaderMismatch)
	}
}

// `dernier_usage` n'avance que sur un appel ACCEPTÉ. Ce n'est pas qu'une
// question de vocabulaire : `db.Open` fixe SetMaxOpenConns(1), donc chaque
// écriture prend l'unique connexion partagée avec le web admin et les PUT.
// Toucher avant validation laisserait un porteur sérialiser les écritures de
// tout le serveur avec du bruit malformé.
func TestMCPDernierUsageNAvancePasSurUnAppelRejete(t *testing.T) {
	srv, database, ids, jetons := setupMCP(t)

	resp := appelMCP(t, srv, jetons["colin"], "tools/list", nil, func(r *http.Request) {
		r.Header.Del("MCP-Protocol-Version")
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("préparation : %d, attendu 400", resp.StatusCode)
	}
	apres, err := database.ListJetonsMCP(ids["colin"])
	if err != nil {
		t.Fatal(err)
	}
	if apres[0].DernierUsage != "" {
		t.Errorf("dernier_usage a avancé sur un appel rejeté : %q", apres[0].DernierUsage)
	}
}

// Le résidu après le premier objet JSON. Mesuré : `json.Decoder.More()` rend
// `false` devant un `}` ou un `]` en trop, donc `{...}}` passait en 200 - alors
// que `json.Unmarshal` sur les MÊMES octets le refuse. Le serveur acceptait un
// document que son propre analyseur juge invalide.
//
// Le premier banc ne l'attrapait que par le hasard d'avoir choisi
// « n_importe_quoi » comme rebut : un seul caractère différent inversait le
// résultat.
func TestMCPResiduApresLObjetEstRefuse(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	base := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"` + champMetaVersion + `":"` + revisionMCP + `"}}}`
	rebuts := []struct {
		nom   string
		corps string
	}{
		{"accolade en trop", base + "}"},
		{"crochet en trop", base + "]"},
		{"objet en trop", base + ` {"x":1}`},
		{"texte en trop", base + " n_importe_quoi"},
		{"virgule en trop", base + ","},
	}
	for _, c := range rebuts {
		t.Run(c.nom, func(t *testing.T) {
			req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader([]byte(c.corps)))
			req.Header.Set("Authorization", "Bearer "+jetons["colin"])
			req.Header.Set("MCP-Protocol-Version", revisionMCP)
			req.Header.Set("Mcp-Method", "tools/list")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s : %d, attendu 400", c.nom, resp.StatusCode)
			}
		})
	}
}

// Une méthode qu'on ne sert PAS garde son 404, même quand le client y joint un
// `Mcp-Name` parfaitement conforme.
//
// C'est une régression que le premier correctif avait introduite : il refusait
// l'en-tête sur toute méthode autre que `tools/call`, donc sur `resources/read`
// et `prompts/get` - où la spec l'EXIGE. Un client conforme recevait -32020 là
// où le contrat promet 404 / -32601. Le test d'origine ne l'attrapait pas
// parce qu'il ne posait jamais l'en-tête.
func TestMCPMethodeNonServieGardeSon404MemeAvecMcpName(t *testing.T) {
	srv, _, _, jetons := setupMCP(t)

	cas := []struct{ methode, nom string }{
		{"resources/read", "file:///un/chemin"},
		{"prompts/get", "un_prompt"},
	}
	for _, c := range cas {
		t.Run(c.methode, func(t *testing.T) {
			resp := appelMCP(t, srv, jetons["colin"], c.methode,
				map[string]any{"name": c.nom},
				func(r *http.Request) { r.Header.Set("Mcp-Name", c.nom) })
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s avec Mcp-Name conforme : %d, attendu 404", c.methode, resp.StatusCode)
			}
			if code := codeErreurMCP(t, resp); code != codeMethodeInconnue {
				t.Errorf("%s : code %d, attendu %d", c.methode, code, codeMethodeInconnue)
			}
		})
	}
}

// Une notification fait AUSSI avancer `dernier_usage` : c'est le seul chemin où
// le serveur répond explicitement « accepté ». L'oublier ferait paraître
// « jamais servi » un jeton qui travaille, ce qui annule la raison d'être de la
// colonne - repérer un jeton oublié dans un fichier de config.
func TestMCPUneNotificationFaitAvancerDernierUsage(t *testing.T) {
	srv, database, ids, jetons := setupMCP(t)

	corps := map[string]any{
		"jsonrpc": "2.0", "method": "notifications/quelque_chose",
		"params": map[string]any{"_meta": map[string]any{champMetaVersion: revisionMCP}},
	}
	brut, _ := json.Marshal(corps)
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(brut))
	req.Header.Set("Authorization", "Bearer "+jetons["colin"])
	req.Header.Set("MCP-Protocol-Version", revisionMCP)
	req.Header.Set("Mcp-Method", "notifications/quelque_chose")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification : %d, attendu 202", resp.StatusCode)
	}

	apres, _ := database.ListJetonsMCP(ids["colin"])
	if apres[0].DernierUsage == "" {
		t.Error("dernier_usage n'a pas avancé sur le seul chemin qui répond « accepté »")
	}
}
