package fichiers

import "testing"

// posteSain : un poste qui a tout reçu, sans aucune exception.
func posteSain(libelle string, serveur map[string]string) Poste {
	emp := map[string]string{}
	for c, h := range serveur {
		emp[c] = h
	}
	return Poste{Libelle: libelle, AParle: true, RecuLe: "2026-09-01T12:00:00Z", Empreintes: emp}
}

var serveurType = map[string]string{
	"shared/note.md":  "aaa",
	"shared/tasks.md": "bbb",
}

// TestPosteMuetNeRendJamaisAJour : LA garde du lot.
//
// Mutation de contrôle : retirer le `if !p.AParle` en tête d'`EtatDuChemin`.
// Le poste muet a une carte d'empreintes VIDE, donc il tomberait sur
// « pas arrivé » - une case rouge plausible, à côté d'un poste sain en vert.
// Le mainteneur lirait « il manque deux fichiers sur le fixe » là où la vérité
// est « le fixe ne m'a rien dit depuis trois semaines ».
func TestPosteMuetNeRendJamaisAJour(t *testing.T) {
	muet := Poste{Libelle: "fixe", AParle: false}
	for chemin := range serveurType {
		got := EtatDuChemin(chemin, serveurType, muet)
		if got.Etat != SansNouvelles {
			t.Errorf("%s sur un poste muet = %q, attendu %q", chemin, got.Etat, SansNouvelles)
		}
	}
}

// TestExceptionBatLArbre : la deuxième garde, et la moins intuitive.
//
// Mutation de contrôle : déplacer les trois tests d'exception APRES la
// comparaison d'arbres. Le poste porte l'empreinte du serveur (son curseur est
// passé), donc il rendrait « à jour » sur un fichier qui n'est PAS sur son
// disque. C'est exactement le mensonge de la tête que les registres rattrapent.
func TestExceptionBatLArbre(t *testing.T) {
	p := posteSain("portable", serveurType)
	p.Exceptions.Laisses = map[string]string{"shared/note.md": "un dossier en travers du chemin"}

	got := EtatDuChemin("shared/note.md", serveurType, p)
	if got.Etat != Laisse {
		t.Fatalf("état = %q, attendu %q - la tête dit « reçu », la laisse dit « pas sur le disque », "+
			"et c'est la laisse qui a raison", got.Etat, Laisse)
	}
	if got.Motif != "un dossier en travers du chemin" {
		t.Errorf("motif = %q, il doit remonter tel quel", got.Motif)
	}
}

func TestArbreCompare(t *testing.T) {
	p := posteSain("portable", serveurType)
	p.Empreintes["shared/tasks.md"] = "vieille-empreinte"
	delete(p.Empreintes, "shared/note.md")

	if got := EtatDuChemin("shared/tasks.md", serveurType, p); got.Etat != EnRetard {
		t.Errorf("empreinte differente = %q, attendu %q", got.Etat, EnRetard)
	}
	if got := EtatDuChemin("shared/note.md", serveurType, p); got.Etat != PasArrive {
		t.Errorf("absent de l'arbre = %q, attendu %q", got.Etat, PasArrive)
	}
}

// TestUnionDesChemins : une copie de conflit locale n'existe que sur le poste.
// Mutation de contrôle : faire partir `Chemins` de `serveur` seul - la ligne
// disparaît, et l'écran perd le seul cas qu'aucune autre surface ne montre.
func TestUnionDesChemins(t *testing.T) {
	p := posteSain("portable", serveurType)
	p.Exceptions.ConflitsLocaux = map[string]bool{"shared/note (conflit local).md": true}

	chemins := Chemins(serveurType, []Poste{p})
	if len(chemins) != 3 {
		t.Fatalf("%d chemins, attendu 3 (2 du serveur + 1 du poste seul) : %v", len(chemins), chemins)
	}
	if got := EtatDuChemin("shared/note (conflit local).md", serveurType, p); got.Etat != ArbitrageEnAttente {
		t.Errorf("copie de conflit = %q, attendu %q", got.Etat, ArbitrageEnAttente)
	}
}

// TestToutArriveDistingueNonEtInconnu : « non » et « je ne sais pas » ne se
// confondent pas. Mutation de contrôle : faire tomber SansNouvelles dans le
// `default` - le cas muet rendrait alors (false, true), soit « ce fichier n'est
// pas arrivé, j'en suis sûr », qui est un mensonge.
func TestToutArriveDistingueNonEtInconnu(t *testing.T) {
	sain := posteSain("portable", serveurType)
	muet := Poste{Libelle: "fixe", AParle: false}
	manquant := posteSain("bureau", serveurType)
	delete(manquant.Empreintes, "shared/note.md")

	cas := []struct {
		nom          string
		postes       []Poste
		oui, certain bool
	}{
		{"tout le monde l'a", []Poste{sain}, true, true},
		{"un poste muet", []Poste{sain, muet}, true, false},
		{"un poste ne l'a pas", []Poste{sain, manquant}, false, true},
		{"muet ET manquant", []Poste{muet, manquant}, false, false},
	}
	for _, c := range cas {
		l := Tableau(serveurType, c.postes)[0] // shared/note.md, premier en ordre trié
		if l.Chemin != "shared/note.md" {
			t.Fatalf("%s : la première ligne est %q, le tri a changé", c.nom, l.Chemin)
		}
		oui, certain := ToutArrive(l)
		if oui != c.oui || certain != c.certain {
			t.Errorf("%s : (arrivé=%v, certain=%v), attendu (%v, %v)", c.nom, oui, certain, c.oui, c.certain)
		}
	}
}

// TestCopieDeConflitNestPasUnManque : une copie de conflit locale est SUR le
// poste, sous un autre nom. La compter comme une absence ferait clignoter en
// rouge une ligne qui attend un arbitrage, pas une réparation.
func TestCopieDeConflitNestPasUnManque(t *testing.T) {
	p := posteSain("portable", serveurType)
	p.Exceptions.ConflitsLocaux = map[string]bool{"shared/note (conflit local).md": true}
	vue := false
	for _, l := range Tableau(serveurType, []Poste{p}) {
		if l.Chemin != "shared/note (conflit local).md" {
			continue
		}
		vue = true
		oui, certain := ToutArrive(l)
		if !oui || !certain {
			t.Errorf("la copie rend (arrivé=%v, certain=%v), attendu (true, true) : "+
				"elle EST sur le poste, c'est le serveur qui ne l'a pas", oui, certain)
		}
		if l.SurLeServeur {
			t.Error("SurLeServeur = true, or ce chemin n'existe que sur le poste")
		}
	}
	if !vue {
		t.Fatal("la ligne n'apparaît pas dans le tableau - l'assertion ne pouvait pas échouer")
	}
}
