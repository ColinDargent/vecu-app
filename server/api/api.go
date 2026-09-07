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
func sousSkills(p string) bool { return skills.SousRacine(p) }

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
	// Logf : journal du serveur, injectable. Nil = silencieux, ce qui est le bon
	// défaut pour les tests. Le binaire y pose `log.Printf`. Sert aujourd'hui à
	// la surface MCP, dont la négociation de révision ne s'observe que dans les
	// logs du premier branchement d'un vrai client.
	Logf func(string, ...any)
}

// logf : journal best-effort. Un serveur sans `Logf` ne journalise pas, et
// aucun appelant n'a à le savoir.
func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
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
	// Les trois routes fichiers acceptent AUSSI un jeton MCP, plafond compris :
	// c'est ce qui permet à une routine cloud d'écrire en HTTP simple plutôt
	// qu'en JSON-RPC, sans renoncer à un jeton révocable et traçable.
	mux.Handle("GET /files/{path...}", s.authFichiers(http.HandlerFunc(s.handleGetFile)))
	mux.Handle("PUT /files/{path...}", s.authFichiers(http.HandlerFunc(s.handlePutFile)))
	mux.Handle("DELETE /files/{path...}", s.authFichiers(http.HandlerFunc(s.handleDeleteFile)))
	// Le poste dit où il en est, à la fin de chaque cycle. Jeton d'APPAREIL
	// seul : `s.auth` ne résout que `device_tokens`. Voir `etat.go`.
	mux.Handle("POST /etat", s.auth(http.HandlerFunc(s.handleEtat)))
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
	// La surface MCP. Elle ne passe PAS par `s.auth` : son jeton n'est pas un
	// jeton d'appareil, il porte en plus un plafond. POST seul - `ServeMux`
	// répond 405 de lui-même sur GET et DELETE, ce que la révision 2026-07-28
	// demande justement pour un client de l'ère des sessions.
	mux.HandleFunc("POST /mcp", s.handleMCP)
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

// appelant : qui fait cette requête, et sous quelle contrainte.
//
// Deux sortes de jetons atteignent les routes fichiers : le jeton d'APPAREIL
// d'un poste synchronisé, et le jeton MCP d'un programme (routine cloud,
// agent). Le second porte un PLAFOND, et c'est toute la différence : il doit
// s'appliquer ici exactement comme sur la porte `/mcp`, sinon un jeton
// « lecture » écrirait en passant par l'API et la propriété centrale du jalon
// tomberait.
type appelant struct {
	user *db.User
	// porteur : non nil quand l'appel vient d'un jeton MCP.
	porteur *db.PorteurMCP
}

const appelantKey ctxKey = 1

func appelantDe(r *http.Request) appelant {
	a, _ := r.Context().Value(appelantKey).(appelant)
	return a
}

// authFichiers : l'authentification des routes `/files/...`, et d'elles seules.
//
// POURQUOI PAS `s.auth` ÉLARGI. Accepter un jeton MCP partout ouvrirait aussi
// `/export` (dont le ZIP a un court-circuit admin), `/sync`, `/log`,
// `/restore`, `/skills` et `/session-web` - toutes des surfaces où le plafond
// du jeton n'est pas câblé. Élargir en bloc aurait donné à un jeton ce qu'aucun
// écran ne peut lui retirer. Les routes fichiers sont le périmètre exact dont
// une routine a besoin.
func (s *Server) authFichiers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "jeton manquant")
			return
		}
		// Le jeton d'appareil D'ABORD : c'est le cas courant (chaque poste
		// synchronise en permanence), et les deux magasins de hash sont
		// disjoints, donc l'ordre ne change rien au verdict.
		if u, err := s.DB.UserByToken(token); err == nil {
			s.servirAppelant(w, r, next, appelant{user: u})
			return
		} else if !errors.Is(err, db.ErrNotFound) {
			writeErr(w, http.StatusInternalServerError, "erreur interne")
			return
		}
		porteur, err := s.DB.PorteurParJetonMCP(token)
		if errors.Is(err, db.ErrNotFound) {
			// Inconnu et révoqué rendent le même refus, comme sur `/mcp`.
			writeErr(w, http.StatusUnauthorized, "jeton invalide")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "erreur interne")
			return
		}
		// `dernier_usage` avance : c'est un appel accepté, et cette colonne est
		// la seule chose qui rende repérable un jeton oublié dans une config.
		// Sans ça, le jeton effectivement en production serait le seul à
		// paraître mort. Best-effort.
		if err := s.DB.ToucheJetonMCP(porteur.JetonID); err != nil {
			s.logf("api : dernier_usage non mis à jour (jeton %d) : %v", porteur.JetonID, err)
		}
		s.servirAppelant(w, r, next, appelant{user: porteur.Compte(), porteur: porteur})
	})
}

