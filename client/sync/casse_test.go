package sync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// casse_test.go : la collision de casse sur un disque insensible à la casse.
//
// Mesuré le 23/08 sur le poste de Colin : `.vecu/state.json` porte TROIS clés
// pour DEUX fichiers dans `shared/process/`.
//
//	shared/process/AGENTS.md          -> 0b7d130ed990  (le contexte, 7130 o)
//	shared/process/registre-agents.md -> 87a0536d8916  (le registre, 3661 o)
//	shared/process/agents.md          -> 87a0536d8916  (AUCUN fichier à cette casse)
//
// La troisième est un fantôme : sur APFS ce chemin résout vers `AGENTS.md`,
// donc vers `0b7d130ed990`. La clé désigne en permanence un contenu qui n'est
// pas le sien.
//
// Et ce sont les trois empreintes de la ligne d'Achille du 22/08 à 05:01:21 :
//
//	édition locale non poussée préservée : shared/process/AGENTS.md -> … (conflit local).md
//	  — local 87a0536d8916 3661o, connu (aucune), entrant 0b7d130ed990 ;
//	    report refusé : chemin non suivi
//
// Le défaut est connu depuis le 24/07 (« défaut 4 » de
// spec-suppression-constatee.md), reporté explicitement, et il a produit sa
// première copie un mois plus tard.

