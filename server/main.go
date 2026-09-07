// Vécu - serveur : back office second cerveau self-hosted.
// Un seul binaire : API HTTP JSON + web admin + SQLite + git bare.
package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
	"github.com/colindargent/vecu/server/storage"
	"github.com/colindargent/vecu/server/tickets"
	"github.com/colindargent/vecu/server/web"
)

func main() {
	addr := envOr("VECU_ADDR", ":8080")
	dataDir := envOr("VECU_DATA", "data")

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("data dir : %v", err)
	}
	database, err := db.Open(filepath.Join(dataDir, "app.db"))
	if err != nil {
		log.Fatalf("ouverture base : %v", err)
	}
	defer database.Close()

	if err := bootstrapAdmin(database, os.Getenv("VECU_ADMIN_USER"), os.Getenv("VECU_ADMIN_PASSWORD")); err != nil {
		log.Fatalf("bootstrap admin : %v", err)
	}

	store, err := storage.Init(filepath.Join(dataDir, "brain.git"))
	if err != nil {
		log.Fatalf("init dépôt : %v", err)
	}

	if err := reparerIncidentSkills(database); err != nil {
		log.Fatalf("réparation incident skills : %v", err)
	}

	if err := backfillSkills(database, store); err != nil {
		log.Fatalf("backfill skills : %v", err)
	}

	// Un seul magasin de billets pour les deux serveurs : l'API les émet
	// (depuis l'app, authentifiée par son jeton d'appareil), le web les consomme.
	billets := tickets.New()
	apiSrv := &api.Server{
		DB: database, Store: store, AppDist: envOr("VECU_APPDIST", "appdist"),
		Tickets: billets, Logf: log.Printf,
	}
	webSrv := &web.Server{DB: database, Store: store, Tickets: billets}
	// /admin/ (préfixe le plus spécifique) → web admin ; le reste → API JSON.
	root := http.NewServeMux()
	root.Handle("/admin/", webSrv.Handler())
	root.Handle("/", apiSrv.Handler())
	// LA RACINE MÈNE QUELQUE PART (04/09). Taper l'adresse du serveur dans un
	// navigateur - le seul geste qu'on fait sans qu'on le lui ait appris -
	// tombait sur le mux de l'API, qui n'a pas de route `/` et rendait un 404
	// nu. `{$}` ne matche QUE le chemin exact `/`, donc l'API garde tout le
	// reste, y compris ses propres 404 quand ils sont mérités.
	root.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
	})
	log.Printf("vecu server à l'écoute sur %s (data: %s)", addr, dataDir)
	if err := http.ListenAndServe(addr, root); err != nil {
		log.Fatal(err)
	}
}

// bootstrapAdmin crée le compte administrateur initial (écriture à la racine)
// depuis VECU_ADMIN_USER / VECU_ADMIN_PASSWORD, uniquement si la base est
// vide. Jamais réappliqué ensuite : changer les variables ne recrée pas de
// compte et ne réinitialise aucun mot de passe (le reset passe par l'admin).
func bootstrapAdmin(d *db.DB, user, password string) error {
	if user == "" && password == "" {
		// Pas de bootstrap demandé. Base vide = serveur inutilisable (aucun
		// autre chemin de création du premier compte) : on le dit.
		if peuplee, err := d.HasUsers(); err == nil && !peuplee {
			log.Printf("attention : aucun compte n'existe - définir VECU_ADMIN_USER et VECU_ADMIN_PASSWORD pour créer l'administrateur initial")
		}
		return nil
	}
	if user == "" || password == "" {
		return errors.New("VECU_ADMIN_USER et VECU_ADMIN_PASSWORD vont ensemble")
	}
	peuplee, err := d.HasUsers()
	if err != nil {
		return err
	}
	if peuplee {
		return nil // no-op avant toute validation : une env périmée ne doit pas empêcher un redémarrage
	}
	if len(password) < 8 {
		return errors.New("VECU_ADMIN_PASSWORD : 8 caractères minimum")
	}
	if _, err := d.CreateUser(user, password, perms.Ecriture, true); err != nil {
		// Deux instances démarrées en même temps : la seconde perd la course
		// (contrainte UNIQUE) - déjà bootstrappé, pas une raison de crasher.
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil
		}
		return err
	}
	log.Printf("compte administrateur initial %q créé (bootstrap)", user)
	return nil
}

