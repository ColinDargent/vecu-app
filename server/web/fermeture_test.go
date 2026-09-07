package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
)

// DAR-204. Jusqu'ici Vécu ne savait fermer aucun compte : ni fonction dans
// `server/db`, ni route, ni commande. Un client aura pourtant des comptes à
// fermer - un départ, une erreur de saisie, un prestataire dont la mission
// s'arrête - et le mainteneur rencontre le geste avant nous.
func TestFermerUnCompteRetireSesAccesEtSesJetons(t *testing.T) {
	s := newServer(t)
	admin, err := s.DB.CreateUser("patronne", "mdp", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	partant, err := s.DB.CreateUser("prestataire", "mdp", perms.Lecture, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.EnsurePermission(partant.ID, "equipe", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	jeton, err := s.DB.IssueToken(partant.ID, "son mac")
	if err != nil {
		t.Fatal(err)
	}
	// Précondition : le jeton ouvre bien quelque chose. Sans elle, le test
	// d'après ne prouverait rien - un jeton qui n'a jamais marché ne peut pas
	// cesser de marcher.
	if _, err := s.DB.UserByToken(jeton); err != nil {
		t.Fatalf("précondition : le jeton du partant n'ouvre rien : %v", err)
	}

	h := s.Handler()
	c := login(t, h, "patronne", "mdp")
	chemin := "/admin/users/" + strconv.FormatInt(partant.ID, 10) + "/fermer"
	rec := postForm(h, chemin, url.Values{"confirmation": {"prestataire"}}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("fermeture : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}

	if _, err := s.DB.UserByID(partant.ID); err == nil {
		t.Error("le compte existe encore après sa fermeture")
	}
	// Ce que la cascade doit avoir emporté. `ON DELETE CASCADE` était déjà écrit
	// sur ces tables ; ce lot ne l'ajoute pas, il le rend atteignable.
	if _, err := s.DB.UserByToken(jeton); err == nil {
		t.Error("le jeton d'appareil d'un compte fermé ouvre encore une session")
	}
	// Et l'administratrice, elle, n'a pas été emportée au passage.
	if _, err := s.DB.UserByID(admin.ID); err != nil {
		t.Errorf("l'administratrice a disparu : %v", err)
	}
}

// La confirmation par le nom, et pas une case à cocher : le geste part d'une
// liste, la ligne est au milieu d'un tableau, et une case se coche par réflexe.
// Retaper un nom demande de regarder LEQUEL on ferme.
func TestFermetureRefuseUneConfirmationQuiNeCorrespondPas(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("patronne", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	cible, err := s.DB.CreateUser("alix", "mdp", perms.Lecture, false)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "patronne", "mdp")
	chemin := "/admin/users/" + strconv.FormatInt(cible.ID, 10) + "/fermer"

	for _, saisie := range []string{"", "alixx", "patronne"} {
		rec := postForm(h, chemin, url.Values{"confirmation": {saisie}}, c)
		if rec.Code == http.StatusSeeOther {
			t.Errorf("confirmation %q acceptée alors qu'elle ne correspond pas", saisie)
		}
		if _, err := s.DB.UserByID(cible.ID); err != nil {
			t.Fatalf("le compte a été fermé sur la confirmation %q : %v", saisie, err)
		}
	}
}

// LA garde d'enfermement : fermer son propre compte détruit la session avec
// laquelle on agit, et si c'était le dernier administrateur, l'instance devient
// inadministrable dans le même clic.
//
// Ce test a servi à retirer du code : il montrait aussi qu'une seconde garde,
// « pas le dernier administrateur », ne pouvait jamais tirer - le middleware
// admin plus ce refus-ci garantissent qu'il reste toujours un administrateur
// après une fermeture acceptée. Elle est partie.
func TestFermetureRefuseLAutoFermeture(t *testing.T) {
	s := newServer(t)
	seule, err := s.DB.CreateUser("patronne", "mdp", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "patronne", "mdp")

	chemin := "/admin/users/" + strconv.FormatInt(seule.ID, 10) + "/fermer"
	if rec := postForm(h, chemin, url.Values{"confirmation": {"patronne"}}, c); rec.Code == http.StatusSeeOther {
		t.Error("un compte a pu se fermer lui-même")
	}
	if _, err := s.DB.UserByID(seule.ID); err != nil {
		t.Fatal("le compte s'est fermé lui-même")
	}

	// Un second administrateur n'y change rien : c'est « moi-même » qui est
	// refusé, pas « le dernier ».
	autre, err := s.DB.CreateUser("second", "mdp", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postForm(h, chemin, url.Values{"confirmation": {"patronne"}}, c); rec.Code == http.StatusSeeOther {
		t.Error("un compte a pu se fermer lui-même alors qu'un autre admin existe")
	}

	// Et fermer UN AUTRE administrateur reste permis : c'est un geste ordinaire
	// d'administration, et il en reste toujours un - celui qui clique.
	cheminAutre := "/admin/users/" + strconv.FormatInt(autre.ID, 10) + "/fermer"
	if rec := postForm(h, cheminAutre, url.Values{"confirmation": {"second"}}, c); rec.Code != http.StatusSeeOther {
		t.Errorf("fermer un autre administrateur a été refusé : %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestFermetureRefuseUnNonAdmin(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("patronne", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	membre, err := s.DB.CreateUser("membre", "mdp", perms.Lecture, false)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "membre", "mdp")
	chemin := "/admin/users/" + strconv.FormatInt(membre.ID, 10) + "/fermer"
	if rec := postForm(h, chemin, url.Values{"confirmation": {"membre"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("un membre a atteint la fermeture de compte : %d", rec.Code)
	}
}

// L'autre porte manquante : `POST /espaces` créait, rien ne supprimait.
func TestSupprimerUnEspaceVide(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("patronne", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if err := espaces.Create(s.Store, "jetable", "patronne"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "patronne", "mdp")

	form := url.Values{"espace": {"jetable"}, "confirmation": {"jetable"}}
	if rec := postForm(h, "/admin/dossiers/espaces/supprimer", form, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("suppression : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	occupation, err := espaces.Occupation(s.Store, "jetable")
	if err != nil {
		t.Fatal(err)
	}
	if occupation != espaces.Libre {
		t.Errorf("l'espace existe encore après sa suppression : %v", occupation)
	}
}

// LE refus qui porte ce lot. Retirer le fichier de métadonnées d'un espace qui
// porte encore des fichiers ne les supprime pas : il les rend ORPHELINS, dans un
// état que le client sait nommer (« chemin hors espace côté serveur ») et dont
// aucun poste ne peut les faire redescendre.
//
// C'est la mutation à tuer sur cette porte.
func TestSupprimerUnEspaceNonVideEstRefuse(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("patronne", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if err := espaces.Create(s.Store, "vivant", "patronne"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("vivant/note.md", "du travail\n", "patronne"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "patronne", "mdp")

	form := url.Values{"espace": {"vivant"}, "confirmation": {"vivant"}}
	rec := postForm(h, "/admin/dossiers/espaces/supprimer", form, c)
	if rec.Code == http.StatusSeeOther {
		t.Fatal("un espace non vide a été supprimé, son contenu est devenu orphelin")
	}
	// Le refus doit NOMMER ce qui reste : « videz-le d'abord » sans dire quoi
	// vider envoie chercher dans un dossier qu'on ne voit pas depuis cet écran.
	if corps := rec.Body.String(); !strings.Contains(corps, "note.md") {
		t.Errorf("le refus ne nomme pas le fichier restant ; corps : %s", corps)
	}
	// Et le contenu est intact.
	if _, err := s.Store.Read("vivant/note.md", ""); err != nil {
		t.Errorf("le contenu a été touché par une suppression refusée : %v", err)
	}
}

// L'icone de niveau doit etre BORNEE sur toute page qui l'emet, pas seulement
// sur celle dont le `<style>` la dimensionnait.
//
// Mesure du 02/09 : sur `/admin/cohortes`, les trois icones s'affichaient a
// pleine largeur. Le gabarit `icone` emet un `<svg>` avec un `viewBox` et sans
// `width` ni `height` ; sa taille vivait dans le bloc `<style>` de
// `skills.html`. L'ecran des cohortes reutilise les memes classes sans cette
// feuille, donc le SVG retombait sur la taille par defaut d'un element remplace
// (300x150), mise a l'echelle du viewBox.
//
// Le test porte sur les DEUX pages qui emettent l'icone : c'est la
// generalisation qui manquait, pas le cas particulier.
func TestIconeDeNiveauEstBorneeSurToutesLesPages(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("patronne", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.CreerGroupe("Test"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "patronne", "mdp")

	// La REGLE vient du gabarit commun, donc elle est sur toute page - y compris
	// celles qui n'emettent aucune icone aujourd'hui et en emettront demain.
	for _, chemin := range []string{"/admin/cohortes", "/admin/skills", "/admin/users", "/admin/"} {
		if page := get(h, chemin, c).Body.String(); !strings.Contains(page, "svg.icone {") {
			t.Errorf("%s : aucune regle de taille pour l'icone, elle s'afficherait a pleine largeur", chemin)
		}
	}

	// Et l'ICONE porte bien sa classe, la ou le defaut a ete mesure. Sans cette
	// moitie, retirer la classe du gabarit passerait : la regle serait toujours
	// la, elle n'atteindrait plus rien.
	page := get(h, "/admin/cohortes", c).Body.String()
	if !strings.Contains(page, `<svg class="icone"`) {
		t.Fatal("/admin/cohortes : l'icone n'emet plus sa classe, la regle ne peut plus l'atteindre")
	}
	if strings.Contains(page, "<svg viewBox=") {
		t.Error("/admin/cohortes : un SVG sans classe subsiste, il s'affichera a pleine largeur")
	}
}

// L'écran d'état doit dire POURQUOI il ne dit rien.
//
// 02/09, Colin : « je ne comprends pas la section État des fichiers ». Il la
// regardait dans l'état où aucun poste ne rapporte - toutes les colonnes en
// « sans nouvelles » - et une grille vide se lit comme une panne quand rien
// n'explique le vide. La cause est nommable : le canal est arrivé côté serveur
// le 01/09, les clients installés lui sont antérieurs.
func TestLEcranDEtatDitPourquoiIlNeDitRien(t *testing.T) {
	s := newServer(t)
	u, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	if err != nil {
		t.Fatal(err)
	}
	// Un poste connu qui n'a jamais rapporté : exactement l'état de production.
	if _, err := s.DB.IssueToken(u.ID, "le mac de Colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	page := get(h, "/admin/etat", c).Body.String()

	if !strings.Contains(page, "ne rapporte encore son état") {
		t.Error("l'écran ne dit pas qu'aucun poste ne rapporte, alors qu'aucun ne rapporte")
	}
	// Et la question à laquelle il répond est écrite, sinon la matrice reste
	// une matrice.
	if !strings.Contains(page, "est bien arrivé chez tout le") {
		t.Error("l'écran ne dit pas quelle question il tranche")
	}
	// « on ne sait pas » ne doit jamais se lire « non » : c'est l'invariant du
	// lot, et l'écran doit le porter en toutes lettres.
	if !strings.Contains(page, "n'est jamais") {
		t.Error("l'écran ne distingue pas l'absence de nouvelles d'une réponse négative")
	}
}

// La TROISIÈME porte de DAR-204 : supprimer un fichier depuis le web.
func TestSupprimerUnFichierDepuisLeWeb(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("equipe/jetable.md", "au revoir\n", "colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// La porte est proposée sur l'écran du fichier, et elle dit ce qu'elle fait.
	page := get(h, "/admin/dossiers/equipe/jetable.md", c).Body.String()
	if !strings.Contains(page, `action="/admin/fichier/supprimer"`) {
		t.Error("aucune porte de suppression sur l'écran d'un fichier")
	}
	if !strings.Contains(page, "de tous les postes") {
		t.Error("l'écran ne dit pas que la suppression atteint tous les postes")
	}

	form := url.Values{"chemin": {"equipe/jetable.md"}, "confirmation": {"jetable.md"}}
	if rec := postForm(h, "/admin/fichier/supprimer", form, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("suppression : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := s.Store.Read("equipe/jetable.md", ""); err == nil {
		t.Error("le fichier existe encore après sa suppression")
	}
}

// LE REFUS QUI PORTE CETTE PORTE, et il rattrape un trou que la porte
// rouvrirait sinon par derrière.
//
// Le fichier de métadonnées fait EXISTER un espace. Le retirer par ici ferait
// cesser l'espace d'exister sans supprimer son contenu, qui deviendrait
// orphelin - exactement ce que `espaces.Supprimer` refuse depuis ce matin en
// exigeant un espace vide. Le ticket DAR-204 nomme d'ailleurs ce détour comme
// le contournement connu.
//
// C'est la mutation à tuer sur ce lot.
func TestLeFichierQuiFaitExisterUnEspaceNeSeSupprimePasIci(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if err := espaces.Create(s.Store, "vivant", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("vivant/note.md", "du travail\n", "colin"); err != nil {
		t.Fatal(err)
	}
	meta := "vivant/" + espaces.FichierMeta
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	// Ni la porte à l'écran…
	page := get(h, "/admin/dossiers/"+meta, c).Body.String()
	if strings.Contains(page, `action="/admin/fichier/supprimer"`) {
		t.Error("la porte est proposée sur le fichier qui fait exister l'espace")
	}
	// …ni la route, qui est la seule des deux qui compte vraiment.
	form := url.Values{"chemin": {meta}, "confirmation": {espaces.FichierMeta}}
	rec := postForm(h, "/admin/fichier/supprimer", form, c)
	if rec.Code == http.StatusSeeOther {
		t.Fatal("l'espace a été défait par la porte des fichiers : son contenu est devenu orphelin")
	}
	if _, err := s.Store.Read(meta, ""); err != nil {
		t.Errorf("le fichier de métadonnées a été touché par un refus : %v", err)
	}
}

// Un compte en lecture seule ne supprime rien, et ne se voit pas proposer la porte.
func TestLaSuppressionDUnFichierExigeLEcriture(t *testing.T) {
	s := newServer(t)
	if _, err := s.DB.CreateUser("colin", "mdp", perms.Ecriture, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.CreateUser("lecteur", "mdp", perms.Lecture, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("equipe/note.md", "contenu\n", "colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "lecteur", "mdp")

	if page := get(h, "/admin/dossiers/equipe/note.md", c).Body.String(); strings.Contains(page, `action="/admin/fichier/supprimer"`) {
		t.Error("un compte en lecture seule se voit proposer la suppression")
	}
	form := url.Values{"chemin": {"equipe/note.md"}, "confirmation": {"note.md"}}
	if rec := postForm(h, "/admin/fichier/supprimer", form, c); rec.Code != http.StatusForbidden {
		t.Errorf("un compte en lecture seule a atteint la suppression : %d", rec.Code)
	}
	if _, err := s.Store.Read("equipe/note.md", ""); err != nil {
		t.Errorf("le fichier a été supprimé par un compte en lecture seule : %v", err)
	}
}
