package espaces

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/storage"
)

// TestNomTechniqueDeriveDUnLibelleHumain : DT3. Quelqu'un qui partage
// `~/Documents/Clients 2026` ne va pas renommer son dossier pour faire plaisir
// à l'outil - c'est l'outil qui dérive un nom technique et garde le libellé.
func TestNomTechniqueDeriveDUnLibelleHumain(t *testing.T) {
	cas := []struct{ libelle, attendu string }{
		{"Clients 2026", "clients-2026"},
		{"Dossier Été", "dossier-ete"},
		{"Notes", "notes"},
		{"Compta / Factures", "compta-factures"},
		{"  espaces   multiples  ", "espaces-multiples"},
		{"Cœur & Âme", "coeur-ame"},
		{"déjà-vu", "deja-vu"},
		{"Ça marche ?", "ca-marche"},
		// Les bords : ValidNom refuse un nom qui commence par « . » ou « - » et
		// un nom qui finit par « . ».
		{"...ébauche...", "ebauche"},
		{"-rf", "rf"},
		{"mon.dossier.", "mon.dossier"},
	}
	for _, c := range cas {
		got := NomTechnique(c.libelle)
		if got != c.attendu {
			t.Errorf("NomTechnique(%q) = %q, attendu %q", c.libelle, got, c.attendu)
		}
		if err := ValidNom(got); err != nil {
			t.Errorf("NomTechnique(%q) = %q, que ValidNom refuse : %v", c.libelle, got, err)
		}
	}
}

// TestNomTechniqueNeRendJamaisUnNomInvalide : la propriété qui compte. Un
// libellé fait d'emojis, d'idéogrammes ou de ponctuation ne doit pas produire un
// nom que le stockage refusera - et surtout pas obliger l'humain à trouver
// lui-même un nom ASCII, ce qui est exactement le geste que DT3 supprime.
func TestNomTechniqueNeRendJamaisUnNomInvalide(t *testing.T) {
	for _, libelle := range []string{
		"🎉🎉🎉", "???", "...", "---", "", "   ", "日本語", "/", ".", "..",
		strings.Repeat("très long ", 40),
		"Mon Application.app", // suffixe réservé par macOS
	} {
		nom := NomTechnique(libelle)
		if nom == "" {
			t.Errorf("NomTechnique(%q) rend une chaîne vide", libelle)
			continue
		}
		if err := ValidNom(nom); err != nil {
			t.Errorf("NomTechnique(%q) = %q, refusé par ValidNom : %v", libelle, nom, err)
		}
	}
}

// TestCollisionDesambiguisee : deux personnes qui partagent chacune un dossier
// `Notes`. Le CDC est explicite - le serveur désambiguïse et le DIT, « plutôt
// que de fusionner deux dossiers sans rapport ». Une fusion ferait apparaître le
// contenu de l'un chez l'autre, avec les droits de l'autre.
func TestCollisionDesambiguisee(t *testing.T) {
	st := magasin(t)

	premier, err := CreateDepuisLibelle(st, "Notes", "colin")
	if err != nil {
		t.Fatal(err)
	}
	if premier != "notes" {
		t.Fatalf("premier = %q", premier)
	}
	second, err := CreateDepuisLibelle(st, "Notes", "achille")
	if err != nil {
		t.Fatal(err)
	}
	if second == premier {
		t.Fatal("deux espaces sans rapport ont fusionné sous le même nom technique")
	}
	if second != "notes-2" {
		t.Errorf("second = %q, attendu \"notes-2\"", second)
	}
	troisieme, err := CreateDepuisLibelle(st, "notes !", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if troisieme != "notes-3" {
		t.Errorf("troisième = %q", troisieme)
	}

	// Et les trois gardent chacun LEUR libellé, qui est ce que l'humain lit.
	if got := LitLibelle(st, second); got != "Notes" {
		t.Errorf("libellé du second = %q, attendu \"Notes\"", got)
	}
	if got := LitLibelle(st, troisieme); got != "notes !" {
		t.Errorf("libellé du troisième = %q", got)
	}
}

// TestUnNomTropLongSeDesambiguiseQuandMeme : la troncature doit mordre sur la
// BASE, jamais sur le suffixe. Un « -2 » rogné retomberait sur le nom déjà pris,
// et la boucle tournerait jusqu'à 999 sans jamais trouver.
func TestUnNomTropLongSeDesambiguiseQuandMeme(t *testing.T) {
	st := magasin(t)
	long := strings.Repeat("a", maxNom+20)

	premier, err := CreateDepuisLibelle(st, long, "colin")
	if err != nil {
		t.Fatal(err)
	}
	if len(premier) > maxNom {
		t.Fatalf("premier trop long : %d", len(premier))
	}
	second, err := CreateDepuisLibelle(st, long, "achille")
	if err != nil {
		t.Fatal(err)
	}
	if second == premier {
		t.Fatal("collision non résolue sur un nom tronqué")
	}
	if len(second) > maxNom {
		t.Errorf("second trop long : %d (%q)", len(second), second)
	}
	if err := ValidNom(second); err != nil {
		t.Errorf("second invalide : %v", err)
	}
}

// TestLibelleAbimeNeCassePasLaListe : un fichier de métadonnées absent, vide ou
// mal formé rend "" sans erreur. Une liste d'espaces qui refuserait de
// s'afficher parce que l'un d'eux a un fichier abîmé serait pire que l'absence
// de libellé.
func TestLibelleAbimeNeCassePasLaListe(t *testing.T) {
	st := magasin(t)
	if err := Create(st, "sanslibelle", "colin"); err != nil {
		t.Fatal(err)
	}
	if got := LitLibelle(st, "sanslibelle"); got != "" {
		t.Errorf("libellé inventé : %q", got)
	}
	if _, err := st.WriteWithMessage("casse/"+FichierMeta, "{ceci n'est pas du json", "colin", "test"); err != nil {
		t.Fatal(err)
	}
	if got := LitLibelle(st, "casse"); got != "" {
		t.Errorf("libellé lu depuis un fichier mal formé : %q", got)
	}
}

// TestLeFichierDeMetaSeSynchronise : le libellé arrive chez les autres parce que
// son fichier voyage comme le reste. S'il tombait dans les motifs ignorés, il ne
// partirait jamais - et personne ne verrait rien, sans qu'aucune erreur ne le
// dise.
//
// La règle d'ignore vit côté client et compare des SEGMENTS entiers (« .vecu »,
// « .git », « .DS_Store »). Ce test fige la propriété qui compte ici : le nom du
// fichier n'est aucun de ces segments. Le jour où quelqu'un passera la règle en
// comparaison de PRÉFIXE, « .vecu-espace.json » commencerait par « .vecu » et le
// libellé cesserait silencieusement de se synchroniser.
func TestLeFichierDeMetaSeSynchronise(t *testing.T) {
	for _, ignore := range []string{".vecu", ".git", ".DS_Store"} {
		if FichierMeta == ignore {
			t.Fatalf("le fichier de métadonnées porte un nom ignoré par la sync : %q", FichierMeta)
		}
	}
	if !strings.HasSuffix(FichierMeta, ".json") {
		t.Errorf("le fichier de métadonnées devrait rester un .json lisible : %q", FichierMeta)
	}
}

func magasin(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Init(filepath.Join(t.TempDir(), "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}
