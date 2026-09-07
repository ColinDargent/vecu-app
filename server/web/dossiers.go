// Les dossiers : l'accueil (ce à quoi j'ai accès), la navigation, la lecture
// d'un fichier, et le réglage de l'accès à l'endroit où il agit.
//
// « Espace » et « fichier » étaient deux écrans pour un seul objet, et la
// distinction n'était claire pour personne. Il n'en reste qu'un : le dossier.
// Un espace est simplement un dossier de premier niveau, parce que c'est un
// vrai point de montage sur le disque de chaque membre.
//
// Tout est filtré au périmètre de l'appelant (perms.CanRead) : le web n'expose
// jamais plus que ce que l'API de sync renverrait.
package web

import (
	"html/template"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/espaces"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
	"github.com/colindargent/vecu/server/storage"
)

// visibleFiles : la liste des fichiers de main lisibles par l'utilisateur.
// Duplique volontairement le filtrage de api.handleTree (15 lignes) : toute
// évolution du filtrage doit être reportée des deux côtés.
func (s *Server) visibleFiles(u *db.User) ([]string, error) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		return nil, err
	}
	all, err := s.Store.List("")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range all {
		if perms.CanRead(f, u.DefaultLevel, rules) {
			out = append(out, f)
		}
	}
	return out, nil
}

// cheminsAdministrables : la vue d'ADMINISTRATION, celle qui ne se laisse pas
// filtrer par ce que le lecteur s'est ferme a lui-meme.
//
// LE DEFAUT QU'ELLE FERME. Les ecrans se construisaient depuis visibleFiles,
// donc depuis la vue du LECTEUR : un administrateur qui fermait un dossier ne
// pouvait plus le rouvrir, l'ecran rendant 404. Mesure du 01/09 : cinq des six
// portes d'ecriture enferment, seul le defaut de dossier porte un garde.
//
// POURQUOI UNE FONCTION DISTINCTE, et pas un booleen sur visibleFiles. Les deux
// repondent a des questions differentes - « qu'est-ce que je peux lire » contre
// « qu'est-ce que je peux administrer ». Un booleen se passe a false par
// distraction et rouvre le trou sans bruit ; deux noms ne se confondent pas.
//
// L'EXEMPTION EST EXACTEMENT CELLE DE L'EXPORT ZIP, zone skills exclue
// comprise : un admin n'administre que les skills qu'on lui a partages, un
// skill etant prive meme de lui (modele F3). Elle n'elargit donc RIEN qu'un
// admin n'obtenait deja par `/admin/export.zip` ; elle aligne l'ecran sur ce
// que la porte d'a cote concede depuis toujours.
func (s *Server) cheminsAdministrables(u *db.User) ([]string, error) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		return nil, err
	}
	all, err := s.Store.List("")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range all {
		if (u.IsAdmin && !skills.SousRacine(f)) || perms.CanRead(f, u.DefaultLevel, rules) {
			out = append(out, f)
		}
	}
	return out, nil
}

