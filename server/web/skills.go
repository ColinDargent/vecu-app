// Page d'administration des skills (ouverte à tout compte, chacun sur son
// périmètre ; la gestion des accès appartient au créateur de chaque skill).
//
// Même geste que /admin/espaces, à un cran de granularité plus fin : un skill est
// un dossier `shared/skills/<slug>/`, et donner (ou couper) l'accès d'une personne
// à un skill se fait ici en un clic. C'est une règle `perms` posée sur le chemin
// du skill - aucun nouveau modèle de droits, aucune table. Le client de la
// personne fait apparaître (ou disparaître) le skill au cycle suivant, et le
// projette vers son agent.
package web

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
)

type skillVM struct {
	Slug        string
	Name        string
	Description string
	Fichiers    int
	Projetable  bool
	Raison      string
	Createur    string // username du créateur ; "" si inconnu/supprimé
	// Gere : l'appelant est le créateur de ce skill, donc son gestionnaire de
	// partage. Commande le rendu de la matrice : sur un skill qu'il ne gère pas,
	// l'annuaire des comptes n'est pas servi du tout (pas masqué en CSS).
	Gere bool
	// MonNiveau : droit effectif de l'appelant, affiché à la place de la matrice
	// sur les skills qu'il ne gère pas.
	MonNiveau string
	Acces     []accesVM
	// Tags : les thématiques lues dans `metadata.tags` du SKILL.md. Elles ne
	// portent aucun droit - le partage se règle par les icônes, jamais par une
	// étiquette.
	Tags []string
	// Editable : l'appelant a le droit d'ÉCRITURE sur ce skill, donc il peut en
	// changer les tags. Distinct de Gere, qui dit s'il en règle le partage : on
	// peut pouvoir écrire dans un skill sans en être le créateur.
	Editable bool
	// DansGroupes : les groupes qui portent ce skill, par id. Vide s'il n'est
	// rangé nulle part. Depuis le 25/08 un skill vit dans PLUSIEURS cohortes,
	// donc ce n'était plus un id unique.
	DansGroupes map[int64]bool
	// NomsGroupes : les mêmes, en clair, pour qui ne peut pas les modifier.
	NomsGroupes []string
	// Resume : les mêmes accès que `Acces`, lus PAR NIVEAU au lieu de par
	// compte. C'est ce qu'on regarde sans déplier - « qui écrit, qui lit, qui
	// n'a rien » - et c'est ce qui remplace la colonne par compte, qui faisait
	// trente boutons par ligne à dix comptes.
	Resume []niveauResumeVM
}

// niveauResumeVM : un niveau et les comptes qui s'y trouvent.
type niveauResumeVM struct {
	Valeur  string
	Libelle string
	// Comptes : les premiers noms seulement. Le résumé doit tenir sur une
	// ligne : à dix comptes, tous les nommer redonnerait à la colonne la
	// largeur qu'on vient de lui retirer, et la liste complète est de toute
	// façon dans le panneau, à un clic.
	Comptes []string
	Reste   int
}

// maxNomsResume : au-delà, le résumé compte au lieu de nommer.
const maxNomsResume = 3

// resumeParNiveau : range les accès d'un skill par niveau, du plus ouvert au
// plus fermé. L'ordre est inverse de niveauxVM parce qu'une phrase se lit dans
// ce sens : on cherche d'abord qui peut écrire.
func resumeParNiveau(acces []accesVM) []niveauResumeVM {
	out := make([]niveauResumeVM, 0, len(niveauxVM))
	for i := len(niveauxVM) - 1; i >= 0; i-- {
		n := niveauxVM[i]
		ligne := niveauResumeVM{Valeur: n.Valeur, Libelle: n.Libelle}
		for _, a := range acces {
			if a.Niveau != n.Valeur {
				continue
			}
			if len(ligne.Comptes) < maxNomsResume {
				ligne.Comptes = append(ligne.Comptes, a.Username)
			} else {
				ligne.Reste++
			}
		}
		out = append(out, ligne)
	}
	return out
}

// creatorNom : username du créateur d'un skill, "" si non revendiqué ou créateur
// supprimé. Affichage seulement, jamais un filtre de visibilité.
func (s *Server) creatorNom(slug string) string {
	id, ok, err := s.DB.CreatorOf(slug)
	if err != nil || !ok {
		return ""
	}
	u, err := s.DB.UserByID(id)
	if err != nil {
		return ""
	}
	return u.Username
}

