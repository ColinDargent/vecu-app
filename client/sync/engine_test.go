package sync

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// testServer démarre un vrai serveur Vécu et renvoie son URL + un token
// d'écriture pour un utilisateur `colin` (écriture partout).
func testServer(t *testing.T) (url string, token string) {
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
	tok, _ := database.IssueToken(u.ID, "test")

	// L'espace doit exister CÔTÉ SERVEUR pour qu'un client le monte : un dossier
	// créé localement dans la racine n'est jamais poussé. C'est volontaire et
	// c'est la protection principale du poste - la racine locale est le dossier
	// de travail de l'utilisateur, Vécu ne touche qu'aux espaces qu'il connaît.
	if _, err := store.Write("equipe/README.md", "# equipe\n", "colin"); err != nil {
		t.Fatal(err)
	}

	fixed := time.Date(2026, 7, 23, 17, 30, 0, 0, time.UTC)
	srv := httptest.NewServer((&api.Server{DB: database, Store: store, Now: func() time.Time { return fixed }}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, tok
}

// newEngine prépare un dossier local configuré vers le serveur.
func newEngine(t *testing.T, url, token string) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "colin", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Les tests déposent des fichiers dans « equipe » avant le premier cycle :
	// c'est le scénario d'import, qui exige une adoption explicite.
	e.Importer([]string{"equipe"})
	return e, dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func TestSyncPushThenPullBetweenTwoFolders(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	// A crée un fichier, pousse.
	writeFile(t, dirA, "equipe/notes/idee.md", "une idée\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("A push : %v", err)
	}
	// B synchronise : il doit recevoir le fichier de A.
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("B pull : %v", err)
	}
	if got, ok := readFile(t, dirB, "equipe/notes/idee.md"); !ok || got != "une idée\n" {
		t.Errorf("B n'a pas reçu le fichier de A : %q %v", got, ok)
	}
}

func TestSyncDeletePropagates(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/tmp.md", "jetable\n")
	a.SyncOnce()
	b.SyncOnce()
	if _, ok := readFile(t, dirB, "equipe/tmp.md"); !ok {
		t.Fatal("B devrait avoir tmp.md")
	}
	// A supprime, pousse ; B doit voir la suppression.
	os.Remove(filepath.Join(dirA, "equipe/tmp.md"))
	a.SyncOnce()
	b.SyncOnce()
	if _, ok := readFile(t, dirB, "equipe/tmp.md"); ok {
		t.Error("B devrait avoir supprimé tmp.md")
	}
}

func TestSyncConflictVisibleBothSides(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	// État partagé : un fichier synchronisé des deux côtés.
	writeFile(t, dirA, "equipe/plan.md", "ligne\n")
	a.SyncOnce()
	b.SyncOnce()
	if _, ok := readFile(t, dirB, "equipe/plan.md"); !ok {
		t.Fatal("prérequis : B devrait avoir plan.md")
	}

	// Les deux éditent la même ligne à partir de la même base, sans se resync.
	writeFile(t, dirA, "equipe/plan.md", "version A\n")
	writeFile(t, dirB, "equipe/plan.md", "version B\n")

	// A pousse en premier (gagne main), puis B pousse (conflit) et resync.
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("A : %v", err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("B : %v", err)
	}
	// A resync pour recevoir la copie de conflit.
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("A resync : %v", err)
	}

	conflictName := "equipe/plan (conflit 2026-07-23 17h30 - colin).md"
	// main garde la version de A ; la copie porte la version de B.
	for _, dir := range []string{dirA, dirB} {
		if got, ok := readFile(t, dir, "equipe/plan.md"); !ok || got != "version A\n" {
			t.Errorf("%s : plan.md devrait valoir version A, obtenu %q", dir, got)
		}
		if got, ok := readFile(t, dir, conflictName); !ok || got != "version B\n" {
			t.Errorf("%s : copie de conflit manquante ou incorrecte : %q %v", dir, got, ok)
		}
	}
}

func TestPullPreservesUnpushedLocalEdit(t *testing.T) {
	// Reproduit la course : édition locale arrivée après le scan de push, alors
	// que le serveur a une autre version. Le pull ne doit PAS l'écraser sans trace.
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "origine\n")
	a.SyncOnce()
	b.SyncOnce()

	// A pousse une nouvelle version (serveur = "serveur A").
	writeFile(t, dirA, "equipe/plan.md", "serveur A\n")
	a.SyncOnce()

	// B a une édition locale JAMAIS poussée (simule l'arrivée post-scan), puis
	// syncte : le pull apporte "serveur A" mais l'édition locale doit survivre.
	writeFile(t, dirB, "equipe/plan.md", "édition locale de B\n")
	// On force l'état connu de B à croire que plan.md est encore "origine" (ce
	// que le scan de push aurait vu si l'édition était arrivée juste après).
	b.state.Files["equipe/plan.md"] = hashContent([]byte("origine\n"))
	if _, err := b.pull(b.state.Head, nil, nil); err != nil {
		t.Fatalf("pull B : %v", err)
	}

	if got, _ := readFile(t, dirB, "equipe/plan.md"); got != "serveur A\n" {
		t.Errorf("plan.md devrait porter la version serveur, obtenu %q", got)
	}
	// L'édition locale doit être quelque part (copie de conflit local), pas perdue.
	found := false
	entries, _ := os.ReadDir(filepath.Join(dirB, "equipe"))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if b, ok := readFile(t, dirB, "equipe/"+e.Name()); ok && b == "édition locale de B\n" {
			found = true
		}
	}
	if !found {
		t.Error("édition locale de B perdue : aucune copie ne la contient")
	}
}

func TestBinaryFileRefusedNotCorrupted(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)

	// Contenu binaire (octets non-UTF-8).
	bin := []byte{0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe, 0x00, 0x80}
	if err := os.MkdirAll(filepath.Join(dirA, "equipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "equipe/image.png"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/note.md", "texte ok\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("sync : %v", err)
	}
	// Le binaire n'est pas suivi (refusé), mais reste intact sur disque.
	if _, ok := a.state.Files["equipe/image.png"]; ok {
		t.Error("le binaire n'aurait pas dû être suivi")
	}
	got, _ := os.ReadFile(filepath.Join(dirA, "equipe/image.png"))
	if string(got) != string(bin) {
		t.Error("le fichier binaire local a été altéré")
	}
	// Le fichier texte, lui, se synchronise.
	if _, ok := a.state.Files["equipe/note.md"]; !ok {
		t.Error("note.md devrait être synchronisé")
	}
}

func TestReconcileRemovesOutOfPerimeter(t *testing.T) {
	// Utilisateur restreint : voit "public", pas "prive". Un fichier passé hors
	// périmètre doit disparaître localement au cycle de réconciliation.
	dir := t.TempDir()
	database, _ := db.Open(filepath.Join(dir, "app.db"))
	t.Cleanup(func() { database.Close() })
	store, _ := storage.Init(filepath.Join(dir, "brain.git"))
	store.Write("public/a.md", "x", "admin")
	store.Write("public/b.md", "y", "admin")
	u, _ := database.CreateUser("membre", "pw", perms.Invisible, false)
	database.SetPermission(u.ID, "public", perms.Lecture)
	database.SetPermission(u.ID, "public/b.md", perms.Lecture)
	tok, _ := database.IssueToken(u.ID, "d")
	srv := httptest.NewServer((&api.Server{DB: database, Store: store}).Handler())
	t.Cleanup(srv.Close)

	e, ldir := newEngine(t, srv.URL, tok)
	e.SyncOnce()
	if _, ok := readFile(t, ldir, "public/b.md"); !ok {
		t.Fatal("membre devrait avoir public/b.md")
	}
	// Révocation : b.md devient invisible.
	database.SetPermission(u.ID, "public/b.md", perms.Invisible)
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("resync : %v", err)
	}
	if _, ok := readFile(t, ldir, "public/b.md"); ok {
		t.Error("public/b.md devrait avoir été retiré après révocation")
	}
	if _, ok := readFile(t, ldir, "public/a.md"); !ok {
		t.Error("public/a.md (toujours visible) ne devrait pas être retiré")
	}
}

func TestVerrouUnSeulProcessusParDossier(t *testing.T) {
	// Deux moteurs sur le même dossier écraseraient .vecu/state.json en
	// last-writer-wins (daemon + vecu sync) : le second doit être refusé.
	// NB : flock est par description de fichier, le conflit vaut aussi
	// à l'intérieur d'un même processus.
	url, token := testServer(t)
	_, dir := newEngine(t, url, token)
	if _, err := NewEngine(dir, func(string, ...any) {}); err == nil {
		t.Error("deuxième moteur sur le même dossier : erreur attendue")
	}
}

// TestBinaireHorsPerimetreNeComptePasCommePerdu : un binaire n'est pas un
// fichier perdu, c'est un fichier hors périmètre.
//
// Ce test portait le contrat inverse jusqu'au 20/08 (`Skipped() == 1`). Les 298
// binaires du vault de Colin faisaient donc sortir `vecu sync` en erreur à
// CHAQUE exécution, et allumaient en permanence l'avertissement de la barre de
// menus. Un signal qui est toujours rouge n'est plus un signal : on désapprend
// à le lire, et on ne voit plus le jour où il dit vrai. Le genre distingue
// « rien ne peut être tenté » de « du contenu est en danger ».
func TestBinaireHorsPerimetreNeComptePasCommePerdu(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/note.md", "texte ok")
	if err := os.WriteFile(filepath.Join(dir, "equipe/image.png"), []byte{0xff, 0xd8, 0x00, 0x9f}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("SyncOnce : %v", err)
	}
	if e.Skipped() != 0 {
		t.Errorf("un binaire ne signale aucune perte : attendu 0, obtenu %d (%+v)", e.Skipped(), e.Laisses())
	}
	// Signalé quand même : le silence est le vrai danger d'un outil de sync.
	// Mais sous un genre qui dit ce qui est en jeu.
	hors := 0
	for _, l := range e.Laisses() {
		if l.Genre == GenreHorsPerimetre {
			hors++
		}
		if l.Perdu() {
			t.Errorf("aucune laisse ne doit être « perdue » ici : %+v", l)
		}
	}
	if hors != 1 {
		t.Errorf("attendu 1 laisse hors périmètre, obtenu %d (%+v)", hors, e.Laisses())
	}
	// Le binaire est toujours là au cycle suivant, et toujours pas une perte.
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("SyncOnce 2 : %v", err)
	}
	if e.Skipped() != 0 {
		t.Errorf("binaire toujours présent : attendu 0, obtenu %d", e.Skipped())
	}
	// Le fichier reste INTACT sur le disque, dans tous les cas.
	if _, err := os.Stat(filepath.Join(dir, "equipe/image.png")); err != nil {
		t.Errorf("Vécu ne supprime rien : %v", err)
	}
}

// serveurAvecMembre : serveur + un compte membre sans aucun accès au départ.
// Les droits se changent en cours de test, comme le ferait un clic dans la
// matrice de l'interface.
func serveurAvecMembre(t *testing.T) (url, token string, database *db.DB, membreID int64) {
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
	for _, f := range []struct{ p, c string }{
		{"shared/note.md", "partagé\n"},
		{"shared/autre.md", "encore\n"},
		{"prive/secret.md", "confidentiel\n"},
	} {
		if _, err := store.Write(f.p, f.c, "colin"); err != nil {
			t.Fatal(err)
		}
	}
	membre, err := database.CreateUser("achille", "pw", perms.Invisible, false)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := database.IssueToken(membre.ID, "test")
	srv := httptest.NewServer((&api.Server{DB: database, Store: store}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, tok, database, membre.ID
}

// TestMontageAutomatique : la promesse centrale de la v2. Un accès accordé
// depuis l'interface fait apparaître le dossier sur la machine, sans qu'aucune
// commande soit tapée ici.
func TestMontageAutomatique(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	e, dir := newEngine(t, url, token)

	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle sans accès : %v", err)
	}
	if _, ok := readFile(t, dir, "shared/note.md"); ok {
		t.Fatal("un fichier est descendu sans aucun accès")
	}

	// Le clic dans l'interface.
	if err := database.SetPermission(membreID, "shared", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle après accès : %v", err)
	}
	if got, ok := readFile(t, dir, "shared/note.md"); !ok || got != "partagé\n" {
		t.Errorf("l'espace accordé n'est pas descendu : %q %v", got, ok)
	}
	// Et rien d'autre : l'espace non accordé n'existe nulle part.
	if _, ok := readFile(t, dir, "prive/secret.md"); ok {
		t.Error("un espace hors périmètre est descendu sur le disque")
	}
	if _, err := os.Stat(filepath.Join(dir, "prive")); !os.IsNotExist(err) {
		t.Error("le dossier d'un espace hors périmètre a été créé")
	}
}

// TestDemontagePreserveEditionLocale : un accès retiré fait disparaître le
// dossier, mais ne détruit JAMAIS un contenu que le serveur n'a pas.
func TestDemontagePreserveEditionLocale(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	e, dir := newEngine(t, url, token)
	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	if _, ok := readFile(t, dir, "shared/note.md"); !ok {
		t.Fatal("prérequis : l'espace devrait être monté")
	}

	// Une édition jamais poussée, puis l'accès est retiré dans l'interface.
	writeFile(t, dir, "shared/autre.md", "travail non poussé\n")
	if err := database.SetPermission(membreID, "shared", perms.Invisible); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle après retrait : %v", err)
	}

	// Le fichier propre est parti, l'édition locale est restée.
	if _, ok := readFile(t, dir, "shared/note.md"); ok {
		t.Error("un fichier propre n'a pas été retiré après révocation")
	}
	if got, ok := readFile(t, dir, "shared/autre.md"); !ok || got != "travail non poussé\n" {
		t.Errorf("édition locale non poussée détruite par la révocation : %q %v", got, ok)
	}
	if len(e.state.Espaces) != 0 {
		t.Errorf("espaces montés après révocation : %v", e.state.Espaces)
	}
}

// TestRacineLocaleIntacte : la racine est le dossier de travail de
// l'utilisateur. Tout ce qui n'est pas un espace monté doit rester strictement
// invisible pour Vécu - ni lu, ni poussé, ni supprimé.
func TestRacineLocaleIntacte(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	e, dir := newEngine(t, url, token)
	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}

	// Des dossiers personnels dans la racine, dont un homonyme d'espace serveur.
	writeFile(t, dir, "code/main.go", "package main\n")
	writeFile(t, dir, "prive/mes-notes.md", "à moi seul\n")
	writeFile(t, dir, "journal.md", "à la racine\n")

	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}

	// Intacts sur le disque.
	for _, p := range []string{"code/main.go", "prive/mes-notes.md", "journal.md"} {
		if _, ok := readFile(t, dir, p); !ok {
			t.Errorf("%s a disparu de la racine locale", p)
		}
	}
	// Et jamais poussés : ils n'apparaissent pas dans le périmètre serveur.
	paths, err := e.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "code/") || p == "journal.md" {
			t.Errorf("un chemin de la racine locale a été poussé : %s", p)
		}
	}
	// « prive » existe côté serveur hors du périmètre du membre : le dossier
	// local du même nom ne doit pas être confondu avec lui.
	if got, _ := readFile(t, dir, "prive/mes-notes.md"); got != "à moi seul\n" {
		t.Errorf("le dossier local homonyme d'un espace serveur a été touché")
	}
	if _, suivi := e.state.Files["prive/mes-notes.md"]; suivi {
		t.Error("un fichier hors espace monté est entré dans l'état de sync")
	}
}

