package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Détection de course : une écriture locale arrivée entre le scan et le pull
// n'est pas une divergence, et ne doit produire aucune copie de conflit.
//
// Les deux premiers tests viennent de `wip/convergence-report-mesures` (18/08),
// portés sur la signature courante. Mesurés en échec sur v0.6.0 avant ce
// chantier : 1 copie sur une course simple, 8 chez B / 6 chez A / 7 sur le
// serveur sur huit cycles.

func TestCourseNeReverteLaVersionDeLAutre(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()

	// A modifie la ligne 1 et pousse.
	writeFile(t, dirA, "equipe/plan.md", "L1-A\nL2\nL3\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// B : photo du cycle, puis écriture pendant le cycle sur une AUTRE ligne.
	// Les deux éditions sont fusionnables, aucun désaccord réel.
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "L1\nL2\nL3-B\n")
	reportes, err := b.pull(b.state.Head, vu, nil)
	if err != nil {
		t.Fatalf("pull B : %v", err)
	}

	if !reportes["equipe/plan.md"] {
		t.Error("la course n'a pas été détectée : le chemin n'est pas reporté")
	}
	if copies := copiesDeConflit(t, dirB, "equipe"); len(copies) != 0 {
		t.Errorf("copie de conflit sur une course : %v", copies)
	}
	if local, _ := readFile(t, dirB, "equipe/plan.md"); local != "L1\nL2\nL3-B\n" {
		t.Errorf("le contenu local n'a pas été laissé intact : %q", local)
	}

	// Cycle suivant de B : il pousse son contenu local, avec le marque-page du
	// chemin comme ancêtre - resté sur le commit d'avant la course.
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B suivant : %v", err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A final : %v", err)
	}

	final, _ := readFile(t, dirA, "equipe/plan.md")
	if !strings.Contains(final, "L1-A") {
		t.Errorf("la modification de A a disparu sans conflit ni copie : %q", final)
	}
	if !strings.Contains(final, "L3-B") {
		t.Errorf("la modification de B n'est pas arrivée : %q", final)
	}
}

// editeSaLigne fait ce que fait un vrai éditeur : relire le fichier courant et
// ne changer QUE sa propre ligne. Réécrire le fichier entier avec une valeur
// figée annulerait délibérément la ligne de l'autre à chaque passage, ce qui est
// un désaccord réel et non une course - le harnais d'origine mesurait ça sans le
// dire, et son verdict attendu était donc contradictoire.
func editeSaLigne(t *testing.T, dir, rel, val string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	lignes := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	lignes[len(lignes)-1] = val
	writeFile(t, dir, rel, strings.Join(lignes, "\n")+"\n")
}

// TestUnCycleCalmeDegeleLeMarquePage : ce qui borne la durée d'un report.
//
// Un chemin reporté garde son marque-page sur le commit d'avant, donc ses push
// repartent d'une base qui vieillit. Un seul cycle sans écriture locale suffit à
// le dégeler : le pull applique la version du serveur et réinscrit le marque-page
// à jour. C'est la propriété qui fait tenir toute la tranche - sans elle, un
// poste qui écrit sans arrêt fabrique des conflits serveur (limite connue,
// documentée dans la spec).
func TestUnCycleCalmeDegeleLeMarquePage(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()
	avant := b.state.Versions["equipe/plan.md"]

	writeFile(t, dirA, "equipe/plan.md", "L1-A\nL2\nL3\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// Cycle en course : le marque-page ne doit pas bouger.
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	editeSaLigne(t, dirB, "equipe/plan.md", "L3-B")
	if _, err := b.pull(b.state.Head, vu, nil); err != nil {
		t.Fatalf("pull en course : %v", err)
	}
	if b.state.Versions["equipe/plan.md"] != avant {
		t.Error("le marque-page a bougé sur un chemin reporté : le poste n'a rien reçu, il ne peut rien déclarer")
	}

	// Cycle calme : aucune écriture locale pendant le cycle.
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle calme : %v", err)
	}
	if b.state.Versions["equipe/plan.md"] == avant {
		t.Error("un cycle calme n'a pas dégelé le marque-page : le report ne serait pas borné")
	}
	local, _ := readFile(t, dirB, "equipe/plan.md")
	if !strings.Contains(local, "L1-A") {
		t.Errorf("le cycle calme n'a pas fait descendre la version de l'autre : %q", local)
	}
	if !strings.Contains(local, "L3-B") {
		t.Errorf("le cycle calme a écrasé l'édition locale : %q", local)
	}
	if copies := copiesDeConflit(t, dirB, "equipe"); len(copies) != 0 {
		t.Errorf("copie de conflit alors que la fusion était propre : %v", copies)
	}
}

