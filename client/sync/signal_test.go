package sync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAmorceNAnnonceRien : le piège de cette slice. Au premier cycle après la
// mise à jour, le registre est absent - donc les 24 skills déjà déposés
// sortiraient tous comme nouveaux. Chez Achille : « 24 nouveaux skills » d'un
// coup, c'est-à-dire exactement le rapport qui crie au loup que F6 a fermé le
// matin même.
func TestAmorceNAnnonceRien(t *testing.T) {
	e := moteurSignal(t, []NouveauSkill{
		{Slug: "revue-de-code", Auteur: "achille", CreeLe: "2026-07-28T00:00:00Z"},
		{Slug: "commit", Auteur: "achille", CreeLe: "2026-07-28T00:00:00Z"},
	})

	if err := e.Signale(); err != nil {
		t.Fatal(err)
	}
	if n := e.NouveauxSkills(); len(n) != 0 {
		t.Fatalf("l'amorce a annoncé %d skill(s) : %+v", len(n), n)
	}
	if len(e.state.SkillsVus) != 2 {
		t.Fatalf("le registre n'a pas été amorcé : %+v", e.state.SkillsVus)
	}

	// Un dépôt APRÈS l'amorce se dit, lui.
	e.client = clientFactice(t, append(distantsDe(e), NouveauSkill{Slug: "pdf", Auteur: "achille", CreeLe: "2026-08-20T10:00:00Z"}))
	if err := e.Signale(); err != nil {
		t.Fatal(err)
	}
	n := e.NouveauxSkills()
	if len(n) != 1 || n[0].Slug != "pdf" {
		t.Fatalf("le dépôt d'après l'amorce n'a pas été annoncé : %+v", n)
	}
}

// TestAmorceSurUnServeurSansSkills : le cas qui fait la différence entre `nil` et
// « vide ». Sans registre non nul, l'amorce se rejouerait à chaque cycle, et le
// PREMIER skill d'Achille entrerait au registre sans avoir jamais été annoncé -
// un signal avalé, sans trace.
func TestAmorceSurUnServeurSansSkills(t *testing.T) {
	e := moteurSignal(t, nil)
	if err := e.Signale(); err != nil {
		t.Fatal(err)
	}
	if e.state.SkillsVus == nil {
		t.Fatal("registre laissé nul après amorce : l'amorce se rejouera et avalera le premier signal")
	}

	// Relu du disque, comme au démarrage suivant : la distinction doit survivre
	// au passage par JSON.
	relu, err := LoadState(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if relu.SkillsVus == nil {
		t.Fatal("`omitempty` a effacé la distinction entre « amorcé » et « jamais amorcé »")
	}

	// Et le premier dépôt d'Achille se dit.
	e.state = relu
	e.client = clientFactice(t, []NouveauSkill{{Slug: "revue-de-code", Auteur: "achille", CreeLe: "2026-08-20T10:00:00Z"}})
	if err := e.Signale(); err != nil {
		t.Fatal(err)
	}
	if n := e.NouveauxSkills(); len(n) != 1 {
		t.Fatalf("le premier dépôt n'a pas été annoncé : %+v", n)
	}
}

// TestSignalNeSeRepeteEtSeMarque : le signal tient jusqu'à ce qu'on le lise (le
// poll tourne toutes les 15 s : un signal qui s'efface tout seul n'a aucune
// chance d'être vu), puis ne revient pas.
func TestSignalNeSeRepeteEtSeMarque(t *testing.T) {
	e := moteurSignal(t, nil)
	if err := e.Signale(); err != nil { // amorce
		t.Fatal(err)
	}
	distants := []NouveauSkill{{Slug: "revue-de-code", Auteur: "achille", CreeLe: "2026-08-20T10:00:00Z"}}
	e.client = clientFactice(t, distants)

	var journal []string
	e.logf = func(f string, a ...any) { journal = append(journal, f) }

	// Il tient sur plusieurs cycles, et ne se re-journalise pas.
	for i := 0; i < 3; i++ {
		if err := e.Signale(); err != nil {
			t.Fatal(err)
		}
		if len(e.NouveauxSkills()) != 1 {
			t.Fatalf("cycle %d : le signal a disparu avant d'être lu", i)
		}
	}
	lignes := 0
	for _, l := range journal {
		if strings.Contains(l, "nouveau skill") {
			lignes++
		}
	}
	if lignes != 1 {
		t.Errorf("le signal s'est journalisé %d fois au lieu d'une", lignes)
	}

	// Lu : il entre au registre et ne revient plus.
	if err := e.MarqueSkillsVus(); err != nil {
		t.Fatal(err)
	}
	if err := e.Signale(); err != nil {
		t.Fatal(err)
	}
	if n := e.NouveauxSkills(); len(n) != 0 {
		t.Fatalf("le signal est revenu après avoir été lu : %+v", n)
	}
	// Et il survit au redémarrage.
	relu, err := LoadState(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(relu.SkillsVus) != 1 || relu.SkillsVus[0] != "revue-de-code" {
		t.Errorf("le registre n'a pas été persisté : %+v", relu.SkillsVus)
	}
}

// TestSignalIndisponibleNeCassePasLeCycle : un serveur qui ne connaît pas encore
// l'endpoint (fenêtre de coexistence, au moins une version) ne doit pas faire
// échouer quoi que ce soit.
func TestSignalIndisponibleNeCassePasLeCycle(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/note.md", "texte\n")
	// Le serveur de test porte l'endpoint ; on le fait échouer autrement.
	e.client = NewHTTPClient("http://127.0.0.1:1", token)
	if err := e.Signale(); err == nil {
		t.Fatal("prérequis : l'appel devait échouer")
	}
	// Cycle complet contre le vrai serveur : le signal est best-effort, et
	// `Cycle` ne remonte jamais son erreur.
	e.client = NewHTTPClient(url, token)
	if err := e.Cycle(); err != nil {
		t.Fatalf("un signal indisponible a fait échouer le cycle : %v", err)
	}
}

// --- outillage ---

// clientFactice : un vrai HTTPClient contre un serveur qui ne répond qu'au
// signal. Le champ `client` de l'Engine est un *HTTPClient concret ; le
// transformer en interface pour ce test serait un refactor du type le plus
// central du moteur, pour du confort de test.
func clientFactice(t *testing.T, distants []NouveauSkill) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/skills/nouveaux" {
			http.NotFound(w, r)
			return
		}
		if distants == nil {
			distants = []NouveauSkill{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"nouveaux": distants})
	}))
	t.Cleanup(srv.Close)
	return NewHTTPClient(srv.URL, "t")
}

