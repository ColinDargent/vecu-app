package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/espaces"
)

// TestSansTableLeModeleDavantTient : la table est ADDITIVE. Une installation
// existante n'a aucune entrée et ne doit rien voir changer - c'est ce qui permet
// de la livrer sans migration et sans risque sur les deux postes qui dogfoodent.
func TestSansTableLeModeleDavantTient(t *testing.T) {
	e := &Engine{dir: "/racine"}
	if got := e.abs("shared/notes/a.md"); got != filepath.FromSlash("/racine/shared/notes/a.md") {
		t.Errorf("abs = %q", got)
	}
	if got := e.racineEspace("shared"); got != filepath.FromSlash("/racine/shared") {
		t.Errorf("racineEspace = %q", got)
	}
	got, err := e.rel(filepath.FromSlash("/racine/shared/notes/a.md"))
	if err != nil || got != "shared/notes/a.md" {
		t.Errorf("rel = %q, %v", got, err)
	}
}

// TestEspaceHorsRacine : le point de DT2. Quelqu'un qui partage
// `~/Documents/Clients 2026` ne va pas déplacer son dossier pour faire plaisir à
// l'outil.
func TestEspaceHorsRacine(t *testing.T) {
	ailleurs := filepath.FromSlash("/Users/x/Documents/Clients 2026")
	e := &Engine{dir: "/racine", montages: map[string]string{"clients": ailleurs}}

	if got := e.racineEspace("clients"); got != ailleurs {
		t.Errorf("racineEspace = %q, attendu %q", got, ailleurs)
	}
	// La racine de l'espace elle-même, sans reste.
	if got := e.abs("clients"); got != ailleurs {
		t.Errorf("abs(racine) = %q", got)
	}
	if got := e.abs("clients/2026/devis.md"); got != filepath.Join(ailleurs, "2026", "devis.md") {
		t.Errorf("abs = %q", got)
	}

	// Et le retour, qui est la moitié qui ne se déduit plus : `filepath.Rel`
	// depuis la racine rendrait « ../../Users/x/... », un chemin qui ne
	// correspond à rien côté serveur.
	got, err := e.rel(filepath.Join(ailleurs, "2026", "devis.md"))
	if err != nil || got != "clients/2026/devis.md" {
		t.Errorf("rel = %q, %v", got, err)
	}
	if got, _ := e.rel(ailleurs); got != "clients" {
		t.Errorf("rel(racine de l'espace) = %q, attendu \"clients\"", got)
	}

	// Aller-retour sur une poignée de chemins : c'est l'invariant qui compte,
	// tout le moteur repose dessus.
	for _, p := range []string{"clients", "clients/a.md", "clients/2026/sous/dossier/b.md"} {
		retour, err := e.rel(e.abs(p))
		if err != nil || retour != p {
			t.Errorf("aller-retour cassé sur %q : %q (%v)", p, retour, err)
		}
	}
}

// TestMontagesImbriquesLePlusProfondGagne : la règle subtile, et celle qui ne
// pardonne pas.
//
// Un espace monté dans un sous-dossier d'un autre tombe sous DEUX montages. Sans
// règle de profondeur, c'est l'ordre d'itération de la map qui trancherait - et
// il est délibérément aléatoire en Go. Le chemin porterait donc le nom d'un
// espace qui ne le contient pas, par intermittence, d'un cycle à l'autre.
//
// La boucle rejoue la résolution : un test qui ne la ferait qu'une fois passerait
// une fois sur deux sans qu'on sache pourquoi.
func TestMontagesImbriquesLePlusProfondGagne(t *testing.T) {
	dehors := filepath.FromSlash("/Users/x/travail")
	dedans := filepath.FromSlash("/Users/x/travail/client-a")
	e := &Engine{dir: "/racine", montages: map[string]string{
		"travail": dehors,
		"clienta": dedans,
	}}

	cible := filepath.Join(dedans, "notes.md")
	for i := 0; i < 50; i++ {
		got, err := e.rel(cible)
		if err != nil {
			t.Fatal(err)
		}
		if got != "clienta/notes.md" {
			t.Fatalf("itération %d : rel = %q, attendu \"clienta/notes.md\" (le montage le plus profond)", i, got)
		}
	}
	// Et ce qui n'est que sous le montage extérieur lui reste.
	got, err := e.rel(filepath.Join(dehors, "divers.md"))
	if err != nil || got != "travail/divers.md" {
		t.Errorf("rel = %q, %v", got, err)
	}
}

