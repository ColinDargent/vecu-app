// Pages d'administration des comptes et des droits (réservées IsAdmin).
// La vue arbre est dérivée de l'arborescence courante de main : chaque dossier
// est annoté du droit effectif de l'utilisateur cible (perms.Effective).
package web

import (
	"errors"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
)

// admin : comme session, mais réservé aux comptes IsAdmin.
func (s *Server) admin(next func(http.ResponseWriter, *http.Request, *db.User)) http.HandlerFunc {
	return s.session(func(w http.ResponseWriter, r *http.Request, u *db.User) {
		if !u.IsAdmin {
			http.Error(w, "réservé aux administrateurs", http.StatusForbidden)
			return
		}
		next(w, r, u)
	})
}

// niveauFR : libellé d'affichage accentué d'un niveau.
func niveauFR(l perms.Level) string {
	if l == perms.Ecriture {
		return "écriture"
	}
	return l.String() // invisible, lecture : déjà corrects
}

type userVM struct {
	ID       int64
	Username string
	Niveau   string
	IsAdmin  bool
}

type usersData struct {
	baseData
	Users   []userVM
	Niveaux []niveauVM
	Erreur  string
}

func (s *Server) renderUsers(w http.ResponseWriter, status int, u *db.User, erreur string) {
	list, err := s.DB.ListUsers()
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	data := usersData{baseData: base(u, "utilisateurs"), Niveaux: niveauxVM, Erreur: erreur}
	for _, x := range list {
		data.Users = append(data.Users, userVM{ID: x.ID, Username: x.Username, Niveau: niveauFR(x.DefaultLevel), IsAdmin: x.IsAdmin})
	}
	render(w, status, "users", data)
}

// GET /admin/users : liste des comptes + formulaire de création.
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request, u *db.User) {
	s.renderUsers(w, http.StatusOK, u, "")
}

// POST /admin/users : création d'un compte.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderUsers(w, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")
	niveau, ok := perms.ParseLevel(r.PostFormValue("niveau"))
	if password == "" || !ok {
		s.renderUsers(w, http.StatusBadRequest, u, "nom, mot de passe et niveau sont requis")
		return
	}
	if !db.ValidUsername(username) {
		s.renderUsers(w, http.StatusBadRequest, u,
			"nom d'utilisateur invalide : lettres, chiffres, « . _ - », 64 caractères max")
		return
	}
	// CreateUser pose atomiquement « skills privés par défaut » pour tout compte
	// (admin inclus) : rien à faire ici.
	if _, err := s.DB.CreateUser(username, password, niveau, r.PostFormValue("admin") == "on"); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.renderUsers(w, http.StatusBadRequest, u, "ce nom d'utilisateur est déjà pris")
			return
		}
		log.Printf("web: création utilisateur : %v", err)
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

type ruleVM struct {
	Path     string
	Niveau   string
	Inconnue bool // ne correspond à aucun chemin actuel du dépôt (typo ou dossier futur)
}

type nodeVM struct {
	Path       string
	Nom        string
	Niveau     string
	Profondeur int
	Direct     bool // une règle est posée exactement sur ce chemin
}

type droitsData struct {
	baseData
	Cible      userVM
	DefautBrut string // forme stockage du niveau par défaut (sélection du select)
	Niveaux    []niveauVM
	Regles     []ruleVM
	Arbre      []nodeVM
	Espaces    []espaceAccesVM // vue miroir de la matrice de /admin/espaces
	Skills     []skillAccesVM  // vue miroir de la matrice de /admin/skills
	Erreur     string
}

// niveauVM : une option de select (valeur stockage + libellé accentué).
// Source unique des niveaux proposés par l'UI.
type niveauVM struct{ Valeur, Libelle string }

var niveauxVM = []niveauVM{
	{"invisible", "invisible"},
	{"lecture", "lecture"},
	{"ecriture", "écriture"},
}

