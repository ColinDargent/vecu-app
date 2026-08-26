package espaces

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// paths : le dépôt de référence des tests de List.
var paths = []string{
	"shared/note.md",
	"clients/vdf/secret.md",
	"clients/note.md",
	"prive/interne.md",
	"racine.md", // hors espace : n'appartient à aucun dossier de premier niveau
}

func nomsEtCompte(list []Info) map[string]int {
	out := map[string]int{}
	for _, e := range list {
		out[e.Nom] = e.Fichiers
	}
	return out
}

func nouveauStore(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatalf("storage.Init : %v", err)
	}
	return st
}

func ecrire(t *testing.T, st *storage.Store, p, contenu string) {
	t.Helper()
	if _, err := st.Write(p, contenu, "colin"); err != nil {
		t.Fatalf("Write %s : %v", p, err)
	}
}

func occupation(t *testing.T, st *storage.Store, nom string) Existant {
	t.Helper()
	occ, err := Occupation(st, nom)
	if err != nil {
		t.Fatalf("Occupation(%s) : %v", nom, err)
	}
	return occ
}

func TestListAdminVoitTout(t *testing.T) {
	got := nomsEtCompte(List(paths, perms.Ecriture, nil))
	want := map[string]int{"shared": 1, "clients": 2, "prive": 1}
	if len(got) != len(want) {
		t.Fatalf("espaces = %v, attendu %v (racine.md ne doit créer aucun espace)", got, want)
	}
	for nom, n := range want {
		if got[nom] != n {
			t.Errorf("espace %s : %d fichiers, attendu %d", nom, got[nom], n)
		}
	}
}

func TestListSurchargeRestrictive(t *testing.T) {
	// achille : invisible par défaut, lecture sur clients, mais clients/vdf masqué.
	rules := []perms.Rule{
		{Path: "clients", Level: perms.Lecture},
		{Path: "clients/vdf", Level: perms.Invisible},
	}
	got := nomsEtCompte(List(paths, perms.Invisible, rules))
	if len(got) != 1 {
		t.Fatalf("espaces = %v, attendu clients seul", got)
	}
	if got["clients"] != 1 {
		t.Errorf("clients : %d fichiers visibles, attendu 1 (vdf/secret.md masqué)", got["clients"])
	}
}

// TestListRacineInvisibleMaisEnfantLisible : le cas qui fait exister ce package.
// Avec des droits surchargeables dans les deux sens, la racine d'un espace peut
// être invisible alors qu'un sous-dossier est accessible. Ne pas lister
// l'espace priverait l'utilisateur de fichiers auxquels il a droit.
func TestListRacineInvisibleMaisEnfantLisible(t *testing.T) {
	rules := []perms.Rule{{Path: "clients/vdf", Level: perms.Lecture}}
	list := List(paths, perms.Invisible, rules)
	got := nomsEtCompte(list)
	if len(got) != 1 || got["clients"] != 1 {
		t.Fatalf("espaces = %v, attendu clients=1", got)
	}
	if list[0].Niveau != perms.Invisible {
		t.Errorf("niveau à la racine = %v, attendu invisible (l'espace est listé mais sa racine reste masquée)", list[0].Niveau)
	}
}

// TestListEspaceEntierementMasqueAbsent : un compte « lecture par défaut, avec
// des exceptions invisibles » ne doit pas se voir annoncer l'existence d'un
// espace dont il ne peut lire aucun fichier. Le nom de l'espace est lui-même
// l'information sensible, et un dossier portant ce nom se créerait tout seul
// sur sa machine.
func TestListEspaceEntierementMasqueAbsent(t *testing.T) {
	// Le dépôt contient le fichier d'accueil que Create dépose : c'est la forme
	// réelle d'un espace créé depuis l'interface, pas un dépôt de laboratoire.
	chemins := []string{"prive/README.md", "prive/a.md", "prive/sub/b.md", "public/x.md"}
	rules := []perms.Rule{
		{Path: "prive/README.md", Level: perms.Invisible},
		{Path: "prive/a.md", Level: perms.Invisible},
		{Path: "prive/sub", Level: perms.Invisible},
	}
	got := nomsEtCompte(List(chemins, perms.Lecture, rules))
	if _, vu := got["prive"]; vu {
		t.Fatalf("espace entièrement masqué annoncé : %v", got)
	}
	if got["public"] != 1 {
		t.Errorf("public : %d fichiers, attendu 1", got["public"])
	}
}