func moteurSignal(t *testing.T, distants []NouveauSkill) *Engine {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, vecuDir), 0o700); err != nil {
		t.Fatal(err)
	}
	e := &Engine{dir: dir, state: State{Files: map[string]string{}}, logf: func(string, ...any) {}}
	e.client = clientFactice(t, distants)
	return e
}

func distantsDe(e *Engine) []NouveauSkill {
	var out []NouveauSkill
	for _, s := range e.state.SkillsVus {
		out = append(out, NouveauSkill{Slug: s, Auteur: "achille", CreeLe: "2026-07-28T00:00:00Z"})
	}
	return out
}

// TestServeurAncienNAmorcePasAVide : le mode d'échec le plus coûteux de F4, et
// il est silencieux.
//
// Pendant la fenêtre de coexistence, un poste à jour interroge un serveur qui ne
// connaît pas encore l'endpoint. Si cet appel se lisait comme « aucun skill
// d'autrui », l'amorce enregistrerait un registre VIDE - et le jour où le
// serveur passe, les 24 skills existants sortiraient tous d'un coup. C'est
// exactement la rafale que l'amorce existe pour empêcher, décalée d'une version.
//
// Le repli sûr est de ne rien amorcer du tout tant qu'on n'a pas une réponse.
func TestServeurAncienNAmorcePasAVide(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, vecuDir), 0o700); err != nil {
		t.Fatal(err)
	}
	// Un serveur qui répond 404 sur tout, comme une version d'avant F4.
	ancien := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(ancien.Close)

	e := &Engine{dir: dir, state: State{Files: map[string]string{}}, logf: func(string, ...any) {}}
	e.client = NewHTTPClient(ancien.URL, "t")

	for i := 0; i < 3; i++ {
		if err := e.Signale(); err == nil {
			t.Fatal("un serveur sans l'endpoint devrait remonter une erreur")
		}
		if e.state.SkillsVus != nil {
			t.Fatalf("cycle %d : le registre a été amorcé sur une erreur (%+v) - les skills existants sortiront tous d'un coup à la mise à jour du serveur", i, e.state.SkillsVus)
		}
	}

	// Le serveur passe à une version qui connaît l'endpoint : l'amorce a
	// enfin lieu, sur la vraie liste, et n'annonce toujours rien.
	e.client = clientFactice(t, []NouveauSkill{
		{Slug: "revue-de-code", Auteur: "achille"},
		{Slug: "commit", Auteur: "achille"},
	})
	if err := e.Signale(); err != nil {
		t.Fatal(err)
	}
	if n := e.NouveauxSkills(); len(n) != 0 {
		t.Errorf("rafale au premier contact avec le serveur à jour : %+v", n)
	}
	if len(e.state.SkillsVus) != 2 {
		t.Errorf("registre = %+v", e.state.SkillsVus)
	}
}