// TestCourseEntretenueNePerdRienEtConverge : le régime entretenu. Les deux
// membres écrivent, un cycle calme sur deux de chaque côté.
//
// Ce test ne demande PAS zéro copie : deux postes qui écrivent en même temps sur
// la même ligne sont en désaccord réel, et la copie est la réponse juste. Il
// demande que rien ne se perde, que les deux postes convergent, et que le nombre
// de copies reste borné par le nombre de cycles disputés.
func TestCourseEntretenueNePerdRienEtConverge(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()

	disputes := 0
	for i := 0; i < 8; i++ {
		writeFile(t, dirA, "equipe/plan.md", fmt.Sprintf("L1-A%d\nL2\nL3\n", i))
		if err := a.SyncOnce(); err != nil {
			t.Fatalf("cycle A %d : %v", i, err)
		}
		vu, err := b.scanLocal()
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			editeSaLigne(t, dirB, "equipe/plan.md", fmt.Sprintf("L3-B%d", i))
			disputes++
		}
		acceptes, err := b.pushLocal(b.state.Head, vu)
		if err != nil {
			t.Fatalf("push B %d : %v", i, err)
		}
		if _, err := b.pull(b.state.Head, vu, acceptes); err != nil {
			t.Fatalf("pull B %d : %v", i, err)
		}
	}

	// La course s'arrête : les cycles suivants doivent converger sans geste manuel.
	for _, cycle := range []func() error{b.SyncOnce, a.SyncOnce, b.SyncOnce, a.SyncOnce} {
		if err := cycle(); err != nil {
			t.Fatalf("cycle de convergence : %v", err)
		}
	}
	finalA, _ := readFile(t, dirA, "equipe/plan.md")
	finalB, _ := readFile(t, dirB, "equipe/plan.md")
	if finalA != finalB {
		t.Errorf("les deux postes ne convergent pas :\nA = %q\nB = %q", finalA, finalB)
	}

	// Rien de perdu : la dernière édition de chacun est lisible quelque part, au
	// nom canonique ou dans une copie.
	copies := copiesDeConflit(t, dirB, "equipe")
	tout := finalB
	for _, c := range copies {
		contenu, _ := readFile(t, dirB, "equipe/"+c)
		tout += contenu
	}
	if !strings.Contains(tout, "L1-A7") {
		t.Errorf("la dernière version de A est introuvable, ni au canonique ni en copie : %q", tout)
	}
	if !strings.Contains(tout, "L3-B6") {
		t.Errorf("la dernière version de B est introuvable, ni au canonique ni en copie : %q", tout)
	}
	if len(copies) > disputes {
		t.Errorf("plus de copies (%d) que de cycles disputés (%d) : %v", len(copies), disputes, copies)
	}
	t.Logf("%d cycles disputés -> %d copies, convergence OK", disputes, len(copies))
}