// TestVecuIgnore : les motifs du .vecuignore protègent la racine des dossiers
// qui n'ont rien à faire dans un second cerveau partagé.
func TestVecuIgnore(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)

	writeFile(t, dir, ".vecuignore", "# dépendances et images\nnode_modules\n*.png\nequipe/brouillons\n")
	writeFile(t, dir, "equipe/note.md", "gardé\n")
	writeFile(t, dir, "equipe/node_modules/lib/index.js", "du code\n")
	writeFile(t, dir, "equipe/sous/node_modules/autre.js", "encore\n")
	// Contenu textuel : sans le motif, ce fichier partirait (ce n'est pas la
	// règle des binaires qui le retient).
	writeFile(t, dir, "equipe/image.png", "en fait du texte\n")
	writeFile(t, dir, "equipe/brouillons/x.md", "brouillon\n")

	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	paths, err := e.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	vus := map[string]bool{}
	for _, p := range paths {
		vus[p] = true
	}
	if !vus["equipe/note.md"] {
		t.Errorf("le fichier ordinaire n'a pas été poussé : %v", paths)
	}
	for _, interdit := range []string{
		"equipe/node_modules/lib/index.js",
		"equipe/sous/node_modules/autre.js",
		"equipe/image.png",
		"equipe/brouillons/x.md",
	} {
		if vus[interdit] {
			t.Errorf("chemin ignoré quand même poussé : %s", interdit)
		}
	}
	// Le .vecuignore lui-même vit à la racine, hors de tout espace : il ne part
	// jamais, et n'a donc pas à être exclu explicitement.
	if vus[".vecuignore"] {
		t.Error(".vecuignore a été poussé")
	}
}

// TestEspaceNAdoptePasUnDossierLocal : le montage automatique ne doit jamais
// publier un dossier personnel qui portait déjà le nom d'un espace. Une racine
// locale contient des dossiers aux noms génériques (« clients », « projets ») ;
// sans cette garde, le premier accès accordé à un espace homonyme enverrait
// leur contenu sur le serveur.
func TestEspaceNAdoptePasUnDossierLocal(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Un dossier personnel antérieur, du même nom que l'espace du serveur.
	writeFile(t, dir, "shared/impots-2025.md", "strictement personnel\n")
	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}

	// L'espace n'est pas monté, et rien n'est parti.
	if len(e.state.Espaces) != 0 {
		t.Errorf("espace monté malgré un dossier local préexistant : %v", e.state.Espaces)
	}
	paths, err := e.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if p == "shared/impots-2025.md" {
			t.Fatal("un fichier personnel a été publié sur le serveur")
		}
	}
	// Le fichier personnel est intact, et le motif est lisible.
	if got, ok := readFile(t, dir, "shared/impots-2025.md"); !ok || got != "strictement personnel\n" {
		t.Errorf("le fichier personnel a été touché : %q %v", got, ok)
	}
	if len(e.Laisses()) == 0 {
		t.Error("aucun motif remonté à l'utilisateur")
	}

	// Adoption explicite : là, et seulement là, le contenu part.
	e.Importer([]string{"shared"})
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle avec adoption : %v", err)
	}
	paths, _ = e.client.Tree()
	trouve := false
	for _, p := range paths {
		if p == "shared/impots-2025.md" {
			trouve = true
		}
	}
	if !trouve {
		t.Errorf("l'adoption explicite n'a pas envoyé le contenu : %v", paths)
	}
}

// TestDossierEspaceSupprimeNeVidePasLeServeur : un dossier d'espace renommé,
// déplacé ou jeté à la corbeille depuis le Finder ne doit pas se traduire par
// la suppression de son contenu pour toute l'équipe.
func TestDossierEspaceSupprimeNeVidePasLeServeur(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/note.md", "important\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	avant, _ := a.client.Tree()
	if len(avant) < 2 {
		t.Fatalf("prérequis : le dépôt devrait contenir des fichiers, %v", avant)
	}

	// Le geste de rangement malheureux.
	if err := os.RemoveAll(filepath.Join(dirA, "equipe")); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle après suppression : %v", err)
	}

	apres, err := a.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(apres) != len(avant) {
		t.Errorf("le dépôt partagé a été vidé par une suppression locale : %v -> %v", avant, apres)
	}
	if len(a.Laisses()) == 0 {
		t.Error("la disparition du dossier n'a pas été signalée")
	}
}

// TestScanDistingueVuDeSur : le scan doit rendre la CERTITUDE de ce qu'il a vu,
// pas seulement la liste des fichiers. Un dossier lu à moitié ne doit jamais
// avoir l'aspect d'un dossier lu en entier - c'est de cette confusion que
// naissent toutes les suppressions abusives.
func TestScanDistingueVuDeSur(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignore les droits : le dossier illisible serait lu quand même")
	}
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}

	writeFile(t, dir, ".vecuignore", "node_modules\n")
	writeFile(t, dir, "equipe/note.md", "ordinaire\n")
	writeFile(t, dir, "equipe/node_modules/lib.js", "ignoré\n")
	writeFile(t, dir, "equipe/secret/dedans.md", "illisible\n")
	dehors := t.TempDir()
	if err := os.Symlink(dehors, filepath.Join(dir, "equipe/ailleurs")); err != nil {
		t.Fatal(err)
	}
	illisible := filepath.Join(dir, "equipe/secret")
	if err := os.Chmod(illisible, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(illisible, 0o755) })

	motifs, err := LoadIgnores(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.motifs = motifs

	s, err := e.scanLocal()
	if err != nil {
		t.Fatalf("scanLocal : %v", err)
	}

	if _, ok := s.fichiers["equipe/note.md"]; !ok {
		t.Errorf("le fichier ordinaire n'est pas dans le scan : %v", s.fichiers)
	}
	if !s.dossiersSurs["equipe"] {
		t.Error("la racine de l'espace, lisible, devrait être sûre")
	}
	// Le cœur du slice : lu à moitié, donc VU mais pas SÛR.
	if s.dossiersSurs["equipe/secret"] {
		t.Error("un dossier dont le ReadDir a échoué a été déclaré sûr")
	}
	if !s.dossiersVus["equipe/secret"] {
		t.Error("le dossier illisible devrait être marqué vu")
	}
	// Un dossier ignoré n'est pas parcouru : on ne sait rien de son contenu.
	if s.dossiersSurs["equipe/node_modules"] {
		t.Error("un dossier ignoré, donc non parcouru, a été déclaré sûr")
	}
	if !s.dossiersVus["equipe/node_modules"] {
		t.Error("le dossier ignoré devrait être marqué vu")
	}
	// Le lien est sur le disque mais hors de `fichiers` : sans `incertains`, il
	// passerait pour supprimé au cycle suivant.
	if !s.incertains["equipe/ailleurs"] {
		t.Error("le lien symbolique devrait être marqué incertain")
	}
	if _, ok := s.fichiers["equipe/ailleurs"]; ok {
		t.Error("le lien symbolique n'aurait pas dû être hashé")
	}
	// Un dossier qui n'existe pas n'est ni vu ni sûr : c'est ce qui permettra de
	// conclure à son absence quand son parent, lui, est sûr.
	if s.dossiersVus["equipe/jamais-cree"] || s.dossiersSurs["equipe/jamais-cree"] {
		t.Error("un dossier inexistant ne devrait être ni vu ni sûr")
	}
}

// arbreDe rend le périmètre serveur en liste.
func arbreDe(t *testing.T, e *Engine) []string {
	t.Helper()
	paths, err := e.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// arbre rend le périmètre serveur sous forme d'ensemble, pour comparer des
// ensembles plutôt que des cardinalités.
func arbre(t *testing.T, e *Engine) map[string]bool {
	t.Helper()
	paths, err := e.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[p] = true
	}
	return out
}

// TestDossierIllisibleNeSupprimePasChezTousLesMembres (défaut 1) : un `chmod
// 000`, un volume réseau à moitié monté ou une restriction TCC rendent un
// sous-dossier illisible. Ses fichiers deviennent absents du scan - ils ne
// deviennent pas supprimés pour autant.
func TestDossierIllisibleNeSupprimePasChezTousLesMembres(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignore les droits : le dossier serait lu quand même")
	}
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/note.md", "ordinaire\n")
	writeFile(t, dirA, "equipe/secret/dedans.md", "précieux\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	avant := arbre(t, a)
	if !avant["equipe/secret/dedans.md"] {
		t.Fatalf("prérequis : le fichier devrait être sur le serveur, %v", avant)
	}

	illisible := filepath.Join(dirA, "equipe/secret")
	if err := os.Chmod(illisible, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(illisible, 0o755) })

	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle avec dossier illisible : %v", err)
	}
	apres := arbre(t, a)
	if !apres["equipe/secret/dedans.md"] {
		t.Errorf("un dossier illisible a supprimé son contenu chez tous les membres : %v -> %v", avant, apres)
	}
	if len(a.Laisses()) == 0 {
		t.Error("le dossier illisible n'a pas été signalé")
	}
}

// TestLienSymboliqueNeSupprimePasSurDeuxCycles (défaut 2) : l'écriture à
// travers un lien est bien refusée, mais l'état enregistre quand même le
// chemin comme synchronisé. Au cycle SUIVANT il est « absent », et c'est là que
// le DELETE partait. Le test du cycle 2 n'en faisait qu'un et ne pouvait pas le
// voir : celui-ci en fait deux.
func TestLienSymboliqueNeSupprimePasSurDeuxCycles(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	dehors := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirA, "equipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dehors, filepath.Join(dirA, "equipe/photos")); err != nil {
		t.Fatal(err)
	}

	// Un autre poste publie sous le chemin détourné.
	b, dirB := newEngine(t, url, token)
	writeFile(t, dirB, "equipe/photos/a.md", "contenu\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	for cycle := 1; cycle <= 2; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle A n°%d : %v", cycle, err)
		}
		if !arbre(t, a)["equipe/photos/a.md"] {
			t.Fatalf("cycle %d : le fichier a été supprimé chez tous les membres à cause d'un lien symbolique", cycle)
		}
	}
	// Et il n'a jamais été écrit hors de la racine.
	if _, err := os.Stat(filepath.Join(dehors, "a.md")); err == nil {
		t.Error("le pull a écrit hors de la racine locale, à travers un lien symbolique")
	}
}

// TestRacineEspaceRemplaceeParUnLien (défaut 3) : déplacer un espace ailleurs
// et laisser un lien à sa place est un geste banal pour garder un vault en
// place. Il ne doit pas vider l'espace pour toute l'équipe.
func TestRacineEspaceRemplaceeParUnLien(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/note.md", "important\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	avant := arbre(t, a)

	dehors := t.TempDir()
	reel := filepath.Join(dehors, "equipe-reel")
	if err := os.Rename(filepath.Join(dirA, "equipe"), reel); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(reel, filepath.Join(dirA, "equipe")); err != nil {
		t.Fatal(err)
	}

	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle après remplacement par un lien : %v", err)
	}
	apres := arbre(t, a)
	if len(apres) != len(avant) {
		t.Errorf("une racine d'espace remplacée par un lien a vidé le dépôt partagé : %v -> %v", avant, apres)
	}
	if got, ok := readFile(t, reel, "note.md"); !ok || got != "important\n" {
		t.Errorf("le contenu déplacé hors de la racine a été touché : %q %v", got, ok)
	}
}

// TestSuppressionDeDossierSePropageToujours : le piège de la règle littérale.
// « Le dossier parent doit avoir été parcouru » interdirait de supprimer un
// dossier, puisqu'un dossier supprimé n'est jamais parcouru. Ce geste doit
// continuer de marcher.
func TestSuppressionDeDossierSePropageToujours(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/garde.md", "je reste\n")
	for _, n := range []string{"a", "b", "c"} {
		writeFile(t, dirA, "equipe/vieux/"+n+".md", "à jeter\n")
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	if !arbre(t, a)["equipe/vieux/b.md"] {
		t.Fatal("prérequis : le sous-dossier devrait être sur le serveur")
	}

	if err := os.RemoveAll(filepath.Join(dirA, "equipe/vieux")); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle après suppression du sous-dossier : %v", err)
	}

	apres := arbre(t, a)
	for _, n := range []string{"a", "b", "c"} {
		if apres["equipe/vieux/"+n+".md"] {
			t.Errorf("la suppression d'un sous-dossier entier ne s'est pas propagée : %v", apres)
			break
		}
	}
	if !apres["equipe/garde.md"] {
		t.Error("un fichier voisin a été emporté par la suppression du sous-dossier")
	}
}

// TestSuppressionPendantLeCycleNestPasAnnulee (défaut 12) : une suppression
// tombée entre le scan du push et le rattrapage était annulée sans trace - le
// fichier redescendait sous les yeux de l'utilisateur qui venait de le jeter.
func TestSuppressionPendantLeCycleNestPasAnnulee(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/jetable.md", "temporaire\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}

	// La photo du cycle : le fichier est là.
	vu, err := a.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := vu.fichiers["equipe/jetable.md"]; !ok {
		t.Fatal("prérequis : le fichier devrait être dans le scan")
	}
	// L'utilisateur le supprime MAINTENANT, après le scan.
	if err := os.Remove(filepath.Join(dirA, "equipe/jetable.md")); err != nil {
		t.Fatal(err)
	}

	a.laisses = nil
	perimetre, err := a.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.rattrape(perimetre, vu, nil, nil); err != nil {
		t.Fatalf("rattrape : %v", err)
	}
	if _, ok := readFile(t, dirA, "equipe/jetable.md"); ok {
		t.Error("le rattrapage a annulé une suppression tombée pendant le cycle")
	}
	signale := false
	for _, l := range a.Laisses() {
		if strings.Contains(l.Raison, "supprimé pendant ce cycle") {
			signale = true
		}
	}
	if !signale {
		t.Error("la suppression tombée pendant le cycle n'a pas été signalée")
	}

	// Et au cycle suivant, elle part vraiment : le mécanisme converge.
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle suivant : %v", err)
	}
	if arbre(t, a)["equipe/jetable.md"] {
		t.Error("la suppression n'est jamais partie au cycle suivant")
	}
}

// TestBasculeDossierVersFichierSePropage : renommer un dossier en fichier (ou
// l'inverse) est un geste banal dans un vault. L'ancien chemin DOIT être
// supprimé, sinon git refuse le conflit dossier/fichier et la sync gèle sur ce
// chemin, indéfiniment et sans que rien n'échoue.
func TestBasculeDossierVersFichierSePropage(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/notes/a.md", "dans le dossier\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	if !arbre(t, a)["equipe/notes/a.md"] {
		t.Fatal("prérequis : le dossier devrait être sur le serveur")
	}

	// Le dossier devient un fichier du même nom.
	if err := os.RemoveAll(filepath.Join(dirA, "equipe/notes")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/notes", "maintenant un fichier\n")
	// Deux cycles, à dessein : push et pull d'un même cycle sont ancrés sur le
	// MÊME head (voir SyncOnce). La suppression part au premier, l'écriture du
	// nouveau chemin ne peut aboutir qu'une fois ce head avancé. Ce qui compte
	// est que ça CONVERGE - avant, ça ne convergeait jamais.
	for cycle := 1; cycle <= 2; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle après bascule n°%d : %v", cycle, err)
		}
	}

	apres := arbre(t, a)
	if apres["equipe/notes/a.md"] {
		t.Errorf("l'ancien dossier n'a pas été supprimé : la sync est gelée sur ce chemin (%v)", apres)
	}
	if !apres["equipe/notes"] {
		t.Errorf("le nouveau fichier n'est jamais arrivé sur le serveur : %v", apres)
	}
}

