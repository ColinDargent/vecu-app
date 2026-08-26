// Package api : couche HTTP JSON du serveur Vécu. Seul point d'entrée du
// client ; chaque requête passe par l'authentification (bearer par appareil)
// et l'évaluation des droits. Le client ne voit jamais git.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
	"github.com/colindargent/vecu/server/storage"
	"github.com/colindargent/vecu/server/tickets"
)

// skillSlug renvoie le slug si `p` est un fichier SOUS un dossier de skill
// (`shared/skills/<slug>/…`), et "" sinon (fichier posé directement dans
// `shared/skills/`, hors de la zone skills, ou slug de segment invalide). Sert à
// reconnaître une écriture qui crée/alimente un skill, pour l'appropriation.
func skillSlug(p string) string {
	reste, ok := strings.CutPrefix(perms.Canon(p), skills.DefaultRoot+"/")
	if !ok {
		return ""
	}
	slug, _, aFichier := strings.Cut(reste, "/")
	if !aFichier || espaces.ValidNom(slug) != nil {
		return ""
	}
	return slug
}

// sousSkills indique si `p` appartient à la zone skills (shared/skills/…). Sur
// cette zone, AUCUNE exemption admin ne s'applique : un skill est privé même de
// l'admin (modèle F3). Sert à borner les rares court-circuits admin (export ZIP).
func sousSkills(p string) bool {
	return strings.HasPrefix(perms.Canon(p), skills.DefaultRoot+"/")
}

// maxBodyBytes borne la taille d'un corps de PUT (garde-fou v1, vault de notes).
const maxBodyBytes = 32 << 20 // 32 Mio

// Server câble la base (users/droits) et le store (contenu git).
type Server struct {
	DB    *db.DB
	Store *storage.Store
	// Now : horloge injectable (nom des copies de conflit). Nil = time.Now.
	Now func() time.Time
	// AppDist : dossier des artefacts de release de l'app de bureau (manifeste
	// scellé + binaires par cible), servis par /app/manifest et /app/download.
	// Vide = aucune release publiée (les deux routes rendent 404).
	AppDist string
	// Tickets : les billets d'entrée à usage unique, PARTAGÉS avec le serveur
	// web - l'un les émet, l'autre les consomme. Nil = la route rend 501, ce qui
	// est le bon comportement pour un déploiement qui n'a pas câblé le magasin
	// plutôt qu'un panic sur un pointeur nul.
	Tickets *tickets.Store
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type ctxKey int

const userKey ctxKey = 0

// Handler construit le routeur HTTP. Les routes de contenu sont protégées par
// le middleware d'authentification ; /health reste public.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.Handle("GET /espaces", s.auth(http.HandlerFunc(s.handleEspaces)))
	mux.Handle("POST /espaces", s.auth(http.HandlerFunc(s.handleCreerEspace)))
	mux.Handle("GET /tree", s.auth(http.HandlerFunc(s.handleTree)))
	mux.Handle("GET /sync", s.auth(http.HandlerFunc(s.handleSync)))
	mux.Handle("GET /files/{path...}", s.auth(http.HandlerFunc(s.handleGetFile)))
	mux.Handle("PUT /files/{path...}", s.auth(http.HandlerFunc(s.handlePutFile)))
	mux.Handle("DELETE /files/{path...}", s.auth(http.HandlerFunc(s.handleDeleteFile)))
	mux.Handle("GET /log/{path...}", s.auth(http.HandlerFunc(s.handleLog)))
	mux.Handle("POST /restore/{path...}", s.auth(http.HandlerFunc(s.handleRestore)))
	mux.Handle("GET /export", s.auth(http.HandlerFunc(s.handleExport)))
	// Skills : liste visible par l'appelant, et gestion des accès réservée au
	// créateur (modèle F3). Surface consommée par l'écran Skills natif (F1).
	mux.Handle("GET /skills", s.auth(http.HandlerFunc(s.handleSkills)))
	// AVANT la route {slug} : « nouveaux » n'est pas un slug. ServeMux choisit le
	// motif le plus spécifique, mais l'ordre explicite évite d'en dépendre.
	// Source: https://pkg.go.dev/net/http#ServeMux
	mux.Handle("GET /skills/nouveaux", s.auth(http.HandlerFunc(s.handleSkillsNouveaux)))
	mux.Handle("GET /skills/{slug}/acces", s.auth(http.HandlerFunc(s.handleSkillAcces)))
	mux.Handle("POST /skills/{slug}/acces", s.auth(http.HandlerFunc(s.handleSetSkillAcces)))
	// Auto-update de l'app de bureau : manifeste scellé + binaires. Bearer requis
	// (le contenu est déjà signé ; l'auth évite d'exposer les binaires en clair).
	mux.Handle("POST /session-web", s.auth(http.HandlerFunc(s.handleSessionWeb)))
	mux.Handle("GET /app/manifest", s.auth(http.HandlerFunc(s.handleAppManifest)))
	mux.Handle("GET /app/download", s.auth(http.HandlerFunc(s.handleAppDownload)))
	return mux
}

