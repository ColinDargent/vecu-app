package api

// Les routes fichiers acceptent un jeton MCP, plafond compris.
//
// Pourquoi cette porte existe : une routine cloud est un PROGRAMME, pas un
// agent. Lui faire payer une enveloppe JSON-RPC, des en-têtes miroirs et un
// `tools/call` pour ce qui est un PUT n'a pas de sens. Mais elle doit garder
// un jeton révocable, plafonnable et traçable - ce que le jeton d'appareil
// n'offre pas (ni plafond, ni dernier usage, ni écran).
//
// LE CRITÈRE DE CETTE PORTE : le plafond s'applique ici EXACTEMENT comme sur
// `/mcp`. Sans ça, un jeton « lecture » écrirait en changeant simplement de
// chemin, et la propriété centrale du jalon tomberait.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// requeteFichier : la forme utile, avec le serveur de test.
func requeteFichier(t *testing.T, url, methode, jeton, chemin, corps string) (int, string) {
	t.Helper()
	var body *strings.Reader
	if corps == "" {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(corps)
	}
	req, err := http.NewRequest(methode, url+"/files/"+chemin, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+jeton)
	req.Header.Set("Content-Type", "application/json")
	resp, err := clientSansRedirection.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func corpsPut(contenu, base string) string {
	b, _ := json.Marshal(map[string]string{"content": contenu, "base_oid": base})
	return string(b)
}

// LE CRITÈRE. Un jeton plafonné « lecture » lit par l'API et n'écrit pas,
// alors que son compte est admin en écriture partout.
func TestApiUnJetonMCPPlafonneLitMaisNEcritPas(t *testing.T) {
	srv, _, _, _, mcp, _ := setupEcriture(t)

	// Il LIT.
	code, corps := requeteFichier(t, srv.URL, "GET", mcp["colin-lecture"], "equipe/README.md", "")
	if code != http.StatusOK {
		t.Fatalf("lecture : %d (%s)", code, corps)
	}
	if !strings.Contains(corps, "# equipe") {
		t.Errorf("contenu inattendu : %s", corps)
	}

	// IL N'ÉCRIT PAS.
	code, corps = requeteFichier(t, srv.URL, "PUT", mcp["colin-lecture"],
		"equipe/interdit.md", corpsPut("non\n", "@head"))
	if code != http.StatusForbidden {
		t.Errorf("écriture par un jeton plafonné : %d, attendu 403 (%s)", code, corps)
	}

	// IL NE SUPPRIME PAS.
	code, _ = requeteFichier(t, srv.URL, "DELETE", mcp["colin-lecture"], "equipe/README.md", "")
	if code != http.StatusForbidden {
		t.Errorf("suppression par un jeton plafonné : %d, attendu 403", code)
	}

	// Et le MÊME compte, par un jeton non plafonné, écrit bien : c'est le
	// plafond qui bloque, pas les droits.
	if code, corps := requeteFichier(t, srv.URL, "PUT", mcp["colin"],
		"equipe/permis.md", corpsPut("oui\n", "@head")); code != http.StatusOK {
		t.Errorf("jeton écriture : %d (%s)", code, corps)
	}
}

// Un jeton MCP écrit par l'API, et le résultat est celui du chemin partagé.
func TestApiUnJetonMCPEcritEtLitCommeUnPoste(t *testing.T) {
	srv, _, store, _, mcp, _ := setupEcriture(t)

	code, corps := requeteFichier(t, srv.URL, "PUT", mcp["colin"],
		"knowledge/meetings/2026-08-31-test.md", corpsPut("# Compte rendu\n", "@head"))
	if code != http.StatusOK {
		t.Fatalf("écriture : %d (%s)", code, corps)
	}
	if c, _ := store.Read("knowledge/meetings/2026-08-31-test.md", ""); c != "# Compte rendu\n" {
		t.Errorf("contenu écrit : %q", c)
	}

	// Relecture par la même porte.
	code, corps = requeteFichier(t, srv.URL, "GET", mcp["colin"], "knowledge/meetings/2026-08-31-test.md", "")
	if code != http.StatusOK || !strings.Contains(corps, "Compte rendu") {
		t.Errorf("relecture : %d (%s)", code, corps)
	}

	// Suppression.
	if code, _ := requeteFichier(t, srv.URL, "DELETE", mcp["colin"], "knowledge/meetings/2026-08-31-test.md", ""); code != http.StatusOK {
		t.Errorf("suppression : %d", code)
	}
	if store.Exists("knowledge/meetings/2026-08-31-test.md") {
		t.Error("le fichier existe encore")
	}
}

// LE PÉRIMÈTRE. Un jeton MCP n'ouvre QUE les routes fichiers.
//
// Élargir `s.auth` en bloc aurait donné accès à `/export` (dont le ZIP a un
// court-circuit admin), `/sync`, `/log`, `/restore`, `/skills` et
// `/session-web` - toutes des surfaces où le plafond n'est pas câblé.
func TestApiUnJetonMCPNOuvrePasLesAutresRoutes(t *testing.T) {
	srv, _, _, _, mcp, dev := setupEcriture(t)

	fermees := []struct{ methode, chemin string }{
		{"GET", "/tree"},
		{"GET", "/sync"},
		{"GET", "/espaces"},
		{"GET", "/export"},
		{"GET", "/skills"},
		{"GET", "/log/equipe/README.md"},
		{"POST", "/restore/equipe/README.md"},
		{"POST", "/session-web"},
		{"GET", "/app/manifest"},
	}
	for _, c := range fermees {
		t.Run(c.methode+" "+c.chemin, func(t *testing.T) {
			req, _ := http.NewRequest(c.methode, srv.URL+c.chemin, strings.NewReader(""))
			req.Header.Set("Authorization", "Bearer "+mcp["colin"])
			resp, err := clientSansRedirection.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("un jeton MCP a été accepté sur %s %s : %d", c.methode, c.chemin, resp.StatusCode)
			}

			// TÉMOIN : la même route accepte bien un jeton d'appareil. Sans lui,
			// une route cassée passerait ce test.
			req2, _ := http.NewRequest(c.methode, srv.URL+c.chemin, strings.NewReader(""))
			req2.Header.Set("Authorization", "Bearer "+dev["colin"])
			resp2, err := clientSansRedirection.Do(req2)
			if err != nil {
				t.Fatal(err)
			}
			defer resp2.Body.Close()
			if resp2.StatusCode == http.StatusUnauthorized {
				t.Errorf("témoin : un jeton d'appareil est refusé sur %s %s", c.methode, c.chemin)
			}
		})
	}
}