// TestBasculeFichierVersDossierSePropage : le sens inverse. Le fichier connu a
// laissé place à un dossier ; il n'existe plus, et c'est une constatation.
func TestBasculeFichierVersDossierSePropage(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/notes.md", "un fichier\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	if !arbre(t, a)["equipe/notes.md"] {
		t.Fatal("prérequis : le fichier devrait être sur le serveur")
	}

	if err := os.Remove(filepath.Join(dirA, "equipe/notes.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/notes.md/dedans.md", "maintenant un dossier\n")
	// Même raison que dans le sens inverse : la convergence prend deux cycles.
	// Et surtout, aucun des deux ne doit ÉCHOUER : la suppression revient dans
	// le delta serveur et tombe sur un dossier local à la place du fichier.
	for cycle := 1; cycle <= 2; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle après bascule n°%d : %v", cycle, err)
		}
	}

	apres := arbre(t, a)
	if apres["equipe/notes.md"] {
		t.Errorf("l'ancien fichier n'a pas été supprimé : la sync est gelée sur ce chemin (%v)", apres)
	}
	if !apres["equipe/notes.md/dedans.md"] {
		t.Errorf("le nouveau dossier n'est jamais arrivé sur le serveur : %v", apres)
	}
}

// TestLienRetireNeSupprimePasChezTousLesMembres : le pull refuse d'écrire à
// travers un lien - mais il tamponnait quand même l'état comme si le fichier
// était sur le disque. Retirer le lien, geste de rangement ordinaire, faisait
// alors constater une absence parfaitement franche, et le fichier partait chez
// tout le monde. Un état ne doit jamais déclarer synchronisé ce qui n'a pas été
// écrit : aucun raisonnement sur l'absence ne peut être sûr sinon.
func TestLienRetireNeSupprimePasChezTousLesMembres(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	dehors := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirA, "equipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dehors, filepath.Join(dirA, "equipe/photos")); err != nil {
		t.Fatal(err)
	}

	b, dirB := newEngine(t, url, token)
	writeFile(t, dirB, "equipe/photos/a.md", "contenu\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A avec le lien : %v", err)
	}
	// Rien n'a été écrit : l'état ne doit pas prétendre le contraire.
	if _, pretend := a.state.Files["equipe/photos/a.md"]; pretend {
		t.Error("l'état déclare synchronisé un chemin que le disque n'a jamais reçu")
	}

	// Le geste de rangement : l'utilisateur retire le lien.
	if err := os.Remove(filepath.Join(dirA, "equipe/photos")); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A après retrait du lien : %v", err)
	}

	if !arbre(t, a)["equipe/photos/a.md"] {
		t.Fatal("retirer un lien symbolique a supprimé le fichier chez tous les membres")
	}
	// Et maintenant que le lien n'est plus là, il descend pour de bon.
	if got, ok := readFile(t, dirA, "equipe/photos/a.md"); !ok || got != "contenu\n" {
		t.Errorf("le fichier n'est pas descendu une fois le lien retiré : %q %v", got, ok)
	}
}

// TestSuppressionNonAppliquableNeRessuscitePas : quand le pull n'arrive pas à
// retirer un fichier du disque (dossier parent en lecture seule, fichier
// verrouillé, volume read-only), le fichier EST TOUJOURS LÀ. L'oublier de
// l'état le ferait repousser comme une création au cycle suivant - et la
// suppression faite par un autre membre serait annulée pour toute l'équipe,
// sans un mot. Un état qui déclare non-synchronisé ce que le disque porte
// encore est aussi faux que l'inverse.
func TestSuppressionNonAppliquableNeRessuscitePas(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignore les droits : le retrait réussirait")
	}
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, _ := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/sous/x.md", "partagé\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	// B supprime, la suppression part au serveur.
	if err := os.Remove(filepath.Join(b.dir, "equipe/sous/x.md")); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if arbre(t, b)["equipe/sous/x.md"] {
		t.Fatal("prérequis : la suppression de B devrait être partie")
	}

	// Chez A, le dossier parent n'est pas inscriptible : le retrait échouera.
	parent := filepath.Join(dirA, "equipe/sous")
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })

	// Le cycle peut échouer (c'est visible, donc sûr) ou passer, mais il ne doit
	// en aucun cas ressusciter le fichier chez tout le monde.
	for cycle := 1; cycle <= 2; cycle++ {
		_ = a.SyncOnce()
		if arbre(t, a)["equipe/sous/x.md"] {
			t.Fatalf("cycle %d : la suppression d'un autre membre a été annulée pour toute l'équipe", cycle)
		}
	}
}

// TestDossierLienIgnoreNeSupprimePas : `certifie` s'arrête sur tout ce qui a
// été vu sans être certifié - mais encore faut-il que le scan l'ait enregistré.
// Un dossier-lien couvert par un motif du .vecuignore sortait du scan AVANT
// d'être reconnu comme lien : invisible pour `certifie`, qui le traversait, et
// le Lstat final allait alors regarder hors de la racine locale pour décider
// d'un DELETE.
func TestDossierLienIgnoreNeSupprimePas(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	// Un autre poste publie sous un chemin qui, ici, sera un lien ignoré.
	b, dirB := newEngine(t, url, token)
	writeFile(t, dirB, "equipe/photos/a.md", "contenu\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, ok := readFile(t, dirA, "equipe/photos/a.md"); !ok {
		t.Fatal("prérequis : A devrait avoir le fichier")
	}

	// A remplace le dossier par un lien vers AILLEURS - un dossier qui ne
	// contient pas ce fichier - et couvre le chemin par un motif d'ignore.
	// Le moteur ne doit pas aller regarder hors de la racine pour conclure.
	dehors := t.TempDir()
	if err := os.RemoveAll(filepath.Join(dirA, "equipe/photos")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dehors, filepath.Join(dirA, "equipe/photos")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, ".vecuignore", "equipe/photos\n")

	for cycle := 1; cycle <= 2; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", cycle, err)
		}
		if !arbre(t, a)["equipe/photos/a.md"] {
			t.Fatalf("cycle %d : un dossier-lien ignoré a fait supprimer le fichier chez tous les membres", cycle)
		}
	}
	// Et rien n'a été écrit hors de la racine locale.
	entrees, err := os.ReadDir(dehors)
	if err != nil {
		t.Fatal(err)
	}
	if len(entrees) != 0 {
		t.Errorf("le moteur a écrit hors de la racine locale : %v", entrees)
	}
}

// TestDossierLocalSurUnCheminEcritNeGelePasLeCycle : un dossier local à la
// place d'un fichier que le serveur veut écrire donnait EISDIR, donc une erreur
// de cycle, donc un head qui n'avance plus - et TOUS les espaces du poste
// cessaient de se synchroniser à cause d'un seul chemin.
func TestDossierLocalSurUnCheminEcritNeGelePasLeCycle(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/notes.md", "origine\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	// B modifie le fichier ; A en fait un dossier au même nom.
	writeFile(t, dirB, "equipe/notes.md", "modifié par B\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dirA, "equipe/notes.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/notes.md/dedans.md", "maintenant un dossier\n")

	for cycle := 1; cycle <= 3; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle %d gelé par un dossier local sur un chemin écrit : %v", cycle, err)
		}
	}
	if !arbre(t, a)["equipe/notes.md/dedans.md"] {
		t.Errorf("la situation n'a jamais convergé : %v", arbre(t, a))
	}
}

// TestBasculeNeLaissePasDEntreeFantome : le DELETE émis sur un chemin devenu
// impossible (un composant est maintenant un fichier) revenait par le pull, où
// il butait sur le même ENOTDIR - donc l'entrée n'était jamais retirée de
// l'état, et le DELETE repartait à chaque cycle, indéfiniment.
func TestBasculeNeLaissePasDEntreeFantome(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/notes/a.md", "dans le dossier\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(dirA, "equipe/notes")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/notes", "maintenant un fichier\n")
	for cycle := 1; cycle <= 3; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", cycle, err)
		}
	}

	if _, fantome := a.state.Files["equipe/notes/a.md"]; fantome {
		t.Error("l'état garde une entrée que rien ne peut plus nettoyer : un DELETE repart à chaque cycle")
	}
	if !arbre(t, a)["equipe/notes"] {
		t.Errorf("la situation n'a jamais convergé : %v", arbre(t, a))
	}
}

// TestGardeVoitLesSuppressionsMalgreDesAjouts (défaut 6) : la garde comparait
// des cardinalités. Restaurer une copie plus ancienne d'un dossier - des
// fichiers en moins, d'autres en plus - donnait une différence faible, donc la
// garde ne voyait rien et les suppressions partaient chez tout le monde.
func TestGardeVoitLesSuppressionsMalgreDesAjouts(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	for i := range 20 {
		writeFile(t, dirA, fmt.Sprintf("equipe/note-%02d.md", i), "contenu\n")
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	avant := arbre(t, a)

	// La copie plus ancienne : 15 fichiers en moins, 10 autres en plus.
	for i := range 15 {
		if err := os.Remove(filepath.Join(dirA, fmt.Sprintf("equipe/note-%02d.md", i))); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10 {
		writeFile(t, dirA, fmt.Sprintf("equipe/ancien-%02d.md", i), "d'une vieille copie\n")
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle après restauration : %v", err)
	}

	for i := range 15 {
		p := fmt.Sprintf("equipe/note-%02d.md", i)
		if !arbre(t, a)[p] {
			t.Fatalf("des ajouts simultanés ont masqué %d suppressions : %s a été supprimé chez tous les membres (avant : %d)", 15, p, len(avant))
		}
	}
	bloque := false
	for _, l := range a.Laisses() {
		if l.Genre == GenreEspace && strings.Contains(l.Raison, "en une fois") {
			bloque = true
		}
	}
	if !bloque {
		t.Errorf("la garde n'a pas signalé la suppression massive : %v", a.Laisses())
	}
}

// TestMotifIgnoreNeBloquePasLesSuppressions (défaut 11) : les deux compteurs de
// la garde étaient construits sur des règles différentes - l'un après les
// motifs d'ignore, l'autre avant. Ajouter un motif au .vecuignore gonflait donc
// le compte des « disparus » au point de bloquer toute suppression ordinaire de
// l'espace, indéfiniment.
func TestMotifIgnoreNeBloquePasLesSuppressions(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	for i := range 12 {
		writeFile(t, dirA, fmt.Sprintf("equipe/photo-%02d.png", i), "en fait du texte\n")
	}
	writeFile(t, dirA, "equipe/jetable.md", "temporaire\n")
	writeFile(t, dirA, "equipe/garde.md", "je reste\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}

	// L'utilisateur décide de ne plus synchroniser les images, et fait par
	// ailleurs un ménage tout à fait ordinaire : un fichier.
	writeFile(t, dirA, ".vecuignore", "*.png\n")
	if err := os.Remove(filepath.Join(dirA, "equipe/jetable.md")); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle après ajout du motif : %v", err)
	}

	apres := arbre(t, a)
	if apres["equipe/jetable.md"] {
		t.Errorf("un motif d'ignore a bloqué une suppression ordinaire : %v", a.Laisses())
	}
	// Et les fichiers désormais ignorés restent bien sur le serveur : ignorer
	// n'est pas supprimer.
	if !apres["equipe/photo-00.png"] {
		t.Error("un fichier devenu ignoré a été supprimé du dépôt partagé")
	}
	if !apres["equipe/garde.md"] {
		t.Error("un fichier voisin a été emporté")
	}
}

// TestMotifIgnoreNeDesarmePasLaGarde : le pendant du défaut 11, dans l'autre
// sens. Si le numérateur de la garde compte les suppressions constatées mais
// que le dénominateur reste un comptage brut de l'état, tout fichier qui ne
// peut structurellement pas entrer au numérateur (ignoré, incertain, dans un
// dossier non parcouru) reste au dénominateur - et la garde devient
// mathématiquement inatteignable dès qu'une moitié de l'espace est ignorée. Un
// « *.png » mis au .vecuignore des mois plus tôt suffirait à la désarmer.
func TestMotifIgnoreNeDesarmePasLaGarde(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	for i := range 30 {
		writeFile(t, dirA, fmt.Sprintf("equipe/photo-%02d.png", i), "en fait du texte\n")
	}
	for i := range 20 {
		writeFile(t, dirA, fmt.Sprintf("equipe/docs/note-%02d.md", i), "précieux\n")
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	// Le motif est posé longtemps avant le geste, et n'a aucun rapport avec lui.
	writeFile(t, dirA, ".vecuignore", "*.png\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle avec motif : %v", err)
	}

	// Le geste malheureux : le dossier docs/ glissé sur le bureau.
	if err := os.RemoveAll(filepath.Join(dirA, "equipe/docs")); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle après le geste : %v", err)
	}

	apres := arbre(t, a)
	for i := range 20 {
		p := fmt.Sprintf("equipe/docs/note-%02d.md", i)
		if !apres[p] {
			t.Fatalf("un motif d'ignore sans rapport a désarmé la garde : %s supprimé chez tous les membres", p)
		}
	}
	bloque := false
	for _, l := range a.Laisses() {
		if l.Genre == GenreEspace && strings.Contains(l.Raison, "en une fois") {
			bloque = true
		}
	}
	if !bloque {
		t.Errorf("la garde n'a rien dit : %v", a.Laisses())
	}
}

// TestEspaceIgnoreNeCriePasAuLoup : mettre le nom d'un espace au .vecuignore
// pour cesser de le synchroniser ici est un geste volontaire. Il ne doit pas
// produire, à chaque cycle et sans qu'on puisse le faire taire, une alerte qui
// annonce un dossier disparu, remplacé ou illisible - trois choses fausses.
func TestEspaceIgnoreNeCriePasAuLoup(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/note.md", "contenu\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	avant := arbre(t, a)

	writeFile(t, dirA, ".vecuignore", "equipe\n")
	for cycle := 1; cycle <= 2; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", cycle, err)
		}
		for _, l := range a.Laisses() {
			if strings.Contains(l.Raison, "n'a pas pu être parcouru") {
				t.Fatalf("cycle %d : fausse alerte sur un espace volontairement ignoré : %s", cycle, l.Raison)
			}
		}
	}
	// Et surtout, ignorer n'est pas supprimer.
	if len(arbre(t, a)) != len(avant) {
		t.Errorf("ignorer un espace a modifié le dépôt partagé : %v -> %v", avant, arbre(t, a))
	}
}

// TestFichierSurUneRacineDEspaceNeGelePasLePoste : une racine d'espace
// remplacée par un fichier faisait échouer le cycle sur un mkdir - donc plus
// AUCUN espace du poste ne se synchronisait, à cause d'un seul chemin.
func TestFichierSurUneRacineDEspaceNeGelePasLePoste(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/note.md", "contenu\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	avant := arbre(t, a)

	if err := os.RemoveAll(filepath.Join(dirA, "equipe")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe", "un fichier a pris la place du dossier\n")

	for cycle := 1; cycle <= 2; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle %d gelé par un fichier à la racine d'un espace : %v", cycle, err)
		}
	}
	if len(arbre(t, a)) != len(avant) {
		t.Errorf("le dépôt partagé a été modifié : %v -> %v", avant, arbre(t, a))
	}
}