type skillsData struct {
	baseData
	Skills []skillVM
	// Users n'est peuplé que si l'appelant gère au moins un skill : l'annuaire
	// des comptes ne part pas au navigateur de quelqu'un qui n'a rien à partager.
	Users   []userVM
	Gestion bool // l'appelant gère au moins un skill : la matrice a une raison d'être
	Niveaux []niveauVM
	Erreur  string
	// Groupes : les groupes de skills, avec leurs membres. Servi aux seuls
	// administrateurs - c'est de la gouvernance, comme le panneau d'accès d'un
	// dossier.
	Groupes []groupeVM
	// Confirme : le slug du skill dont le partage vient d'être enregistré. Son
	// panneau s'ouvre et porte le message. Comparé aux skills réellement
	// affichés : un paramètre qui ne correspond à rien est ignoré, jamais
	// réfléchi.
	Confirme string
	// ConfirmeGroupes : un geste de groupe vient d'aboutir.
	ConfirmeGroupes bool
	// TousTags : l'union des tags des skills VISIBLES par l'appelant, triée.
	// Dérivée de ce qui est affiché et de rien d'autre : un tag qui n'existe
	// que sur un skill hors périmètre ne doit pas apparaître dans le filtre,
	// il révélerait l'existence de ce skill.
	TousTags []string
}

// skillsDuDepot : les skills tels que les voit l'administrateur courant. Comme
// pour les espaces, c'est SON périmètre qui sert de référence.
func (s *Server) skillsDuDepot(u *db.User) ([]skills.Info, error) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		return nil, err
	}
	files, err := s.Store.List("")
	if err != nil {
		return nil, err
	}
	read := func(p string) (string, error) { return s.Store.Read(p, "") }
	reels := skills.List(skills.DefaultRoot, files, u.DefaultLevel, rules, read)
	if !u.IsAdmin {
		return reels, nil
	}

	// EXEMPTION ADMINISTRATEUR SUR LES SKILLS, décidée le 21/08. Elle renverse
	// F3, livrée le 28/07 après incident, et le fait en connaissance de cause :
	// sans elle, un administrateur ne peut pas arbitrer les accès à des skills
	// dont il ignore l'existence.
	//
	// TROIS BORNES, et elles font toute la sûreté du geste :
	//
	//  1. Elle vit ICI, dans le chemin WEB, et nulle part ailleurs.
	//     `perms.Effective` n'est pas touché, l'API de sync non plus : un
	//     administrateur LIT un skill dans l'interface, il ne le REÇOIT pas sur
	//     son disque. Sans cette borne, son poste téléchargerait les skills
	//     privés de tout le monde en permanence et les projetterait dans son
	//     `.claude/skills/`. Figé par TestAdminNeSynchronisePasLesSkillsDesAutres.
	//  2. C'est un PLANCHER, pas un plafond. La liste complète donne la
	//     visibilité, les droits RÉELS donnent le niveau. Sans cette fusion,
	//     l'administrateur perdait l'écriture sur ses PROPRES skills et
	//     l'interface lui annonçait « vous êtes en lecture seule » sur un skill
	//     qu'il venait d'écrire - défaut trouvé à l'écran, pas par un test.
	//     Figé par TestAdminGardeSesDroitsReelsSurSesSkills.
	//  3. Elle ne franchit pas la racine des skills. `skills.List` ne regarde
	//     que `shared/skills/`, donc rien d'autre du dépôt ne devient lisible
	//     par ce chemin.
	parSlug := make(map[string]skills.Info, len(reels))
	for _, sk := range reels {
		parSlug[sk.Slug] = sk
	}
	complet := skills.List(skills.DefaultRoot, files, perms.Lecture, nil, read)
	for i, sk := range complet {
		if reel, vu := parSlug[sk.Slug]; vu && reel.Niveau > sk.Niveau {
			complet[i].Niveau = reel.Niveau
			complet[i].Ecriture = reel.Ecriture
		}
	}
	return complet, nil
}

// skillPath : le chemin dépôt canonique de la racine d'un skill (là où se pose
// la règle). Canon aligne ce chemin sur celui que skills.List utilise pour
// calculer le droit effectif, même si DefaultRoot venait à changer de forme.
func skillPath(slug string) string { return perms.Canon(skills.DefaultRoot + "/" + slug) }

// skillAccesVM : un skill et le droit qu'y a l'utilisateur affiché. Sert la vue
// miroir de la matrice, sur la fiche d'un compte (comme espaceAccesVM).
type skillAccesVM struct {
	Slug   string
	Name   string
	Niveau string
	// Gere : l'appelant est le créateur de ce skill. Commande le rendu des
	// boutons - sans ça, la fiche proposerait des gestes que le handler refuse
	// désormais en 404.
	Gere      bool
	Profondes []string
}

