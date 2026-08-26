package sync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// lienVers : la cible lue du lien `.claude/skills/<slug>`, ou "" si absent/pas un lien.
func lienVers(t *testing.T, dir, slug string) string {
	t.Helper()
	dest, err := os.Readlink(filepath.Join(dir, ".claude", "skills", slug))
	if err != nil {
		return ""
	}
	return dest
}

// poseLien crée un lien symbolique `.claude/skills/<slug>` -> cible (brut).
func poseLien(t *testing.T, dir, slug, cible string) {
	t.Helper()
	d := filepath.Join(dir, ".claude", "skills")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cible, filepath.Join(d, slug)); err != nil {
		t.Fatal(err)
	}
}

func TestProjeterClaudeCreation(t *testing.T) {
	dir := t.TempDir()
	projetes, laisses := projeterClaude(dir, []string{"dev-scope", "daily"}, nil)
	if len(laisses) != 0 {
		t.Fatalf("accrocs inattendus : %v", laisses)
	}
	if !slices.Equal(projetes, []string{"daily", "dev-scope"}) {
		t.Fatalf("projetes = %v", projetes)
	}
	if got := lienVers(t, dir, "dev-scope"); got != "../../shared/skills/dev-scope" {
		t.Errorf("cible du lien = %q", got)
	}
}

// TestProjeterClaudeAdoption : un lien pré-existant déjà correct (posé à la main,
// inconnu de l'état) est ADOPTÉ, pas recréé, et déclaré géré. C'est le cas des 27
// skills montés à la main avant la feature.
func TestProjeterClaudeAdoption(t *testing.T) {
	dir := t.TempDir()
	poseLien(t, dir, "dev-scope", "../../shared/skills/dev-scope")
	avant, err := os.Lstat(filepath.Join(dir, ".claude/skills/dev-scope"))
	if err != nil {
		t.Fatal(err)
	}

	projetes, laisses := projeterClaude(dir, []string{"dev-scope"}, nil) // previous vide
	if len(laisses) != 0 {
		t.Fatalf("adoption d'un lien correct ne doit pas produire d'accroc : %v", laisses)
	}
	if !slices.Equal(projetes, []string{"dev-scope"}) {
		t.Fatalf("projetes = %v, attendu [dev-scope] adopté", projetes)
	}
	apres, _ := os.Lstat(filepath.Join(dir, ".claude/skills/dev-scope"))
	if !avant.ModTime().Equal(apres.ModTime()) {
		t.Error("le lien correct a été recréé au lieu d'être adopté")
	}
}

func TestProjeterClaudeIdempotent(t *testing.T) {
	dir := t.TempDir()
	p1, _ := projeterClaude(dir, []string{"dev-scope"}, nil)
	p2, l2 := projeterClaude(dir, []string{"dev-scope"}, p1)
	if len(l2) != 0 || !slices.Equal(p2, []string{"dev-scope"}) {
		t.Fatalf("second passage non idempotent : projetes=%v laisses=%v", p2, l2)
	}
}

// TestProjeterClaudeRetrait : un skill disparu (dans previous, plus dans desired)
// voit SON lien géré retiré ; un autre skill géré reste.
func TestProjeterClaudeRetrait(t *testing.T) {
	dir := t.TempDir()
	projeterClaude(dir, []string{"dev-scope", "ancien"}, nil)
	projetes, laisses := projeterClaude(dir, []string{"dev-scope"}, []string{"dev-scope", "ancien"})
	if len(laisses) != 0 {
		t.Fatalf("accrocs : %v", laisses)
	}
	if !slices.Equal(projetes, []string{"dev-scope"}) {
		t.Fatalf("projetes = %v", projetes)
	}
	if lienVers(t, dir, "ancien") != "" {
		t.Error("le lien du skill disparu n'a pas été retiré")
	}
	if lienVers(t, dir, "dev-scope") == "" {
		t.Error("le lien du skill maintenu a été retiré à tort")
	}
}