// TestListIgnoreNomsInvalides : un espace naît du premier fichier écrit dedans.
// Le `.obsidian/` d'un vault ne doit pas devenir un espace montable, ni un
// bundle macOS, ni un dossier accentué (deux formes Unicode, un seul dossier).
func TestListIgnoreNomsInvalides(t *testing.T) {
	chemins := []string{
		".obsidian/appearance.json",
		"Notes.app/note.md",
		"équipe/note.md",
		"shared/note.md",
	}
	got := nomsEtCompte(List(chemins, perms.Ecriture, nil))
	if len(got) != 1 || got["shared"] != 1 {
		t.Fatalf("espaces = %v, attendu shared=1 seulement", got)
	}
}

// TestListEcriture : un espace en lecture seule doit pouvoir être annoncé comme
// tel, sinon l'utilisateur édite et ses écritures sont refusées en silence.
func TestListEcriture(t *testing.T) {
	rules := []perms.Rule{
		{Path: "shared", Level: perms.Lecture},
		{Path: "clients", Level: perms.Ecriture},
	}
	for _, e := range List(paths, perms.Invisible, rules) {
		switch e.Nom {
		case "shared":
			if e.Ecriture {
				t.Errorf("shared annoncé en écriture alors qu'il est en lecture seule")
			}
		case "clients":
			if !e.Ecriture {
				t.Errorf("clients annoncé en lecture seule alors qu'il est modifiable")
			}
		}
	}
}

func TestListTriAlphabetique(t *testing.T) {
	list := List(paths, perms.Ecriture, nil)
	for i := 1; i < len(list); i++ {
		if list[i-1].Nom > list[i].Nom {
			t.Fatalf("liste non triée : %s avant %s", list[i-1].Nom, list[i].Nom)
		}
	}
}

func TestValidNom(t *testing.T) {
	ok := []string{"shared", "clients", "espace-de-travail", "Notes_2026", "v2.final"}
	for _, nom := range ok {
		if err := ValidNom(nom); err != nil {
			t.Errorf("ValidNom(%q) = %v, attendu nil", nom, err)
		}
	}
	ko := []string{
		"", ".", "..", ".cache", ".obsidian", "a/b", "/abs", " shared", "shared ",
		"a\x00b", "a\nb",
		// Accents : deux formes Unicode du même nom = un seul dossier sur macOS,
		// deux espaces côté serveur. Refusés à la source.
		"équipe", "équipe",
		// Bundles et dossiers spéciaux macOS.
		"Notes.app", "Truc.bundle", "doc.rtfd", "fr.lproj", "Docs.localized", "X.APP",
		// Caractère d'affichage trompeur (RTL override).
		"a‮b",
		// Ne se comportent pas comme des dossiers ordinaires en ligne de commande.
		"-rf", "--exclude", "notes.",
	}
	for _, nom := range ko {
		if err := ValidNom(nom); err == nil {
			t.Errorf("ValidNom(%q) = nil, attendu une erreur", nom)
		}
	}
	long := strings.Repeat("a", maxNom+1)
	if err := ValidNom(long); err == nil {
		t.Errorf("ValidNom(nom de %d caractères) = nil, attendu une erreur", len(long))
	}
	// Un nom accentué court doit être refusé pour son caractère, pas pour sa
	// longueur : le message est le seul retour dont dispose l'utilisateur.
	if err := ValidNom("idées"); err == nil || !strings.Contains(err.Error(), "interdit") {
		t.Errorf("ValidNom(\"idées\") = %v, attendu un refus sur le caractère", err)
	}
}

func TestValidChemin(t *testing.T) {
	ok := []string{"shared/note.md", "shared/sous/dossier/idées.md", "clients/vdf/note.md"}
	for _, p := range ok {
		if err := ValidChemin(p); err != nil {
			t.Errorf("ValidChemin(%q) = %v, attendu nil", p, err)
		}
	}
	ko := []string{
		"note.md",     // à la racine : n'appartient à aucun espace
		"shared/",     // pas de fichier
		"",            // vide
		"équipe/x.md", // premier segment refusé
		".obsidian/appearance.json",
	}
	for _, p := range ko {
		if err := ValidChemin(p); err == nil {
			t.Errorf("ValidChemin(%q) = nil, attendu une erreur", p)
		}
	}
}

