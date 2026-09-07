package web

// SLICE 7 (DAR-196) : la posture du coffre dans l'interface, et les trois
// regles du contrat de latence qui s'appliquent a un admin en HTML poste.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func TestLInterfaceParleEnPostureDeCoffre(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("shared/note.md", "x", "colin"); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "colin", "mdp")
	page := get(h, hrefDossiers("shared"), c).Body.String()

	for _, mot := range []string{"privé", "lecture seule", "ouvert"} {
		if !strings.Contains(page, mot) {
			t.Errorf("le mot %q du coffre n'apparaît pas à l'écran", mot)
		}
	}
	// LE MODELE NE BOUGE PAS : ce que le formulaire POSTE reste la forme de
	// stockage. Sans ce controle, traduire l'affichage aurait pu traduire la
	// valeur, et le serveur aurait refuse le geste.
	for _, valeur := range []string{`value="invisible"`, `value="lecture"`, `value="ecriture"`} {
		if !strings.Contains(page, valeur) {
			t.Errorf("la valeur postée %s a disparu : le vocabulaire a débordé sur le modèle", valeur)
		}
	}
}

func TestLesTroisReglesDeLatenceSontCablees(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "colin", "mdp")
	page := get(h, "/admin/", c).Body.String()

	// REGLE 1 : accuser le clic dans la meme image.
	if !strings.Contains(page, "button:active") {
		t.Error("règle 1 : aucun état pressé sur les boutons")
	}
	// REGLE 3 : le garde de re-entrance, immediat.
	if !strings.Contains(page, "dataset.envoye") {
		t.Error("règle 3 : rien n'empêche un second envoi")
	}
	if !strings.Contains(page, `button[aria-busy="true"]`) {
		t.Error("règle 3 : le geste parti ne se voit pas")
	}
	// REGLE 4, qui vient avec : la largeur est figée avant le changement d'état.
	if !strings.Contains(page, "minWidth") {
		t.Error("règle 4 : le bouton peut rétrécir sous le curseur")
	}
	// REGLE 7 : aucune erreur ne s'efface toute seule.
	if strings.Contains(page, "setTimeout") {
		t.Error("règle 7 : un minuteur traîne, un message peut disparaître tout seul")
	}
}

// Le garde ne doit RIEN retirer du formulaire. Le bouton nommé de l'écran des
// dossiers décide du geste par sa valeur : si le garde touchait à la
// sérialisation, « Retirer le défaut » cesserait de retirer quoi que ce soit.
func TestLeGardeNeTouchePasALaSerialisation(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "colin", "mdp")
	page := get(h, "/admin/", c).Body.String()
	if strings.Contains(page, ".disabled = true") {
		t.Error("le garde désactive un contrôle : la valeur d'un bouton nommé sortirait du POST")
	}
	if !strings.Contains(page, "pointer-events: none") {
		t.Error("le garde ne bloque pas le second clic")
	}
}

func TestLeVocabulaireDuJetonResteCeluiDUneAction(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	colin, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.IssueMCPToken(colin.ID, "routine", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "colin", "mdp")
	rec := get(h, "/admin/jetons", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("écran des jetons : %d", rec.Code)
	}
	// Un jeton n'est pas « ouvert » : il peut lire, ou lire et écrire.
	if !strings.Contains(rec.Body.String(), "lecture et écriture") {
		t.Error("le plafond d'un jeton devrait se dire comme une action")
	}
	if strings.Contains(rec.Body.String(), "<td>ouvert</td>") {
		t.Error("le vocabulaire du coffre a débordé sur le plafond d'un jeton")
	}
}