// loginRequest / loginResponse : échange identifiants → jeton d'appareil.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Device   string `json:"device"` // libellé de l'appareil (optionnel)
}

type loginResponse struct {
	Token    string `json:"token"`
	Username string `json:"username"`
}

// POST /login : authentifie et émet un jeton d'appareil. Public (pas de bearer).
// L'anti-énumération de compte est portée par db.Authenticate (hash factice).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "corps JSON invalide")
		return
	}
	u, err := s.DB.Authenticate(req.Username, req.Password)
	if errors.Is(err, db.ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, "identifiants invalides")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	token, err := s.DB.IssueToken(u.ID, req.Device)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: token, Username: u.Username})
}

// auth : extrait le bearer token, résout l'utilisateur, l'attache au contexte.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "jeton manquant")
			return
		}
		u, err := s.DB.UserByToken(token)
		if errors.Is(err, db.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "jeton invalide")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "erreur interne")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userFrom(r *http.Request) *db.User { return r.Context().Value(userKey).(*db.User) }

// GET /tree : liste des chemins visibles (lecture ou plus) par l'utilisateur.
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	all, err := s.Store.List("")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	var visible []string
	for _, p := range all {
		if perms.CanRead(p, u.DefaultLevel, rules) {
			visible = append(visible, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": visible})
}

// espaceResponse : un espace tel que rendu au client.
//
// Le niveau de droit n'est volontairement PAS exposé : un espace peut être
// listé alors que sa racine est invisible (droit accordé sur un sous-dossier
// seulement). Un client qui déciderait de monter d'après le niveau casserait
// précisément ce cas. La présence dans cette liste EST l'autorisation de monter.
type espaceResponse struct {
	Nom string `json:"nom"`
	// Libelle : ce que l'humain lit. Vide = l'espace s'affiche sous son nom
	// technique, repli prévu par DT3. `omitempty` : un client d'une version
	// antérieure ne voit tout simplement pas le champ.
	Libelle  string `json:"libelle,omitempty"`
	Fichiers int    `json:"fichiers"`
	// Ecriture : au moins un chemin de l'espace est modifiable. Sans cette
	// information, un espace en lecture seule est indiscernable d'un espace
	// modifiable : l'utilisateur édite, ses écritures sont refusées, et le
	// dossier reste visuellement normal.
	Ecriture bool `json:"ecriture"`
}

// GET /espaces : espaces visibles par l'utilisateur. C'est la source du
// montage automatique côté client : le client aligne ses dossiers locaux sur
// cette liste à chaque cycle.
func (s *Server) handleEspaces(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	all, err := s.Store.List("")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	list := espaces.List(all, u.DefaultLevel, rules)
	out := make([]espaceResponse, 0, len(list))
	for _, e := range list {
		// Le libellé voyage avec la liste. Le client n'a pas à aller le chercher
		// fichier par fichier, et un espace qui n'en a pas s'affiche sous son nom
		// technique - le repli prévu par DT3.
		out = append(out, espaceResponse{
			Nom: e.Nom, Libelle: espaces.LitLibelle(s.Store, e.Nom),
			Fichiers: e.Fichiers, Ecriture: e.Ecriture,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"espaces": out})
}

// POST /espaces : créer un espace depuis l'app, à partir d'un libellé humain.
//
// DÉCISION D'AUTORISATION, et elle est délibérée : **tout compte authentifié
// peut créer un espace**, pas seulement un administrateur. La route web
// équivalente (`POST /admin/espaces`) reste, elle, réservée aux admins.
//
// Pourquoi la différence. Le parcours A du CDC - « je partage un dossier qui
// existe déjà chez moi » - est le geste central de la V1 publique. Le réserver
// aux admins le rendrait impossible pour Achille, donc pour tout le monde sauf
// une personne, et viderait la fonction de son sens.
//
// Ce que ça n'ouvre PAS, et c'est ce qui rend la décision tenable : le créateur
// reçoit l'écriture sur SON espace et personne d'autre ne reçoit rien. C'est le
// modèle posé par F3 le 28/07 pour les skills - ça naît privé, propriété de son
// auteur - appliqué au même endroit avec les mêmes primitives. Créer un espace
// ne donne accès à rien qu'on n'avait pas, et n'en retire à personne.
func (s *Server) handleCreerEspace(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Libelle string `json:"libelle"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if strings.TrimSpace(req.Libelle) == "" {
		writeErr(w, http.StatusBadRequest, "libellé vide")
		return
	}
	nom, err := espaces.CreateDepuisLibelle(s.Store, req.Libelle, u.Username)
	if nom == "" {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// L'espace existe : le créateur doit pouvoir y écrire, sinon il vient de
	// créer un dossier qu'il ne voit pas.
	if err := s.DB.SetPermission(u.ID, nom, perms.Ecriture); err != nil {
		writeErr(w, http.StatusInternalServerError, "espace créé, mais le droit d'écriture n'a pas pu être posé : "+err.Error())
		return
	}
	// Le nom RETENU est rendu, et c'est une exigence de DT3 : quelqu'un qui
	// partage « Notes » et reçoit `notes-2` doit l'apprendre maintenant, pas le
	// découvrir un jour dans un chemin.
	reponse := map[string]any{"nom": nom, "libelle": strings.TrimSpace(req.Libelle)}
	if err != nil {
		// Espace créé mais libellé non écrit : utilisable, affiché sous son nom
		// technique. On le dit plutôt que de le taire.
		reponse["avertissement"] = err.Error()
	}
	writeJSON(w, http.StatusOK, reponse)
}

// GET /files/{path...} : contenu d'un fichier si l'utilisateur peut le lire.
// Un chemin hors droit renvoie 404 (indistinguable d'un fichier inexistant :
// on ne révèle pas l'existence d'un contenu hors périmètre).
func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	p := r.PathValue("path")
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	if !perms.CanRead(p, u.DefaultLevel, rules) {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	// Le head D'ABORD, puis la lecture À CE head. La lecture à la révision
	// implicite ne dirait pas de quel commit sort le contenu, et le client en est
	// réduit à supposer - or ce qu'il inscrit là devient l'ancêtre commun de la
	// prochaine fusion. Dans cet ordre, le couple (contenu, commit) est cohérent
	// même si main avance entre les deux appels.
	head, err := s.Store.Head()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	content, err := s.Store.Read(p, head)
	if err != nil {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": p, "content": content, "commit": head})
}

// putRequest : corps d'un PUT /files/{path}.
type putRequest struct {
	Content string `json:"content"`
	BaseOID string `json:"base_oid"` // version que le client détenait (curseur de sync)
}

// writeResponse : issue d'une écriture, renvoyée au client.
type writeResponse struct {
	Head         string `json:"head"`
	Merged       bool   `json:"merged"`
	Conflict     bool   `json:"conflict"`
	ConflictPath string `json:"conflict_path,omitempty"`
}

// PUT /files/{path...} : écriture avec base_oid et fusion (modèle Dropbox).
// L'écriture exige le droit d'écriture ; un chemin hors droit renvoie 404
// (comme la lecture, on ne révèle pas l'existence hors périmètre).
func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	p := r.PathValue("path")

	// Tout fichier vit dans un espace au nom valide. Contrôlé AVANT les droits :
	// c'est la forme du chemin qui est en cause, pas le périmètre, et un chemin
	// accepté ici mais orphelin serait stocké puis synchronisé nulle part.
	if err := espaces.ValidChemin(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var req putRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "corps JSON invalide")
		return
	}
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	// Appropriation d'un skill : la PREMIÈRE écriture sous shared/skills/<slug>/
	// (aucun fichier du skill n'existe encore dans le dépôt) est une CRÉATION,
	// autorisée pour tout compte authentifié malgré le « shared/skills = invisible »
	// par défaut. La revendication est atomique (claim + grant écriture dans une
	// transaction) et se fait AVANT l'écriture : le gagnant écrit en propriétaire,
	// un créateur concurrent perdant retombe sur les droits normaux (donc refusé,
	// il n'écrit pas). Un skill déjà existant (fichiers présents dans git) n'est
	// jamais « créable » : on ne le revendique pas, ses écritures suivent les
	// droits normaux - un compte tiers ne peut pas s'en emparer.
	slug := skillSlug(p)
	creation := false
	if slug != "" && !s.Store.Exists(skills.DefaultRoot+"/"+slug) {
		claimed, err := s.DB.ClaimSkill(slug, skills.DefaultRoot+"/"+slug, u.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "erreur interne")
			return
		}
		creation = claimed
	}
	if !creation {
		if !perms.CanRead(p, u.DefaultLevel, rules) {
			writeErr(w, http.StatusNotFound, "introuvable")
			return
		}
		if !perms.CanWrite(p, u.DefaultLevel, rules) {
			writeErr(w, http.StatusForbidden, "écriture non autorisée")
			return
		}
	}

	// Création d'un espace par écriture : refuser un homonyme de casse
	// différente. APFS et HFS+ étant insensibles à la casse, « clients » et
	// « Clients » atterriraient dans un seul dossier local ; la sync verrait
	// alors les fichiers de l'un comme supprimés et ceux de l'autre comme
	// nouveaux, et déplacerait le contenu d'un périmètre de droits vers l'autre.
	// Le parcours ne coûte que sur le premier fichier d'un espace.
	if nom, _, _ := strings.Cut(p, "/"); !s.Store.Exists(nom) {
		occupation, err := espaces.Occupation(s.Store, nom)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "erreur interne")
			return
		}
		if occupation != espaces.Libre {
			writeErr(w, http.StatusBadRequest,
				"un espace ou un fichier porte déjà ce nom avec une casse différente : "+nom)
			return
		}
	}

	res, err := s.Store.WriteMerge(p, req.Content, u.Username, req.BaseOID, conflictName(p, u.Username, s.now()))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "écriture refusée")
		return
	}
	// La copie de conflit naît dans le dossier de l'original : couverte par la
	// même règle quand le droit vient d'un dossier ancêtre. Limite connue : si
	// le droit du client vient d'une règle posée sur le fichier précis, la
	// copie n'est pas couverte et peut sortir de son périmètre (elle reste
	// visible de l'admin, rien n'est perdu).
	writeJSON(w, http.StatusOK, writeResponse{
		Head: res.Head, Merged: res.Merged,
		Conflict: res.Conflict, ConflictPath: res.ConflictPath,
	})
}

// DELETE /files/{path...} : suppression avec base_oid et fusion.
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	p := r.PathValue("path")
	baseOID := r.URL.Query().Get("base_oid")

	// La même garde que le PUT, et pour la même raison : depuis que la suppression
	// range la version de main dans une copie, ce handler ÉCRIT. Sans ce contrôle
	// il pourrait déposer une copie sur un chemin que le PUT refuse - hors espace,
	// donc hors de tout dossier local, donc une copie que la sync ne peut ni
	// descendre ni effacer.
	if err := espaces.ValidChemin(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	if !perms.CanRead(p, u.DefaultLevel, rules) {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	if !perms.CanWrite(p, u.DefaultLevel, rules) {
		writeErr(w, http.StatusForbidden, "écriture non autorisée")
		return
	}
	res, err := s.Store.DeleteMerge(p, u.Username, baseOID, conflictName(p, u.Username, s.now()))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "suppression refusée")
		return
	}
	// `ConflictPath` rendu, comme pour l'écriture : une suppression qui croise
	// une modification range désormais cette modification dans une copie, et le
	// client doit pouvoir la nommer. Sans ce champ, la seule trace émise par ce
	// chemin annonçait « copie créée () ».
	writeJSON(w, http.StatusOK, writeResponse{
		Head: res.Head, Merged: res.Merged,
		Conflict: res.Conflict, ConflictPath: res.ConflictPath,
	})
}

// conflictName forge le nom de la copie de conflit :
// « nom (conflit YYYY-MM-DD HHhMM - user).ext », l'extension préservée.
func conflictName(p, user string, now time.Time) string {
	dir, base := path.Split(p)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	suffix := fmt.Sprintf(" (conflit %s - %s)", now.Format("2006-01-02 15h04"), user)
	return dir + stem + suffix + ext
}

// syncChange : une entrée de delta renvoyée au client.
type syncChange struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted"`
	Content string `json:"content,omitempty"`
}

// GET /sync?since=<oid> : delta filtré au périmètre depuis la révision connue
// du client. Renvoie la tête courante + les changements visibles.
//
// Limite connue : le delta est piloté par les commits git ; une révocation de
// droit (SQLite, sans commit) ne génère aucun changement et ne retire donc pas
// un fichier déjà en cache local. C'est le scan de réconciliation du client
// (slice 6, via GET /tree = périmètre complet) qui retire les fichiers locaux
// sortis du périmètre. /sync reste, lui, toujours filtré (aucune fuite).
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	since := r.URL.Query().Get("since")

	// Un since inconnu/malformé resync (jamais d'erreur bloquante). head est le
	// curseur figé : on lit les contenus à ce head, pas à main flottant.
	head, changes, err := s.Store.Diff(since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}

	out := make([]syncChange, 0, len(changes))
	for _, c := range changes {
		if !perms.CanRead(c.Path, u.DefaultLevel, rules) {
			continue // hors périmètre : jamais exposé, même en suppression
		}
		sc := syncChange{Path: c.Path, Deleted: c.Deleted}
		if !c.Deleted {
			content, err := s.Store.Read(c.Path, head)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "erreur interne")
				return
			}
			sc.Content = content
		}
		out = append(out, sc)
	}
	writeJSON(w, http.StatusOK, map[string]any{"head": head, "changes": out})
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return h[len(prefix):], true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// POST /session-web : un billet à usage unique pour ouvrir l'admin web sans
// ressaisir de mot de passe.
//
// CE QUE CETTE ROUTE NE FAIT PAS, et c'est tout son intérêt : elle n'expose pas
// le jeton d'appareil. Le refus du 29/07 - « exposer un jeton d'appareil au
// JavaScript serait un recul » - vaut au moins autant pour une URL, qui atterrit
// dans un historique. Le billet rendu ici ne vaut qu'une fois et deux minutes.
//
// Rend un CHEMIN et pas une URL absolue : le serveur ne connaît son adresse
// publique que par l'en-tête `Host`, que le client contrôle. L'app, elle, sait
// à quel serveur elle parle - c'est écrit dans sa configuration.
func (s *Server) handleSessionWeb(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if s.Tickets == nil {
		writeErr(w, http.StatusNotImplemented, "ouverture de session non disponible sur ce serveur")
		return
	}
	jeton, err := s.Tickets.Emet(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"chemin": "/admin/entrer?jeton=" + url.QueryEscape(jeton),
	})
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
