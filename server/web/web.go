// Package web : web admin server-rendered de Vécu (html/template, zéro
// framework front). Servi par le même binaire que l'API, monté sur /admin/.
// Session par cookie HttpOnly portant un jeton d'appareil (label « web ») ;
// SameSite=Lax fait office de protection CSRF v1 (les POST cross-site ne
// portent pas le cookie).
// Source: https://pkg.go.dev/html/template
package web

import (
	"bytes"
	"embed"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
	"github.com/colindargent/vecu/server/tickets"
)

// Server câble la base (users/droits) et le store (contenu git) pour le web.
type Server struct {
	DB    *db.DB
	Store *storage.Store
	// Tickets : les billets d'entrée à usage unique, PARTAGÉS avec l'API - elle
	// les émet depuis l'app, cette porte les consomme. Nil = /admin/entrer se
	// comporte comme un billet invalide, donc renvoie au formulaire.
	Tickets *tickets.Store
	// Now : horloge injectable, pour que le rendu des dates relatives des
	// jetons soit testable. Nil = time.Now.
	Now func() time.Time
}

const sessionCookie = "vecu_session"

//go:embed templates/*.html
var templatesFS embed.FS

// pages : un template par page, chacun composé avec le layout.
var pages = func() map[string]*template.Template {
	out := map[string]*template.Template{}
	for _, name := range []string{"login", "accueil", "users", "droits", "skills", "cohortes", "dossiers", "fichier", "historique", "jetons", "etat"} {
		out[name] = template.Must(template.ParseFS(templatesFS,
			"templates/layout.html", "templates/"+name+".html"))
	}
	return out
}()