// cleIncidentSkills : marqueur de la réparation ponctuelle ci-dessous.
const cleIncidentSkills = "incident-skills-2026-07-28"

// reparerIncidentSkills efface les revendications posées par le backfill fautif
// du 28/07, qui a attribué les 24 skills existants à un compte admin résiduel
// (sans poste appairé) au lieu du compte réellement utilisé.
//
// Sans ce nettoyage, la nouvelle migration ne peut rien réparer : `ClaimSkill`
// est un INSERT OR IGNORE sur le slug, donc les lignes erronées bloqueraient
// toute ré-attribution, et les skills resteraient possédés par un compte mort.
//
// UNE SEULE FOIS par base, gardé par un marqueur : après le retour de F3, les
// lignes de `skill_creators` sont légitimes (posées à la création d'un skill) et
// les effacer serait destructeur. C'est une réparation datée, pas une règle.
//
// Sûr par construction : ne défait qu'une revendication et le droit d'écriture
// que cette revendication avait accordé (cf. AnnulerRevendication). Les droits
// posés à la main - par exemple les niveaux par skill réglés pour Achille le
// 26/07 - ne sont pas touchés.
//
// C'est la SEULE opération de ce fichier qui retire un droit. Elle ne s'y
// autorise que sur un compte SANS poste appairé : aucun client ne synchronise ce
// compte, donc aucun fichier local ne peut être supprimé par le retrait. Un
// créateur avec un poste est laissé en place et signalé - la réparation
// s'interrompt plutôt que de reproduire l'incident en croyant le corriger.
func reparerIncidentSkills(d *db.DB) error {
	fait, err := d.DejaFait(cleIncidentSkills)
	if err != nil {
		return err
	}
	if fait {
		return nil
	}
	createurs, err := d.Createurs()
	if err != nil {
		return err
	}
	annulees := 0
	for slug, userID := range createurs {
		if !userID.Valid {
			continue
		}
		postes, err := d.NbDevices(userID.Int64)
		if err != nil {
			return err
		}
		if postes > 0 {
			log.Printf("réparation incident skills : %q appartient à un compte qui synchronise (id %d, %d poste(s)) - revendication CONSERVÉE, retirer un droit supprimerait ses fichiers en local",
				slug, userID.Int64, postes)
			continue
		}
		if err := d.AnnulerRevendication(slug, skills.DefaultRoot+"/"+slug, userID.Int64); err != nil {
			return err
		}
		annulees++
	}
	if annulees > 0 {
		log.Printf("réparation incident skills : %d revendication(s) erronée(s) annulée(s) sur des comptes sans poste appairé, ré-attribution au démarrage", annulees)
	}
	return d.MarquerFait(cleIncidentSkills)
}

// regleAPoser : une règle de droit que la migration se propose d'écrire.
type regleAPoser struct {
	userID int64
	path   string
	niveau perms.Level
}