// hrefAdmin : URL `base/p`, chaque segment de p échappé (« # », « ? » ou
// « %xx » littéral dans un nom de fichier casseraient le lien sinon -
// l'échappeur d'attribut de html/template les laisse passer).
func hrefAdmin(base, p string) string {
	if p == "" {
		return base
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + "/" + strings.Join(segs, "/")
}

func hrefDossiers(p string) string { return hrefAdmin("/admin/dossiers", p) }

type segmentVM struct{ Nom, Href string }

// fil : le fil d'Ariane d'un chemin (chaque segment cliquable).
func fil(p string) []segmentVM {
	if p == "" {
		return nil
	}
	var out []segmentVM
	cur := ""
	for _, seg := range strings.Split(p, "/") {
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		out = append(out, segmentVM{Nom: seg, Href: hrefDossiers(cur)})
	}
	return out
}

type entreeVM struct {
	Nom     string
	Href    string
	Dossier bool
	// Niveau : droit effectif de l'appelant sur cette entrée, et Direct dit si
	// une règle est posée dessus. Les deux servent à montrer la surcharge À
	// L'ENDROIT OÙ ELLE AGIT, au lieu d'un avertissement en bas d'un autre
	// écran. Le modèle autorise une règle à n'importe quelle profondeur, dans
	// les deux sens : sans ce repère, une règle profonde est invisible.
	Niveau string
	Direct bool
	// Createur : qui a créé cette entrée. Sur un DOSSIER, c'est le créateur de
	// son fichier le plus ancien - un dossier n'existe que parce qu'un fichier
	// y vit, donc c'est ce fichier-là qui l'a fait exister. Vide quand la carte
	// des créateurs ne répond pas (dépôt importé hors du produit, ou historique
	// illisible) : l'écran se tait plutôt que d'affirmer.
	Createur string
}

// children : entrées directes (dossiers puis fichiers) d'un dossier, dérivées
// de la liste des fichiers visibles. Un dossier n'apparaît que s'il contient
// au moins un fichier visible : un dossier invisible avec une surcharge
// lecture sur un fichier précis reste navigable jusqu'à ce fichier.
func children(visible []string, dir string, defaut perms.Level, rules []perms.Rule,
	createurs map[string]storage.Createur) []entreeVM {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	directes := map[string]bool{}
	for _, ru := range rules {
		directes[perms.Canon(ru.Path)] = true
	}
	// LE CREATEUR D'UN DOSSIER DEMANDE DE VOIR TOUS SES FICHIERS avant de
	// répondre, là où le reste de l'entrée se décidait à la première
	// occurrence. D'où les deux temps : on relève d'abord le plus ancien
	// créateur de chaque dossier, on construit les entrées ensuite.
	//
	// CE QUE CETTE DERIVATION DIT EXACTEMENT : « qui a créé le plus ancien
	// fichier que VOUS voyez ici ». Deux conséquences, assumées, et que la
	// légende de l'écran énonce plutôt que de laisser croire à un absolu.
	//
	//   - Elle dépend du lecteur. Un dossier dont le fichier le plus ancien est
	//     invisible pour quelqu'un lui nomme le suivant. C'est le prix de la
	//     frontière « un compte qui ne voit pas un chemin ne voit pas son
	//     créateur », qui prime : l'alternative serait de dériver le créateur
	//     d'un dossier sur TOUT son contenu, donc de dire qui a posé le fichier
	//     caché qu'il abrite.
	//   - Elle bouge quand on RENOMME. Un chemin recréé est un chemin neuf (voir
	//     `storage.Createur`), donc renommer le plus ancien fichier d'un dossier
	//     fait passer le dossier au créateur du suivant. Aucune porte du produit
	//     ne renomme aujourd'hui ; le jour où l'une le fera, c'est ici que ça se
	//     verra.
	ancien := map[string]storage.Createur{}
	vus := map[string]bool{}
	var fichiers []entreeVM
	for _, f := range visible {
		if !strings.HasPrefix(f, prefix) {
			continue
		}
		rest := f[len(prefix):]
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			fichiers = append(fichiers, entreeVM{
				Nom: rest, Href: hrefDossiers(f),
				Niveau: niveauFR(perms.Effective(f, defaut, rules)), Direct: directes[f],
				Createur: createurs[f].Auteur,
			})
			continue
		}
		nom := rest[:i]
		vus[nom] = true
		c, connu := createurs[f]
		if !connu {
			continue
		}
		if plusAncien, deja := ancien[nom]; !deja || c.Rang < plusAncien.Rang {
			ancien[nom] = c
		}
	}
	dossiers := make([]entreeVM, 0, len(vus))
	for nom := range vus {
		chemin := prefix + nom
		dossiers = append(dossiers, entreeVM{
			Nom: nom, Href: hrefDossiers(chemin), Dossier: true,
			Niveau: niveauFR(perms.Effective(chemin, defaut, rules)), Direct: directes[chemin],
			Createur: ancien[nom].Auteur,
		})
	}
	sort.Slice(dossiers, func(i, j int) bool { return dossiers[i].Nom < dossiers[j].Nom })
	sort.Slice(fichiers, func(i, j int) bool { return fichiers[i].Nom < fichiers[j].Nom })
	return append(dossiers, fichiers...)
}

// createursDuPerimetre : les créateurs des chemins que cet écran montre déjà,
// et d'eux seuls.
//
// LE PERIMETRE EST PORTE PAR L'APPEL, pas par un filtre posé après coup : le
// store ne rend que ce qu'on lui a nommé (voir `CreateursDe`), donc un chemin
// hors périmètre ne peut pas se retrouver dans une carte qu'on fait circuler.
// Une fuite demanderait de nommer soi-même le chemin caché, ce qui n'est plus
// une distraction.
//
// Le périmètre passé est celui de l'écran appelant, et c'est voulu : sur un
// administrateur, `cheminsAdministrables` inclut les dossiers qu'il s'est
// fermés à lui-même (DAR-196), et le créateur s'y affiche comme le niveau s'y
// affiche. Un non-administrateur, lui, n'y reçoit que ses chemins lisibles.
//
// Une carte illisible n'est PAS une erreur d'écran : on rend une carte vide,
// les colonnes restent muettes, et la navigation continue de fonctionner. Le
// créateur est un confort ; l'accès aux fichiers ne l'est pas.
func (s *Server) createursDuPerimetre(perimetre []string) map[string]storage.Createur {
	carte, err := s.Store.CreateursDe(perimetre)
	if err != nil {
		log.Printf("web: carte des créateurs indisponible : %v", err)
		return nil
	}
	return carte
}

