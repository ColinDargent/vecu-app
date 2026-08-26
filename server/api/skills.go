// Endpoints API des skills (modèle F3 : privés par défaut, gérés par leur
// créateur). Consommés par l'écran Skills natif de F1 ; le web admin garde sa
// propre page. Le serveur reste seul évaluateur de droits : ces handlers
// exposent la résolution existante, ils ne la réimplémentent pas.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
)

type skillDTO struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Creator     string `json:"creator"` // username ; "" si créateur inconnu/supprimé
	Niveau      string `json:"niveau"`  // droit effectif de l'appelant
	Projetable  bool   `json:"projetable"`
}

// creatorName : username du créateur d'un skill, "" si non revendiqué ou créateur
// supprimé. Sert l'AFFICHAGE, jamais le filtrage de visibilité.
func (s *Server) creatorName(slug string) string {
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

// GET /skills : les skills visibles par l'appelant (les siens + ceux partagés
// avec lui), avec leur créateur et le droit effectif de l'appelant.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	files, err := s.Store.List("")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	read := func(p string) (string, error) { return s.Store.Read(p, "") }
	list := skills.List(skills.DefaultRoot, files, u.DefaultLevel, rules, read)
	out := make([]skillDTO, 0, len(list))
	for _, sk := range list {
		out = append(out, skillDTO{
			Slug: sk.Slug, Name: sk.Name, Description: sk.Description,
			Creator: s.creatorName(sk.Slug), Niveau: sk.Niveau.String(), Projetable: sk.Projetable,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}

// nouveauDTO : ce que le signal de naissance porte, et RIEN de plus. Pas de
// description, pas de contenu, pas de niveau d'accès.
type nouveauDTO struct {
	Slug   string `json:"slug"`
	Auteur string `json:"auteur"`
	CreeLe string `json:"cree_le"`
}

// GET /skills/nouveaux : les skills déposés par les AUTRES, pour que leur
// existence cesse d'être un secret.
//
// ATTENTION, ET NE PAS « CORRIGER » : cet endpoint révèle délibérément le nom
// d'un skill et son auteur à des comptes qui n'ont PAS le droit de le lire. Il
// contredit frontalement la règle appliquée trente lignes plus bas par
// `handleSkillAcces` (« un non-créateur reçoit 404 : on ne révèle pas
// l'existence d'un skill qu'il ne gère pas »), et la contradiction est le
// SUJET, pas un oubli.
//
// Depuis le 28/07 un skill naît privé, propriété de son auteur. Ce choix est
// bon - il a été pris après un incident - mais il rend le partage impossible à
// AMORCER : personne n'apprend jamais qu'un skill existe, donc personne ne
// demande à le voir, donc rien ne se partage. Mesuré : 25 skills, tous de
// `colin`, et aucune sonde ne peut dire depuis son compte si Achille en a
// déposé, puisque l'API filtre par droits sans exemption admin.
//
// Le prix est acté : un slug est parfois parlant. Le signal s'arrête là -
// jamais la description, jamais le contenu, et le partage reste un geste
// volontaire de l'auteur.
func (s *Server) handleSkillsNouveaux(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	revs, err := s.DB.RevendicationsSauf(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	out := make([]nouveauDTO, 0, len(revs))
	for _, rev := range revs {
		out = append(out, nouveauDTO{Slug: rev.Slug, Auteur: rev.Auteur, CreeLe: rev.CreeLe})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nouveaux": out})
}

type accesDTO struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Niveau   string `json:"niveau"`
}

// GET /skills/{slug}/acces : la matrice d'accès d'un skill. Réservé au créateur.
// Un non-créateur reçoit 404 : on ne révèle pas l'existence d'un skill qu'il ne
// gère pas.
func (s *Server) handleSkillAcces(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	slug := r.PathValue("slug")
	if espaces.ValidNom(slug) != nil {
		writeErr(w, http.StatusBadRequest, "slug invalide")
		return
	}
	if !s.DB.EstCreateur(u.ID, slug) {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	users, err := s.DB.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	path := skills.DefaultRoot + "/" + slug
	out := make([]accesDTO, 0, len(users))
	for _, x := range users {
		rules, err := s.DB.Rules(x.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "erreur interne")
			return
		}
		out = append(out, accesDTO{
			UserID: x.ID, Username: x.Username,
			Niveau: perms.Effective(path, x.DefaultLevel, rules).String(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "acces": out})
}

// POST /skills/{slug}/acces : pose le droit d'un utilisateur sur CE skill.
// Réservé au créateur ; la règle est bornée à shared/skills/<slug>, jamais
// ailleurs, et ne touche pas le statut admin (anti-escalade par construction).
func (s *Server) handleSetSkillAcces(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	slug := r.PathValue("slug")
	if espaces.ValidNom(slug) != nil {
		writeErr(w, http.StatusBadRequest, "slug invalide")
		return
	}
	if !s.DB.EstCreateur(u.ID, slug) {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	var req struct {
		UserID int64  `json:"user_id"`
		Niveau string `json:"niveau"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "corps JSON invalide")
		return
	}
	niveau, ok := perms.ParseLevel(req.Niveau)
	if !ok {
		writeErr(w, http.StatusBadRequest, "niveau invalide")
		return
	}
	if _, err := s.DB.UserByID(req.UserID); err != nil {
		writeErr(w, http.StatusBadRequest, "utilisateur introuvable")
		return
	}
	if err := s.DB.SetPermission(req.UserID, skills.DefaultRoot+"/"+slug, niveau); err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
