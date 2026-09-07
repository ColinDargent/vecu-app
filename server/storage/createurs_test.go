package storage

import (
	"fmt"
	"path/filepath"
	"testing"
)

// LA GARDE DE LA MESURE. Sans elle, un refactor qui repasse au `git log` par
// fichier ne casse AUCUNE assertion de résultat - la carte reste juste, elle
// coûte seulement 260 fois plus cher. Ce que le test fige n'est donc pas un
// nombre absolu mais une INVARIANCE : le coût d'un appel froid ne dépend pas du
// nombre de fichiers du dépôt.
func TestCreateursUneSeulePasseQuelQueSoitLeNombreDeFichiers(t *testing.T) {
	compte := func(nbFichiers int) int64 {
		t.Helper()
		s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
		if err != nil {
			t.Fatal(err)
		}
		for i := range nbFichiers {
			if _, err := s.Write(fmt.Sprintf("notes/f%d.md", i), "x", "colin"); err != nil {
				t.Fatal(err)
			}
		}
		s.appelsGit.Store(0)
		carte, err := s.Createurs()
		if err != nil {
			t.Fatalf("Createurs : %v", err)
		}
		if len(carte) != nbFichiers {
			t.Fatalf("précondition : %d chemins dans la carte, %d attendus", len(carte), nbFichiers)
		}
		return s.appelsGit.Load()
	}

	petit, gros := compte(3), compte(30)
	if petit != gros {
		t.Errorf("le coût suit le nombre de fichiers : %d invocations pour 3 fichiers, %d pour 30",
			petit, gros)
	}
	// Le nombre lui-même est épinglé : `rev-parse` pour le head (la clé du
	// cache) puis `log` pour la passe. Un troisième process est un parcours de
	// trop, et c'est exactement ce qu'on refuse.
	if petit != 2 {
		t.Errorf("appel froid : %d invocations git, 2 attendues (rev-parse + log)", petit)
	}
}

func TestCreateursCacheInvalideParLeHead(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/a.md", "x", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Createurs(); err != nil {
		t.Fatal(err)
	}

	// Head inchangé : le cache sert, donc plus de parcours - seul le rev-parse
	// qui lit la clé subsiste.
	s.appelsGit.Store(0)
	if _, err := s.Createurs(); err != nil {
		t.Fatal(err)
	}
	if n := s.appelsGit.Load(); n != 1 {
		t.Errorf("appel à chaud : %d invocations git, 1 attendue (rev-parse seul)", n)
	}

	// Une écriture bouge le head : la carte doit voir le nouveau chemin.
	if _, err := s.Write("notes/b.md", "y", "achille"); err != nil {
		t.Fatal(err)
	}
	carte, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if carte["notes/b.md"].Auteur != "achille" {
		t.Errorf("après écriture : createur de notes/b.md = %q, « achille » attendu (cache périmé ?)",
			carte["notes/b.md"].Auteur)
	}
}

func TestCreateurEstLAuteurDeLaPremiereVersion(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/décisions.md", "v1", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/décisions.md", "v2", "achille"); err != nil {
		t.Fatal(err)
	}
	carte, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if got := carte["notes/décisions.md"].Auteur; got != "colin" {
		t.Errorf("créé par colin, modifié par achille : créateur = %q, « colin » attendu", got)
	}
}

// Le chemin VIVANT est celui qu'on décrit, pas son homonyme mort : après une
// suppression, la recréation repart de zéro.
func TestCreateurApresSuppressionPuisRecreation(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/a.md", "v1", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete("notes/a.md", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/a.md", "v2", "achille"); err != nil {
		t.Fatal(err)
	}
	carte, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if got := carte["notes/a.md"].Auteur; got != "achille" {
		t.Errorf("supprimé puis recréé : créateur = %q, « achille » attendu", got)
	}
}

// Un chemin supprimé et jamais recréé ne figure pas dans la carte : elle décrit
// ce qui existe, et un mort qui traîne ferait afficher un créateur pour un
// fichier absent de tous les écrans.
func TestCreateursIgnoreLesCheminsSupprimes(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/mort.md", "x", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete("notes/mort.md", "colin"); err != nil {
		t.Fatal(err)
	}
	carte, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if c, present := carte["notes/mort.md"]; present {
		t.Errorf("chemin supprimé encore dans la carte, attribué à %q", c.Auteur)
	}
}