func (s *Server) servirAppelant(w http.ResponseWriter, r *http.Request, next http.Handler, a appelant) {
	ctx := context.WithValue(r.Context(), userKey, a.user)
	ctx = context.WithValue(ctx, appelantKey, a)
	next.ServeHTTP(w, r.WithContext(ctx))
}

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
	p := r.PathValue("path")
	// LE MÊME calcul de droit que l'écriture, plafond du jeton compris. Un
	// `perms.CanRead(p, u.DefaultLevel, rules)` écrit ici ignorerait le
	// plafond - il n'a aujourd'hui aucun effet sur la lecture, mais poser
	// l'exception ici la rendrait invisible le jour où un plafond plus fin
	// existera.
	e, err := s.ecrivainDe(appelantDe(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	if e.niveau(p) < perms.Lecture {
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
	// EmpreinteAttendue : ce que le client croit REMPLACER à ce chemin (DAR-114).
	//
	// Facultatif, et c'est ce qui rend son déploiement sûr : vide, la garde
	// d'écho est désactivée et le serveur se comporte exactement comme avant.
	// Un client d'une version antérieure ne l'envoie pas et ne casse pas ; il
	// n'est simplement pas protégé.
	EmpreinteAttendue string `json:"empreinte_attendue,omitempty"`
}

// writeResponse : issue d'une écriture, renvoyée au client.
type writeResponse struct {
	Head         string `json:"head"`
	Merged       bool   `json:"merged"`
	Conflict     bool   `json:"conflict"`
	ConflictPath string `json:"conflict_path,omitempty"`
}

// PUT /files/{path...} : écriture avec base_oid et fusion (modèle Dropbox).
//
// Le corps de cette écriture vit dans `ecriture.go`, PARTAGÉ avec la porte MCP.
// Ce handler n'est plus que le transport : décoder, appeler, traduire le refus
// en code HTTP. C'est ce qui garantit que les deux portes ne peuvent pas
// diverger - il n'y a qu'un seul jeu de contrôles d'accès à maintenir.
func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")

	// AVANT le décodage du corps, comme dans la version d'origine : c'est la
	// forme du chemin qui est en cause, et un corps illisible masquerait ce
	// diagnostic-là. `ecrisFichier` le revérifie pour son propre compte - c'est
	// une validation, pas un chemin d'écriture, et la dupliquer ne fait dériver
	// personne.
	if err := espaces.ValidChemin(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req putRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "corps JSON invalide")
		return
	}
	e, err := s.ecrivainDe(appelantDe(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	res, err := s.ecrisFichier(e, p, req.Content, req.BaseOID, req.EmpreinteAttendue)
	if err != nil {
		status, msg := statutDe(err)
		writeErr(w, status, msg)
		return
	}
	// La trace qui nomme le jeton, comme sur la porte `/mcp` : une écriture de
	// programme doit rester distinguable d'une écriture de poste.
	if a := appelantDe(r); a.porteur != nil {
		s.journaliseEcritureMCP(a.porteur, "écrit (api)", p, res)
	}
	writeJSON(w, http.StatusOK, res)
}

// DELETE /files/{path...} : suppression avec base_oid et fusion.
//
// Même partage que le PUT : le corps vit dans `ecriture.go`.
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")

	e, err := s.ecrivainDe(appelantDe(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	res, err := s.supprimeFichier(e, p, r.URL.Query().Get("base_oid"))
	if err != nil {
		status, msg := statutDe(err)
		writeErr(w, status, msg)
		return
	}
	if a := appelantDe(r); a.porteur != nil {
		s.journaliseEcritureMCP(a.porteur, "supprimé (api)", p, res)
	}
	// `ConflictPath` rendu, comme pour l'écriture : une suppression qui croise
	// une modification range désormais cette modification dans une copie, et le
	// client doit pouvoir la nommer. Sans ce champ, la seule trace émise par ce
	// chemin annonçait « copie créée () ».
	writeJSON(w, http.StatusOK, res)
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
