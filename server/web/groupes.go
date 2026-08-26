// Les groupes de skills : la surface de gestion.
//
// Un groupe porte les droits d'un ensemble de skills ; le tag, lui, porte la
// thématique et ne décide de rien. Deux dimensions distinctes, décidées le
// 21/08 : « le groupe porte les droits, le tag sert à s'y retrouver ».
//
// Créer, supprimer et régler les membres d'un groupe est réservé aux
// administrateurs (décision de Colin : un membre ne fabrique pas de périmètre).
// RANGER un skill est ouvert à son créateur aussi, parce que c'est son geste de
// partage - le pendant de la délégation que F3 lui a donnée.
package web

import (
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
)

// groupeVM : un groupe avec ses membres et ce qu'il contient.
type groupeVM struct {
	ID     int64
	Nom    string
	Skills []string
	// Dossiers : EN LECTURE SEULE ici. Ils se règlent depuis la page du dossier,
	// qui est la seule surface autoritaire de cet ensemble - deux écrans qui
	// font tous les deux foi s'écrasent l'un l'autre.
	Dossiers []string
	Membres  []accesVM
	// Resume : les mêmes accès que `Membres`, lus par niveau. Même panneau que
	// le partage d'un skill, pour la même raison - une colonne par compte
	// faisait grandir le tableau avec l'équipe.
	Resume []niveauResumeVM
}

// retourGroupe : revenir À l'endroit du geste, avec de quoi afficher qu'il a
// abouti. Sans ça, le navigateur revient en haut d'une page de 220 Ko dont
// cette section est à la fin : le geste écrivait bien et ne se voyait jamais.
// `ancre` est fabriquée ici, jamais reçue du formulaire.
func retourGroupe(w http.ResponseWriter, r *http.Request, ancre string) {
	http.Redirect(w, r, "/admin/skills?okg=1#"+ancre, http.StatusSeeOther)
}

func ancreGroupe(prefixe string, id int64) string {
	return prefixe + strconv.FormatInt(id, 10)
}

// maxNomGroupe : un nom de groupe est un libellé d'interface, pas un chemin.
// Il n'a donc pas les contraintes d'un nom de dossier - il peut porter des
// accents - mais il reste borné.
const maxNomGroupe = 60

func validNomGroupe(nom string) error {
	nom = strings.TrimSpace(nom)
	switch {
	case nom == "":
		return errNomGroupe("un nom de groupe ne peut pas être vide")
	case len([]rune(nom)) > maxNomGroupe:
		return errNomGroupe("nom de groupe trop long (60 caractères maximum)")
	case strings.ContainsAny(nom, "\n\r\t"):
		return errNomGroupe("un nom de groupe tient sur une ligne")
	}
	return nil
}

type errNomGroupe string

func (e errNomGroupe) Error() string { return string(e) }

// groupesDuDepot : tous les groupes, avec leurs membres et leurs skills.
// Réservé aux administrateurs par les routes qui l'appellent : « qui a accès à
// quoi » est une information de gouvernance, comme pour les dossiers.
func (s *Server) groupesDuDepot(users []db.User) ([]groupeVM, error) {
	groupes, err := s.DB.ListGroupes()
	if err != nil {
		return nil, err
	}
	out := make([]groupeVM, 0, len(groupes))
	for _, g := range groupes {
		vm := groupeVM{ID: g.ID, Nom: g.Nom}
		if vm.Skills, err = s.DB.SkillsDuGroupe(g.ID); err != nil {
			return nil, err
		}
		lignes, err := s.DB.CheminsDuGroupe(g.ID, "dossier")
		if err != nil {
			return nil, err
		}
		for _, l := range lignes {
			vm.Dossiers = append(vm.Dossiers, l.Chemin)
		}
		membres, err := s.DB.MembresGroupe(g.ID)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			niveau, dans := membres[u.ID]
			if !dans {
				niveau = perms.Invisible
			}
			vm.Membres = append(vm.Membres, accesVM{
				UserID: u.ID, Username: u.Username, Niveau: niveau.String(),
			})
		}
		vm.Resume = resumeParNiveau(vm.Membres)
		out = append(out, vm)
	}
	return out, nil
}