// TestSuppressionMassiveExigeUneConfirmation (défaut 9) : la garde n'avait
// aucune sortie. Un vrai ménage bouclait indéfiniment - les fichiers
// réapparaissaient au rattrapage, cycle après cycle. Sans quelqu'un devant le
// clavier, rien ne part ; avec une confirmation, tout part.
func TestSuppressionMassiveExigeUneConfirmation(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	for i := range 20 {
		writeFile(t, dirA, fmt.Sprintf("equipe/note-%02d.md", i), "contenu\n")
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	jeter := func() {
		t.Helper()
		for i := range 18 {
			os.Remove(filepath.Join(dirA, fmt.Sprintf("equipe/note-%02d.md", i)))
		}
	}

	// Sans confirmation possible (le daemon, un cron) : rien ne part, jamais.
	jeter()
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle sans confirmation : %v", err)
	}
	if !arbre(t, a)["equipe/note-00.md"] {
		t.Fatal("une suppression massive est partie sans confirmation")
	}
	// Et le dossier se reconstitue : c'est ce qui rendait la boucle sans issue.
	if _, ok := readFile(t, dirA, "equipe/note-00.md"); !ok {
		t.Fatal("le dossier ne s'est pas reconstitué")
	}

	// Une réponse à côté ne vaut pas confirmation.
	a.SurSuppressionMassive(func(espace string, constatees, verifies int) bool { return false })
	jeter()
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle avec refus : %v", err)
	}
	if !arbre(t, a)["equipe/note-00.md"] {
		t.Fatal("une suppression massive est partie malgré un refus")
	}

	// Confirmée : elle part, et l'espace est bien celui qu'on annonce.
	var vuEspace string
	var vuConstatees int
	a.SurSuppressionMassive(func(espace string, constatees, verifies int) bool {
		vuEspace, vuConstatees = espace, constatees
		return true
	})
	jeter()
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle avec confirmation : %v", err)
	}
	if vuEspace != "equipe" || vuConstatees != 18 {
		t.Errorf("la question posée ne décrit pas le geste : espace %q, %d fichiers", vuEspace, vuConstatees)
	}
	apres := arbre(t, a)
	for i := range 18 {
		if apres[fmt.Sprintf("equipe/note-%02d.md", i)] {
			t.Fatalf("la suppression confirmée n'est pas partie : %v", apres)
		}
	}
	if !apres["equipe/note-19.md"] {
		t.Error("un fichier non supprimé a été emporté")
	}
}

// TestDeplacementEntreEspacesNeDuplique (défaut 10) : l'ajout partait côté
// destination, la suppression était bloquée côté source - les fichiers
// existaient donc en double chez tout le monde, et le rattrapage les remettait
// à chaque cycle.
func TestDeplacementEntreEspacesNeDuplique(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	for _, esp := range []string{"shared", "prive"} {
		if err := database.SetPermission(membreID, esp, perms.Ecriture); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	e.SurSuppressionMassive(func(string, int, int) bool { return true })

	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	for i := range 15 {
		writeFile(t, dir, fmt.Sprintf("shared/note-%02d.md", i), "contenu\n")
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle de dépôt : %v", err)
	}

	// Le déplacement, d'un espace vers l'autre.
	for i := range 15 {
		nom := fmt.Sprintf("note-%02d.md", i)
		if err := os.Rename(filepath.Join(dir, "shared", nom), filepath.Join(dir, "prive", nom)); err != nil {
			t.Fatal(err)
		}
	}
	for cycle := 1; cycle <= 2; cycle++ {
		if err := e.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", cycle, err)
		}
	}

	apres := arbre(t, e)
	for i := range 15 {
		nom := fmt.Sprintf("note-%02d.md", i)
		if apres["shared/"+nom] {
			t.Fatalf("les fichiers déplacés existent en double : shared/%s est toujours là", nom)
		}
		if !apres["prive/"+nom] {
			t.Fatalf("le déplacement n'est pas arrivé à destination : prive/%s manque", nom)
		}
	}
}

// TestSuppressionRefuseeEnLectureSeuleEstSignalee (défaut 7) : le code HTTP du
// DELETE n'était jamais examiné, contrairement au PUT juste en dessous. Un
// refus en lecture seule passait pour un succès : rien dans le rapport, et le
// chemin reparti en DELETE à chaque cycle, indéfiniment et sans trace.
func TestSuppressionRefuseeEnLectureSeuleEstSignalee(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetPermission(membreID, "shared", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	if _, ok := readFile(t, dir, "shared/note.md"); !ok {
		t.Fatal("prérequis : le fichier devrait être descendu")
	}

	// L'utilisateur supprime localement un fichier qu'il n'a pas le droit de
	// modifier. La suppression est refusée : il doit l'apprendre.
	if err := os.Remove(filepath.Join(dir, "shared/note.md")); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle après suppression : %v", err)
	}
	if !arbre(t, e)["shared/note.md"] {
		t.Fatal("un compte en lecture seule a supprimé un fichier du dépôt partagé")
	}
	signale := false
	for _, l := range e.Laisses() {
		if strings.Contains(l.Raison, "lecture seule") {
			signale = true
		}
	}
	if !signale {
		t.Errorf("le refus de suppression n'a pas été signalé : %v", e.Laisses())
	}
}

// TestRienNestTouchéHorsDeLaRacineAuDemontage (défaut 3, côté disque) :
// `demonte`, `reconcilePerimeter` et `preserveLocalEditIfAny` manipulaient le
// disque sans la garde `sansLien`. Un espace déplacé et remplacé par un lien
// faisait supprimer et renommer des fichiers personnels hors de la racine.
func TestRienNestToucheHorsDeLaRacineAuDemontage(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}

	// L'espace est déplacé hors de la racine, un lien prend sa place.
	dehors := t.TempDir()
	reel := filepath.Join(dehors, "shared-reel")
	if err := os.Rename(filepath.Join(dir, "shared"), reel); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(reel, filepath.Join(dir, "shared")); err != nil {
		t.Fatal(err)
	}
	avantDehors, err := os.ReadDir(reel)
	if err != nil {
		t.Fatal(err)
	}

	// L'accès est retiré : le démontage ne doit rien toucher à travers le lien.
	if err := database.SetPermission(membreID, "shared", perms.Invisible); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle après retrait d'accès : %v", err)
	}

	apresDehors, err := os.ReadDir(reel)
	if err != nil {
		t.Fatal(err)
	}
	if len(apresDehors) != len(avantDehors) {
		t.Errorf("le démontage a touché des fichiers hors de la racine locale : %d -> %d", len(avantDehors), len(apresDehors))
	}
	for _, f := range apresDehors {
		if strings.Contains(f.Name(), "conflit") {
			t.Errorf("un fichier hors de la racine a été renommé en copie de conflit : %s", f.Name())
		}
	}
}

// TestFichierIgnoreSupprimeNeRevientPas (défaut 8) : boucle entre deux
// mécanismes du même cycle. Le push refusait - à juste titre - de propager la
// suppression d'un fichier ignoré ; le rattrapage le retéléchargeait. Résultat,
// il revenait à chaque cycle, indéfiniment, sans qu'aucun des deux ait tort.
func TestFichierIgnoreSupprimeNeRevientPas(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/note.md", "ordinaire\n")
	writeFile(t, dirA, "equipe/photo.png", "en fait du texte\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	if !arbre(t, a)["equipe/photo.png"] {
		t.Fatal("prérequis : le fichier devrait être sur le serveur")
	}

	// L'utilisateur ignore les images, puis supprime celle-ci de son poste.
	writeFile(t, dirA, ".vecuignore", "*.png\n")
	if err := os.Remove(filepath.Join(dirA, "equipe/photo.png")); err != nil {
		t.Fatal(err)
	}

	for cycle := 1; cycle <= 3; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", cycle, err)
		}
		if _, revenu := readFile(t, dirA, "equipe/photo.png"); revenu {
			t.Fatalf("cycle %d : un fichier ignoré et supprimé est revenu tout seul sur le poste", cycle)
		}
	}
	// Et il reste bien sur le serveur : ignorer n'est pas supprimer.
	if !arbre(t, a)["equipe/photo.png"] {
		t.Error("le fichier ignoré a été supprimé du dépôt partagé")
	}
}

// TestDossierReutiliseNestPasPublieAuRemontage (défaut 5) : `state.Connus`
// n'était jamais purgé. Un dossier démonté, vidé, puis réutilisé pour des
// fichiers personnels était publié au remontage, sans avertissement.
func TestDossierReutiliseNestPasPublieAuRemontage(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	accorde := func(niveau perms.Level) {
		t.Helper()
		if err := database.SetPermission(membreID, "shared", niveau); err != nil {
			t.Fatal(err)
		}
		if err := e.SyncOnce(); err != nil {
			t.Fatalf("cycle : %v", err)
		}
	}

	accorde(perms.Ecriture)
	if _, ok := readFile(t, dir, "shared/note.md"); !ok {
		t.Fatal("prérequis : l'espace devrait être monté")
	}
	// Accès retiré : tout est propre, donc le dossier se vide entièrement.
	accorde(perms.Invisible)
	if len(e.state.Connus) != 0 {
		t.Errorf("un espace sans le moindre résidu reste dans Connus : %v", e.state.Connus)
	}

	// Le dossier libéré est réutilisé pour des fichiers strictement personnels.
	writeFile(t, dir, "shared/impots-2025.md", "strictement personnel\n")

	accorde(perms.Ecriture)
	if len(e.state.Espaces) != 0 {
		t.Errorf("l'espace a été monté sur un dossier personnel : %v", e.state.Espaces)
	}
	for _, p := range arbreDe(t, e) {
		if p == "shared/impots-2025.md" {
			t.Fatal("un fichier personnel a été publié sur le serveur")
		}
	}
	if got, ok := readFile(t, dir, "shared/impots-2025.md"); !ok || got != "strictement personnel\n" {
		t.Errorf("le fichier personnel a été touché : %q %v", got, ok)
	}
	if len(e.Laisses()) == 0 {
		t.Error("le refus de monter n'a pas été expliqué")
	}
}

// TestGenresDeLaisse (défaut 13) : deux genres ne suffisaient pas. Un nom
// d'espace refusé sortait sous « ces fichiers n'existent que sur ce poste », ce
// qui est faux ; et le rattrapage classait en fichier perdu un chemin qui est
// sur le serveur et seulement absent d'ici. Un rapport qui crie au loup cesse
// d'être lu, et c'est le seul canal contre le silence.
func TestGenresDeLaisse(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	// Un chemin qui est sur le serveur et ne peut pas être écrit ici : ce n'est
	// pas une perte, le contenu existe ailleurs.
	dehors := t.TempDir()
	if err := os.Symlink(dehors, filepath.Join(dirA, "equipe/photos")); err != nil {
		t.Fatal(err)
	}
	b, dirB := newEngine(t, url, token)
	writeFile(t, dirB, "equipe/photos/a.md", "contenu\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	// Et un binaire : hors périmètre depuis le 20/08. Il n'est nulle part
	// ailleurs, mais rien ne peut être tenté - ni par le cycle suivant, ni par la
	// personne. Le compter comme une perte faisait sortir `vecu sync` en erreur à
	// chaque exécution sur les 298 binaires du vault de Colin.
	if err := os.WriteFile(filepath.Join(dirA, "equipe/image.png"), []byte{0xff, 0xd8, 0x00, 0x9f}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}

	var perdus, serveur, espaces, hors int
	for _, l := range a.Laisses() {
		switch {
		case l.Genre == GenreEspace:
			espaces++
		case l.Genre == GenreHorsPerimetre:
			hors++
			if !strings.Contains(l.Chemin, "image.png") {
				t.Errorf("classé hors périmètre à tort : %s (%s)", l.Chemin, l.Raison)
			}
		case l.Perdu():
			perdus++
			// Le seul contenu que Vécu n'a réellement pas et qu'il POURRAIT avoir :
			// le lien dont la cible est hors de son contrôle. Celui-là se répare.
			if l.Chemin != "equipe/photos" {
				t.Errorf("classé « n'existe que sur ce poste » à tort : %s (%s)", l.Chemin, l.Raison)
			}
		default:
			serveur++
		}
	}
	if perdus != 1 {
		t.Errorf("attendu 1 contenu réellement absent du serveur (le lien), obtenu %d : %v", perdus, a.Laisses())
	}
	if hors != 1 {
		t.Errorf("attendu 1 hors périmètre (le binaire), obtenu %d : %v", hors, a.Laisses())
	}
	if serveur == 0 {
		t.Errorf("aucun chemin classé « sur le serveur » : %v", a.Laisses())
	}
	_ = espaces
}

// TestNomEspaceRefuseNestPasUnFichierPerdu (défaut 13, l'autre moitié) : un nom
// d'espace que le client refuse ne doit pas faire sortir `vecu sync` en erreur
// sous prétexte que « ces fichiers n'existent que sur ce poste ».
func TestNomEspaceRefuseNestPasUnFichierPerdu(t *testing.T) {
	e := &Engine{logf: func(string, ...any) {}}
	e.laisseEspace("mon espace", "nom d'espace refusé par le client")
	for _, l := range e.Laisses() {
		if l.Perdu() {
			t.Errorf("une remarque d'espace est comptée comme un fichier perdu : %+v", l)
		}
	}
}

// TestConfirmationRevalideAvantDEnvoyer : la question posée à l'humain rouvre
// la course que tout le modèle ferme. Entre la vérification disque et la
// réponse, il s'écoule un temps NON BORNÉ - et c'est précisément le moment où
// la personne agit sur ces fichiers.
//
// Le scénario n'est pas tordu, c'est le geste de rattrapage naturel : elle
// jette un dossier, l'outil demande confirmation, elle réalise son erreur,
// remet le dossier en place, puis tape le nom de l'espace en croyant valider le
// nouvel état. Sans revérification, ses fichiers restaurés partent chez tout le
// monde - et le pull qui suit efface aussi les copies locales.
func TestConfirmationRevalideAvantDEnvoyer(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	for i := range 20 {
		writeFile(t, dirA, fmt.Sprintf("equipe/note-%02d.md", i), "contenu\n")
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	avant := arbre(t, a)

	for i := range 18 {
		if err := os.Remove(filepath.Join(dirA, fmt.Sprintf("equipe/note-%02d.md", i))); err != nil {
			t.Fatal(err)
		}
	}
	// Elle remet tout en place AVANT de répondre, puis confirme.
	a.SurSuppressionMassive(func(string, int, int) bool {
		for i := range 18 {
			writeFile(t, dirA, fmt.Sprintf("equipe/note-%02d.md", i), "contenu\n")
		}
		return true
	})
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle avec confirmation : %v", err)
	}

	apres := arbre(t, a)
	for i := range 18 {
		p := fmt.Sprintf("equipe/note-%02d.md", i)
		if !apres[p] {
			t.Fatalf("un fichier remis en place avant la confirmation a été supprimé chez tous les membres : %s (%d -> %d)", p, len(avant), len(apres))
		}
		if _, ok := readFile(t, dirA, p); !ok {
			t.Fatalf("le fichier restauré a aussi été effacé du disque local : %s", p)
		}
	}
}

// TestMotifIgnoreRetireNeSupprimePas : le rattrapage écarte les chemins
// ignorés, mais laissait leur entrée dans l'état avec un hash que le disque ne
// porte plus. Retirer le motif des mois plus tard faisait alors partir un
// DELETE pour une suppression locale ancienne, sans que rien ne relie la cause
// à l'effet. Éditer le .vecuignore ne supprime pas du contenu partagé.
func TestMotifIgnoreRetireNeSupprimePas(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/note.md", "ordinaire\n")
	writeFile(t, dirA, "equipe/photo.png", "en fait du texte\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	writeFile(t, dirA, ".vecuignore", "*.png\n")
	if err := os.Remove(filepath.Join(dirA, "equipe/photo.png")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.SyncOnce(); err != nil {
			t.Fatal(err)
		}
	}
	if !arbre(t, a)["equipe/photo.png"] {
		t.Fatal("prérequis : le fichier ignoré doit rester sur le serveur")
	}

	// Des mois plus tard, le motif est retiré.
	writeFile(t, dirA, ".vecuignore", "# plus rien\n")
	for cycle := 1; cycle <= 2; cycle++ {
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", cycle, err)
		}
		if !arbre(t, a)["equipe/photo.png"] {
			t.Fatalf("cycle %d : retirer un motif du .vecuignore a supprimé un fichier chez tous les membres", cycle)
		}
	}
	// Le comportement attendu est l'inverse : il redescend.
	if _, ok := readFile(t, dirA, "equipe/photo.png"); !ok {
		t.Error("le fichier n'est pas redescendu une fois le motif retiré")
	}
}

// TestDossierIllisibleNestPasUnDossierVide : `contientDesFichiers` avalait
// toute erreur de parcours et rendait « non ». C'est exactement la confusion
// que tout ce chantier bannit - « je n'ai pas pu regarder » pris pour « il n'y
// a rien » - réintroduite dans la garde qui protège un dossier personnel.
func TestDossierIllisibleNestPasUnDossierVide(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignore les droits")
	}
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "perso/impots-2025.md", "strictement personnel\n")
	illisible := filepath.Join(dir, "perso")
	if err := os.Chmod(illisible, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(illisible, 0o755) })

	if !e.contientDesFichiers("perso") {
		t.Error("un dossier illisible a été déclaré vide : sur ce « non », l'espace se monte et son contenu part au premier cycle où il redevient lisible")
	}
}

