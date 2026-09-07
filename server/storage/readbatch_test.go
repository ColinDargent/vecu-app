package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// ReadBatch doit rendre EXACTEMENT ce que Read rend, fichier par fichier. Un
// découpage de flux qui dérive d'un octet corromprait silencieusement des
// contenus - le pire mode d'échec pour un outil de lecture.
func TestReadBatchRendLaMemeChoseQueRead(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}

	// Des contenus qui exercent le découpage : vide, sans saut final, avec des
	// sauts, avec de l'UTF-8 multi-octets (où taille en octets != en runes), et
	// un contenu qui RESSEMBLE à un en-tête de --batch.
	cas := map[string]string{
		"a/vide.md":       "",
		"a/sans-saut.md":  "pas de saut final",
		"a/multi.md":      "ligne 1\nligne 2\n\nligne 4\n",
		"a/accents.md":    "éèêàùç — le vault est en français, où les runes pèsent 2 octets\n",
		"a/piege.md":      "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef blob 42\ncontenu\n",
		"b/imbrique/x.md": "autre dossier\n",
	}
	for p, c := range cas {
		if _, err := s.Write(p, c, "colin"); err != nil {
			t.Fatal(err)
		}
	}

	chemins := make([]string, 0, len(cas))
	for p := range cas {
		chemins = append(chemins, p)
	}
	// Un chemin absent : il doit simplement manquer de la map, sans erreur.
	chemins = append(chemins, "a/nexiste-pas.md")

	lot, err := s.ReadBatch(chemins, "")
	if err != nil {
		t.Fatalf("ReadBatch : %v", err)
	}
	if _, present := lot["a/nexiste-pas.md"]; present {
		t.Error("un chemin absent est présent dans le lot")
	}
	for p, attendu := range cas {
		seul, err := s.Read(p, "")
		if err != nil {
			t.Fatalf("Read(%q) : %v", p, err)
		}
		if seul != attendu {
			t.Fatalf("précondition : Read(%q) = %q", p, seul)
		}
		if lot[p] != attendu {
			t.Errorf("ReadBatch(%q) = %q, Read = %q", p, lot[p], attendu)
		}
	}
}

func TestReadBatchSurUneListeVide(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	lot, err := s.ReadBatch(nil, "")
	if err != nil || len(lot) != 0 {
		t.Errorf("liste vide : lot=%v err=%v", lot, err)
	}
}

// Un chemin refusé par validatePath n'est pas demandé à git, et ne fait pas
// échouer le lot entier : les autres chemins doivent revenir.
func TestReadBatchIgnoreUnCheminRefuseSansCasserLeLot(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	s.Write("a/bon.md", "bon\n", "colin")

	lot, err := s.ReadBatch([]string{"a/bon.md", "../evasion.md", "a/../../ailleurs.md"}, "")
	if err != nil {
		t.Fatalf("ReadBatch : %v", err)
	}
	if lot["a/bon.md"] != "bon\n" {
		t.Errorf("le chemin valide n'est pas revenu : %q", lot["a/bon.md"])
	}
	for p := range lot {
		if strings.Contains(p, "..") {
			t.Errorf("un chemin d'évasion est dans le lot : %q", p)
		}
	}
}

// Un chemin ABSENT qui contient un espace ne doit pas tronquer le lot.
//
// git réécrit la requête suivie de « missing » pour un objet absent, et une
// requête contient un chemin : « main:a/mon fichier.md missing » rendait trois
// champs à `strings.Fields`, donc un en-tête pris pour valide, donc un arrêt de
// la boucle. Tout ce qui suivait disparaissait en silence.
//
// Atteignable par la course naturelle : `List` à T1, `ReadBatch` à T2, une
// suppression entre les deux.
func TestReadBatchNeTronquePasSurUnCheminAbsentAvecEspace(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a/un.md", "a/deux.md", "a/trois.md"} {
		s.Write(p, "contenu de "+p+"\n", "colin")
	}

	// L'absent est AU MILIEU, et il porte un espace : les deux conditions du
	// défaut. Sans elles, le bug ne se voit pas.
	lot, err := s.ReadBatch([]string{"a/un.md", "a/fichier absent.md", "a/deux.md", "a/trois.md"}, "")
	if err != nil {
		t.Fatalf("ReadBatch : %v", err)
	}
	for _, p := range []string{"a/un.md", "a/deux.md", "a/trois.md"} {
		if lot[p] != "contenu de "+p+"\n" {
			t.Errorf("%q manque ou est faux : %q (le lot a été tronqué)", p, lot[p])
		}
	}
	if _, present := lot["a/fichier absent.md"]; present {
		t.Error("le chemin absent est présent dans le lot")
	}
}

// Un chemin de DOSSIER ne rend rien. Sans contrôle du type, `cat-file --batch`
// rendrait l'objet tree brut, donc les noms des enfants du dossier - une
// primitive de fuite d'existence dans une API de stockage partagée.
func TestReadBatchNeRendJamaisUnObjetTree(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	s.Write("prive/tres-secret.md", "contenu\n", "colin")

	lot, err := s.ReadBatch([]string{"prive", "prive/tres-secret.md"}, "")
	if err != nil {
		t.Fatalf("ReadBatch : %v", err)
	}
	if contenu, present := lot["prive"]; present {
		t.Errorf("un dossier a rendu un contenu : %q", contenu)
	}
	if lot["prive/tres-secret.md"] != "contenu\n" {
		t.Errorf("le blob voisin n'est pas revenu : %q", lot["prive/tres-secret.md"])
	}
}
