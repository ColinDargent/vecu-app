package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatalf("Init : %v", err)
	}
	return s
}

func TestInitIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "brain.git")
	s1, err := Init(dir)
	if err != nil {
		t.Fatalf("Init 1 : %v", err)
	}
	h1, _ := s1.Head()
	s2, err := Init(dir)
	if err != nil {
		t.Fatalf("Init 2 : %v", err)
	}
	h2, _ := s2.Head()
	if h1 != h2 {
		t.Errorf("réouverture a changé main : %s != %s", h1, h2)
	}
}

func TestWriteRead(t *testing.T) {
	s := newStore(t)
	content := "# Décisions\n\n- Vécu : ça part.\n"
	if _, err := s.Write("projets/vecu/décisions.md", content, "colin"); err != nil {
		t.Fatalf("Write : %v", err)
	}
	got, err := s.Read("projets/vecu/décisions.md", "")
	if err != nil {
		t.Fatalf("Read : %v", err)
	}
	if got != content {
		t.Errorf("contenu altéré :\n%q\n!=\n%q", got, content)
	}
}

func TestReadPreservesExactBytes(t *testing.T) {
	s := newStore(t)
	// Sans \n final + \n multiples : le contenu doit revenir à l'octet près.
	for _, content := range []string{"sans newline final", "double\n\n", ""} {
		if _, err := s.Write("f.md", content, "colin"); err != nil {
			t.Fatalf("Write %q : %v", content, err)
		}
		got, err := s.Read("f.md", "")
		if err != nil {
			t.Fatalf("Read %q : %v", content, err)
		}
		if got != content {
			t.Errorf("altéré : %q != %q", got, content)
		}
	}
}

func TestList(t *testing.T) {
	s := newStore(t)
	paths := []string{"a.md", "dossier/b.md", "dossier/sous/c.md"}
	for _, p := range paths {
		if _, err := s.Write(p, "x", "colin"); err != nil {
			t.Fatalf("Write %s : %v", p, err)
		}
	}
	got, err := s.List("")
	if err != nil {
		t.Fatalf("List : %v", err)
	}
	if len(got) != len(paths) {
		t.Fatalf("attendu %d chemins, obtenu %v", len(paths), got)
	}
	for i, p := range paths {
		if got[i] != p {
			t.Errorf("chemin %d : %s != %s", i, got[i], p)
		}
	}
}

