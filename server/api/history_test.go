package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func apiGet(t *testing.T, srv *httptest.Server, token, path string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s : %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func apiPost(t *testing.T, srv *httptest.Server, token, path string, payload any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s : %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestLogEtPerimetre(t *testing.T) {
	srv, tokens := setup(t)

	// colin voit l'historique de son fichier.
	resp, body := apiGet(t, srv, tokens["colin"], "/log/clients/vdf/secret.md")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log : attendu 200, obtenu %d", resp.StatusCode)
	}
	var lr struct {
		Commits []logCommit `json:"commits"`
	}
	json.Unmarshal(body, &lr)
	if len(lr.Commits) != 1 || lr.Commits[0].Author != "colin" || lr.Commits[0].Deleted {
		t.Errorf("log inattendu : %+v", lr.Commits)
	}

	// achille : hors droit = 404, indistinguable de l'inexistant.
	respCache, _ := apiGet(t, srv, tokens["achille"], "/log/clients/vdf/secret.md")
	respAbsent, _ := apiGet(t, srv, tokens["achille"], "/log/clients/nexiste-pas.md")
	if respCache.StatusCode != http.StatusNotFound || respAbsent.StatusCode != http.StatusNotFound {
		t.Errorf("attendu 404/404, obtenu %d/%d", respCache.StatusCode, respAbsent.StatusCode)
	}
}

func TestRestoreVersionAnterieure(t *testing.T) {
	srv, tokens := setup(t)

	// colin écrit v2 par-dessus le contenu initial, puis restaure v1.
	put(t, srv, tokens["colin"], "equipe/doc.md", "v1", headCursor(t, srv, tokens["colin"]))
	put(t, srv, tokens["colin"], "equipe/doc.md", "v2", headCursor(t, srv, tokens["colin"]))

	_, body := apiGet(t, srv, tokens["colin"], "/log/equipe/doc.md")
	var lr struct {
		Commits []logCommit `json:"commits"`
	}
	json.Unmarshal(body, &lr)
	if len(lr.Commits) != 2 {
		t.Fatalf("attendu 2 commits, obtenu %+v", lr.Commits)
	}
	oidV1 := lr.Commits[1].OID

	code, out := apiPost(t, srv, tokens["colin"], "/restore/equipe/doc.md", map[string]string{"rev": oidV1})
	if code != http.StatusOK || out["head"] == "" {
		t.Fatalf("restore : attendu 200+head, obtenu %d %v", code, out)
	}
	resp, body := apiGet(t, srv, tokens["colin"], "/files/equipe/doc.md")
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"content":"v1"`)) {
		t.Errorf("contenu non restauré : %s", body)
	}
	// La restauration est un commit de plus, pas une réécriture.
	_, body = apiGet(t, srv, tokens["colin"], "/log/equipe/doc.md")
	json.Unmarshal(body, &lr)
	if len(lr.Commits) != 3 {
		t.Errorf("attendu 3 commits après restore, obtenu %d", len(lr.Commits))
	}
}

func TestRestoreSuppressionEtDroits(t *testing.T) {
	srv, tokens := setup(t)

	// Créer, supprimer, puis restaurer depuis l'historique : le fichier revit.
	put(t, srv, tokens["colin"], "equipe/tmp.md", "précieux", "")
	head := headCursor(t, srv, tokens["colin"])
	req, _ := http.NewRequest("DELETE", srv.URL+"/files/equipe/tmp.md?base_oid="+head, nil)
	req.Header.Set("Authorization", "Bearer "+tokens["colin"])
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	_, body := apiGet(t, srv, tokens["colin"], "/log/equipe/tmp.md")
	var lr struct {
		Commits []logCommit `json:"commits"`
	}
	json.Unmarshal(body, &lr)
	if len(lr.Commits) != 2 || !lr.Commits[0].Deleted {
		t.Fatalf("attendu [suppression, création], obtenu %+v", lr.Commits)
	}

	// Restaurer depuis le commit de suppression échoue proprement (tombstone)...
	code, _ := apiPost(t, srv, tokens["colin"], "/restore/equipe/tmp.md", map[string]string{"rev": lr.Commits[0].OID})
	if code != http.StatusBadRequest {
		t.Errorf("restore d'un tombstone : attendu 400, obtenu %d", code)
	}
	// ... depuis le commit de création, le fichier revit.
	code, _ = apiPost(t, srv, tokens["colin"], "/restore/equipe/tmp.md", map[string]string{"rev": lr.Commits[1].OID})
	if code != http.StatusOK {
		t.Fatalf("restore : attendu 200, obtenu %d", code)
	}
	resp2, body := apiGet(t, srv, tokens["colin"], "/files/equipe/tmp.md")
	if resp2.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("précieux")) {
		t.Errorf("fichier non ressuscité : %s", body)
	}

	// achille (lecture seule sur public) ne peut pas restaurer.
	put(t, srv, tokens["colin"], "public/page.md", "v2", "")
	_, body = apiGet(t, srv, tokens["achille"], "/log/public/page.md")
	json.Unmarshal(body, &lr)
	code, _ = apiPost(t, srv, tokens["achille"], "/restore/public/page.md", map[string]string{"rev": lr.Commits[0].OID})
	if code != http.StatusForbidden {
		t.Errorf("restore sans droit d'écriture : attendu 403, obtenu %d", code)
	}
}

func TestExportZIPFiltre(t *testing.T) {
	srv, tokens := setup(t)

	lire := func(token string) map[string]string {
		resp, body := apiGet(t, srv, token, "/export")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("export : attendu 200, obtenu %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
			t.Errorf("Content-Type : %q", ct)
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("ZIP invalide : %v", err)
		}
		out := map[string]string{}
		for _, f := range zr.File {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			out[f.Name] = string(b)
		}
		return out
	}

	// Admin : tout le dépôt.
	adminFiles := lire(tokens["colin"])
	for _, attendu := range []string{"public/guide.md", "clients/vdf/secret.md", "prive/interne.md"} {
		if _, ok := adminFiles[attendu]; !ok {
			t.Errorf("export admin : %s absent", attendu)
		}
	}

	// Membre : son périmètre seulement, contenu intact.
	achilleFiles := lire(tokens["achille"])
	if achilleFiles["public/guide.md"] != "# Guide" {
		t.Errorf("export membre : contenu de public/guide.md inattendu : %q", achilleFiles["public/guide.md"])
	}
	for interdit := range map[string]bool{"clients/vdf/secret.md": true, "prive/interne.md": true} {
		if _, ok := achilleFiles[interdit]; ok {
			t.Errorf("export membre : %s hors périmètre présent", interdit)
		}
	}
}

func TestLogRepertoireEtRevVide(t *testing.T) {
	srv, tokens := setup(t)

	// /log sur un répertoire = 404 (pas d'historique propre, pas de fuite des
	// chemins enfants hors périmètre - achille ne voit pas clients/vdf).
	for _, token := range []string{tokens["colin"], tokens["achille"]} {
		if resp, _ := apiGet(t, srv, token, "/log/clients"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("/log/clients : attendu 404, obtenu %d", resp.StatusCode)
		}
	}

	// restore avec rev vide = 400 (sinon commit fantôme du head).
	code, _ := apiPost(t, srv, tokens["colin"], "/restore/public/guide.md", map[string]string{"rev": ""})
	if code != http.StatusBadRequest {
		t.Errorf("restore rev vide : attendu 400, obtenu %d", code)
	}

	// restore hors lecture = 404 (existence jamais révélée).
	code, _ = apiPost(t, srv, tokens["achille"], "/restore/prive/interne.md", map[string]string{"rev": "abcdef1234567890abcdef1234567890abcdef12"})
	if code != http.StatusNotFound {
		t.Errorf("restore hors lecture : attendu 404, obtenu %d", code)
	}
}