// TestNomEspaceHostileRefuse : le client ne confie pas la sûreté de son disque
// à la sortie du serveur.
func TestNomEspaceHostileRefuse(t *testing.T) {
	for _, nom := range []string{"..", ".", "../evasion", "/etc", ".ssh", "-rf", "idées", ""} {
		if espaceValide(nom) {
			t.Errorf("espaceValide(%q) = true, attendu false", nom)
		}
	}
	for _, nom := range []string{"shared", "clients", "Notes_2026", "v2.final"} {
		if !espaceValide(nom) {
			t.Errorf("espaceValide(%q) = false, attendu true", nom)
		}
	}
	for _, p := range []string{"equipe/../../x", "../x", "/abs", "equipe//x", "equipe/./x", ""} {
		if cheminSur(p) {
			t.Errorf("cheminSur(%q) = true, attendu false", p)
		}
	}
	if !cheminSur("equipe/sous/idées.md") {
		t.Error("cheminSur refuse un chemin légitime")
	}
}

// TestRemontageApresRetraitDAcces : un aller-retour d'accès dans l'interface
// est le geste le plus banal du produit. Les résidus laissés par le démontage
// (fichiers modifiés, jamais poussés) ne doivent pas faire passer l'espace pour
// un dossier personnel et bloquer le remontage.
func TestRemontageApresRetraitDAcces(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	accorde := func(niveau perms.Level) {
		t.Helper()
		if err := database.SetPermission(membreID, "shared", niveau); err != nil {
			t.Fatal(err)
		}
		if err := e.SyncOnce(); err != nil {
			t.Fatalf("cycle : %v", err)
		}
	}

	accorde(perms.Ecriture)
	if _, ok := readFile(t, dir, "shared/note.md"); !ok {
		t.Fatal("prérequis : l'espace devrait être monté")
	}
	// Une édition non poussée, qui survivra au démontage : c'est le résidu.
	writeFile(t, dir, "shared/autre.md", "travail en cours\n")

	accorde(perms.Invisible)
	if len(e.state.Espaces) != 0 {
		t.Fatalf("l'espace devrait être démonté : %v", e.state.Espaces)
	}
	if _, ok := readFile(t, dir, "shared/autre.md"); !ok {
		t.Fatal("le résidu devrait être conservé")
	}

	accorde(perms.Ecriture)
	if len(e.state.Espaces) != 1 {
		t.Fatalf("l'espace n'a pas été remonté après rétablissement de l'accès : %v (laisses : %v)", e.state.Espaces, e.Laisses())
	}
	if got, ok := readFile(t, dir, "shared/note.md"); !ok || got != "partagé\n" {
		t.Errorf("le contenu n'est pas redescendu au remontage : %q %v", got, ok)
	}
}

// TestRestaurationApresSuppressionDuDossier : quand la garde refuse d'envoyer
// une suppression massive, le dossier doit se reconstituer tout seul. Sinon le
// poste reste bloqué sur un espace vide, sans issue.
func TestRestaurationApresSuppressionDuDossier(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	for i := range 12 {
		writeFile(t, dirA, fmt.Sprintf("equipe/note-%02d.md", i), "contenu\n")
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	avant, _ := a.client.Tree()

	// Le geste malheureux dans le Finder.
	if err := os.RemoveAll(filepath.Join(dirA, "equipe")); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle après suppression : %v", err)
	}

	apres, err := a.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(apres) != len(avant) {
		t.Errorf("le dépôt partagé a été vidé : %d -> %d", len(avant), len(apres))
	}
	// Et le dossier local est revenu.
	if got, ok := readFile(t, dirA, "equipe/note-03.md"); !ok || got != "contenu\n" {
		t.Errorf("le dossier local n'a pas été reconstitué : %q %v", got, ok)
	}
}

// TestSuppressionOrdinairePasseToujours : la garde ne doit pas transformer un
// ménage ordinaire en blocage. Supprimer le dernier fichier d'un petit espace
// reste possible.
func TestSuppressionOrdinairePasseToujours(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	writeFile(t, dirA, "equipe/jetable.md", "temporaire\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dirA, "equipe/jetable.md")); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	paths, _ := a.client.Tree()
	for _, p := range paths {
		if p == "equipe/jetable.md" {
			t.Errorf("une suppression ordinaire a été bloquée : %v", paths)
		}
	}
}

// TestElargissementDeDroitsRattrape : un changement de droits ne produit aucun
// commit, donc le delta ne rapporte rien. Un accès élargi À L'INTÉRIEUR d'un
// espace déjà monté doit quand même arriver sur le poste.
func TestElargissementDeDroitsRattrape(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Accès à un seul fichier : l'espace est monté, mais amputé.
	if err := database.SetPermission(membreID, "shared/note.md", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if _, ok := readFile(t, dir, "shared/autre.md"); ok {
		t.Fatal("prérequis : autre.md ne devrait pas être visible")
	}

	// Élargissement à l'espace entier, sans qu'aucun fichier ne change.
	if err := database.SetPermission(membreID, "shared", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle après élargissement : %v", err)
	}
	if got, ok := readFile(t, dir, "shared/autre.md"); !ok || got != "encore\n" {
		t.Errorf("l'élargissement de droits n'est jamais arrivé sur le poste : %q %v", got, ok)
	}
}

// TestPasDEcritureATraversUnLien : le pull écrit ce que dit le serveur, mais
// c'est le disque local qui peut rediriger. Un lien symbolique dans un espace
// ferait sortir l'écriture de la racine.
func TestPasDEcritureATraversUnLien(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	dehors := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirA, "equipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dehors, filepath.Join(dirA, "equipe/photos")); err != nil {
		t.Fatal(err)
	}

	// Un autre poste publie un fichier sous le chemin détourné.
	b, dirB := newEngine(t, url, token)
	writeFile(t, dirB, "equipe/photos/a.md", "contenu\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	if _, err := os.Stat(filepath.Join(dehors, "a.md")); err == nil {
		t.Fatal("le pull a écrit hors de la racine locale, à travers un lien symbolique")
	}
	refus := false
	for _, l := range a.Laisses() {
		if strings.Contains(l.Raison, "lien symbolique") {
			refus = true
		}
	}
	if !refus {
		t.Error("l'écriture détournée n'a pas été signalée")
	}
}

// TestJournalDitQuelleVersionAEteRemplacee : le journal doit permettre de
// répondre après coup à « quelle version le moteur a-t-il remplacée, et par
// quoi ». Les trois pertes des 09-12/08 ont toutes dû être reconstituées depuis
// un dépôt git tiers, parce que la seule ligne écrite disait qu'un arbitrage
// avait eu lieu sans dire lequel.
func TestJournalDitQuelleVersionAEteRemplacee(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	var journal []string
	b.logf = func(f string, args ...any) { journal = append(journal, fmt.Sprintf(f, args...)) }

	writeFile(t, dirA, "equipe/plan.md", "origine\n")
	a.SyncOnce()
	b.SyncOnce()

	writeFile(t, dirA, "equipe/plan.md", "serveur A\n")
	a.SyncOnce()

	// Même course que TestPullPreservesUnpushedLocalEdit : édition locale arrivée
	// après le scan de push.
	writeFile(t, dirB, "equipe/plan.md", "édition locale de B\n")
	b.state.Files["equipe/plan.md"] = hashContent([]byte("origine\n"))
	journal = nil
	if _, err := b.pull(b.state.Head, nil, nil); err != nil {
		t.Fatalf("pull B : %v", err)
	}

	trace := strings.Join(journal, "\n")
	t.Logf("journal du cycle :\n%s", trace) // visible en -v : documente la forme des lignes
	locale := court(hashContent([]byte("édition locale de B\n")))
	serveur := court(hashContent([]byte("serveur A\n")))
	connu := court(hashContent([]byte("origine\n")))

	// Ce qui a été mis de côté, et ce qui l'a remplacé, doivent être identifiables
	// tous les deux : une trace qui ne nomme qu'un des deux ne dit rien.
	for _, attendu := range []string{locale, serveur, connu} {
		if !strings.Contains(trace, attendu) {
			t.Errorf("empreinte %s absente du journal :\n%s", attendu, trace)
		}
	}
	if !strings.Contains(trace, "écrit (pull) : equipe/plan.md") {
		t.Errorf("l'écriture du pull n'est pas journalisée :\n%s", trace)
	}
	if !strings.Contains(trace, "édition locale non poussée préservée") {
		t.Errorf("la mise de côté n'est pas journalisée :\n%s", trace)
	}
	// La taille doit y être : c'est elle qui a fait tiquer sur les copies d'un
	// dossier client (7844 contre 6528 octets), pas l'empreinte.
	if !strings.Contains(trace, "o,") && !strings.Contains(trace, "o\n") {
		t.Errorf("aucune taille dans le journal :\n%s", trace)
	}
}

// TestJournalDistingueLesDeuxOrigines : pull et rattrapage écrivent le même
// contenu de la même façon, mais pas pour la même raison. Un journal qui les
// confond ne permet pas de distinguer un delta d'un rattrapage massif - ce qui
// est exactement la question ouverte sur le revert global du 10/08.
func TestJournalDistingueLesDeuxOrigines(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	var journal []string
	b.logf = func(f string, args ...any) { journal = append(journal, fmt.Sprintf(f, args...)) }

	writeFile(t, dirA, "equipe/plan.md", "origine\n")
	a.SyncOnce()
	b.SyncOnce()

	// Le fichier disparaît du disque de B sans que rien ne le sache : c'est le
	// rattrapage, pas le pull, qui doit le faire redescendre.
	if err := os.Remove(filepath.Join(dirB, "equipe", "plan.md")); err != nil {
		t.Fatal(err)
	}
	delete(b.state.Files, "equipe/plan.md")
	journal = nil
	b.SyncOnce()

	trace := strings.Join(journal, "\n")
	if !strings.Contains(trace, "écrit (rattrapage) : equipe/plan.md") {
		t.Errorf("le rattrapage n'est pas identifié comme tel :\n%s", trace)
	}
	if strings.Contains(trace, "écrit (pull) : equipe/plan.md") {
		t.Errorf("un rattrapage journalisé comme un pull :\n%s", trace)
	}
}

// TestEditionCroiseeFusionnableNeProduitAucuneCopie : le cas nominal du travail
// à deux. A et B modifient des lignes différentes du même fichier ; le merge à
// trois voies du serveur sait fusionner, et le canonique porte la fusion.
//
// Avant le 13/08, une copie de conflit sortait quand même, contenant une
// version que le fichier canonique portait DÉJÀ fusionnée : `pushLocal` jetait
// le résultat de l'envoi, et `preserveLocalEditIfAny`, quelques millisecondes
// plus tard dans le même cycle, prenait le contenu local pour une édition que
// personne n'avait reçue. Une copie par cycle et par fichier dès que deux
// membres travaillaient en même temps.
func TestEditionCroiseeFusionnableNeProduitAucuneCopie(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "ligne 1\nligne 2\nligne 3\n")
	a.SyncOnce()
	b.SyncOnce()

	writeFile(t, dirA, "equipe/plan.md", "ligne 1 (A)\nligne 2\nligne 3\n")
	a.SyncOnce()
	writeFile(t, dirB, "equipe/plan.md", "ligne 1\nligne 2\nligne 3 (B)\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	if got, _ := readFile(t, dirB, "equipe/plan.md"); got != "ligne 1 (A)\nligne 2\nligne 3 (B)\n" {
		t.Errorf("le canonique doit porter la fusion des deux éditions, obtenu %q", got)
	}
	if copies := copiesDeConflit(t, dirB, "equipe"); len(copies) != 0 {
		t.Errorf("un merge réussi ne doit produire aucune copie, obtenu %v", copies)
	}
}

// TestEditionJamaisPousseeGardeSaCopie : la non-régression. Quand le push n'a
// PAS eu lieu - serveur injoignable, espace en lecture seule, écriture arrivée
// après le scan - le contenu local n'est chez personne et la copie de conflit
// reste la seule chose qui le préserve.
func TestEditionJamaisPousseeGardeSaCopie(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "origine\n")
	a.SyncOnce()
	b.SyncOnce()
	writeFile(t, dirA, "equipe/plan.md", "serveur A\n")
	a.SyncOnce()

	// Pull seul, sans push : rien n'a été accepté par le serveur.
	writeFile(t, dirB, "equipe/plan.md", "jamais poussé\n")
	b.state.Files["equipe/plan.md"] = hashContent([]byte("origine\n"))
	if _, err := b.pull(b.state.Head, nil, nil); err != nil {
		t.Fatal(err)
	}

	trouve := false
	for _, nom := range copiesDeConflit(t, dirB, "equipe") {
		if c, ok := readFile(t, dirB, "equipe/"+nom); ok && c == "jamais poussé\n" {
			trouve = true
		}
	}
	if !trouve {
		t.Error("une édition jamais poussée doit être préservée en copie")
	}
}

// copiesDeConflit liste les copies de conflit d'un dossier, quel que soit leur
// format de nommage (local ou serveur).
func copiesDeConflit(t *testing.T, dir, sousDossier string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dir, sousDossier))
	var noms []string
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), "(conflit") {
			noms = append(noms, e.Name())
		}
	}
	return noms
}