// Handler construit le routeur du web admin. Toutes les routes vivent sous
// /admin/ ; seul /admin/login est public.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/entrer", s.handleEntrer)
	mux.HandleFunc("GET /admin/login", s.handleLoginForm)
	mux.HandleFunc("POST /admin/login", s.handleLogin)
	mux.HandleFunc("POST /admin/logout", s.handleLogout)
	// L'accueil EST la liste des dossiers : « espace » et « fichier » étaient
	// deux écrans pour un seul objet, et la distinction n'était claire pour
	// personne. Un dossier se liste, s'ouvre, et son accès se règle dedans.
	mux.HandleFunc("GET /admin/{$}", s.session(s.handleAccueil))
	mux.HandleFunc("GET /admin/dossiers", s.session(s.handleDossiers))
	mux.HandleFunc("GET /admin/dossiers/{path...}", s.session(s.handleDossiers))
	mux.HandleFunc("POST /admin/dossiers/acces", s.admin(s.handleSetAcces))
	mux.HandleFunc("POST /admin/dossiers/defaut", s.admin(s.handleSetDefautDossier))
	mux.HandleFunc("POST /admin/dossiers/espaces/supprimer", s.admin(s.handleSupprimerEspace))
	mux.HandleFunc("POST /admin/fichier/supprimer", s.session(s.handleSupprimerFichier))
	// Les adresses d'avant restent servies : elles sont dans des favoris, dans
	// l'historique des navigateurs, et dans le menu de l'app de bureau.
	// L'écran du mainteneur (DAR-198). PAS sur /admin/fichiers, qui sert une
	// redirection posée pour les favoris : qui l'a en favori voulait PARCOURIR
	// des fichiers, pas lire un tableau de santé. Deux besoins, deux adresses.
	mux.HandleFunc("GET /admin/etat", s.admin(s.handleEtatFichiers))
	mux.HandleFunc("GET /admin/fichiers", redirigeVers("/admin/dossiers"))
	mux.HandleFunc("GET /admin/fichiers/{path...}", s.redirigeFichiers)
	mux.HandleFunc("GET /admin/espaces", redirigeVers("/admin/"))
	mux.HandleFunc("GET /admin/conflits", redirigeVers("/admin/"))
	mux.HandleFunc("GET /admin/historique/{path...}", s.session(s.handleHistorique))
	mux.HandleFunc("POST /admin/restaurer/{path...}", s.session(s.handleRestaurer))
	// L'EXPORT EST UN OUTIL D'ADMINISTRATION (Colin, 01/09). Il servait deux
	// publics avec deux règles - le périmètre du lecteur pour un non-admin, tout
	// le dépôt pour un admin - donc son exemption ressemblait à une fuite alors
	// qu'elle est la fonction même de l'outil. Réservé aux administrateurs, elle
	// redevient lisible. La porte API `GET /export` n'est PAS touchée ici : elle
	// est hors du périmètre de ce lot.
	mux.HandleFunc("GET /admin/export.zip", s.admin(s.handleExportZip))
	// Lecture ouverte à tout compte : chacun voit les skills que le résolveur lui
	// accorde, et rien d'autre (cf. skillsDuDepot, qui part de SES règles). Sans
	// ça, la délégation « le créateur gère son skill » (F3) n'a aucune surface :
	// l'API existe mais aucun non-admin ne peut l'atteindre.
	mux.HandleFunc("GET /admin/skills", s.session(s.handleSkills))
	// Écriture ouverte à tout compte, mais bornée au créateur du skill par
	// handleSetAccesSkill (modèle F3 : pas de super-admin skills). Le middleware
	// ne peut pas trancher ici - l'autorisation dépend du slug posté, pas du
	// statut de l'appelant.
	mux.HandleFunc("POST /admin/skills/acces", s.session(s.handleSetAccesSkill))
	// Même geste, sans rechargement. Le formulaire ci-dessus reste le chemin sans
	// JavaScript (il sert aussi droits.html) : les deux partagent valideBascule.
	// Les tags s'écrivent dans le SKILL.md, donc l'autorisation est le droit
	// d'écriture sur le skill, pas le statut de l'appelant : le middleware ne
	// peut pas trancher ici.
	mux.HandleFunc("POST /admin/skills/tags", s.session(s.handleSetTags))
	// Le partage complet d'un skill : même autorisation que la bascule unitaire
	// (le créateur, ou un admin), donc le handler tranche via valideBascule.
	mux.HandleFunc("POST /admin/skills/partage", s.session(s.handleSetPartageSkill))
	// Les groupes : créer, supprimer et régler les membres est réservé aux
	// administrateurs. RANGER un skill est ouvert à son créateur aussi, donc
	// c'est le handler qui tranche, pas le middleware.
	// LES COHORTES ONT LEUR PROPRE ECRAN (DAR-196, 01/09). Elles vivaient dans
	// un encart de l'ecran des skills, qui parle d'autre chose : le concept que
	// l'administration doit avoir au centre etait un reglage d'une page voisine.
	// Les anciennes adresses restent servies, elles sont dans des favoris.
	mux.HandleFunc("GET /admin/cohortes", s.admin(s.handleCohortes))
	mux.HandleFunc("POST /admin/cohortes", s.admin(s.handleCreerGroupe))
	mux.HandleFunc("POST /admin/cohortes/supprimer", s.admin(s.handleSupprimerGroupe))
	mux.HandleFunc("POST /admin/cohortes/membres", s.admin(s.handleSetMembresGroupe))
	mux.HandleFunc("POST /admin/cohortes/modifier", s.admin(s.handleModifierGroupe))
	mux.HandleFunc("POST /admin/cohortes/niveau-chemin", s.admin(s.handleSetNiveauChemin))
	mux.HandleFunc("POST /admin/cohortes/retirer-chemin", s.admin(s.handleRetirerChemin))
	mux.HandleFunc("POST /admin/skills/groupes", s.admin(s.handleCreerGroupe))
	mux.HandleFunc("POST /admin/skills/groupes/supprimer", s.admin(s.handleSupprimerGroupe))
	mux.HandleFunc("POST /admin/skills/groupes/membres", s.admin(s.handleSetMembresGroupe))
	mux.HandleFunc("POST /admin/skills/groupes/modifier", s.admin(s.handleModifierGroupe))
	mux.HandleFunc("POST /admin/skills/ranger", s.session(s.handleRangerSkill))
	// Les jetons MCP : chaque compte gère les siens, donc `session` et pas
	// `admin`. Créer un jeton pour soi n'accorde aucun droit qu'on n'a pas.
	mux.HandleFunc("GET /admin/jetons", s.session(s.handleJetons))
	mux.HandleFunc("POST /admin/jetons", s.session(s.handleCreerJeton))
	mux.HandleFunc("POST /admin/jetons/revoquer", s.session(s.handleRevoquerJeton))
	mux.HandleFunc("GET /admin/users", s.admin(s.handleUsers))
	mux.HandleFunc("POST /admin/users", s.admin(s.handleCreateUser))
	mux.HandleFunc("GET /admin/users/{id}", s.admin(s.handleDroits))
	mux.HandleFunc("POST /admin/users/{id}/defaut", s.admin(s.handleSetDefaut))
	mux.HandleFunc("POST /admin/users/{id}/droits", s.admin(s.handleSetDroit))
	mux.HandleFunc("POST /admin/users/{id}/droits/supprimer", s.admin(s.handleSupprimerDroit))
	mux.HandleFunc("POST /admin/users/{id}/fermer", s.admin(s.handleFermerCompte))
	return mux
}

