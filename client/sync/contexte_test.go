package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// Le contenu du pointeur est un CONTRAT, pas un détail de présentation. Deux
// postes qui créent le fichier au même cycle ne s'évitent une copie de conflit
// que si l'octet est identique - vérifié sur `git merge-tree`, qui rend exit 0
// sur un ajout au même contenu et exit 1 dès qu'il diffère.
//
// Ce test golden est là pour qu'une « amélioration » du texte se voie. Le
// changer est permis ; le changer sans le savoir ne l'est pas.
func TestContenuPointeurEstFige(t *testing.T) {
	const attendu = "@AGENTS.md\n" +
		"\n" +
		"<!-- Posé par Vécu, une seule fois, à la création. Le contexte de cet espace\n" +
		"     vit dans AGENTS.md : Cursor et Codex le lisent tel quel, Claude Code\n" +
		"     l'importe par la ligne ci-dessus. Écrire le contexte dans AGENTS.md.\n" +
		"     Ce fichier s'édite librement : tant que la ligne d'import reste, Vécu\n" +
		"     n'y touche plus jamais. -->\n"
	if contenuPointeur != attendu {
		t.Errorf("contenu du pointeur modifié.\nobtenu :\n%q\nattendu :\n%q", contenuPointeur, attendu)
	}
}

// Le pointeur que Vécu pose doit satisfaire son propre prédicat, sinon il
// serait reposé - ou signalé comme fait main - à chaque cycle.
func TestPointeurSatisfaitSonProprePredicat(t *testing.T) {
	if !importeAgents(contenuPointeur) {
		t.Fatal("le pointeur posé par Vécu n'est pas reconnu comme important AGENTS.md")
	}
}

func TestImporteAgents(t *testing.T) {
	cas := []struct {
		nom     string
		contenu string
		veut    bool
	}{
		{"ligne seule", "@AGENTS.md", true},
		{"en tête de fichier", "@AGENTS.md\n\n# Suite\n", true},
		{"au milieu du texte", "# Titre\n\nvoir @AGENTS.md pour le reste\n", true},
		{"précédé d'espaces", "   @AGENTS.md\n", true},
		{"en fin de fichier sans saut de ligne", "# Titre\n@AGENTS.md", true},
		{"dans une liste", "- contexte commun @AGENTS.md\n", true},
		{"entouré de gras", "**@AGENTS.md**\n", true},

		{"vide", "", false},
		{"aucune mention", "# Contexte\n\nDes règles.\n", false},
		{"mentionné sans arobase", "voir AGENTS.md\n", false},

		// Les trois faux positifs que la doc et le nommage rendent réels.
		{"entre accents graves", "écrire `@AGENTS.md` en tête\n", false},
		{"dans un bloc clôturé", "Exemple :\n\n```\n@AGENTS.md\n```\n", false},
		{"dans un bloc clôturé par tildes", "~~~\n@AGENTS.md\n~~~\n", false},
		{"autre fichier, suffixe", "@AGENTS.md.bak\n", false},
		{"autre fichier, extension", "@AGENTS.mdx\n", false},
		{"collé à un mot", "x@AGENTS.md\n", false},
		{"chemin plus profond", "@docs/AGENTS.md\n", false},

		// Un bloc REFERMÉ ne masque pas ce qui suit.
		{"après un bloc refermé", "```\ndu code\n```\n\n@AGENTS.md\n", true},
		// Deux spans sur la même ligne : le segment hors span compte.
		{"un span ailleurs sur la ligne", "voir `x` puis @AGENTS.md\n", true},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			if got := importeAgents(c.contenu); got != c.veut {
				t.Errorf("importeAgents(%q) = %v, veut %v", c.contenu, got, c.veut)
			}
		})
	}
}

// Un accent grave laissé ouvert ne doit pas faire INVENTER un import : le repli
// est de rater la mention, jamais d'en fabriquer une.
func TestImporteAgentsAccentGraveNonReferme(t *testing.T) {
	if importeAgents("un `span ouvert @AGENTS.md\n") {
		t.Error("un accent grave non refermé ne doit pas produire un import détecté")
	}
}

// --- slice 2 : le moteur retient qui peut écrire ---------------------------

