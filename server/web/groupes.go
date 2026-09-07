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
	"github.com/colindargent/vecu/server/skills"
)

// groupeVM : un groupe avec ses membres et ce qu'il contient.
type groupeVM struct {
	ID  int64
	Nom string
	// Genre : `dossier` ou `skill`. Une cohorte ne porte qu'un des deux depuis
	// le 02/09, et chaque écran ne rend que le sien - c'est tout l'objet du
	// typage : « les cohortes de skill et les cohortes de dossier doivent être
	// séparées » (Colin).
	Genre  string
	Skills []string
	// Dossiers : le chemin ET le niveau que la cohorte y accorde. Le RANGEMENT
	// (quel dossier est dans la cohorte) reste posé depuis la page du dossier,
	// seule surface autoritaire de cet ensemble ; le NIVEAU se règle ici, parce
	// qu'il n'a de sens que rapporté à la cohorte qui le porte.
	Dossiers []cheminCohorteVM
	// Arbre : les dossiers proposés au réglage dans le panneau, espaces et
	// enfants directs. Vide sur une cohorte de skills.
	Arbre []noeudCohorteVM
	// Profonds : ce que la cohorte porte et que l'arbre NE MONTRE PAS, parce
	// qu'il s'arrête à deux niveaux. Sans cette partition, le panneau listait
	// une deuxième fois, en dessous de l'arbre, les mêmes dossiers que l'arbre
	// venait de régler : `clients` s'y lisait trois fois, sous trois formes et
	// trois vocabulaires. Ici, chaque chemin apparaît à un seul endroit - dans
	// l'arbre s'il y est, dans cette liste sinon.
	Profonds []cheminCohorteVM
	Membres  []accesVM
	// Resume : les mêmes accès que `Membres`, lus par niveau. Même panneau que
	// le partage d'un skill, pour la même raison - une colonne par compte
	// faisait grandir le tableau avec l'équipe.
	Resume []niveauResumeVM
}

// cheminCohorteVM : un dossier porte par une cohorte, et le niveau qu'elle y
// accorde. Niveau vide = le chemin suit le niveau du membre.
type cheminCohorteVM struct {
	Chemin string
	Niveau string
}