// TestDivergenceReelleProduitToujoursSaCopie : la non-régression du report. Un
// fichier qui avait DÉJÀ divergé au moment du scan n'est pas une course, et son
// arbitrage ne change pas.
func TestDivergenceReelleProduitToujoursSaCopie(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()

	writeFile(t, dirA, "equipe/plan.md", "L1-A\nL2\nL3\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// L'édition locale de B est là AVANT le scan, et son état connu ne la porte
	// pas : c'est une édition jamais poussée, pas une course.
	writeFile(t, dirB, "equipe/plan.md", "divergence\n")
	b.state.Files["equipe/plan.md"] = hashContent([]byte("L1\nL2\nL3\n"))
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	reportes, err := b.pull(b.state.Head, vu, nil)
	if err != nil {
		t.Fatalf("pull B : %v", err)
	}

	if reportes["equipe/plan.md"] {
		t.Error("une divergence antérieure au scan a été prise pour une course")
	}
	if copies := copiesDeConflit(t, dirB, "equipe"); len(copies) != 1 {
		t.Errorf("la copie de conflit d'une divergence réelle a disparu : %v", copies)
	}
}

// TestCheminIgnoreJamaisDiagnostiqueEnCourse : un chemin écarté par le
// .vecuignore ne doit produire ni report ni copie, quoi qu'il arrive sur le
// disque pendant le cycle. Sans quoi un motif d'ignore gèlerait la descente du
// delta pour tout le monde.
func TestCheminIgnoreJamaisDiagnostiqueEnCourse(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/notes.md", "origine\n")
	a.SyncOnce()
	b.SyncOnce()

	writeFile(t, dirA, "equipe/notes.md", "version de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	writeFile(t, dirB, ".vecuignore", "notes.md\n")
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/notes.md", "ecriture locale pendant le cycle\n")

	motifs, err := LoadIgnores(dirB)
	if err != nil {
		t.Fatal(err)
	}
	b.motifs = motifs

	reportes, err := b.pull(b.state.Head, vu, nil)
	if err != nil {
		t.Fatalf("pull B : %v", err)
	}
	if reportes["equipe/notes.md"] {
		t.Error("un chemin ignoré a été diagnostiqué en course")
	}
	if copies := copiesDeConflit(t, dirB, "equipe"); len(copies) != 0 {
		t.Errorf("copie de conflit sur un chemin ignoré : %v", copies)
	}
}

// TestLienSymboliqueJamaisDiagnostiqueEnCourse : le scan classe un lien en
// `incertains`, son état n'est pas établi. Le reporter le reporterait à chaque
// cycle sans jamais pouvoir conclure, alors qu'un report est temporaire par
// construction.
func TestLienSymboliqueJamaisDiagnostiqueEnCourse(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "origine\n")
	a.SyncOnce()
	b.SyncOnce()

	writeFile(t, dirA, "equipe/plan.md", "version de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// Un lien prend la place du fichier sur le poste de B.
	abs := filepath.Join(dirB, "equipe", "plan.md")
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/ailleurs.md", "cible\n")
	if err := os.Symlink(filepath.Join(dirB, "equipe", "ailleurs.md"), abs); err != nil {
		t.Fatal(err)
	}

	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	reportes, err := b.pull(b.state.Head, vu, nil)
	if err != nil {
		t.Fatalf("pull B : %v", err)
	}
	if reportes["equipe/plan.md"] {
		t.Error("un lien symbolique a été diagnostiqué en course")
	}
	if cible, _ := readFile(t, dirB, "equipe/ailleurs.md"); cible != "cible\n" {
		t.Errorf("le contenu visé par le lien a été touché : %q", cible)
	}
}

// TestReporteResteIntactJusquaLaFinDuCycle : `rattrape` et `reconcilePerimeter`
// tournent APRÈS le pull, et aucun des deux ne doit défaire ce que le report
// vient de préserver.
//
// Ce test mesure la PROPRIÉTÉ, pas le mécanisme : côté `reconcilePerimeter`
// c'est bien le set de reportés qui la tient, côté `rattrape` ce sont ses gardes
// propres (« connu et présent »), qui n'ont aucun lien avec le report. Le dire,
// parce qu'un nom qui promet un arrimage inexistant fait croire à une couverture
// qu'on n'a pas.
func TestReporteResteIntactJusquaLaFinDuCycle(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "origine\n")
	a.SyncOnce()
	b.SyncOnce()

	writeFile(t, dirA, "equipe/plan.md", "version de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "ecriture locale pendant le cycle\n")
	reportes, err := b.pull(b.state.Head, vu, nil)
	if err != nil {
		t.Fatalf("pull B : %v", err)
	}
	if !reportes["equipe/plan.md"] {
		t.Fatal("la course n'a pas été détectée")
	}

	perimetre, err := b.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.rattrape(perimetre, vu, nil, nil); err != nil {
		t.Fatalf("rattrape : %v", err)
	}
	if err := b.reconcilePerimeter(perimetre, reportes); err != nil {
		t.Fatalf("reconcile : %v", err)
	}

	local, present := readFile(t, dirB, "equipe/plan.md")
	if !present {
		t.Fatal("le chemin reporté a été retiré du disque dans le même cycle")
	}
	if local != "ecriture locale pendant le cycle\n" {
		t.Errorf("le chemin reporté a été réécrit dans le même cycle : %q", local)
	}
}

// TestConflitDePushPuisCourseNeFaitQuUneCopie : trouvé en revue adversariale.
//
// La branche `res.Conflict` de `pushLocal` fait `oublie(p)` et compte sur le
// rattrapage du MÊME cycle pour redescendre le canonique. Un garde de report
// posé trop large dans `rattrape` annulait cette réparation : deux copies pour
// une seule divergence, et un push suivant sans ancêtre.
func TestConflitDePushPuisCourseNeFaitQuUneCopie(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()

	// A et B modifient la MÊME ligne : le serveur ne pourra pas fusionner.
	writeFile(t, dirA, "equipe/plan.md", "ligne de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}
	writeFile(t, dirB, "equipe/plan.md", "ligne de B\n")

	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	acceptes, err := b.pushLocal(b.state.Head, vu)
	if err != nil {
		t.Fatalf("push B : %v", err)
	}
	// Course : une écriture locale tombe après le push, sur un chemin que le
	// conflit vient d'oublier de l'état.
	writeFile(t, dirB, "equipe/plan.md", "ecriture pendant le cycle\n")
	if _, err := b.pull(b.state.Head, vu, acceptes); err != nil {
		t.Fatalf("pull B : %v", err)
	}
	perimetre, err := b.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.rattrape(perimetre, vu, acceptes, nil); err != nil {
		t.Fatalf("rattrape : %v", err)
	}

	// Deux contenus distincts sont en jeu - celui que le push a envoyé et perdu
	// au profit de A, et celui écrit pendant le cycle - donc deux copies sont la
	// réponse juste : une côté serveur pour le désaccord réel, une locale pour
	// l'écriture de course. Mesuré identique sur `main` : ce test verrouille
	// l'absence de copie SUPPLÉMENTAIRE, pas un progrès sur ce cas-là.
	copies := copiesDeConflit(t, dirB, "equipe")
	locales := 0
	for _, c := range copies {
		if strings.Contains(c, "conflit local") {
			locales++
		}
	}
	if locales > 1 {
		t.Errorf("%d copies LOCALES pour une seule écriture de course : %v", locales, copies)
	}
	if len(copies) > 2 {
		t.Errorf("%d copies là où le code d'avant en produit 2 : %v", len(copies), copies)
	}
	// Le chemin a été oublié par le conflit, donc il n'a plus de marque-page :
	// il n'est PAS reporté, et le rattrapage du même cycle le répare comme avant
	// la tranche. Ce que ce test verrouille est qu'il ne s'en fabrique pas une
	// deuxième, et que rien ne se perd.
	tout, _ := readFile(t, dirB, "equipe/plan.md")
	for _, c := range copies {
		contenu, _ := readFile(t, dirB, "equipe/"+c)
		tout += contenu
	}
	if !strings.Contains(tout, "ecriture pendant le cycle") {
		t.Errorf("l'écriture arrivée pendant le cycle est introuvable : %q", tout)
	}
}

// TestCheminSansMarquePageNestPasReporte : la précondition du report, trouvée
// au deuxième passage de revue adversariale.
//
// Reporter n'a de sens que si le push différé pourra déclarer un ancêtre. Sur un
// chemin sans marque-page - fichier local tout neuf, ou entrée qu'un conflit de
// push vient d'oublier - `ancetreDe` rend la chaîne vide, donc la fusion serveur
// part sans base commune et range la divergence dans une copie. Le report ne
// ferait que retarder cette copie en promettant une convergence qu'il ne peut
// pas produire.
//
// Le comportement attendu est donc celui d'avant la tranche : une copie, et
// surtout rien de perdu.
func TestCheminSansMarquePageNestPasReporte(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "version de A\n")
	a.SyncOnce()
	b.SyncOnce()

	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	// Ni état connu ni marque-page : la forme que prend un chemin oublié par un
	// conflit de push, et celle d'un fichier jamais reçu.
	delete(b.state.Files, "equipe/plan.md")
	delete(b.state.Versions, "equipe/plan.md")
	writeFile(t, dirB, "equipe/plan.md", "ecriture pendant le cycle\n")

	perimetre, err := b.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.rattrape(perimetre, vu, nil, nil); err != nil {
		t.Fatalf("rattrape : %v", err)
	}

	// Rien de perdu : le contenu écrit pendant le cycle est lisible quelque part.
	tout, _ := readFile(t, dirB, "equipe/plan.md")
	for _, c := range copiesDeConflit(t, dirB, "equipe") {
		contenu, _ := readFile(t, dirB, "equipe/"+c)
		tout += contenu
	}
	if !strings.Contains(tout, "ecriture pendant le cycle") {
		t.Errorf("l'écriture arrivée pendant le cycle est introuvable : %q", tout)
	}
}

// TestCourseNeLitJamaisATraversUnLien : trouvé en revue adversariale.
//
// `hashFile` fait un ReadFile, qui suit les liens. Un lien posé PENDANT le cycle
// est absent du scan, donc absent de `incertains` : sans garde, la détection
// lisait et hachait un contenu situé hors de la racine locale.
func TestCourseNeLitJamaisATraversUnLien(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "origine\n")
	a.SyncOnce()
	b.SyncOnce()

	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}

	// Un lien vers un contenu HORS de la racine prend la place du fichier,
	// pendant le cycle donc après le scan.
	dehors := filepath.Join(t.TempDir(), "prive.txt")
	if err := os.WriteFile(dehors, []byte("contenu personnel hors racine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dirB, "equipe", "plan.md")
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dehors, abs); err != nil {
		t.Fatal(err)
	}

	course, motif := b.changeDepuisLeScan("equipe/plan.md", vu)
	if course {
		t.Errorf("un lien posé pendant le cycle a été lu et diagnostiqué en course (%s)", motif)
	}
}

// TestEditionDeCourseDisparueRecoitQuandMemeLaVersionSautee : le registre des
// reportés, et la perte qu'il ferme.
//
// Le report fait avancer le head au-dessus du changement sauté. `Sync(head)` ne
// le re-listera donc jamais, et `rattrape` saute le chemin comme « connu et
// présent ». La seule route de retour est le push différé du cycle suivant - et
// ce push peut ne jamais partir. Ici, l'édition de course revient exactement au
// contenu déjà connu de l'état : le scan suivant ne voit plus rien à envoyer.
//
// Sans le registre, ce poste garde une version périmée indéfiniment pendant que
// l'équipe entière a l'autre, et rien ne le dit.
func TestEditionDeCourseDisparueRecoitQuandMemeLaVersionSautee(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()

	writeFile(t, dirA, "equipe/plan.md", "L1-A\nL2\nL3\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// Course : B écrit entre son scan et son pull.
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "L1\nL2\nL3-B\n")
	reportes, err := b.pull(b.state.Head, vu, nil)
	if err != nil {
		t.Fatalf("pull B : %v", err)
	}
	if !reportes["equipe/plan.md"] {
		t.Fatal("la course n'a pas été détectée")
	}
	if len(b.state.Reportes) != 1 || b.state.Reportes[0] != "equipe/plan.md" {
		t.Fatalf("le chemin n'est pas entré au registre des reportés : %v", b.state.Reportes)
	}

	// L'édition de course disparaît : le fichier revient au contenu que l'état
	// connaît déjà. Le push du cycle suivant n'aura donc rien à envoyer.
	writeFile(t, dirB, "equipe/plan.md", "L1\nL2\nL3\n")

	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle suivant : %v", err)
	}

	local, _ := readFile(t, dirB, "equipe/plan.md")
	if !strings.Contains(local, "L1-A") {
		t.Errorf("la version de A n'est jamais arrivée : %q", local)
	}
	if len(b.state.Reportes) != 0 {
		t.Errorf("le chemin est resté au registre après avoir été repêché : %v", b.state.Reportes)
	}
	if b.state.Versions["equipe/plan.md"] == "" {
		t.Error("le chemin repêché est reparti sans marque-page")
	}
	if copies := copiesDeConflit(t, dirB, "equipe"); len(copies) != 0 {
		t.Errorf("copie de conflit sur un repêchage : %v", copies)
	}
}