// La table d'écriture est lue à CHAQUE cycle par F3, or `aligneMontage` sort
// par un retour anticipé dès que le montage n'a pas bougé - c'est-à-dire à la
// quasi-totalité des cycles. Remplir la table après ce retour la laisserait
// vide en régime permanent, donc F3 ne ferait jamais rien.
func TestPeutEcrireSurvitAuMontageInchange(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	e, _ := newEngine(t, url, token)
	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("premier cycle : %v", err)
	}
	if !e.peutEcrire("shared") {
		t.Fatal("prérequis : l'écriture devrait être connue après le cycle de montage")
	}
	// Deuxième cycle : le montage est identique, `aligneMontage` sort par son
	// retour anticipé.
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("second cycle : %v", err)
	}
	if !e.peutEcrire("shared") {
		t.Error("la table d'écriture a été perdue sur un cycle à montage inchangé")
	}
}

// Le droit vient du serveur et rien d'autre : un compte en lecture seule ne
// doit jamais se croire autorisé à poser un fichier dans l'espace.
func TestPeutEcrireSuitLeDroitDuServeur(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	e, _ := newEngine(t, url, token)

	if err := database.SetPermission(membreID, "shared", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle en lecture seule : %v", err)
	}
	if e.peutEcrire("shared") {
		t.Error("un espace en lecture seule est annoncé comme accessible en écriture")
	}

	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle après élargissement : %v", err)
	}
	if !e.peutEcrire("shared") {
		t.Error("l'élargissement du droit n'est pas remonté dans la table")
	}

	// Le RETRAIT, et c'est le sens qui compte pour la sûreté : l'espace reste
	// monté, donc `aligneMontage` sort par son retour anticipé. Une table qu'on
	// complète au lieu de la refaire garderait ici un droit qui n'existe plus.
	if err := database.SetPermission(membreID, "shared", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle après retrait : %v", err)
	}
	if e.peutEcrire("shared") {
		t.Error("un droit d'écriture retiré est resté dans la table")
	}
}

// « Je n'ai pas pu savoir » n'est pas « j'ai le droit ». Un moteur neuf, avant
// tout cycle, ne peut écrire nulle part.
func TestPeutEcrireFauxSansCycle(t *testing.T) {
	url, token, _, _ := serveurAvecMembre(t)
	e, _ := newEngine(t, url, token)
	if e.peutEcrire("shared") || e.peutEcrire("inconnu") {
		t.Error("un moteur qui n'a pas encore interrogé le serveur ne doit s'autoriser aucune écriture")
	}
}

// --- slice 3 : Contexte() branché sur le cycle -----------------------------

// remarqueContexte : la remarque F3 sur cet espace, si elle existe.
func remarqueContexte(e *Engine, espace string) (Laisse, bool) {
	for _, l := range e.Laisses() {
		if l.Genre == GenreEspace && l.Chemin == espace {
			return l, true
		}
	}
	return Laisse{}, false
}