// backfillSkills fait basculer une base existante vers le modèle « skills privés
// par défaut », SANS jamais retirer à personne un accès qu'il avait déjà.
//
// INVARIANT STRUCTUREL : la migration ne réduit jamais le niveau effectif d'un
// compte sur un skill existant. Elle ne le promet pas, elle le prouve : le plan
// d'écriture est intégralement projeté en mémoire, le résultat est recalculé avec
// le vrai résolveur, et RIEN n'est écrit si un seul niveau baisse.
//
// Déroulé :
//  1. relever le niveau effectif de chaque compte sur chaque skill du dépôt ;
//  2. projeter en mémoire : geler ce niveau en règle explicite par skill, puis
//     poser `shared/skills = invisible` - qui ne mord donc que sur les skills
//     créés APRÈS la migration, jamais sur l'existant ;
//  3. recalculer les niveaux effectifs sur cette projection ;
//  4. si un niveau baisse : ne rien écrire, le dire, réessayer au prochain
//     démarrage. Sinon appliquer, gels d'abord, masque ensuite.
//
// Pourquoi ce dessin (incident du 28/07) : la version précédente masquait
// d'abord et attribuait ensuite à « l'admin de plus petit id ». En prod cet
// admin était un compte résiduel sans poste appairé : le masque a été posé, et
// l'octroi compensatoire est parti sur un compte mort. Le périmètre du compte
// réel a fondu de 24 skills, que son client a supprimés en local. Une heuristique
// d'attribution PEUT se tromper ; ce qui ne doit pas être possible, c'est
// qu'elle se trompe ET que le masque tombe quand même.
func backfillSkills(d *db.DB, store *storage.Store) error {
	users, err := d.ListUsers()
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}

	slugsExistants, err := slugsDuDepot(store)
	if err != nil {
		// Dépôt vide ou illisible : on ne connaît pas le périmètre à préserver,
		// donc on ne peut pas prouver qu'on ne le réduit pas. On ne masque rien.
		log.Printf("migration skills : dépôt illisible (%v) - migration reportée au prochain démarrage", err)
		return nil
	}

	// 1. État courant : règles et niveau effectif de chaque compte sur chaque skill.
	regles := make(map[int64][]perms.Rule, len(users))
	avant := make(map[int64]map[string]perms.Level, len(users))
	for _, u := range users {
		r, err := d.Rules(u.ID)
		if err != nil {
			return err
		}
		regles[u.ID] = r
		avant[u.ID] = niveauxParSkill(u, slugsExistants, r)
	}

	// 2. Projection en mémoire du plan d'écriture.
	var gels, masques []regleAPoser
	projete := make(map[int64][]perms.Rule, len(users))
	for _, u := range users {
		r := append([]perms.Rule(nil), regles[u.ID]...)
		for _, slug := range slugsExistants {
			p := skills.DefaultRoot + "/" + slug
			if regleExacte(regles[u.ID], p) {
				continue // déjà explicite : le masque ne peut pas l'atteindre
			}
			niveau := avant[u.ID][slug]
			r = append(r, perms.Rule{Path: p, Level: niveau})
			gels = append(gels, regleAPoser{u.ID, p, niveau})
		}
		if !regleExacte(regles[u.ID], skills.DefaultRoot) {
			r = append(r, perms.Rule{Path: skills.DefaultRoot, Level: perms.Invisible})
			masques = append(masques, regleAPoser{u.ID, skills.DefaultRoot, perms.Invisible})
		}
		projete[u.ID] = r
	}

	// 3-4. Preuve avant écriture : aucun niveau effectif ne baisse.
	if perte, ok := chercherPerte(users, slugsExistants, avant, projete); ok {
		log.Printf("migration skills : ABANDON - %q perdrait l'accès à %q (%s -> %s). Aucune règle écrite, l'état reste inchangé.",
			perte.compte, perte.slug, perte.avant, perte.apres)
		return nil
	}

	// Application. Les gels AVANT les masques : si le processus meurt entre les
	// deux, on a des règles explicites redondantes et aucun masque - inoffensif.
	// L'ordre inverse laisserait un masque sans filet, c'est-à-dire l'incident.
	for _, g := range gels {
		if err := d.EnsurePermission(g.userID, g.path, g.niveau); err != nil {
			return err
		}
	}
	for _, m := range masques {
		if err := d.EnsurePermission(m.userID, m.path, m.niveau); err != nil {
			return err
		}
	}

	return attribuerSkillsExistants(d, users, slugsExistants, projete)
}

// attribuerSkillsExistants donne un propriétaire aux skills antérieurs au modèle
// créateur, pour que la délégation de partage ait un titulaire.
//
// Candidat légitime : un admin qui a DÉJÀ l'écriture effective sur ce skill.
// Ce seul critère suffisait à éviter l'incident du 28/07 - le compte résiduel
// qui a tout raflé était `default_level=invisible` et n'avait aucune règle, donc
// aucune écriture sur quoi que ce soit. Il a le mérite d'être un principe et pas
// une heuristique : l'attribution ne peut jamais ÉLEVER un droit, seulement
// nommer propriétaire quelqu'un qui pouvait déjà écrire.
//
// Départage si plusieurs admins qualifient : celui qui a des postes appairés (un
// compte qui n'a jamais synchronisé ne possède pas le contenu existant). Si
// l'ambiguïté demeure, on n'attribue pas et on le dit. Un skill sans
// propriétaire reste visible et modifiable par ceux qui y ont accès ; seule la
// délégation de partage attend. Ne rien faire vaut mieux qu'attribuer au hasard.
func attribuerSkillsExistants(d *db.DB, users []db.User, slugs []string, regles map[int64][]perms.Rule) error {
	dejaCreateur, err := d.Createurs()
	if err != nil {
		return err
	}

	for _, slug := range slugs {
		if _, revendique := dejaCreateur[slug]; revendique {
			continue
		}
		p := skills.DefaultRoot + "/" + slug
		var candidats []db.User
		for _, u := range users {
			if u.IsAdmin && perms.Effective(p, u.DefaultLevel, regles[u.ID]) >= perms.Ecriture {
				candidats = append(candidats, u)
			}
		}
		if len(candidats) > 1 {
			candidats = filtreAvecPoste(d, candidats)
		}
		if len(candidats) != 1 {
			log.Printf("migration skills : %q laissé sans propriétaire (%d admin(s) éligible(s)) - partage délégué indisponible jusqu'à attribution explicite",
				slug, len(candidats))
			continue
		}
		if _, err := d.ClaimSkill(slug, p, candidats[0].ID); err != nil {
			return err
		}
	}
	return nil
}