// L'ÉCRITURE FUSIONNÉE, qui est le cas NOMINAL en production : un poste écrit
// depuis une base en retard, le serveur matérialise son édition en commit
// éphémère puis fusionne. Deux pièges y vivent, et aucun ne se voit sur un
// dépôt linéaire.
func TestCreateurSurEcritureFusionnee(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/a.md", "a", "colin"); err != nil {
		t.Fatal(err)
	}
	base, err := s.Head()
	if err != nil {
		t.Fatal(err)
	}
	// main avance pendant que le poste d'achille est resté sur `base`.
	if _, err := s.Write("notes/b.md", "b", "colin"); err != nil {
		t.Fatal(err)
	}

	// Piège 1 : le fichier neuf d'achille arrive PAR une fusion. Le commit de
	// merge ne montre aucun diff ; c'est le commit éphémère qui porte l'ajout,
	// et c'est bien achille qui doit être crédité.
	res, err := s.WriteMerge("notes/c.md", "c", "achille", base, "conflit.md")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Merged {
		t.Fatalf("précondition : la fusion n'a pas eu lieu (%+v)", res)
	}

	// Piège 2 : achille écrit `notes/b.md` depuis la MÊME base périmée, avec le
	// contenu que main y porte déjà. Son commit éphémère part d'un tree où
	// `notes/b.md` n'existe pas, donc git y voit un AJOUT ; les deux côtés
	// portant le même blob, la fusion passe et cet ajout entre dans
	// l'historique de main. Le fichier, lui, n'a jamais cessé d'exister : le
	// créateur reste colin.
	//
	// Le contenu identique n'est pas un artifice de test, c'est la seule forme
	// que ce cas peut prendre : un ajout des deux côtés avec des contenus
	// différents part en copie de conflit, et son commit éphémère reste alors
	// hors de main.
	res2, err := s.WriteMerge("notes/b.md", "b", "achille", base, "conflit2.md")
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Merged || res2.Conflict {
		t.Fatalf("précondition : l'ajout identique devait fusionner proprement (%+v)", res2)
	}

	carte, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if got := carte["notes/c.md"].Auteur; got != "achille" {
		t.Errorf("fichier créé par une écriture fusionnée : créateur = %q, « achille » attendu", got)
	}
	if got := carte["notes/b.md"].Auteur; got != "colin" {
		t.Errorf("fichier de colin réécrit depuis une base périmée : créateur = %q, « colin » attendu", got)
	}
}

// Un chemin que `validatePath` accepte mais que git CITE dans sa sortie texte :
// guillemet, antislash, et de l'UTF-8 pour faire bonne mesure. Sans `-z`, la
// carte porte la forme citée - une clé que personne ne cherchera jamais - et
// rend "" pour le vrai chemin, sans la moindre erreur.
func TestCreateursCheminCiteParGit(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	chemin := `notes/réunion "kickoff" \ jeudi.md`
	if err := ValidPath(chemin); err != nil {
		t.Fatalf("précondition : le produit accepte ce chemin, or %v", err)
	}
	if _, err := s.Write(chemin, "x", "colin"); err != nil {
		t.Fatal(err)
	}
	carte, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if got := carte[chemin].Auteur; got != "colin" {
		t.Errorf("créateur de %q = %q, « colin » attendu ; clés de la carte : %v",
			chemin, got, carte)
	}
	// La carte doit s'indexer comme `List`, sinon aucun écran ne peut joindre
	// les deux.
	chemins, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range chemins {
		if _, present := carte[p]; !present {
			t.Errorf("chemin listé absent de la carte des créateurs : %q", p)
		}
	}
	if len(carte) != len(chemins) {
		t.Errorf("la carte porte %d clés pour %d chemins listés : %v", len(carte), len(chemins), carte)
	}
}

// UN COMMIT QUI DEPLACE UN CHEMIN. La détection de renommage de git est active
// par défaut : sans `--no-renames`, ce commit sort en `R`, que `--diff-filter=AD`
// écarte - donc aucun événement, le chemin d'arrivée sans créateur et celui de
// départ vivant à tort.
//
// Aucune porte du produit ne fabrique un tel commit aujourd'hui (`commitIndex`
// ne touche qu'un chemin), donc le test le fabrique en plomberie. C'est
// exactement pourquoi il existe : le jour où une porte le fera, rien d'autre
// ne le signalera.
func TestCreateursCommitQuiDeplaceUnChemin(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/avant.md", "un contenu assez long pour ressembler à lui-même\n", "colin"); err != nil {
		t.Fatal(err)
	}
	// Suppression et ajout DANS LE MEME COMMIT, ce que l'API publique ne sait
	// pas faire. On passe par `commitIndex`, la porte d'écriture du paquet :
	// le commit fabriqué est donc de la même forme que ceux du produit, index
	// temporaire compris.
	blob, err := s.gitStdin("un contenu assez long pour ressembler à lui-même\n", nil,
		"hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.commitIndex("achille", "deplace notes/avant.md", func(env []string) error {
		if _, err := s.gitStdin("0 0000000000000000000000000000000000000000\tnotes/avant.md\n",
			env, "update-index", "--index-info"); err != nil {
			return err
		}
		_, err := s.git(env, "update-index", "--add", "--cacheinfo", "100644,"+blob+",notes/apres.md")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	carte, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if got := carte["notes/apres.md"].Auteur; got != "achille" {
		t.Errorf("chemin d'arrivée : créateur = %q, « achille » attendu", got)
	}
	if c, present := carte["notes/avant.md"]; present {
		t.Errorf("chemin de départ encore dans la carte, attribué à %q", c.Auteur)
	}
}