// Le cas nominal : un AGENTS.md seul reçoit son pointeur, en un cycle.
func TestContextePosePointeurQuandAgentsSeul(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/"+FichierContexteAgents, "# contexte de l'équipe\n")

	if err := e.Cycle(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	got, ok := readFile(t, dir, "equipe/"+FichierContexteClaude)
	if !ok {
		t.Fatal("le pointeur CLAUDE.md n'a pas été posé")
	}
	if got != contenuPointeur {
		t.Errorf("pointeur inattendu :\n%q", got)
	}
	if l, ok := remarqueContexte(e, "equipe"); ok {
		t.Errorf("le cas nominal ne doit produire aucune remarque : %q", l.Raison)
	}
}

// Le pointeur posé ne doit pas être RÉÉCRIT au cycle suivant. Un WriteFile
// inconditionnel touche le mtime, fsnotify le voit comme une modification, le
// daemon relance un cycle, réécrit, se réveille : la boucle que `ecris`
// documente déjà et qu'il ne faut pas rouvrir ici.
func TestContexteNeReecritPasLePointeur(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/"+FichierContexteAgents, "# contexte\n")
	if err := e.Cycle(); err != nil {
		t.Fatalf("premier cycle : %v", err)
	}
	abs := filepath.Join(dir, "equipe", FichierContexteClaude)
	avant, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Cycle(); err != nil {
		t.Fatalf("second cycle : %v", err)
	}
	apres, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !apres.ModTime().Equal(avant.ModTime()) {
		t.Error("le pointeur a été réécrit alors qu'il était déjà correct")
	}
}

// L'invariant de la projection depuis juillet, porté au contexte : jamais
// d'écrasement ni de modification d'un fichier fait main. Ici les deux fichiers
// existent et le CLAUDE.md n'importe rien : Vécu le laisse et le dit.
func TestContexteNeTouchePasUnClaudeFaitMain(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	const faitMain = "# Règles de l'équipe\n\nÉcrire en français.\n"
	writeFile(t, dir, "equipe/"+FichierContexteAgents, "# contexte\n")
	writeFile(t, dir, "equipe/"+FichierContexteClaude, faitMain)

	if err := e.Cycle(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if got, _ := readFile(t, dir, "equipe/"+FichierContexteClaude); got != faitMain {
		t.Errorf("un CLAUDE.md fait main a été modifié :\n%q", got)
	}
	l, ok := remarqueContexte(e, "equipe")
	if !ok {
		t.Fatal("deux contextes concurrents doivent être signalés")
	}
	if !strings.Contains(l.Raison, importAgents) {
		t.Errorf("la remarque doit nommer le geste à faire : %q", l.Raison)
	}
	if l.Perdu() {
		t.Error("la remarque de contexte ne signale aucune perte : elle ne doit pas faire échouer vecu sync")
	}
}

// Un CLAUDE.md riche, écrit à la main, qui porte DÉJÀ l'import : c'est l'état
// nominal. Un prédicat par égalité de contenu le classerait « fait main » et
// signalerait à tort - c'est exactement pourquoi le prédicat porte sur
// l'import et non sur l'auteur du fichier.
func TestContexteSilencieuxSiLImportEstDejaLa(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	const richeEtCorrect = "@AGENTS.md\n\n## Claude Code\n\nUtiliser le mode plan.\n"
	writeFile(t, dir, "equipe/"+FichierContexteAgents, "# contexte\n")
	writeFile(t, dir, "equipe/"+FichierContexteClaude, richeEtCorrect)

	if err := e.Cycle(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if got, _ := readFile(t, dir, "equipe/"+FichierContexteClaude); got != richeEtCorrect {
		t.Errorf("un CLAUDE.md déjà correct a été modifié :\n%q", got)
	}
	if l, ok := remarqueContexte(e, "equipe"); ok {
		t.Errorf("un CLAUDE.md qui importe déjà AGENTS.md est nominal, aucune remarque attendue : %q", l.Raison)
	}
}

// Le cas réel de l'installation de Colin : un CLAUDE.md seul, riche, fait main.
// Vécu ne peut pas déplacer le contenu ; il dit le geste, en une commande.
func TestContexteSignaleUnContexteNonPortable(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/"+FichierContexteClaude, "# Carte de l'espace\n")

	if err := e.Cycle(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if _, ok := readFile(t, dir, "equipe/"+FichierContexteAgents); ok {
		t.Error("Vécu ne doit jamais fabriquer un AGENTS.md : il ne rédige pas le contexte")
	}
	l, ok := remarqueContexte(e, "equipe")
	if !ok {
		t.Fatal("un contexte illisible de Cursor et Codex doit être signalé")
	}
	if !strings.Contains(l.Raison, "mv equipe/"+FichierContexteClaude+" equipe/"+FichierContexteAgents) {
		t.Errorf("la remarque doit porter la commande exacte : %q", l.Raison)
	}
	if l.Perdu() {
		t.Error("la remarque ne doit pas faire sortir vecu sync en erreur")
	}
}

// Pas de contexte dans l'espace : rien à rendre portable, et rien à dire.
func TestContexteRienQuandAucunFichier(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/note.md", "du texte\n")

	if err := e.Cycle(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if _, ok := readFile(t, dir, "equipe/"+FichierContexteClaude); ok {
		t.Error("un pointeur a été posé sans aucun AGENTS.md")
	}
	if l, ok := remarqueContexte(e, "equipe"); ok {
		t.Errorf("un espace sans contexte ne doit rien produire : %q", l.Raison)
	}
}

// Écrire à travers un lien symbolique sortirait du périmètre : c'est la garde
// que tout le moteur tient (`sansLien`, `traverseUnLien`), appliquée ici.
func TestContexteRefuseUnLienSymbolique(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	dehors := filepath.Join(t.TempDir(), "contexte-externe.md")
	if err := os.WriteFile(dehors, []byte("# ailleurs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "equipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dehors, filepath.Join(dir, "equipe", FichierContexteAgents)); err != nil {
		t.Fatal(err)
	}

	if err := e.Cycle(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if _, ok := readFile(t, dir, "equipe/"+FichierContexteClaude); ok {
		t.Error("un pointeur a été posé alors qu'AGENTS.md est un lien symbolique")
	}
	l, ok := remarqueContexte(e, "equipe")
	if !ok {
		t.Fatal("le refus doit être dit, pas silencieux")
	}
	if !strings.Contains(l.Raison, "lien symbolique") {
		t.Errorf("le motif doit nommer le lien : %q", l.Raison)
	}
}

// Un espace en lecture seule pour ce compte : ni écriture, ni remarque.
//
// L'écriture serait une faute franche - 403 au push, Laisse de genre `fichier`,
// `vecu sync` en erreur à chaque cycle. Et la remarque serait du bruit : sa
// seule suite possible serait « demander à quelqu'un d'autre ».
func TestContexteNeFaitRienEnLectureSeule(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	e, dir := newEngine(t, url, token)
	if err := database.SetPermission(membreID, "shared", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.Cycle(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if _, ok := readFile(t, dir, "shared/note.md"); !ok {
		t.Fatal("prérequis : l'espace en lecture seule devrait être descendu")
	}
	if _, ok := readFile(t, dir, "shared/"+FichierContexteClaude); ok {
		t.Error("un pointeur a été posé dans un espace en lecture seule")
	}
	if l, ok := remarqueContexte(e, "shared"); ok {
		t.Errorf("aucune remarque ne doit partir vers un compte qui ne peut pas agir : %q", l.Raison)
	}
}

// Le scénario d'adoption complet, et l'ordre qu'il exige. Le `mv` doit voir sa
// SUPPRESSION partir avant qu'un pointeur soit posé à la place : Contexte()
// tournant avant SyncOnce recréerait le fichier dans le cycle même qui le
// supprime, et la suppression ne partirait jamais.
func TestContexteApresRenommageDuFichierFaitMain(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/"+FichierContexteClaude, "# Carte de l'espace\n")
	if err := a.Cycle(); err != nil {
		t.Fatalf("A, cycle initial : %v", err)
	}
	if _, ok := remarqueContexte(a, "equipe"); !ok {
		t.Fatal("prérequis : le contexte non portable devrait être signalé")
	}

	// Le geste humain, une fois et d'un seul côté.
	if err := os.Rename(
		filepath.Join(dirA, "equipe", FichierContexteClaude),
		filepath.Join(dirA, "equipe", FichierContexteAgents)); err != nil {
		t.Fatal(err)
	}
	if err := a.Cycle(); err != nil {
		t.Fatalf("A, cycle après renommage : %v", err)
	}
	if got, ok := readFile(t, dirA, "equipe/"+FichierContexteClaude); !ok || got != contenuPointeur {
		t.Fatalf("A n'a pas reçu son pointeur : %q %v", got, ok)
	}
	// Le renommage est parti au serveur, et le pointeur part au cycle suivant.
	if err := a.Cycle(); err != nil {
		t.Fatalf("A, cycle de publication : %v", err)
	}
	sur := arbre(t, a)
	if !sur["equipe/"+FichierContexteAgents] || !sur["equipe/"+FichierContexteClaude] {
		t.Fatalf("le serveur devrait porter les deux fichiers : %v", sur)
	}

	// B reçoit les deux et n'a rien à faire.
	if err := b.Cycle(); err != nil {
		t.Fatalf("B : %v", err)
	}
	if got, ok := readFile(t, dirB, "equipe/"+FichierContexteAgents); !ok || got != "# Carte de l'espace\n" {
		t.Errorf("B n'a pas reçu le contexte : %q %v", got, ok)
	}
	if got, _ := readFile(t, dirB, "equipe/"+FichierContexteClaude); got != contenuPointeur {
		t.Errorf("B n'a pas reçu le pointeur : %q", got)
	}
	if l, ok := remarqueContexte(b, "equipe"); ok {
		t.Errorf("B ne doit rien avoir à dire : %q", l.Raison)
	}
}

// LA propriété qui rend la création sûre. Deux postes qui posent le pointeur
// chacun de leur côté, avant d'avoir reçu celui de l'autre, poussent un ajout
// sans ancêtre commun - donc une fusion en add-or-conflict. Le contenu étant
// identique à l'octet près, git la résout proprement et AUCUNE copie de conflit
// n'apparaît. C'est ce que le caractère figé de `contenuPointeur` achète, et
// c'est pour ça que ce test existe au niveau du vrai serveur.
func TestContexteDeuxPostesNeProduisentPasDeCopieDeConflit(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/"+FichierContexteAgents, "# contexte commun\n")
	if err := a.Cycle(); err != nil { // A pousse AGENTS.md et pose son pointeur
		t.Fatalf("A : %v", err)
	}
	if err := b.Cycle(); err != nil { // B reçoit AGENTS.md et pose SON pointeur
		t.Fatalf("B : %v", err)
	}
	// Prérequis délibérément faible - « B a bien posé un pointeur », pas « il est
	// identique ». L'égalité est justement ce que le test doit MESURER par ses
	// conséquences, plus bas : un prérequis strict ferait échouer le test avant
	// d'avoir regardé s'il y a une copie de conflit.
	if got, ok := readFile(t, dirB, "equipe/"+FichierContexteClaude); !ok || !importeAgents(got) {
		t.Fatalf("prérequis : B devrait avoir posé son propre pointeur, obtenu %q %v", got, ok)
	}
	// Les deux publient le leur, aucun n'a vu celui de l'autre.
	if err := a.Cycle(); err != nil {
		t.Fatalf("A, publication : %v", err)
	}
	if err := b.Cycle(); err != nil {
		t.Fatalf("B, publication : %v", err)
	}

	for chemin := range arbre(t, a) {
		if strings.Contains(chemin, "conflit") {
			t.Errorf("une copie de conflit est apparue sur le fichier de contexte : %s", chemin)
		}
	}
	if got, _ := readFile(t, dirA, "equipe/"+FichierContexteClaude); got != contenuPointeur {
		t.Errorf("le pointeur de A a bougé : %q", got)
	}
}

// Une remarque STABLE ne se journalise qu'une fois. Journalisée à chaque cycle,
// elle écrirait ~5 700 lignes par jour et par espace : DT8 en miniature, et il
// vaut mieux ne pas repayer la leçon.
func TestContexteNeRepetePasLaLigneDeJournal(t *testing.T) {
	url, token := testServer(t)
	var lignes []string
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "colin", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, func(f string, a ...any) { lignes = append(lignes, fmt.Sprintf(f, a...)) })
	if err != nil {
		t.Fatal(err)
	}
	e.Importer([]string{"equipe"})
	writeFile(t, dir, "equipe/"+FichierContexteClaude, "# Carte\n")

	for i := 0; i < 3; i++ {
		if err := e.Cycle(); err != nil {
			t.Fatalf("cycle %d : %v", i, err)
		}
	}
	n := 0
	for _, l := range lignes {
		if strings.HasPrefix(l, "contexte, espace ") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("la remarque stable a été journalisée %d fois, attendu 1", n)
	}
	// Mais la Laisse, elle, est bien réémise : c'est une photo du dernier cycle.
	if _, ok := remarqueContexte(e, "equipe"); !ok {
		t.Error("la remarque doit rester présente dans l'état, même sans nouvelle ligne de journal")
	}
}

// Une absence sur le DISQUE n'est pas une absence tout court. Le serveur porte
// un CLAUDE.md fait main que le rattrapage n'a pas encore ramené ici : poser le
// pointeur dessus pousserait un ajout sans ancêtre commun contre un contenu
// différent, donc une copie de conflit fabriquée par Vécu sur le fichier de
// contexte de l'équipe.
func TestContexteNePosePasSurUnClaudeConnuDuServeur(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/"+FichierContexteAgents, "# contexte\n")
	writeFile(t, dirA, "equipe/"+FichierContexteClaude, "# fait main, riche\n")
	if err := a.Cycle(); err != nil {
		t.Fatalf("A : %v", err)
	}
	if err := b.Cycle(); err != nil {
		t.Fatalf("B : %v", err)
	}
	if got, ok := readFile(t, dirB, "equipe/"+FichierContexteClaude); !ok || got != "# fait main, riche\n" {
		t.Fatalf("prérequis : B devrait avoir reçu le fichier fait main, obtenu %q %v", got, ok)
	}

	// Le fichier disparaît du disque de B sans que la suppression soit poussée -
	// rattrapage en échec, volume décroché, fichier retiré à la main juste avant
	// le cycle. Il reste connu de `state.Files`.
	if err := os.Remove(filepath.Join(dirB, "equipe", FichierContexteClaude)); err != nil {
		t.Fatal(err)
	}
	if err := b.Contexte(); err != nil {
		t.Fatalf("Contexte : %v", err)
	}
	if got, ok := readFile(t, dirB, "equipe/"+FichierContexteClaude); ok {
		t.Errorf("Vécu a posé un pointeur par-dessus un fichier fait main connu du serveur : %q", got)
	}
}

// Un `.vecuignore` qui écarte le chemin est une consigne explicite. Créer le
// fichier quand même poserait un contenu que la synchronisation ne portera
// jamais, sans que personne l'ait demandé.
func TestContexteRespecteLeVecuignore(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, ".vecuignore", FichierContexteClaude+"\n")
	writeFile(t, dir, "equipe/"+FichierContexteAgents, "# contexte\n")

	if err := e.Cycle(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if got, ok := readFile(t, dir, "equipe/"+FichierContexteClaude); ok {
		t.Errorf("un chemin ignoré a quand même été créé : %q", got)
	}
}