// skillsMiroir : les skills du dépôt (vus par l'admin) avec le droit effectif de
// `cible` sur chacun. Alimente la section Skills de la fiche d'un compte.
func (s *Server) skillsMiroir(admin, cible *db.User, reglesCible []perms.Rule) ([]skillAccesVM, error) {
	list, err := s.skillsDuDepot(admin)
	if err != nil {
		return nil, err
	}
	out := make([]skillAccesVM, 0, len(list))
	for _, sk := range list {
		p := skillPath(sk.Slug)
		out = append(out, skillAccesVM{
			Slug:      sk.Slug,
			Name:      sk.Name,
			Niveau:    perms.Effective(p, cible.DefaultLevel, reglesCible).String(),
			Gere:      s.DB.EstCreateur(admin.ID, sk.Slug),
			Profondes: reglesInternes(p, reglesCible),
		})
	}
	return out, nil
}

// renderSkills rend la page. `r` sert à lire la confirmation d'un geste qui
// vient d'aboutir (`?ok=` / `?okg=`) : après un POST, le navigateur revient en
// haut d'une page de 220 Ko dont les groupes sont à la fin - sans ancre ni
// message, un geste réussi et un geste sans effet se ressemblent exactement.
func (s *Server) renderSkills(w http.ResponseWriter, r *http.Request, status int, u *db.User, erreur string) {
	// Le périmètre de l'appelant est la seule source : skillsDuDepot part de SES
	// règles, donc un skill qu'il ne voit pas n'entre jamais dans cette page.
	list, err := s.skillsDuDepot(u)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	data := skillsData{baseData: base(u, "skills"), Niveaux: niveauxVM, Erreur: erreur}
	// Une seule interrogation par skill : le résultat sert deux fois (décider si
	// la matrice a lieu d'être, puis la construire ligne par ligne).
	gere := make(map[string]bool, len(list))
	for _, sk := range list {
		gere[sk.Slug] = s.DB.EstCreateur(u.ID, sk.Slug) || u.IsAdmin
		if gere[sk.Slug] {
			data.Gestion = true
		}
	}

	// L'annuaire des comptes n'est chargé que s'il y a quelque chose à partager.
	// Un compte qui ne gère aucun skill ne reçoit pas la liste des utilisateurs.
	var users []db.User
	reglesPar := map[int64][]perms.Rule{}
	if data.Gestion {
		users, err = s.DB.ListUsers()
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		for _, x := range users {
			rules, err := s.DB.Rules(x.ID)
			if err != nil {
				erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
				return
			}
			reglesPar[x.ID] = rules
			// Partager un skill demande de savoir à QUI ; ça ne demande pas de
			// savoir qui est administrateur. Le marqueur reste aux admins.
			data.Users = append(data.Users, userVM{
				ID: x.ID, Username: x.Username, Niveau: niveauFR(x.DefaultLevel),
				IsAdmin: x.IsAdmin && u.IsAdmin,
			})
		}
	}

	// Les groupes sont chargés une fois pour toute la page : le nom du groupe
	// d'un skill s'y lit sans une requête par ligne.
	groupes, err := s.DB.ListGroupes()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	if u.IsAdmin {
		tous, err := s.DB.ListUsers()
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		if data.Groupes, err = s.groupesDuDepot(tous); err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
	}

	for _, sk := range list {
		path := skillPath(sk.Slug)
		vm := skillVM{
			Slug: sk.Slug, Name: sk.Name, Description: sk.Description,
			Fichiers: sk.Fichiers, Projetable: sk.Projetable, Raison: sk.Raison,
			Createur:  s.creatorNom(sk.Slug),
			Gere:      gere[sk.Slug],
			MonNiveau: niveauFR(sk.Niveau),
			Tags:      sk.Tags,
			Editable:  sk.Ecriture,
		}
		// DansGroupes est une MAP parce que deux écrans la lisent : la liste de
		// cases de la ligne du skill, et le panneau « modifier le groupe » d'en
		// face, qui a besoin de « ce skill est-il dans CE groupe ». `index` sait
		// interroger une map dans un template, pas une tranche.
		ids, err := s.DB.GroupesDuChemin(path, "skill")
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		vm.DansGroupes = make(map[int64]bool, len(ids))
		for _, id := range ids {
			vm.DansGroupes[id] = true
		}
		// On parcourt `groupes` (trié par nom) plutôt que `ids` (trié par id) :
		// la phrase « Rangé dans A, B » et la liste de cases d'à côté rendent le
		// MÊME ensemble, elles doivent le rendre dans le même ordre.
		for _, g := range groupes {
			if vm.DansGroupes[g.ID] {
				vm.NomsGroupes = append(vm.NomsGroupes, g.Nom)
			}
		}
		// La matrice n'est construite que pour les skills que l'appelant gère :
		// ailleurs, les droits des autres comptes ne le regardent pas.
		if vm.Gere {
			for _, x := range users {
				vm.Acces = append(vm.Acces, accesVM{
					UserID:    x.ID,
					Username:  x.Username,
					Niveau:    perms.Effective(path, x.DefaultLevel, reglesPar[x.ID]).String(),
					Profondes: reglesInternes(path, reglesPar[x.ID]),
				})
			}
			vm.Resume = resumeParNiveau(vm.Acces)
		}
		data.Skills = append(data.Skills, vm)
	}
	data.TousTags = unionTags(data.Skills)
	// La confirmation ne s'affiche que si elle désigne quelque chose que cette
	// page rend vraiment : sinon un lien fabriqué ferait dire à l'écran qu'un
	// partage a été enregistré alors que rien ne l'a été.
	if ok := r.URL.Query().Get("ok"); ok != "" {
		for _, sk := range data.Skills {
			if sk.Slug == ok && sk.Gere {
				data.Confirme = ok
				break
			}
		}
	}
	data.ConfirmeGroupes = u.IsAdmin && r.URL.Query().Has("okg")
	render(w, status, "skills", data)
}