func (s *Server) renderDroits(w http.ResponseWriter, status int, u, cible *db.User, erreur string) {
	rules, err := s.DB.Rules(cible.ID)
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	files, err := s.Store.List("")
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	data := droitsData{
		baseData:   base(u, "utilisateurs"),
		Cible:      userVM{ID: cible.ID, Username: cible.Username, Niveau: niveauFR(cible.DefaultLevel), IsAdmin: cible.IsAdmin},
		DefautBrut: cible.DefaultLevel.String(),
		Niveaux:    niveauxVM,
		Erreur:     erreur,
	}
	// Chemins existants (dossiers + fichiers) : marque les règles « mortes ».
	// Membership sur TOUT le dépôt, jamais rendu : sert seulement à savoir si le
	// chemin d'une règle de la cible existe encore.
	existe := map[string]bool{}
	for _, f := range files {
		existe[f] = true
	}
	tousDirs := dirsOf(files)
	for _, d := range tousDirs {
		existe[d] = true
	}
	// L'arbre RENDU part du périmètre de lecture de l'admin appelant : la fiche
	// d'un compte n'énumère pas les chemins que cet admin n'a pas le droit de
	// lire. Même discipline que espacesDuDepot, qui part déjà de SES règles.
	adminRules, err := s.DB.Rules(u.ID)
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	dirs := make([]string, 0, len(tousDirs))
	for _, d := range tousDirs {
		if perms.CanRead(d, u.DefaultLevel, adminRules) {
			dirs = append(dirs, d)
		}
	}
	// Vue miroir : les espaces du dépôt (vus par l'admin) avec le droit effectif
	// de la cible sur chacun. Même geste que la matrice, dans l'autre sens.
	espacesList, err := s.espacesDuDepot(u)
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	for _, e := range espacesList {
		data.Espaces = append(data.Espaces, espaceAccesVM{
			Nom:       e.Nom,
			Niveau:    perms.Effective(e.Nom, cible.DefaultLevel, rules).String(),
			Profondes: reglesInternes(e.Nom, rules),
		})
	}
	// Miroir des skills : même geste que la matrice /admin/skills, dans l'autre sens.
	data.Skills, err = s.skillsMiroir(u, cible, rules)
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	direct := map[string]bool{}
	for _, ru := range rules {
		direct[ru.Path] = true
		data.Regles = append(data.Regles, ruleVM{Path: ru.Path, Niveau: niveauFR(ru.Level), Inconnue: !existe[ru.Path]})
	}
	for _, d := range dirs {
		data.Arbre = append(data.Arbre, nodeVM{
			Path:       d,
			Nom:        path.Base(d),
			Niveau:     niveauFR(perms.Effective(d, cible.DefaultLevel, rules)),
			Profondeur: strings.Count(d, "/"),
			Direct:     direct[perms.Canon(d)],
		})
	}
	render(w, status, "droits", data)
}

// dirsOf : ensemble trié des dossiers présents dans une liste de fichiers.
func dirsOf(files []string) []string {
	set := map[string]bool{}
	for _, f := range files {
		for d := path.Dir(f); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			set[d] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	// Tri hiérarchique : « / » remplacé par \x00 pour que les enfants d'un
	// dossier restent adjacents à leur parent (« clients-old » ne s'intercale
	// pas entre « clients » et « clients/vdf »).
	sort.Slice(out, func(i, j int) bool {
		return strings.ReplaceAll(out[i], "/", "\x00") < strings.ReplaceAll(out[j], "/", "\x00")
	})
	return out
}

// validChemin : un chemin de règle acceptable depuis un formulaire.
// Canon a déjà nettoyé ; on rejette l'échappement racine et les caractères
// de contrôle (qui rendraient la règle insupprimable via le HTML rendu).
func validChemin(canon string) bool {
	if canon == ".." || strings.HasPrefix(canon, "../") {
		return false
	}
	for _, r := range canon {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// cibleFrom résout l'utilisateur cible depuis {id}, ou nil (404 déjà écrit).
func (s *Server) cibleFrom(w http.ResponseWriter, r *http.Request) *db.User {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "introuvable", http.StatusNotFound)
		return nil
	}
	cible, err := s.DB.UserByID(id)
	if errors.Is(err, db.ErrNotFound) {
		http.Error(w, "introuvable", http.StatusNotFound)
		return nil
	}
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return nil
	}
	return cible
}

// GET /admin/users/{id} : droits d'un utilisateur (règles + vue arbre).
func (s *Server) handleDroits(w http.ResponseWriter, r *http.Request, u *db.User) {
	cible := s.cibleFrom(w, r)
	if cible == nil {
		return
	}
	s.renderDroits(w, http.StatusOK, u, cible, "")
}

// POST /admin/users/{id}/defaut : change le niveau par défaut (racine).
func (s *Server) handleSetDefaut(w http.ResponseWriter, r *http.Request, u *db.User) {
	cible := s.cibleFrom(w, r)
	if cible == nil {
		return
	}
	niveau, ok := perms.ParseLevel(r.PostFormValue("niveau"))
	if !ok {
		s.renderDroits(w, http.StatusBadRequest, u, cible, "niveau invalide")
		return
	}
	if err := s.DB.SetDefaultLevel(cible.ID, niveau); err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(cible.ID, 10), http.StatusSeeOther)
}

// POST /admin/users/{id}/droits : pose (ou remplace) une règle.
func (s *Server) handleSetDroit(w http.ResponseWriter, r *http.Request, u *db.User) {
	cible := s.cibleFrom(w, r)
	if cible == nil {
		return
	}
	canon := perms.Canon(r.PostFormValue("chemin"))
	niveau, ok := perms.ParseLevel(r.PostFormValue("niveau"))
	if canon == "" || !ok {
		s.renderDroits(w, http.StatusBadRequest, u, cible,
			"chemin et niveau requis (pour la racine, utiliser le niveau par défaut)")
		return
	}
	if !validChemin(canon) {
		s.renderDroits(w, http.StatusBadRequest, u, cible, "chemin invalide")
		return
	}
	if err := s.DB.SetPermission(cible.ID, canon, niveau); err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(cible.ID, 10), http.StatusSeeOther)
}

// POST /admin/users/{id}/droits/supprimer : retire une règle.
func (s *Server) handleSupprimerDroit(w http.ResponseWriter, r *http.Request, u *db.User) {
	cible := s.cibleFrom(w, r)
	if cible == nil {
		return
	}
	if err := s.DB.DeletePermission(cible.ID, r.PostFormValue("chemin")); err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(cible.ID, 10), http.StatusSeeOther)
}
