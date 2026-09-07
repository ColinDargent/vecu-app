package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copies_test.go : la copie de conflit locale.
//
// Contexte mesuré le 20/08 : 36 préservations d'édition locale sur le poste de
// Colin depuis le 27/07, et quatre copies sur un même fichier chez Achille en
// deux heures, dont deux prises SUR une copie. Le journal disait qu'une copie
// avait eu lieu et avec quelles empreintes ; il ne disait jamais POURQUOI une
// copie plutôt qu'un report - qui est la seule question dont dépend la suite.

// moteurBavard : un moteur dont on lit le journal.
func moteurBavard(t *testing.T, url, token string) (*Engine, string, *[]string) {
	t.Helper()
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: url, Username: "colin", Token: token}); err != nil {
		t.Fatal(err)
	}
	var lignes []string
	e, err := NewEngine(dir, func(f string, a ...any) { lignes = append(lignes, fmt.Sprintf(f, a...)) })
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e.Close() })
	e.Importer([]string{"equipe"})
	return e, dir, &lignes
}

func lignePreservation(t *testing.T, lignes []string) string {
	t.Helper()
	for _, l := range lignes {
		if strings.HasPrefix(l, "édition locale non poussée préservée") {
			return l
		}
	}
	return ""
}

// Le rattrapage n'a pas de branche de report. Quand il préserve, la ligne doit
// le dire, avec la précondition qui manquait.
func TestPreservationDitPourquoiAuRattrapage(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB, journal := moteurBavard(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "version de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	// La forme exacte d'un chemin qu'un conflit de push vient d'oublier, et
	// celle d'un fichier jamais reçu.
	delete(b.state.Files, "equipe/plan.md")
	delete(b.state.Versions, "equipe/plan.md")
	writeFile(t, dirB, "equipe/plan.md", "écriture locale non poussée\n")

	perimetre, err := b.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.rattrape(perimetre, vu, nil, nil); err != nil {
		t.Fatal(err)
	}

	ligne := lignePreservation(t, *journal)
	if ligne == "" {
		t.Fatal("prérequis : une préservation aurait dû avoir lieu")
	}
	for _, attendu := range []string{"report refusé :", "rattrapage", "chemin non suivi"} {
		if !strings.Contains(ligne, attendu) {
			t.Errorf("la ligne ne porte pas %q :\n  %s", attendu, ligne)
		}
	}
	// L'ancienne moitié de la ligne reste : elle répond à une autre question.
	for _, attendu := range []string{"local ", "connu ", "entrant "} {
		if !strings.Contains(ligne, attendu) {
			t.Errorf("la ligne a perdu %q :\n  %s", attendu, ligne)
		}
	}
}

// Le pull, lui, a une branche de report. Quand elle refuse faute de
// marque-page, la ligne doit nommer CE motif-là et pas un autre.
func TestPreservationDitPourquoiAuPull(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB, journal := moteurBavard(t, url, token)

	writeFile(t, dirA, "equipe/note.md", "v1\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	// A publie une v2 : c'est elle qui redescendra chez B.
	writeFile(t, dirA, "equipe/note.md", "v2 venue de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	// Cycle de B rejoué à la main, pour glisser l'écriture locale entre le push
	// et le pull - ce que `SyncOnce` ne permet pas d'injecter.
	base := b.state.Head
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	// Marque-page abandonné : la forme que prend un chemin dont le serveur a
	// refusé l'envoi par un 400.
	b.state.Versions["equipe/note.md"] = ""
	acceptes, err := b.pushLocal(base, vu)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/note.md", "écriture locale de B\n")
	if _, err := b.pull(base, vu, acceptes); err != nil {
		t.Fatal(err)
	}

	ligne := lignePreservation(t, *journal)
	if ligne == "" {
		t.Fatal("prérequis : une préservation aurait dû avoir lieu")
	}
	if !strings.Contains(ligne, "report refusé : sans marque-page") {
		t.Errorf("le motif attendu est « sans marque-page » :\n  %s", ligne)
	}
	// Et rien n'est perdu, ce qui reste le contrat principal.
	tout, _ := readFile(t, dirB, "equipe/note.md")
	for _, c := range copiesDeConflit(t, dirB, "equipe") {
		contenu, _ := readFile(t, dirB, "equipe/"+c)
		tout += contenu
	}
	if !strings.Contains(tout, "écriture locale de B") {
		t.Errorf("l'écriture locale est introuvable : %q", tout)
	}
}

// `reportable` et `refusDeReport` doivent rester deux faces de la même
// décision. Les laisser diverger ferait journaliser un motif qui décrit autre
// chose que ce qui s'est produit, c'est-à-dire pire que pas de motif du tout.
func TestRefusDeReportEstLaMemeDecisionQueReportable(t *testing.T) {
	url, token := testServer(t)
	e, _ := newEngine(t, url, token)
	e.state.Files = map[string]string{"a/suivi.md": "x", "a/sans-page.md": "y"}
	e.state.Versions = map[string]string{"a/suivi.md": "c1", "a/sans-page.md": ""}

	for _, rel := range []string{"a/suivi.md", "a/sans-page.md", "a/inconnu.md"} {
		if e.reportable(rel) != (e.refusDeReport(rel) == "") {
			t.Errorf("%s : reportable=%v mais refus=%q", rel, e.reportable(rel), e.refusDeReport(rel))
		}
	}
	if got := e.refusDeReport("a/inconnu.md"); got != "chemin non suivi" {
		t.Errorf("motif inattendu pour un chemin inconnu : %q", got)
	}
	if got := e.refusDeReport("a/sans-page.md"); got != "sans marque-page" {
		t.Errorf("motif inattendu pour un chemin sans marque-page : %q", got)
	}
	if got := e.refusDeReport("a/suivi.md"); got != "" {
		t.Errorf("un chemin reportable ne doit porter aucun motif, obtenu %q", got)
	}
}

// --- slice 2 : une copie locale ne voyage plus -----------------------------

// Le contrat central de la slice : la copie reste sur le disque, là où on
// l'arbitre, et n'arrive jamais chez l'autre.
func TestCopieLocaleResteSurLeDisqueEtNeVaPasAuServeur(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/note.md", "v1\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirA, "equipe/note.md", "v2 venue de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	// Cycle de B rejoué à la main pour glisser l'écriture locale après le push.
	base := b.state.Head
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	b.state.Versions["equipe/note.md"] = "" // pas reportable : la copie est le repli
	acceptes, err := b.pushLocal(base, vu)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/note.md", "écriture locale de B\n")
	if _, err := b.pull(base, vu, acceptes); err != nil {
		t.Fatal(err)
	}

	copies := copiesDeConflit(t, dirB, "equipe")
	if len(copies) == 0 {
		t.Fatal("prérequis : une copie locale aurait dû être créée")
	}
	// Elle est bien là, sur le disque.
	trouve := false
	for _, c := range copies {
		if contenu, ok := readFile(t, dirB, "equipe/"+c); ok && strings.Contains(contenu, "écriture locale de B") {
			trouve = true
		}
	}
	if !trouve {
		t.Errorf("l'écriture locale n'est pas dans les copies sur le disque : %v", copies)
	}

	// Deux cycles complets de B, puis le serveur ne doit rien en savoir.
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	for chemin := range arbre(t, b) {
		if strings.Contains(chemin, marqueCopieLocale) {
			t.Errorf("une copie locale est partie au serveur : %s", chemin)
		}
	}
	// Et elle est TOUJOURS sur le disque : ignorer n'est pas supprimer.
	if apres := copiesDeConflit(t, dirB, "equipe"); len(apres) != len(copies) {
		t.Errorf("les copies ont disparu du disque : %v -> %v", copies, apres)
	}

	// LE POINT QUE LE CONTRE-EXAMEN A TROUVÉ. Un fichier sur le disque que le
	// serveur n'aura jamais est la définition littérale d'une Laisse de genre
	// `fichier` - celle qui fait sortir `vecu sync` en erreur, à chaque cycle,
	// indéfiniment. Ce serait le déluge de F6 reconstruit par une autre porte,
	// et sur un cas qui se produit 36 fois par mois.
	for _, l := range b.Laisses() {
		if l.Perdu() {
			t.Errorf("une copie locale ne doit produire aucune alerte de perte : %s — %s", l.Chemin, l.Raison)
		}
	}
	if _, err := readFile(t, dirB, "equipe/note.md"); !err {
		t.Error("le canonique doit être là, à côté de sa copie")
	}
}

// La marque est produite d'un côté et reconnue de l'autre. Produire un nom que
// la reconnaissance ne retrouve pas remettrait en circulation exactement ce que
// la slice ferme.
func TestMarqueCopieLocaleLieProductionEtReconnaissance(t *testing.T) {
	for _, rel := range []string{
		"equipe/note.md",
		"equipe/sous/dossier/note.md",
		"equipe/sans-extension",
		"equipe/nom avec espaces.md",
	} {
		copie := localConflictName(rel)
		if !ignored(copie) {
			t.Errorf("localConflictName(%q) = %q, non reconnu par ignored", rel, copie)
		}
		if ignored(rel) {
			t.Errorf("le canonique %q ne doit pas être ignoré", rel)
		}
	}
	// Et la forme numérotée que produit freeLocalName, ainsi que la copie de
	// copie qui a été observée en production le 20/08.
	for _, copie := range []string{
		"equipe/note (conflit local) (2).md",
		"equipe/note (conflit local) (2) (conflit local).md",
	} {
		if !ignored(copie) {
			t.Errorf("%q devrait être ignoré", copie)
		}
	}
}

// Les copies du SERVEUR restent synchronisées : le modèle Dropbox est délibéré
// et cette slice n'y touche pas.
func TestCopieServeurResteSynchronisee(t *testing.T) {
	for _, chemin := range []string{
		"equipe/note (conflit 2026-08-20 10h00 - achille).md",
		"equipe/note (conflit 2026-08-20 19h01 - colin).md",
	} {
		if ignored(chemin) {
			t.Errorf("%q est une copie du serveur, elle doit continuer à circuler", chemin)
		}
	}
}

// Une copie déjà présente sur le serveur avant cette version devient un résidu
// INERTE : ni suppression distante poussée par ce poste, ni retrait du disque.
// Sans ça, la mise à jour effacerait chez tout le monde des fichiers qu'un
// autre client y a légitimement mis.
func TestCopieLocaleHeriteeDuServeurNeDeclencheAucuneSuppression(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)

	// Posée directement par l'API : elle date d'avant la règle d'ignore.
	const heritee = "equipe/note (conflit local).md"
	if _, err := e.client.Put(heritee, "contenu hérité\n", "", ""); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, heritee, "contenu hérité\n")
	// L'état la porte, comme sur un poste qui l'avait reçue avant la mise à jour.
	e.state.Files[heritee] = hashContent([]byte("contenu hérité\n"))

	for i := 0; i < 2; i++ {
		if err := e.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", i, err)
		}
	}

	if !arbre(t, e)[heritee] {
		t.Error("la copie héritée a été supprimée du serveur : une mise à jour ne doit effacer le fichier de personne")
	}
	if _, ok := readFile(t, dir, heritee); !ok {
		t.Error("la copie héritée a été retirée du disque : ignorer n'est pas supprimer")
	}
	for _, l := range e.Laisses() {
		if l.Perdu() {
			t.Errorf("une copie héritée ne doit produire aucune alerte de perte : %s — %s", l.Chemin, l.Raison)
		}
	}
}