// TestProjeterClaudeCollisionDossierReel : un vrai dossier (skill fait main) au
// nom d'un skill désiré n'est JAMAIS écrasé ; accroc signalé, dossier intact.
func TestProjeterClaudeCollisionDossierReel(t *testing.T) {
	dir := t.TempDir()
	reel := filepath.Join(dir, ".claude/skills/dev-scope")
	if err := os.MkdirAll(reel, 0o755); err != nil {
		t.Fatal(err)
	}
	temoin := filepath.Join(reel, "SKILL.md")
	os.WriteFile(temoin, []byte("fait main"), 0o644)

	projetes, laisses := projeterClaude(dir, []string{"dev-scope"}, nil)
	if len(projetes) != 0 {
		t.Errorf("un skill en collision ne doit pas être déclaré projeté : %v", projetes)
	}
	if len(laisses) != 1 {
		t.Fatalf("collision non signalée : %v", laisses)
	}
	if b, _ := os.ReadFile(temoin); string(b) != "fait main" {
		t.Error("le dossier fait main a été écrasé")
	}
	if fi, _ := os.Lstat(reel); fi == nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Error("le vrai dossier a été remplacé par un lien")
	}
}

// TestProjeterClaudeCollisionLienEtranger : un lien vers un AUTRE store (ex:
// langfuse -> ../../.agents/skills/langfuse) n'est pas touché.
func TestProjeterClaudeCollisionLienEtranger(t *testing.T) {
	dir := t.TempDir()
	poseLien(t, dir, "langfuse", "../../.agents/skills/langfuse")
	projetes, laisses := projeterClaude(dir, []string{"langfuse"}, nil)
	if len(projetes) != 0 {
		t.Errorf("un lien étranger ne doit pas être adopté : %v", projetes)
	}
	if len(laisses) != 1 {
		t.Fatalf("collision étrangère non signalée : %v", laisses)
	}
	if lienVers(t, dir, "langfuse") != "../../.agents/skills/langfuse" {
		t.Error("le lien étranger a été modifié")
	}
}

// TestRetraitNeToucheJamaisUnEtranger : un slug géré au cycle précédent mais dont
// le chemin est désormais un vrai dossier (remplacé par l'utilisateur) n'est pas
// supprimé au retrait.
func TestRetraitNeToucheJamaisUnEtranger(t *testing.T) {
	dir := t.TempDir()
	reel := filepath.Join(dir, ".claude/skills/ancien")
	os.MkdirAll(reel, 0o755)
	os.WriteFile(filepath.Join(reel, "SKILL.md"), []byte("repris à la main"), 0o644)

	// 'ancien' était géré, n'est plus désiré : mais c'est un vrai dossier maintenant.
	projeterClaude(dir, nil, []string{"ancien"})
	if b, _ := os.ReadFile(filepath.Join(reel, "SKILL.md")); string(b) != "repris à la main" {
		t.Error("un vrai dossier au nom d'un ancien skill géré a été supprimé")
	}
}

// TestProjeterClaudeRefuseSiClaudeEstLien : si `.claude` est lui-même un lien
// symbolique, la projection est refusée en bloc et n'écrit rien (invariant :
// jamais écrire à travers un lien, ça sortirait de la racine).
func TestProjeterClaudeRefuseSiClaudeEstLien(t *testing.T) {
	dir := t.TempDir()
	ailleurs := t.TempDir()
	if err := os.Symlink(ailleurs, filepath.Join(dir, ".claude")); err != nil {
		t.Fatal(err)
	}
	projetes, laisses := projeterClaude(dir, []string{"dev-scope"}, nil)
	if len(projetes) != 0 || len(laisses) != 1 {
		t.Fatalf("projection non refusée : projetes=%v laisses=%v", projetes, laisses)
	}
	// Rien n'a été créé hors de la racine (dans la cible du lien .claude).
	if entries, _ := os.ReadDir(ailleurs); len(entries) != 0 {
		t.Errorf("la projection a écrit hors de la racine : %v", entries)
	}
}