// baseData : champs communs à toutes les pages (promus dans les data par page).
type baseData struct {
	User *db.User
	// Page : l'entrée de menu à surligner. Le layout n'a aucun moyen de deviner
	// quelle page il rend - le titre ne suffit pas, « shared/tasks.md » et
	// « Droits de colin » sont des titres de la même entrée. Valeurs :
	// "dossiers", "skills", "utilisateurs", ou vide (connexion).
	Page string
}

// base : les champs communs, avec l'entrée de menu à surligner. Passe par une
// fonction plutôt qu'un littéral à chaque handler pour que l'oubli de `Page` se
// voie à l'appel.
func base(u *db.User, page string) baseData {
	return baseData{User: u, Page: page}
}

type loginData struct {
	baseData
	Erreur string
}

// render exécute le template dans un buffer avant d'écrire : jamais de page
// à moitié rendue en cas d'erreur de template.
func render(w http.ResponseWriter, status int, page string, data any) {
	tpl := pages[page]
	if tpl == nil {
		log.Printf("web: page inconnue %q", page)
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("web: rendu %s : %v", page, err)
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Jamais de HTML authentifié en cache (proxy, bfcache après logout).
	w.Header().Set("Cache-Control", "no-store")
	entetesDurcies(w)
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// erreurHTTP : réponse d'erreur avec les mêmes garanties de cache que render
// (une 404/500 authentifiée ne doit pas plus être cachée qu'une page).
func erreurHTTP(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Cache-Control", "no-store")
	entetesDurcies(w)
	http.Error(w, msg, status)
}

// entetesDurcies : AUCUNE REQUETE RESEAU SORTANTE DEPUIS UNE PAGE SERVIE.
//
// C'est une contrainte que le produit s'était déjà donnée pour ses propres
// ressources - le layout n'utilise que des polices système, et son commentaire
// dit pourquoi : « une police distante serait une requête réseau sortante
// depuis une page servie ». Le rendu markdown ouvrait cette porte en grand, et
// à un TIERS : `![](https://tracker.example/p.png)` dans une note, et le
// navigateur de l'administrateur va la chercher tout seul à l'ouverture de la
// page. Sans une ligne de script, ça donne à l'auteur de la note un accusé de
// lecture horodaté, l'IP et l'agent de l'administrateur ; et
// `![](http://192.168.1.1/x.png)` fait du navigateur une sonde du réseau
// interne, depuis l'intérieur du périmètre.
//
// `img-src 'none'` ne coûte RIEN aujourd'hui : aucun template n'utilise
// `<img>` (les icônes sont des `<svg>` en ligne), et le serveur n'expose aucune
// route qui servirait le contenu d'un fichier - une image de note ne pourrait
// donc pas s'afficher de toute façon. Un lien reste cliquable : une navigation
// est un geste de l'administrateur, pas une requête que la page déclenche.
//
// `'unsafe-inline'` sur le style et le script est ce que le produit EST : un
// binaire unique qui embarque ses templates, avec son style et son script dans
// le layout. La directive ne prétend donc pas se protéger d'une injection dans
// la page ; c'est `WithUnsafe` qui reste interdit pour ça. Elle ferme les
// requêtes sortantes, et `frame-ancestors` le détournement de clic.
// Source: https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Security-Policy
func entetesDurcies(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'none'",
		"img-src 'none'",
		"style-src 'unsafe-inline'",
		"script-src 'unsafe-inline'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
	}, "; "))
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// sessionUser résout l'utilisateur depuis le cookie de session, ou nil.
func (s *Server) sessionUser(r *http.Request) *db.User {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	u, err := s.DB.UserByToken(c.Value)
	if err != nil {
		return nil
	}
	return u
}