// filtreAvecPoste restreint aux comptes ayant au moins un poste appairé. Rend la
// liste inchangée si aucun n'en a (le départage n'apporte rien) ou en cas
// d'erreur de lecture : ce filtre affine, il ne décide pas seul.
func filtreAvecPoste(d *db.DB, candidats []db.User) []db.User {
	var avecPoste []db.User
	for _, u := range candidats {
		n, err := d.NbDevices(u.ID)
		if err != nil {
			return candidats
		}
		if n > 0 {
			avecPoste = append(avecPoste, u)
		}
	}
	if len(avecPoste) == 0 {
		return candidats
	}
	return avecPoste
}

// perte : un compte qui perdrait du terrain sur un skill si le plan était écrit.
type perte struct {
	compte       string
	slug         string
	avant, apres perms.Level
}

// chercherPerte recalcule les niveaux effectifs sur le plan projeté et renvoie la
// première régression trouvée. C'est le filet qui rend la migration incapable de
// reproduire l'incident du 28/07 : quelle que soit la justesse de l'attribution,
// une baisse de niveau annule tout au lieu d'être appliquée.
//
// Fonction pure et isolée à dessein : elle est testable sur des plans fabriqués,
// y compris ceux que le code de production ne sait pas produire.
func chercherPerte(users []db.User, slugs []string, avant map[int64]map[string]perms.Level, projete map[int64][]perms.Rule) (perte, bool) {
	for _, u := range users {
		apres := niveauxParSkill(u, slugs, projete[u.ID])
		for _, slug := range slugs {
			if apres[slug] < avant[u.ID][slug] {
				return perte{u.Username, slug, avant[u.ID][slug], apres[slug]}, true
			}
		}
	}
	return perte{}, false
}

// slugsDuDepot liste les slugs de skills présents dans le dépôt à HEAD. Un skill
// existe s'il contient au moins un fichier ; les slugs invalides sont ignorés
// (même contrainte que les espaces : le nom devient un segment de chemin).
func slugsDuDepot(store *storage.Store) ([]string, error) {
	head, err := store.Head()
	if err != nil {
		return nil, err
	}
	files, err := store.List(head)
	if err != nil {
		return nil, err
	}
	prefix := skills.DefaultRoot + "/"
	vus := map[string]bool{}
	var out []string
	for _, f := range files {
		reste, ok := strings.CutPrefix(f, prefix)
		if !ok {
			continue
		}
		slug, _, aFichier := strings.Cut(reste, "/")
		if !aFichier || vus[slug] || espaces.ValidNom(slug) != nil {
			continue
		}
		vus[slug] = true
		out = append(out, slug)
	}
	sort.Strings(out) // ordre stable : logs et tests déterministes
	return out, nil
}

// niveauxParSkill relève le niveau effectif d'un compte sur chaque skill, avec le
// résolveur de production (jamais une réimplémentation : la preuve ne vaut que si
// elle utilise le même calcul que la lecture).
func niveauxParSkill(u db.User, slugs []string, rules []perms.Rule) map[string]perms.Level {
	m := make(map[string]perms.Level, len(slugs))
	for _, slug := range slugs {
		m[slug] = perms.Effective(skills.DefaultRoot+"/"+slug, u.DefaultLevel, rules)
	}
	return m
}

// regleExacte dit si une règle porte exactement sur ce chemin (pas un ancêtre).
func regleExacte(rules []perms.Rule, path string) bool {
	path = perms.Canon(path)
	for _, r := range rules {
		if perms.Canon(r.Path) == path {
			return true
		}
	}
	return false
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