// TestRepechageNecraseJamaisUneEcritureEnCours : le repêchage est un chemin
// d'écriture, il doit donc porter le même contrôle de course que le pull.
func TestRepechageNecraseJamaisUneEcritureEnCours(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()
	writeFile(t, dirA, "equipe/plan.md", "L1-A\nL2\nL3\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "L1\nL2\nL3-B\n")
	if _, err := b.pull(b.state.Head, vu, nil); err != nil {
		t.Fatalf("pull B : %v", err)
	}

	// Cycle suivant : le chemin est au registre, mais une nouvelle écriture
	// tombe encore pendant le cycle.
	vu2, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "L1\nL2\nL3-B-encore\n")
	perimetre, err := b.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.rattrape(perimetre, vu2, nil, map[string]bool{}); err != nil {
		t.Fatalf("rattrape : %v", err)
	}

	if local, _ := readFile(t, dirB, "equipe/plan.md"); local != "L1\nL2\nL3-B-encore\n" {
		t.Errorf("le repêchage a écrasé une écriture arrivée pendant le cycle : %q", local)
	}
	if len(b.state.Reportes) != 1 {
		t.Errorf("le chemin doit rester au registre tant qu'il n'a pas été repêché : %v", b.state.Reportes)
	}
}

// TestEtatSeRechargeDansLesDeuxSens : la règle du champ additif. Un état écrit
// par cette version se recharge sur la précédente, et l'inverse.
func TestEtatSeRechargeDansLesDeuxSens(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, vecuDir), 0o755); err != nil {
		t.Fatal(err)
	}

	// Sens 1 : un état SANS `reportes` (écrit par v0.6.0) se charge ici.
	ancien := `{"head":"abc","files":{"equipe/plan.md":"deadbeef"},"versions":{"equipe/plan.md":"abc"}}`
	if err := os.WriteFile(filepath.Join(dir, vecuDir, "state.json"), []byte(ancien), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("un état de v0.6.0 ne se charge pas : %v", err)
	}
	if len(st.Reportes) != 0 {
		t.Errorf("registre non vide sur un état qui n'en portait pas : %v", st.Reportes)
	}

	// Sens 2 : un état AVEC `reportes` se relit sans perte, et un état sans
	// aucun report s'écrit exactement comme avant (omitempty).
	st.Reportes = []string{"equipe/plan.md"}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	relu, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(relu.Reportes) != 1 || relu.Reportes[0] != "equipe/plan.md" {
		t.Errorf("le registre ne survit pas à un aller-retour : %v", relu.Reportes)
	}

	relu.Reportes = nil
	if err := relu.Save(dir); err != nil {
		t.Fatal(err)
	}
	brut, err := os.ReadFile(filepath.Join(dir, vecuDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(brut), "reportes") {
		t.Errorf("un état sans report ne doit pas écrire la clé (omitempty) : %s", brut)
	}
}