// session : middleware d'authentification web. Sans session valide →
// redirection vers le formulaire de connexion.
func (s *Server) session(next func(http.ResponseWriter, *http.Request, *db.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.sessionUser(r)
		if u == nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r, u)
	}
}

// GET /admin/login : formulaire de connexion (redirige si déjà connecté).
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.sessionUser(r) != nil {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	render(w, http.StatusOK, "login", loginData{})
}

// POST /admin/login : authentifie, émet un jeton d'appareil « web », pose le
// cookie de session. L'anti-énumération est portée par db.Authenticate.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render(w, http.StatusBadRequest, "login", loginData{Erreur: "formulaire invalide"})
		return
	}
	u, err := s.DB.Authenticate(r.PostFormValue("username"), r.PostFormValue("password"))
	if errors.Is(err, db.ErrNotFound) {
		render(w, http.StatusUnauthorized, "login", loginData{Erreur: "identifiants invalides"})
		return
	}
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	token, err := s.DB.IssueToken(u.ID, db.LabelSessionWeb)
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

// GET /admin/entrer?jeton=… : ouvre une session depuis un billet à usage unique.
//
// C'est la porte que l'app pousse quand on clique « Ouvrir l'admin web ». Le
// billet est consommé, une session ordinaire est posée, et la redirection
// n'emporte PAS la requête : l'URL qui reste dans la barre d'adresse et dans
// l'historique est « /admin/ », sans rien de secret.
//
// Un billet invalide, périmé ou déjà servi ne dit pas lequel des trois : il
// renvoie au formulaire de connexion, comme n'importe quelle session absente.
func (s *Server) handleEntrer(w http.ResponseWriter, r *http.Request) {
	if s.Tickets == nil {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	userID, ok := s.Tickets.Consomme(r.URL.Query().Get("jeton"))
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	u, err := s.DB.UserByID(userID)
	if err != nil {
		// Le compte a disparu entre l'émission et l'entrée. Rare, et sans
		// conséquence : le billet est déjà consommé, il ne resservira pas.
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	token, err := s.DB.IssueToken(u.ID, db.LabelSessionWeb)
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

// POST /admin/logout : révoque le jeton de session et efface le cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.DB.RevokeToken(c.Value); err != nil {
			log.Printf("web: révocation jeton : %v", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/admin",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// L'accueil vit dans dossiers.go : c'est la liste des dossiers.

// redirigeVers : une redirection permanente vers une destination FIXE, écrite
// dans le code. Aucune valeur reçue de la requête n'entre dans la cible : une
// redirection ouverte serait un vecteur d'hameçonnage depuis un domaine de
// confiance. 301 et pas 302, parce que ces adresses ne reviendront pas.
func redirigeVers(cible string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, cible, http.StatusMovedPermanently)
	}
}

// GET /admin/fichiers/{path...} : l'ancienne navigation. Le chemin est
// re-fabriqué segment par segment par hrefDossiers, jamais recollé depuis
// l'URL brute - un « .. » ou un « // » reçu ne doit pas ressortir tel quel.
// Un chemin hors périmètre n'est pas filtré ici : la cible le refera, et cette
// route ne rend aucun contenu.
func (s *Server) redirigeFichiers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, hrefDossiers(perms.Canon(r.PathValue("path"))), http.StatusMovedPermanently)
}
