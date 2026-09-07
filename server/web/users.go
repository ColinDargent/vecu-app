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
	"github.com/colindargent/vecu/server/skills"
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
// LA POSTURE DU COFFRE (DAR-196, 01/09). Trois mots a la place du vocabulaire
// de niveaux : Prive / Lecture seule / Ouvert.
//
// POURQUOI. Le lecteur de ces ecrans est le mainteneur du second cerveau chez
// un client, pas nous. « invisible » decrit ce que le SYSTEME fait ; « privé »
// decrit ce que la personne veut. Et « ecriture » ne dit pas qu'elle inclut la
// lecture, ce qui fait hesiter a chaque reglage.
//
// LE MODELE NE BOUGE PAS. Ces trois mots vivent dans l'INTERFACE seulement : la
// base, l'API et le MCP continuent de parler `invisible`/`lecture`/`ecriture`,
// et les valeurs postees par les formulaires aussi. Traduire jusqu'au stockage
// aurait demande une migration pour un gain d'affichage.
func niveauFR(l perms.Level) string {
	switch l {
	case perms.Ecriture:
		return "ouvert"
	case perms.Lecture:
		return "lecture seule"
	default:
		return "privé"
	}
}

type userVM struct {
	ID       int64
	Username string
	Niveau   string
	IsAdmin  bool
	// Fermable : vide si le compte peut être fermé, sinon le MOTIF du refus.
	//
	// Calculé au rendu et pas au clic, pour que la raison soit lisible AVANT
	// d'essayer. Un bouton qui n'explique qu'après coup fait recommencer.
	Fermable string
}

type usersData struct {
	baseData
	Users   []userVM
	Niveaux []niveauVM
	// Cohortes : celles auxquelles la création peut rattacher le compte. C'est
	// le geste courant à l'arrivée de quelqu'un - on le met dans une cohorte,
	// on ne lui pose pas ses dossiers un par un.
	//
	// SÉPARÉES PAR GENRE, comme sur leur propre écran (04/09) : une case
	// « Skills contenu » et une case « Équipe delivery » dans la même liste ne
	// disent pas ce qu'elles ouvrent, et le paragraphe au-dessus parle des deux
	// à la fois. Le formulaire, lui, poste toujours le même `cohorte_id`.
	CohortesDossiers []cohorteChoixVM
	CohortesSkills   []cohorteChoixVM
	// Cohortes : les deux listes réunies, pour la seule question qui ne regarde
	// pas le genre - « y a-t-il seulement une cohorte à proposer ».
	Cohortes []cohorteChoixVM
	Erreur   string
}