// TestPushRefusePourToujoursNempechePasDeRecevoir : la régression que la slice 1
// ouvrait, trouvée au troisième passage de revue adversariale.
//
// Le report parie sur le push du cycle suivant. `pushLocal` a des branches de
// refus PERMANENT qui ne touchent pas `Files` - contenu non-UTF8, espace passé
// en lecture seule. Le pari est alors perdu pour toujours : le head a dépassé le
// changement, `rattrape` saute le chemin comme « connu et présent », et la
// version de l'autre membre n'arrive jamais sur ce poste.
//
// Le registre des reportés est ce qui la fait arriver quand même.
func TestPushRefusePourToujoursNempechePasDeRecevoir(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()

	writeFile(t, dirA, "equipe/plan.md", "L1-A\nL2\nL3\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// Course, avec un contenu que le push refusera à chaque cycle, pour toujours.
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "equipe", "plan.md"), []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pull(b.state.Head, vu, nil); err != nil {
		t.Fatalf("pull B : %v", err)
	}

	// Le contenu binaire reste en place : le push le refusera cycle après cycle.
	for i := 0; i < 3; i++ {
		if err := b.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", i, err)
		}
	}

	local, _ := readFile(t, dirB, "equipe/plan.md")
	if !strings.Contains(local, "L1-A") {
		t.Errorf("la version de A n'est jamais arrivée alors que le push est refusé pour toujours : %q", local)
	}
	// Rien de perdu : le contenu binaire local part en copie plutôt qu'à la
	// poubelle, comme toute édition locale que le serveur n'a pas.
	if copies := copiesDeConflit(t, dirB, "equipe"); len(copies) == 0 {
		t.Error("le contenu local refusé par le serveur a été détruit")
	}
}