func TestLog(t *testing.T) {
	s := newStore(t)
	s.Write("note.md", "v1", "colin")
	s.Write("note.md", "v2", "achille")
	s.Write("autre.md", "x", "colin")

	log, err := s.Log("note.md")
	if err != nil {
		t.Fatalf("Log : %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("attendu 2 commits, obtenu %d : %+v", len(log), log)
	}
	if log[0].Author != "achille" || log[1].Author != "colin" {
		t.Errorf("auteurs inattendus : %+v", log)
	}
	if log[0].Message != "update note.md" {
		t.Errorf("message inattendu : %q", log[0].Message)
	}
	if !strings.Contains(log[0].Date, "T") {
		t.Errorf("date non ISO 8601 : %q", log[0].Date)
	}
}

func TestReadOldRevision(t *testing.T) {
	s := newStore(t)
	oid1, _ := s.Write("note.md", "v1", "colin")
	s.Write("note.md", "v2", "colin")

	got, err := s.Read("note.md", oid1)
	if err != nil {
		t.Fatalf("Read à l'ancienne révision : %v", err)
	}
	if got != "v1" {
		t.Errorf("attendu v1, obtenu %q", got)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	s.Write("a.md", "x", "colin")
	s.Write("b.md", "y", "colin")

	if _, err := s.Delete("a.md", "colin"); err != nil {
		t.Fatalf("Delete : %v", err)
	}
	if _, err := s.Read("a.md", ""); err == nil {
		t.Error("a.md lisible après suppression")
	}
	// Le voisin survit, l'historique reste lisible.
	if _, err := s.Read("b.md", ""); err != nil {
		t.Errorf("b.md perdu par la suppression de a.md : %v", err)
	}
	log, err := s.Log("a.md")
	if err != nil || len(log) != 2 {
		t.Errorf("historique de a.md attendu (2 commits), obtenu %v, %v", log, err)
	}
}

func TestDeleteUnknown(t *testing.T) {
	s := newStore(t)
	if _, err := s.Delete("fantome.md", "colin"); err == nil {
		t.Error("Delete d'un fichier inexistant aurait dû échouer")
	}
}

func TestInvalidPaths(t *testing.T) {
	s := newStore(t)
	for _, p := range []string{
		"", "/abs.md", "../hors.md", "a/../../hors.md", "a//b.md", "a/./b.md", ".",
		"a\nb.md", "a\x1fb.md", "a\tb.md", "a\x00b.md",
	} {
		if _, err := s.Write(p, "x", "colin"); err == nil {
			t.Errorf("Write(%q) aurait dû être rejeté", p)
		}
	}
}

func TestInvalidAuthorRejected(t *testing.T) {
	s := newStore(t)
	// Un \x1f dans l'auteur décalerait le parsing de Log : refusé en amont.
	if _, err := s.Write("f.md", "x", "ev\x1fil"); err == nil {
		t.Error("auteur avec caractère de contrôle aurait dû être rejeté")
	}
	if _, err := s.Write("f.md", "x", ""); err == nil {
		t.Error("auteur vide aurait dû être rejeté")
	}
}

func TestLogIntegrityUnderNormalInput(t *testing.T) {
	s := newStore(t)
	s.Write("note.md", "v1", "colin")
	log, err := s.Log("note.md")
	if err != nil {
		t.Fatalf("Log : %v", err)
	}
	// Les champs restent alignés : OID hex, auteur exact, date ISO, message.
	if !isHex(log[0].OID) || log[0].Author != "colin" || log[0].Message != "update note.md" {
		t.Errorf("champs de log désalignés : %+v", log[0])
	}
}

func TestDiff(t *testing.T) {
	s := newStore(t)
	base, _ := s.Head() // commit racine vide
	s.Write("a.md", "1", "colin")
	s.Write("b.md", "1", "colin")
	mid, _ := s.Head()
	s.Write("a.md", "2", "colin") // modif
	s.Write("c.md", "1", "colin") // ajout
	s.Delete("b.md", "colin")     // suppression

	// Depuis mid : a modifié, c ajouté, b supprimé.
	_, changes, err := s.Diff(mid)
	if err != nil {
		t.Fatalf("Diff : %v", err)
	}
	got := map[string]bool{} // path -> deleted
	for _, c := range changes {
		got[c.Path] = c.Deleted
	}
	if len(got) != 3 || got["a.md"] || got["c.md"] || !got["b.md"] {
		t.Errorf("delta inattendu : %+v", got)
	}

	// Depuis le commit racine : état complet en ajouts (a et c ; b supprimé absent).
	_, full, _ := s.Diff(base)
	names := map[string]bool{}
	for _, c := range full {
		names[c.Path] = true
		if c.Deleted {
			t.Errorf("resync depuis racine ne devrait contenir que des ajouts : %+v", c)
		}
	}
	if !names["a.md"] || !names["c.md"] || names["b.md"] {
		t.Errorf("état complet inattendu : %+v", names)
	}
}

func TestDiffNoChange(t *testing.T) {
	s := newStore(t)
	s.Write("a.md", "1", "colin")
	head, _ := s.Head()
	gotHead, changes, err := s.Diff(head)
	if err != nil {
		t.Fatalf("Diff : %v", err)
	}
	if gotHead != head {
		t.Errorf("head renvoyé %s != %s", gotHead, head)
	}
	if len(changes) != 0 {
		t.Errorf("attendu aucun changement, obtenu %+v", changes)
	}
}

func TestDiffMalformedSinceResyncs(t *testing.T) {
	s := newStore(t)
	s.Write("a.md", "1", "colin")
	// Curseurs cassés : hex absent, non-hex, trop court, ref → tous resync, jamais d'erreur.
	for _, since := range []string{
		"0123456789abcdef0123456789abcdef01234567", // hex absent
		"pas-un-oid", "0123", "refs/heads/main", "HEAD",
	} {
		_, changes, err := s.Diff(since)
		if err != nil {
			t.Fatalf("Diff(%q) : erreur au lieu de resync : %v", since, err)
		}
		if len(changes) != 1 || changes[0].Path != "a.md" || changes[0].Deleted {
			t.Errorf("Diff(%q) : resync attendu (a.md ajouté), obtenu %+v", since, changes)
		}
	}
}

func TestWriteMergeFastPath(t *testing.T) {
	s := newStore(t)
	head, _ := s.Head()
	res, err := s.WriteMerge("a.md", "contenu", "colin", head, "ignore")
	if err != nil {
		t.Fatalf("WriteMerge : %v", err)
	}
	if res.Conflict || res.Merged {
		t.Errorf("attendu écriture directe, obtenu %+v", res)
	}
	if got, _ := s.Read("a.md", ""); got != "contenu" {
		t.Errorf("contenu non écrit : %q", got)
	}
}

func TestWriteMergeCleanMerge(t *testing.T) {
	s := newStore(t)
	s.Write("doc.md", "L1\nL2\nL3\nL4\nL5\nL6\nL7\n", "colin")
	base, _ := s.Head()
	// Writer A avance main (édite L1).
	s.Write("doc.md", "L1-A\nL2\nL3\nL4\nL5\nL6\nL7\n", "colin")
	// Client B, parti de base, édite L7 (loin de L1) → fusion propre.
	res, err := s.WriteMerge("doc.md", "L1\nL2\nL3\nL4\nL5\nL6\nL7-B\n", "achille", base, "ignore")
	if err != nil {
		t.Fatalf("WriteMerge : %v", err)
	}
	if !res.Merged || res.Conflict {
		t.Fatalf("attendu fusion propre, obtenu %+v", res)
	}
	got, _ := s.Read("doc.md", "")
	if got != "L1-A\nL2\nL3\nL4\nL5\nL6\nL7-B\n" {
		t.Errorf("fusion incorrecte :\n%q", got)
	}
}

func TestWriteMergeConflictCreatesCopy(t *testing.T) {
	s := newStore(t)
	s.Write("note.md", "base\n", "colin")
	base, _ := s.Head()
	// Writer A modifie la même ligne.
	s.Write("note.md", "version-colin\n", "colin")
	headA, _ := s.Head()
	// Client B, parti de base, modifie la même ligne → conflit.
	res, err := s.WriteMerge("note.md", "version-achille\n", "achille", base,
		"note (conflit 2026-07-23 17h30 - achille).md")
	if err != nil {
		t.Fatalf("WriteMerge : %v", err)
	}
	if !res.Conflict {
		t.Fatalf("attendu conflit, obtenu %+v", res)
	}
	// main garde la version du premier écrivain.
	if got, _ := s.Read("note.md", ""); got != "version-colin\n" {
		t.Errorf("main devrait garder version-colin, obtenu %q", got)
	}
	// La copie de conflit porte la version du client.
	if got, _ := s.Read(res.ConflictPath, ""); got != "version-achille\n" {
		t.Errorf("copie de conflit incorrecte : %q", got)
	}
	// Aucune version perdue : les deux existent, head a avancé depuis A.
	if res.Head == headA {
		t.Error("le head devrait avoir avancé (copie committée)")
	}
}

func TestDeleteMergeCleanAndConflict(t *testing.T) {
	s := newStore(t)
	s.Write("keep.md", "x\n", "colin")
	s.Write("gone.md", "y\n", "colin")
	base, _ := s.Head()

	// Cas propre : main avance sur keep.md, client supprime gone.md depuis base.
	s.Write("keep.md", "x-modifié\n", "colin")
	res, err := s.DeleteMerge("gone.md", "achille", base, "gone (conflit).md")
	if err != nil {
		t.Fatalf("DeleteMerge propre : %v", err)
	}
	if !res.Merged || res.Conflict {
		t.Fatalf("attendu fusion propre, obtenu %+v", res)
	}
	if _, err := s.Read("gone.md", ""); err == nil {
		t.Error("gone.md devrait être supprimé")
	}
	if got, _ := s.Read("keep.md", ""); got != "x-modifié\n" {
		t.Errorf("modif concurrente de keep.md perdue : %q", got)
	}

	// Cas conflit : suppression d'un fichier modifié entre-temps. La version de
	// main part en COPIE et la suppression s'applique.
	//
	// C'est le contraire de ce que faisait ce test avant, et c'est délibéré :
	// abandonner la suppression laissait le fichier en place, donc git refusait
	// un dossier du même nom, donc la bascule fichier -> dossier gelait le chemin
	// indéfiniment. Et surtout, c'est la sortie du refus d'écriture - retirer
	// l'obstacle fait constater une suppression, qui doit passer sans effacer la
	// version de l'autre.
	s.Write("f.md", "orig\n", "colin")
	b2, _ := s.Head()
	s.Write("f.md", "modifié\n", "colin")
	res, err = s.DeleteMerge("f.md", "achille", b2, "f (conflit 2026-07-23 17h30 - achille).md")
	if err != nil {
		t.Fatalf("DeleteMerge conflit : %v", err)
	}
	if !res.Conflict {
		t.Fatalf("attendu conflit delete/modify, obtenu %+v", res)
	}
	if res.ConflictPath == "" {
		t.Fatal("ConflictPath vide : le client ne peut pas nommer la copie qu'on vient de créer")
	}
	if _, err := s.Read("f.md", ""); err == nil {
		t.Error("la suppression n'a pas été appliquée : le chemin gèlerait à chaque cycle")
	}
	if got, _ := s.Read(res.ConflictPath, ""); got != "modifié\n" {
		t.Errorf("la version modifiée n'est pas dans la copie : %q", got)
	}
}

// TestDeleteMergeBaseVideNAnnonceJamaisUnSuccesSansEffet : une suppression sans
// base commune ne doit pas être annoncée réussie pendant que le chemin survit.
//
// Le côté client est alors un commit orphelin sur l'arbre vide, dont retirer `p`
// laisse l'arbre vide : `merge-tree --allow-unrelated-histories` voit un côté qui
// n'apporte rien, rend l'arbre de main à l'identique, et la réponse dit
// `Merged: true`. Le client, qui ne voit plus le fichier sur son disque,
// reconstate la suppression au cycle suivant - indéfiniment, sans une trace.
func TestDeleteMergeBaseVideNAnnonceJamaisUnSuccesSansEffet(t *testing.T) {
	s := newStore(t)
	s.Write("equipe/a.md", "contenu\n", "colin")

	res, err := s.DeleteMerge("equipe/a.md", "achille", "", "equipe/a (conflit).md")
	if err != nil {
		t.Fatalf("DeleteMerge base vide : %v", err)
	}
	if _, err := s.Read("equipe/a.md", ""); err == nil {
		t.Fatalf("le chemin survit alors que la réponse annonce %+v : la suppression sera reconstatée à chaque cycle", res)
	}
	if !res.Conflict || res.ConflictPath == "" {
		t.Fatalf("sans base commune, la version de main doit partir en copie : %+v", res)
	}
	if got, _ := s.Read(res.ConflictPath, ""); got != "contenu\n" {
		t.Errorf("la version de main n'est pas dans la copie : %q", got)
	}
}

func TestConflictCopyNeverOverwrites(t *testing.T) {
	s := newStore(t)
	s.Write("note.md", "V0\n", "colin")
	base, _ := s.Head()
	name := "note (conflit 2026-07-23 17h30 - achille).md"

	// Premier conflit : main modifie, achille pousse depuis base → copie #1.
	s.Write("note.md", "V-colin-1\n", "colin")
	r1, err := s.WriteMerge("note.md", "contenu-A\n", "achille", base, name)
	if err != nil || !r1.Conflict {
		t.Fatalf("premier conflit attendu : %+v %v", r1, err)
	}

	// Deuxième conflit, MÊME nom proposé (même minute) : ne doit pas écraser #1.
	s.Write("note.md", "V-colin-2\n", "colin")
	r2, err := s.WriteMerge("note.md", "contenu-B\n", "achille", base, name)
	if err != nil || !r2.Conflict {
		t.Fatalf("deuxième conflit attendu : %+v %v", r2, err)
	}
	if r1.ConflictPath == r2.ConflictPath {
		t.Fatalf("les deux copies partagent le même chemin %q → écrasement", r1.ConflictPath)
	}
	// Les DEUX contenus survivent comme fichiers vivants.
	if got, _ := s.Read(r1.ConflictPath, ""); got != "contenu-A\n" {
		t.Errorf("copie #1 perdue : %q", got)
	}
	if got, _ := s.Read(r2.ConflictPath, ""); got != "contenu-B\n" {
		t.Errorf("copie #2 incorrecte : %q", got)
	}
}

func TestConflictCopyDoesNotClobberExistingFile(t *testing.T) {
	s := newStore(t)
	s.Write("note.md", "V0\n", "colin")
	base, _ := s.Head()
	name := "note (conflit 2026-07-23 17h30 - achille).md"
	// Un vrai fichier occupe déjà le nom de copie.
	s.Write(name, "fichier-preexistant\n", "colin")
	s.Write("note.md", "V-colin\n", "colin")

	r, err := s.WriteMerge("note.md", "contenu-client\n", "achille", base, name)
	if err != nil || !r.Conflict {
		t.Fatalf("conflit attendu : %+v %v", r, err)
	}
	if r.ConflictPath == name {
		t.Fatal("la copie a écrasé le fichier préexistant")
	}
	if got, _ := s.Read(name, ""); got != "fichier-preexistant\n" {
		t.Errorf("fichier préexistant altéré : %q", got)
	}
}

func TestWriteMergeRejectsBadBaseOID(t *testing.T) {
	s := newStore(t)
	s.Write("a.md", "x", "colin")
	if _, err := s.WriteMerge("a.md", "y", "colin", "pas-hex", "c.md"); err == nil {
		t.Error("base_oid non-hex aurait dû être rejeté")
	}
}

func TestReadRejectsNonHexRev(t *testing.T) {
	s := newStore(t)
	s.Write("f.md", "x", "colin")
	for _, rev := range []string{"refs/heads/main", "HEAD", "HEAD@{0}", ":0:f.md", "main"} {
		if _, err := s.Read("f.md", rev); err == nil {
			t.Errorf("Read avec rev=%q aurait dû être rejeté", rev)
		}
	}
}

func TestLogMarqueSuppression(t *testing.T) {
	s := newStore(t)
	s.Write("note.md", "v1", "colin")
	s.Delete("note.md", "achille")
	s.Write("note.md", "v2", "colin") // résurrection volontaire

	log, err := s.Log("note.md")
	if err != nil {
		t.Fatalf("Log : %v", err)
	}
	if len(log) != 3 {
		t.Fatalf("attendu 3 commits, obtenu %d : %+v", len(log), log)
	}
	// Du plus récent au plus ancien : réécriture, suppression, création.
	if log[0].Deleted || !log[1].Deleted || log[2].Deleted {
		t.Errorf("flags Deleted inattendus : %+v", log)
	}
}

func TestLogTraverseLesMerges(t *testing.T) {
	s := newStore(t)
	base, _ := s.Write("a.md", "v1", "colin")
	s.Write("b.md", "x", "colin") // main avance : le prochain write de a.md fusionne

	res, err := s.WriteMerge("a.md", "v2", "achille", base, "a (conflit).md")
	if err != nil || res.Conflict || !res.Merged {
		t.Fatalf("WriteMerge : %+v (err %v)", res, err)
	}

	log, err := s.Log("a.md")
	if err != nil {
		t.Fatalf("Log : %v", err)
	}
	// Le commit de merge doit apparaître (sans --first-parent, la simplification
	// d'historique suivrait le parent éphémère et le perdrait).
	if len(log) != 2 {
		t.Fatalf("attendu 2 commits (merge + création), obtenu %d : %+v", len(log), log)
	}
	if log[0].Author != "achille" || log[1].Author != "colin" {
		t.Errorf("auteurs inattendus : %+v", log)
	}
}

func TestLogRefuseLesRepertoires(t *testing.T) {
	s := newStore(t)
	s.Write("clients/vdf/secret.md", "x", "colin")

	// Log est un historique de fichier : un répertoire n'a pas d'historique
	// propre (sinon /log fuiterait les chemins des enfants hors périmètre).
	log, err := s.Log("clients")
	if err != nil {
		t.Fatalf("Log : %v", err)
	}
	if len(log) != 0 {
		t.Errorf("log d'un répertoire : attendu vide, obtenu %+v", log)
	}
}

func TestWriteWithMessage(t *testing.T) {
	s := newStore(t)
	s.Write("a.md", "v1", "colin")
	if _, err := s.WriteWithMessage("a.md", "v0", "colin", "restore a.md @ abc123"); err != nil {
		t.Fatalf("WriteWithMessage : %v", err)
	}
	log, _ := s.Log("a.md")
	if len(log) != 2 || log[0].Message != "restore a.md @ abc123" {
		t.Errorf("message de restore absent du log : %+v", log)
	}
	if _, err := s.WriteWithMessage("a.md", "x", "colin", "mauvais\x00message"); err == nil {
		t.Error("message avec caractère de contrôle accepté")
	}
}

func TestCheminsAccentues(t *testing.T) {
	// Cas nominal d'un vault français : sans core.quotePath=off, git cite les
	// chemins non-ASCII (« "notes/id\303\251es.md" ») et casse List/Diff/Log.
	s := newStore(t)
	base, _ := s.Write("notes/idées.md", "v1", "colin")
	s.Write("réunions/été.md", "x", "colin")

	list, err := s.List("")
	if err != nil {
		t.Fatalf("List : %v", err)
	}
	attendu := map[string]bool{"notes/idées.md": true, "réunions/été.md": true}
	for _, p := range list {
		if !attendu[p] {
			t.Errorf("List : chemin inattendu (cité ?) : %q", p)
		}
	}

	log, err := s.Log("notes/idées.md")
	if err != nil || len(log) != 1 {
		t.Fatalf("Log accentué : attendu 1 commit, obtenu %d (err %v)", len(log), err)
	}

	_, changes, err := s.Diff(base)
	if err != nil {
		t.Fatalf("Diff : %v", err)
	}
	if len(changes) != 1 || changes[0].Path != "réunions/été.md" {
		t.Errorf("Diff accentué : %+v", changes)
	}
}

// TestDeleteMergeRepeteNeCommitteRien : un DELETE répété sur un chemin déjà
// absent ne doit produire aucun commit.
//
// Le no-op idempotent en tête de DeleteMerge ne couvre que `baseOID == head`.
// Depuis qu'un marque-page périmé amène à la fusion, chaque appel produisait un
// commit de merge ET son parent éphémère - définitivement enracinés, donc jamais
// collectés. Déclencheur réel : un cycle dont le pull n'aboutit pas renvoie
// exactement le même DELETE au cycle suivant, toutes les 15 secondes.
func TestDeleteMergeRepeteNeCommitteRien(t *testing.T) {
	s := newStore(t)
	s.Write("equipe/a.md", "contenu\n", "colin")
	base, _ := s.Head()
	s.Write("equipe/b.md", "autre\n", "colin") // le head avance : plus de chemin rapide

	if _, err := s.DeleteMerge("equipe/a.md", "achille", base, "equipe/a (conflit).md"); err != nil {
		t.Fatal(err)
	}
	apres, _ := s.Head()

	for i := range 5 {
		res, err := s.DeleteMerge("equipe/a.md", "achille", base, "equipe/a (conflit).md")
		if err != nil {
			t.Fatalf("appel n°%d : %v", i+2, err)
		}
		if res.Head != apres {
			t.Fatalf("appel n°%d : le head a bougé (%s -> %s) sur une suppression sans effet",
				i+2, apres[:12], res.Head[:12])
		}
	}
	head, _ := s.Head()
	if head != apres {
		t.Errorf("5 suppressions répétées ont fait avancer le dépôt : %s -> %s", apres[:12], head[:12])
	}
}