// retourGroupe : revenir À l'endroit du geste, avec de quoi afficher qu'il a
// abouti. Sans ça, le navigateur revient en haut d'une page de 220 Ko dont
// cette section est à la fin : le geste écrivait bien et ne se voyait jamais.
// `ancre` est fabriquée ici, jamais reçue du formulaire.
//
// `okg` PORTE L'ID DE LA COHORTE, et plus un `1` (04/09). Le gabarit s'en
// servait pour rouvrir le panneau après le retour ; comme il ne disait pas
// LEQUEL, la page revenait avec tous les panneaux de toutes les cohortes
// dépliés - une dizaine d'arbres et de grilles de partage à parcourir pour
// relire la ligne qu'on venait d'enregistrer. Un id, un panneau ouvert.
// `okg=0` (création) n'en ouvre aucun : la nouvelle cohorte est vide, il n'y a
// rien à y relire.
func retourGroupe(w http.ResponseWriter, r *http.Request, id int64, ancre string) {
	http.Redirect(w, r, "/admin/cohortes?okg="+strconv.FormatInt(id, 10)+"&ou="+ancre+"#"+ancre, http.StatusSeeOther)
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
func (s *Server) groupesDuDepot(appelant *db.User, users []db.User) ([]groupeVM, error) {
	groupes, err := s.DB.ListGroupes()
	if err != nil {
		return nil, err
	}
	out := make([]groupeVM, 0, len(groupes))
	for _, g := range groupes {
		vm := groupeVM{ID: g.ID, Nom: g.Nom, Genre: g.Genre}
		if vm.Skills, err = s.DB.SkillsDuGroupe(g.ID); err != nil {
			return nil, err
		}
		lignes, err := s.DB.CheminsDuGroupe(g.ID, db.GenreDossier)
		if err != nil {
			return nil, err
		}
		for _, l := range lignes {
			vm.Dossiers = append(vm.Dossiers, cheminCohorteVM{Chemin: l.Chemin, Niveau: l.Niveau})
		}
		// L'arbre n'est construit que pour les cohortes de DOSSIERS : sur une
		// cohorte de skills il ne serait jamais rendu, et le construire coûterait
		// une liste du dépôt par cohorte pour rien.
		if g.Genre == db.GenreDossier {
			if vm.Arbre, err = s.arbreDeCohorte(appelant, g.ID); err != nil {
				return nil, err
			}
			couvert := make(map[string]bool, len(vm.Arbre))
			for _, n := range vm.Arbre {
				couvert[n.Chemin] = true
			}
			for _, d := range vm.Dossiers {
				if !couvert[d.Chemin] {
					vm.Profonds = append(vm.Profonds, d)
				}
			}
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
	// LE GENRE VIENT DE L'ÉCRAN QUI CRÉE, et il est explicite dans le
	// formulaire. Deux surfaces créent des cohortes - celle des dossiers et
	// celle des skills - et un défaut implicite ferait créer des cohortes de
	// dossiers depuis l'écran des skills sans que personne ne le remarque avant
	// le premier rangement refusé.
	genre := r.PostFormValue("genre")
	if genre == "" {
		// Le défaut vient de l'ÉCRAN qui a servi le formulaire, pas d'une
		// constante : une cohorte créée depuis l'écran des skills est une
		// cohorte de skills, et l'inverse serait un piège - elle refuserait le
		// premier rangement, une fois créée et nommée.
		genre = db.GenreDossier
		if strings.HasPrefix(r.URL.Path, "/admin/skills/") {
			genre = db.GenreSkill
		}
	}
	if genre != db.GenreDossier && genre != db.GenreSkill {
		s.renderSkills(w, r, http.StatusBadRequest, u, "genre de cohorte inconnu")
		return
	}
	id, err := s.DB.CreerGroupeDeGenre(nom, genre)
	if err != nil {
		// Le nom est UNIQUE en base : un doublon revient ici, et le message
		// métier vaut mieux qu'une erreur interne.
		s.renderSkills(w, r, http.StatusBadRequest, u, "un groupe porte déjà le nom « "+nom+" »")
		return
	}
	retourGroupe(w, r, id, ancreGroupe("groupe-", id))
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
	retourGroupe(w, r, 0, "creer-groupe")
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
	retourGroupe(w, r, id, ancreGroupe("acces-groupe-", id))
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
	// LES DOSSIERS, cochés dans le même panneau que les skills (02/09).
	//
	// Jusqu'ici ils étaient EN LECTURE ici, et ne se rangeaient que depuis la
	// page du dossier. C'était le geste inverse de celui qu'on fait vraiment :
	// on part de la cohorte - « l'équipe contenu » - et on lui donne ses
	// dossiers, on ne part pas de chaque dossier pour se demander qui y entre.
	//
	// LE PÉRIMÈTRE DES CASES EST CELUI DES ESPACES, et rien d'autre. C'est ce
	// qui rend le retrait sûr : un chemin PROFOND rangé depuis la page d'un
	// dossier (`shared/direction`) n'a pas de case ici, donc l'ensemble coché
	// ne peut pas le décrire, donc il n'est jamais retiré par ce formulaire.
	// Sans cette borne, ouvrir ce panneau et enregistrer viderait en silence
	// tous les réglages fins de la cohorte.
	if err := s.rangeArbreDuGroupe(w, r, u, id); err != nil {
		return // la réponse est déjà écrite
	}
	retourGroupe(w, r, id, ancreGroupe("groupe-", id))
}

// rangeArbreDuGroupe applique l'arbre de dossiers du panneau d'une cohorte.
//
// CE QUE LE PANNEAU POSTE, ET POURQUOI CE N'EST PLUS UNE CASE À COCHER. Colin,
// 02/09 : « depuis la cohorte, je veux pouvoir enlever des dossiers dans un
// dossier. Sinon c'est juste je te donne accès à un dossier. » Une case ne sait
// dire que dedans/dehors ; le geste réel est « `clients` oui, mais pas
// `clients/confidentiel` », et il demande un NIVEAU par chemin.
//
// Chaque nœud poste donc `noeud=<chemin>` et `niveau_<chemin>` parmi :
//   - `hors`     : le chemin n'est pas dans la cohorte ;
//   - `""`       : il y est, au niveau du membre ;
//   - un niveau  : il y est, à ce niveau - `invisible` étant l'exclusion.
//
// LA BORNE DU RETRAIT EST L'ENSEMBLE DES NŒUDS POSTÉS, pas ce que la cohorte
// porte. Un chemin plus profond que l'arbre (réglé par le formulaire d'en
// dessous) n'a pas de nœud, donc il ne peut pas être emporté par un panneau
// resté ouvert. C'est la même garde qu'avant, sur une surface plus riche.
func (s *Server) rangeArbreDuGroupe(w http.ResponseWriter, r *http.Request, u *db.User, id int64) error {
	proposes, err := s.arbreDeCohorte(u, id)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return err
	}
	connus := make(map[string]bool, len(proposes))
	for _, n := range proposes {
		connus[n.Chemin] = true
	}
	// Ce que le formulaire décrit, chemin par chemin.
	voulus := map[string]string{}
	for _, brut := range r.PostForm["noeud"] {
		chemin := perms.Canon(strings.TrimSpace(brut))
		// LA ZONE DES SKILLS D'ABORD. Elle n'est pas dans l'arbre - il l'écarte
		// par construction - donc le refus générique la couvrirait. Mais il
		// dirait « dossier inconnu » à quelqu'un qui vise un chemin bien réel,
		// au lieu de l'envoyer à l'écran qui sait le régler.
		if motif := refusZoneSkills(chemin); motif != "" {
			s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", motif)
			return errNomGroupe(motif)
		}
		if !connus[chemin] {
			s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "dossier inconnu : « "+chemin+" »")
			return errNomGroupe("dossier inconnu")
		}
		niveau := r.PostFormValue("niveau_" + chemin)
		if niveau != "hors" && niveau != "" {
			if _, ok := perms.ParseLevel(niveau); !ok {
				s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "niveau invalide sur « "+chemin+" »")
				return errNomGroupe("niveau invalide")
			}
		}
		voulus[chemin] = niveau
	}

	// Ordre déterministe : sur échec au milieu, ce qui a survécu ne dépend pas
	// de l'ordre de parcours d'une map.
	for _, n := range proposes {
		niveau, decrit := voulus[n.Chemin]
		if !decrit {
			continue // nœud absent du formulaire : on n'en conclut rien
		}
		switch {
		case niveau == "hors":
			if n.Dedans {
				if err := s.DB.SortChemin(id, n.Chemin); err != nil {
					log.Printf("web: sortie de %s de la cohorte %d : %v", n.Chemin, id, err)
					erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
					return err
				}
			}
		default:
			if !n.Dedans {
				if err := s.DB.RangeChemin(id, n.Chemin, db.GenreDossier, ""); err != nil {
					log.Printf("web: rangement de %s dans la cohorte %d : %v", n.Chemin, id, err)
					s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", err.Error())
					return err
				}
			}
			if niveau != n.Niveau {
				if err := s.DB.SetNiveauChemin(id, n.Chemin, niveau); err != nil {
					log.Printf("web: niveau de %s dans la cohorte %d : %v", n.Chemin, id, err)
					erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
					return err
				}
			}
		}
	}
	return nil
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

// cohortesData : l'ecran des cohortes.
type cohortesData struct {
	baseData
	// Groupes : toutes les cohortes, tous genres confondus. Sert aux questions
	// qui ne regardent pas le genre - « y a-t-il seulement une cohorte ».
	Groupes []groupeVM
	// LES DEUX GENRES SONT SÉPARÉS ICI, pas dans le gabarit (04/09). « Les
	// cohortes de skill et les cohortes de dossier doivent être séparées »
	// (Colin, 02/09) : le typage est parti en base ce jour-là, mais l'écran a
	// continué de rendre UN tableau alphabétique où « Skills contenu » se
	// glissait entre deux cohortes de dossiers. Un modèle Go n'a pas de filtre :
	// la partition se fait là où elle se calcule.
	//
	// EN SECTIONS, et pas en deux champs, pour que le gabarit les parcoure au
	// lieu de recopier son tableau. Deux copies d'un tableau de trois colonnes
	// et d'un panneau finissent par diverger ; celle du résumé de partage,
	// entre l'écran des skills et celui-ci, l'avait déjà fait.
	Sections []sectionCohortesVM
	// Skills : la liste complete, pour cocher ceux qu'une cohorte porte. Le
	// panneau de composition en a besoin ; l'ecran ne les rend pas autrement.
	Skills []skillVM
	// Niveaux : les trois niveaux, pour les radios du panneau des membres.
	Niveaux         []niveauVM
	ConfirmeGroupes bool
	// Confirme : la cohorte dont le panneau vient d'écrire, et la SEULE que la
	// page rouvre. Zéro = aucune (création, suppression, simple visite).
	Confirme int64
	// Ou : l'ancre du panneau d'où le geste est parti. Sans elle, une cohorte
	// revenait avec SES DEUX panneaux ouverts - sa composition et ses membres -
	// alors qu'un seul des deux venait d'écrire.
	Ou     string
	Erreur string
}

// sectionCohortesVM : un genre de cohorte, et ce que l'écran en dit. Les
// libellés vivent ici plutôt que dans une condition du gabarit : « dossiers
// portés » contre « skills portés », et le vide de l'un ne se lit pas comme le
// vide de l'autre.
type sectionCohortesVM struct {
	Titre   string
	Ancre   string
	Colonne string
	// Vide : ce que la section dit quand elle ne porte aucune cohorte. Un
	// « aucune cohorte » sec laisse le lecteur sans le geste suivant.
	Vide string
	// Dossier : cette section porte des cohortes de dossiers. Décide du panneau
	// de composition - un arbre de chemins, ou une liste de skills à cocher.
	Dossier bool
	Groupes []groupeVM
}

// noeudCohorteVM : un dossier proposé au réglage dans le panneau d'une cohorte.
//
// LA PROFONDEUR EST BORNÉE À 2 - les espaces et leurs enfants directs - et c'est
// un compromis assumé, pas un oubli. Le gabarit rend ce bloc pour CHAQUE
// cohorte de la page ; un vault de deux cents dossiers ferait donc six cents
// contrôles à chaque affichage, très au-delà du contrat de latence de DAR-195.
//
// Ce que ça couvre : « la cohorte a `clients`, mais pas `clients/confidentiel` »,
// qui est le geste que Colin a nommé. Ce que ça ne couvre pas : une exclusion
// plus profonde, qui garde le formulaire « réserver un sous-dossier » plus bas -
// il range et règle en un geste depuis le 02/09.
type noeudCohorteVM struct {
	Chemin string
	Nom    string
	Enfant bool
	// Niveau : ce que la cohorte accorde SUR CE CHEMIN, forme de stockage.
	// Vide = rien de posé, le chemin suit son parent (ou n'est pas dans la
	// cohorte du tout).
	Niveau string
	// Dedans : ce chemin est rangé dans la cohorte. Distinct d'un niveau vide :
	// un chemin rangé sans niveau prend celui du membre, ce qui n'est pas la
	// même chose que ne pas y être.
	Dedans bool
}

// arbreDeCohorte : les dossiers proposés au réglage, pour une cohorte donnée.
func (s *Server) arbreDeCohorte(u *db.User, groupeID int64) ([]noeudCohorteVM, error) {
	liste, err := s.espacesDuDepot(u)
	if err != nil {
		return nil, err
	}
	lignes, err := s.DB.CheminsDuGroupe(groupeID, db.GenreDossier)
	if err != nil {
		return nil, err
	}
	pose := make(map[string]string, len(lignes))
	dedans := make(map[string]bool, len(lignes))
	for _, l := range lignes {
		pose[l.Chemin] = l.Niveau
		dedans[l.Chemin] = true
	}
	fichiers, err := s.Store.List("")
	if err != nil {
		return nil, err
	}
	enfants := map[string]map[string]bool{}
	for _, f := range fichiers {
		if skills.SousRacine(f) {
			continue // les skills ont leur propre écran, et leur propre cohorte
		}
		morceaux := strings.Split(f, "/")
		if len(morceaux) < 3 {
			continue // un fichier à la racine d'un espace n'est pas un dossier
		}
		if enfants[morceaux[0]] == nil {
			enfants[morceaux[0]] = map[string]bool{}
		}
		enfants[morceaux[0]][morceaux[1]] = true
	}

	var out []noeudCohorteVM
	for _, e := range liste {
		out = append(out, noeudCohorteVM{
			Chemin: e.Nom, Nom: e.Nom, Niveau: pose[e.Nom], Dedans: dedans[e.Nom],
		})
		var noms []string
		for n := range enfants[e.Nom] {
			noms = append(noms, n)
		}
		sort.Strings(noms)
		for _, n := range noms {
			chemin := e.Nom + "/" + n
			out = append(out, noeudCohorteVM{
				Chemin: chemin, Nom: n, Enfant: true,
				Niveau: pose[chemin], Dedans: dedans[chemin],
			})
		}
	}
	return out, nil
}

// GET /admin/cohortes : l'ecran de plein droit des cohortes.
//
// Il rend exactement ce que l'encart de l'ecran des skills rendait, au meme
// modele (groupesDuDepot). Ce qui change est ou il vit : le concept que
// l'administration doit avoir au centre cesse d'etre un reglage d'une page qui
// parle d'autre chose.
//
// Reserve aux administrateurs par sa route, comme l'encart l'etait deja par son
// `{{if .Gestion}}`.
func (s *Server) handleCohortes(w http.ResponseWriter, r *http.Request, u *db.User) {
	// `okg` porte l'id de la cohorte qui vient d'écrire, `ou` le panneau d'où
	// le geste est parti (voir `retourGroupe`). Illisible ou absent : on
	// confirme sans rien rouvrir plutôt que de refuser - ce sont des paramètres
	// d'affichage, pas des droits, et `ou` ne sert qu'à comparer à une ancre
	// que le gabarit fabrique lui-même.
	confirme, _ := strconv.ParseInt(r.URL.Query().Get("okg"), 10, 64)
	s.renderCohortes(w, http.StatusOK, u, r.URL.Query().Has("okg"), confirme, r.URL.Query().Get("ou"), "")
}

func (s *Server) renderCohortes(w http.ResponseWriter, status int, u *db.User, confirme bool, quelle int64, ou, erreur string) {
	users, err := s.DB.ListUsers()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	groupes, err := s.groupesDuDepot(u, users)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	cochables, err := s.skillsCochables(u)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	var dossiers, skillsCohortes []groupeVM
	for _, g := range groupes {
		if g.Genre == db.GenreSkill {
			skillsCohortes = append(skillsCohortes, g)
			continue
		}
		dossiers = append(dossiers, g)
	}
	sections := []sectionCohortesVM{{
		Titre: "Cohortes de dossiers", Ancre: "dossiers", Colonne: "Dossiers portés",
		Vide:    "Aucune cohorte de dossiers. Créez-en une ci-dessus pour donner un paquet de dossiers à plusieurs comptes d'un seul geste.",
		Dossier: true, Groupes: dossiers,
	}, {
		Titre: "Cohortes de skills", Ancre: "skills", Colonne: "Skills portés",
		Vide:    "Aucune cohorte de skills. Un skill rangé dans une cohorte se projette vers l'agent de chacun de ses membres.",
		Groupes: skillsCohortes,
	}}
	render(w, status, "cohortes", cohortesData{
		baseData: base(u, "cohortes"), Groupes: groupes, Sections: sections,
		Skills:  cochables,
		Niveaux: niveauxVM, ConfirmeGroupes: confirme, Confirme: quelle, Ou: ou, Erreur: erreur,
	})
}

// skillsCochables : les skills, reduits a ce que le panneau de composition
// affiche - le slug, et les cohortes qui le portent.
//
// Volontairement PAS le skillVM complet de l'ecran des skills : celui-la calcule
// en plus les tags, la matrice d'acces par compte et les noms de groupes, dont
// aucun n'apparait ici. Reutiliser sa boucle aurait demande de l'extraire, donc
// de faire porter a l'ecran des skills un refactor qui ne le sert pas.
func (s *Server) skillsCochables(u *db.User) ([]skillVM, error) {
	list, err := s.skillsDuDepot(u)
	if err != nil {
		return nil, err
	}
	out := make([]skillVM, 0, len(list))
	for _, sk := range list {
		ids, err := s.DB.GroupesDuChemin(skillPath(sk.Slug), "skill")
		if err != nil {
			return nil, err
		}
		vm := skillVM{Slug: sk.Slug, DansGroupes: make(map[int64]bool, len(ids))}
		for _, id := range ids {
			vm.DansGroupes[id] = true
		}
		out = append(out, vm)
	}
	return out, nil
}

// POST /admin/cohortes/niveau-chemin : le niveau qu'une cohorte accorde sur un
// de ses dossiers.
//
// C'EST LE GESTE « ce sous-dossier n'est pas pour cette cohorte ». La cohorte
// porte `shared` en lecture ; on range `shared/direction` dedans et on lui pose
// `invisible`, et les membres cessent de le voir sans qu'aucune regle
// individuelle ne soit ecrite.
//
// Un niveau vide RETIRE le reglage : le chemin reprend le niveau du membre.
// Sans ce cas, revenir en arriere demanderait de sortir le chemin de la cohorte
// puis de l'y remettre, et ce detour se paierait en droits mal poses.
// POST /admin/cohortes/retirer-chemin : sortir UN chemin d'une cohorte.
//
// POURQUOI CETTE ROUTE EXISTE, et c'est un trou que le déménagement du 02/09 a
// ouvert avant de le refermer. Les cases « dossiers » du panneau ne portent que
// les ESPACES - c'est ce qui les rend sûres, un chemin profond ne peut pas être
// retiré par un panneau resté ouvert. Mais du coup, un chemin profond rangé
// dans une cohorte n'avait plus AUCUNE surface de retrait, la page du dossier
// ayant cessé d'en être une. Un réglage qu'on peut poser et pas défaire est un
// cul-de-sac.
//
// Le geste est explicite et porte sur un seul chemin : il ne peut donc pas
// emporter autre chose que ce que le mainteneur a désigné, ce qui est
// exactement la propriété que l'ensemble coché ne peut pas offrir.
func (s *Server) handleRetirerChemin(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "formulaire invalide")
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("groupe_id"), 10, 64)
	if err != nil {
		s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "cohorte introuvable")
		return
	}
	chemin := perms.Canon(r.PostFormValue("chemin"))
	if chemin == "" {
		s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "chemin manquant")
		return
	}
	if err := s.DB.SortChemin(id, chemin); err != nil {
		log.Printf("web: sortie de %s de la cohorte %d : %v", chemin, id, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	retourGroupe(w, r, id, ancreGroupe("groupe-", id))
}

func (s *Server) handleSetNiveauChemin(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "formulaire invalide")
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("groupe_id"), 10, 64)
	if err != nil {
		s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "cohorte introuvable")
		return
	}
	chemin := perms.Canon(r.PostFormValue("chemin"))
	brut := r.PostFormValue("niveau")
	if brut != "" {
		if _, ok := perms.ParseLevel(brut); !ok {
			s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "niveau invalide")
			return
		}
	}
	// LA ZONE DES SKILLS D'ABORD, et c'est vital ici : `RangeChemin` fait un
	// upsert qui RÉÉCRIT le genre. Ranger un chemin de skill comme « dossier »
	// le ferait disparaître de l'écran des skills sans rien dire à personne.
	if motif := refusZoneSkills(chemin); motif != "" {
		s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", motif)
		return
	}
	// CE FORMULAIRE RANGE AUSSI, depuis le 02/09, et sans lui il ne servirait
	// plus à rien. Il ne posait qu'un niveau sur un chemin déjà rangé, et son
	// propre texte disait « rangez-le d'abord depuis sa page » - or la page du
	// dossier a cessé d'être une surface de rangement le même jour. Les cases
	// du panneau, elles, ne portent que les espaces. Un sous-dossier n'avait
	// donc plus AUCUNE porte d'entrée dans une cohorte.
	//
	// `RangeChemin` ne touche pas au niveau (son upsert ne réécrit que genre et
	// slug), donc l'ordre range-puis-règle est sûr, et rejouer le formulaire sur
	// un chemin déjà rangé ne change rien d'autre que ce qu'on vient de saisir.
	if err := s.DB.RangeChemin(id, chemin, "dossier", ""); err != nil {
		log.Printf("web: rangement de %s dans la cohorte %d : %v", chemin, id, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	if err := s.DB.SetNiveauChemin(id, chemin, brut); err != nil {
		log.Printf("web: niveau de %s dans la cohorte %d : %v", chemin, id, err)
		s.renderCohortes(w, http.StatusBadRequest, u, false, 0, "", "ce dossier n'est pas rangé dans cette cohorte")
		return
	}
	retourGroupe(w, r, id, ancreGroupe("groupe-", id))
}