// TestRetraitIgnoreSlugInvalide : un slug avec « .. » dans state.Projetes (état
// corrompu) ne doit JAMAIS servir à un os.Remove - il ferait sortir de
// .claude/skills/. Le slug est filtré avant tout usage.
func TestRetraitIgnoreSlugInvalide(t *testing.T) {
	dir := t.TempDir()
	// Un lien planté là où « ../evil » résoudrait, avec la cible que le code
	// attendrait, pour prouver qu'il n'est PAS retiré.
	claudeDir := filepath.Join(dir, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	temoin := filepath.Join(claudeDir, "evil")
	if err := os.Symlink("../../shared/evil", temoin); err != nil {
		t.Fatal(err)
	}
	projeterClaude(dir, nil, []string{"../evil"})
	if _, err := os.Lstat(temoin); err != nil {
		t.Error("un slug invalide de previous a provoqué une suppression hors de .claude/skills/")
	}
}

func TestSkillsMontes(t *testing.T) {
	e := &Engine{state: State{Files: map[string]string{
		"shared/skills/dev-scope/SKILL.md":     "h",
		"shared/skills/dev-scope/reference.md": "h",
		"shared/skills/daily/SKILL.md":         "h",
		"shared/skills/sans-md/autre.md":       "h", // pas de SKILL.md : ignoré
		"shared/skills/README.md":              "h", // fichier direct : pas un skill
		"shared/note.md":                       "h", // hors skills
		"shared/skills/équipe/SKILL.md":        "h", // slug invalide : ignoré
	}}}
	got := e.skillsMontes()
	if !slices.Equal(got, []string{"daily", "dev-scope"}) {
		t.Errorf("skillsMontes = %v, attendu [daily dev-scope]", got)
	}
}

// TestDetecteClaude vérifie le signal d'activation par défaut de la projection.
// $HOME et $PATH sont neutralisés pour que seul le `.claude/` de la racine
// compte (la machine de test a un vrai ~/.claude et parfois `claude` sur le PATH).
func TestDetecteClaude(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // home sans .claude
	t.Setenv("PATH", "")          // pas de `claude` trouvable

	dir := t.TempDir()
	if detecteClaude(dir) {
		t.Fatal("racine sans .claude, home vide, PATH vide : détection attendue à false")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !detecteClaude(dir) {
		t.Fatal("racine avec .claude/ : détection attendue à true")
	}
}

// TestNewEngineActiveProjection est le garde-fou du bug F4 : un poste où Claude
// est présent mais dont la config n'a jamais renseigné Projections (poste
// onboardé avant la feature skills, ou adopté par l'app sans `vecu setup`) doit
// finir avec la projection active ET persistée, sans aucune commande.
func TestNewEngineActiveProjection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")

	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: "https://x", Username: "u", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if !slices.Equal(e.projections, []string{"claude"}) {
		t.Fatalf("projections = %v, attendu [claude]", e.projections)
	}
	// Persistée : un `vecu status` ultérieur (qui relit la config) doit la voir.
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Projections, []string{"claude"}) {
		t.Fatalf("config.Projections persistée = %v, attendu [claude]", cfg.Projections)
	}
}

// TestNewEngineSansClaudeNeProjettePas : sans agent détecté, la projection reste
// éteinte (on n'impose rien à un poste qui n'utilise pas Claude).
func TestNewEngineSansClaudeNeProjettePas(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")

	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: "https://x", Username: "u", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if len(e.projections) != 0 {
		t.Fatalf("projections = %v, attendu vide (aucun agent détecté)", e.projections)
	}
}

// moteurProjection : un moteur minimal dont `skillsMontes()` rendra exactement
// `slugs`, avec la projection Claude active. Calqué sur `moteurAdoption`, dont
// il ne partage pas les besoins (pas de racine sur disque à préparer).
func moteurProjection(t *testing.T, dir string, slugs []string) *Engine {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".vecu"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, slug := range slugs {
		files["shared/skills/"+slug+"/SKILL.md"] = "empreinte"
	}
	return &Engine{
		dir:         dir,
		state:       State{Files: files, Espaces: []string{"shared"}},
		projections: []string{"claude"},
		logf:        func(string, ...any) {},
	}
}

// --- Cible `.agents/skills/` (Cursor + Codex) ---
//
// La règle qui gouverne ces tests : la cible n'est peuplée QUE si son dossier
// existe déjà. Le `mkdir` de l'utilisateur porte l'intention.

