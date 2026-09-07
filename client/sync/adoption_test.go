package sync

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// poseRacine : une racine synchronisée où l'espace `shared` est monté. Sans état,
// aucune adoption n'est possible, et c'est voulu : ranger un skill dans un espace
// non monté le rendrait invisible à la sync (cf. contexte.espaceMonte).
func poseRacine(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vecu"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (State{Files: map[string]string{}, Espaces: []string{"shared"}}).Save(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// poseIgnore écrit un .vecuignore à la racine.
func poseIgnore(t *testing.T, dir string, motifs ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ignoreFichier), []byte(strings.Join(motifs, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// poseSkill crée un vrai dossier de skill `<dir>/<rel>/<slug>/SKILL.md`.
func poseSkill(t *testing.T, dir, rel, slug string) string {
	t.Helper()
	d := filepath.Join(dir, filepath.FromSlash(rel), slug)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+slug+"\n---\ncorps\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

// maisonDeTest : un faux dossier personnel dont le chemin est RÉSOLU. Sur macOS,
// t.TempDir() rend un /var/... qui est un lien vers /private/var/..., et
// l'adoption hors racine refuse par principe tout chemin qui passe par un lien.
// Un test doit exercer la règle, pas la contourner ni s'y cogner.
func maisonDeTest(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// raisonDe : la raison du refus portant sur ce chemin, ou "".
func raisonDe(refuses []Candidat, chemin string) string {
	for _, c := range refuses {
		if c.Chemin == chemin {
			return c.Raison
		}
	}
	return ""
}

// TestAdopteNominal : le cas qui motive la feature. Un skill créé à la main dans
// `.claude/skills/` part dans `shared/skills/` et un lien prend sa place.
func TestAdopteNominal(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "mon-skill")
	if err := os.WriteFile(filepath.Join(source, "reference.md"), []byte("annexe"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := adopte(dir, source)
	if err != nil {
		t.Fatalf("adoption refusée : %v", err)
	}
	if res.Avertissement != "" {
		t.Fatalf("avertissement inattendu : %s", res.Avertissement)
	}
	for _, rel := range []string{"shared/skills/mon-skill/SKILL.md", "shared/skills/mon-skill/reference.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s absent après adoption : %v", rel, err)
		}
	}
	// Le lien laissé à la place doit avoir EXACTEMENT la cible de la projection,
	// sinon projeterClaude le prendrait pour un lien étranger.
	if got := lienVers(t, dir, "mon-skill"); got != "../../shared/skills/mon-skill" {
		t.Errorf("cible du lien = %q", got)
	}
}

// TestAdopteLienReconnuParLaProjection verrouille la couture entre les deux
// étapes : le lien posé par l'adoption est ADOPTÉ par la projection du même
// cycle, pas recréé ni signalé comme étranger.
func TestAdopteLienReconnuParLaProjection(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "mon-skill")
	if _, err := adopte(dir, source); err != nil {
		t.Fatal(err)
	}
	avant, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}

	projetes, laisses := projeterClaude(dir, []string{"mon-skill"}, nil)
	if len(laisses) != 0 {
		t.Fatalf("la projection n'a pas reconnu son propre lien : %v", laisses)
	}
	if !slices.Equal(projetes, []string{"mon-skill"}) {
		t.Fatalf("projetes = %v", projetes)
	}
	apres, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(avant, apres) {
		t.Error("le lien a été recréé alors qu'il était déjà correct")
	}
}

// TestCandidatsIgnoreEnSilence : ce qui n'est pas un skill ne produit ni
// adoption ni bruit. Le symlink couvre les 25 liens de projection déjà en place
// et les liens étrangers (langfuse, typefully).
func TestCandidatsIgnoreEnSilence(t *testing.T) {
	dir := poseRacine(t)
	poseLien(t, dir, "deja-projete", "../../shared/skills/deja-projete")
	poseLien(t, dir, "etranger", "../../.agents/skills/etranger")
	if err := os.MkdirAll(filepath.Join(dir, ".claude/skills/pas-un-skill"), 0o755); err != nil {
		t.Fatal(err)
	}

	adoptables, refuses := candidats(dir)
	if len(adoptables) != 0 || len(refuses) != 0 {
		t.Fatalf("adoptables = %v, refuses = %v", adoptables, refuses)
	}
	// Et rien n'a bougé.
	if got := lienVers(t, dir, "etranger"); got != "../../.agents/skills/etranger" {
		t.Errorf("lien étranger touché : %q", got)
	}
}

func TestCandidatsRefusSlugInvalide(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "-tiret-en-tete")

	_, refuses := candidats(dir)
	if r := raisonDe(refuses, ".claude/skills/-tiret-en-tete"); !strings.Contains(r, "nom de dossier invalide") {
		t.Fatalf("raison = %q", r)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude/skills/-tiret-en-tete/SKILL.md")); err != nil {
		t.Error("la source doit rester intacte après un refus")
	}
}

// TestCandidatsRefusSynced : dossier réservé par Claude Code, quelle que soit la
// casse (la doc précise « in any capitalization »).
func TestCandidatsRefusSynced(t *testing.T) {
	for _, nom := range []string{"synced", "Synced"} {
		dir := poseRacine(t)
		poseSkill(t, dir, ".claude/skills", nom)

		_, refuses := candidats(dir)
		if r := raisonDe(refuses, ".claude/skills/"+nom); !strings.Contains(r, "réservé") {
			t.Errorf("%s : raison = %q", nom, r)
		}
	}
}

func TestCandidatsRefusPlugin(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "un-plugin")
	if err := os.MkdirAll(filepath.Join(source, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".claude-plugin/plugin.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, refuses := candidats(dir)
	if r := raisonDe(refuses, ".claude/skills/un-plugin"); !strings.Contains(r, "plugin") {
		t.Fatalf("raison = %q", r)
	}
}

// TestCandidatsRefusVerrou : un skill installé par un gestionnaire tiers n'a pas
// à devenir un skill de la boîte.
//
// Ce test fabrique un vrai dossier `langfuse`, une forme qui n'existe pas dans le
// vault de Colin (langfuse et typefully y sont des LIENS vers `.agents/skills/`,
// donc c'est le premier Lstat qui les écarte, jamais le verrou). Le verrou sert
// la machine où le gestionnaire tiers pose de vrais dossiers.
func TestCandidatsRefusVerrou(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "langfuse")
	poseSkill(t, dir, ".claude/skills", "maison")
	lock := `{"version":1,"skills":{"langfuse":{"source":"langfuse/skills"}}}`
	if err := os.WriteFile(filepath.Join(dir, "skills-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptables, refuses := candidats(dir)
	if !slices.Equal(adoptables, []string{"maison"}) {
		t.Fatalf("adoptables = %v", adoptables)
	}
	if r := raisonDe(refuses, ".claude/skills/langfuse"); !strings.Contains(r, "gestionnaire tiers") {
		t.Fatalf("raison = %q", r)
	}
}

// TestCandidatsVerrouIllisibleNeProtegePlus : un manifeste cassé retire une
// protection, il ne bloque pas la commande.
func TestCandidatsVerrouIllisibleNeProtegePlus(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "maison")
	if err := os.WriteFile(filepath.Join(dir, "skills-lock.json"), []byte("{ pas du json"), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptables, _ := candidats(dir)
	if !slices.Equal(adoptables, []string{"maison"}) {
		t.Fatalf("adoptables = %v", adoptables)
	}
}

func TestCandidatsRefusEntreeNonReguliere(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "avec-lien")
	if err := os.Symlink("/etc/hosts", filepath.Join(source, "raccourci")); err != nil {
		t.Fatal(err)
	}

	_, refuses := candidats(dir)
	if r := raisonDe(refuses, ".claude/skills/avec-lien"); !strings.Contains(r, "ni un fichier ni un dossier") {
		t.Fatalf("raison = %q", r)
	}
}

// TestCandidatsRefusCollision : on ne devine jamais une fusion, et la source
// reste strictement intacte.
func TestCandidatsRefusCollision(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "daily")
	poseSkill(t, dir, "shared/skills", "daily")
	if err := os.WriteFile(filepath.Join(dir, "shared/skills/daily/SKILL.md"), []byte("version du dépôt"), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptables, refuses := candidats(dir)
	if len(adoptables) != 0 {
		t.Fatalf("adoptables = %v", adoptables)
	}
	if r := raisonDe(refuses, ".claude/skills/daily"); !strings.Contains(r, "existe déjà") {
		t.Fatalf("raison = %q", r)
	}
	got, err := os.ReadFile(filepath.Join(dir, "shared/skills/daily/SKILL.md"))
	if err != nil || string(got) != "version du dépôt" {
		t.Errorf("le skill du dépôt a été touché : %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude/skills/daily/SKILL.md")); err != nil {
		t.Error("la source doit rester intacte après un refus")
	}
}

// TestCandidatsRefusIgnore : ranger un skill dans un chemin écarté par le
// .vecuignore serait un rangement en trompe-l'œil.
func TestCandidatsRefusIgnore(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "brouillon")
	poseIgnore(t, dir, "shared/skills/brouillon")

	_, refuses := candidats(dir)
	if r := raisonDe(refuses, ".claude/skills/brouillon"); !strings.Contains(r, ".vecuignore") {
		t.Fatalf("raison = %q", r)
	}
}

// TestCandidatsRefusClaudeSymlinke : même garde que la projection, on n'écrit
// jamais à travers un lien - ça sortirait de la racine.
func TestCandidatsRefusClaudeSymlinke(t *testing.T) {
	dir := poseRacine(t)
	ailleurs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ailleurs, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ailleurs, filepath.Join(dir, ".claude")); err != nil {
		t.Fatal(err)
	}

	adoptables, refuses := candidats(dir)
	if len(adoptables) != 0 {
		t.Fatalf("adoptables = %v", adoptables)
	}
	if r := raisonDe(refuses, ".claude/skills"); !strings.Contains(r, "lien symbolique") {
		t.Fatalf("raison = %q", r)
	}
}

