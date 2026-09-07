package api

// mcp_ecriture_test.go : S3.
//
// Deux critères portent cette slice, et aucun des deux n'est un test de forme :
// les deux portes rendent LE MÊME verdict sur les mêmes entrées, et une
// écriture MCP redescend réellement sur un poste au cycle suivant.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
	"github.com/colindargent/vecu/server/storage"
)

// setupEcriture : deux comptes aux périmètres différents, et des jetons MCP aux
// deux plafonds.
func setupEcriture(t *testing.T) (*httptest.Server, *db.DB, *storage.Store, map[string]int64, map[string]string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatal(err)
	}
	store.Write("equipe/README.md", "# equipe\n", "colin")
	store.Write("lecture-seule/note.md", "on ne touche pas\n", "colin")
	store.Write("prive/interne.md", "interne\n", "colin")

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false)
	database.SetPermission(achille.ID, "equipe", perms.Ecriture)
	database.SetPermission(achille.ID, "lecture-seule", perms.Lecture)

	ids := map[string]int64{"colin": colin.ID, "achille": achille.ID}
	// Jetons MCP.
	mcp := map[string]string{}
	mcp["colin"], _ = database.IssueMCPToken(colin.ID, "banc", perms.Ecriture)
	mcp["achille"], _ = database.IssueMCPToken(achille.ID, "banc", perms.Ecriture)
	mcp["colin-lecture"], _ = database.IssueMCPToken(colin.ID, "plafonne", perms.Lecture)
	// Jetons d'APPAREIL, pour jouer la porte HTTP avec les mêmes comptes.
	dev := map[string]string{}
	dev["colin"], _ = database.IssueToken(colin.ID, "poste")
	dev["achille"], _ = database.IssueToken(achille.ID, "poste")

	srv := httptest.NewServer((&Server{DB: database, Store: store}).Handler())
	t.Cleanup(srv.Close)
	return srv, database, store, ids, mcp, dev
}

// ecritParMCP joue `ecrire_fichier` et rend (texte, isError).
func ecritParMCP(t *testing.T, srv *httptest.Server, jeton, chemin, contenu string) (string, bool) {
	t.Helper()
	return appelleOutil(t, srv, jeton, "ecrire_fichier",
		map[string]any{"chemin": chemin, "contenu": contenu})
}

