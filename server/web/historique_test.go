package web

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestHistoriqueEtRestauration(t *testing.T) {
	s := newServerAvecContenu(t)
	s.Store.Write("notes/idees.md", "version 2", "colin") // 2e version
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// La page liste les deux versions avec leurs auteurs.
	rec := get(h, "/admin/historique/notes/idees.md", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("historique : attendu 200, obtenu %d", rec.Code)
	}
	if got := strings.Count(rec.Body.String(), "Restaurer cette version"); got != 2 {
		t.Errorf("attendu 2 boutons Restaurer, obtenu %d", got)
	}

	// Restaurer la première version (la plus ancienne).
	log, _ := s.Store.Log("notes/idees.md")
	if len(log) != 2 {
		t.Fatalf("log attendu à 2, obtenu %+v", log)
	}
	rec = postForm(h, "/admin/restaurer/notes/idees.md", url.Values{"rev": {log[1].OID}}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restaurer : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	contenu, _ := s.Store.Read("notes/idees.md", "")
	if contenu != "contenu idées" {
		t.Errorf("contenu non restauré : %q", contenu)
	}
	// Le commit de restauration est visible et tracé.
	log, _ = s.Store.Log("notes/idees.md")
	if len(log) != 3 || !strings.HasPrefix(log[0].Message, "restore notes/idees.md @") {
		t.Errorf("commit de restore absent ou non tracé : %+v", log)
	}

	// rev vide → 400 avec message.
	if rec := postForm(h, "/admin/restaurer/notes/idees.md", url.Values{"rev": {""}}, c); rec.Code != http.StatusBadRequest {
		t.Errorf("rev vide : attendu 400, obtenu %d", rec.Code)
	}
}

func TestHistoriqueRespecteLesDroits(t *testing.T) {
	s := newServerAvecContenu(t)
	h := s.Handler()
	c := login(t, h, "achille", "mdp")

	// Lecture seule : la page s'affiche sans bouton Restaurer, le POST → 403.
	rec := get(h, "/admin/historique/notes/idees.md", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("historique lecteur : attendu 200, obtenu %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Restaurer cette version") {
		t.Error("bouton Restaurer visible sans droit d'écriture")
	}
	log, _ := s.Store.Log("notes/idees.md")
	if rec := postForm(h, "/admin/restaurer/notes/idees.md", url.Values{"rev": {log[0].OID}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("restaurer sans écriture : attendu 403, obtenu %d", rec.Code)
	}

	// Hors périmètre : 404 indistinguable, historique comme restauration.
	cache := get(h, "/admin/historique/clients/vdf/secret.md", c)
	absent := get(h, "/admin/historique/clients/nexiste-pas.md", c)
	if cache.Code != http.StatusNotFound || absent.Code != http.StatusNotFound || cache.Body.String() != absent.Body.String() {
		t.Errorf("historique caché : attendu 404 indistinguable, obtenu %d/%d", cache.Code, absent.Code)
	}
	// Un répertoire n'a pas d'historique propre.
	if rec := get(h, "/admin/historique/clients", c); rec.Code != http.StatusNotFound {
		t.Errorf("historique d'un répertoire : attendu 404, obtenu %d", rec.Code)
	}
}

func TestExportZipWeb(t *testing.T) {
	s := newServerAvecContenu(t)
	h := s.Handler()

	lire := func(c *http.Cookie) map[string]bool {
		rec := get(h, "/admin/export.zip", c)
		if rec.Code != http.StatusOK {
			t.Fatalf("export : attendu 200, obtenu %d", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
			t.Errorf("Content-Type : %q", ct)
		}
		zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		if err != nil {
			t.Fatalf("ZIP invalide : %v", err)
		}
		out := map[string]bool{}
		for _, f := range zr.File {
			out[f.Name] = true
		}
		return out
	}

	// UN MEMBRE N'EXPORTE PLUS RIEN (Colin, 01/09). L'export servait deux
	// publics avec deux règles - le périmètre du lecteur pour un membre, tout le
	// dépôt pour un admin - et cette double nature faisait lire son exemption
	// admin comme une fuite. C'est un outil d'administration, il est réservé aux
	// administrateurs. La porte API `GET /export` n'est pas touchée.
	if rec := get(h, "/admin/export.zip", login(t, h, "achille", "mdp")); rec.Code != http.StatusForbidden {
		t.Errorf("export par un membre : attendu 403, obtenu %d", rec.Code)
	}

	// Admin : tout, hors skills des autres comptes.
	fichiers := lire(login(t, h, "colin", "mdp"))
	if !fichiers["clients/vdf/secret.md"] {
		t.Error("export admin : dépôt complet attendu")
	}
	if !fichiers["notes/idees.md"] {
		t.Error("export admin : fichiers ordinaires attendus")
	}
}

func TestHistoriqueCheminsImpossibles(t *testing.T) {
	s := newServerAvecContenu(t)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// Chemin vide, caractère de contrôle, échappement racine : 404 (jamais 500),
	// indistinguables d'un fichier inexistant.
	reference := get(h, "/admin/historique/nexiste-pas.md", c)
	for _, chemin := range []string{
		"/admin/historique/",
		"/admin/historique/foo%01.md",
		"/admin/historique/..%2F..%2Fetc",
	} {
		rec := get(h, chemin, c)
		if rec.Code != http.StatusNotFound || rec.Body.String() != reference.Body.String() {
			t.Errorf("%s : attendu 404 indistinguable, obtenu %d", chemin, rec.Code)
		}
	}
}

func TestHistoriqueSuppressionSansBouton(t *testing.T) {
	s := newServerAvecContenu(t)
	s.Store.Delete("notes/idees.md", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rec := get(h, "/admin/historique/notes/idees.md", c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "suppression") {
		t.Fatalf("historique après suppression : attendu 200 avec « suppression », obtenu %d", rec.Code)
	}
	// 2 versions, mais un seul bouton : pas de restore depuis le tombstone.
	if got := strings.Count(rec.Body.String(), "Restaurer cette version"); got != 1 {
		t.Errorf("attendu 1 bouton Restaurer (pas sur la suppression), obtenu %d", got)
	}
	// POST avec le rev du tombstone → 400 avec message, rien de commité.
	log, _ := s.Store.Log("notes/idees.md")
	rec = postForm(h, "/admin/restaurer/notes/idees.md", url.Values{"rev": {log[0].OID}}, c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ne contient pas le fichier") {
		t.Errorf("restore d'un tombstone : attendu 400 avec message, obtenu %d", rec.Code)
	}
	// Restaurer la version d'avant la suppression : le fichier revit.
	rec = postForm(h, "/admin/restaurer/notes/idees.md", url.Values{"rev": {log[1].OID}}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("résurrection : attendu 303, obtenu %d", rec.Code)
	}
	if contenu, err := s.Store.Read("notes/idees.md", ""); err != nil || contenu != "contenu idées" {
		t.Errorf("fichier non ressuscité : %q (err %v)", contenu, err)
	}
}

func TestHistoriqueLiensEchappes(t *testing.T) {
	s := newServerAvecContenu(t)
	s.Store.Write("notes/plan#1.md", "x", "colin")
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// La page fichier pointe vers l'historique avec le nom échappé, et la page
	// historique poste vers /admin/restaurer avec le même échappement.
	rec := get(h, "/admin/dossiers/notes/plan%231.md", c)
	if !strings.Contains(rec.Body.String(), "/admin/historique/notes/plan%231.md") {
		t.Error("lien historique non échappé sur la page fichier")
	}
	rec = get(h, "/admin/historique/notes/plan%231.md", c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/admin/restaurer/notes/plan%231.md") {
		t.Errorf("action restaurer non échappée (%d)", rec.Code)
	}
}
