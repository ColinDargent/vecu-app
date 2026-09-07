package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// perimetreSans rend l'arborescence serveur privée d'un chemin : c'est ce que
// voit un poste dont on vient de retirer l'accès. Le serveur, lui, garde tout.
func perimetreSans(t *testing.T, e *Engine, exclu string) []string {
	t.Helper()
	tout, err := e.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	var reste []string
	for _, p := range tout {
		if p != exclu {
			reste = append(reste, p)
		}
	}
	if len(reste) == len(tout) {
		t.Fatalf("précondition : %s n'était pas dans le périmètre, le test ne prouve rien", exclu)
	}
	return reste
}

// lotsDeCorbeille rend les noms des lots présents dans la corbeille.
func lotsDeCorbeille(t *testing.T, e *Engine) []string {
	t.Helper()
	entrees, err := os.ReadDir(e.racineCorbeille())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var noms []string
	for _, entree := range entrees {
		noms = append(noms, entree.Name())
	}
	return noms
}

// DAR-199. Un retrait d'accès sort le fichier du dossier de travail, il ne le
// détruit pas : le serveur garde le contenu, donc la perte serait purement
// locale, et sans recours.
func TestRetraitDAccesMetEnCorbeilleAuLieuDeSupprimer(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)

	const rel = "equipe/dossier/secret.md"
	writeFile(t, dir, rel, "contenu confidentiel\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	var journal []string
	e.logf = func(f string, args ...any) { journal = append(journal, fmt.Sprintf(f, args...)) }

	if err := e.reconcilePerimeter(perimetreSans(t, e, rel), nil); err != nil {
		t.Fatalf("reconcile : %v", err)
	}

	if _, present := readFile(t, dir, rel); present {
		t.Error("le fichier est resté à sa place alors que l'accès a été retiré")
	}
	lots := lotsDeCorbeille(t, e)
	if len(lots) != 1 {
		t.Fatalf("attendu un lot de corbeille, trouvé : %v", lots)
	}
	// Le chemin relatif est conservé sous l'horodatage : c'est ce qui rend la
	// reprise possible sans deviner d'où venait le fichier.
	cible := filepath.Join(e.racineCorbeille(), lots[0], filepath.FromSlash(rel))
	contenu, err := os.ReadFile(cible)
	if err != nil {
		t.Fatalf("le fichier n'est pas à sa place dans la corbeille : %v", err)
	}
	if string(contenu) != "contenu confidentiel\n" {
		t.Errorf("contenu altéré par le passage en corbeille : %q", contenu)
	}
	// La ligne de journal EST l'interface de restauration de ce lot. Si elle ne
	// donne pas le chemin en entier, il n'y en a aucune.
	var dit bool
	for _, l := range journal {
		if strings.Contains(l, rel) && strings.Contains(l, cible) {
			dit = true
		}
	}
	if !dit {
		t.Errorf("le journal ne donne pas le chemin de reprise ; journal : %v", journal)
	}
}

// La garde qui compte, et elle est ANTÉRIEURE à ce lot : un contenu que ce poste
// est le seul à porter ne part jamais, corbeille comprise. `h == known` sort de
// la branche avant qu'on ne touche au fichier.
//
// C'est la mutation à tuer : retirer cette garde ferait passer une édition
// locale non poussée en corbeille, donc hors du dossier de travail, sur un
// simple changement de droits.
func TestEditionLocaleNonPousseeNeVaPasEnCorbeille(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)

	const rel = "equipe/brouillon.md"
	writeFile(t, dir, rel, "version poussée\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	// Écrite après le dernier cycle : le serveur ne l'a jamais eue.
	writeFile(t, dir, rel, "travail que personne d'autre ne porte\n")

	if err := e.reconcilePerimeter(perimetreSans(t, e, rel), nil); err != nil {
		t.Fatalf("reconcile : %v", err)
	}

	local, present := readFile(t, dir, rel)
	if !present {
		t.Fatal("une édition locale non poussée a quitté le dossier de travail")
	}
	if local != "travail que personne d'autre ne porte\n" {
		t.Errorf("l'édition locale a été altérée : %q", local)
	}
	if lots := lotsDeCorbeille(t, e); len(lots) != 0 {
		t.Errorf("une édition locale non poussée est passée en corbeille : %v", lots)
	}
}

// La borne de rétention, et elle est dans le code pour une raison : ce dépôt a
// laissé un journal atteindre 3,3 Go avant que quiconque ne le remarque. Une
// consigne dans un runbook n'aurait pas plus marché ici que là-bas.
func TestCorbeillePurgeLesLotsPerimes(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)

	const rel = "equipe/vieux.md"
	writeFile(t, dir, rel, "ancien\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}

	// Le retrait a lieu il y a 31 jours.
	depart := time.Now()
	e.maintenant = func() time.Time { return depart.Add(-31 * 24 * time.Hour) }
	if err := e.reconcilePerimeter(perimetreSans(t, e, rel), nil); err != nil {
		t.Fatalf("reconcile : %v", err)
	}
	if len(lotsDeCorbeille(t, e)) != 1 {
		t.Fatal("précondition : le lot périmé n'a pas été créé")
	}

	// Un second retrait, aujourd'hui : il ne doit pas être emporté par la purge
	// du premier. Sans ce second lot, le test ne distinguerait pas une purge
	// correcte d'un `RemoveAll` sur la corbeille entière.
	const recent = "equipe/recent.md"
	writeFile(t, dir, recent, "frais\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle 2 : %v", err)
	}
	e.maintenant = func() time.Time { return depart }
	if err := e.reconcilePerimeter(perimetreSans(t, e, recent), nil); err != nil {
		t.Fatalf("reconcile 2 : %v", err)
	}

	lots := lotsDeCorbeille(t, e)
	if len(lots) != 1 {
		t.Fatalf("attendu le seul lot du jour après purge, trouvé : %v", lots)
	}
	if _, err := os.Stat(filepath.Join(e.racineCorbeille(), lots[0], filepath.FromSlash(recent))); err != nil {
		t.Errorf("la purge a emporté le lot du jour : %v", err)
	}
	_ = dir
}

// La corbeille vit sous `.vecu/`, que le scan écarte déjà. Elle ne peut donc pas
// se repousser au serveur - ce qui ferait redescendre chez les autres membres un
// fichier dont on vient précisément de retirer l'accès à quelqu'un.
func TestCorbeilleNeRemonteJamaisAuServeur(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)

	const rel = "equipe/parti.md"
	writeFile(t, dir, rel, "contenu\n")
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	if err := e.reconcilePerimeter(perimetreSans(t, e, rel), nil); err != nil {
		t.Fatalf("reconcile : %v", err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle après corbeille : %v", err)
	}

	perimetre, err := e.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range perimetre {
		if strings.Contains(p, "corbeille") || strings.Contains(p, vecuDir) {
			t.Errorf("un chemin de corbeille est arrivé sur le serveur : %s", p)
		}
	}
	_ = dir
}