func (s *Server) renderUsers(w http.ResponseWriter, status int, u *db.User, erreur string) {
	list, err := s.DB.ListUsers()
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	cohortes, err := s.DB.ListGroupes()
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	data := usersData{baseData: base(u, "utilisateurs"), Niveaux: niveauxVM, Erreur: erreur}
	for _, g := range cohortes {
		choix := cohorteChoixVM{ID: g.ID, Nom: g.Nom, Genre: g.Genre}
		data.Cohortes = append(data.Cohortes, choix)
		if g.Genre == db.GenreSkill {
			data.CohortesSkills = append(data.CohortesSkills, choix)
			continue
		}
		data.CohortesDossiers = append(data.CohortesDossiers, choix)
	}
	for _, x := range list {
		vm := userVM{ID: x.ID, Username: x.Username, Niveau: niveauFR(x.DefaultLevel), IsAdmin: x.IsAdmin}
		if err := s.DB.CompteFermable(x.ID, u.ID); err != nil {
			vm.Fermable = err.Error()
		}
		data.Users = append(data.Users, vm)
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
	// LES COHORTES SONT VALIDÉES AVANT LA CRÉATION, pour ne pas laisser un
	// compte à moitié installé derrière un message d'erreur. Créer puis échouer
	// sur les cohortes obligerait à deviner s'il faut recommencer ou compléter.
	connues, err := s.DB.ListGroupes()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	valide := make(map[int64]bool, len(connues))
	for _, g := range connues {
		valide[g.ID] = true
	}
	var cohortes []int64
	for _, brut := range r.PostForm["cohorte_id"] {
		id, err := strconv.ParseInt(strings.TrimSpace(brut), 10, 64)
		if err != nil || !valide[id] {
			s.renderUsers(w, http.StatusBadRequest, u, "cohorte introuvable")
			return
		}
		cohortes = append(cohortes, id)
	}
	// LE NIVEAU DANS LES COHORTES EST UN PLAFOND, et il est distinct du défaut
	// du compte - c'est ce qui fait tenir le modèle « privé par défaut, les
	// cohortes ouvrent ». Le prendre égal au défaut le rendrait inutile : un
	// compte privé serait plafonné à `invisible`, donc ses cohortes ne lui
	// ouvriraient rien.
	//
	// « lecture seule » à défaut, et c'est le sens prudent. L'audit du 01/09 l'a
	// posé : ce qu'on cherche en priorité est l'OUVERTURE non voulue, pas la
	// fermeture. Une fermeture excessive se signale toute seule le jour où
	// quelqu'un ne trouve pas un fichier ; une ouverture excessive ne se signale
	// jamais.
	plafond := perms.Lecture
	if brut := r.PostFormValue("niveau_cohorte"); brut != "" {
		n, ok := perms.ParseLevel(brut)
		if !ok {
			s.renderUsers(w, http.StatusBadRequest, u, "niveau de cohorte invalide")
			return
		}
		plafond = n
	}

	// CreateUser pose atomiquement « skills privés par défaut » pour tout compte
	// (admin inclus) : rien à faire ici.
	nouveau, err := s.DB.CreateUser(username, password, niveau, r.PostFormValue("admin") == "on")
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.renderUsers(w, http.StatusBadRequest, u, "ce nom d'utilisateur est déjà pris")
			return
		}
		log.Printf("web: création utilisateur : %v", err)
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	for _, id := range cohortes {
		if err := s.DB.SetMembreGroupe(id, nouveau.ID, plafond); err != nil {
			// Le compte EXISTE : ne pas faire passer un rattachement manqué pour
			// un échec de création, sinon on le recrée et le nom est déjà pris.
			log.Printf("web: rattachement de %s à la cohorte %d : %v", username, id, err)
			s.renderUsers(w, http.StatusBadRequest, u,
				"le compte « "+username+" » est créé, mais son rattachement aux cohortes a échoué : "+
					"reprenez-le depuis l'écran des cohortes")
			return
		}
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// POST /admin/users/{id}/fermer : fermer un compte (DAR-204).
//
// Jusqu'ici, Vécu ne savait pas le faire. Ni `DeleteUser` dans `server/db`, ni
// route, ni commande : la seule voie était du SQL direct sur la base de
// production, et elle est fermée aussi - le conteneur n'a ni `sqlite3` ni
// `python3`. Un client aura pourtant des comptes à fermer, et le mainteneur de
// DAR-198 rencontre le geste avant nous.
//
// LA CONFIRMATION EST LE NOM DU COMPTE, retapé. Pas une case à cocher : le
// geste est irréversible pour les droits, la ligne est au milieu d'un tableau,
// et une case se coche par réflexe. Retaper un nom demande de regarder LEQUEL
// on ferme - qui est exactement l'erreur qu'on cherche à empêcher.
func (s *Server) handleFermerCompte(w http.ResponseWriter, r *http.Request, u *db.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderUsers(w, http.StatusBadRequest, u, "identifiant de compte invalide")
		return
	}
	cible, err := s.DB.UserByID(id)
	if errors.Is(err, db.ErrNotFound) {
		s.renderUsers(w, http.StatusNotFound, u, "ce compte n'existe pas (ou plus)")
		return
	}
	if err != nil {
		log.Printf("web: lecture du compte à fermer : %v", err)
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderUsers(w, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	// La garde de saisie AVANT la garde d'enfermement : dire « c'est le dernier
	// administrateur » à quelqu'un qui a mal tapé le nom lui apprend un fait sur
	// un compte qu'il ne visait peut-être pas.
	if r.PostFormValue("confirmation") != cible.Username {
		s.renderUsers(w, http.StatusBadRequest, u,
			"compte non fermé : le nom saisi ne correspond pas à « "+cible.Username+" »")
		return
	}
	if err := s.DB.CompteFermable(id, u.ID); err != nil {
		s.renderUsers(w, http.StatusBadRequest, u, "compte non fermé : "+err.Error())
		return
	}
	if err := s.DB.DeleteUser(id); err != nil {
		log.Printf("web: fermeture du compte %d : %v", id, err)
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
	// Origine : d'OÙ vient ce droit, en clair. Sans elle, le mainteneur lit
	// « Marie : lecture » et ne sait pas s'il doit toucher la règle de Marie, sa
	// cohorte, le défaut du dossier ou celui de son compte - donc il tâtonne, et
	// il tâtonne sur des droits.
	//
	// L'écran des dossiers le disait depuis le 01/09 ; la fiche d'un compte,
	// non. C'est pourtant elle qu'on ouvre quand on se demande « pourquoi cette
	// personne voit ça », et c'est le seul écran où la réponse « par sa
	// cohorte » évite de poser une exception qui doublerait la cohorte.
	Origine string
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

// L'ordre va du plus ferme au plus ouvert, comme les boutons se lisent.
// `Valeur` reste la forme de STOCKAGE : c'est ce que le formulaire poste, et le
// serveur n'apprend aucun vocabulaire d'interface.
var niveauxVM = []niveauVM{
	{"invisible", "privé"},
	{"lecture", "lecture seule"},
	{"ecriture", "ouvert"},
}

// origineCouvrante : l'origine de la règle qui décide de `cible`.
//
// La même arbitrage que `perms.Effective` : la règle la plus PROFONDE qui
// couvre le chemin gagne. Recalculer ici plutôt que faire porter l'origine par
// `perms.Rule` garde le paquet `perms` ignorant de la base - il ne connaît que
// des chemins et des niveaux, et c'est ce qui le rend testable seul.
func origineCouvrante(chemin string, regles []perms.Rule, origines []db.Origine) db.Origine {
	chemin = perms.Canon(chemin)
	meilleure, profondeur := db.Origine{}, -1
	for i, r := range regles {
		rp := perms.Canon(r.Path)
		if rp != "" && rp != chemin && !strings.HasPrefix(chemin, rp+"/") {
			continue
		}
		p := 0
		if rp != "" {
			p = strings.Count(rp, "/") + 1
		}
		if p > profondeur && i < len(origines) {
			meilleure, profondeur = origines[i], p
		}
	}
	return meilleure
}

func (s *Server) renderDroits(w http.ResponseWriter, status int, u, cible *db.User, erreur string) {
	rules, origines, err := s.DB.ReglesEtOrigines(cible.ID)
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
	// La VUE D'ADMINISTRATION, pas le perimetre de lecture (DAR-196). Deux des
	// six portes d'ecriture vivent sur cet ecran (`/droits` et
	// `/droits/supprimer`) : le laisser filtre y rouvrait l'enfermement corrige
	// sur l'accueil et sur l'ecran des dossiers, un ecran plus loin.
	dirs := make([]string, 0, len(tousDirs))
	for _, d := range tousDirs {
		if (u.IsAdmin && !skills.SousRacine(d)) || perms.CanRead(d, u.DefaultLevel, adminRules) {
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
		// `DroitAvecOrigine` relit la base à chaque appel ; sur un arbre de
		// plusieurs centaines de dossiers, c'est ce qui a coûté 645 ms à l'écran
		// du mainteneur le 01/09. On prend donc l'origine dans les règles DÉJÀ
		// résolues, qui portent la leur - une seule requête pour tout l'arbre.
		niveau := perms.Effective(d, cible.DefaultLevel, rules)
		data.Arbre = append(data.Arbre, nodeVM{
			Path:       d,
			Nom:        path.Base(d),
			Niveau:     niveauFR(niveau),
			Profondeur: strings.Count(d, "/"),
			Direct:     direct[perms.Canon(d)],
			Origine:    origineEnClair(origineCouvrante(d, rules, origines)),
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
	if refus := refusZoneSkills(canon); refus != "" {
		s.renderDroits(w, http.StatusBadRequest, u, cible, refus)
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