func TestCreate(t *testing.T) {
	st := nouveauStore(t)
	if err := Create(st, "shared", "colin"); err != nil {
		t.Fatalf("Create : %v", err)
	}
	list, err := st.List("")
	if err != nil {
		t.Fatalf("List : %v", err)
	}
	if len(list) != 1 || list[0] != "shared/"+FichierMeta {
		t.Fatalf("dépôt = %v, attendu [shared/%s]", list, FichierMeta)
	}
	// Le fichier de métadonnées est du contenu ordinaire : il compte, et c'est
	// lui qui fait exister l'espace pour quelqu'un qui peut le lire.
	//
	// Ce fut un `README.md` jusqu'au 21/08, et il entrait en collision avec
	// celui du dossier qu'on adoptait dans le parcours A - donc avec le README
	// de n'importe quel dossier de projet.
	if got := nomsEtCompte(List(list, perms.Ecriture, nil)); got["shared"] != 1 {
		t.Errorf("espace après création : %v, attendu shared=1", got)
	}
	// Un espace existant n'est pas recréé silencieusement, casse comprise.
	if err := Create(st, "shared", "colin"); err == nil {
		t.Errorf("Create d'un espace existant = nil, attendu une erreur")
	}
	if err := Create(st, "SHARED", "colin"); err == nil {
		t.Errorf("Create(SHARED) avec « shared » existant = nil, attendu une erreur")
	}
	// Un nom invalide n'écrit rien.
	for _, nom := range []string{"a/b", ".cache", "idées"} {
		if err := Create(st, nom, "colin"); err == nil {
			t.Errorf("Create(%q) = nil, attendu une erreur", nom)
		}
	}
	if list, err := st.List(""); err != nil || len(list) != 1 {
		t.Errorf("un refus a quand même écrit : %v (%v)", list, err)
	}
}

// TestCreateNomPrisParUnFichierRacine : un fichier de premier niveau du même nom
// ferait échouer le commit sur une erreur git brute (« appears as both a file
// and as a directory »). On le détecte avant, avec le bon motif.
func TestCreateNomPrisParUnFichierRacine(t *testing.T) {
	st := nouveauStore(t)
	ecrire(t, st, "clients", "un fichier, pas un dossier")
	err := Create(st, "clients", "colin")
	if err == nil {
		t.Fatalf("Create sur un nom pris par un fichier racine = nil, attendu une erreur")
	}
	if strings.Contains(err.Error(), "appears as both") {
		t.Errorf("erreur git brute remontée à l'appelant : %v", err)
	}
	if !strings.Contains(err.Error(), "fichier") {
		t.Errorf("le motif du refus n'est pas dit : %v", err)
	}
}

func TestOccupation(t *testing.T) {
	st := nouveauStore(t)
	ecrire(t, st, "shared/note.md", "n")
	ecrire(t, st, "journal", "un fichier racine")

	if got := occupation(t, st, "shared"); got != Dossier {
		t.Errorf("Occupation(shared) = %v, attendu Dossier", got)
	}
	// Casse : APFS est insensible à la casse, « Shared » et « shared » seraient
	// un seul dossier local portant deux périmètres.
	if got := occupation(t, st, "Shared"); got != Dossier {
		t.Errorf("Occupation(Shared) = %v, attendu Dossier (collision de casse sur APFS)", got)
	}
	if got := occupation(t, st, "journal"); got != Fichier {
		t.Errorf("Occupation(journal) = %v, attendu Fichier", got)
	}
	if got := occupation(t, st, "clients"); got != Libre {
		t.Errorf("Occupation(clients) = %v, attendu Libre", got)
	}
	// Un préfixe de chaîne n'est pas un espace : « share » ne couvre pas « shared ».
	if got := occupation(t, st, "share"); got != Libre {
		t.Errorf("Occupation(share) = %v, attendu Libre", got)
	}
}
