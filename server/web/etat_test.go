package web

// Le banc de l'écran du mainteneur (DAR-198, slices 4 à 6).

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/auth"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
)

// serveurEtat : deux postes pour colin - un qui a parlé, un qui se taira.
func serveurEtat(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	s := newServer(t)
	colin, _ := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "mdp", perms.Invisible, false)
	s.DB.SetPermission(achille.ID, "shared", perms.Lecture)
	s.Store.Write("shared/note.md", "v1", "colin")
	s.Store.Write("shared/tasks.md", "taches", "colin")

	jetons := map[string]string{}
	jetons["portable"], _ = s.DB.IssueToken(colin.ID, "portable")
	jetons["fixe"], _ = s.DB.IssueToken(colin.ID, "fixe")
	return s, jetons
}

func teteCourante(t *testing.T, s *Server) string {
	t.Helper()
	h, err := s.Store.Head()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestEcranEtatMuetNeDitJamaisQueCaVa : LA garde de l'écran.
//
// Mutation de contrôle : dans le gabarit, faire rendre « à jour » au lieu de
// « sans nouvelles » pour le poste muet. Ce test doit tomber.
func TestEcranEtatMuetNeDitJamaisQueCaVa(t *testing.T) {
	s, jetons := serveurEtat(t)
	s.DB.EnregistreEtatPoste(auth.HashToken(jetons["portable"]), teteCourante(t, s),
		"2026-09-01T11:00:00Z", db.ExceptionsPoste{}, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))

	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	rec := get(h, "/admin/etat", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/etat = %d, attendu 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "sans nouvelles") {
		t.Error("le poste « fixe » n'a rien dit et l'écran ne le signale pas")
	}
	if !strings.Contains(body, "ne disent pas que les fichiers manquent") {
		t.Error("l'avertissement de tête manque : un tableau plein de « sans nouvelles » doit s'expliquer")
	}
	// La colonne de résumé ne doit PAS dire « oui » : un poste muet rend la
	// réponse incertaine, pas positive.
	if !strings.Contains(body, "on ne sait pas") {
		t.Error("le résumé par ligne conclut alors qu'un poste est muet")
	}
}