// TestEcrisNeReecritPasUnFichierIdentique : `ecris` est idempotent.
//
// Un WriteFile inconditionnel touche le mtime, ce que fsnotify voit comme une
// modification : le daemon se réveille, relance un cycle, réécrit, se réveille.
// Mesuré sur la fenêtre du 13 au 15/08 : 186 des 249 lignes `écrit` du journal
// ne changeaient rien, soit 75 %, et la ligne annonçait pourtant une écriture -
// un journal qui ment sur ce qu'il a fait est pire qu'un journal muet, puisque
// c'est le seul canal dont on dispose pour reconstituer une perte.
func TestEcrisNeReecritPasUnFichierIdentique(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	var journal []string
	e.logf = func(f string, args ...any) { journal = append(journal, fmt.Sprintf(f, args...)) }

	writeFile(t, dir, "equipe/plan.md", "identique\n")
	abs := filepath.Join(dir, "equipe", "plan.md")
	avant, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}

	ecrit, err := e.ecris("equipe/plan.md", "identique\n", "pull")
	if err != nil {
		t.Fatal(err)
	}
	// Le contrat de ce retour est « le disque porte-t-il le contenu attendu »,
	// pas « ai-je appelé WriteFile » : rendre false ferait sauter la mise à jour
	// de l'état connu chez l'appelant, donc repousser au cycle suivant un
	// contenu que le serveur a déjà.
	if !ecrit {
		t.Error("`ecris` doit rendre true quand le disque porte déjà le contenu")
	}
	apres, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !apres.ModTime().Equal(avant.ModTime()) {
		t.Errorf("mtime touché sur un contenu identique (%v -> %v) : fsnotify va réveiller le daemon pour rien",
			avant.ModTime(), apres.ModTime())
	}

	trace := strings.Join(journal, "\n")
	if strings.Contains(trace, "écrit (pull)") {
		t.Errorf("le journal annonce une écriture qui n'a pas eu lieu :\n%s", trace)
	}
	if !strings.Contains(trace, "inchangé (pull) : equipe/plan.md") {
		t.Errorf("le no-op n'est pas journalisé :\n%s", trace)
	}
}

// TestEcrisReecritQuandLeContenuChange : le pendant du test ci-dessus. La garde
// d'idempotence ne doit pas avoir désarmé l'écriture elle-même.
func TestEcrisReecritQuandLeContenuChange(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	var journal []string
	e.logf = func(f string, args ...any) { journal = append(journal, fmt.Sprintf(f, args...)) }

	writeFile(t, dir, "equipe/plan.md", "avant\n")
	ecrit, err := e.ecris("equipe/plan.md", "après\n", "pull")
	if err != nil {
		t.Fatal(err)
	}
	if !ecrit {
		t.Fatal("le contenu a changé : l'écriture doit avoir lieu")
	}
	if got, _ := readFile(t, dir, "equipe/plan.md"); got != "après\n" {
		t.Errorf("contenu sur le disque = %q, attendu %q", got, "après\n")
	}
	if trace := strings.Join(journal, "\n"); !strings.Contains(trace, "écrit (pull) : equipe/plan.md") {
		t.Errorf("l'écriture n'est pas journalisée :\n%s", trace)
	}
}

// TestPullRespecteLeVecuignore : un chemin écarté par le .vecuignore ne descend
// pas, même quand il arrive par le delta.
//
// `rattrape` filtrait déjà, le pull non : le motif ne tenait que sur le chemin
// de rattrapage. Un fichier ignoré ici mais poussé par un autre membre
// atterrissait donc quand même sur ce poste, et - c'est le vrai dégât - le
// fichier local homonyme partait en copie de conflit, copie dont le nom n'est
// plus ignoré et que le push envoyait au serveur au cycle suivant.
func TestPullRespecteLeVecuignore(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirB, ".vecuignore", "equipe/brouillons\n")
	// Un fichier local sous le motif, avec un contenu à lui : c'est le candidat
	// à la copie de conflit.
	writeFile(t, dirB, "equipe/brouillons/note.md", "version locale, jamais partagée\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B initial : %v", err)
	}

	writeFile(t, dirA, "equipe/brouillons/note.md", "version de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B : %v", err)
	}

	if got, _ := readFile(t, dirB, "equipe/brouillons/note.md"); got != "version locale, jamais partagée\n" {
		t.Errorf("le fichier ignoré a été écrasé par la version serveur : %q", got)
	}
	if copies := copiesDeConflit(t, dirB, "equipe/brouillons"); len(copies) != 0 {
		t.Errorf("un chemin ignoré ne doit produire aucune copie de conflit, obtenu %v", copies)
	}
	if _, suivi := b.state.Files["equipe/brouillons/note.md"]; suivi {
		t.Error("un chemin ignoré ne doit pas rester dans l'état : le jour où le motif " +
			"est retiré, l'écart partirait comme une suppression chez tous les membres")
	}
	// Et le poste de A, qui n'ignore rien, garde bien le fichier : le motif est
	// local, il ne retire rien du serveur ni de chez les autres.
	if got, _ := readFile(t, dirA, "equipe/brouillons/note.md"); got != "version de A\n" {
		t.Errorf("le motif local de B a suivi le fichier chez A : %q", got)
	}
}

// TestMarquePageSeRemplitEtSuitLesFichiers : après un cycle, TOUTE entrée de
// `Files` a son marque-page. C'est l'invariant qui rend l'envoi capable de
// déclarer un ancêtre au lieu de le deviner.
func TestMarquePageSeRemplitEtSuitLesFichiers(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "un\n")
	writeFile(t, dirA, "equipe/notes/deux.md", "deux\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if len(a.state.Files) == 0 {
		t.Fatal("prérequis : le cycle n'a rien synchronisé")
	}
	for p := range a.state.Files {
		if _, ok := a.state.Versions[p]; !ok {
			t.Errorf("%s est dans Files sans marque-page : versions = %v", p, a.state.Versions)
		}
	}
	if len(a.state.Versions) != len(a.state.Files) {
		t.Errorf("versions (%d) et files (%d) divergent : %v", len(a.state.Versions), len(a.state.Files), a.state.Versions)
	}
}

// TestEtatAnterieurSeMigreAuPremierCycle : un `state.json` écrit par une
// version antérieure n'a aucun marque-page. Il doit se remplir seul, sans
// geste et sans resynchronisation.
//
// La migration est vraie par construction : ce que ce poste a sur le disque EST
// la version de son head, c'est ce que `Files` affirme.
func TestEtatAnterieurSeMigreAuPremierCycle(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "un\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	// On revient à un état de v0.4.1 : les empreintes, pas les marque-pages.
	head := a.state.Head
	a.state.Versions = nil

	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle de migration : %v", err)
	}
	for p := range a.state.Files {
		if _, ok := a.state.Versions[p]; !ok {
			t.Errorf("%s n'a pas été migré : versions = %v", p, a.state.Versions)
		}
	}
	if got := a.state.Versions["equipe/plan.md"]; got != head {
		t.Errorf("marque-page migré = %q, attendu le head d'avant le cycle (%q)", court(got), court(head))
	}
}

// TestMarquePageNeSurvitPasALaSortieDeSonChemin : un marque-page qui reste
// après la sortie de son chemin devient immortel - plus personne ne le
// réconcilie, et il fait fusionner depuis un commit arbitrairement vieux le
// jour où le chemin revient.
//
// Vérifié sur l'UNION `Files` ∪ `Versions` : le cas qui échappe à une purge
// écrite sur `Files` seul est justement celui d'un marque-page dont le chemin
// n'a plus d'empreinte.
func TestMarquePageNeSurvitPasALaSortieDeSonChemin(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "un\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	// Un marque-page orphelin : son chemin n'a pas (ou plus) d'empreinte, et le
	// serveur ne le connaît pas. Rien d'autre que la purge sur l'union ne peut
	// le voir.
	a.noteVersion("equipe/disparu.md", a.state.Head)

	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if v, ok := a.state.Versions["equipe/disparu.md"]; ok {
		t.Errorf("marque-page orphelin conservé (%q) : il survivra à son chemin", court(v))
	}
	if _, ok := a.state.Versions["equipe/plan.md"]; !ok {
		t.Errorf("la purge a emporté un marque-page encore dans le périmètre")
	}

	// Motif .vecuignore : le chemin sort du périmètre de ce poste, les deux maps
	// doivent le lâcher ensemble.
	writeFile(t, dirA, ".vecuignore", "plan.md\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.state.Files["equipe/plan.md"]; ok {
		t.Errorf("prérequis : le motif aurait dû sortir le chemin de Files")
	}
	if v, ok := a.state.Versions["equipe/plan.md"]; ok {
		t.Errorf("le marque-page a survécu au motif .vecuignore : %q", court(v))
	}
}

// TestEtatSeChargeDansLesDeuxSens : la fenêtre de bascule des deux postes. Un
// état écrit par v0.4.1 (sans le champ) se charge ici, et un état écrit ici se
// charge sur v0.4.1 (champ inconnu, ignoré par encoding/json).
func TestEtatSeChargeDansLesDeuxSens(t *testing.T) {
	dir := t.TempDir()

	// Sens 1 : un état d'avant, sans le champ.
	if err := os.MkdirAll(filepath.Join(dir, vecuDir), 0o755); err != nil {
		t.Fatal(err)
	}
	ancien := `{"head":"abc","files":{"equipe/a.md":"deadbeef"},"espaces":["equipe"]}`
	if err := os.WriteFile(filepath.Join(dir, vecuDir, "state.json"), []byte(ancien), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("un état sans marque-page ne se charge pas : %v", err)
	}
	if st.Versions != nil {
		t.Errorf("Versions devrait être absente, pas vide : %v", st.Versions)
	}
	if st.Files["equipe/a.md"] != "deadbeef" {
		t.Errorf("le reste de l'état n'a pas survécu : %+v", st)
	}

	// Sens 2 : un état d'ici, relu par le décodeur de v0.4.1 - qui ne connaît
	// pas le champ. `encoding/json` ignore les clés inconnues.
	// Source: https://pkg.go.dev/encoding/json#Unmarshal
	st.Versions = map[string]string{"equipe/a.md": "abc"}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	brut, err := os.ReadFile(filepath.Join(dir, vecuDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var avant struct {
		Head  string            `json:"head"`
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(brut, &avant); err != nil {
		t.Fatalf("v0.4.1 ne saurait plus lire cet état : %v", err)
	}
	if avant.Head != "abc" || avant.Files["equipe/a.md"] != "deadbeef" {
		t.Errorf("v0.4.1 lirait un état amputé : %+v", avant)
	}
}

// TestMigrationNeDonnePasLeHeadAUnCheminObstrue : la migration ne doit pas
// reconstruire la perte qu'elle répare.
//
// Un poste dont le head a DÉJÀ franchi une écriture refusée - toute la base
// installée au moment où cette correction sort - porte dans `Files` une version
// ANTÉRIEURE à son head. Lui recopier le head lui ferait déclarer au serveur un
// ancêtre qu'il ne contient pas : fusion propre, écrasement muet.
//
// Le sens du test est celui qui perd des données : trop vieux est sûr, trop
// récent est la perte.
func TestMigrationNeDonnePasLeHeadAUnCheminObstrue(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "ligne commune\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	planB := filepath.Join(dirB, "equipe", "plan.md")
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dirB, "ailleurs.md"), planB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/plan.md", "ligne commune\nligne de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (refus) : %v", err)
	}

	// L'état d'un poste en v0.4.1 au moment de la bascule : les empreintes et le
	// rapport du dernier cycle, aucun marque-page. Relu DEPUIS LE DISQUE, comme
	// le fera le poste réel.
	if err := b.Close(); err != nil { // un seul moteur par dossier (flock)
		t.Fatal(err)
	}
	st, err := LoadState(dirB)
	if err != nil {
		t.Fatal(err)
	}
	st.Versions = nil
	if err := st.Save(dirB); err != nil {
		t.Fatal(err)
	}
	b2, err := NewEngine(dirB, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if b2.state.Versions != nil {
		t.Fatal("prérequis : l'état rechargé ne doit porter aucun marque-page")
	}
	if _, connu := b2.state.Files["equipe/plan.md"]; !connu {
		t.Fatal("prérequis : le chemin obstrué doit rester connu de Files")
	}

	if err := b2.SyncOnce(); err != nil {
		t.Fatalf("cycle de migration : %v", err)
	}
	got, ok := b2.state.Versions["equipe/plan.md"]
	if !ok {
		t.Fatalf("invariant rompu : un chemin de Files sans marque-page — %v", b2.state.Versions)
	}
	if got == b2.state.Head {
		t.Errorf("la migration a donné le head (%s) à un chemin obstrué : il décrit une version que ce poste n'a jamais reçue", court(got))
	}
	if got != "" {
		t.Errorf("marque-page d'un chemin obstrué = %q, attendu la chaîne vide (fusion sans base commune, donc copie)", court(got))
	}
	// Et l'invariant tient sur tout le reste : la garde ne doit pas creuser de
	// trou ailleurs.
	for p := range b2.state.Files {
		if _, ok := b2.state.Versions[p]; !ok {
			t.Errorf("%s est dans Files sans marque-page", p)
		}
	}
}

// TestRefusDEcritureNeReverteRienEnSilence : une écriture refusée ne doit pas
// faire disparaître la modification d'un autre membre.
//
// Quand une écriture du pull est refusée - un lien ou un dossier occupe le
// chemin, un composant est devenu un fichier - le disque ne reçoit PAS cette
// version. Avancer le head quand même faisait déclarer au push suivant un
// ancêtre qu'il ne contient pas : le serveur lisait la différence comme un
// retrait volontaire et la modification de l'autre disparaissait, sans conflit
// ni copie, sans une ligne de journal.
func TestRefusDEcritureNeReverteRienEnSilence(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "ligne commune\n")
	a.SyncOnce()
	b.SyncOnce()

	// Un lien occupe maintenant le chemin sur le poste de B : le moteur refuse
	// d'écrire à travers (il n'écrit jamais hors de ce qu'il contrôle).
	planB := filepath.Join(dirB, "equipe", "plan.md")
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dirB, "ailleurs.md"), planB); err != nil {
		t.Fatal(err)
	}

	// A modifie le fichier pendant ce temps.
	writeFile(t, dirA, "equipe/plan.md", "ligne commune\nligne de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// B synchronise : l'écriture est refusée, le disque de B ne reçoit rien.
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (refus) : %v", err)
	}
	refus := false
	for _, l := range b.Laisses() {
		if strings.Contains(l.Chemin, "plan.md") {
			refus = true
		}
	}
	if !refus {
		t.Fatalf("prérequis : l'écriture aurait dû être refusée et signalée, laisses = %+v", b.Laisses())
	}

	// L'obstacle est levé et B repart de la version qu'il détenait VRAIMENT,
	// celle d'avant la modification de A, sur laquelle il ajoute la sienne.
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "ligne commune\nligne de B\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (push) : %v", err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A final : %v", err)
	}

	final, _ := readFile(t, dirA, "equipe/plan.md")
	t.Logf("contenu final chez A :\n%s", final)
	if !strings.Contains(final, "ligne de A") {
		copies := copiesDeConflit(t, dirA, "equipe")
		t.Errorf("la modification de A a disparu du canonique sans conflit ni copie (copies = %v) : %q", copies, final)
	}
	// B a écrit sur le même point d'ancrage que A : le désaccord est réel, et la
	// copie de conflit est la bonne réponse. Ce qui compte est qu'il soit VISIBLE
	// quelque part, pas qu'il soit au nom canonique.
	if !visibleQuelquePart(t, dirA, "equipe", "equipe/plan.md", "ligne de B") {
		t.Errorf("la modification de B n'est ni au canonique ni dans une copie : copies = %v",
			copiesDeConflit(t, dirA, "equipe"))
	}
}