// disqueInsensibleALaCasse CONSTATE ce que le disque fait, plutôt que de le
// déduire de `runtime.GOOS`. Un dossier APFS peut être créé sensible à la
// casse, et un volume monté peut l'être aussi : sur ce sujet précis, déduire
// du système d'exploitation serait exactement l'erreur qu'on instrumente.
func disqueInsensibleALaCasse(t *testing.T, dir string) bool {
	t.Helper()
	sonde := filepath.Join(dir, "SondeCasse.tmp")
	if err := os.WriteFile(sonde, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(sonde)
	_, err := os.Stat(filepath.Join(dir, "sondecasse.tmp"))
	return err == nil
}

// nomsSurLeDisque rend les noms RÉELS d'un dossier, à la casse exacte.
// `os.Stat` ne peut pas répondre à cette question sur APFS : il résout la
// casse. Seule la lecture du dossier dit ce que le disque porte vraiment.
func nomsSurLeDisque(t *testing.T, dir string) []string {
	t.Helper()
	entrees, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var noms []string
	for _, e := range entrees {
		noms = append(noms, e.Name())
	}
	return noms
}

// TestCollisionDeCasseNeProduitPlusLaCopieDu22Aout rejoue le cas du 22/08 dans
// sa forme mesuree, et asserte qu'il ne produit plus rien.
//
// CE QU'IL PRODUISAIT, avant le refus (commit 298bf5d, ou ce test etait ecrit a
// l'envers et passait) :
//
//	edition locale non poussee preservee : equipe/AGENTS.md -> equipe/AGENTS (conflit local).md
//	  — local a0bcaa1f7a31 26o, connu (aucune), entrant 5cd04f4bccf2 ;
//	    report refuse : chemin non suivi
//
// La meme ligne que celle d'Achille, aux empreintes pres. Elle ne doit plus
// jamais sortir sur ce cas.
//
// Le `git mv agents.md registre-agents.md` de Colin ayant ECHOUE le 22/08,
// `agents.md` n'a jamais ete retire du serveur : c'est pour ca que le fantome y
// est encore, et c'est cette configuration-la qu'on rejoue.
func TestCollisionDeCasseNeProduitPlusLaCopieDu22Aout(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB, journal := moteurBavard(t, url, token)

	if !disqueInsensibleALaCasse(t, dirB) {
		t.Skip("disque sensible a la casse : les deux chemins coexistent legitimement, il n'y a rien a reproduire")
	}

	const registre = "# Registre des sub-agents\n"
	const contexte = "# Contexte du dossier\n"

	writeFile(t, dirA, "equipe/agents.md", registre)
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if got := b.state.Files["equipe/agents.md"]; got == "" {
		t.Fatal("prerequis : B devrait suivre equipe/agents.md")
	}

	// Le second chemin apparait sur le serveur, a la casse pres. Pousse par un
	// chemin qui ne passe pas par le disque de A : sur APFS son poste ne peut pas
	// porter les deux non plus, et c'est le SERVEUR qu'on veut dans cet etat.
	if _, err := a.client.Put("equipe/AGENTS.md", contexte, "", ""); err != nil {
		t.Fatal(err)
	}

	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	if ligne := lignePreservation(t, *journal); ligne != "" {
		t.Errorf("la copie du 22/08 est de retour :\n  %s", ligne)
	}
	var laisse string
	for _, l := range *journal {
		if strings.Contains(l, "collision de casse") {
			laisse = l
		}
	}
	if laisse == "" {
		t.Fatalf("la collision devrait etre signalee :\n  %v", *journal)
	}
	// La laisse nomme LES DEUX chemins : sans les deux, elle n'est pas
	// actionnable, et c'est un humain qui doit trancher lequel garder.
	for _, attendu := range []string{"equipe/AGENTS.md", "equipe/agents.md"} {
		if !strings.Contains(laisse, attendu) {
			t.Errorf("la laisse ne nomme pas %q :\n  %s", attendu, laisse)
		}
	}
	// Et le disque n'a pas bouge : le voisin garde son contenu et son nom.
	noms := nomsSurLeDisque(t, filepath.Join(dirB, "equipe"))
	if !slices.Contains(noms, "agents.md") {
		t.Errorf("le disque devrait toujours porter agents.md : %v", noms)
	}
	t.Logf("laisse : %s", laisse)
}

// TestFantomeDeCassePreexistantEstInerte : l'etat du poste de Colin
// AUJOURD'HUI, reproduit tel qu'il a ete mesure le 23/08.
//
//	serveur : agents.md ET AGENTS.md, contenus differents
//	disque  : UN fichier, nomme AGENTS.md, portant le contexte
//	etat    : les DEUX cles, agents.md portant l'empreinte du registre
//
// Le refus de la slice 2 empeche de creer de NOUVELLES cles fantomes ; il
// n'efface pas celles qui existent. La question qui compte est donc : est-ce
// qu'un poste dans cet etat peut detruire quelque chose au prochain cycle ?
//
// Mesure : non. Le fantome est INERTE, et il faut nommer les deux raisons sous
// peine de les voir « corrigees » un jour :
//
//  1. Le disque ne porte aucun fichier nomme `agents.md`, donc le scan ne le
//     voit pas, donc il devrait compter pour une suppression constatee. Ce qui
//     l'en empeche est la derniere verification de `suppressionsConstatees`,
//     `absentCommeFichier`, qui appelle `os.Lstat` - et `os.Lstat` RESOUT la
//     casse sur APFS. Il repond « quelque chose occupe encore ce chemin ».
//     L'insensibilite qui cause le defaut est ce qui l'empeche de detruire.
//  2. `AGENTS.md` est dans le perimetre serveur, donc le rattrapage ne le
//     retire pas du disque. C'est la condition qui manque a la perte decrite
//     dans la carte « retrait hors perimetre » : voir le Change Summary.
//
// Ce test fige les deux. S'ils cessaient de tenir, il tomberait ici plutot
// qu'en production.
func TestFantomeDeCassePreexistantEstInerte(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB, journal := moteurBavard(t, url, token)

	if !disqueInsensibleALaCasse(t, dirB) {
		t.Skip("disque sensible a la casse")
	}

	const registre = "# Registre des sub-agents\n"
	const contexte = "# Contexte du dossier\n"

	writeFile(t, dirA, "equipe/agents.md", registre)
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.Put("equipe/AGENTS.md", contexte, "", ""); err != nil {
		t.Fatal(err)
	}

	// L'etat herite, fabrique a la main : c'est celui qu'on ne peut plus
	// atteindre depuis la slice 2, et c'est celui des deux postes aujourd'hui.
	if err := os.Remove(filepath.Join(dirB, "equipe", "agents.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "equipe", "AGENTS.md"), []byte(contexte), 0o644); err != nil {
		t.Fatal(err)
	}
	b.state.Files["equipe/AGENTS.md"] = hashContent([]byte(contexte))
	b.state.Versions["equipe/AGENTS.md"] = b.state.Versions["equipe/agents.md"]

	for i := 0; i < 3; i++ {
		if err := b.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", i+1, err)
		}
	}

	perimetre, err := b.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	for _, chemin := range []string{"equipe/agents.md", "equipe/AGENTS.md"} {
		if !slices.Contains(perimetre, chemin) {
			t.Errorf("PERTE : %s a disparu du serveur alors que personne ne l'a supprime.\n"+
				"  Chemins restants : %v", chemin, perimetre)
		}
	}
	noms := nomsSurLeDisque(t, filepath.Join(dirB, "equipe"))
	if !slices.Contains(noms, "AGENTS.md") {
		t.Errorf("le fichier local a disparu : %v", noms)
	}
	for _, n := range noms {
		if strings.Contains(n, marqueCopieLocale) {
			t.Errorf("copie de conflit inattendue : %s", n)
		}
	}
	if ligne := lignePreservation(t, *journal); ligne != "" {
		t.Errorf("une copie a ete faite sur un etat pourtant stable :\n  %s", ligne)
	}
}

// --- slice 1 : la détection, et ses trois configurations ---

// Sur un disque insensible à la casse, le voisin est trouvé et nommé.
func TestVoisinDeCasseTrouveLautreCasse(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	if !disqueInsensibleALaCasse(t, dir) {
		t.Skip("disque sensible à la casse")
	}
	writeFile(t, dir, "equipe/agents.md", "registre\n")

	if got := e.voisinDeCasse("equipe/AGENTS.md"); got != "equipe/agents.md" {
		t.Errorf("voisinDeCasse = %q, attendu equipe/agents.md", got)
	}
}

// Le chemin qui existe à sa casse EXACTE n'a rien à départager. C'est aussi la
// branche qui protège un disque SENSIBLE à la casse portant les deux fichiers :
// là-bas les deux coexistent légitimement, et le nom exact est dans la liste.
// Cette configuration ne peut pas être créée sur APFS, d'où le test sur la
// moitié qui s'y reproduit.
func TestVoisinDeCasseSeTaitSurLeNomExact(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/AGENTS.md", "contexte\n")

	if got := e.voisinDeCasse("equipe/AGENTS.md"); got != "" {
		t.Errorf("voisinDeCasse = %q, attendu vide : le nom existe à sa casse exacte", got)
	}
}

// Aucun voisin, aucun fichier : rien à signaler, et surtout pas d'erreur.
func TestVoisinDeCasseSeTaitSansVoisin(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	writeFile(t, dir, "equipe/plan.md", "x\n")

	for _, rel := range []string{"equipe/autre.md", "equipe/PLAN2.md", "inconnu/x.md"} {
		if got := e.voisinDeCasse(rel); got != "" {
			t.Errorf("voisinDeCasse(%q) = %q, attendu vide", rel, got)
		}
	}
}

// Un nom de casse différente dans un AUTRE dossier n'est pas un voisin : la
// question se pose dossier par dossier.
func TestVoisinDeCasseNeTraversePasLesDossiers(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	if !disqueInsensibleALaCasse(t, dir) {
		t.Skip("disque sensible à la casse")
	}
	writeFile(t, dir, "equipe/sous/agents.md", "registre\n")

	if got := e.voisinDeCasse("equipe/AGENTS.md"); got != "" {
		t.Errorf("voisinDeCasse = %q, attendu vide", got)
	}
}

// Le banc du 22/08, relu : la collision se NOMME désormais dans le journal,
// avec les deux chemins. Sans changer quoi que ce soit au comportement.
func TestCollisionDeCasseSeJournalise(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB, journal := moteurBavard(t, url, token)

	if !disqueInsensibleALaCasse(t, dirB) {
		t.Skip("disque sensible à la casse")
	}

	writeFile(t, dirA, "equipe/agents.md", "# Registre\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.Put("equipe/AGENTS.md", "# Contexte\n", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	var ligne string
	for _, l := range *journal {
		if strings.Contains(l, "collision de casse") {
			ligne = l
		}
	}
	if ligne == "" {
		t.Fatalf("aucune ligne de collision dans le journal :\n  %v", *journal)
	}
	for _, attendu := range []string{"equipe/AGENTS.md", "equipe/agents.md"} {
		if !strings.Contains(ligne, attendu) {
			t.Errorf("la ligne ne nomme pas %q :\n  %s", attendu, ligne)
		}
	}
	t.Logf("ligne : %s", ligne)
}

// La garde du paquet tient ici aussi : un lien symbolique sur le chemin fait
// taire la détection, plutôt que de faire lire un dossier hors de la racine
// locale et d'en nommer le contenu dans le journal.
func TestVoisinDeCasseRefuseDeTraverserUnLien(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	if !disqueInsensibleALaCasse(t, dir) {
		t.Skip("disque sensible à la casse")
	}

	// Un dossier hors de la racine, portant le voisin qu'on ne doit PAS voir.
	dehors := t.TempDir()
	if err := os.WriteFile(filepath.Join(dehors, "agents.md"), []byte("perso\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// `equipe/sous` devient un lien vers ce dossier.
	if err := os.MkdirAll(filepath.Join(dir, "equipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dehors, filepath.Join(dir, "equipe", "sous")); err != nil {
		t.Fatal(err)
	}

	if got := e.voisinDeCasse("equipe/sous/AGENTS.md"); got != "" {
		t.Errorf("voisinDeCasse a traversé un lien et rendu %q", got)
	}
}

// Un DOSSIER de casse voisine est une collision aussi : le disque ne peut pas
// porter les deux non plus, et le pull échouerait à écrire. Le signaler vaut
// mieux que se taire.
func TestVoisinDeCasseVoitAussiUnDossier(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	if !disqueInsensibleALaCasse(t, dir) {
		t.Skip("disque sensible à la casse")
	}
	if err := os.MkdirAll(filepath.Join(dir, "equipe", "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := e.voisinDeCasse("equipe/NOTES"); got != "equipe/notes" {
		t.Errorf("voisinDeCasse = %q, attendu equipe/notes", got)
	}
}

// --- slice 2 : le refus ---

// Le banc du 22/08, après correction : plus de copie, plus de fantôme, et le
// disque garde le contenu qu'il avait. Sur PLUSIEURS cycles, parce que le mode
// d'échec redouté ici est le déluge - un refus qui se rejoue à chaque tour et
// fabrique une copie par cycle, ce qui serait pire que le défaut d'origine.
func TestCollisionDeCasseRefuseeEtStable(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB, journal := moteurBavard(t, url, token)

	if !disqueInsensibleALaCasse(t, dirB) {
		t.Skip("disque sensible à la casse")
	}

	const registre = "# Registre des sub-agents\n"
	const contexte = "# Contexte du dossier\n"

	writeFile(t, dirA, "equipe/agents.md", registre)
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.Put("equipe/AGENTS.md", contexte, "", ""); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		if err := b.SyncOnce(); err != nil {
			t.Fatalf("cycle %d : %v", i+1, err)
		}
	}

	// 1. Aucune copie de conflit, jamais.
	if ligne := lignePreservation(t, *journal); ligne != "" {
		t.Errorf("une copie a été faite alors que rien n'est écrasé :\n  %s", ligne)
	}
	for _, n := range nomsSurLeDisque(t, filepath.Join(dirB, "equipe")) {
		if strings.Contains(n, marqueCopieLocale) {
			t.Errorf("copie de conflit sur le disque : %s", n)
		}
	}

	// 2. Le disque garde le contenu qu'il avait, sous le nom qu'il avait.
	surLeDisque, err := hashFile(b.abs("equipe/agents.md"))
	if err != nil {
		t.Fatal(err)
	}
	if surLeDisque != hashContent([]byte(registre)) {
		t.Errorf("le disque a changé de contenu : le refus a écrit quand même")
	}

	// 3. Aucune clé fantôme : le chemin refusé n'entre pas dans l'état.
	if got := b.state.Files["equipe/AGENTS.md"]; got != "" {
		t.Errorf("le chemin refusé a produit une entrée d'état : %q", court(got))
	}

	// 4. Le contenu n'est pas perdu : il reste sur le serveur, pour un poste
	//    qui saura le distinguer, et pour l'arbitrage humain.
	perimetre, err := b.client.Tree()
	if err != nil {
		t.Fatal(err)
	}
	var vus int
	for _, p := range perimetre {
		if p == "equipe/agents.md" || p == "equipe/AGENTS.md" {
			vus++
		}
	}
	if vus != 2 {
		t.Errorf("les deux chemins devraient être intacts sur le serveur : %v", perimetre)
	}
}

// Le genre de la laisse, et c'est un piège que ce dépôt a déjà vu de près.
//
// Le doubt gate du 20/08 avait trouvé qu'un fichier ignoré produisait une
// `Laisse` de genre `fichier`, donc `vecu sync` en erreur à chaque cycle,
// indéfiniment - le déluge de F6 reconstruit par une autre porte. Ici la
// situation est la même : un cas qui se reproduit tant que la collision n'est
// pas arbitrée. Le genre doit dire « c'est sur le serveur », pas « c'est perdu ».
func TestCollisionDeCasseNeFaitPasEchouerLaCommande(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB, _ := moteurBavard(t, url, token)

	if !disqueInsensibleALaCasse(t, dirB) {
		t.Skip("disque sensible à la casse")
	}

	writeFile(t, dirA, "equipe/agents.md", "# Registre\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.Put("equipe/AGENTS.md", "# Contexte\n", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	var trouvee bool
	for _, l := range b.Laisses() {
		if !strings.Contains(l.Raison, "collision de casse") {
			continue
		}
		trouvee = true
		if l.Genre != GenreServeur {
			t.Errorf("genre = %q, attendu %q", l.Genre, GenreServeur)
		}
		if l.Perdu() {
			t.Error("la laisse se dit PERDUE : `vecu sync` sortirait en erreur à chaque cycle, " +
				"alors que le contenu est sur le serveur et que rien n'est perdu")
		}
	}
	if !trouvee {
		t.Fatal("aucune laisse de collision")
	}
}

// --- slice 3 : les fantômes se nomment ---

// Le cas mesuré le 23/08 sur le poste de Colin, aux chemins près.
func TestFantomesDeCasseNommeLeCasMesure(t *testing.T) {
	files := map[string]string{
		"shared/process/AGENTS.md":          "0b7d130ed990aaaaaaaa",
		"shared/process/registre-agents.md": "87a0536d8916aaaaaaaa",
		"shared/process/agents.md":          "87a0536d8916aaaaaaaa",
		"shared/process/CLAUDE.md":          "97f988f20f1faaaaaaaa",
		"shared/tasks.md":                   "636f857edb5eaaaaaaaa",
	}
	groupes := FantomesDeCasse(files)
	if len(groupes) != 1 {
		t.Fatalf("groupes = %v, attendu un seul", groupes)
	}
	attendu := []string{"shared/process/AGENTS.md", "shared/process/agents.md"}
	if !slices.Equal(groupes[0], attendu) {
		t.Errorf("groupe = %v, attendu %v", groupes[0], attendu)
	}
	// `registre-agents.md` porte la MÊME empreinte que le fantôme et n'est pas
	// une collision : la question porte sur le nom, jamais sur le contenu.
	for _, g := range groupes {
		if slices.Contains(g, "shared/process/registre-agents.md") {
			t.Error("un homonyme de contenu a été pris pour une collision de casse")
		}
	}
}

// Un état sain ne produit rien : la sortie doit rester vide sur les 933
// chemins d'un vault ordinaire, sinon personne ne la lira le jour où elle
// portera quelque chose.
func TestFantomesDeCasseSeTaitSurUnEtatSain(t *testing.T) {
	files := map[string]string{
		"shared/tasks.md":          "a",
		"shared/process/AGENTS.md": "b",
		"shared/process/CLAUDE.md": "c",
		"autre/AGENTS.md":          "d",
	}
	if groupes := FantomesDeCasse(files); len(groupes) != 0 {
		t.Errorf("groupes = %v, attendu aucun", groupes)
	}
	if groupes := FantomesDeCasse(nil); len(groupes) != 0 {
		t.Errorf("sur un état vide : %v", groupes)
	}
}

// Trois variantes du même nom forment UN groupe, pas trois paires : c'est un
// seul arbitrage à rendre.
func TestFantomesDeCasseGroupeToutesLesVariantes(t *testing.T) {
	files := map[string]string{
		"e/Notes.md": "a",
		"e/notes.md": "b",
		"e/NOTES.md": "c",
	}
	groupes := FantomesDeCasse(files)
	if len(groupes) != 1 || len(groupes[0]) != 3 {
		t.Fatalf("groupes = %v, attendu un groupe de trois", groupes)
	}
	if !slices.IsSorted(groupes[0]) {
		t.Errorf("le groupe n'est pas trié, la sortie ne se compare pas : %v", groupes[0])
	}
}

// La sortie est déterministe : un diagnostic qui change d'ordre d'un appel à
// l'autre ne se compare pas d'un poste à l'autre.
func TestFantomesDeCasseEstDeterministe(t *testing.T) {
	files := map[string]string{
		"e/b.md": "1", "e/B.md": "2",
		"e/a.md": "3", "e/A.md": "4",
		"e/c.md": "5", "e/C.md": "6",
	}
	premier := FantomesDeCasse(files)
	for i := 0; i < 20; i++ {
		if got := FantomesDeCasse(files); !slices.EqualFunc(got, premier, slices.Equal) {
			t.Fatalf("appel %d : %v, attendu %v", i, got, premier)
		}
	}
}

// L'état d'un poste réel, après un cycle : rien à signaler. Le fantôme ne naît
// plus, c'est tout l'objet de la slice 2.
func TestFantomesDeCasseAucunApresUnCycleSain(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, _, _ := moteurBavard(t, url, token)

	writeFile(t, dirA, "equipe/agents.md", "# Registre\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.Put("equipe/AGENTS.md", "# Contexte\n", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatal(err)
	}

	if groupes := FantomesDeCasse(b.State().Files); len(groupes) != 0 {
		t.Errorf("le refus aurait dû empêcher toute collision dans l'état : %v", groupes)
	}
}