// TestAgentsNonProjeteSiDossierAbsent : sans `.agents/skills/`, rien n'est créé
// et l'état reste vide. C'est le cas du poste qui n'utilise ni Cursor ni Codex :
// il ne doit voir aucune différence avec la version précédente.
func TestAgentsNonProjeteSiDossierAbsent(t *testing.T) {
	dir := t.TempDir()
	e := moteurProjection(t, dir, []string{"dev-scope"})
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agents")); !os.IsNotExist(err) {
		t.Errorf(".agents/ a été créé alors que le dossier n'existait pas (err=%v)", err)
	}
	if got := e.State().ProjetesAgents; len(got) != 0 {
		t.Errorf("ProjetesAgents = %v, attendu vide", got)
	}
}

// TestAgentsProjeteSiDossierPresent : dossier créé à la main, donc les liens
// sont posés, avec la même cible relative que pour Claude.
func TestAgentsProjeteSiDossierPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"dev-scope", "daily"})
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	if got := e.State().ProjetesAgents; !slices.Equal(got, []string{"daily", "dev-scope"}) {
		t.Fatalf("ProjetesAgents = %v", got)
	}
	dest, err := os.Readlink(filepath.Join(dir, ".agents", "skills", "dev-scope"))
	if err != nil {
		t.Fatalf("lien absent : %v", err)
	}
	if dest != "../../shared/skills/dev-scope" {
		t.Errorf("cible du lien = %q", dest)
	}
}

// TestAgentsNEcrasePasUnDossierFaitMain : `langfuse` et `typefully` existent
// dans le `.agents/skills/` du poste de Colin, faits main et inconnus de Vécu.
// Un skill du même slug ne doit ni les écraser ni les déplacer.
func TestAgentsNEcrasePasUnDossierFaitMain(t *testing.T) {
	dir := t.TempDir()
	faitMain := filepath.Join(dir, ".agents", "skills", "langfuse")
	if err := os.MkdirAll(faitMain, 0o755); err != nil {
		t.Fatal(err)
	}
	temoin := filepath.Join(faitMain, "SKILL.md")
	if err := os.WriteFile(temoin, []byte("fait main"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"langfuse"})
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	b, err := os.ReadFile(temoin)
	if err != nil || string(b) != "fait main" {
		t.Fatalf("le dossier fait main a été touché (contenu=%q, err=%v)", b, err)
	}
	if got := e.State().ProjetesAgents; len(got) != 0 {
		t.Errorf("ProjetesAgents = %v, attendu vide (rien n'est géré)", got)
	}
	var vu bool
	for _, l := range e.Laisses() {
		if strings.Contains(l.Raison, agentsSkillsRel+"/langfuse existe déjà") {
			vu = true
			if l.Genre != GenreServeur {
				t.Errorf("genre = %q, attendu %q", l.Genre, GenreServeur)
			}
		}
	}
	if !vu {
		t.Errorf("aucune Laisse ne signale la collision : %v", e.Laisses())
	}
}

// TestAgentsSurvitAUneDisparitionDuDossier : le dossier disparaît puis revient
// (rename, volume démonté, bascule de dotfiles). Deux exigences opposées, et
// c'est leur conjonction qui compte : les liens sont REPOSÉS sur le dossier
// recréé, ET le registre n'a pas été vidé entre-temps. Le vider laisserait
// orphelin, à jamais, tout skill sorti du périmètre pendant la fenêtre.
func TestAgentsSurvitAUneDisparitionDuDossier(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, ".agents", "skills")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"dev-scope"})
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	if len(e.State().ProjetesAgents) != 1 {
		t.Fatalf("préalable non tenu : %v", e.State().ProjetesAgents)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".agents")); err != nil {
		t.Fatal(err)
	}
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette après suppression : %v", err)
	}
	if got := e.State().ProjetesAgents; !slices.Equal(got, []string{"dev-scope"}) {
		t.Errorf("registre = %v, attendu conservé pendant l'absence", got)
	}
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette après recréation : %v", err)
	}
	if _, err := os.Readlink(filepath.Join(agents, "dev-scope")); err != nil {
		t.Errorf("le lien n'a pas été reposé sur le dossier recréé : %v", err)
	}
	// Le vrai enjeu : un skill sorti du périmètre PENDANT l'absence doit encore
	// pouvoir être retiré au retour. Sans registre, son lien resterait à vie.
	delete(e.state.Files, "shared/skills/dev-scope/SKILL.md")
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette après retrait du périmètre : %v", err)
	}
	if _, err := os.Lstat(filepath.Join(agents, "dev-scope")); !os.IsNotExist(err) {
		t.Errorf("le lien d'un skill hors périmètre subsiste (err=%v)", err)
	}
}