// TestEcranEtatMontreLaisseEtMotif : le motif est la moitié de l'information.
func TestEcranEtatMontreLaisseEtMotif(t *testing.T) {
	s, jetons := serveurEtat(t)
	tete := teteCourante(t, s)
	exc := db.ExceptionsPoste{
		Laisses:        []db.LaissePoste{{Chemin: "shared/note.md", Raison: "un dossier en travers du chemin"}},
		ConflitsLocaux: []string{"shared/tasks (conflit local).md"},
	}
	for _, nom := range []string{"portable", "fixe"} {
		s.DB.EnregistreEtatPoste(auth.HashToken(jetons[nom]), tete, "2026-09-01T11:00:00Z",
			exc, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	body := get(h, "/admin/etat", c).Body.String()

	for _, attendu := range []string{
		"laissé", "un dossier en travers du chemin",
		"arbitrage en attente", "shared/tasks (conflit local).md",
		"n'existe que sur un poste",
	} {
		if !strings.Contains(body, attendu) {
			t.Errorf("l'écran ne montre pas %q", attendu)
		}
	}
}

// TestEcranEtatEchappeLeMotif : le motif et les chemins viennent du POSTE.
//
// C'est le seul texte de cet écran qu'un tiers contrôle : un poste compromis,
// ou simplement un nom de fichier malicieux, arrive ici tel quel. `html/template`
// échappe par défaut, mais « par défaut » n'est pas une preuve - ce test l'est.
func TestEcranEtatEchappeLeMotif(t *testing.T) {
	s, jetons := serveurEtat(t)
	exc := db.ExceptionsPoste{
		Laisses: []db.LaissePoste{{
			Chemin: "shared/<script>alert(1)</script>.md",
			Raison: "<img src=x onerror=alert(2)>",
		}},
	}
	s.DB.EnregistreEtatPoste(auth.HashToken(jetons["portable"]), teteCourante(t, s),
		"2026-09-01T11:00:00Z", exc, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	body := get(h, "/admin/etat", c).Body.String()

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("un chemin de poste est rendu sans échappement")
	}
	if strings.Contains(body, "<img src=x onerror=") {
		t.Error("un motif de poste est rendu sans échappement")
	}
	if !strings.Contains(body, "&lt;img src=x onerror=alert(2)&gt;") {
		t.Error("le motif échappé ne s'affiche pas : échapper ne doit pas vouloir dire perdre")
	}
}

// TestEcranEtatDitQuiAAcces : la seconde moitié du contenu de DAR-198.
func TestEcranEtatDitQuiAAcces(t *testing.T) {
	s, jetons := serveurEtat(t)
	s.DB.EnregistreEtatPoste(auth.HashToken(jetons["portable"]), teteCourante(t, s),
		"2026-09-01T11:00:00Z", db.ExceptionsPoste{}, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	body := get(h, "/admin/etat", c).Body.String()

	if !strings.Contains(body, "achille") {
		t.Error("achille a la lecture sur shared, l'écran ne le dit pas")
	}
	if !strings.Contains(body, "lecture seule") {
		t.Error("le niveau doit s'afficher dans le vocabulaire du coffre (DAR-196)")
	}
	if !strings.Contains(body, "posé sur ce compte") {
		t.Error("la provenance du droit doit s'afficher, comme sur l'écran des droits")
	}
}

// TestEcranEtatTeteIllisible : un poste peut déclarer n'importe quoi.
//
// Le contrat est « sans nouvelles », JAMAIS une flotte à zéro : une carte
// d'empreintes vide se lirait « ce poste n'a aucun fichier », donc tout en
// rouge, alors que la vérité est « je ne sais pas lire sa position ».
func TestEcranEtatTeteIllisible(t *testing.T) {
	s, jetons := serveurEtat(t)
	s.DB.EnregistreEtatPoste(auth.HashToken(jetons["portable"]), "pas-un-commit",
		"2026-09-01T11:00:00Z", db.ExceptionsPoste{}, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	rec := get(h, "/admin/etat", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("une tête illisible fait tomber l'écran : %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "position illisible") {
		t.Error("une tête illisible doit se dire, pas se confondre avec un poste sain")
	}
	if strings.Contains(body, `class="etat pas-arrive"`) {
		t.Error("une tête illisible rend « pas arrivé » : le poste passe pour vide alors qu'on ne sait pas le lire")
	}
}

// TestEcranEtatIgnoreLesSessionsWeb : trouvé en REGARDANT LE RENDU, pas par un
// test - c'est exactement ce que DAR-195 annonce.
//
// Chaque connexion au web émet un jeton d'appareil libellé « web ». Sans
// filtre, l'écran affiche une colonne « sans nouvelles » de plus à chaque
// connexion : au bout d'une semaine le tableau est illisible, le compteur de
// postes muets est faux, et la colonne « Partout ? » dit « on ne sait pas »
// partout et pour toujours.
//
// Mutation de contrôle : retirer le `WHERE t.label <> ?` d'`EtatsPostes`.
func TestEcranEtatIgnoreLesSessionsWeb(t *testing.T) {
	s, jetons := serveurEtat(t)
	s.DB.EnregistreEtatPoste(auth.HashToken(jetons["portable"]), teteCourante(t, s),
		"2026-09-01T11:00:00Z", db.ExceptionsPoste{}, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))

	h := s.Handler()
	// Trois connexions au web : trois jetons « web » de plus en base.
	login(t, h, "colin", "mdp")
	login(t, h, "colin", "mdp")
	c := login(t, h, "colin", "mdp")

	postes, err := s.DB.EtatsPostes()
	if err != nil {
		t.Fatalf("EtatsPostes : %v", err)
	}
	if len(postes) != 2 {
		var libelles []string
		for _, p := range postes {
			libelles = append(libelles, p.Libelle)
		}
		t.Fatalf("%d postes après trois connexions web, attendu 2 (portable, fixe) : %v", len(postes), libelles)
	}
	body := get(h, "/admin/etat", c).Body.String()
	if strings.Contains(body, ">web<") {
		t.Error("une session de navigateur s'affiche comme un poste")
	}
	if !strings.Contains(body, "1 poste(s) ne se sont pas manifestés") {
		t.Error("le compteur de postes muets compte les sessions web")
	}
}

// TestEcranEtatReserveAuxAdmins : c'est un écran d'administration.
func TestEcranEtatReserveAuxAdmins(t *testing.T) {
	s, _ := serveurEtat(t)
	h := s.Handler()
	c := login(t, h, "achille", "mdp")
	if rec := get(h, "/admin/etat", c); rec.Code == http.StatusOK {
		t.Errorf("un non-admin atteint /admin/etat (code %d)", rec.Code)
	}
}

// TestEcranFichiersRedirigeToujours : la redirection des favoris n'est pas
// reprise par le nouvel écran.
func TestEcranFichiersRedirigeToujours(t *testing.T) {
	s, _ := serveurEtat(t)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	rec := get(h, "/admin/fichiers", c)
	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("/admin/fichiers = %d, attendu 301 - les favoris du 21/08 restent servis", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/dossiers" {
		t.Errorf("Location = %q, attendu /admin/dossiers", loc)
	}
}
