package sync

// Le banc du rapport d'état (DAR-198, slice 2), de bout en bout : un vrai
// serveur, un vrai cycle, et la ligne relue côté base.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/auth"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// serveurAvecBase : comme `testServer`, mais rend aussi la base pour pouvoir
// relire ce que le poste a déclaré. `avale404` coupe la route /etat, pour jouer
// un client neuf devant un serveur qui ne la connaît pas encore.
func serveurAvecBase(t *testing.T, avale404 bool) (string, string, *db.DB) {
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
	tok, _ := database.IssueToken(u.ID, "portable")
	if _, err := store.Write("equipe/README.md", "# equipe\n", "colin"); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	h := (&api.Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if avale404 && r.URL.Path == "/etat" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, tok, database
}

func TestRapporteApresCycle(t *testing.T) {
	url, token, database := serveurAvecBase(t, false)
	e, _ := newEngine(t, url, token)

	if err := e.Cycle(); err != nil {
		t.Fatalf("Cycle : %v", err)
	}
	p, err := database.EtatPosteParHash(auth.HashToken(token))
	if err != nil {
		t.Fatalf("EtatPosteParHash : %v", err)
	}
	if !p.AParle() {
		t.Fatal("le poste a fait un cycle complet et le serveur n'a rien reçu")
	}
	if p.Tete == "" {
		t.Error("la tête rapportée est vide après un cycle qui a reçu du contenu")
	}
	if p.Tete != e.state.Head {
		t.Errorf("tête rapportée %q, le poste est à %q", p.Tete, e.state.Head)
	}
	if p.Libelle != "portable" {
		t.Errorf("libellé = %q, attendu « portable » - il vient de device_tokens", p.Libelle)
	}
}

// TestBinairesNeRemontentJamais : LA garde de la decision du 01/09.
//
// Un binaire pose dans un espace partage est ecarte par le poste et inscrit
// dans son registre `HorsPerimetre`. Le serveur ne l'a JAMAIS eu, et ne doit
// jamais en apprendre le nom : chez un client, le mainteneur y lirait les
// fichiers personnels que quelqu'un a laisses trainer.
//
// L'assertion porte sur DEUX surfaces, parce qu'une seule ne prouverait rien :
// le corps HTTP reellement envoye, et ce que la base finit par contenir.
//
// Mutation de controle : remettre un champ `hors_perimetre` dans `rapportEtat`
// et l'alimenter depuis `e.state.HorsPerimetre`.
func TestBinairesNeRemontentJamais(t *testing.T) {
	url, token, database := serveurAvecBase(t, false)

	// On intercale un mouchard qui lit le corps de CHAQUE POST /etat.
	var corps []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/etat" {
			b, _ := io.ReadAll(r.Body)
			corps = append(corps, string(b))
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		req, _ := http.NewRequest(r.Method, url+r.URL.String(), r.Body)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	e, dir := newEngine(t, proxy.URL, token)
	if err := e.Cycle(); err != nil {
		t.Fatalf("Cycle : %v", err)
	}
	// Un binaire depose dans l'espace, puis un second cycle : le poste le voit,
	// l'ecarte, et l'inscrit dans son registre.
	writeFile(t, dir, "equipe/photo.png", "\x89PNG\r\n\x1a\n\x00\x00binaire")
	if err := e.Cycle(); err != nil {
		t.Fatalf("second Cycle : %v", err)
	}
	if len(e.state.HorsPerimetre) == 0 {
		t.Fatal("le poste n'a pas ecarte le binaire : l'assertion ne pourrait pas echouer")
	}

	if len(corps) == 0 {
		t.Fatal("aucun POST /etat intercepte")
	}
	for i, c := range corps {
		if strings.Contains(c, "photo.png") {
			t.Errorf("corps %d : le nom du binaire part sur le reseau : %s", i, c)
		}
		if strings.Contains(c, "hors_perimetre") {
			t.Errorf("corps %d : le champ hors_perimetre est encore envoye : %s", i, c)
		}
	}
	p, err := database.EtatPosteParHash(auth.HashToken(token))
	if err != nil {
		t.Fatalf("EtatPosteParHash : %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", p.Exceptions), "photo.png") {
		t.Errorf("le nom du binaire est stocke en base : %+v", p.Exceptions)
	}
}

// TestRapportNeCassePasLeCycle : LA garde de la slice.
//
// Un client neuf devant un serveur sans la route reçoit 404 à chaque cycle.
// Si cette erreur remontait, la synchronisation d'un poste entier tomberait
// pour un renseignement d'affichage.
//
// Mutation de contrôle : remplacer le `e.logf(...)` d'appel dans `Cycle` par
// un `return err`. Ce test doit alors tomber sur « Cycle : POST /etat : HTTP 404 ».
func TestRapportNeCassePasLeCycle(t *testing.T) {
	url, token, database := serveurAvecBase(t, true)
	e, dir := newEngine(t, url, token)

	if err := e.Cycle(); err != nil {
		t.Fatalf("le cycle a échoué parce que le rapport a échoué : %v", err)
	}
	// Et le cycle a bien fait son travail : le contenu du serveur est arrivé.
	if _, err := readFileAbs(filepath.Join(dir, "equipe", "README.md")); err != nil {
		t.Errorf("le contenu n'est pas descendu alors que le cycle a réussi : %v", err)
	}
	p, err := database.EtatPosteParHash(auth.HashToken(token))
	if err != nil {
		t.Fatalf("EtatPosteParHash : %v", err)
	}
	if p.AParle() {
		t.Error("le serveur a enregistré un état alors que la route était coupée")
	}
}