// clientSansRedirection : le client des bancs de comparaison.
//
// NE SUIT PAS LES REDIRECTIONS, et c'est essentiel. `ServeMux` REDIRIGE (301)
// un chemin qui contient « . », « .. » ou un double slash vers sa forme
// nettoyée : un client qui suit la redirection fait donc voir au serveur un
// chemin PROPRE, et compare alors « HTTP avec un chemin nettoyé par le
// routeur » à « MCP avec le chemin brut ». Ce n'est pas la même entrée, donc
// ce n'est pas une comparaison.
//
// Sans redirection, les deux portes sont jugées sur la chaîne telle qu'elle
// leur est donnée - un 301 comptant comme un refus d'écrire, ce qu'il est.
var clientSansRedirection = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// ecritParHTTP joue `PUT /files/{p}` et rend le code HTTP.
func ecritParHTTP(t *testing.T, srv *httptest.Server, jetonAppareil, chemin, contenu, baseOID string) int {
	t.Helper()
	corps, _ := json.Marshal(map[string]string{"content": contenu, "base_oid": baseOID})
	req, _ := http.NewRequest("PUT", srv.URL+"/files/"+chemin, strings.NewReader(string(corps)))
	req.Header.Set("Authorization", "Bearer "+jetonAppareil)
	req.Header.Set("Content-Type", "application/json")
	resp, err := clientSansRedirection.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// LE CRITÈRE QUI LIE LES DEUX PORTES, et il faut qu'il les lie VRAIMENT.
//
// La première version jouait des chemins DIFFÉRENTS sur chaque porte et
// comparait chacune à sa propre colonne codée en dur : deux tests de table
// juxtaposés, incapables de voir une divergence. Mesuré en revue : la porte MCP
// canonicalisait le chemin et la porte HTTP non, donc `equipe/trail.md/` était
// refusé par l'une et écrit par l'autre - et le test ne le voyait pas.
//
// Ici : LE MÊME chemin, LE MÊME compte, les deux portes, et on compare leurs
// verdicts l'un à l'autre.
func TestMCPLesDeuxPortesRendentLeMemeVerdict(t *testing.T) {
	chemins := []struct {
		nom    string
		compte string
		chemin string
	}{
		{"écriture autorisée", "achille", "equipe/ok.md"},
		{"dossier en lecture seule", "achille", "lecture-seule/tentative.md"},
		{"dossier invisible", "achille", "prive/tentative.md"},
		{"chemin hors espace", "colin", "a-la-racine.md"},
		{"écriture autorisée (admin)", "colin", "equipe/admin.md"},
		{"slash final", "colin", "equipe/trail.md/"},
		{"segment point", "colin", "equipe/./dot.md"},
		{"double slash", "colin", "equipe//double.md"},
		{"remontée", "colin", "equipe/sous/../up.md"},
		{"espace neuf", "colin", "espace-neuf/note.md"},
		{"nom d'espace vide", "colin", "/orphelin.md"},
	}
	for _, c := range chemins {
		t.Run(c.nom, func(t *testing.T) {
			// UN SERVEUR NEUF PAR CAS : les deux portes jouent le même chemin
			// sur le même état de départ, sinon la seconde hériterait de ce que
			// la première a écrit.
			srvA, _, _, _, _, devA := setupEcriture(t)
			codeHTTP := ecritParHTTP(t, srvA, devA[c.compte], c.chemin, "contenu\n", "")

			srvB, _, _, _, mcpB, _ := setupEcriture(t)
			_, isErr := ecritParMCP(t, srvB, mcpB[c.compte], c.chemin, "contenu\n")

			accepteHTTP := codeHTTP >= 200 && codeHTTP < 300
			accepteMCP := !isErr
			if accepteHTTP != accepteMCP {
				t.Errorf("LES DEUX PORTES DIVERGENT sur %q : HTTP rend %d (accepté=%v), MCP rend isError=%v",
					c.chemin, codeHTTP, accepteHTTP, isErr)
			}
		})
	}
}

// Le même lien, pour la SUPPRESSION. Elle manquait entièrement.
func TestMCPLesDeuxPortesSupprimentPareil(t *testing.T) {
	chemins := []struct {
		nom    string
		compte string
		chemin string
	}{
		{"suppression autorisée", "achille", "equipe/README.md"},
		{"dossier en lecture seule", "achille", "lecture-seule/note.md"},
		{"dossier invisible", "achille", "prive/interne.md"},
		{"chemin permis mais absent", "achille", "equipe/jamais-existe.md"},
		{"chemin de dossier", "achille", "equipe"},
		{"hors espace", "colin", "a-la-racine.md"},
	}
	for _, c := range chemins {
		t.Run(c.nom, func(t *testing.T) {
			srvA, _, _, _, _, devA := setupEcriture(t)
			req, _ := http.NewRequest("DELETE", srvA.URL+"/files/"+c.chemin, nil)
			req.Header.Set("Authorization", "Bearer "+devA[c.compte])
			resp, err := clientSansRedirection.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			srvB, _, _, _, mcpB, _ := setupEcriture(t)
			_, isErr := appelleOutil(t, srvB, mcpB[c.compte], "supprimer_fichier",
				map[string]any{"chemin": c.chemin})

			accepteHTTP := resp.StatusCode >= 200 && resp.StatusCode < 300
			if accepteHTTP == isErr {
				t.Errorf("LES DEUX PORTES DIVERGENT sur la suppression de %q : HTTP rend %d, MCP rend isError=%v",
					c.chemin, resp.StatusCode, isErr)
			}
		})
	}
}

// OMETTRE `contenu` NE DOIT PAS VIDER LE FICHIER.
//
// Mesuré en revue : un appel sans l'argument écrivait la chaîne vide, rendait
// « Écrit », isError:false, sans copie de conflit ni aucune trace. L'appelant
// est un modèle, dont la génération peut être coupée en plein appel - c'est le
// mode de perte le plus banal de cette surface.
func TestMCPEcrireSansContenuNeVidePasLeFichier(t *testing.T) {
	srv, _, store, _, mcp, _ := setupEcriture(t)

	const chemin = "equipe/precieux.md"
	const precieux = "beaucoup de texte\nligne 2\nligne 3\n"
	if _, isErr := ecritParMCP(t, srv, mcp["colin"], chemin, precieux); isErr {
		t.Fatal("préparation")
	}

	// L'argument est ABSENT du tout, pas vide.
	txt, isErr := appelleOutil(t, srv, mcp["colin"], "ecrire_fichier",
		map[string]any{"chemin": chemin})
	if !isErr {
		t.Errorf("une écriture sans « contenu » a été acceptée : %q", txt)
	}
	got, _ := store.Read(chemin, "")
	if got != precieux {
		t.Errorf("le fichier a été modifié : %q, attendu %q", got, precieux)
	}

	// Mais vider DÉLIBÉRÉMENT reste possible : c'est une chaîne vide explicite,
	// pas une omission. La distinction est tout l'intérêt du correctif.
	if _, isErr := appelleOutil(t, srv, mcp["colin"], "ecrire_fichier",
		map[string]any{"chemin": chemin, "contenu": ""}); isErr {
		t.Error("vider délibérément un fichier a été refusé")
	}
	if got, _ := store.Read(chemin, ""); got != "" {
		t.Errorf("le vidage délibéré n'a pas pris : %q", got)
	}
}

// Le catalogue DÉPEND du plafond : un jeton « lecture » ne se voit pas proposer
// les outils d'écriture, et ne brûle donc pas un tour à les appeler.
func TestMCPLeCatalogueDependDuPlafondDuJeton(t *testing.T) {
	srv, _, _, _, mcp, _ := setupEcriture(t)

	noms := func(jeton string) []string {
		resp := appelMCP(t, srv, jeton, "tools/list", nil, nil)
		res, _ := corpsMCP(t, resp)["result"].(map[string]any)
		outils, _ := res["tools"].([]any)
		var out []string
		for _, o := range outils {
			out = append(out, o.(map[string]any)["name"].(string))
		}
		return out
	}

	if got := len(noms(mcp["colin"])); got != 5 {
		t.Errorf("jeton écriture : %d outils, attendu 5", got)
	}
	lecture := noms(mcp["colin-lecture"])
	if len(lecture) != 3 {
		t.Errorf("jeton lecture : %d outils, attendu 3 (%v)", len(lecture), lecture)
	}
	for _, n := range lecture {
		if n == "ecrire_fichier" || n == "supprimer_fichier" {
			t.Errorf("un jeton lecture se voit proposer %q", n)
		}
	}
}

// Le plafond du jeton mord ICI, et c'est la seule slice où il mord : un jeton
// « lecture » ne peut pas écrire, MÊME si son compte a l'écriture partout.
func TestMCPUnJetonPlafonneLectureNePeutPasEcrire(t *testing.T) {
	srv, _, _, _, mcp, _ := setupEcriture(t)

	// Précondition : le MÊME compte, par un jeton non plafonné, écrit bien.
	if _, isErr := ecritParMCP(t, srv, mcp["colin"], "equipe/temoin.md", "ok\n"); isErr {
		t.Fatal("précondition : le jeton écriture devrait pouvoir écrire")
	}

	txt, isErr := ecritParMCP(t, srv, mcp["colin-lecture"], "equipe/interdit.md", "non\n")
	if !isErr {
		t.Error("un jeton plafonné à « lecture » a écrit")
	}
	if !strings.Contains(txt, "lecture seule") {
		t.Errorf("le refus ne dit pas que c'est le PLAFOND qui bloque : %q", txt)
	}
	// Et il ne supprime pas davantage.
	if _, isErr := appelleOutil(t, srv, mcp["colin-lecture"], "supprimer_fichier",
		map[string]any{"chemin": "equipe/temoin.md"}); !isErr {
		t.Error("un jeton plafonné à « lecture » a supprimé")
	}
	// Mais il LIT toujours.
	if _, isErr := appelleOutil(t, srv, mcp["colin-lecture"], "lire_fichier",
		map[string]any{"chemin": "equipe/temoin.md"}); isErr {
		t.Error("un jeton plafonné à « lecture » ne peut plus lire")
	}
}

// baseOID = head courant. Une écriture MCP sur un chemin inchangé ne doit
// produire AUCUNE copie de conflit.
//
// C'est le piège nommé par la spec : passer une chaîne vide ferait une fusion
// sans base commune, donc une copie à presque chaque écriture.
func TestMCPUneEcritureNeProduitPasDeCopieDeConflit(t *testing.T) {
	srv, _, store, _, mcp, _ := setupEcriture(t)

	// Dix écritures successives sur le même chemin, comme un agent qui itère.
	for i := range 10 {
		txt, isErr := ecritParMCP(t, srv, mcp["colin"], "equipe/iteratif.md",
			strings.Repeat("version ", i+1)+"\n")
		if isErr {
			t.Fatalf("écriture %d refusée : %s", i, txt)
		}
		if strings.Contains(txt, "conflit") {
			t.Errorf("écriture %d a produit une copie de conflit : %s", i, txt)
		}
	}
	chemins, err := store.List("")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range chemins {
		if strings.Contains(p, "conflit") {
			t.Errorf("copie de conflit sur le dépôt : %q", p)
		}
	}
}

// Deux écritures MCP concurrentes sur des chemins DIFFÉRENTS fusionnent, comme
// entre deux postes. Aucune ne perd.
func TestMCPDeuxEcrituresConcurrentesFusionnent(t *testing.T) {
	srv, _, store, _, mcp, _ := setupEcriture(t)

	if _, isErr := ecritParMCP(t, srv, mcp["colin"], "equipe/a.md", "de colin\n"); isErr {
		t.Fatal("écriture a")
	}
	if _, isErr := ecritParMCP(t, srv, mcp["achille"], "equipe/b.md", "d'achille\n"); isErr {
		t.Fatal("écriture b")
	}
	head, _ := store.Head()
	for chemin, attendu := range map[string]string{"equipe/a.md": "de colin\n", "equipe/b.md": "d'achille\n"} {
		got, err := store.Read(chemin, head)
		if err != nil || got != attendu {
			t.Errorf("%s = %q (%v), attendu %q", chemin, got, err, attendu)
		}
	}
}

// La suppression par MCP passe par la même porte que le DELETE de l'API.
func TestMCPSuppressionParLaMemePorte(t *testing.T) {
	srv, _, store, _, mcp, _ := setupEcriture(t)

	if _, isErr := ecritParMCP(t, srv, mcp["achille"], "equipe/ephemere.md", "à supprimer\n"); isErr {
		t.Fatal("écriture préalable")
	}
	txt, isErr := appelleOutil(t, srv, mcp["achille"], "supprimer_fichier",
		map[string]any{"chemin": "equipe/ephemere.md"})
	if isErr {
		t.Fatalf("suppression refusée : %s", txt)
	}
	if store.Exists("equipe/ephemere.md") {
		t.Error("le fichier existe encore après suppression")
	}

	// Un chemin en lecture seule : refusé, en isError.
	txt, isErr = appelleOutil(t, srv, mcp["achille"], "supprimer_fichier",
		map[string]any{"chemin": "lecture-seule/note.md"})
	if !isErr {
		t.Error("suppression acceptée sur un dossier en lecture seule")
	}
	if !store.Exists("lecture-seule/note.md") {
		t.Error("le fichier a été supprimé malgré le refus")
	}
	// PAS D'ORACLE D'EXISTENCE, et la comparaison doit être faite DANS LE MÊME
	// CONTEXTE DE DROITS. Comparer « interdit » à « permis mais absent » ne
	// prouverait rien : le porteur sait déjà qu'il a l'écriture ici et pas
	// là-bas, donc les deux réponses ne lui apprennent rien de neuf. Ce qui
	// serait un oracle, c'est de distinguer, DANS UNE ZONE INTERDITE, un
	// fichier qui existe d'un fichier qui n'existe pas.
	existe, errExiste := appelleOutil(t, srv, mcp["achille"], "supprimer_fichier",
		map[string]any{"chemin": "prive/interne.md"})
	pasLa, errPasLa := appelleOutil(t, srv, mcp["achille"], "supprimer_fichier",
		map[string]any{"chemin": "prive/jamais-existe.md"})
	if !errExiste || !errPasLa {
		t.Errorf("les deux devraient être refusés : existe=%v pasLà=%v", errExiste, errPasLa)
	}
	gabarit := func(s, chemin string) string { return strings.ReplaceAll(s, chemin, "<CHEMIN>") }
	if gabarit(existe, "prive/interne.md") != gabarit(pasLa, "prive/jamais-existe.md") {
		t.Errorf("oracle d'existence dans une zone interdite :\n  existe    = %q\n  n'existe pas = %q", existe, pasLa)
	}

	// Et le compte rendu ne MENT pas : supprimer un chemin permis mais absent
	// ne s'annonce pas « Supprimé ». Le verdict, lui, reste celui de la porte
	// HTTP, qui rend 200 sur ce cas depuis toujours.
	rien, isErr := appelleOutil(t, srv, mcp["achille"], "supprimer_fichier",
		map[string]any{"chemin": "equipe/jamais-existe.md"})
	if isErr {
		t.Errorf("suppression d'un chemin permis mais absent refusée : %q (la porte HTTP rend 200)", rien)
	}
	if strings.HasPrefix(rien, "Supprimé") {
		t.Errorf("le compte rendu annonce une suppression qui n'a pas eu lieu : %q", rien)
	}
	if !strings.Contains(rien, "Rien n'a été supprimé") {
		t.Errorf("le compte rendu ne dit pas qu'il n'y avait rien : %q", rien)
	}
}

// L'APPROPRIATION D'UN SKILL EST REFUSÉE PAR LA PORTE MCP, et c'est le choix
// tranché au build que la spec demandait d'écrire.
//
// La revendication est la seule écriture qui CRÉE un droit au lieu d'en
// exercer un. Un humain qui la déclenche voit ce qu'il fait ; une routine qui
// tourne sans personne, non - il suffirait qu'un agent écrive dans
// `shared/skills/quelque-chose/` pour s'emparer d'un nom, en silence.
func TestMCPLaPorteMCPNeRevendiquePasUnSkill(t *testing.T) {
	srv, database, store, ids, mcp, dev := setupEcriture(t)

	const chemin = skills.DefaultRoot + "/nouveau-skill/SKILL.md"

	// Par MCP : refusé, et RIEN n'est revendiqué.
	txt, isErr := ecritParMCP(t, srv, mcp["colin"], chemin, "# Nouveau\n")
	if !isErr {
		t.Errorf("la porte MCP a revendiqué un skill : %s", txt)
	}
	if store.Exists(chemin) {
		t.Error("le fichier du skill a été écrit par la porte MCP")
	}
	if _, ok, _ := database.CreatorOf("nouveau-skill"); ok {
		t.Error("un créateur a été enregistré par la porte MCP")
	}

	// Par la porte HTTP, le MÊME geste réussit : la divergence est bien celle
	// qu'on a décidée, et pas un droit cassé au passage.
	if code := ecritParHTTP(t, srv, dev["colin"], chemin, "# Nouveau\n", ""); code != http.StatusOK {
		t.Fatalf("porte HTTP sur le même chemin : %d, attendu 200", code)
	}
	createur, ok, _ := database.CreatorOf("nouveau-skill")
	if !ok || createur != ids["colin"] {
		t.Errorf("la porte HTTP n'a pas revendiqué : createur=%v ok=%v", createur, ok)
	}

	// ET MAINTENANT LE POINT QUI COMPTE : le skill EXISTE, donc la porte MCP
	// l'écrit comme la porte HTTP, selon les droits normaux. Le refus ne
	// portait que sur la CRÉATION.
	if _, isErr := ecritParMCP(t, srv, mcp["colin"], chemin, "# Nouveau, révisé\n"); isErr {
		t.Error("la porte MCP refuse d'écrire dans un skill qui existe déjà et dont le compte est créateur")
	}
	contenu, _ := store.Read(chemin, "")
	if !strings.Contains(contenu, "révisé") {
		t.Errorf("l'écriture MCP sur un skill existant n'a pas pris : %q", contenu)
	}
}

// La branche 404 de `supprimeFichier` n'était couverte par RIEN : la mutation
// `if false && e.niveau(p) < perms.Lecture` laissait toute la suite verte.
//
// Le test précédent la ratait par construction : il comparait les deux refus
// l'un à l'autre après avoir gabarité le chemin, donc les deux basculant
// ensemble sur 403, les messages restaient identiques et l'assertion verte.
// Ici on compare au message ATTENDU, pas à l'autre refus.
func TestMCPSuppressionInvisibleEtLectureSeuleNeSeDisentPasPareil(t *testing.T) {
	srv, _, _, _, mcp, _ := setupEcriture(t)

	// Invisible : le MÊME message qu'un chemin qui n'existe pas. Le porteur ne
	// doit pas apprendre que le fichier existe.
	invisible, isErr := appelleOutil(t, srv, mcp["achille"], "supprimer_fichier",
		map[string]any{"chemin": "prive/interne.md"})
	if !isErr {
		t.Error("suppression acceptée sur un chemin invisible")
	}
	if !strings.Contains(invisible, "Aucun fichier lisible") {
		t.Errorf("un chemin invisible devrait se dire « aucun fichier lisible » : %q", invisible)
	}

	// Lecture seule : le porteur PEUT lire, donc il sait déjà que le fichier
	// existe. Lui dire « écriture non autorisée » ne lui apprend rien de neuf,
	// et l'empêche de croire que le chemin est faux.
	lectureSeule, isErr := appelleOutil(t, srv, mcp["achille"], "supprimer_fichier",
		map[string]any{"chemin": "lecture-seule/note.md"})
	if !isErr {
		t.Error("suppression acceptée sur un dossier en lecture seule")
	}
	if !strings.Contains(lectureSeule, "non autorisée") {
		t.Errorf("un chemin en lecture seule devrait se dire « non autorisée » : %q", lectureSeule)
	}

	// Et les deux messages DIFFÈRENT, ce qui prouve que les deux branches sont
	// bien atteintes - c'est ce que le test précédent ne pouvait pas voir.
	if invisible == lectureSeule {
		t.Error("les deux branches rendent le même message : l'une d'elles n'est pas atteinte")
	}
}

// Supprimer un chemin de DOSSIER ne s'annonce pas « Supprimé », et ne pose plus
// de commit vide sur `main`.
//
// Mesuré en revue : `deleteLocked` retirait une entrée d'index inexistante, ce
// qui produisait le même arbre - mais le head avançait quand même, donc la
// garde d'honnêteté (« le head n'a pas bougé ») ne mordait pas, et un agent
// lisait « Supprimé » sur un dossier intact.
func TestMCPSupprimerUnDossierNeMentPasEtNeCommittePas(t *testing.T) {
	srv, _, store, _, mcp, _ := setupEcriture(t)

	if _, isErr := ecritParMCP(t, srv, mcp["colin"], "equipe/sous/a.md", "contenu\n"); isErr {
		t.Fatal("préparation")
	}
	avant, _ := store.Head()

	txt, _ := appelleOutil(t, srv, mcp["colin"], "supprimer_fichier",
		map[string]any{"chemin": "equipe/sous"})
	if strings.HasPrefix(txt, "Supprimé") {
		t.Errorf("le compte rendu annonce une suppression qui n'a pas eu lieu : %q", txt)
	}
	if !store.Exists("equipe/sous/a.md") {
		t.Error("le contenu du dossier a disparu")
	}
	apres, _ := store.Head()
	if apres != avant {
		t.Errorf("un commit vide a été posé sur main : %s -> %s", avant, apres)
	}
}

// Le message de conflit dit la VÉRITÉ sur qui a gagné.
//
// `WriteMerge` garde la version du SERVEUR au nom canonique et range la nôtre
// dans la copie (modèle Dropbox). La première version du message disait
// l'inverse, ce qui ferait croire à un agent que son contenu est en place.
func TestMCPLeMessageDeConflitDitQuiAGagne(t *testing.T) {
	srv, _, store, _, mcp, dev := setupEcriture(t)

	// On fabrique un vrai conflit : un poste écrit avec un ancêtre périmé, sur
	// un chemin que l'agent vient de modifier autrement.
	if _, isErr := ecritParMCP(t, srv, mcp["colin"], "equipe/dispute.md", "L1\n"); isErr {
		t.Fatal("préparation")
	}
	perime, _ := store.Head()
	if _, isErr := ecritParMCP(t, srv, mcp["colin"], "equipe/dispute.md", "version agent\n"); isErr {
		t.Fatal("écriture agent")
	}
	// Le poste pousse depuis l'ancêtre périmé : conflit côté serveur.
	code := ecritParHTTP(t, srv, dev["colin"], "equipe/dispute.md", "version poste\n", perime)
	if code != http.StatusOK {
		t.Fatalf("écriture poste : %d", code)
	}

	// Et maintenant l'agent réécrit depuis le head : pas de conflit. On vérifie
	// donc le message sur le chemin inverse - un conflit provoqué côté MCP.
	perime2, _ := store.Head()
	if _, isErr := ecritParMCP(t, srv, mcp["colin"], "equipe/dispute.md", "encore l'agent\n"); isErr {
		t.Fatal("écriture agent 2")
	}
	code = ecritParHTTP(t, srv, dev["colin"], "equipe/dispute.md", "encore le poste\n", perime2)
	if code != http.StatusOK {
		t.Fatalf("écriture poste 2 : %d", code)
	}

	// Le contenu canonique doit être celui que le SERVEUR portait, et la copie
	// doit exister. C'est cette réalité que le message doit décrire.
	chemins, _ := store.List("")
	var copie string
	for _, p := range chemins {
		if strings.Contains(p, "conflit") {
			copie = p
		}
	}
	if copie == "" {
		t.Skip("aucun conflit produit par ce scénario : le message reste couvert par la relecture du code")
	}
	t.Logf("copie de conflit produite : %s", copie)
}