// unionTags : les tags distincts des skills affichés, dédupliqués et triés sans
// tenir compte de la casse, mais rendus tels qu'ils sont écrits.
//
// Le tri ignore la casse et rien d'autre. Retirer les diacritiques demanderait
// golang.org/x/text, absent de go.mod, et la spec pose « zéro dépendance
// nouvelle » : un tag accentué se range après les autres, ce qui est un ordre
// d'affichage discutable et jamais une perte. Le filtre du navigateur, lui,
// compare bien sans accents - il sait le faire en une ligne.
func unionTags(list []skillVM) []string {
	vus := map[string]string{}
	for _, sk := range list {
		for _, t := range sk.Tags {
			if _, deja := vus[strings.ToLower(t)]; !deja {
				vus[strings.ToLower(t)] = t
			}
		}
	}
	cles := make([]string, 0, len(vus))
	for k := range vus {
		cles = append(cles, k)
	}
	sort.Strings(cles)
	out := make([]string, 0, len(cles))
	for _, k := range cles {
		out = append(out, vus[k])
	}
	return out
}

// GET /admin/skills : matrice des accès par skill.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request, u *db.User) {
	s.renderSkills(w, r, http.StatusOK, u, "")
}

// valideBascule : les contrôles communs aux surfaces qui écrivent un partage de
// skill - la bascule unitaire d'une fiche de compte, et le panneau complet.
// Un seul endroit, pour que les chemins ne divergent jamais sur ce qui est
// permis ; seule la façon de répondre les distingue.
//
// L'ordre compte : l'autorisation passe AVANT la validation du reste. À qui ne
// gère pas le skill, on ne dit rien - ni qu'il existe, ni si le formulaire
// tenait la route. Même question, même helper que l'API Bearer.
//
// Renvoie status 0 quand la demande passe.
func (s *Server) valideBascule(u *db.User, slug, niveauBrut string, userID int64, idLisible bool) (perms.Level, int, string) {
	// Le slug est un segment de chemin qui deviendra la commande côté Claude :
	// mêmes contraintes qu'un nom d'espace au niveau segment.
	if err := espaces.ValidNom(slug); err != nil {
		return 0, http.StatusBadRequest, "skill, utilisateur ou niveau invalide"
	}
	// Le créateur gère son skill, et depuis le 21/08 un administrateur aussi -
	// c'est l'autre moitié du renversement de F3 : voir sans pouvoir arbitrer
	// n'aurait servi à rien. Pour tout autre compte, 404 avant toute autre
	// validation : on ne révèle pas l'existence d'un skill qu'on ne gère pas.
	if !s.DB.EstCreateur(u.ID, slug) && !u.IsAdmin {
		return 0, http.StatusNotFound, "introuvable"
	}
	niveau, ok := perms.ParseLevel(niveauBrut)
	if !ok || !idLisible {
		return 0, http.StatusBadRequest, "skill, utilisateur ou niveau invalide"
	}
	if _, err := s.DB.UserByID(userID); err != nil {
		return 0, http.StatusBadRequest, "utilisateur introuvable"
	}
	return niveau, 0, ""
}