// TestSuppressionServeurNestJamaisReportee : trouvé au deuxième passage de revue
// adversariale.
//
// Le report ne couvre que les écritures entrantes. Une suppression venue du
// serveur doit s'appliquer : la reporter revient à la défaire pour toute
// l'équipe au cycle suivant, quand le poste renverra son contenu. L'édition
// locale n'est pas perdue pour autant, elle part en copie.
func TestSuppressionServeurNestJamaisReportee(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "contenu partagé\n")
	writeFile(t, dirA, "equipe/garde.md", "pour que l'espace ne se vide pas\n")
	a.SyncOnce()
	b.SyncOnce()

	// A supprime le fichier et pousse la suppression.
	if err := os.Remove(filepath.Join(dirA, "equipe", "plan.md")); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// B écrit dans ce fichier entre son scan et son pull.
	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "edition de B pendant le cycle\n")
	reportes, err := b.pull(b.state.Head, vu, nil)
	if err != nil {
		t.Fatalf("pull B : %v", err)
	}

	if reportes["equipe/plan.md"] {
		t.Error("une suppression venue du serveur a été reportée : elle sera défaite pour toute l'équipe")
	}
	if _, present := readFile(t, dirB, "equipe/plan.md"); present {
		t.Error("la suppression de A n'a pas été appliquée sur le poste de B")
	}
	copies := copiesDeConflit(t, dirB, "equipe")
	if len(copies) != 1 {
		t.Fatalf("l'édition locale devait partir en copie, une seule fois : %v", copies)
	}
	if contenu, _ := readFile(t, dirB, "equipe/"+copies[0]); contenu != "edition de B pendant le cycle\n" {
		t.Errorf("la copie ne porte pas l'édition locale : %q", contenu)
	}
}