// TestDeuxCyclesApresConflitNeReverteRien : la perte ne doit pas être DÉCALÉE
// d'un cycle.
//
// C'est le test qui a montré l'échec de la tentative précédente. Après un envoi
// que le serveur range en copie de conflit, l'entrée connue de ce chemin décrit
// une version périmée. La laisser telle quelle fait revoir notre contenu comme
// un changement à pousser au cycle suivant - cette fois avec un marque-page sur
// le head courant, donc par le chemin rapide du serveur, donc en écrasant
// directement la version de l'autre. Quinze secondes de poll plus tard.
func TestDeuxCyclesApresConflitNeReverteRien(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "titre\ncorps\n")
	a.SyncOnce()
	b.SyncOnce()

	planB := filepath.Join(dirB, "equipe", "plan.md")
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dirB, "ailleurs.md"), planB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/plan.md", "titre de A\ncorps\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (refus) : %v", err)
	}

	// B lève l'obstacle et écrit au MÊME point d'ancrage que A : le désaccord est
	// réel, le serveur doit ranger l'un des deux en copie.
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "titre de B\ncorps\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (conflit) : %v", err)
	}

	// LE point du test : les cycles qui suivent, sans aucun geste local.
	for i := range 2 {
		if err := b.SyncOnce(); err != nil {
			t.Fatalf("cycle B n°%d après le conflit : %v", i+2, err)
		}
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle A n°%d après le conflit : %v", i+2, err)
		}
		if !visibleQuelquePart(t, dirA, "equipe", "equipe/plan.md", "titre de A") {
			t.Fatalf("cycle %d : la modification de A a disparu, copies = %v",
				i+2, copiesDeConflit(t, dirA, "equipe"))
		}
		if !visibleQuelquePart(t, dirB, "equipe", "equipe/plan.md", "titre de A") {
			t.Fatalf("cycle %d : la modification de A a disparu du poste de B, copies = %v",
				i+2, copiesDeConflit(t, dirB, "equipe"))
		}
	}
	if !visibleQuelquePart(t, dirA, "equipe", "equipe/plan.md", "titre de B") {
		t.Errorf("la modification de B n'est nulle part chez A : copies = %v", copiesDeConflit(t, dirA, "equipe"))
	}
}

// TestPosteDejaObstrueConvergeSansPerte : un poste dont le head a DÉJÀ franchi
// un refus - c'est-à-dire toute la base installée au moment où cette correction
// sort - doit converger sans geste manuel et sans rien perdre.
//
// Le delta ne repassera plus jamais sur ce chemin : le head l'a franchi. Ce qui
// le rattrape est la migration, qui refuse de donner le head à un chemin que le
// rapport du dernier cycle signalait.
func TestPosteDejaObstrueConvergeSansPerte(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "ligne commune\n")
	a.SyncOnce()
	b.SyncOnce()

	planB := filepath.Join(dirB, "equipe", "plan.md")
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dirB, "ailleurs.md"), planB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/plan.md", "ligne commune\nligne de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	// L'état d'un poste mis à jour APRÈS coup : les empreintes et le rapport du
	// dernier cycle, aucun marque-page.
	b.state.Versions = nil

	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle de migration : %v", err)
	}
	got, ok := b.state.Versions["equipe/plan.md"]
	if !ok {
		// Une map nil rend "" elle aussi : sans cette assertion, le test passerait
		// à l'identique que la migration ait tourné ou non.
		t.Fatalf("la migration n'a pas tourné : versions = %v", b.state.Versions)
	}
	if got == b.state.Head {
		t.Errorf("le marque-page est le head courant (%s) : il décrit une version que ce poste n'a jamais reçue", court(got))
	}

	// Et la preuve par le comportement : B lève l'obstacle, repart de ce qu'il
	// détenait, pousse. La modification de A doit rester visible quelque part.
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "ligne commune\nligne de B\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle de push : %v", err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if !visibleQuelquePart(t, dirA, "equipe", "equipe/plan.md", "ligne de A") {
		t.Errorf("la modification de A a disparu sans conflit ni copie : copies = %v",
			copiesDeConflit(t, dirA, "equipe"))
	}
}

// TestPremierCycleObstrueNeDeclareAucunAncetre : sur un poste neuf dont un
// dossier personnel occupe déjà le chemin, rien n'est jamais arrivé sur le
// disque - donc aucun ancêtre ne peut être nommé, et l'envoi doit tomber sur la
// chaîne vide. Côté serveur : fusion sans base commune, donc copie de conflit.
func TestPremierCycleObstrueNeDeclareAucunAncetre(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/doc.md", "contenu de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	// Poste neuf, dont un dossier personnel occupe déjà le chemin.
	writeFile(t, dirB, "equipe/doc.md/dedans.md", "à moi\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("premier cycle de B : %v", err)
	}
	if got := b.ancetreDe("equipe/doc.md"); got != "" {
		t.Fatalf("ancêtre déclaré = %q sur un chemin jamais reçu : attendu la chaîne vide", court(got))
	}

	// B lève l'obstacle et pose son propre fichier : le contenu de A ne doit pas
	// disparaître en silence.
	if err := os.RemoveAll(filepath.Join(dirB, "equipe", "doc.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/doc.md", "contenu de B\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle de push : %v", err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if !visibleQuelquePart(t, dirA, "equipe", "equipe/doc.md", "contenu de A") {
		t.Errorf("le contenu de A a disparu sans conflit ni copie : copies = %v",
			copiesDeConflit(t, dirA, "equipe"))
	}
}

// TestCheminJamaisRecuNeDeclareJamaisLeHead : le piège central, et il ne se voit
// pas sur un poste neuf.
//
// Sur un poste INSTALLÉ, le head décrit un dépôt où le chemin existe déjà, avec
// un contenu que le disque n'a jamais vu. Le déclarer revient à dire « je
// détenais cette version et je la modifie » : le chemin n'ayant pas bougé côté
// serveur depuis, la fusion est PROPRE et le contenu des autres est écrasé, sans
// conflit ni copie.
func TestCheminJamaisRecuNeDeclareJamaisLeHead(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	// B est un poste INSTALLÉ : il a déjà un head non vide.
	writeFile(t, dirA, "equipe/autre.md", "sans rapport\n")
	a.SyncOnce()
	b.SyncOnce()
	if b.state.Head == "" {
		t.Fatal("prérequis : le head de B doit être non vide")
	}

	// Un lien occupe un chemin que B n'a jamais reçu, et A y dépose du contenu.
	noteB := filepath.Join(dirB, "equipe", "note.md")
	if err := os.MkdirAll(filepath.Dir(noteB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dirB, "ailleurs.md"), noteB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/note.md", "partagé par A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (refus) : %v", err)
	}
	if got := b.ancetreDe("equipe/note.md"); got != "" {
		t.Errorf("ancêtre déclaré = %q sur un chemin jamais reçu : attendu la chaîne vide", court(got))
	}

	// B lève l'obstacle et pose SON contenu au même chemin.
	if err := os.Remove(noteB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/note.md", "ma version\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (push) : %v", err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if !visibleQuelquePart(t, dirA, "equipe", "equipe/note.md", "partagé par A") {
		t.Errorf("le contenu de A a disparu sans conflit ni copie : copies = %v",
			copiesDeConflit(t, dirA, "equipe"))
	}
}

// TestObstructionSoloNeProduitAucuneCopie : une obstruction SANS aucun désaccord
// ne doit produire aucune copie de conflit.
//
// C'est ce qui a fait échouer la tentative précédente : elle figeait la chaîne
// vide à chaque cycle sur un chemin obstrué, si bien qu'un membre seul, que
// personne ne contredisait, fabriquait une copie et faisait reculer le canonique
// d'un cycle chez tout le monde. Un marque-page ÉCRIT à la réception, lui, reste
// exact : la fusion est propre et silencieuse.
func TestObstructionSoloNeProduitAucuneCopie(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/solo.md", "version initiale\n")
	a.SyncOnce()
	b.SyncOnce()

	// Un dossier occupe le chemin sur le poste de B, et personne ne touche à
	// `solo.md`. A travaille sur d'AUTRES fichiers : le head avance, donc il ne
	// sera plus jamais égal au marque-page de `solo.md`.
	soloB := filepath.Join(dirB, "equipe", "solo.md")
	if err := os.Remove(soloB); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(soloB, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		writeFile(t, dirA, fmt.Sprintf("equipe/ailleurs-%d.md", i), "du travail\n")
		if err := a.SyncOnce(); err != nil {
			t.Fatal(err)
		}
		if err := b.SyncOnce(); err != nil {
			t.Fatalf("cycle B n°%d : %v", i, err)
		}
	}

	// B retire le dossier et pose SON fichier au même chemin, sans que personne
	// n'ait touché à ce contenu entre-temps.
	if err := os.RemoveAll(soloB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/solo.md", "version initiale\nsuite écrite par B\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (push) : %v", err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	if copies := copiesDeConflit(t, dirA, "equipe"); len(copies) != 0 {
		t.Errorf("une obstruction sans désaccord a fabriqué %d copie(s) : %v", len(copies), copies)
	}
	final, _ := readFile(t, dirA, "equipe/solo.md")
	if !strings.Contains(final, "suite écrite par B") {
		t.Errorf("le travail de B n'est pas au canonique : %q", final)
	}
}

// visibleQuelquePart : `attendu` est-il lisible au nom canonique, ou dans l'une
// des copies de conflit du dossier ?
//
// L'arbitrage - qui garde le nom canonique - est hors sujet ici. Ce qui doit
// être vrai, c'est qu'aucun contenu ne DISPARAÎT.
func visibleQuelquePart(t *testing.T, dir, sousDossier, canonique, attendu string) bool {
	t.Helper()
	// os.ReadFile direct et erreur ignorée : après une bascule fichier -> dossier,
	// le chemin canonique EST un dossier. « pas lisible ici » est une réponse
	// valide à la question posée, pas un échec de test.
	if c, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(canonique))); err == nil && strings.Contains(string(c), attendu) {
		return true
	}
	for _, nom := range copiesDeConflit(t, dir, sousDossier) {
		if c, ok := readFile(t, dir, sousDossier+"/"+nom); ok && strings.Contains(c, attendu) {
			return true
		}
	}
	return false
}

// TestMarquePageIrresoluNeBloquePasLeChemin : un marque-page que le dépôt ne
// résout plus - sauvegarde restaurée, dépôt réinitialisé, serveur changé - ferait
// échouer l'envoi de ce chemin à chaque cycle, indéfiniment, sous une alerte
// « ce fichier n'existe que sur ce poste » qui serait fausse.
//
// Le repli est la chaîne vide, ÉCRITE et pas retirée : une entrée absente de
// `Versions` alors que `Files` la porte est le seul état que `migreVersions` ne
// sait pas distinguer d'un état écrit par une version antérieure - elle
// redistribuerait le head à tout le monde.
func TestMarquePageIrresoluNeBloquePasLeChemin(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "un\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	// Un commit bien formé que ce dépôt ne contient pas.
	a.state.Versions["equipe/plan.md"] = "0123456789abcdef0123456789abcdef01234567"

	writeFile(t, dirA, "equipe/plan.md", "un\ndeux\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("un ancêtre irrésolu ne doit pas faire échouer le cycle : %v", err)
	}
	got, ok := a.state.Versions["equipe/plan.md"]
	if !ok {
		t.Fatalf("le marque-page a été RETIRÉ au lieu d'être vidé : Files le porte encore, "+
			"un rechargement rejouerait la migration — versions = %v", a.state.Versions)
	}
	if got != "" {
		t.Errorf("marque-page après refus = %q, attendu la chaîne vide", court(got))
	}

	// Et le chemin repart : le cycle suivant pousse sans ancêtre, le serveur
	// accepte, et le contenu local est bien au canonique.
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle de reprise : %v", err)
	}
	b, dirB := newEngine(t, url, token)
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if !visibleQuelquePart(t, dirB, "equipe", "equipe/plan.md", "deux") {
		t.Errorf("le contenu local n'a jamais atteint le serveur : copies = %v", copiesDeConflit(t, dirB, "equipe"))
	}
}

// TestEspaceNeufNAtterritJamaisSansMarquePage : le contenu d'un espace
// fraîchement monté n'arrive pas par le delta - il était hors périmètre - mais
// par le rattrapage, qui le télécharge fichier par fichier. Chacun doit repartir
// avec le commit d'où il sort, sinon le premier envoi de ce poste sur cet espace
// déclarerait un ancêtre supposé.
func TestEspaceNeufNAtterritJamaisSansMarquePage(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, _ := newEngine(t, url, token)

	for i := range 4 {
		writeFile(t, dirA, fmt.Sprintf("equipe/f%d.md", i), fmt.Sprintf("contenu %d\n", i))
		if err := a.SyncOnce(); err != nil { // un commit par fichier : le head bouge
			t.Fatal(err)
		}
	}

	// B monte l'espace pour la première fois : tout descend par le rattrapage.
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("premier cycle de B : %v", err)
	}
	if len(b.state.Files) < 4 {
		t.Fatalf("prérequis : l'espace n'est pas descendu — files = %v", b.state.Files)
	}
	for p := range b.state.Files {
		v, ok := b.state.Versions[p]
		if !ok || v == "" {
			t.Errorf("%s a atterri sans marque-page (présent=%v, valeur=%q)", p, ok, v)
		}
	}
}

// TestLeGetDateCeQuIlSert : le commit rendu par le GET doit être celui d'où sort
// le contenu, pas une approximation postérieure. Vérifié en lisant le fichier au
// commit annoncé.
func TestLeGetDateCeQuIlSert(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/date.md", "v1\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/date.md", "v2\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	contenu, commit, err := a.client.Read("equipe/date.md")
	if err != nil {
		t.Fatal(err)
	}
	if commit == "" {
		t.Fatal("le GET ne date pas ce qu'il sert")
	}
	if contenu != "v2\n" {
		t.Errorf("contenu = %q", contenu)
	}
	// Le contrat porte sur le COUPLE (contenu, commit), pas sur chaque moitié :
	// le commit annoncé doit être celui où ce contenu est courant. On le vérifie
	// contre l'état complet du dépôt, qui rend head et contenus ensemble.
	etat, err := a.client.Sync("")
	if err != nil {
		t.Fatal(err)
	}
	if commit != etat.Head {
		t.Errorf("le GET annonce %s alors que le dépôt est à %s", court(commit), court(etat.Head))
	}
	trouve := false
	for _, ch := range etat.Changes {
		if ch.Path == "equipe/date.md" {
			trouve = true
			if ch.Content != contenu {
				t.Errorf("à %s le fichier vaut %q, mais le GET a servi %q", court(commit), ch.Content, contenu)
			}
		}
	}
	if !trouve {
		t.Errorf("le chemin est absent de l'état à %s", court(commit))
	}
}

