package web

// Les formulaires de droits acceptent un chemin TAPE A LA MAIN. Rien ne les
// empechait de viser la zone skills, que la vue d'administration exclut par
// construction : la regle etait ecrite, puis l'auteur du geste atterrissait sur
// une page d'erreur sans savoir si elle avait pris. Elle avait pris.
//
// Les quatre portes qui acceptent un chemin libre refusent desormais cette
// zone, et le disent.

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
)

func TestLesFormulairesDeDroitsRefusentLaZoneSkills(t *testing.T) {
	// Le motif tel qu'il ARRIVE DANS LA PAGE : html/template echappe
	// l'apostrophe. Chercher la chaine brute rendrait ce test faux-negatif.
	const messageAttendu = "les droits d&#39;un skill se règlent"

	// La racine elle-meme et un chemin dedans : la premiere n'est pas « sous »
	// la racine au sens du prefixe, et c'etait le trou de la premiere version.
	for _, chemin := range []string{skills.DefaultRoot, skills.DefaultRoot + "/blank-page"} {
		t.Run(chemin, func(t *testing.T) {
			s := newServer(t)
			h := s.Handler()
			colin, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
			if err != nil {
				t.Fatal(err)
			}
			cible, err := s.DB.CreateUser("marie", "mdp", perms.Lecture, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Store.Write(skills.DefaultRoot+"/blank-page/SKILL.md", "x", "marie"); err != nil {
				t.Fatal(err)
			}
			groupe, err := s.DB.CreerGroupe("direction")
			if err != nil {
				t.Fatal(err)
			}
			c := login(t, h, "colin", "mdp")
			_ = colin
			avant := empreinteRegles(t, s, cible.ID)

			portes := []struct {
				nom, route string
				form       url.Values
			}{
				{"droit sur le dossier", "/admin/dossiers/acces", url.Values{
					"chemin": {chemin}, "niveau": {"lecture"},
					"user_id": {strconv.FormatInt(cible.ID, 10)},
				}},
				// 02/09 : le rangement dans une cohorte a déménagé sur la
				// cohorte. La garde suit la porte - et elle compte DOUBLE ici,
				// parce que `RangeChemin` réécrit le genre : ranger un chemin de
				// skill comme « dossier » le ferait disparaître de l'écran des
				// skills sans un mot.
				{"cohorte", "/admin/cohortes/niveau-chemin", url.Values{
					"groupe_id": {strconv.FormatInt(groupe, 10)}, "chemin": {chemin},
				}},
				{"cohorte (arbre)", "/admin/cohortes/modifier", url.Values{
					"groupe_id": {strconv.FormatInt(groupe, 10)}, "nom": {"g"},
					"noeud": {chemin}, "niveau_" + chemin: {"lecture"},
				}},
				{"défaut du dossier", "/admin/dossiers/defaut", url.Values{
					"chemin": {chemin}, "niveau": {"lecture"},
				}},
				{"droit depuis la fiche", "/admin/users/" + strconv.FormatInt(cible.ID, 10) + "/droits",
					url.Values{"chemin": {chemin}, "niveau": {"lecture"}}},
			}

			for _, p := range portes {
				rec := poste(t, h, c, p.route, p.form)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("%s : attendu 400, obtenu %d", p.nom, rec.Code)
				}
				if !strings.Contains(rec.Body.String(), messageAttendu) {
					t.Errorf("%s : le motif du refus n'est pas dit à l'écran", p.nom)
				}
			}

			// ET RIEN N'A CHANGE. Un refus qui affiche une erreur apres avoir
			// ecrit serait le defaut d'origine, deguise.
			//
			// On compare a un INSTANTANE pris avant, pas a l'absence de regle :
			// CreateUser pose deja une regle `invisible` d'origine `defaut` sur
			// la racine des skills pour tout compte (modele F3, prive par
			// defaut). Asserter « aucune regle ici » serait faux des la creation
			// du compte, et le test echouerait sans qu'aucun formulaire n'ait
			// rien ecrit.
			if apres := empreinteRegles(t, s, cible.ID); apres != avant {
				t.Errorf("les règles de la cible ont changé malgré le refus :\n avant %s\n après %s", avant, apres)
			}
			chemins, err := s.DB.CheminsDuGroupe(groupe, "dossier")
			if err != nil {
				t.Fatal(err)
			}
			if len(chemins) != 0 {
				t.Errorf("un chemin de skill a été rangé dans la cohorte : %v", chemins)
			}
		})
	}
}

// empreinteRegles rend une forme comparable des regles d'un compte.
func empreinteRegles(t *testing.T, s *Server, userID int64) string {
	t.Helper()
	regles, err := s.DB.Rules(userID)
	if err != nil {
		t.Fatal(err)
	}
	parts := make([]string, 0, len(regles))
	for _, r := range regles {
		parts = append(parts, perms.Canon(r.Path)+"="+r.Level.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