// TestAdopteHorsRacine : source hors de la racine (cas `~/.claude/skills/`), le
// lien de remplacement doit être ABSOLU.
func TestAdopteHorsRacine(t *testing.T) {
	dir := poseRacine(t)
	maison := maisonDeTest(t)
	source := poseSkill(t, maison, ".claude/skills", "perso")

	if _, err := adopte(dir, source); err != nil {
		t.Fatalf("adoption refusée : %v", err)
	}
	dest, err := os.Readlink(source)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(dest) {
		t.Fatalf("lien hors racine = %q, attendu absolu", dest)
	}
	if dest != filepath.Join(dir, "shared/skills/perso") {
		t.Fatalf("cible = %q", dest)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared/skills/perso/SKILL.md")); err != nil {
		t.Errorf("contenu non arrivé : %v", err)
	}
}

// TestCopieVerifieeRepli : le chemin de repli de `deplace`, quand Rename ne peut
// pas (volumes différents). Testé seul, un test ne pouvant pas faire échouer
// Rename sur un même volume.
func TestCopieVerifieeRepli(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, "ailleurs", "mon-skill")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "references/patron.md"), []byte("patron"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "shared/skills/mon-skill")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := copieVerifiee(source, dest); err != nil {
		t.Fatalf("copie : %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "references/patron.md"))
	if err != nil || string(got) != "patron" {
		t.Errorf("arbre non copié : %q, %v", got, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Error("la source doit être retirée après une copie vérifiée")
	}
}

// TestMemeArbreDetecteUneTroncature : c'est cette vérification qui autorise la
// suppression de la source. Si elle passe à côté d'une copie incomplète, on perd
// du contenu.
func TestMemeArbreDetecteUneTroncature(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, "src", "s")
	dest := poseSkill(t, dir, "dst", "s")
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("tronqué"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := memeArbre(source, dest)
	if err == nil || !strings.Contains(err.Error(), "tronqué") {
		t.Fatalf("troncature non détectée : %v", err)
	}

	// Et un fichier absent de la copie.
	if err := os.Remove(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := memeArbre(source, dest); err == nil || !strings.Contains(err.Error(), "manquant") {
		t.Fatalf("fichier manquant non détecté : %v", err)
	}
}

// TestAdopteRefuseCeQuiNestPasUnSkill : l'appel direct (commande manuelle) doit
// refuser proprement, pas se taire comme le scan.
func TestAdopteRefuseCeQuiNestPasUnSkill(t *testing.T) {
	dir := poseRacine(t)
	nu := filepath.Join(dir, ".claude/skills/nu")
	if err := os.MkdirAll(nu, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := adopte(dir, nu); err == nil || !strings.Contains(err.Error(), "SKILL.md") {
		t.Fatalf("err = %v", err)
	}
}

// ---- Tests issus de la revue adversariale (chacun vérifié comme échouant sur
// le code d'avant correction). ----

// TestCopieVerifieeNeDetruitPasUneDestinationExistante : CopyFS copie ce qui ne
// collisionne pas puis échoue sur le premier conflit, en laissant la destination
// en place. Un RemoveAll aveugle derrière effacerait du contenu du dépôt.
func TestCopieVerifieeNeDetruitPasUneDestinationExistante(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, "src", "s")
	dest := poseSkill(t, dir, "shared/skills", "s")
	precieux := filepath.Join(dest, "SKILL.md")
	if err := os.WriteFile(precieux, []byte("version du dépôt"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := copieVerifiee(source, dest); err == nil {
		t.Fatal("une destination existante doit être refusée")
	}
	got, err := os.ReadFile(precieux)
	if err != nil || string(got) != "version du dépôt" {
		t.Fatalf("contenu du dépôt détruit : %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// TestCopieVerifieeEchoueeLaisseLaSourceIntacte : l'invariant central du fichier,
// testé sur la branche qui compte (la copie qui rate en cours de route).
func TestCopieVerifieeEchoueeLaisseLaSourceIntacte(t *testing.T) {
	// Ce test rend une opération impossible en retirant un bit de permission
	// Unix. Windows ne les porte pas : `os.Chmod` n'y touche que le drapeau
	// lecture seule, la précondition ne peut donc pas être créée et le test
	// vérifierait le contraire de ce qu'il annonce.
	//
	// Ce que ça laisse découvert, et il faut le dire : le chemin d'ERREUR de
	// `copieVerifiee` / `adopte` n'est pas exercé sur Windows. À couvrir
	// autrement le jour où on saura rendre une copie impossible là-bas
	// (fichier ouvert en exclusif, ACL) - c'est écrit dans spec-port-windows.md.
	if runtime.GOOS == "windows" {
		t.Skip("précondition = bit de permission Unix, inexistant sur Windows")
	}
	dir := poseRacine(t)
	source := poseSkill(t, dir, "src", "s")
	if os.Geteuid() == 0 {
		t.Skip("root lit un fichier 0o000 : le cas ne peut pas être provoqué")
	}
	illisible := filepath.Join(source, "secret.md")
	if err := os.WriteFile(illisible, []byte("contenu"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(illisible, 0o644) })
	dest := filepath.Join(dir, "shared/skills/s")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := copieVerifiee(source, dest); err == nil {
		t.Fatal("une copie impossible doit échouer")
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("source supprimée après une copie ratée")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("destination partielle laissée derrière")
	}
}

// TestAdopteSlashFinal : un chemin avec slash final (complétion shell) ne doit
// pas décaler la cible du lien d'un cran.
func TestAdopteSlashFinal(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	source := filepath.Join(dir, ".claude/skills/mon-skill") + string(filepath.Separator)

	if _, err := adopte(dir, source); err != nil {
		t.Fatalf("adoption refusée : %v", err)
	}
	if got := lienVers(t, dir, "mon-skill"); got != "../../shared/skills/mon-skill" {
		t.Fatalf("cible du lien = %q", got)
	}
}

// TestAdopteSourceRelative : un chemin relatif ne doit pas basculer le lien en
// absolu (la projection ne l'adopterait jamais, et signalerait à chaque cycle).
func TestAdopteSourceRelative(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	t.Chdir(dir)

	if _, err := adopte(dir, ".claude/skills/mon-skill"); err != nil {
		t.Fatalf("adoption refusée : %v", err)
	}
	if got := lienVers(t, dir, "mon-skill"); got != "../../shared/skills/mon-skill" {
		t.Fatalf("cible du lien = %q", got)
	}
}

// TestAdopteRefuseClaudeSymlinke : la garde de `candidats` doit aussi tenir sur
// la fonction qui mute le disque, sinon on écrit et supprime à travers un lien.
func TestAdopteRefuseClaudeSymlinke(t *testing.T) {
	dir := poseRacine(t)
	ailleurs := t.TempDir()
	source := poseSkill(t, ailleurs, "skills", "perso")
	if err := os.Symlink(ailleurs, filepath.Join(dir, ".claude")); err != nil {
		t.Fatal(err)
	}

	_, err := adopte(dir, filepath.Join(dir, ".claude/skills/perso"))
	if err == nil {
		t.Fatal("adoption à travers un lien acceptée")
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// TestCandidatsRefusSkillMdNonRegulier : un SKILL.md lié (dotfiles) est un skill,
// pas un non-événement : il mérite une raison.
func TestCandidatsRefusSkillMdNonRegulier(t *testing.T) {
	dir := poseRacine(t)
	d := filepath.Join(dir, ".claude/skills/lie")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	cible := filepath.Join(dir, "ailleurs.md")
	if err := os.WriteFile(cible, []byte("---\nname: lie\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cible, filepath.Join(d, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	_, refuses := candidats(dir)
	if r := raisonDe(refuses, ".claude/skills/lie"); r == "" {
		t.Fatal("un SKILL.md non régulier doit être signalé, pas ignoré en silence")
	}
}

// TestCandidatsRefusIgnoreSurLeChemin : un motif qui désigne le dossier parent
// (« shared/skills ») écarte tout ce qu'il contient, sans jamais matcher le
// chemin complet du skill.
func TestCandidatsRefusIgnoreSurLeChemin(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "brouillon")
	poseIgnore(t, dir, "shared/skills")

	adoptables, refuses := candidats(dir)
	if len(adoptables) != 0 {
		t.Fatalf("adoptables = %v", adoptables)
	}
	if r := raisonDe(refuses, ".claude/skills/brouillon"); !strings.Contains(r, ignoreFichier) {
		t.Fatalf("raison = %q", r)
	}
}

// TestCandidatsCasseDuSkillMd : sur un système insensible à la casse (APFS), un
// `skill.md` passerait pour un skill ici et ne serait jamais projeté ailleurs
// (SkillsMontes compare exactement). Une adoption invisible sur l'autre poste
// est le pire mode de défaillance pour un outil de sync.
func TestCandidatsCasseDuSkillMd(t *testing.T) {
	dir := poseRacine(t)
	d := filepath.Join(dir, ".claude/skills/casse")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "skill.md"), []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptables, refuses := candidats(dir)
	if len(adoptables) != 0 {
		t.Fatalf("adoptables = %v (un skill.md minuscule n'est pas un SKILL.md)", adoptables)
	}
	if r := raisonDe(refuses, ".claude/skills/casse"); !strings.Contains(r, "Linux") {
		t.Fatalf("un quasi-homonyme doit être signalé, pas tu : %q", r)
	}
}

// ---- Tests issus du deuxième passage adversarial. ----

// TestAdopteSourceNonSupprimable : os.RemoveAll supprime les enfants d'abord.
// Quand il échoue, il ne reste qu'un dossier VIDE, qui occupe le chemin du lien.
// Le contenu est rangé, mais l'agent local ne verra rien tant que ce dossier est
// là - et la projection refusera de l'écraser à chaque cycle. Donc : pas de
// tentative de lien, et un message qui appelle une action humaine.
func TestAdopteSourceNonSupprimable(t *testing.T) {
	// Ce test rend une opération impossible en retirant un bit de permission
	// Unix. Windows ne les porte pas : `os.Chmod` n'y touche que le drapeau
	// lecture seule, la précondition ne peut donc pas être créée et le test
	// vérifierait le contraire de ce qu'il annonce.
	//
	// Ce que ça laisse découvert, et il faut le dire : le chemin d'ERREUR de
	// `copieVerifiee` / `adopte` n'est pas exercé sur Windows. À couvrir
	// autrement le jour où on saura rendre une copie impossible là-bas
	// (fichier ouvert en exclusif, ACL) - c'est écrit dans spec-port-windows.md.
	if runtime.GOOS == "windows" {
		t.Skip("précondition = bit de permission Unix, inexistant sur Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignore les droits du dossier parent")
	}
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "mon-skill")
	parent := filepath.Join(dir, ".claude/skills")
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })

	res, err := adopte(dir, source)
	if err != nil {
		t.Fatalf("le contenu a été rangé, ça ne doit pas être une erreur : %v", err)
	}
	if res.Slug != "mon-skill" {
		t.Errorf("slug = %q", res.Slug)
	}
	if !strings.Contains(res.Avertissement, "retirer à la main") {
		t.Fatalf("avertissement = %q", res.Avertissement)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared/skills/mon-skill/SKILL.md")); err != nil {
		t.Errorf("contenu non rangé : %v", err)
	}
	// Le résidu est un dossier, pas un lien : ne rien promettre à son sujet.
	info, err := os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("résidu = %v, %v", info, err)
	}
}

// TestAdopteSlugVientDuDossier : le slug n'est plus un paramètre. Un slug qui
// diverge du nom de dossier poserait un deuxième lien, orphelin, que la boucle
// de retrait de projeterClaude ne verrait jamais.
func TestAdopteSlugVientDuDossier(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "foo")

	res, err := adopte(dir, source)
	if err != nil {
		t.Fatal(err)
	}
	if res.Slug != "foo" {
		t.Fatalf("slug = %q", res.Slug)
	}
	entrees, err := os.ReadDir(filepath.Join(dir, ".claude/skills"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entrees) != 1 || entrees[0].Name() != "foo" {
		t.Fatalf("une seule entrée attendue, obtenu %v", entrees)
	}
}

// TestAdopteRefuseCheminsImbriques : copier un dossier dans son propre
// sous-dossier part en récursion et remplit le disque.
func TestAdopteRefuseCheminsImbriques(t *testing.T) {
	dir := poseRacine(t)
	source := filepath.Join(dir, "shared")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := adopte(dir, source)
	if err == nil || !strings.Contains(err.Error(), "imbriqués") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// TestAdopteRefuseEspaceNonMonte : ranger un skill dans un espace non monté le
// rendrait invisible à la sync ET bloquerait le montage futur de cet espace
// (aligneMontage refuse un dossier local préexistant non vide).
func TestAdopteRefuseEspaceNonMonte(t *testing.T) {
	dir := t.TempDir() // racine sans état : aucun espace monté
	source := poseSkill(t, dir, ".claude/skills", "mon-skill")

	_, err := adopte(dir, source)
	if err == nil || !strings.Contains(err.Error(), "n'est pas monté") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
	if _, err := os.Stat(filepath.Join(dir, "shared")); !os.IsNotExist(err) {
		t.Error("aucun dossier ne doit être créé dans un espace non monté")
	}
}

// TestDeplaceRefuseUneDestinationExistante : la garde appartient à `deplace`, pas
// à l'appelant - POSIX autorise Rename à remplacer un répertoire vide (darwin
// renvoie EEXIST, Linux non), donc la laisser chez l'appelant la ferait dépendre
// du noyau.
func TestDeplaceRefuseUneDestinationExistante(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, "src", "s")
	dest := filepath.Join(dir, "shared/skills/s")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := deplace(source, dest); err == nil {
		t.Fatal("une destination existante doit être refusée")
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// ---- Tests issus du troisième passage adversarial. ----

// TestCandidatsRacineRelative : `vecu sync --dir ./vault` passe un chemin relatif
// jusqu'ici (main.go ne l'absolutise pas). Une source relative re-jointe sur une
// racine absolue donnait un chemin doublé, donc inexistant, donc « rien à
// adopter » en silence - la sortie exacte que ce fichier existe pour empêcher.
func TestCandidatsRacineRelative(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	t.Chdir(filepath.Dir(dir))

	adoptables, refuses := candidats(filepath.Base(dir))
	if !slices.Equal(adoptables, []string{"mon-skill"}) {
		t.Fatalf("adoptables = %v, refuses = %v", adoptables, refuses)
	}
}

// TestAdopteAliasDeChemin : /var et /private/var désignent le même dossier sur
// macOS. Une source jugée « hors racine » pour une différence d'orthographe
// basculerait le lien en absolu, que la projection signalerait à chaque cycle
// sans jamais l'adopter.
func TestAdopteAliasDeChemin(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	autreOrthographe := resolu(dir)
	if autreOrthographe == dir {
		t.Skip("pas d'alias de chemin sur ce système de fichiers")
	}

	if _, err := adopte(dir, filepath.Join(autreOrthographe, ".claude/skills/mon-skill")); err != nil {
		t.Fatalf("adoption refusée : %v", err)
	}
	if got := lienVers(t, dir, "mon-skill"); got != "../../shared/skills/mon-skill" {
		t.Fatalf("cible du lien = %q (la projection ne l'adopterait pas)", got)
	}
	projetes, laisses := projeterClaude(dir, []string{"mon-skill"}, nil)
	if len(laisses) != 0 || len(projetes) != 1 {
		t.Fatalf("projetes = %v, laisses = %v", projetes, laisses)
	}
}

// TestAdopteRefuseImbricationInsensibleALaCasse : sur APFS, <dir>/SHARED EST
// <dir>/shared. Une garde purement lexicale se ferait contourner par le cas le
// plus banal du disque de destination, et la copie partirait en récursion.
func TestAdopteRefuseImbricationInsensibleALaCasse(t *testing.T) {
	dir := poseRacine(t)
	source := filepath.Join(dir, "SHARED")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := adopte(dir, source)
	if err == nil {
		t.Fatal("adoption acceptée alors que la source contient la destination")
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// TestAdopteRefuseSourceDansEspaceMonte : le lien de remplacement atterrirait
// dans l'arbre synchronisé, où le scan le verrait comme une Laisse de genre
// fichier - donc `vecu sync` sortirait en erreur à chaque cycle, sur un fichier
// que Vécu aurait lui-même créé.
func TestAdopteRefuseSourceDansEspaceMonte(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, "shared/notes", "mon-skill")

	_, err := adopte(dir, source)
	if err == nil || !strings.Contains(err.Error(), "espace synchronisé") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// TestCandidatsRefuseDestinationImpossible : un `shared` qui est un fichier
// faisait annoncer le skill « adoptable », puis mourir sur le MkdirAll.
func TestCandidatsRefuseDestinationImpossible(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	if err := os.WriteFile(filepath.Join(dir, "shared"), []byte("pas un dossier"), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptables, refuses := candidats(dir)
	if len(adoptables) != 0 {
		t.Fatalf("adoptables = %v", adoptables)
	}
	if r := raisonDe(refuses, ".claude/skills/mon-skill"); !strings.Contains(r, "n'est pas un dossier") {
		t.Fatalf("raison = %q", r)
	}
}

// TestDestinationRefuseHomonymeDeCasse : `Daily` et `daily` cohabitent sur ext4
// et collisionnent sur le Mac du binôme. Le message diffère selon la sensibilité
// à la casse du disque de test ; ce qui compte est que ce soit un refus.
func TestDestinationRefuseHomonymeDeCasse(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, "shared/skills", "Daily")

	if raison := destinationAccueillante(dir, "daily"); raison == "" {
		t.Fatal("un homonyme de casse doit être refusé")
	}
}

// TestMemeArbreNeMentPasSurUnLien : WalkDir rend un lstat côté source (taille du
// lien) ; un Stat côté destination rendrait celle de sa cible, et la
// vérification qui autorise à supprimer la source crierait « tronqué » sur une
// copie parfaite.
func TestMemeArbreNeMentPasSurUnLien(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, "src", "s")
	dest := poseSkill(t, dir, "dst", "s")
	cible := filepath.Join(dir, "une-cible-au-contenu-bien-plus-long.md")
	if err := os.WriteFile(cible, []byte(strings.Repeat("x", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{source, dest} {
		if err := os.Symlink(cible, filepath.Join(d, "lien")); err != nil {
			t.Fatal(err)
		}
	}

	if err := memeArbre(source, dest); err != nil {
		t.Fatalf("copie correcte signalée comme incomplète : %v", err)
	}
}

// ---- Câblage moteur. ----

// moteurAdoption : un moteur minimal, sans client HTTP, suffisant pour Adopte()
// (qui ne parle qu'au disque et à l'état).
func moteurAdoption(t *testing.T, dir string) *Engine {
	t.Helper()
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{
		dir:         dir,
		state:       st,
		projections: []string{"claude"},
		logf:        func(string, ...any) {},
	}
}

// TestEngineAdopte : le geste complet, tel qu'il se produira au début d'un cycle.
// Un skill est rangé et tracé ; un skill refusé reste où il est et se lit dans
// l'état, sans jamais devenir une Laisse.
func TestEngineAdopte(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	poseSkill(t, dir, ".claude/skills", "synced")
	e := moteurAdoption(t, dir)

	if err := e.Adopte(); err != nil {
		t.Fatalf("Adopte : %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared/skills/mon-skill/SKILL.md")); err != nil {
		t.Fatalf("skill non rangé : %v", err)
	}
	if got := lienVers(t, dir, "mon-skill"); got != "../../shared/skills/mon-skill" {
		t.Errorf("cible du lien = %q", got)
	}
	if len(e.state.Adoptes) != 1 || e.state.Adoptes[0].Slug != "mon-skill" {
		t.Fatalf("adoptes = %+v", e.state.Adoptes)
	}
	if e.state.Adoptes[0].Source != ".claude/skills/mon-skill" {
		t.Errorf("source = %q", e.state.Adoptes[0].Source)
	}
	if e.state.Adoptes[0].Date == "" {
		t.Error("date manquante")
	}
	if len(e.state.Candidats) != 1 || !strings.Contains(e.state.Candidats[0].Raison, "réservé") {
		t.Fatalf("candidats = %+v", e.state.Candidats)
	}
	if len(e.state.Laisses) != 0 {
		t.Fatalf("un refus d'adoption ne doit jamais devenir une Laisse : %+v", e.state.Laisses)
	}
	// L'état est persisté : `vecu status` le lit sans moteur.
	relu, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(relu.Adoptes) != 1 || len(relu.Candidats) != 1 {
		t.Fatalf("état relu = %+v", relu)
	}
}

// TestEngineAdopteSansProjection : sans agent à servir sur ce poste, on ne touche
// pas à `.claude/`.
func TestEngineAdopteSansProjection(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	e := moteurAdoption(t, dir)
	e.projections = nil

	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude/skills/mon-skill/SKILL.md")); err != nil {
		t.Error("le skill a été déplacé alors qu'aucun agent n'est servi ici")
	}
	if len(e.state.Adoptes) != 0 {
		t.Errorf("adoptes = %+v", e.state.Adoptes)
	}
}

// TestEngineAdopteEtProjette : la couture complète. Le lien posé par l'adoption
// est adopté par la projection, donc le skill est géré dès le premier cycle.
func TestEngineAdopteEtProjette(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	e := moteurAdoption(t, dir)

	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	// Ce que SyncOnce aurait inscrit en poussant le skill.
	e.state.Files["shared/skills/mon-skill/SKILL.md"] = "empreinte"
	if err := e.Projette(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.state.Projetes, []string{"mon-skill"}) {
		t.Fatalf("projetes = %v, laisses = %+v", e.state.Projetes, e.state.Laisses)
	}
	if len(e.state.Laisses) != 0 {
		t.Fatalf("laisses = %+v", e.state.Laisses)
	}
}

// ---- Adoption manuelle (skills hors de la racine). ----

// TestAdoptionManuelleHorsRacine : le geste qui couvre ce que le cycle ne voit
// pas. Lien de remplacement absolu, et trace inscrite dans l'état.
func TestAdoptionManuelleHorsRacine(t *testing.T) {
	dir := poseRacine(t)
	maison := maisonDeTest(t)
	source := poseSkill(t, maison, ".claude/skills", "perso")

	res, sansTrace, err := AdoptionManuelle(dir, source)
	if err != nil {
		t.Fatalf("adoption refusée : %v", err)
	}
	if sansTrace != "" {
		t.Errorf("le verrou était libre : la trace devait être inscrite (%s)", sansTrace)
	}
	if res.Slug != "perso" || !filepath.IsAbs(res.Source) {
		t.Errorf("res = %+v", res)
	}
	dest, err := os.Readlink(source)
	if err != nil || dest != filepath.Join(dir, "shared/skills/perso") {
		t.Fatalf("lien = %q, %v", dest, err)
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Adoptes) != 1 || st.Adoptes[0].Slug != "perso" {
		t.Fatalf("adoptes = %+v", st.Adoptes)
	}
}

// TestAdoptionManuelleServiceEnCours : le service tient le verrou. Le rangement
// se fait quand même - il est sûr et le cycle suivant poussera le skill - mais
// la trace n'est pas écrite, parce que deux processus qui écrivent state.json se
// perdent en last-writer-wins. Faire dépendre le rangement de l'arrêt du service
// serait payer une trace au prix du service rendu.
func TestAdoptionManuelleServiceEnCours(t *testing.T) {
	dir := poseRacine(t)
	maison := maisonDeTest(t)
	source := poseSkill(t, maison, ".claude/skills", "perso")
	verrou, err := acquireLock(dir) // simule le daemon
	if err != nil {
		t.Fatal(err)
	}
	defer verrou.Close()

	res, sansTrace, err := AdoptionManuelle(dir, source)
	if err != nil {
		t.Fatalf("adoption refusée alors que le déplacement est sûr : %v", err)
	}
	if !strings.Contains(sansTrace, "tient ce dossier") {
		t.Errorf("la cause annoncée doit être le verrou, pas autre chose : %q", sansTrace)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared/skills/perso/SKILL.md")); err != nil {
		t.Errorf("skill non rangé : %v", err)
	}
	_ = res
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Adoptes) != 0 {
		t.Errorf("état réécrit malgré le verrou : %+v", st.Adoptes)
	}
}

// TestAdoptionManuelleRefuseUnCheminQuiPasseParUnLien : le cas réel est un
// `~/.claude` géré par un dépôt de dotfiles. Vécu ne sort pas du contenu d'un
// dépôt tiers sur la foi d'un chemin qui ne dit pas où il mène ; le refus NOMME
// le chemin réel, pour que relancer dessus soit un accord explicite.
func TestAdoptionManuelleRefuseUnCheminQuiPasseParUnLien(t *testing.T) {
	dir := poseRacine(t)
	maison := maisonDeTest(t)
	t.Setenv("HOME", maison)
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", maison)
	t.Setenv("AppData", filepath.Join(maison, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(maison, "AppData", "Local"))
	dotfiles := filepath.Join(maison, "dotfiles", "claude")
	source := poseSkill(t, dotfiles, "skills", "perso")
	if err := os.Symlink(dotfiles, filepath.Join(maison, ".claude")); err != nil {
		t.Fatal(err)
	}

	_, _, err := AdoptionManuelle(dir, filepath.Join(maison, ".claude/skills/perso"))
	if err == nil || !strings.Contains(err.Error(), "lien symbolique") {
		t.Fatalf("err = %v", err)
	}
	// Le message doit nommer le chemin EXACT à relancer, pas son dossier parent :
	// « relancer sur ce chemin-là » suivi d'un dossier qui n'est pas un skill
	// envoie droit dans « SKILL.md absent ».
	if !strings.Contains(err.Error(), source) {
		t.Errorf("le refus doit nommer le chemin exact à relancer (%s) : %v", source, err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
	// Et sur le chemin réel, ça passe : le refus est une demande d'explicitation,
	// pas une interdiction.
	if _, _, err := AdoptionManuelle(dir, source); err != nil {
		t.Fatalf("le chemin réel doit être accepté : %v", err)
	}
}

// TestCandidatsPersonnels : lecture seule du `~/.claude/skills/`. Rien n'en part
// tout seul, et les installs tierces (liens, verrouillées) n'y font pas de bruit.
func TestCandidatsPersonnels(t *testing.T) {
	dir := poseRacine(t)
	maison := maisonDeTest(t)
	t.Setenv("HOME", maison)
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", maison)
	t.Setenv("AppData", filepath.Join(maison, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(maison, "AppData", "Local"))
	poseSkill(t, maison, ".claude/skills", "perso")
	poseSkill(t, maison, ".claude/skills", "verrouille")
	lock := `{"skills":{"verrouille":{"source":"x/y"}}}`
	if err := os.WriteFile(filepath.Join(maison, "skills-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/ailleurs", filepath.Join(maison, ".claude/skills/lien")); err != nil {
		t.Fatal(err)
	}

	prets := CandidatsPersonnels(dir)
	if len(prets) != 1 || filepath.Base(prets[0]) != "perso" {
		t.Fatalf("candidats = %v", prets)
	}
	// Lecture seule : rien n'a bougé.
	if _, err := os.Stat(filepath.Join(maison, ".claude/skills/perso/SKILL.md")); err != nil {
		t.Error("un candidat personnel a été déplacé")
	}
}

// ---- Tests issus du passage adversarial sur le câblage. ----

// TestCandidatsRefusBinaire : tant que le skill vit dans `.claude/`, rien ne le
// scanne. Rangé dans `shared/`, un binaire devient une Laisse de genre fichier à
// CHAQUE cycle, donc `vecu sync` en code d'erreur pour toujours - à cause d'un
// geste que personne n'a demandé. Les skills embarquent couramment des assets.
func TestCandidatsRefusBinaire(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "avec-image")
	if err := os.WriteFile(filepath.Join(source, "logo.png"), []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}

	adoptables, refuses := candidats(dir)
	if len(adoptables) != 0 {
		t.Fatalf("adoptables = %v", adoptables)
	}
	if r := raisonDe(refuses, ".claude/skills/avec-image"); !strings.Contains(r, "binaire") {
		t.Fatalf("raison = %q", r)
	}
	if _, err := os.Stat(filepath.Join(source, "logo.png")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// TestCandidatsRefusContenuIgnore : un fichier du skill écarté par le
// .vecuignore part en silence (les fichiers ignorés sont muets par
// construction). Le skill arriverait amputé chez les autres, sans que rien ne le
// dise.
func TestCandidatsRefusContenuIgnore(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "guide")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "references/api.md"), []byte("annexe"), 0o644); err != nil {
		t.Fatal(err)
	}
	poseIgnore(t, dir, "references")

	adoptables, refuses := candidats(dir)
	if len(adoptables) != 0 {
		t.Fatalf("adoptables = %v", adoptables)
	}
	if r := raisonDe(refuses, ".claude/skills/guide"); !strings.Contains(r, "amputé") {
		t.Fatalf("raison = %q", r)
	}
}

// TestEngineAdopteRacineRelative : le câblage passait `e.dir` brut, donc une
// source relative re-jointe sur une racine absolue - chemin doublé, rien
// d'adopté, et un motif faux affiché dans `status`.
func TestEngineAdopteRacineRelative(t *testing.T) {
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	t.Chdir(filepath.Dir(dir))
	e := moteurAdoption(t, filepath.Base(dir))

	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	if len(e.state.Adoptes) != 1 {
		t.Fatalf("adoptes = %+v, candidats = %+v", e.state.Adoptes, e.state.Candidats)
	}
}

// TestEngineAdopteAvertissementVoyageAvecLAdoption : un skill rangé mais dont le
// résidu bloque le lien ne doit pas sortir à la fois sous « adoptés » et sous
// « non adoptés », avec un pied de page affirmant qu'il n'a pas bougé. Et la
// consigne doit survivre au cycle suivant.
func TestEngineAdopteAvertissementVoyageAvecLAdoption(t *testing.T) {
	// Précondition : un dossier parent en lecture seule (0555) pour faire échouer
	// la pose du lien et vérifier que l'avertissement voyage avec l'adoption.
	// Windows ne porte pas ce bit - le lien se pose, il n'y a pas d'avertissement,
	// et le test vérifierait le contraire de ce qu'il annonce. Même raison que le
	// saut « root » ci-dessus, autre système.
	if runtime.GOOS == "windows" {
		t.Skip("précondition = dossier parent en lecture seule Unix, sans effet sur Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignore les droits du dossier parent")
	}
	dir := poseRacine(t)
	poseSkill(t, dir, ".claude/skills", "mon-skill")
	parent := filepath.Join(dir, ".claude/skills")
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	e := moteurAdoption(t, dir)

	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	if len(e.state.Adoptes) != 1 || e.state.Adoptes[0].Avertissement == "" {
		t.Fatalf("adoptes = %+v", e.state.Adoptes)
	}
	for _, c := range e.state.Candidats {
		if strings.Contains(c.Chemin, "mon-skill") {
			t.Errorf("le skill sort aussi en refus : %+v", c)
		}
	}
}

// TestEngineAdopteVideLesCandidatsSansProjection : sans projection, les refus du
// dernier cycle actif resteraient affichés indéfiniment, sur un dossier qu'on ne
// regarde plus.
func TestEngineAdopteVideLesCandidatsSansProjection(t *testing.T) {
	dir := poseRacine(t)
	e := moteurAdoption(t, dir)
	e.state.Candidats = []Candidat{{Chemin: ".claude/skills/vieux", Raison: "d'un cycle précédent"}}
	e.projections = nil

	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	if len(e.state.Candidats) != 0 {
		t.Fatalf("candidats = %+v", e.state.Candidats)
	}
}

// TestEngineAdopteNEcritPasPourRien : le daemon appelle Adopte à chaque cycle.
// Réécrire state.json alors que rien n'a changé, c'est quatre écritures par
// minute au repos.
func TestEngineAdopteNEcritPasPourRien(t *testing.T) {
	dir := poseRacine(t)
	e := moteurAdoption(t, dir)
	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	avant, err := os.Stat(filepath.Join(dir, ".vecu", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	apres, err := os.Stat(filepath.Join(dir, ".vecu", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !apres.ModTime().Equal(avant.ModTime()) {
		t.Error("state.json réécrit alors qu'il n'y avait rien à adopter")
	}
}

// TestEngineRecense : quand le cycle n'aboutit pas, on n'adopte pas - mais on
// dit ce qui attend. Un skill prêt à partir et dont personne ne parle est la
// situation que toute cette feature existe pour supprimer.
func TestEngineRecense(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "mon-skill")
	e := moteurAdoption(t, dir)

	if err := e.Recense(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Fatal("le recensement ne doit rien déplacer")
	}
	if len(e.state.Adoptes) != 0 {
		t.Fatalf("adoptes = %+v", e.state.Adoptes)
	}
	// Recense écrit sur le disque et ne touche PAS l'état en mémoire : il est
	// appelé sur le chemin d'erreur d'un cycle, où e.state peut porter un demi-cycle.
	if len(e.state.Candidats) != 0 {
		t.Errorf("l'état en mémoire ne doit pas être touché : %+v", e.state.Candidats)
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Candidats) != 1 || !strings.Contains(st.Candidats[0].Raison, "en attente") {
		t.Fatalf("candidats sur disque = %+v", st.Candidats)
	}
}

// ---- Tests issus du passage adversarial sur la tranche 3. ----

// TestCandidatsAccepteLesDefautsIgnores : régression introduite par la garde sur
// le contenu. Le Finder pose un `.DS_Store` rien qu'en ouvrant un dossier, et un
// skill installé depuis git porte un `.git/`. Les refuser rendait tout skill
// ordinaire inadoptable À VIE sur un Mac, avec un message qui envoyait greper un
// `.vecuignore` vide.
func TestCandidatsAccepteLesDefautsIgnores(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "normal")
	if err := os.WriteFile(filepath.Join(source, ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git/config"), []byte("[core]"), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptables, refuses := candidats(dir)
	if !slices.Equal(adoptables, []string{"normal"}) {
		t.Fatalf("adoptables = %v, refuses = %+v", adoptables, refuses)
	}
}

// TestRefusDuCheminSansHome : la garde ne doit pas dépendre de HOME. launchd,
// systemd, cron et `env -i` n'exportent pas HOME, et le défaut sûr est le refus -
// pas le passage, qui sortirait du contenu d'un dépôt tiers sans un mot.
func TestRefusDuCheminSansHome(t *testing.T) {
	dir := poseRacine(t)
	maison := maisonDeTest(t)
	t.Setenv("HOME", "")
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", "")
	dotfiles := filepath.Join(maison, "dotfiles", "claude")
	source := poseSkill(t, dotfiles, "skills", "perso")
	if err := os.Symlink(dotfiles, filepath.Join(maison, ".claude")); err != nil {
		t.Fatal(err)
	}

	_, _, err := AdoptionManuelle(dir, filepath.Join(maison, ".claude/skills/perso"))
	if err == nil || !strings.Contains(err.Error(), "lien symbolique") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// TestRefusDuCheminHorsDossierPersonnel : un dépôt de dotfiles d'équipe dans
// /opt ou sur un volume monté n'est pas sous le dossier personnel. L'ancienne
// garde s'y taisait, donc l'asymétrie allait dans le sens destructif.
func TestRefusDuCheminHorsDossierPersonnel(t *testing.T) {
	dir := poseRacine(t)
	ailleurs := maisonDeTest(t)
	t.Setenv("HOME", maisonDeTest(t))
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", maisonDeTest(t)) // un home valide, mais qui ne contient pas la source
	depot := filepath.Join(ailleurs, "depot-equipe", "claude")
	source := poseSkill(t, depot, "skills", "perso")
	if err := os.Symlink(depot, filepath.Join(ailleurs, "claude-lien")); err != nil {
		t.Fatal(err)
	}

	_, _, err := AdoptionManuelle(dir, filepath.Join(ailleurs, "claude-lien/skills/perso"))
	if err == nil || !strings.Contains(err.Error(), "lien symbolique") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Error("la source doit rester intacte")
	}
}

// TestEngineAdopteRafraichitLaRaison : la raison est le contenu utile du refus.
// Comparer les seuls chemins figeait le premier motif constaté, donc `status`
// nommait un fichier supprimé depuis et taisait celui qui bloquait vraiment.
func TestEngineAdopteRafraichitLaRaison(t *testing.T) {
	dir := poseRacine(t)
	source := poseSkill(t, dir, ".claude/skills", "guide")
	if err := os.WriteFile(filepath.Join(source, "logo.png"), []byte{0x00, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	e := moteurAdoption(t, dir)
	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.state.Candidats[0].Raison, "binaire") {
		t.Fatalf("raison initiale = %q", e.state.Candidats[0].Raison)
	}

	// Le binaire part, un motif utilisateur prend le relais : même chemin, autre cause.
	if err := os.Remove(filepath.Join(source, "logo.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	poseIgnore(t, dir, "*.txt")
	if err := e.Adopte(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.state.Candidats[0].Raison, "binaire") {
		t.Fatalf("raison périmée conservée : %q", e.state.Candidats[0].Raison)
	}
	if !strings.Contains(e.state.Candidats[0].Raison, "notes.txt") {
		t.Fatalf("raison = %q", e.state.Candidats[0].Raison)
	}
}