// TestAgentsRefuseSiLeDossierEstUnLien : `.agents/` géré par un gestionnaire de
// dotfiles. Écrire au travers sortirait de la racine.
func TestAgentsRefuseSiLeDossierEstUnLien(t *testing.T) {
	dir := t.TempDir()
	ailleurs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ailleurs, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ailleurs, filepath.Join(dir, ".agents")); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"dev-scope"})
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	if _, err := os.Readlink(filepath.Join(ailleurs, "skills", "dev-scope")); err == nil {
		t.Error("un lien a été écrit à travers le lien symbolique, hors de la racine")
	}
	if got := e.State().ProjetesAgents; len(got) != 0 {
		t.Errorf("ProjetesAgents = %v, attendu vide", got)
	}
}

// TestAgentsFichierEnTraversNeProduitAucunAccroc : `.agents` est un fichier
// ordinaire, donc il n'y a pas de dossier cible. C'est « absent », pas
// « illisible » : sans ça, `os.Stat` rendait ENOTDIR, la cible était jugée
// présente, et chaque skill produisait une Laisse à chaque cycle, indéfiniment.
func TestAgentsFichierEnTraversNeProduitAucunAccroc(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".agents"), []byte("pas un dossier"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"dev-scope", "daily"})
	for i := range 2 {
		if err := e.Projette(); err != nil {
			t.Fatalf("Projette (cycle %d) : %v", i+1, err)
		}
	}
	for _, l := range e.Laisses() {
		if strings.Contains(l.Raison, agentsSkillsRel) {
			t.Errorf("accroc sur une cible qui n'existe pas : %+v", l)
		}
	}
	if got := e.State().ProjetesAgents; len(got) != 0 {
		t.Errorf("ProjetesAgents = %v, attendu vide", got)
	}
}

// TestAgentsLienCasseEstRefuseEtSignale : `.agents/skills` est un lien
// symbolique dont la cible n'existe pas encore (gestionnaire de dotfiles pas
// déployé). Le refus doit être explicite, et le registre conservé - un lien
// cassé lu comme « dossier absent » effacerait le registre en silence.
func TestAgentsLienCasseEstRefuseEtSignale(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nulle-part"), filepath.Join(dir, ".agents", "skills")); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"dev-scope"})
	e.state.ProjetesAgents = []string{"dev-scope"}
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	if got := e.State().ProjetesAgents; !slices.Equal(got, []string{"dev-scope"}) {
		t.Errorf("registre = %v, attendu conservé sur un refus", got)
	}
	var vu bool
	for _, l := range e.Laisses() {
		if strings.Contains(l.Raison, "lien symbolique") {
			vu = true
		}
	}
	if !vu {
		t.Errorf("le refus n'est signalé nulle part : %+v", e.Laisses())
	}
}

// TestProjetteNEcritPasSansChangement : sans Claude et sans `.agents/skills/`,
// aucune projection n'est active. L'ancien code sortait avant toute écriture ;
// réécrire state.json toutes les 15 s pour rien serait une régression.
func TestProjetteNEcritPasSansChangement(t *testing.T) {
	dir := t.TempDir()
	e := moteurProjection(t, dir, []string{"dev-scope"})
	e.projections = nil
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	if _, err := os.Stat(statePath(dir)); !os.IsNotExist(err) {
		t.Errorf("state.json écrit alors qu'aucune projection n'est active (err=%v)", err)
	}
}

// TestLesDeuxLiensPartentEnsemble : un skill qui sort du périmètre voit ses deux
// liens retirés, et eux seuls.
func TestLesDeuxLiensPartentEnsemble(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"dev-scope", "daily"})
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	// `daily` sort du périmètre (accès retiré côté serveur).
	delete(e.state.Files, "shared/skills/daily/SKILL.md")
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette (2) : %v", err)
	}
	for _, cible := range []string{".claude/skills", ".agents/skills"} {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(cible), "daily")); !os.IsNotExist(err) {
			t.Errorf("%s/daily subsiste (err=%v)", cible, err)
		}
		if _, err := os.Readlink(filepath.Join(dir, filepath.FromSlash(cible), "dev-scope")); err != nil {
			t.Errorf("%s/dev-scope a été retiré à tort : %v", cible, err)
		}
	}
}