// auMoinsUnCreateur : une seule entrée qui sait qui l'a créée suffit à
// justifier la légende de la colonne.
func auMoinsUnCreateur(entrees []entreeVM) bool {
	for _, e := range entrees {
		if e.Createur != "" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Les copies de conflit
// ---------------------------------------------------------------------------

type conflitVM struct{ Path, Href string }

// reConflit : le motif exact des copies forgées par api.conflictName,
// ancré sur le nom de fichier (pas le chemin : un dossier nommé
// « x (conflit y) » ou une note « réunion (conflit avec Marc).md » ne
// doivent pas matcher).
var reConflit = regexp.MustCompile(`\(conflit \d{4}-\d{2}-\d{2} \d{2}h\d{2} - [^/)]+\)`)

// conflitsVisibles : les copies de conflit du périmètre de l'appelant.
//
// L'onglet dédié a disparu au profit d'un bandeau sur l'accueil, affiché
// seulement s'il y a quelque chose. Motif : depuis que les copies LOCALES ne
// remontent plus au serveur (v0.7.2), l'écran était vide en permanence - un
// onglet qui ne dit jamais rien apprend à ne plus être regardé. La détection,
// elle, garde son sens : storage.WriteMerge en fabrique toujours quand le
// serveur ne peut pas fusionner.
func conflitsVisibles(visible []string) []conflitVM {
	var out []conflitVM
	for _, f := range visible {
		if reConflit.MatchString(path.Base(f)) {
			out = append(out, conflitVM{Path: f, Href: hrefDossiers(f)})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// L'accueil : les dossiers auxquels j'ai accès
// ---------------------------------------------------------------------------

// membreVM : un compte et son droit effectif sur un chemin donné.
type membreVM struct {
	UserID   int64
	Username string
	IsAdmin  bool
	Niveau   string // forme stockage : compare directement aux valeurs des boutons
	// Origine : d'OU vient le droit affiche, en clair. Le niveau seul ne dit pas
	// quoi toucher pour le changer : « lecture » peut venir de la regle du
	// compte, de sa cohorte, du defaut du dossier ou du defaut du compte, et
	// chacune se corrige a un endroit different. Sans ca le mainteneur tatonne,
	// et il tatonne sur des droits.
	Origine string
	// Profondes : règles posées À L'INTÉRIEUR du chemin pour ce compte. Sans
	// cette information le panneau ment - les boutons agissent sur ce dossier,
	// mais une règle plus profonde l'emporte dans le calcul du droit effectif.
	// Cliquer « invisible » serait alors un geste sans effet, et
	// l'administrateur croirait avoir coupé un accès resté ouvert.
	Profondes []string
}

type dossierVM struct {
	Nom      string
	Libelle  string
	Href     string
	Fichiers int
	Niveau   string
	Membres  []membreVM
	// Vide : l'espace ne porte que son fichier de métadonnées, il peut être
	// supprimé. Le refus d'un espace non vide se calcule au RENDU pour que la
	// porte n'apparaisse que quand elle mène quelque part - proposer un bouton
	// qui refusera toujours est une promesse qu'on ne tient pas.
	Vide bool
}

type accueilData struct {
	baseData
	Dossiers []dossierVM
	Conflits []conflitVM
	Niveaux  []niveauVM
	Erreur   string
}

// cohorteChoixVM : un groupe, et si le dossier courant y est rangé. Servi aux
// seuls administrateurs, comme la liste des membres : « qui a accès à quoi »
// est une information de gouvernance.
type cohorteChoixVM struct {
	ID  int64
	Nom string
	// Genre : `dossier` ou `skill`. Sert à la fiche de création d'un compte, qui
	// propose les deux dans une même liste : sans le genre, « Skills contenu »
	// et « Équipe delivery » s'y cochent côte à côte sans qu'on sache ce que
	// l'une et l'autre vont ouvrir.
	Genre  string
	Dedans bool
}

// membresDe : le droit effectif de chaque compte sur un chemin.
//
// **Réservé aux administrateurs, et l'appelant en porte la responsabilité.**
// Savoir qui a accès à quoi est une information de gouvernance : la donner à
// tout le monde apprendrait à un membre l'existence de comptes et de périmètres
// qui ne le regardent pas. C'est la règle qui valait déjà pour la matrice des
// espaces, conservée telle quelle en changeant d'écran.
func (s *Server) membresDe(chemin string) ([]membreVM, error) {
	users, err := s.DB.ListUsers()
	if err != nil {
		return nil, err
	}
	out := make([]membreVM, 0, len(users))
	for _, x := range users {
		rules, err := s.DB.Rules(x.ID)
		if err != nil {
			return nil, err
		}
		niveau, origine, err := s.DB.DroitAvecOrigine(&x, chemin)
		if err != nil {
			return nil, err
		}
		out = append(out, membreVM{
			UserID: x.ID, Username: x.Username, IsAdmin: x.IsAdmin,
			Niveau:    niveau.String(),
			Origine:   origineEnClair(origine),
			Profondes: reglesInternes(chemin, rules),
		})
	}
	return out, nil
}

// cohortesDe : les groupes, et lesquels portent ce dossier.
//
// Le genre est demandé explicitement : une ligne rangée comme SKILL sur le même
// chemin ne doit pas se cocher ici, sinon décocher la supprimerait et les deux
// écrans se contrediraient.
func (s *Server) cohortesDe(chemin string) ([]cohorteChoixVM, error) {
	groupes, err := s.DB.ListGroupes()
	if err != nil {
		return nil, err
	}
	ids, err := s.DB.GroupesDuChemin(chemin, "dossier")
	if err != nil {
		return nil, err
	}
	dedans := make(map[int64]bool, len(ids))
	for _, id := range ids {
		dedans[id] = true
	}
	out := make([]cohorteChoixVM, 0, len(groupes))
	for _, g := range groupes {
		out = append(out, cohorteChoixVM{ID: g.ID, Nom: g.Nom, Dedans: dedans[g.ID]})
	}
	return out, nil
}

func (s *Server) renderAccueil(w http.ResponseWriter, status int, u *db.User, erreur string) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	visible, err := s.cheminsAdministrables(u)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	data := accueilData{
		baseData: base(u, "dossiers"), Niveaux: niveauxVM,
		Conflits: conflitsVisibles(lisiblesParmi(visible, u, rules)), Erreur: erreur,
	}
	// Un administrateur voit sur l'accueil TOUS les espaces, y compris ceux
	// qu'il s'est fermes, annotes de leur niveau reel. Sans ca il ne peut plus
	// les rouvrir qu'en tapant leur adresse (DAR-196).
	listeEspaces := espaces.List
	if u.IsAdmin {
		listeEspaces = espaces.ListAdmin
	}
	// La vue ADMINISTRABLE, pas la liste brute du depot : elle exclut deja les
	// skills des autres comptes, qui gonfleraient sinon les compteurs de
	// l'espace `shared` pour un administrateur.
	for _, e := range listeEspaces(visible, u.DefaultLevel, rules) {
		vm := dossierVM{
			Nom: e.Nom, Href: hrefDossiers(e.Nom), Fichiers: e.Fichiers,
			// Libellé vide = pas de libellé : le template montre alors le nom
			// technique seul (DT3), au lieu de le répéter deux fois.
			Niveau: niveauFR(e.Niveau), Libelle: libelleLisible(s.Store, e, u, rules),
		}
		if u.IsAdmin {
			reste, err := espaces.Contenu(s.Store, e.Nom)
			if err != nil {
				erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
				return
			}
			vm.Vide = len(reste) == 0
			membres, err := s.membresDe(e.Nom)
			if err != nil {
				erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
				return
			}
			vm.Membres = membres
		}
		data.Dossiers = append(data.Dossiers, vm)
	}
	render(w, status, "accueil", data)
}

// GET /admin/ : les dossiers auxquels l'appelant a accès.
func (s *Server) handleAccueil(w http.ResponseWriter, r *http.Request, u *db.User) {
	s.renderAccueil(w, http.StatusOK, u, "")
}

// ---------------------------------------------------------------------------
// Un dossier ouvert, ou un fichier
// ---------------------------------------------------------------------------

type dossiersData struct {
	baseData
	Chemin  string
	Fil     []segmentVM
	Entrees []entreeVM
	Niveau  string
	// Membres et Niveaux : le panneau d'accès, servi aux seuls administrateurs
	// (voir membresDe). Vide pour les autres, et le template n'affiche alors
	// rien plutôt qu'un panneau désactivé.
	Membres []membreVM
	Niveaux []niveauVM
	// Cohortes : les groupes, et lesquels portent ce dossier. Même réserve que
	// Membres - servi aux seuls administrateurs.
	Cohortes []cohorteChoixVM
	// AuMoinsUnCreateur : au moins une entrée sait qui l'a créée. La légende
	// qui explique la colonne ne s'imprime que dans ce cas - sinon la page
	// promet une colonne absente, et le vide se lit comme une anomalie.
	AuMoinsUnCreateur bool
	// Confirme : un geste de cohorte ou de défaut vient d'aboutir.
	Confirme bool
	// Defaut : le niveau par défaut porté par CE dossier, et s'il en porte un.
	Defaut  string
	ADefaut bool
	// Deverrouille : le geste qui vient d'aboutir aurait fermé ce dossier à son
	// auteur, et lui a donc écrit une exception. À dire, sinon il croit avoir
	// posé une règle qui vaut aussi pour lui.
	Deverrouille bool
	Gestion      bool
}

type fichierData struct {
	baseData
	Chemin         string
	Nom            string
	Fil            []segmentVM
	HrefHistorique string
	Contenu        string
	// Createur : qui a créé ce fichier. Vide si la carte ne répond pas -
	// l'écran se tait plutôt que d'affirmer.
	Createur string
	// Rendu : le markdown mis en forme. Vide quand on montre le source, quand
	// le fichier n'est pas du markdown, ou quand la note est vide.
	Rendu template.HTML
	// Entete : le frontmatter de la note, sorti du markdown mais RENDU quand
	// même. Voir separeFrontmatter.
	Entete string
	// HTMLOmis : la note contient du HTML brut, que goldmark n'affiche pas. La
	// page le dit : retirer un morceau d'une note en silence, sur l'écran dont
	// le métier est de la lire, est le pire des deux maux.
	HTMLOmis bool
	// Source : on montre les octets, pas la mise en forme. UN CHAMP A LUI, et
	// pas une déduction sur `Rendu` vide : une note markdown vide rend une
	// chaîne vide, et la page annoncerait alors « source brut » sur la vue par
	// défaut, avec un lien de sortie qui pointe sur l'URL courante.
	Source bool
	// Markdown : ce fichier SAIT se mettre en forme. Ce qui n'est pas la même
	// question que « est-ce qu'on le met en forme là, maintenant » (`Source`) :
	// c'est lui qui décide si la page propose la bascule.
	Markdown bool
	// HrefSource, HrefRendu : les deux vues du même fichier. LE SOURCE RESTE
	// A UN GESTE, et ce n'est pas un détail de confort : quelqu'un qui se
	// demande ce qu'il y a vraiment dans un fichier - un caractère invisible,
	// un frontmatter cassé - a besoin des octets, et une mise en forme les lui
	// cache par construction.
	HrefSource string
	HrefRendu  string
	// Supprimable : ce compte a l'écriture sur ce chemin, et ce n'est pas le
	// fichier qui fait exister un espace. Calculé au rendu, pour que la porte
	// n'apparaisse pas là où elle refusera.
	Supprimable bool
}

// GET /admin/dossiers[/{path...}] : listing d'un dossier ou contenu d'un
// fichier. Un chemin hors périmètre est un 404 (comme l'API : on ne révèle
// pas l'existence d'un contenu invisible).
func (s *Server) handleDossiers(w http.ResponseWriter, r *http.Request, u *db.User) {
	p := perms.Canon(r.PathValue("path"))
	visible, err := s.cheminsAdministrables(u)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	// DEUX VUES, et la frontiere entre elles est le coeur de ce handler.
	// ADMINISTRER un dossier (le voir dans l'arbre, regler ses droits) n'est pas
	// LIRE un fichier : un administrateur qui s'est ferme un dossier doit
	// pouvoir le rouvrir sans en lire le contenu au passage.
	//
	// La lisibilite se decide sur LE chemin demande, pas en reconstruisant tout
	// le perimetre du lecteur. Une premiere version rappelait visibleFiles ici :
	// deux Store.List complets par requete, et l'ecran de navigation passait de
	// 10 a 21 ms sur le banc de DAR-195, pour un renseignement binaire.
	for _, f := range visible {
		if f == p {
			if !perms.CanRead(p, u.DefaultLevel, rules) {
				erreurHTTP(w, "introuvable", http.StatusNotFound)
				return
			}
			contenu, err := s.Store.Read(p, "")
			if err != nil {
				erreurHTTP(w, "introuvable", http.StatusNotFound)
				return
			}
			rules, err := s.DB.Rules(u.ID)
			if err != nil {
				erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
				return
			}
			href := hrefDossiers(p)
			data := fichierData{
				baseData: base(u, "dossiers"), Chemin: p, Nom: path.Base(p), Fil: fil(p),
				HrefHistorique: hrefAdmin("/admin/historique", p), Contenu: contenu,
				// UN SEUL CHEMIN DEMANDE : cet écran n'a pas affaire au reste du
				// dépôt, et chaque appel coûte un fork de `git rev-parse` pour
				// vérifier la clé du cache. On ne le paie pas sur un 404.
				Createur:   s.createursDuPerimetre([]string{p})[p].Auteur,
				Markdown:   estMarkdown(p),
				Source:     !estMarkdown(p) || r.URL.Query().Has("source"),
				HrefSource: href + "?source",
				HrefRendu:  href,
				Supprimable: perms.CanWrite(p, u.DefaultLevel, rules) &&
					path.Base(p) != espaces.FichierMeta,
			}
			if !data.Source {
				note, err := metEnForme(contenu)
				if err != nil {
					// Le repli est le source, qui est toujours juste. Cette
					// branche ne peut pas se produire aujourd'hui (voir
					// metEnForme) ; on ne l'ignore pas pour autant.
					log.Printf("web: rendu markdown de %q : %v", p, err)
					data.Source = true
				} else {
					data.Rendu, data.Entete, data.HTMLOmis = note.Corps, note.Entete, note.HTMLOmis
				}
			}
			render(w, http.StatusOK, "fichier", data)
			return
		}
	}

	// Dossier (la racine existe toujours, même vide) ?
	entrees := children(visible, p, u.DefaultLevel, rules, s.createursDuPerimetre(visible))
	if p != "" && len(entrees) == 0 {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}
	data := dossiersData{
		baseData: base(u, "dossiers"), Chemin: p, Fil: fil(p), Entrees: entrees,
		AuMoinsUnCreateur: auMoinsUnCreateur(entrees),
		Niveau:            niveauFR(perms.Effective(p, u.DefaultLevel, rules)),
		Gestion:           u.IsAdmin, Niveaux: niveauxVM,
		Confirme: u.IsAdmin && r.URL.Query().Has("okc"),
	}
	// L'accès se règle sur un DOSSIER, jamais à la racine du dépôt : une règle
	// posée sur "" est le niveau par défaut du compte, qui se change sur sa
	// fiche et pas ici.
	if u.IsAdmin && p != "" {
		membres, err := s.membresDe(p)
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		data.Membres = membres
		if data.Cohortes, err = s.cohortesDe(p); err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		niveau, pose, err := s.DB.DefautDossier(p)
		if err != nil {
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		data.ADefaut, data.Defaut = pose, niveau.String()
		data.Deverrouille = r.URL.Query().Has("okd")
	}
	render(w, http.StatusOK, "dossiers", data)
}

// ---------------------------------------------------------------------------
// Régler l'accès à un dossier
//
// CRÉER un dossier ne se fait plus ici : le web le posait à la racine Vécu sur
// le poste de chaque membre, alors que la ligne du 21/08 donne le local à l'app.
// Le geste vit dans « Partager un dossier… » (parcours A), qui ne déplace rien,
// et dans `POST /espaces` de l'API que l'app appelle. `espaces.Create` reste :
// c'est cette voie-là qui s'en sert.
// ---------------------------------------------------------------------------

// POST /admin/dossiers/acces : pose le droit d'un compte sur un chemin.
//
// Généralise l'ancien POST /admin/espaces/acces, qui n'acceptait qu'un nom de
// premier niveau. Le modèle autorise une règle à n'importe quelle profondeur,
// dans les deux sens ; ce handler est la surface qui manquait pour l'exercer
// sans taper un chemin à la main dans la fiche d'un compte.
//
// La redirection est bornée à des destinations que le code fabrique (jamais
// une URL reçue du formulaire) : le dossier concerné, ou la fiche du compte
// d'où vient le clic.
func (s *Server) handleSetAcces(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	chemin := perms.Canon(r.PostFormValue("chemin"))
	niveau, ok := perms.ParseLevel(r.PostFormValue("niveau"))
	userID, errID := strconv.ParseInt(r.PostFormValue("user_id"), 10, 64)
	// espaces.ValidChemin refuse la racine du dépôt et les chemins dont le
	// premier segment ne peut pas devenir un dossier sur le poste d'un membre.
	// Il exige un fichier DANS un dossier, d'où le nom seul traité à part.
	valide := espaces.ValidNom(chemin) == nil || espaces.ValidChemin(chemin) == nil
	if !valide || !ok || errID != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "chemin, utilisateur ou niveau invalide")
		return
	}
	if refus := refusZoneSkills(chemin); refus != "" {
		s.renderAccueil(w, http.StatusBadRequest, u, refus)
		return
	}
	if _, err := s.DB.UserByID(userID); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "utilisateur introuvable")
		return
	}
	if err := s.DB.SetPermission(userID, chemin, niveau); err != nil {
		log.Printf("web: accès dossier : %v", err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	if r.PostFormValue("retour") == "user" {
		http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(userID, 10), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, hrefDossiers(chemin), http.StatusSeeOther)
}

// POST /admin/fichier/supprimer : la troisième porte de DAR-204.
//
// `DELETE /files/…` existait côté API et le client sait supprimer ; ce qui
// manquait était la surface web. Le mainteneur d'un client ne va pas ouvrir un
// terminal pour retirer un fichier.
//
// CE N'EST PAS UNE SUPPRESSION LOCALE, ET L'ÉCRAN LE DIT. Le fichier part du
// dépôt, donc de TOUS les postes au cycle suivant. C'est le geste le plus
// destructeur de l'interface, et le seul dont l'effet dépasse l'écran où on le
// déclenche - d'où la confirmation par le nom, comme sur les deux autres portes.
//
// LE FICHIER DE MÉTADONNÉES D'UN ESPACE EST REFUSÉ ICI, et ce refus est le
// point le plus important de ce handler. Le retirer ferait cesser l'espace
// d'exister sans supprimer son contenu, qui deviendrait ORPHELIN - et c'est
// précisément le trou que `espaces.Supprimer` a fermé ce matin, en refusant de
// supprimer un espace non vide. Sans ce refus, cette porte le rouvrirait par
// derrière : le ticket DAR-204 nomme d'ailleurs ce détour comme le contournement
// connu.
func (s *Server) handleSupprimerFichier(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		erreurHTTP(w, "formulaire invalide", http.StatusBadRequest)
		return
	}
	p := perms.Canon(r.PostFormValue("chemin"))
	if storage.ValidPath(p) != nil {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}
	// LECTURE D'ABORD : un compte qui ne voit pas ce chemin reçoit 404, pas 403.
	// Un 403 lui apprendrait que le fichier existe.
	if !s.canReadOr404(w, u, p) {
		return
	}
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	if !perms.CanWrite(p, u.DefaultLevel, rules) {
		erreurHTTP(w, "écriture non autorisée", http.StatusForbidden)
		return
	}
	if path.Base(p) == espaces.FichierMeta {
		erreurHTTP(w, "ce fichier fait exister l'espace : supprimez l'espace depuis l'accueil, "+
			"ce qui vérifie d'abord qu'il est vide", http.StatusBadRequest)
		return
	}
	if r.PostFormValue("confirmation") != path.Base(p) {
		erreurHTTP(w, "fichier non supprimé : le nom saisi ne correspond pas à « "+path.Base(p)+" »",
			http.StatusBadRequest)
		return
	}
	if _, err := s.Store.Delete(p, u.Username); err != nil {
		log.Printf("web: suppression de %s : %v", p, err)
		erreurHTTP(w, "suppression refusée", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, hrefDossiers(path.Dir(p)), http.StatusSeeOther)
}

// La route POST /admin/dossiers/cohortes a été RETIRÉE le 02/09, et le geste
// n'a pas disparu : il a déménagé sur la cohorte (`/admin/cohortes`), qui est
// l'objet dont on part vraiment.
//
// Elle n'est pas seulement retirée du gabarit. Une porte d'écriture qui ne
// s'affiche plus mais répond toujours en HTTP est exactement la SECONDE
// SURFACE AUTORITAIRE que `TestUneSeuleSurfaceAutoritaire` interdit : deux
// écrans qui font tous les deux foi s'écrasent l'un l'autre dès que l'un est
// resté ouvert pendant que l'autre enregistrait. L'invariant tient toujours ;
// c'est le côté qui écrit qui a changé.

// POST /admin/dossiers/defaut : poser ou retirer le niveau par défaut d'un
// dossier.
//
// C'EST LE MÉCANISME QUI FERME. Une cohorte ouvre et ne ferme rien qu'une autre
// ouvre ; sans ce défaut, protéger un dossier sensible demanderait une règle
// individuelle par compte, à rejouer à chaque embauche. Une ligne ici vaut pour
// tout le monde, y compris pour les comptes qui n'existent pas encore.
//
// Réservé aux administrateurs par sa route.
// POST /admin/dossiers/espaces/supprimer : retirer un espace (DAR-204).
//
// `POST /espaces` créait, et RIEN ne supprimait. Le seul détour connu était de
// retirer le `.vecu-espace.json` par `DELETE /files/…` - c'est-à-dire de
// connaître le mécanisme interne, ce qu'on ne peut pas demander au mainteneur
// d'un client.
//
// La confirmation est le NOM de l'espace, retapé, pour la même raison que sur la
// fermeture d'un compte : le geste part d'une liste, et une case se coche par
// réflexe. Le refus d'un espace non vide vit dans `espaces.Supprimer`, pas ici :
// c'est une propriété du modèle, elle doit valoir pour tout appelant.
func (s *Server) handleSupprimerEspace(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	nom := perms.Canon(r.PostFormValue("espace"))
	if r.PostFormValue("confirmation") != nom {
		s.renderAccueil(w, http.StatusBadRequest, u,
			"espace non supprimé : le nom saisi ne correspond pas à « "+nom+" »")
		return
	}
	if err := espaces.Supprimer(s.Store, nom, u.Username); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "espace non supprimé : "+err.Error())
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleSetDefautDossier(w http.ResponseWriter, r *http.Request, u *db.User) {
	if err := r.ParseForm(); err != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "formulaire invalide")
		return
	}
	chemin := perms.Canon(r.PostFormValue("chemin"))
	if espaces.ValidNom(chemin) != nil && espaces.ValidChemin(chemin) != nil {
		s.renderAccueil(w, http.StatusBadRequest, u, "chemin invalide")
		return
	}
	if refus := refusZoneSkills(chemin); refus != "" {
		s.renderAccueil(w, http.StatusBadRequest, u, refus)
		return
	}

	if r.PostFormValue("retirer") != "" {
		if err := s.DB.SupprimeDefautDossier(chemin); err != nil {
			log.Printf("web: retrait du défaut de %s : %v", chemin, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, hrefDossiers(chemin)+"?okc=1", http.StatusSeeOther)
		return
	}

	brut := r.PostFormValue("niveau")
	// La valeur vide est l'option « aucun » du formulaire : elle RETIRE le
	// défaut au lieu d'en poser un. Sans ce cas, choisir « aucun » tomberait en
	// « niveau invalide » et l'écran refuserait un geste qu'il propose.
	if brut == "" {
		if err := s.DB.SupprimeDefautDossier(chemin); err != nil {
			log.Printf("web: retrait du défaut de %s : %v", chemin, err)
			erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, hrefDossiers(chemin)+"?okc=1", http.StatusSeeOther)
		return
	}
	niveau, ok := perms.ParseLevel(brut)
	if !ok {
		s.renderAccueil(w, http.StatusBadRequest, u, "niveau invalide")
		return
	}
	if err := s.DB.SetDefautDossier(chemin, niveau); err != nil {
		log.Printf("web: défaut de %s : %v", chemin, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}

	// LE GARDE ANTI-ENFERMEMENT EST RETIRE ICI (DAR-196, 01/09), et son retrait
	// ferme un trou plutot qu'il n'en ouvre un.
	//
	// Il re-posait a l'auteur du geste une exception au niveau qu'il avait
	// AVANT - donc l'ecriture - sur un dossier qu'il venait de declarer
	// invisible. Il preservait l'administrabilite en lui rendant la LECTURE des
	// fichiers, ce que les cinq autres portes ne font pas : mesure du 01/09, le
	// contenu d'un fichier ferme restait servi par cette porte-la seulement.
	//
	// L'administrabilite est desormais portee par la vue (cheminsAdministrables),
	// pour les six portes et sans rendre aucun droit de lecture. Un garde par
	// porte est exactement ce qui avait produit une couverture d'un sixieme.
	http.Redirect(w, r, hrefDossiers(chemin)+"?okc=1", http.StatusSeeOther)
}

// lisiblesParmi restreint une vue d'administration a ce que le lecteur peut
// reellement ouvrir. Sert au bandeau des copies de conflit : y lister une copie
// que le clic rend en 404 fabrique un lien mort, et le nom d'une copie porte la
// date et l'auteur du conflit.
func lisiblesParmi(chemins []string, u *db.User, rules []perms.Rule) []string {
	out := make([]string, 0, len(chemins))
	for _, p := range chemins {
		if perms.CanRead(p, u.DefaultLevel, rules) {
			out = append(out, p)
		}
	}
	return out
}

// libelleLisible rend le libelle d'un espace, ou la chaine vide si le lecteur
// n'a pas le droit de le lire.
//
// Le libelle vit dans le fichier de metadonnees de l'espace, qui est un fichier
// ORDINAIRE soumis aux memes droits que les autres (doctrine d'espaces.Create).
// Le servir a un administrateur qui s'est ferme l'espace reviendrait a lui
// servir du contenu ferme sur l'ecran qui pretend n'en pas servir.
func libelleLisible(store *storage.Store, e espaces.Info, u *db.User, rules []perms.Rule) string {
	if !perms.CanRead(e.Nom+"/", u.DefaultLevel, rules) {
		return ""
	}
	return espaces.LitLibelle(store, e.Nom)
}

// refusZoneSkills rend le motif de refus d'un chemin de la zone skills, ou la
// chaine vide.
//
// POURQUOI CE REFUS EXISTE. Les formulaires de droits acceptent un chemin TAPE
// A LA MAIN, donc n'importe lequel, y compris sous `shared/skills/`. Le serveur
// ecrivait la regle, puis renvoyait vers la page de ce dossier - que la vue
// d'administration exclut par construction, un skill etant prive meme de
// l'admin pour ses fichiers. L'auteur du geste atterrissait sur une erreur juste
// apres son propre clic, sans savoir si sa regle avait pris. Elle avait pris.
//
// Deux regles du depot se contredisaient donc dans cette zone, et le code
// tranchait en silence. Il le dit maintenant, et il renvoie a l'ecran qui sait
// vraiment le faire.
func refusZoneSkills(chemin string) string {
	if skills.SousRacine(chemin) || perms.Canon(chemin) == skills.DefaultRoot {
		return "les droits d'un skill se règlent sur l'écran des skills, pas ici"
	}
	return ""
}

// origineEnClair : le barreau de l'echelle, dit a quelqu'un qui n'a pas lu le
// code.
//
// Le vocabulaire evite « regle », « exception » et « resolution » : le lecteur
// de cet ecran est le mainteneur du second cerveau chez un client, pas nous. Ce
// qu'il doit pouvoir deduire de la phrase, c'est OU aller pour changer le droit.
func origineEnClair(o db.Origine) string {
	switch o.Barreau {
	case "exception":
		return "posé sur ce compte"
	case "cohorte":
		if o.Cohorte != "" {
			return "par la cohorte " + o.Cohorte
		}
		return "par une cohorte"
	case "defaut":
		return "défaut du compte sur ce chemin"
	case "dossier":
		return "défaut de ce dossier"
	default:
		return "défaut du compte"
	}
}