// --- slice 3 : les copies se voient ----------------------------------------

// Depuis la slice 2 une copie locale ne se synchronise plus, donc aucune autre
// surface ne peut la montrer. Si le scan ne la compte pas, elle n'existe pour
// personne jusqu'à ce qu'on tombe dessus dans un dossier.
func TestScanCompteLesCopiesLocales(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/note.md", "canonique\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if n := len(e.CopiesEnAttente()); n != 0 {
		t.Fatalf("aucune copie attendue au départ, obtenu %d", n)
	}

	writeFile(t, dir, "equipe/note (conflit local).md", "édition mise de côté\n")
	writeFile(t, dir, "equipe/sous/plan (conflit local) (2).md", "une autre\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	attendu := []string{"equipe/note (conflit local).md", "equipe/sous/plan (conflit local) (2).md"}
	if got := e.CopiesEnAttente(); !slicesEgales(got, attendu) {
		t.Errorf("copies comptées = %v, attendu %v", got, attendu)
	}

	// Le compte REDESCEND quand la copie est arbitrée puis supprimée à la main.
	// Une liste qui ne baisse jamais cesse d'être lue.
	if err := os.Remove(filepath.Join(dir, "equipe", "note (conflit local).md")); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if got := e.CopiesEnAttente(); len(got) != 1 {
		t.Errorf("après arbitrage d'une copie, il devrait en rester une : %v", got)
	}
	// Et ça ne fait toujours pas sortir la commande en erreur.
	for _, l := range e.Laisses() {
		if l.Perdu() {
			t.Errorf("une copie en attente n'est pas une perte : %s — %s", l.Chemin, l.Raison)
		}
	}
}

// `vecu status` ne construit pas de moteur, donc pas de scan : il a sa propre
// fonction, et les deux doivent voir la même chose. Les laisser diverger
// donnerait deux comptes différents pour la même question.
func TestStatusVoitLesMemesCopiesQueLeScan(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/note.md", "canonique\n")
	writeFile(t, dir, "equipe/note (conflit local).md", "mise de côté\n")
	writeFile(t, dir, "equipe/sous/plan (conflit local) (2) (conflit local).md", "copie de copie\n")
	// Un fichier ordinaire et une copie du SERVEUR : ni l'un ni l'autre ne compte.
	writeFile(t, dir, "equipe/ordinaire.md", "rien à voir\n")
	writeFile(t, dir, "equipe/note (conflit 2026-08-20 10h00 - achille).md", "copie serveur\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	parLeScan := e.CopiesEnAttente()
	parStatus := CopiesDeConflitLocales(dir, nil, []string{"equipe"})
	if !slicesEgales(parLeScan, parStatus) {
		t.Errorf("le scan voit %v, status voit %v", parLeScan, parStatus)
	}
	if len(parStatus) != 2 {
		t.Errorf("deux copies locales attendues, obtenu %v", parStatus)
	}
	for _, c := range parStatus {
		if strings.Contains(c, "achille") || strings.Contains(c, "ordinaire") {
			t.Errorf("ni une copie serveur ni un fichier ordinaire ne doit être compté : %s", c)
		}
	}
}

func slicesEgales(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