// TestAgentsParentSymlinkeEstRefuseEtSignale : `.agents` est LUI-MÊME un lien,
// et sa cible ne contient pas encore `skills`. Lstat ne refuse de suivre que le
// dernier composant, donc sans garde explicite ce cas se lit « absent » et passe
// en silence - alors que c'est le scénario dotfiles que le refus doit couvrir.
func TestAgentsParentSymlinkeEstRefuseEtSignale(t *testing.T) {
	dir := t.TempDir()
	ailleurs := t.TempDir() // volontairement SANS sous-dossier `skills`
	if err := os.Symlink(ailleurs, filepath.Join(dir, ".agents")); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"dev-scope"})
	e.state.ProjetesAgents = []string{"dev-scope"}
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	if got := e.State().ProjetesAgents; !slices.Equal(got, []string{"dev-scope"}) {
		t.Errorf("registre = %v, attendu conservé sur un refus", got)
	}
	var vu bool
	for _, l := range e.Laisses() {
		if strings.Contains(l.Raison, "lien symbolique") {
			vu = true
		}
	}
	if !vu {
		t.Errorf("le refus n'est signalé nulle part : %+v", e.Laisses())
	}
}

// TestClaudeRefuseConserveLeRegistre : le pendant du contrat 9 côté Claude.
// `Projette` ne doit pas écraser `Projetes` quand la cible entière est refusée -
// sinon les liens déjà posés deviennent orphelins. Ce chemin passe par
// `projeteCible`, que les tests Claude n'empruntent pas (ils passent par le
// wrapper `projeterClaude`, qui jette le drapeau de refus).
func TestClaudeRefuseConserveLeRegistre(t *testing.T) {
	dir := t.TempDir()
	ailleurs := t.TempDir()
	if err := os.Symlink(ailleurs, filepath.Join(dir, ".claude")); err != nil {
		t.Fatal(err)
	}
	e := moteurProjection(t, dir, []string{"dev-scope"})
	e.state.Projetes = []string{"dev-scope"}
	if err := e.Projette(); err != nil {
		t.Fatalf("Projette : %v", err)
	}
	if got := e.State().Projetes; !slices.Equal(got, []string{"dev-scope"}) {
		t.Errorf("Projetes = %v, attendu conservé sur un refus", got)
	}
}

// TestDetectionNeReactivePasUnChoixRetire : l'utilisateur retire "claude" de sa
// config. La détection ne doit pas le remettre au démarrage suivant. C'est le
// cas que l'ancienne condition (`len(Projections) == 0`) ne savait pas
// distinguer d'un poste jamais configuré.
func TestDetectionNeReactivePasUnChoixRetire(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil { // Claude détecté
		t.Fatal(err)
	}
	if err := SaveConfig(dir, Config{Server: "http://x", Username: "u", Token: "t"}); err != nil {
		t.Fatal(err)
	}

	// Premier démarrage : la détection propose, la projection s'active.
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Projections, "claude") || !slices.Contains(cfg.ProjectionsVues, "claude") {
		t.Fatalf("premier démarrage : Projections=%v Vues=%v", cfg.Projections, cfg.ProjectionsVues)
	}

	// L'utilisateur retire la projection.
	cfg.Projections = nil
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}

	// Deuxième démarrage : le choix tient.
	e2, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	e2.Close()
	cfg2, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cfg2.Projections, "claude") {
		t.Errorf("la détection a réactivé un outil retiré à la main : %v", cfg2.Projections)
	}
}

// TestDetectionMigreUnPosteDejaConfigure : un poste installé avant ce champ, avec
// `Projections: ["claude"]`, ne doit rien voir changer sinon l'apparition de
// "claude" dans les outils déjà proposés.
func TestDetectionMigreUnPosteDejaConfigure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(dir, Config{Server: "http://x", Username: "u", Token: "t", Projections: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Projections, []string{"claude"}) {
		t.Errorf("Projections = %v, attendu inchangé", cfg.Projections)
	}
	if !slices.Contains(cfg.ProjectionsVues, "claude") {
		t.Errorf("ProjectionsVues = %v, attendu contenir claude", cfg.ProjectionsVues)
	}
}