// POST /admin/skills/groupes : créer un groupe.
func (s *Server) handleCreerGroupe(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	nom := strings.TrimSpace(r.PostFormValue("nom"))
	if err := validNomGroupe(nom); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, err.Error())
		return
	}
	id, err := s.DB.CreerGroupe(nom)
	if err != nil {
		// Le nom est UNIQUE en base : un doublon revient ici, et le message
		// métier vaut mieux qu'une erreur interne.
		s.renderSkills(w, r, http.StatusBadRequest, u, "un groupe porte déjà le nom « "+nom+" »")
		return
	}
	retourGroupe(w, r, ancreGroupe("groupe-", id))
}

// POST /admin/skills/groupes/supprimer : effacer un groupe.
//
// Les accès qu'il ouvrait se referment avec lui (ON DELETE CASCADE, et le DSN
// active bien foreign_keys). Aucune règle n'est à nettoyer dans `permissions` :
// les groupes n'y écrivent jamais.
func (s *Server) handleSupprimerGroupe(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("groupe_id"), 10, 64)
	if err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "groupe invalide")
		return
	}
	if err := s.DB.SupprimeGroupe(id); err != nil {
		log.Printf("web: suppression du groupe %d : %v", id, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	// La ligne du groupe n'existe plus : on revient au bloc qui, lui, est
	// toujours rendu.
	retourGroupe(w, r, "creer-groupe")
}

// POST /admin/skills/groupes/membres : les niveaux de TOUS les comptes sur un
// groupe, en une fois.
//
// Même panneau, même geste et même « Enregistrer » que le partage d'un skill.
// Le réglage bouton par bouton écrivait bien - mesuré, 303 et l'état changeait -
// mais rechargeait une page dont cette section est à la fin : on ne voyait
// jamais le résultat, donc on croyait le groupe verrouillé.
//
// « invisible » SORT le compte du groupe plutôt que d'y stocker un refus : un
// compte hors groupe et un compte au niveau invisible sont la même chose, et
// une seule des deux formes évite qu'elles divergent.
func (s *Server) handleSetMembresGroupe(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("groupe_id"), 10, 64)
	if err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "groupe invalide")
		return
	}
	users, err := s.DB.ListUsers()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	actuels, err := s.DB.MembresGroupe(id)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	// Tout valider avant d'écrire quoi que ce soit : un formulaire à moitié
	// accepté laisserait un groupe à moitié réglé, sans que l'écran le dise.
	type poser struct {
		userID int64
		niveau perms.Level
	}
	var aPoser []poser
	for _, x := range users {
		brut := r.PostFormValue("niveau_" + strconv.FormatInt(x.ID, 10))
		if brut == "" {
			continue // compte absent du formulaire : on ne lui invente rien
		}
		niveau, ok := perms.ParseLevel(brut)
		if !ok {
			s.renderSkills(w, r, http.StatusBadRequest, u, "groupe, utilisateur ou niveau invalide")
			return
		}
		courant, dans := actuels[x.ID]
		if !dans {
			courant = perms.Invisible
		}
		if courant == niveau {
			continue
		}
		aPoser = append(aPoser, poser{x.ID, niveau})
	}
	for _, p := range aPoser {
		var err error
		if p.niveau == perms.Invisible {
			err = s.DB.RetireMembreGroupe(id, p.userID)
		} else {
			err = s.DB.SetMembreGroupe(id, p.userID, p.niveau)
		}
		if err != nil {
			log.Printf("web: membres du groupe %d : %v", id, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
	}
	retourGroupe(w, r, ancreGroupe("acces-groupe-", id))
}

// POST /admin/skills/groupes/modifier : le NOM et la COMPOSITION d'un groupe,
// en une fois.
//
// Ce qui manquait pour qu'un groupe soit vraiment modifiable après sa création.
// Le niveau d'un compte se réglait déjà, ranger un skill aussi - mais depuis la
// ligne du skill, à l'autre bout de la page, et la colonne « Skills » de la vue
// groupe était du texte figé : on croyait le groupe verrouillé.
//
// Tout est validé avant qu'une ligne soit écrite, et un slug inconnu est refusé
// plutôt que rangé : `RangeSkill` insère par slug sans se demander si le skill
// existe, un formulaire bricolé y créerait des appartenances fantômes.
func (s *Server) handleModifierGroupe(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("groupe_id"), 10, 64)
	if err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "groupe invalide")
		return
	}
	nom := strings.TrimSpace(r.PostFormValue("nom"))
	if err := validNomGroupe(nom); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, err.Error())
		return
	}

	// Les skills que le formulaire propose sont ceux que l'appelant VOIT : on
	// n'accepte pas d'en ranger un qui n'existe pas, ou qu'il ne voit pas.
	visibles, err := s.skillsDuDepot(u)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	connus := make(map[string]bool, len(visibles))
	for _, sk := range visibles {
		connus[sk.Slug] = true
	}
	voulus := map[string]bool{}
	for _, slug := range r.PostForm["slug"] {
		if !connus[slug] {
			s.renderSkills(w, r, http.StatusBadRequest, u, "skill inconnu : « "+slug+" »")
			return
		}
		voulus[slug] = true
	}
	actuels, err := s.DB.SkillsDuGroupe(id)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	if err := s.DB.RenommeGroupe(id, nom); err != nil {
		// Le nom est UNIQUE en base : un doublon revient ici, et le message
		// métier vaut mieux qu'une erreur interne.
		s.renderSkills(w, r, http.StatusBadRequest, u, "un groupe porte déjà le nom « "+nom+" »")
		return
	}
	// ON N'ÉCRIT QUE CE QUI CHANGE, comme le panneau d'en face. Reposer une
	// appartenance déjà là ferait plus que du bruit : l'upsert de RangeChemin
	// réécrit le genre, donc un simple renommage de groupe retournerait en
	// `skill` toute ligne `dossier` dont le chemin coïncide.
	//
	// L'ordre est déterministe (tri) : sur échec au milieu, ce qui a survécu
	// ne change pas d'une exécution à l'autre, et le message qui suit peut être
	// rapproché de l'état constaté.
	deja := map[string]bool{}
	for _, slug := range actuels {
		deja[slug] = true
	}
	ajouts := make([]string, 0, len(voulus))
	for slug := range voulus {
		if !deja[slug] {
			ajouts = append(ajouts, slug)
		}
	}
	sort.Strings(ajouts)
	for _, slug := range ajouts {
		if err := s.DB.RangeChemin(id, skillPath(slug), "skill", slug); err != nil {
			log.Printf("web: rangement de %s dans le groupe %d : %v", slug, id, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
	}
	for _, slug := range actuels {
		if voulus[slug] {
			continue
		}
		// Sortir un skill de CE groupe referme les accès que CE groupe lui
		// ouvrait, et le laisse dans les autres. Depuis le 25/08 un skill vit
		// dans plusieurs cohortes : décocher ici ne doit pas le sortir d'ailleurs.
		if err := s.DB.SortChemin(id, skillPath(slug)); err != nil {
			log.Printf("web: sortie de %s du groupe %d : %v", slug, id, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
	}
	retourGroupe(w, r, ancreGroupe("groupe-", id))
}

// POST /admin/skills/ranger : régler l'ensemble des groupes d'un skill.
//
// Le formulaire porte AUTANT de `groupe_id` que de cases cochées, et cet
// ensemble fait foi : ce qui n'y est pas est retiré. Aucune case = skill privé.
// C'est la forme symétrique du panneau d'en face (« modifier le groupe »), où
// c'est l'ensemble des skills cochés qui fait foi pour un groupe donné.
//
// Un `<select>` ne convenait plus : depuis le 25/08 un skill vit dans plusieurs
// cohortes, et un choix unique ne sait pas dire « les opérations ET le
// marketing ».
//
// Ouvert au CRÉATEUR du skill comme à un administrateur : ranger son skill est
// le geste de partage de son auteur. Pour tout autre compte, 404 avant toute
// autre validation - on ne révèle pas l'existence d'un skill qu'on ne gère pas.
func (s *Server) handleRangerSkill(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	slug := r.PostFormValue("slug")
	if espaces.ValidNom(slug) != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "skill invalide")
		return
	}
	if !s.DB.EstCreateur(u.ID, slug) && !u.IsAdmin {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}

	// Les groupes doivent exister : sans ce contrôle, un id inventé rangerait
	// le skill dans un groupe fantôme, invisible de l'écran et impossible à
	// défaire autrement qu'en base.
	groupes, err := s.DB.ListGroupes()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	connus := map[int64]bool{}
	for _, g := range groupes {
		connus[g.ID] = true
	}

	// LA PORTÉE DU GESTE (25/08, Q3). Le 21/08 a ouvert le rangement au créateur
	// du skill, parce que c'est son geste de partage. Sous « un skill, un
	// groupe » sa portée était d'un groupe ; depuis que l'ensemble coché fait
	// foi, elle serait globale - un créateur non-admin sortirait son skill de
	// TOUTES les cohortes qu'un administrateur a réglées, en un POST.
	//
	// Un non-administrateur ne touche donc que les cohortes dont il est membre.
	// Les autres restent en place, invisibles et intactes : ce qu'il ne voit pas
	// de l'intérieur, il ne peut pas le défaire.
	portee := connus
	if !u.IsAdmin {
		miennes, err := s.DB.GroupesDuMembre(u.ID)
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		portee = map[int64]bool{}
		for _, id := range miennes {
			portee[id] = true
		}
	}

	voulus := map[int64]bool{}
	for _, brut := range r.PostForm["groupe_id"] {
		brut = strings.TrimSpace(brut)
		if brut == "" {
			continue
		}
		id, err := strconv.ParseInt(brut, 10, 64)
		if err != nil {
			s.renderSkills(w, r, http.StatusBadRequest, u, "groupe invalide")
			return
		}
		// Même message pour « ce groupe n'existe pas » et « il ne vous concerne
		// pas » : distinguer les deux apprendrait à un membre l'existence de
		// cohortes qui ne le regardent pas.
		if !connus[id] || !portee[id] {
			s.renderSkills(w, r, http.StatusBadRequest, u, "groupe introuvable")
			return
		}
		voulus[id] = true
	}

	chemin := skillPath(slug)
	actuels, err := s.DB.GroupesDuChemin(chemin, "skill")
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	// ON N'ÉCRIT QUE CE QUI CHANGE : reposer une appartenance déjà là ferait du
	// bruit en base pour rien, et la retirer puis la remettre rouvrirait une
	// fenêtre où l'accès n'existe pas.
	dedans := map[int64]bool{}
	for _, id := range actuels {
		dedans[id] = true
		// Hors de sa portée : on ne retire pas. C'est ce qui empêche l'ensemble
		// coché de faire foi au-delà de ce que l'appelant connaît.
		if !voulus[id] && portee[id] {
			if err := s.DB.SortChemin(id, chemin); err != nil {
				log.Printf("web: sortie de %s du groupe %d : %v", slug, id, err)
				erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
				return
			}
		}
	}
	// Trié : `for id := range voulus` parcourt une map, donc sur échec au
	// milieu, QUELLES appartenances ont survécu changerait d'une exécution à
	// l'autre.
	ajouts := make([]int64, 0, len(voulus))
	for id := range voulus {
		if !dedans[id] {
			ajouts = append(ajouts, id)
		}
	}
	sort.Slice(ajouts, func(i, j int) bool { return ajouts[i] < ajouts[j] })
	for _, id := range ajouts {
		if err := s.DB.RangeChemin(id, chemin, "skill", slug); err != nil {
			log.Printf("web: rangement de %s dans le groupe %d : %v", slug, id, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
	}
	retourSkill(w, r, slug)
}
