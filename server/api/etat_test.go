package api

// Le banc de la porte `POST /etat` (DAR-198, slice 1).
//
// L'ASSERTION QUI COMPTE EST LA TROISIEME : un poste qui n'a jamais parlé doit
// ressortir de `EtatsPostes`. Elle est écrite pour TOMBER si la jointure passe
// de LEFT à INNER - vérifié par mutation, pas supposé. Sans elle, un poste muet
// disparaît de la liste et son silence se lit comme « rien à signaler ».

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// setupEtat : un serveur, la base, et DEUX postes pour le même compte - dont un
// qui ne parlera jamais. Deux postes pour un humain n'est pas une commodité de
// test : c'est le cas réel (trois jetons pour `colin` en production).
func setupEtat(t *testing.T) (*httptest.Server, *db.DB, map[string]string) {
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
	store.Write("shared/note.md", "note", "colin")

	colin, err := database.CreateUser("colin", "pw", perms.Ecriture, true)
	if err != nil {
		t.Fatalf("CreateUser : %v", err)
	}
	jetons := map[string]string{}
	jetons["portable"], _ = database.IssueToken(colin.ID, "portable")
	jetons["fixe"], _ = database.IssueToken(colin.ID, "fixe")

	fixed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer((&Server{DB: database, Store: store,
		Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)
	return srv, database, jetons
}

func posteParle(t *testing.T, srv *httptest.Server, jeton string, corps any) int {
	t.Helper()
	body, _ := json.Marshal(corps)
	req, _ := http.NewRequest("POST", srv.URL+"/etat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jeton)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /etat : %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestEtatPosteEnregistre(t *testing.T) {
	srv, database, jetons := setupEtat(t)

	code := posteParle(t, srv, jetons["portable"], etatRequest{
		Tete:  "abc123",
		Cycle: "2026-09-01T11:59:00Z",
		Laisses: []db.LaissePoste{
			{Chemin: "shared/gros.md", Raison: "un dossier en travers du chemin"},
		},
		ConflitsLocaux: []string{"shared/note (conflit local).md"},
	})
	if code != http.StatusNoContent {
		t.Fatalf("POST /etat = %d, attendu 204", code)
	}

	p, err := database.EtatPosteParHash(hashDe(t, database, jetons["portable"]))
	if err != nil {
		t.Fatalf("EtatPosteParHash : %v", err)
	}
	if !p.AParle() {
		t.Error("le poste vient de parler, AParle() rend false")
	}
	if p.Tete != "abc123" {
		t.Errorf("tête = %q, attendu abc123", p.Tete)
	}
	// L'horloge du SERVEUR fait foi, pas celle déclarée par le poste.
	if p.RecuLe != "2026-09-01T12:00:00Z" {
		t.Errorf("recu_le = %q, attendu l'horloge serveur 2026-09-01T12:00:00Z", p.RecuLe)
	}
	if p.Cycle != "2026-09-01T11:59:00Z" {
		t.Errorf("cycle = %q, attendu l'horloge du poste", p.Cycle)
	}
	if len(p.Exceptions.Laisses) != 1 || p.Exceptions.Laisses[0].Raison != "un dossier en travers du chemin" {
		t.Errorf("laisses = %+v, le motif doit survivre à l'aller-retour", p.Exceptions.Laisses)
	}
	if len(p.Exceptions.ConflitsLocaux) != 1 {
		t.Errorf("exceptions = %+v, les deux listes doivent survivre", p.Exceptions)
	}
}

// TestEtatPosteMuetResteVisible : LA garde de la slice.
//
// Mutation de contrôle : passer le LEFT JOIN d'`EtatsPostes` en INNER JOIN.
// Ce test doit alors tomber sur « 1 poste, attendu 2 ».
func TestEtatPosteMuetResteVisible(t *testing.T) {
	srv, database, jetons := setupEtat(t)
	posteParle(t, srv, jetons["portable"], etatRequest{Tete: "abc123", Cycle: "2026-09-01T11:59:00Z"})

	postes, err := database.EtatsPostes()
	if err != nil {
		t.Fatalf("EtatsPostes : %v", err)
	}
	if len(postes) != 2 {
		t.Fatalf("%d poste(s), attendu 2 - un poste muet DOIT rester dans la liste", len(postes))
	}
	var muets, parlants int
	for _, p := range postes {
		if p.AParle() {
			parlants++
		} else {
			muets++
			if p.Libelle != "fixe" {
				t.Errorf("le poste muet est %q, attendu « fixe »", p.Libelle)
			}
		}
	}
	if muets != 1 || parlants != 1 {
		t.Errorf("%d muet(s) et %d parlant(s), attendu 1 et 1", muets, parlants)
	}
}

func TestEtatPosteJetonInvalide(t *testing.T) {
	srv, _, _ := setupEtat(t)
	if code := posteParle(t, srv, "jeton-qui-n-existe-pas", etatRequest{Tete: "abc"}); code != http.StatusUnauthorized {
		t.Errorf("POST /etat avec un jeton inconnu = %d, attendu 401", code)
	}
}

// TestEtatPosteRemplace : un second cycle écrase le premier. Sans le
// ON CONFLICT, l'INSERT échouerait sur la clé primaire et le poste resterait
// figé sur son premier cycle - un écran à jour affichant un état périmé.
func TestEtatPosteRemplace(t *testing.T) {
	srv, database, jetons := setupEtat(t)
	posteParle(t, srv, jetons["portable"], etatRequest{Tete: "vieux", Cycle: "2026-09-01T10:00:00Z",
		ConflitsLocaux: []string{"shared/note (conflit local).md"}})
	if code := posteParle(t, srv, jetons["portable"], etatRequest{Tete: "neuf", Cycle: "2026-09-01T11:00:00Z"}); code != http.StatusNoContent {
		t.Fatalf("second POST = %d, attendu 204", code)
	}
	p, err := database.EtatPosteParHash(hashDe(t, database, jetons["portable"]))
	if err != nil {
		t.Fatalf("EtatPosteParHash : %v", err)
	}
	if p.Tete != "neuf" {
		t.Errorf("tête = %q, attendu neuf", p.Tete)
	}
	if len(p.Exceptions.ConflitsLocaux) != 0 {
		t.Errorf("conflits locaux = %v, le second cycle n'en déclarait aucun : "+
			"une exception résolue doit disparaître, pas s'accumuler", p.Exceptions.ConflitsLocaux)
	}
}

// TestEtatPosteCascade : révoquer le poste efface ce qu'il a dit. La cascade ne
// part QUE si la connexion a PRAGMA foreign_keys - désactivé par défaut, et
// réglé par connexion (https://www.sqlite.org/foreignkeys.html). `db.Open`
// l'active ; ce test est ce qui le prouve.
func TestEtatPosteCascade(t *testing.T) {
	srv, database, jetons := setupEtat(t)
	posteParle(t, srv, jetons["portable"], etatRequest{Tete: "abc", Cycle: "2026-09-01T11:00:00Z"})
	hash := hashDe(t, database, jetons["portable"])

	if err := database.RevokeToken(jetons["portable"]); err != nil {
		t.Fatalf("RevokeToken : %v", err)
	}
	if _, err := database.EtatPosteParHash(hash); err != db.ErrPosteInconnu {
		t.Errorf("après révocation, EtatPosteParHash rend %v, attendu ErrPosteInconnu - "+
			"la ligne d'état a survécu au jeton, donc la cascade n'a pas joué", err)
	}
	postes, _ := database.EtatsPostes()
	if len(postes) != 1 {
		t.Errorf("%d poste(s) après révocation, attendu 1", len(postes))
	}
}