// TestMarquePageVideNestPasReportable : la précondition, verrouillée.
//
// `Versions[p]` vide n'est pas un état théorique : `pushLocal` l'écrit lui-même
// quand le serveur refuse un envoi en 400, pour que le chemin reparte sans
// ancêtre. Reporter un tel chemin ne mène à rien - le push différé déclarerait
// la chaîne vide, donc une fusion sans base commune, donc la même copie qu'on
// prétendait éviter, avec une laisse qui promet le contraire.
func TestMarquePageVideNestPasReportable(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	writeFile(t, dirA, "equipe/plan.md", "L1\nL2\nL3\n")
	a.SyncOnce()
	b.SyncOnce()
	writeFile(t, dirA, "equipe/plan.md", "L1-A\nL2\nL3\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}

	// L'état que `pushLocal` produit après un refus serveur en 400 : le chemin
	// reste suivi, son marque-page est vidé.
	b.state.Versions["equipe/plan.md"] = ""

	vu, err := b.scanLocal()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirB, "equipe/plan.md", "L1\nL2\nL3-B\n")
	reportes, err := b.pull(b.state.Head, vu, nil)
	if err != nil {
		t.Fatalf("pull B : %v", err)
	}

	if reportes["equipe/plan.md"] {
		t.Error("un chemin sans marque-page a été reporté : le push différé n'aura aucun ancêtre à déclarer")
	}
	if len(b.state.Reportes) != 0 {
		t.Errorf("un chemin non reportable est entré au registre : %v", b.state.Reportes)
	}
}
