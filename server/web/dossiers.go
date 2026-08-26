// Les dossiers : l'accueil (ce à quoi j'ai accès), la navigation, la lecture
// d'un fichier, et le réglage de l'accès à l'endroit où il agit.
//
// « Espace » et « fichier » étaient deux écrans pour un seul objet, et la
// distinction n'était claire pour personne. Il n'en reste qu'un : le dossier.
// Un espace est simplement un dossier de premier niveau, parce que c'est un
// vrai point de montage sur le disque de chaque membre.
//
// Tout est filtré au périmètre de l'appelant (perms.CanRead) : le web n'expose
// jamais plus que ce que l'API de sync renverrait.
package web

import (
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
)

// visibleFiles : la liste des fichiers de main lisibles par l'utilisateur.
// Duplique volontairement le filtrage de api.handleTree (15 lignes) : toute
// évolution du filtrage doit être reportée des deux côtés.
func (s *Server) visibleFiles(u *db.User) ([]string, error) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		return nil, err
	}
	all, err := s.Store.List("")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range all {
		if perms.CanRead(f, u.DefaultLevel, rules) {
			out = append(out, f)
		}
	}
	return out, nil
}

// hrefAdmin : URL `base/p`, chaque segment de p échappé (« # », « ? » ou
// « %xx » littéral dans un nom de fichier casseraient le lien sinon -
// l'échappeur d'attribut de html/template les laisse passer).
func hrefAdmin(base, p string) string {
	if p == "" {
		return base
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + "/" + strings.Join(segs, "/")
}

func hrefDossiers(p string) string { return hrefAdmin("/admin/dossiers", p) }

type segmentVM struct{ Nom, Href string }

// fil : le fil d'Ariane d'un chemin (chaque segment cliquable).
func fil(p string) []segmentVM {
	if p == "" {
		return nil
	}
	var out []segmentVM
	cur := ""
	for _, seg := range strings.Split(p, "/") {
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		out = append(out, segmentVM{Nom: seg, Href: hrefDossiers(cur)})
	}
	return out
}

type entreeVM struct {
	Nom     string
	Href    string
	Dossier bool
	// Niveau : droit effectif de l'appelant sur cette entrée, et Direct dit si
	// une règle est posée dessus. Les deux servent à montrer la surcharge À
	// L'ENDROIT OÙ ELLE AGIT, au lieu d'un avertissement en bas d'un autre
	// écran. Le modèle autorise une règle à n'importe quelle profondeur, dans
	// les deux sens : sans ce repère, une règle profonde est invisible.
	Niveau string
	Direct bool
}

// children : entrées directes (dossiers puis fichiers) d'un dossier, dérivées
// de la liste des fichiers visibles. Un dossier n'apparaît que s'il contient
// au moins un fichier visible : un dossier invisible avec une surcharge
// lecture sur un fichier précis reste navigable jusqu'à ce fichier.
func children(visible []string, dir string, defaut perms.Level, rules []perms.Rule) []entreeVM {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	directes := map[string]bool{}
	for _, ru := range rules {
		directes[perms.Canon(ru.Path)] = true
	}
	seen := map[string]bool{}
	var dossiers, fichiers []entreeVM
	for _, f := range visible {
		if !strings.HasPrefix(f, prefix) {
			continue
		}
		rest := f[len(prefix):]
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			nom := rest[:i]
			if seen[nom] {
				continue
			}
			seen[nom] = true
			chemin := prefix + nom
			dossiers = append(dossiers, entreeVM{
				Nom: nom, Href: hrefDossiers(chemin), Dossier: true,
				Niveau: niveauFR(perms.Effective(chemin, defaut, rules)), Direct: directes[chemin],
			})
		} else {
			fichiers = append(fichiers, entreeVM{
				Nom: rest, Href: hrefDossiers(f),
				Niveau: niveauFR(perms.Effective(f, defaut, rules)), Direct: directes[f],
			})
		}
	}
	sort.Slice(dossiers, func(i, j int) bool { return dossiers[i].Nom < dossiers[j].Nom })
	sort.Slice(fichiers, func(i, j int) bool { return fichiers[i].Nom < fichiers[j].Nom })
	return append(dossiers, fichiers...)
}

// ---------------------------------------------------------------------------
// Les copies de conflit
// ---------------------------------------------------------------------------

type conflitVM struct{ Path, Href string }