// LE CONTRAT DIT QUE L'APPELANT FILTRE, donc l'appelant mute ce qu'on lui rend.
// Rendre la map du cache lui ferait effacer des entrées pour tous les lecteurs
// suivants - et sous concurrence, tuer le process.
func TestCreateursRendUneCopie(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/a.md", "x", "colin"); err != nil {
		t.Fatal(err)
	}
	premiere, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	// Ce que fera le filtrage par l'ACL, un écran plus loin.
	delete(premiere, "notes/a.md")
	premiere["notes/intrus.md"] = Createur{Auteur: "quelqu'un"}

	seconde, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if got := seconde["notes/a.md"].Auteur; got != "colin" {
		t.Errorf("après filtrage par un appelant : créateur = %q, « colin » attendu", got)
	}
	if _, present := seconde["notes/intrus.md"]; present {
		t.Error("un ajout de l'appelant a atteint le cache")
	}
}

// LE RANG ORDONNE LES CREATIONS, et c'est de lui que dépend le créateur d'un
// dossier : un dossier n'existe que parce qu'un fichier y vit, donc c'est le
// fichier le plus ancien qui répond. Une recréation reprend un rang neuf - le
// chemin vivant est un chemin neuf.
func TestCreateurRangSuitLOrdreDesCreations(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"notes/premier.md", "notes/second.md", "notes/tiers.md"} {
		if _, err := s.Write(p, "x", "colin"); err != nil {
			t.Fatal(err)
		}
	}
	carte, err := s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if a, b := carte["notes/premier.md"].Rang, carte["notes/second.md"].Rang; a >= b {
		t.Errorf("premier.md rang %d, second.md rang %d : l'ordre des créations n'est pas rendu", a, b)
	}
	if a, b := carte["notes/second.md"].Rang, carte["notes/tiers.md"].Rang; a >= b {
		t.Errorf("second.md rang %d, tiers.md rang %d : l'ordre des créations n'est pas rendu", a, b)
	}

	// Recréation : le chemin repart derrière tout le monde.
	if _, err := s.Delete("notes/premier.md", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/premier.md", "y", "achille"); err != nil {
		t.Fatal(err)
	}
	carte, err = s.Createurs()
	if err != nil {
		t.Fatal(err)
	}
	if a, b := carte["notes/premier.md"].Rang, carte["notes/tiers.md"].Rang; a <= b {
		t.Errorf("recréé après tiers.md : rang %d contre %d, il devrait être le plus récent", a, b)
	}
}

// CreateursDe NE REND QUE CE QU'ON LUI A NOMME. C'est la frontière de droits :
// un écran qui ne peut obtenir que les chemins qu'il a demandés ne peut pas
// fuiter le créateur d'un chemin caché par distraction.
func TestCreateursDeNeRendQueLePerimetreDemande(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/ouvert.md", "x", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("notes/caché.md", "y", "achille"); err != nil {
		t.Fatal(err)
	}
	carte, err := s.CreateursDe([]string{"notes/ouvert.md", "notes/absent.md"})
	if err != nil {
		t.Fatal(err)
	}
	if got := carte["notes/ouvert.md"].Auteur; got != "colin" {
		t.Errorf("chemin demandé : créateur = %q, « colin » attendu", got)
	}
	if c, present := carte["notes/caché.md"]; present {
		t.Errorf("chemin NON demandé présent dans la carte, attribué à %q", c.Auteur)
	}
	// Un chemin demandé mais inconnu manque simplement, sans erreur : c'est le
	// cas d'un dépôt importé hors du produit, ou d'un chemin qui vient
	// d'apparaître.
	if _, present := carte["notes/absent.md"]; present {
		t.Error("un chemin inconnu de l'historique ne devrait pas figurer dans la carte")
	}
	if len(carte) != 1 {
		t.Errorf("la carte porte %d clés, 1 attendue : %v", len(carte), carte)
	}
}