// Un jeton révoqué est refusé sur les routes fichiers, au premier appel.
func TestApiUnJetonMCPRevoqueEstRefuseSurFiles(t *testing.T) {
	srv, database, _, ids, mcp, _ := setupEcriture(t)

	if code, _ := requeteFichier(t, srv.URL, "GET", mcp["colin"], "equipe/README.md", ""); code != http.StatusOK {
		t.Fatalf("avant révocation : %d", code)
	}
	liste, _ := database.ListJetonsMCP(ids["colin"])
	var id int64
	for _, j := range liste {
		if j.Libelle == "banc" {
			id = j.ID
		}
	}
	if err := database.RevoqueJetonMCPDuCompte(ids["colin"], id); err != nil {
		t.Fatal(err)
	}
	if code, _ := requeteFichier(t, srv.URL, "GET", mcp["colin"], "equipe/README.md", ""); code != http.StatusUnauthorized {
		t.Errorf("après révocation, premier appel : %d, attendu 401", code)
	}
}

// `dernier_usage` avance sur un appel API : sans ça, le jeton effectivement en
// production serait le seul à paraître mort.
func TestApiDernierUsageAvanceSurUnAppelFichier(t *testing.T) {
	srv, database, _, ids, mcp, _ := setupEcriture(t)

	avant, _ := database.ListJetonsMCP(ids["colin"])
	for _, j := range avant {
		if j.DernierUsage != "" {
			t.Fatalf("précondition : %q a déjà servi", j.Libelle)
		}
	}
	requeteFichier(t, srv.URL, "GET", mcp["colin"], "equipe/README.md", "")

	apres, _ := database.ListJetonsMCP(ids["colin"])
	var vu bool
	for _, j := range apres {
		if j.Libelle == "banc" && j.DernierUsage != "" {
			vu = true
		}
	}
	if !vu {
		t.Error("dernier_usage n'a pas avancé après un appel sur /files")
	}
}

// L'appropriation d'un skill reste FERMÉE par cette porte aussi : le refus
// porte sur le jeton, pas sur le protocole.
func TestApiUnJetonMCPNeRevendiquePasUnSkill(t *testing.T) {
	srv, database, store, _, mcp, dev := setupEcriture(t)

	const chemin = "shared/skills/nouveau/SKILL.md"
	code, _ := requeteFichier(t, srv.URL, "PUT", mcp["colin"], chemin, corpsPut("# Nouveau\n", "@head"))
	if code == http.StatusOK {
		t.Error("un jeton MCP a revendiqué un skill par l'API")
	}
	if store.Exists(chemin) {
		t.Error("le fichier du skill a été écrit")
	}
	if _, ok, _ := database.CreatorOf("nouveau"); ok {
		t.Error("un créateur a été enregistré")
	}

	// Par jeton d'APPAREIL, le même geste réussit : la divergence tient au
	// jeton, pas à la route.
	if code, corps := requeteFichier(t, srv.URL, "PUT", dev["colin"], chemin, corpsPut("# Nouveau\n", "")); code != http.StatusOK {
		t.Errorf("jeton d'appareil sur le même chemin : %d (%s)", code, corps)
	}
}

// Un jeton d'appareil se comporte EXACTEMENT comme avant sur les routes
// fichiers : le nouveau middleware ne doit rien changer pour les postes.
func TestApiUnJetonDAppareilInchangeSurFiles(t *testing.T) {
	srv, _, _, _, _, dev := setupEcriture(t)

	if code, corps := requeteFichier(t, srv.URL, "GET", dev["achille"], "equipe/README.md", ""); code != http.StatusOK {
		t.Errorf("lecture : %d (%s)", code, corps)
	}
	if code, _ := requeteFichier(t, srv.URL, "PUT", dev["achille"], "equipe/ok.md", corpsPut("x\n", "")); code != http.StatusOK {
		t.Errorf("écriture : %d", code)
	}
	// Et ses refus sont les mêmes.
	if code, _ := requeteFichier(t, srv.URL, "GET", dev["achille"], "prive/interne.md", ""); code != http.StatusNotFound {
		t.Errorf("chemin invisible : %d, attendu 404", code)
	}
	if code, _ := requeteFichier(t, srv.URL, "PUT", dev["achille"], "lecture-seule/x.md", corpsPut("x\n", "")); code != http.StatusForbidden {
		t.Errorf("dossier en lecture seule : %d, attendu 403", code)
	}
}