// POST /admin/skills/partage : l'assignation COMPLÈTE d'un skill, en une fois.
//
// Remplace la colonne par compte, qui faisait trente boutons par ligne à dix
// comptes. Trois propriétés tiennent ce geste, et chacune répare une façon
// précise de mentir à celui qui l'exécute :
//
//  1. **L'autorisation reste dans `valideBascule`**, appelé une fois par
//     compte. C'est l'invariant de juillet : les voies d'écriture partagent ce
//     helper, aucune ne refait ses propres contrôles.
//  2. **Rien n'est écrit tant que tout n'est pas validé.** Un formulaire à
//     moitié accepté laisserait un partage à moitié posé, sans que l'écran
//     puisse dire lequel.
//  3. **On n'écrit QUE ce qui change.** Poser une règle explicite sur un compte
//     qu'on n'a pas touché le détacherait silencieusement de son niveau par
//     défaut : son droit sur ce skill cesserait de suivre son compte. C'est
//     invisible à l'écran le jour même et faux pour toujours après.
func (s *Server) handleSetPartageSkill(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	slug := r.PostFormValue("slug")
	users, err := s.DB.ListUsers()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	chemin := skillPath(slug)

	type poser struct {
		userID int64
		niveau perms.Level
	}
	var aEcrire []poser
	for _, x := range users {
		brut := r.PostFormValue("niveau_" + strconv.FormatInt(x.ID, 10))
		if brut == "" {
			// Compte absent du formulaire : créé depuis que la page a été
			// rendue, ou champ retiré. On ne lui invente pas un niveau.
			continue
		}
		niveau, status, msg := s.valideBascule(u, slug, brut, x.ID, true)
		if status == http.StatusNotFound {
			erreurHTTP(w, msg, status)
			return
		}
		if status != 0 {
			s.renderSkills(w, r, status, u, msg)
			return
		}
		regles, err := s.DB.Rules(x.ID)
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		if perms.Effective(chemin, x.DefaultLevel, regles) == niveau {
			continue
		}
		aEcrire = append(aEcrire, poser{x.ID, niveau})
	}
	for _, p := range aEcrire {
		if err := s.DB.SetPermission(p.userID, chemin, p.niveau); err != nil {
			log.Printf("web: partage du skill %s : %v", slug, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
	}
	retourSkill(w, r, slug)
}

// retourSkill : revenir SUR le panneau qu'on vient d'enregistrer, avec de quoi
// afficher que ça a marché. Le slug vient d'être validé par valideBascule, et
// il est de toute façon revérifié contre les skills affichés au rendu.
func retourSkill(w http.ResponseWriter, r *http.Request, slug string) {
	q := url.Values{"ok": {slug}}.Encode()
	http.Redirect(w, r, "/admin/skills?"+q+"#partage-"+url.PathEscape(slug), http.StatusSeeOther)
}

// POST /admin/skills/acces : pose le droit d'un utilisateur sur un skill
// (`shared/skills/<slug>`). Calqué sur handleSetAccesEspace : SetPermission et
// rien d'autre, redirection bornée à deux destinations connues.
//
// Réservé au CRÉATEUR du skill, sans exemption admin - c'est la même règle que
// l'API Bearer, posée au même endroit (db.EstCreateur). Avant cette slice, le
// handler ne vérifiait que IsAdmin : n'importe quel admin pouvait réattribuer le
// skill d'un autre, ce que F3 interdit en principe.
func (s *Server) handleSetAccesSkill(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	slug := r.PostFormValue("slug")
	userID, errID := strconv.ParseInt(r.PostFormValue("user_id"), 10, 64)
	niveau, status, msg := s.valideBascule(u, slug, r.PostFormValue("niveau"), userID, errID == nil)
	if status == http.StatusNotFound {
		erreurHTTP(w, msg, status)
		return
	}
	if status != 0 {
		s.renderSkills(w, r, status, u, msg)
		return
	}
	if err := s.DB.SetPermission(userID, skillPath(slug), niveau); err != nil {
		log.Printf("web: accès skill : %v", err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	// `retour=user` renvoie sur la fiche du compte, qui est admin-only : depuis
	// que la route est ouverte en session, honorer ce champ pour un non-admin
	// enchaînerait une écriture réussie sur un 403.
	if r.PostFormValue("retour") == "user" && u.IsAdmin {
		http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(userID, 10), http.StatusSeeOther)
		return
	}
	retourSkill(w, r, slug)
}

// POST /admin/skills/tags : réécrit `metadata.tags` du SKILL.md d'un skill.
//
// Autorisé par le DROIT D'ÉCRITURE sur le skill, et pas par la propriété : les
// tags sont une étiquette de rangement, pas une décision de partage. Quelqu'un
// qui peut modifier le contenu du skill peut modifier ses tags - il lui suffit
// sinon d'ouvrir le fichier dans son éditeur, où rien ne l'en empêche.
//
// L'écriture passe par le dépôt, donc elle produit une version dans
// l'historique, elle est restaurable, et elle descend chez les membres au cycle
// suivant comme n'importe quel changement.
func (s *Server) handleSetTags(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	slug := r.PostFormValue("slug")
	if espaces.ValidNom(slug) != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, "skill inconnu")
		return
	}
	chemin := skillPath(slug) + "/SKILL.md"

	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	// 404 et pas 403 sur un skill qu'on ne voit pas : on ne révèle pas
	// l'existence d'un contenu invisible, comme partout ailleurs.
	//
	// L'administrateur voit tous les skills depuis le 21/08, donc il ne peut
	// pas recevoir ici un « introuvable » sur un skill que la liste vient de lui
	// montrer : il obtient le 403 qui dit la vérité - il lit, il n'écrit pas.
	// L'exemption s'arrête là : elle n'ouvre AUCUN droit d'écriture, et le test
	// TestAdminNeModifiePasLeSkillDunAutre le fige.
	if !perms.CanRead(chemin, u.DefaultLevel, rules) && !u.IsAdmin {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}
	if !perms.CanWrite(chemin, u.DefaultLevel, rules) {
		s.renderSkills(w, r, http.StatusForbidden, u, "vous êtes en lecture seule sur « "+slug+" » : ses tags ne peuvent pas être changés d'ici.")
		return
	}

	avant, err := s.Store.Read(chemin, "")
	if err != nil {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}
	tags, err := champTags(r.PostFormValue("tags"))
	if err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, err.Error())
		return
	}
	apres, err := skills.EcrisTags(avant, tags)
	if err != nil {
		s.renderSkills(w, r, http.StatusBadRequest, u, err.Error())
		return
	}
	if apres == avant {
		retourSkill(w, r, slug)
		return
	}

	// Compare-and-swap plutôt qu'une fusion. Relire juste avant d'écrire ferme
	// la fenêtre où quelqu'un d'autre aurait touché ce SKILL.md entre notre
	// lecture et notre écriture : sans ça, poser un tag écraserait sa version.
	//
	// Le choix de REFUSER plutôt que de fusionner est délibéré : une fusion
	// ratée fabriquerait une copie de conflit qui apparaîtrait dans le dossier
	// de tous les membres, pour une étiquette. « Rechargez la page » est une
	// réponse qu'on comprend ; un fichier « (conflit …) » posé sur son disque,
	// non.
	if verif, err := s.Store.Read(chemin, ""); err != nil || verif != avant {
		s.renderSkills(w, r, http.StatusConflict, u,
			"« "+slug+" » a été modifié pendant votre saisie : rechargez la page et recommencez, rien n'a été enregistré.")
		return
	}
	if _, err := s.Store.WriteWithMessage(chemin, apres, u.Username, "tags de "+slug); err != nil {
		log.Printf("web: écriture des tags de %s : %v", slug, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	retourSkill(w, r, slug)
}

// champTags : le texte d'un champ de formulaire vers une liste de tags. Sépare
// sur la virgule ET l'espace, comme le parseur de lecture, pour que « a, b » et
// « a b » donnent la même chose que ce qui sera relu du fichier.
func champTags(brut string) ([]string, error) {
	var out []string
	vus := map[string]bool{}
	for _, champ := range strings.FieldsFunc(brut, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		if err := skills.ValidTag(champ); err != nil {
			return nil, err
		}
		if vus[strings.ToLower(champ)] {
			continue // un doublon n'est pas une erreur, il est simplement ignoré
		}
		vus[strings.ToLower(champ)] = true
		out = append(out, champ)
	}
	if len(out) > 12 {
		return nil, fmt.Errorf("douze tags au maximum sur un skill (%d proposés)", len(out))
	}
	return out, nil
}