// TestUnFrereNestPasUnEnfant : `/a/bc` ne tombe pas sous le montage `/a/b`.
// Une comparaison par préfixe de chaîne le croirait, et enverrait le contenu
// d'un dossier dans l'espace du voisin.
func TestUnFrereNestPasUnEnfant(t *testing.T) {
	e := &Engine{dir: "/racine", montages: map[string]string{
		"b": filepath.FromSlash("/a/b"),
	}}
	got, err := e.rel(filepath.FromSlash("/a/bc/note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got == "b/note.md" || got == "b/c/note.md" {
		t.Errorf("un dossier frère a été rattaché au montage : rel = %q", got)
	}
}

// TestLeVecuInterneResteHorsDesEspaces : `.vecu/` vit dans la racine, jamais
// dans un espace. Un montage ne doit pas se l'approprier, sinon l'état partirait
// au serveur.
func TestLeVecuInterneResteHorsDesEspaces(t *testing.T) {
	e := &Engine{dir: filepath.FromSlash("/racine"), montages: map[string]string{
		"shared": filepath.FromSlash("/racine/shared"),
	}}
	got, err := e.rel(filepath.FromSlash("/racine/.vecu/state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != ".vecu/state.json" {
		t.Errorf("rel = %q : l'état interne a été rattaché à un espace", got)
	}
	if !e.ignore(got) {
		t.Errorf("%q n'est plus reconnu comme interne", got)
	}
}

// TestDeuxRacinesNePeuventPasSyncherLeMemeEspace : le danger que l'étape 2a a
// créé, et que le verrou de la racine ne couvrait pas.
//
// Avant les montages, « un processus par racine » suffisait : la racine était le
// contenu. Depuis qu'un espace peut vivre ailleurs, deux moteurs lancés sur deux
// racines DIFFÉRENTES peuvent monter le MÊME dossier. Chacun prend le verrou de
// sa racine, aucun ne voit l'autre, et les deux cyclent sur les mêmes fichiers -
// la condition exacte qui a produit les copies de conflit en série en août.
func TestDeuxRacinesNePeuventPasSyncherLeMemeEspace(t *testing.T) {
	// Les fichiers de verrou vivent dans le dossier d'application : sans HOME
	// isolé, ce test en sème dans celui de la machine.
	t.Setenv("HOME", t.TempDir())
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", t.TempDir())
	partage := t.TempDir() // le dossier convoité, hors des deux racines
	racineA, racineB := t.TempDir(), t.TempDir()
	for _, d := range []string{racineA, racineB} {
		if err := os.MkdirAll(filepath.Join(d, vecuDir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	montages := map[string]string{"clients": partage}

	verrousA, err := acquireLocks(racineA, montages)
	if err != nil {
		t.Fatalf("le premier moteur devrait obtenir ses verrous : %v", err)
	}

	// Racine différente, donc le verrou de racine est libre - et pourtant le
	// second doit être refusé, parce que le CONTENU est déjà pris.
	if _, err := acquireLocks(racineB, montages); err == nil {
		t.Fatal("deux moteurs synchronisent le même dossier depuis deux racines : c'est la condition des copies en série d'août")
	} else if !strings.Contains(err.Error(), "clients") {
		t.Errorf("le refus ne dit pas quel espace est contesté : %v", err)
	}

	// Relâché, le second passe.
	for _, f := range verrousA {
		f.Close()
	}
	verrousB, err := acquireLocks(racineB, montages)
	if err != nil {
		t.Fatalf("après relâchement, le second moteur devrait passer : %v", err)
	}
	for _, f := range verrousB {
		f.Close()
	}
}

// TestPrisePartielleNeLaissePasDeVerrouOrphelin : tout ou rien. Un moteur qui
// obtiendrait la racine mais pas l'un de ses espaces doit repartir les mains
// vides - sinon il garde la racine verrouillée pour rien, et personne ne peut
// plus travailler dessus.
func TestPrisePartielleNeLaissePasDeVerrouOrphelin(t *testing.T) {
	// Les fichiers de verrou vivent dans le dossier d'application : sans HOME
	// isolé, ce test en sème dans celui de la machine.
	t.Setenv("HOME", t.TempDir())
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", t.TempDir())
	partage := t.TempDir()
	racineA, racineB := t.TempDir(), t.TempDir()
	for _, d := range []string{racineA, racineB} {
		if err := os.MkdirAll(filepath.Join(d, vecuDir), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// A tient l'espace partagé.
	verrousA, err := acquireLocks(racineA, map[string]string{"clients": partage})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, f := range verrousA {
			f.Close()
		}
	}()

	// B échoue sur l'espace... et ne doit PAS avoir gardé le verrou de sa racine.
	if _, err := acquireLocks(racineB, map[string]string{"clients": partage}); err == nil {
		t.Fatal("prérequis : B devait échouer")
	}
	// Preuve : un troisième moteur sur la racine de B, sans montage contesté,
	// doit pouvoir passer.
	verrousC, err := acquireLocks(racineB, nil)
	if err != nil {
		t.Fatalf("la racine de B est restée verrouillée par une prise échouée : %v", err)
	}
	for _, f := range verrousC {
		f.Close()
	}
}

// TestUnMontageDansLaRacineNeSeVerrouillePasDeuxFois : un espace monté sous la
// racine est déjà couvert par le verrou de celle-ci. Reprendre un flock dessus
// serait au mieux inutile ; c'est surtout le genre de doublon qui finit par se
// verrouiller contre soi-même.
func TestUnMontageDansLaRacineNeSeVerrouillePasDeuxFois(t *testing.T) {
	// Les fichiers de verrou vivent dans le dossier d'application : sans HOME
	// isolé, ce test en sème dans celui de la machine.
	t.Setenv("HOME", t.TempDir())
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", t.TempDir())
	racine := t.TempDir()
	if err := os.MkdirAll(filepath.Join(racine, vecuDir), 0o700); err != nil {
		t.Fatal(err)
	}
	dedans := filepath.Join(racine, "shared")
	verrous, err := acquireLocks(racine, map[string]string{"shared": dedans})
	if err != nil {
		t.Fatalf("un montage sous la racine a été refusé : %v", err)
	}
	defer func() {
		for _, f := range verrous {
			f.Close()
		}
	}()
	if len(verrous) != 1 {
		t.Errorf("%d verrous pour un montage interne, attendu 1 (celui de la racine)", len(verrous))
	}
}

// TestLeNomDuFichierDeMetaEstLeMemeDesDeuxCotes : le libellé est écrit par le
// serveur et lu par le client. Deux constantes qui divergent produiraient un
// libellé écrit que personne ne lit - sans erreur, sans trace, et découvert le
// jour où quelqu'un s'étonne que son espace s'affiche sous son nom technique.
func TestLeNomDuFichierDeMetaEstLeMemeDesDeuxCotes(t *testing.T) {
	if FichierMetaEspace != espaces.FichierMeta {
		t.Fatalf("client %q, serveur %q", FichierMetaEspace, espaces.FichierMeta)
	}
	// Et il n'est pas ignoré par la synchronisation, sinon il ne partirait jamais.
	if ignored("shared/" + FichierMetaEspace) {
		t.Errorf("%q est ignoré : le libellé n'arriverait chez personne", FichierMetaEspace)
	}
}

// TestAffichageRetombeSurLeNomTechnique : un serveur d'avant DT3, ou un espace
// sans libellé, doit rester lisible. Une liste d'espaces vides serait pire que
// des noms techniques.
func TestAffichageRetombeSurLeNomTechnique(t *testing.T) {
	if got := (Espace{Nom: "clients-2026"}).Affichage(); got != "clients-2026" {
		t.Errorf("Affichage sans libellé = %q", got)
	}
	if got := (Espace{Nom: "clients-2026", Libelle: "Clients 2026"}).Affichage(); got != "Clients 2026" {
		t.Errorf("Affichage avec libellé = %q", got)
	}
}

// TestSansLienPartDuPointDeMontage : la garde qui protège les écritures doit
// marcher le même chemin que l'écriture elle-même.
//
// Le défaut, dormant jusqu'à la première entrée dans `Montages` : `sansLien`
// partait de la racine et joignait le nom de l'espace, alors que `abs` part du
// point de montage. Sur un espace monté ailleurs, la garde inspectait un chemin
// inexistant, l'ENOENT la faisait rendre nil, et l'écriture partait sans garde
// à travers le lien que la garde était censée voir.
func TestSansLienPartDuPointDeMontage(t *testing.T) {
	racine := t.TempDir()
	dehors := t.TempDir()
	montage := filepath.Join(dehors, "equipe")
	if err := os.MkdirAll(montage, 0o755); err != nil {
		t.Fatal(err)
	}
	// Un lien en travers du chemin, DANS l'espace monté : « shared/sous » pointe
	// vers un dossier personnel. Écrire « shared/sous/note.md » y déposerait un
	// fichier hors de tout périmètre synchronisé.
	perso := filepath.Join(dehors, "perso")
	if err := os.MkdirAll(perso, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(perso, filepath.Join(montage, "sous")); err != nil {
		t.Fatal(err)
	}

	e := &Engine{dir: racine, montages: map[string]string{"shared": montage}}
	if err := e.sansLien("shared/sous/note.md"); err == nil {
		t.Error("écriture autorisée à travers un lien dans un espace monté hors de la racine")
	}
	// Le fichier lui-même est un composant du chemin : un lien posé à la place
	// d'un fichier suivi doit être refusé aussi.
	if err := os.Symlink(filepath.Join(perso, "cible.md"), filepath.Join(montage, "direct.md")); err != nil {
		t.Fatal(err)
	}
	if err := e.sansLien("shared/direct.md"); err == nil {
		t.Error("écriture autorisée sur un lien à la place d'un fichier suivi")
	}
	// Et le cas nominal reste permis : sans lien, la garde ne refuse rien.
	if err := e.sansLien("shared/ordinaire.md"); err != nil {
		t.Errorf("écriture ordinaire refusée dans un espace monté : %v", err)
	}
}

// TestSansLienSansMontageInchange : le repli. Un espace sans entrée dans la
// table continue de se vérifier depuis la racine, exactement comme avant.
func TestSansLienSansMontageInchange(t *testing.T) {
	racine := t.TempDir()
	dehors := t.TempDir()
	if err := os.Symlink(dehors, filepath.Join(racine, "shared")); err != nil {
		t.Fatal(err)
	}
	e := &Engine{dir: racine, montages: map[string]string{}}
	if err := e.sansLien("shared/note.md"); err == nil {
		t.Error("écriture autorisée à travers un lien posé à la place d'un espace non monté")
	}
}