// TestBasculeFichierDossierAvecModificationConcurrente : le geste le plus banal
// d'un vault - renommer un fichier en dossier du même nom - doit passer même
// quand un autre membre modifie ce fichier au même moment. Le dossier passe, ET
// la version modifiée existe dans une copie.
//
// Avant, la suppression était abandonnée sur conflit : le fichier restait en
// place, git refusait un dossier du même nom, et le chemin gelait indéfiniment.
func TestBasculeFichierDossierAvecModificationConcurrente(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "version initiale\n")
	a.SyncOnce()
	b.SyncOnce()

	// A modifie le fichier ; B en fait un dossier, au même moment.
	writeFile(t, dirA, "equipe/plan.md", "version initiale\nmodifiée par A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dirB, "equipe", "plan.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md/dedans.md", "contenu du dossier\n")

	// Convergence en 3 cycles MAXIMUM : on note celui où elle a lieu, pour qu'une
	// régression de 1 à 3 cycles ne passe pas inaperçue derrière une boucle qui
	// n'assère qu'à la fin.
	converge := 0
	for i := range 3 {
		if err := b.SyncOnce(); err != nil {
			t.Fatalf("cycle B n°%d : %v", i+1, err)
		}
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle A n°%d : %v", i+1, err)
		}
		if _, ok := readFile(t, dirA, "equipe/plan.md/dedans.md"); ok && converge == 0 {
			converge = i + 1
		}
	}
	if converge != 1 {
		t.Errorf("convergence au cycle %d (attendu 1) : régression du nombre de cycles", converge)
	}
	// Une seule copie pour une seule divergence.
	if copies := copiesDeConflit(t, dirA, "equipe"); len(copies) != 1 {
		t.Errorf("attendu exactement une copie, obtenu %d : %v", len(copies), copies)
	}

	// 1. Le dossier passe : son contenu est arrivé chez A.
	if got, ok := readFile(t, dirA, "equipe/plan.md/dedans.md"); !ok || !strings.Contains(got, "contenu du dossier") {
		t.Errorf("la bascule fichier -> dossier n'est pas passée : dedans.md présent=%v contenu=%q", ok, got)
	}
	// 2. Et la modification de A n'a pas disparu.
	if !visibleQuelquePart(t, dirA, "equipe", "equipe/plan.md", "modifiée par A") {
		t.Errorf("la modification de A a disparu : copies = %v", copiesDeConflit(t, dirA, "equipe"))
	}
	// 3. Les deux postes voient la même chose.
	if !visibleQuelquePart(t, dirB, "equipe", "equipe/plan.md", "modifiée par A") {
		t.Errorf("les postes n'ont pas convergé : la modification de A est absente chez B, copies = %v",
			copiesDeConflit(t, dirB, "equipe"))
	}
}

// TestObstacleLeveSansRienRecreerNeSupprimePourPersonne : l'autre sortie du refus
// d'écriture.
//
// Le moteur demande lui-même de retirer le lien ou le dossier en travers. Si
// l'utilisateur le retire SANS rien remettre à la place, le chemin devient une
// suppression constatée - et cette suppression partait du head, donc effaçait la
// modification de l'autre membre pour tout le monde, sans conflit ni copie.
func TestObstacleLeveSansRienRecreerNeSupprimePourPersonne(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "version initiale\n")
	a.SyncOnce()
	b.SyncOnce()

	planB := filepath.Join(dirB, "equipe", "plan.md")
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dirB, "ailleurs.md"), planB); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/plan.md", "version initiale\nmodifiée par A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (refus) : %v", err)
	}

	// L'obstacle est levé, et RIEN n'est remis à la place.
	if err := os.Remove(planB); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := b.SyncOnce(); err != nil {
			t.Fatalf("cycle B n°%d : %v", i+1, err)
		}
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle A n°%d : %v", i+1, err)
		}
	}

	// 1. La suppression a PRIS EFFET : sans cette assertion, le test passait aussi
	// avec un serveur qui abandonne la suppression sur conflit - le fichier
	// restait au canonique, ce que `visibleQuelquePart` acceptait.
	if _, ok := readFile(t, dirA, "equipe/plan.md"); ok {
		t.Errorf("la suppression n'a pas été appliquée : le fichier est toujours au canonique chez A")
	}
	// 2. Et la modification de A est dans une copie, chez les deux membres.
	if !visibleQuelquePart(t, dirA, "equipe", "equipe/plan.md", "modifiée par A") {
		t.Errorf("la modification de A a été effacée pour tout le monde : copies = %v",
			copiesDeConflit(t, dirA, "equipe"))
	}
	if !visibleQuelquePart(t, dirB, "equipe", "equipe/plan.md", "modifiée par A") {
		t.Errorf("les postes n'ont pas convergé : la modification de A est absente chez B, copies = %v",
			copiesDeConflit(t, dirB, "equipe"))
	}
}

// TestSuppressionSoloNeProduitAucuneCopie : le pendant du test d'obstruction
// solo, côté suppression.
//
// Depuis que la suppression range la version de main dans une copie sur
// divergence, il faut vérifier qu'elle ne le fait PAS quand il n'y a aucun
// désaccord - sinon le geste le plus banal (supprimer un fichier) rendrait ce
// fichier à toute l'équipe sous un nom de conflit.
func TestSuppressionSoloNeProduitAucuneCopie(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/jetable.md", "à supprimer\n")
	a.SyncOnce()
	b.SyncOnce()

	// Le head avance sur d'autres chemins : le marque-page de `jetable.md` ne lui
	// est donc plus égal, et la suppression ne peut plus passer par le chemin
	// rapide du serveur.
	for i := range 3 {
		writeFile(t, dirA, fmt.Sprintf("equipe/autre-%d.md", i), "du travail\n")
		if err := a.SyncOnce(); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(dirB, "equipe", "jetable.md")); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle de suppression : %v", err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	if copies := copiesDeConflit(t, dirA, "equipe"); len(copies) != 0 {
		t.Errorf("une suppression sans désaccord a fabriqué %d copie(s) : %v", len(copies), copies)
	}
	if _, ok := readFile(t, dirA, "equipe/jetable.md"); ok {
		t.Errorf("la suppression n'a pas été propagée à A")
	}
}

// TestRegistreHorsPerimetreFermeLeDeluge : le classement (slice 1) corrige le
// code de sortie, PAS le journal. Le déluge a sa propre cause.
//
// Un binaire refusé faisait `continue` sans jamais entrer dans aucun registre.
// Il restait donc éternellement « présent sur le disque et différent du connu »,
// donc relu, re-refusé et re-journalisé à chaque cycle : sur le vault de Colin,
// 298 binaires × un cycle toutes les ~35 s = 17,6 millions de lignes et 3,3 Go
// depuis le 26/07, soit 99,98 % du fichier. L'instrument de diagnostic sur
// lequel on compterait si la perte du 10/08 revenait était devenu illisible.
func TestRegistreHorsPerimetreFermeLeDeluge(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	var journal []string
	e.logf = func(f string, args ...any) { journal = append(journal, fmt.Sprintf(f, args...)) }

	const rel = "equipe/image.png"
	abs := filepath.Join(dir, filepath.FromSlash(rel))

	// cycle rend les lignes de journal que CE cycle a écrites sur le hors-périmètre.
	cycle := func(quoi string) []string {
		t.Helper()
		journal = nil
		if err := e.SyncOnce(); err != nil {
			t.Fatalf("%s : %v", quoi, err)
		}
		var dites []string
		for _, l := range journal {
			if strings.Contains(l, "hors périmètre") {
				dites = append(dites, l)
			}
		}
		return dites
	}

	writeFile(t, dir, "equipe/note.md", "texte ok\n")
	if err := os.WriteFile(abs, []byte{0xff, 0xd8, 0x00, 0x9f}, 0o644); err != nil {
		t.Fatal(err)
	}

	// Cycle 1 : le binaire se dit UNE fois. Le silence est le vrai danger d'un
	// outil de sync - il doit se dire. Une fois.
	if dites := cycle("cycle 1"); len(dites) != 1 {
		t.Fatalf("cycle 1 : attendu 1 ligne, obtenu %d : %v", len(dites), dites)
	}
	if e.state.HorsPerimetre[rel] == "" {
		t.Fatalf("le binaire n'est pas entré au registre : %+v", e.state.HorsPerimetre)
	}
	// L'INVARIANT de cette slice. `Files` signifie « synchronisé à cette
	// empreinte » : y écrire un binaire que le serveur n'a pas serait un mensonge,
	// et il se paierait sur la détection de suppression - le chemin passerait pour
	// un fichier connu disparu, donc une suppression partirait chez tout le monde.
	if _, y := e.state.Files[rel]; y {
		t.Fatalf("un hors-périmètre est entré dans Files : %+v", e.state.Files)
	}

	// Cycles suivants, contenu inchangé : silence complet. C'est le critère qui
	// vaut 140 Mo de journal par jour.
	for i := 2; i <= 4; i++ {
		if dites := cycle(fmt.Sprintf("cycle %d", i)); len(dites) != 0 {
			t.Errorf("cycle %d : le binaire s'est redit alors qu'il n'a pas bougé : %v", i, dites)
		}
	}

	// Un binaire MODIFIÉ se redit, une fois. C'est pour ça que le registre est
	// indexé par empreinte et n'est pas un simple ensemble de chemins.
	if err := os.WriteFile(abs, []byte{0xff, 0xd8, 0x00, 0x42, 0x13}, 0o644); err != nil {
		t.Fatal(err)
	}
	if dites := cycle("après modification"); len(dites) != 1 {
		t.Errorf("un binaire modifié doit se redire exactement une fois, obtenu %d : %v", len(dites), dites)
	}
	if dites := cycle("cycle suivant la modification"); len(dites) != 0 {
		t.Errorf("puis se taire : %v", dites)
	}

	// Le fichier reste INTACT sur le disque, dans tous les cas. Vécu ne supprime
	// rien ici.
	if b, err := os.ReadFile(abs); err != nil || len(b) != 5 {
		t.Errorf("le binaire a été touché : %v %d octets", err, len(b))
	}

	// Le registre sort avec le fichier : une entrée qui survit à son chemin est
	// immortelle, et un binaire recréé à l'identique ne se dirait plus jamais.
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SyncOnce(), error(nil); err != nil {
		t.Fatal(err)
	}
	if _, encore := e.state.HorsPerimetre[rel]; encore {
		t.Errorf("le fichier a disparu, son entrée est restée : %+v", e.state.HorsPerimetre)
	}
	// Recréé à l'identique : il se redit, preuve que la purge n'a pas juste
	// masqué l'entrée.
	if err := os.WriteFile(abs, []byte{0xff, 0xd8, 0x00, 0x9f}, 0o644); err != nil {
		t.Fatal(err)
	}
	if dites := cycle("après recréation"); len(dites) != 1 {
		t.Errorf("un binaire recréé doit se redire une fois, obtenu %d : %v", len(dites), dites)
	}
}

// TestBasculeBinaireTexteDansLesDeuxSens : le registre est indexé par empreinte
// justement pour ça. Un `.png` réenregistré en texte, ou un `.md` remplacé par
// une image au même nom, change d'empreinte - donc le chemin est réévalué au
// lieu d'être classé une fois pour toutes.
func TestBasculeBinaireTexteDansLesDeuxSens(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)

	const rel = "equipe/piece"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	binaire := []byte{0xff, 0xd8, 0x00, 0x9f}
	writeFile(t, dir, "equipe/note.md", "de quoi créer le dossier de l'espace\n")

	// Binaire d'abord : au registre, jamais dans Files.
	if err := os.WriteFile(abs, binaire, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, y := e.state.HorsPerimetre[rel]; !y {
		t.Fatalf("binaire absent du registre : %+v", e.state.HorsPerimetre)
	}

	// Devient du texte : il part au serveur, et QUITTE le registre. Sans cette
	// purge, le jour où il redevient binaire, plus rien ne le dirait.
	writeFile(t, dir, rel, "c'est devenu du texte\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, encore := e.state.HorsPerimetre[rel]; encore {
		t.Errorf("le chemin est passé au texte, il est resté au registre : %+v", e.state.HorsPerimetre)
	}
	if e.state.Files[rel] == "" {
		t.Errorf("le texte n'a pas été synchronisé : %+v", e.state.Files)
	}

	// Et redevient binaire : re-refusé, re-enregistré. `Files` garde l'empreinte
	// du texte, qui est bien ce que le serveur a - ce n'est pas un mensonge.
	if err := os.WriteFile(abs, binaire, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, y := e.state.HorsPerimetre[rel]; !y {
		t.Fatalf("redevenu binaire, absent du registre : %+v", e.state.HorsPerimetre)
	}
}

// empreintesDe : sha256 de chaque fichier sous `racine`, chemin relatif -> hex.
// Une comparaison de PRÉSENCE ne prouverait rien : un fichier tronqué, vidé ou
// réécrit est toujours présent.
func empreintesDe(t *testing.T, racine string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(racine, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(racine, p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatalf("empreintes de %s : %v", racine, err)
	}
	return out
}

// LE test du lot. « Retirer ce dossier de la synchronisation » ne doit effacer
// AUCUN fichier - c'est la promesse écrite de F1, et le chemin de démontage
// ordinaire supprime justement les fichiers propres.
//
// La différence avec TestDemontagePreserveEditionLocale, qui reste vert sans
// être modifié : là-bas c'est le SERVEUR qui retire l'accès, et les fichiers
// propres doivent partir. Ici c'est la PERSONNE qui retire le dossier, et rien
// ne doit partir. Les deux comportements coexistent, et c'est `cfg.Detaches`
// qui les sépare.
func TestRetraitGardeToutLeContenuLocal(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	e, dir := newEngine(t, url, token)
	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle initial : %v", err)
	}
	// Un fichier propre (descendu du serveur) ET une édition locale : le
	// démontage ordinaire traite les deux différemment, le retrait ne doit
	// distinguer ni l'un ni l'autre.
	writeFile(t, dir, "shared/autre.md", "travail non poussé\n")
	if _, ok := readFile(t, dir, "shared/note.md"); !ok {
		t.Fatal("prérequis : l'espace devrait être monté")
	}
	avant := empreintesDe(t, filepath.Join(dir, "shared"))
	if len(avant) < 2 {
		t.Fatalf("prérequis : au moins 2 fichiers attendus, %d trouvés", len(avant))
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// Le geste : écrire la config, puis redémarrer le moteur. C'est exactement
	// ce que fait l'app (RetireEspace puis redemarre).
	if err := RetireEspace(dir, "shared"); err != nil {
		t.Fatalf("RetireEspace : %v", err)
	}
	e2, err := NewEngine(dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	if err := e2.SyncOnce(); err != nil {
		t.Fatalf("cycle après retrait : %v", err)
	}

	apres := empreintesDe(t, filepath.Join(dir, "shared"))
	if len(apres) != len(avant) {
		t.Fatalf("le retrait a changé le nombre de fichiers : %d -> %d", len(avant), len(apres))
	}
	for rel, h := range avant {
		if apres[rel] != h {
			t.Errorf("fichier %s altéré ou supprimé par le retrait : %s -> %s", rel, h, apres[rel])
		}
	}
	if len(e2.state.Espaces) != 0 {
		t.Errorf("l'espace est encore monté après le retrait : %v", e2.state.Espaces)
	}
	// L'amorçage ne doit pas pouvoir se redéclencher : `Connus` garde le nom,
	// sans quoi `len(Connus) == 0` remonterait d'un coup tout ce qui est
	// disponible, au cycle suivant un geste qui disait « retire ».
	if !slices.Contains(e2.state.Connus, "shared") {
		t.Errorf("« shared » a disparu de Connus : l'amorçage peut se redéclencher (Connus = %v)", e2.state.Connus)
	}
	// Et il ne redescend pas au cycle d'après non plus.
	if err := e2.SyncOnce(); err != nil {
		t.Fatalf("second cycle : %v", err)
	}
	if len(e2.state.Espaces) != 0 {
		t.Errorf("l'espace est redescendu au cycle suivant : %v", e2.state.Espaces)
	}
	if encore := empreintesDe(t, filepath.Join(dir, "shared")); len(encore) != len(avant) {
		t.Errorf("le second cycle a changé le contenu : %d fichiers au lieu de %d", len(encore), len(avant))
	}
}