// reConflit : le motif exact des copies forgées par api.conflictName,
// ancré sur le nom de fichier (pas le chemin : un dossier nommé
// « x (conflit y) » ou une note « réunion (conflit avec Marc).md » ne
// doivent pas matcher).
var reConflit = regexp.MustCompile(`\(conflit \d{4}-\d{2}-\d{2} \d{2}h\d{2} - [^/)]+\)`)

// conflitsVisibles : les copies de conflit du périmètre de l'appelant.
//
// L'onglet dédié a disparu au profit d'un bandeau sur l'accueil, affiché
// seulement s'il y a quelque chose. Motif : depuis que les copies LOCALES ne
// remontent plus au serveur (v0.7.2), l'écran était vide en permanence - un
// onglet qui ne dit jamais rien apprend à ne plus être regardé. La détection,
// elle, garde son sens : storage.WriteMerge en fabrique toujours quand le
// serveur ne peut pas fusionner.
func conflitsVisibles(visible []string) []conflitVM {
	var out []conflitVM
	for _, f := range visible {
		if reConflit.MatchString(path.Base(f)) {
			out = append(out, conflitVM{Path: f, Href: hrefDossiers(f)})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// L'accueil : les dossiers auxquels j'ai accès
// ---------------------------------------------------------------------------

// membreVM : un compte et son droit effectif sur un chemin donné.
type membreVM struct {
	UserID   int64
	Username string
	IsAdmin  bool
	Niveau   string // forme stockage : compare directement aux valeurs des boutons
	// Profondes : règles posées À L'INTÉRIEUR du chemin pour ce compte. Sans
	// cette information le panneau ment - les boutons agissent sur ce dossier,
	// mais une règle plus profonde l'emporte dans le calcul du droit effectif.
	// Cliquer « invisible » serait alors un geste sans effet, et
	// l'administrateur croirait avoir coupé un accès resté ouvert.
	Profondes []string
}

type dossierVM struct {
	Nom      string
	Libelle  string
	Href     string
	Fichiers int
	Niveau   string
	Membres  []membreVM
}

type accueilData struct {
	baseData
	Dossiers []dossierVM
	Conflits []conflitVM
	Niveaux  []niveauVM
	Erreur   string
}

// cohorteChoixVM : un groupe, et si le dossier courant y est rangé. Servi aux
// seuls administrateurs, comme la liste des membres : « qui a accès à quoi »
// est une information de gouvernance.
type cohorteChoixVM struct {
	ID     int64
	Nom    string
	Dedans bool
}

// membresDe : le droit effectif de chaque compte sur un chemin.
//
// **Réservé aux administrateurs, et l'appelant en porte la responsabilité.**
// Savoir qui a accès à quoi est une information de gouvernance : la donner à
// tout le monde apprendrait à un membre l'existence de comptes et de périmètres
// qui ne le regardent pas. C'est la règle qui valait déjà pour la matrice des
// espaces, conservée telle quelle en changeant d'écran.
func (s *Server) membresDe(chemin string) ([]membreVM, error) {
	users, err := s.DB.ListUsers()
	if err != nil {
		return nil, err
	}
	out := make([]membreVM, 0, len(users))
	for _, x := range users {
		rules, err := s.DB.Rules(x.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, membreVM{
			UserID: x.ID, Username: x.Username, IsAdmin: x.IsAdmin,
			Niveau:    perms.Effective(chemin, x.DefaultLevel, rules).String(),
			Profondes: reglesInternes(chemin, rules),
		})
	}
	return out, nil
}

// cohortesDe : les groupes, et lesquels portent ce dossier.
//
// Le genre est demandé explicitement : une ligne rangée comme SKILL sur le même
// chemin ne doit pas se cocher ici, sinon décocher la supprimerait et les deux
// écrans se contrediraient.
func (s *Server) cohortesDe(chemin string) ([]cohorteChoixVM, error) {
	groupes, err := s.DB.ListGroupes()
	if err != nil {
		return nil, err
	}
	ids, err := s.DB.GroupesDuChemin(chemin, "dossier")
	if err != nil {
		return nil, err
	}
	dedans := make(map[int64]bool, len(ids))
	for _, id := range ids {
		dedans[id] = true
	}
	out := make([]cohorteChoixVM, 0, len(groupes))
	for _, g := range groupes {
		out = append(out, cohorteChoixVM{ID: g.ID, Nom: g.Nom, Dedans: dedans[g.ID]})
	}
	return out, nil
}

func (s *Server) renderAccueil(w http.ResponseWriter, status int, u *db.User, erreur string) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	fichiers, err := s.Store.List("")
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	visible, err := s.visibleFiles(u)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	data := accueilData{
		baseData: base(u, "dossiers"), Niveaux: niveauxVM,
		Conflits: conflitsVisibles(visible), Erreur: erreur,
	}
	for _, e := range espaces.List(fichiers, u.DefaultLevel, rules) {
		vm := dossierVM{
			Nom: e.Nom, Href: hrefDossiers(e.Nom), Fichiers: e.Fichiers,
			// Libellé vide = pas de libellé : le template montre alors le nom
			// technique seul (DT3), au lieu de le répéter deux fois.
			Niveau: niveauFR(e.Niveau), Libelle: espaces.LitLibelle(s.Store, e.Nom),
		}
		if u.IsAdmin {
			membres, err := s.membresDe(e.Nom)
			if err != nil {
				erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
				return
			}
			vm.Membres = membres
		}
		data.Dossiers = append(data.Dossiers, vm)
	}
	render(w, status, "accueil", data)
}

// GET /admin/ : les dossiers auxquels l'appelant a accès.
func (s *Server) handleAccueil(w http.ResponseWriter, r *http.Request, u *db.User) {
	s.renderAccueil(w, http.StatusOK, u, "")
}

// ---------------------------------------------------------------------------
// Un dossier ouvert, ou un fichier
// ---------------------------------------------------------------------------

type dossiersData struct {
	baseData
	Chemin  string
	Fil     []segmentVM
	Entrees []entreeVM
	Niveau  string
	// Membres et Niveaux : le panneau d'accès, servi aux seuls administrateurs
	// (voir membresDe). Vide pour les autres, et le template n'affiche alors
	// rien plutôt qu'un panneau désactivé.
	Membres []membreVM
	Niveaux []niveauVM
	// Cohortes : les groupes, et lesquels portent ce dossier. Même réserve que
	// Membres - servi aux seuls administrateurs.
	Cohortes []cohorteChoixVM
	// Confirme : un geste de cohorte ou de défaut vient d'aboutir.
	Confirme bool
	// Defaut : le niveau par défaut porté par CE dossier, et s'il en porte un.
	Defaut  string
	ADefaut bool
	// Deverrouille : le geste qui vient d'aboutir aurait fermé ce dossier à son
	// auteur, et lui a donc écrit une exception. À dire, sinon il croit avoir
	// posé une règle qui vaut aussi pour lui.
	Deverrouille bool
	Gestion      bool
}

type fichierData struct {
	baseData
	Chemin         string
	Fil            []segmentVM
	HrefHistorique string
	Contenu        string
}

// GET /admin/dossiers[/{path...}] : listing d'un dossier ou contenu d'un
// fichier. Un chemin hors périmètre est un 404 (comme l'API : on ne révèle
// pas l'existence d'un contenu invisible).
func (s *Server) handleDossiers(w http.ResponseWriter, r *http.Request, u *db.User) {
	p := perms.Canon(r.PathValue("path"))
	visible, err := s.visibleFiles(u)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	// Fichier exact ?
	for _, f := range visible {
		if f == p {
			contenu, err := s.Store.Read(p, "")
			if err != nil {
				erreurHTTP(w, "introuvable", http.StatusNotFound)
				return
			}
			render(w, http.StatusOK, "fichier", fichierData{
				baseData: base(u, "dossiers"), Chemin: p, Fil: fil(p),
				HrefHistorique: hrefAdmin("/admin/historique", p), Contenu: contenu,
			})
			return
		}
	}

	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	// Dossier (la racine existe toujours, même vide) ?
	entrees := children(visible, p, u.DefaultLevel, rules)
	if p != "" && len(entrees) == 0 {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}
	data := dossiersData{
		baseData: base(u, "dossiers"), Chemin: p, Fil: fil(p), Entrees: entrees,
		Niveau:  niveauFR(perms.Effective(p, u.DefaultLevel, rules)),
		Gestion: u.IsAdmin, Niveaux: niveauxVM,
		Confirme: u.IsAdmin && r.URL.Query().Has("okc"),
	}
	// L'accès se règle sur un DOSSIER, jamais à la racine du dépôt : une règle
	// posée sur "" est le niveau par défaut du compte, qui se change sur sa
	// fiche et pas ici.
	if u.IsAdmin && p != "" {
		membres, err := s.membresDe(p)
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		data.Membres = membres
		if data.Cohortes, err = s.cohortesDe(p); err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		niveau, pose, err := s.DB.DefautDossier(p)
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		data.ADefaut, data.Defaut = pose, niveau.String()
		data.Deverrouille = r.URL.Query().Has("okd")
	}
	render(w, http.StatusOK, "dossiers", data)
}

// ---------------------------------------------------------------------------
// Régler l'accès à un dossier
//
// CRÉER un dossier ne se fait plus ici : le web le posait à la racine Vécu sur
// le poste de chaque membre, alors que la ligne du 21/08 donne le local à l'app.
// Le geste vit dans « Partager un dossier… » (parcours A), qui ne déplace rien,
// et dans `POST /espaces` de l'API que l'app appelle. `espaces.Create` reste :
// c'est cette voie-là qui s'en sert.
// ---------------------------------------------------------------------------

// POST /admin/dossiers/acces : pose le droit d'un compte sur un chemin.
//
// Généralise l'ancien POST /admin/espaces/acces, qui n'acceptait qu'un nom de
// premier niveau. Le modèle autorise une règle à n'importe quelle profondeur,
// dans les deux sens ; ce handler est la surface qui manquait pour l'exercer
// sans taper un chemin à la main dans la fiche d'un compte.
//
// La redirection est bornée à des destinations que le code fabrique (jamais
// une URL reçue du formulaire) : le dossier concerné, ou la fiche du compte
// d'où vient le clic.
func (s *Server) handleSetAcces(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	chemin := perms.Canon(r.PostFormValue("chemin"))
	niveau, ok := perms.ParseLevel(r.PostFormValue("niveau"))
	userID, errID := strconv.ParseInt(r.PostFormValue("user_id"), 10, 64)
	// espaces.ValidChemin refuse la racine du dépôt et les chemins dont le
	// premier segment ne peut pas devenir un dossier sur le poste d'un membre.
	// Il exige un fichier DANS un dossier, d'où le nom seul traité à part.
	valide := espaces.ValidNom(chemin) == nil || espaces.ValidChemin(chemin) == nil
	if !valide || !ok || errID != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "chemin, utilisateur ou niveau invalide")
		return
	}
	if _, err := s.DB.UserByID(userID); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "utilisateur introuvable")
		return
	}
	if err := s.DB.SetPermission(userID, chemin, niveau); err != nil {
		log.Printf("web: accès dossier : %v", err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	if r.PostFormValue("retour") == "user" {
		http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(userID, 10), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, hrefDossiers(chemin), http.StatusSeeOther)
}

// POST /admin/dossiers/cohortes : régler l'ensemble des cohortes d'un dossier.
//
// UNE SEULE SURFACE AUTORITAIRE PAR ENSEMBLE. Les cohortes d'un dossier se
// règlent ICI et nulle part ailleurs ; la vue d'un groupe les liste en lecture,
// avec un lien vers le dossier. Deux écrans qui prétendent tous les deux « ce
// que je montre fait foi » se marchent dessus dès que l'un est resté ouvert
// pendant que l'autre enregistrait, et le second écrase le premier sans rien
// signaler.
//
// Réservé aux administrateurs par sa route (« un membre ne fabrique pas de
// périmètre », 21/08). Le geste du créateur, lui, ne concerne que les skills.
func (s *Server) handleSetCohortes(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	chemin := perms.Canon(r.PostFormValue("chemin"))
	// Même validation que POST /admin/dossiers/acces : un chemin qui ne peut pas
	// devenir un dossier sur le poste d'un membre n'a rien à faire dans une
	// cohorte non plus.
	if espaces.ValidNom(chemin) != nil && espaces.ValidChemin(chemin) != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "chemin invalide")
		return
	}
	groupes, err := s.DB.ListGroupes()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	connus := map[int64]bool{}
	for _, g := range groupes {
		connus[g.ID] = true
	}
	voulus := map[int64]bool{}
	for _, brut := range r.PostForm["groupe_id"] {
		brut = strings.TrimSpace(brut)
		if brut == "" {
			continue
		}
		id, err := strconv.ParseInt(brut, 10, 64)
		if err != nil || !connus[id] {
			s.renderAccueil(w, http.StatusBadRequest, u, "groupe introuvable")
			return
		}
		voulus[id] = true
	}

	actuels, err := s.DB.GroupesDuChemin(chemin, "dossier")
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	// ON N'ÉCRIT QUE CE QUI CHANGE, et dans un ordre trié : sur échec au milieu,
	// ce qui a survécu ne dépend pas de l'ordre de parcours d'une map.
	dedans := map[int64]bool{}
	for _, id := range actuels {
		dedans[id] = true
		if !voulus[id] {
			if err := s.DB.SortChemin(id, chemin); err != nil {
				log.Printf("web: sortie de %s du groupe %d : %v", chemin, id, err)
				erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
				return
			}
		}
	}
	ajouts := make([]int64, 0, len(voulus))
	for id := range voulus {
		if !dedans[id] {
			ajouts = append(ajouts, id)
		}
	}
	sort.Slice(ajouts, func(i, j int) bool { return ajouts[i] < ajouts[j] })
	for _, id := range ajouts {
		if err := s.DB.RangeChemin(id, chemin, "dossier", ""); err != nil {
			log.Printf("web: rangement de %s dans le groupe %d : %v", chemin, id, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, hrefDossiers(chemin)+"?okc=1", http.StatusSeeOther)
}

// POST /admin/dossiers/defaut : poser ou retirer le niveau par défaut d'un
// dossier.
//
// C'EST LE MÉCANISME QUI FERME. Une cohorte ouvre et ne ferme rien qu'une autre
// ouvre ; sans ce défaut, protéger un dossier sensible demanderait une règle
// individuelle par compte, à rejouer à chaque embauche. Une ligne ici vaut pour
// tout le monde, y compris pour les comptes qui n'existent pas encore.
//
// Réservé aux administrateurs par sa route.
func (s *Server) handleSetDefautDossier(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	chemin := perms.Canon(r.PostFormValue("chemin"))
	if espaces.ValidNom(chemin) != nil && espaces.ValidChemin(chemin) != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "chemin invalide")
		return
	}

	// LE GARDE ANTI-AUTO-ENFERMEMENT, mesuré AVANT le geste.
	//
	// `visibleFiles` s'appuie sur les règles de l'administrateur lui-même :
	// fermer un dossier le ferme aussi pour lui, et il perd du même coup l'écran
	// depuis lequel il pourrait revenir en arrière. On relève donc son niveau
	// effectif d'avant, et on le lui rend en exception s'il allait tomber sous
	// la lecture.
	//
	// POUR LUI SEUL. Écrire une règle pour des comptes que le geste ne visait
	// pas les détacherait de leur défaut : invisible le jour même, faux pour
	// toujours après (leçon du 21/08).
	avant, err := s.DB.Effective(u, chemin)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	if r.PostFormValue("retirer") != "" {
		if err := s.DB.SupprimeDefautDossier(chemin); err != nil {
			log.Printf("web: retrait du défaut de %s : %v", chemin, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, hrefDossiers(chemin)+"?okc=1", http.StatusSeeOther)
		return
	}

	brut := r.PostFormValue("niveau")
	// La valeur vide est l'option « aucun » du formulaire : elle RETIRE le
	// défaut au lieu d'en poser un. Sans ce cas, choisir « aucun » tomberait en
	// « niveau invalide » et l'écran refuserait un geste qu'il propose.
	if brut == "" {
		if err := s.DB.SupprimeDefautDossier(chemin); err != nil {
			log.Printf("web: retrait du défaut de %s : %v", chemin, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, hrefDossiers(chemin)+"?okc=1", http.StatusSeeOther)
		return
	}
	niveau, ok := perms.ParseLevel(brut)
	if !ok {
		s.renderAccueil(w, http.StatusBadRequest, u, "niveau invalide")
		return
	}
	if err := s.DB.SetDefautDossier(chemin, niveau); err != nil {
		log.Printf("web: défaut de %s : %v", chemin, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	apres, err := s.DB.Effective(u, chemin)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	suffixe := "?okc=1"
	if avant >= perms.Lecture && apres < perms.Lecture {
		if err := s.DB.SetPermission(u.ID, chemin, avant); err != nil {
			log.Printf("web: exception anti-enfermement sur %s : %v", chemin, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		suffixe = "?okc=1&okd=1"
	}
	http.Redirect(w, r, hrefDossiers(chemin)+suffixe, http.StatusSeeOther)
}
