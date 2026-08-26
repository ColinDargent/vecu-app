package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	sync2 "sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// seuilSuppressionMasse : en dessous de ce nombre de fichiers disparus d'un
// coup, une suppression locale part sans question (nettoyer trois fichiers doit
// rester possible). Au-delà, elle doit AUSSI concerner plus de la moitié de
// l'espace pour être bloquée : c'est la signature d'un dossier déplacé, pas
// d'un ménage.
const seuilSuppressionMasse = 10

// Engine synchronise une racine locale avec le serveur.
//
// La racine contient un sous-dossier par espace monté. Tout le reste de la
// racine - dossiers personnels, dépôts de code, `.obsidian/` - n'est JAMAIS lu,
// écrit ni supprimé : le moteur ne travaille que dans les espaces montés.
type Engine struct {
	dir    string
	client *HTTPClient
	state  State
	exclus map[string]bool // espaces que ce poste refuse de monter (config locale)
	// detaches : parmi les exclus, ceux dont le contenu local doit RESTER sur
	// le disque au démontage. C'est la seule chose qui distingue « je retire
	// ce dossier de la synchronisation » d'« on m'a retiré l'accès », et la
	// distinction se paie en fichiers effacés si elle manque.
	detaches map[string]bool
	// propositions : espaces auxquels ce compte a droit, jamais montés ici, et
	// qui attendent un geste. Recalculées à chaque cycle, comme les laisses :
	// un droit retiré doit faire disparaître la proposition, et une liste qu'on
	// complète ne perd jamais rien.
	propositions []Proposition
	// motifsEspace : espace -> motifs de SON `.vecuignore`, lu à sa racine.
	//
	// `motifs` ne porte que les règles de la racine locale. Elles ne peuvent pas
	// suffire depuis qu'un espace vit où on veut : un dossier partagé a ses
	// propres exclusions, écrites dedans, et personne ne les lisait - mesuré le
	// 21/08, un `node_modules` est parti sur le serveur au premier partage. DT2
	// l'avait posé : « un fichier global dans le dossier d'état, plus un fichier
	// optionnel par espace, le second complétant le premier ».
	motifsEspace map[string][]string
	montages     map[string]string // espace -> chemin local choisi sur ce poste (DT2)
	ecriture     map[string]bool   // espace -> ce compte y a l'écriture (refait à chaque cycle)
	contexteDit  map[string]string // espace -> dernière remarque F3 journalisée (mémoire, jamais persisté)
	copies       []string          // copies de conflit locales vues au dernier scan (mémoire, dérivé du disque)
	importer     map[string]bool   // espaces dont on adopte volontairement le dossier local
	projections  []string          // outils vers lesquels projeter les skills (config locale)
	montes       map[string]bool   // espaces montés au cycle courant (= state.Espaces)
	motifs       []string          // motifs du .vecuignore de cette racine
	logf         func(string, ...any)
	mu           sync2.Mutex    // sérialise les cycles (un seul à la fois, in-process)
	locks        []*os.File     // flocks : la racine, plus chaque espace monté hors d'elle
	laisses      []Laisse       // fichiers non synchronisés au dernier cycle, avec le motif
	nouveaux     []NouveauSkill // skills d'autrui appris et pas encore lus (F4)
	confirme     func(espace string, constatees, verifies int) bool
}

// NewEngine charge config + état et prépare le moteur.
func NewEngine(dir string, logf func(string, ...any)) (*Engine, error) {
	cfg, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	// Activation par défaut de la projection Claude. Un poste où Claude Code est
	// présent doit projeter ses skills sans qu'aucune commande soit tapée :
	// l'app de bureau adopte la config existante et ne lance jamais `vecu setup`,
	// et les postes onboardés avant la feature skills n'ont jamais eu la question.
	// Sans ce défaut, `Projette()` reste un no-op et Claude ne voit aucun skill.
	//
	// La détection COMPLÈTE la liste, elle ne la remplace pas. Un outil est
	// proposé UNE fois : il entre alors dans `ProjectionsVues`, et le retirer de
	// `Projections` devient un choix qui tient. Avant, la condition portait sur
	// `len(cfg.Projections) == 0`, donc un « non » explicite était indiscernable
	// d'un poste jamais configuré et se faisait réécrire à chaque démarrage.
	//
	// Migration silencieuse : un poste dont `Projections` vaut déjà ["claude"]
	// voit simplement "claude" entrer dans `ProjectionsVues`, sans rien changer
	// d'autre. Un poste dont la liste est vide reçoit le défaut, comme avant.
	if detecteClaude(dir) && !slices.Contains(cfg.ProjectionsVues, "claude") {
		cfg.ProjectionsVues = append(cfg.ProjectionsVues, "claude")
		if !slices.Contains(cfg.Projections, "claude") {
			cfg.Projections = append(cfg.Projections, "claude")
		}
		_ = SaveConfig(dir, cfg) // best-effort : non persisté, la projection reste active ce run
	}
	st, err := LoadState(dir)
	if err != nil {
		return nil, err
	}
	if logf == nil {
		logf = log.Printf
	}
	locks, err := acquireLocks(dir, cfg.Montages)
	if err != nil {
		return nil, err
	}
	exclus := make(map[string]bool, len(cfg.Exclus))
	for _, nom := range cfg.Exclus {
		exclus[nom] = true
	}
	detaches := make(map[string]bool, len(cfg.Detaches))
	for _, nom := range cfg.Detaches {
		detaches[nom] = true
	}
	e := &Engine{
		dir:         dir,
		client:      NewHTTPClient(cfg.Server, cfg.Token),
		state:       st,
		exclus:      exclus,
		detaches:    detaches,
		montages:    cfg.Montages,
		projections: cfg.Projections,
		logf:        logf,
		locks:       locks,
	}
	e.indexeMontes()
	// L'intention d'adoption écrite par le parcours A survit au redémarrage du
	// service : c'est tout l'objet de `Config.Adoptes`.
	e.Importer(cfg.Adoptes)
	return e, nil
}

// Importer déclare les espaces dont le dossier local préexistant doit être
// adopté (donc envoyé au serveur). Sans cette déclaration explicite, un espace
// dont le dossier existe déjà ici n'est pas monté : voir aligneMontage.
func (e *Engine) Importer(noms []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.importer == nil {
		e.importer = make(map[string]bool, len(noms))
	}
	for _, nom := range noms {
		e.importer[nom] = true
	}
}

// SurSuppressionMassive branche le point de confirmation d'une suppression
// massive constatée. Le moteur ne décide pas seul : il demande.
//
// LAISSÉ À NIL - le daemon, un shell non interactif, un cron - aucune
// suppression massive ne part JAMAIS. C'est le comportement sûr par défaut, et
// c'est ce qui permet d'ouvrir une issue sans affaiblir la protection : un
// `rm -rf` accidentel ne s'accompagne jamais de quelqu'un qui répond à une
// question.
//
// Sans cette issue, la garde n'avait aucune sortie : un vrai ménage, ou un
// déplacement de fichiers entre deux espaces, bouclait indéfiniment - les
// fichiers réapparaissaient au rattrapage, cycle après cycle, sous les yeux de
// l'utilisateur.
func (e *Engine) SurSuppressionMassive(f func(espace string, constatees, verifies int) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.confirme = f
}

// indexeMontes reconstruit l'index des espaces montés depuis l'état.
func (e *Engine) indexeMontes() {
	e.montes = make(map[string]bool, len(e.state.Espaces))
	for _, nom := range e.state.Espaces {
		e.montes[nom] = true
	}
}

// Espaces : espaces montés (lecture, pour `status` et le daemon).
func (e *Engine) Espaces() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.state.Espaces...)
}

// espaceDe renvoie l'espace d'un chemin du dépôt, et s'il est monté ici.
func (e *Engine) espaceDe(rel string) (nom string, monte bool, dansEspace bool) {
	nom, _, dansEspace = strings.Cut(rel, "/")
	if !dansEspace {
		return "", false, false
	}
	return nom, e.montes[nom], true
}

// peutEcrire : ce compte a-t-il l'écriture sur cet espace, d'après le dernier
// `GET /espaces` ?
//
// FAUX sur absence, et c'est le point. Un espace hors de la table - serveur
// injoignable, cycle jamais abouti, espace démonté entre-temps - est un
// « je n'ai pas pu savoir », qui n'est pas « j'ai le droit ». Le seul appelant
// aujourd'hui (F3) écrit dans le dossier de l'espace : se tromper dans ce
// sens-là poserait un fichier que le serveur refuserait à chaque cycle, sous
// une alerte de perte qui serait fausse.
func (e *Engine) peutEcrire(espace string) bool { return e.ecriture[espace] }

// espaceValide : mêmes règles que le serveur, revérifiées ici.
//
// Le client ne confie pas la sûreté de son disque à la sortie d'un service
// distant. Un nom d'espace devient un `filepath.Join` avec la racine locale :
// un « .. » renvoyé par un serveur compromis ou bogué écrirait, lirait et
// supprimerait hors de la racine.
func espaceValide(nom string) bool {
	if nom == "" || len(nom) > 64 {
		return false
	}
	if strings.HasPrefix(nom, ".") || strings.HasPrefix(nom, "-") || strings.HasSuffix(nom, ".") {
		return false
	}
	bas := strings.ToLower(nom)
	for _, suffixe := range []string{".app", ".bundle", ".framework", ".kext", ".plugin", ".rtfd", ".lproj", ".localized", ".pkg", ".dsym"} {
		if strings.HasSuffix(bas, suffixe) {
			return false
		}
	}
	for _, r := range nom {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// cheminSur : un chemin du dépôt doit rester sous la racine locale, quoi que
// dise le serveur. Aucun segment vide, « . » ou « .. ».
func cheminSur(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// contientDesFichiers indique si le dossier local d'un espace existe déjà et
// contient au moins un fichier.
//
// Aucun motif d'ignore n'est appliqué ici, volontairement : la question posée
// n'est pas « Vécu synchroniserait-il ces fichiers ? » mais « ce dossier
// appartient-il déjà à quelqu'un ? ». Un dossier personnel ne contenant que des
// PDF, avec « *.pdf » dans le .vecuignore, serait sinon jugé vide et adopté -
// et le jour où le motif serait retiré, tout partirait d'un coup.
// Une erreur de parcours rend VRAI, jamais faux. « Je n'ai pas pu regarder »
// n'est pas « il n'y a rien » - c'est la confusion que tout ce fichier bannit,
// et elle serait ici la plus coûteuse : sur un « non », l'espace se monte et le
// contenu du dossier part au premier cycle où il redevient lisible. Un volume
// réseau décroché, une restriction TCC ou un `chmod 000` suffisent.
func (e *Engine) contientDesFichiers(espace string) bool {
	trouve := false
	racine := e.racineEspace(espace)
	filepath.WalkDir(racine, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if abs == racine && errors.Is(err, fs.ErrNotExist) {
				return nil // le dossier n'existe pas : là, on SAIT qu'il est vide
			}
			trouve = true // on ne sait pas : on suppose qu'il appartient à quelqu'un
			return fs.SkipAll
		}
		if !d.IsDir() && d.Name() != ".DS_Store" {
			trouve = true
			return fs.SkipAll // ne pas parcourir un dossier personnel entier
		}
		return nil
	})
	return trouve
}

// aligneMontage cale les espaces montés sur ceux auxquels le compte a accès,
// moins les exclusions locales. C'est le mécanisme qui tient la promesse de la
// v2 : un accès accordé dans l'interface fait apparaître le dossier ici, un
// accès retiré le fait disparaître, sans qu'aucune commande soit tapée.
//
// UN ESPACE N'ADOPTE JAMAIS UN DOSSIER LOCAL PRÉEXISTANT. Si le dossier existe
// déjà et contient des fichiers, l'espace n'est pas monté : le montage
// automatique fabriquerait sinon une collision de noms - une racine locale
// contient des dossiers personnels aux noms génériques (« clients », « notes »,
// « projets »), et le premier accès accordé à un espace homonyme publierait leur
// contenu. L'adoption doit être un geste explicite : `vecu sync --importer NOM`.
//
// Renvoie les espaces fraîchement montés (à hydrater).
func (e *Engine) aligneMontage() ([]string, error) {
	dispo, err := e.client.Espaces()
	if err != nil {
		return nil, err
	}
	dejaMonte := make(map[string]bool, len(e.state.Espaces))
	for _, nom := range e.state.Espaces {
		dejaMonte[nom] = true
	}
	connu := make(map[string]bool, len(e.state.Connus))
	for _, nom := range e.state.Connus {
		connu[nom] = true
	}

	montes := make([]string, 0, len(dispo))
	var nouveaux []string
	// Le serveur pourrait renvoyer deux fois le même nom. Dédoublonner ICI et
	// pas après la boucle : un doublon persisté dans `state.Espaces` ferait
	// partir chaque suppression de cet espace en double (la boucle de
	// suppression itère sur cette liste), et le même nom serait aussi inscrit
	// deux fois dans `nouveaux` et dans `state.Connus`. Le client ne confie pas
	// sa sûreté à la sortie du serveur.
	vus := make(map[string]bool, len(dispo))
	// Qui peut écrire où, refait à neuf à chaque cycle. Une map neuve et pas une
	// mise à jour : un droit RETIRÉ doit disparaître, et une map qu'on complète
	// ne perd jamais rien.
	e.ecriture = make(map[string]bool, len(dispo))
	// Les propositions se refont à neuf, comme `ecriture` et pour la même
	// raison : un accès retiré doit faire DISPARAÎTRE la proposition, et une
	// liste qu'on complète ne perd jamais rien.
	e.propositions = nil
	for _, esp := range dispo {
		if e.exclus[esp.Nom] || vus[esp.Nom] {
			continue
		}
		vus[esp.Nom] = true
		// Avant tout retour anticipé : F3 lit cette table à chaque cycle, y
		// compris ceux - la quasi-totalité - où le montage n'a pas bougé et où la
		// fonction sort plus bas sur `slices.Equal`.
		e.ecriture[esp.Nom] = esp.Ecriture
		if !espaceValide(esp.Nom) {
			e.laisseEspace(esp.Nom, "nom d'espace refusé par le client : il ne serait pas un dossier ordinaire dans la racine")
			continue
		}
		if !dejaMonte[esp.Nom] {
			// LE MONTAGE EST UN GESTE (DT2), avec un repli par ancienneté.
			//
			// Un espace JAMAIS VU ici ne descend plus tout seul : il devient une
			// proposition, et c'est la personne qui dit où le poser. C'est la
			// promesse du parcours B - « un accès accordé plus tard fait
			// apparaître une proposition, jamais un dossier surgi sans
			// prévenir ».
			//
			// Un espace DÉJÀ CONNU de ce poste continue exactement comme avant.
			// C'est ce repli qui évite une migration : `shared` se monte sans
			// qu'aucune entrée ne soit écrite nulle part, sur les deux postes,
			// et un aller-retour d'accès reste le geste banal qu'il a toujours
			// été.
			//
			// L'AMORÇAGE. Un poste qui n'a encore monté aucun espace descend ce
			// à quoi il a droit, d'un coup : c'est l'installation, et c'est le
			// moment où « ça apparaît tout seul » est exactement ce qu'on veut.
			// Ce n'est qu'ENSUITE que l'arrivée d'un espace devient un
			// événement qui mérite d'être annoncé. Même motif que le signal des
			// skills, pour la même raison : sans amorçage, le premier démarrage
			// proposerait tout, et une liste de propositions longue comme la
			// liste des espaces n'est pas une proposition, c'est un formulaire.
			//
			// `len(Connus) == 0` et pas un champ neuf : `Connus` porte déjà
			// « ce poste a-t-il monté quelque chose », il est persisté depuis
			// juillet, et vide ou absent veulent dire la même chose ici - donc
			// pas de piège d'`omitempty`, pas de migration, pas de champ de plus
			// à ne pas oublier dans `clone()`.
			amorce := len(e.state.Connus) == 0
			_, choisi := e.montages[esp.Nom]
			if !amorce && !connu[esp.Nom] && !choisi && !e.importer[esp.Nom] {
				e.propositions = append(e.propositions, Proposition{
					Nom: esp.Nom, Libelle: esp.Affichage(), Ecriture: esp.Ecriture, Fichiers: esp.Fichiers,
				})
				continue
			}
			// La garde ne vaut QUE pour un espace jamais monté sur ce poste. Un
			// espace déjà connu ici a laissé des résidus au démontage (fichiers
			// modifiés, binaires refusés, fichiers ignorés) : les prendre pour un
			// dossier personnel bloquerait définitivement tout aller-retour
			// d'accès, qui est le geste le plus banal du produit.
			if !connu[esp.Nom] && !e.importer[esp.Nom] && e.contientDesFichiers(esp.Nom) {
				e.laisseEspace(esp.Nom, "le dossier choisi pour cet espace contient déjà des fichiers : l'espace n'est PAS monté et rien n'est envoyé. "+
					"Le rejoindre dans un dossier vide, ou partager le dossier existant depuis le menu (« Partager un dossier… »), qui annonce ce qui partira avant d'envoyer quoi que ce soit")
				continue
			}
			nouveaux = append(nouveaux, esp.Nom)
		}
		montes = append(montes, esp.Nom)
	}
	sort.Strings(montes)
	if slices.Equal(montes, e.state.Espaces) {
		return nil, nil
	}

	nouveau := make(map[string]bool, len(montes))
	for _, nom := range montes {
		nouveau[nom] = true
	}
	for _, ancien := range e.state.Espaces {
		if !nouveau[ancien] {
			// Une erreur de démontage ne gèle pas la synchronisation des autres
			// espaces : un fichier verrouillé ou un volume débranché suffirait
			// sinon à tuer la sync du poste entier.
			if err := e.demonte(ancien); err != nil {
				// Pas de « nouvel essai au prochain cycle » : `state.Espaces` est
				// réécrit juste en dessous sans cet espace, et `demonte` a déjà purgé
				// les entrées concernées. Il n'y aura pas de seconde tentative -
				// le dire plutôt que promettre.
				e.laisseEspace(ancien, "démontage incomplet ("+err.Error()+") : les fichiers concernés restent sur ce poste "+
					"comme des résidus ordinaires, sans conséquence pour la synchronisation")
			}
		}
	}
	e.logf("espaces montés : %v", montes)
	e.state.Espaces = montes
	for _, nom := range nouveaux {
		if !connu[nom] {
			e.state.Connus = append(e.state.Connus, nom)
		}
	}
	sort.Strings(e.state.Connus)
	e.indexeMontes()
	return nouveaux, nil
}

// sansLien vérifie qu'aucun composant du chemin n'est un lien symbolique.
//
// Écrire ou supprimer à travers un lien sortirait de la racine locale : la
// garde du scan ne suffit pas, puisque c'est le pull qui écrit. Un lien créé
// par l'utilisateur (« equipe/photos » vers « ~/Documents/photos ») redirige
// une écriture parfaitement légitime côté serveur vers n'importe où.
//
// La garde marche le MÊME chemin que l'écriture, donc elle part du point de
// montage quand l'espace en a un : c'est exactement le découpage de `abs`, et
// les deux doivent rester d'accord. Partir de la racine pour un espace monté
// ailleurs inspectait un chemin qui n'existe pas ; l'ENOENT faisait rendre nil,
// et l'écriture partait SANS GARDE à travers un lien éventuel. Dormant tant que
// `Montages` est vide, ça cesse de l'être à la première entrée écrite.
//
// Le dossier de montage lui-même n'est pas inspecté, par symétrie avec la
// racine, qui ne l'a jamais été : c'est le point de départ, pas un composant
// traversé. Un montage qui serait lui-même un lien se refuse au moment du
// montage, une fois, et pas à chaque écriture de chaque cycle.
func (e *Engine) sansLien(rel string) error {
	espace, reste, _ := strings.Cut(rel, "/")
	courant := e.dir
	segments := strings.Split(rel, "/")
	if racine, montee := e.montages[espace]; montee {
		courant = racine
		segments = nil
		if reste != "" {
			segments = strings.Split(reste, "/")
		}
	}
	for _, seg := range segments {
		courant = filepath.Join(courant, seg)
		info, err := os.Lstat(courant)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			// Pas encore créé, ou un composant du chemin n'est pas un dossier :
			// dans les deux cas il n'y a rien à traverser, donc rien à refuser.
			// Rendre l'ENOTDIR bloquait le retrait d'une entrée d'état devenue
			// impossible, qui repartait alors en DELETE à chaque cycle.
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("lien symbolique sur le chemin (%s) : le contenu sortirait de la racine locale", seg)
		}
	}
	return nil
}

// ecris écrit un fichier du dépôt sur le disque, sans jamais traverser un lien.
//
// Rend `ecrit` : le refus d'écrire n'est PAS une erreur de cycle (il ne doit
// pas geler la sync des autres fichiers), mais l'appelant doit savoir que rien
// n'a atterri sur le disque. Sans cette distinction il tamponnait `state.Files`
// comme si le fichier était là - et un état qui déclare synchronisé ce que le
// disque n'a jamais reçu rend TOUT raisonnement sur l'absence faux : retirer le
// lien plus tard faisait constater une absence parfaitement franche, et le
// fichier partait chez tous les membres.
//
// `origine` dit d'où vient l'écriture (« pull » ou « rattrapage ») : les deux
// chemins écrivent le même contenu de la même façon, mais pas pour la même
// raison, et le journal est inutilisable si les deux se ressemblent.
func (e *Engine) ecris(rel, contenu, origine string) (ecrit bool, err error) {
	avant := e.etatDisque(rel)
	if err := e.sansLien(rel); err != nil {
		// Le motif exact, sans y ajouter d'explication : `sansLien` dit lui-même
		// quand c'est un lien. Annoncer « le contenu sortirait de la racine » sur
		// une erreur de droits raconterait une histoire de sûreté qui est fausse,
		// dans le seul canal qui existe contre le silence.
		e.laisseServeur(rel, "non écrit : "+err.Error())
		return false, nil
	}
	// Ne pas réécrire ce qui est déjà là. Un WriteFile inconditionnel touche le
	// mtime, ce que fsnotify voit comme une modification : le daemon se réveille,
	// relance un cycle, réécrit, se réveille. Le journal le mesure déjà - 186 des
	// 249 lignes `écrit` de la fenêtre du 13 au 15/08 ne changeaient rien, soit
	// 75 % - et la ligne mentait, puisqu'aucune écriture n'avait lieu.
	//
	// Rend `true` et non `false` : le contrat de ce retour est « le disque
	// porte-t-il le contenu attendu », pas « ai-je appelé WriteFile ». Rendre
	// false ferait sauter la mise à jour de l'état connu par l'appelant, donc
	// repousser au cycle suivant un contenu que le serveur a déjà.
	apres := empreinteTaille(hashContent([]byte(contenu)), int64(len(contenu)))
	if avant == apres {
		e.logf("inchangé (%s) : %s — %s", origine, rel, avant)
		return true, nil
	}
	abs := e.abs(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		// Un FICHIER occupe un dossier du chemin (une racine d'espace remplacée
		// par un fichier, typiquement). Rendre l'erreur gèle le cycle, donc la
		// synchronisation de TOUS les espaces du poste, à cause d'un seul chemin.
		if errors.Is(err, syscall.ENOTDIR) {
			e.laisseServeur(rel, "non écrit : un fichier occupe un dossier de ce chemin sur ce poste "+
				"(le renommer laissera le contenu redescendre)")
			return false, nil
		}
		return false, err
	}
	if err := os.WriteFile(abs, []byte(contenu), 0o644); err != nil {
		// Un DOSSIER local occupe ce chemin (bascule fichier -> dossier faite ici
		// pendant qu'un autre poste modifiait le fichier). Rendre l'erreur gèle le
		// head, donc la synchronisation de TOUS les espaces du poste, à cause d'un
		// seul chemin. L'écraser détruirait le dossier local. On refuse et on le dit.
		if info, errStat := os.Lstat(abs); errStat == nil && info.IsDir() {
			e.laisseServeur(rel, "non écrit : un dossier occupe ce chemin sur ce poste "+
				"(le renommer laissera le fichier redescendre)")
			return false, nil
		}
		return false, err
	}
	e.logf("écrit (%s) : %s — avant %s, après %s", origine, rel, avant, apres)
	return true, nil
}

// rattrape redescend ce qui manque sur le disque, à chaque cycle.
//
// Une seule règle, comparative et donc idempotente : tout chemin visible du
// serveur, dans un espace monté, qui n'est pas connu de l'état OU pas présent
// sur le disque, est retéléchargé. Elle remplace trois mécanismes qui manquaient
// chacun un cas :
//
//   - espace fraîchement monté : son contenu n'est pas dans le delta depuis
//     notre head, il était hors périmètre ;
//   - élargissement de droits DANS un espace déjà monté : aucun commit n'est
//     produit par un changement de droits, le delta ne rapporte donc rien ;
//   - dossier local supprimé ou déplacé alors que la suppression a été refusée
//     par la garde : c'est le chemin de retour, le dossier se reconstitue.
//
// Un téléchargement en échec n'est pas définitif : le cycle suivant repose la
// même question et reprend là où il en est.
//
// `vu` DOIT être le scan pris avant le push de ce cycle, pas un scan frais : la
// branche « supprimé pendant ce cycle » compare l'état du disque à cette
// photo-là. Un scan pris après le pull dirait exactement l'inverse.
func (e *Engine) rattrape(perimetre []string, vu *scan, acceptes map[string]string, reportes map[string]bool) error {
	// Compté par espace, jamais ligne à ligne : un vault dont on ignore les
	// images produirait des centaines de lignes identiques. Mais le dire, car
	// c'est ici qu'un espace fraîchement monté ou un élargissement de droits
	// s'hydrate - et un espace qui arrive incomplet en silence est le pire cas.
	ignores := map[string]int{}
	// Les chemins reportés lors d'un cycle ANTÉRIEUR : le delta ne les
	// reproposera pas (le head les a dépassés), donc c'est ici qu'on va les
	// chercher. Photo prise avant la boucle, qui modifie le registre.
	aRepecher := make(map[string]bool, len(e.state.Reportes))
	for _, p := range e.state.Reportes {
		if !reportes[p] {
			aRepecher[p] = true
		}
	}
	for _, p := range perimetre {
		if _, monte, _ := e.espaceDe(p); !monte || !cheminSur(p) {
			continue
		}
		if e.ignore(p) || e.ignoreSurLeChemin(p) {
			// Boucle entre deux mécanismes du même cycle : un fichier ignoré et
			// supprimé localement était retéléchargé ici à chaque cycle, pendant
			// que le push refusait - à juste titre - d'en propager la suppression.
			// L'utilisateur voyait le fichier revenir indéfiniment sans qu'aucun
			// des deux mécanismes n'ait tort.
			//
			// L'entrée sort AUSSI de l'état, et ce n'est pas un détail : la laisser
			// avec un hash que le disque ne porte plus armait une bombe à
			// retardement. Le jour où le motif serait retiré, la suppression locale
			// vieille de N cycles partirait chez tous les membres, sans que rien ne
			// relie la cause à l'effet. Éditer le .vecuignore ne supprime pas du
			// contenu partagé - au contraire, retirer un motif fait redescendre.
			e.oublie(p)
			ignores[strings.SplitN(p, "/", 2)[0]]++
			continue
		}
		if aRepecher[p] {
			// REPÊCHAGE. Ce chemin a été reporté à un cycle antérieur : le head a
			// dépassé le changement, `Sync` ne le re-listera jamais, et le bloc
			// `connu` ci-dessous le ferait sauter au motif qu'il est « connu et
			// présent » - ce qu'il est. Sauf que le contenu présent est celui de ce
			// poste, pas celui que le serveur sert depuis. Personne d'autre ne
			// viendra le proposer : on va le chercher.
			//
			// Branche SŒUR du bloc `connu`, et non un aménagement dedans : les
			// gardes de ce bloc supposent toutes qu'un fichier ordinaire présent a
			// déjà fait `continue`, et l'une d'elles répondrait « un lien ou un
			// dossier occupe ce chemin » sur un fichier parfaitement ordinaire.
			if course, motif := e.changeDepuisLeScan(p, vu); course {
				// Une écriture est encore en cours : le repêchage écraserait ce que
				// le report protège. Le chemin reste au registre.
				e.laisseServeur(p, "non repêché ce cycle : "+motif)
				continue
			}
			// tombe dans le téléchargement, plus bas
		} else if _, connu := e.state.Files[p]; connu {
			// Lstat, comme partout ailleurs : `Stat` suit les liens, si bien qu'un
			// lien vers n'importe quel fichier existant faisait passer le chemin
			// pour « connu et présent » à jamais - un fichier réellement manquant
			// n'était alors plus jamais redescendu.
			info, err := os.Lstat(e.abs(p))
			if err == nil && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				continue // connu et présent : rien à faire
			}
			if err == nil {
				// Un lien ou un dossier occupe le chemin. Le télécharger ne mènerait
				// à rien : l'écriture serait refusée à chaque cycle.
				e.laisseServeur(p, "non redescendu : un lien symbolique ou un dossier occupe ce chemin sur ce poste")
				continue
			}
			if !errors.Is(err, fs.ErrNotExist) {
				// Droits, volume décroché : le fichier est peut-être là, on n'en sait
				// rien. Le redescendre serait un GET par fichier et par cycle pour une
				// écriture qui échouera de toute façon. Seule une absence FRANCHE
				// justifie un téléchargement.
				e.laisseServeur(p, "non redescendu : ce chemin n'est pas lisible sur ce poste ("+err.Error()+")")
				continue
			}
			if _, vuAuScan := vu.fichiers[p]; vuAuScan {
				// Il était là au scan de ce cycle et il n'y est plus : la
				// suppression est tombée PENDANT le cycle, trop tard pour que le
				// push la voie. La redescendre l'annulerait sans trace - le geste
				// de l'utilisateur défait sous ses yeux. Le prochain push la
				// constatera et l'enverra.
				// Ne rien promettre ici : le prochain cycle la CONSTATERA, mais la
				// garde anti-suppression massive peut encore la retenir. Annoncer
				// « elle partira » serait faux le jour où l'utilisateur jette un
				// gros dossier - exactement le moment où il lit ce message.
				e.laisseServeur(p, "supprimé pendant ce cycle : non redescendu, la suppression sera examinée au prochain cycle")
				continue
			}
		}
		// La seconde porte du refus de collision. Posée AVANT le téléchargement :
		// le contenu ne servirait à rien, et un GET par fichier et par cycle sur
		// un chemin qu'on refusera de toute façon est le déluge par une autre
		// porte. Même gabarit que le pull, même message.
		if e.refusDeReport(p) == "chemin non suivi" {
			if motif := e.motifCollisionDeCasse(p); motif != "" {
				e.laisseServeur(p, motif)
				continue
			}
		}
		contenu, commit, err := e.client.Read(p)
		if err != nil {
			// Le prochain cycle réessaiera : l'état n'a pas bougé pour ce chemin.
			e.laisseServeur(p, "non redescendu ce cycle ("+err.Error()+")")
			continue
		}
		empreinte := hashContent([]byte(contenu))
		// Le rattrapage n'a pas de branche de report : il ne descend que ce qui
		// manque au disque ou à l'état. Le dire tel quel, plus la précondition
		// qui manquait s'il y en a une.
		motifRefus := "rattrapage, hors du chemin de report"
		if r := e.refusDeReport(p); r != "" {
			motifRefus += " (" + r + ")"
		}
		if err := e.preserveLocalEditIfAny(p, empreinte, acceptes, motifRefus); err != nil {
			return err
		}
		ecrit, err := e.ecris(p, contenu, "rattrapage")
		if err != nil {
			return err
		}
		if !ecrit {
			continue // rien sur le disque : ne pas prétendre le contraire
		}
		e.state.Files[p] = empreinte
		// Le commit rendu par le GET : le serveur date ce qu'il sert, donc plus
		// rien n'est supposé ici.
		//
		// Repli sur le head connu face à un serveur qui ne le rend pas encore
		// (fenêtre de bascule). Et sur le head, pas sur la chaîne vide : le
		// rattrapage lit l'état COURANT du serveur, donc un commit au moins aussi
		// récent que ce head. Déclarer le head est une SOUS-estimation, et une
		// sous-estimation est sûre - la fusion rejoue un changement que ce poste
		// porte déjà, elle ne peut rien écraser. La chaîne vide serait sûre aussi,
		// mais elle ferait une copie de conflit par fichier au premier montage
		// d'un espace : du bruit à l'échelle du vault entier, pour rien.
		if commit == "" {
			commit = e.state.Head
		}
		e.noteVersion(p, commit)
		// Le contenu est arrivé : le chemin sort du registre des reportés.
		e.oublieReport(p)
	}
	for espace, n := range ignores {
		e.laisseServeur(espace, fmt.Sprintf("%d chemin(s) de cet espace ne descendent pas sur ce poste : ils sont écartés par le %s. "+
			"Ils restent sur le serveur et chez les autres membres.", n, ignoreFichier))
	}
	return nil
}

// demonte retire du disque les fichiers d'un espace sorti du périmètre.
//
// INVARIANT (le même que partout ailleurs) : on ne supprime qu'un fichier
// PROPRE, dont le contenu disque est exactement celui qu'on a synchronisé. Une
// édition locale non poussée reste sur le disque, comme les fichiers que le
// serveur n'a jamais acceptés (binaires refusés en v2) : un retrait d'accès ne
// doit jamais détruire la seule copie d'un contenu.
//
// Une erreur sur un fichier n'interrompt pas le démontage des autres : elle est
// accumulée et remontée à la fin. Le fichier concerné reste alors sur le disque,
// comme n'importe quel résidu - c'est sans conséquence, un espace déjà connu de
// ce poste se remonte malgré ses résidus.
func (e *Engine) demonte(nom string) error {
	prefixe := nom + "/"
	// LE GESTE DE RETRAIT NE SUPPRIME RIEN. `e.detaches` porte la seule
	// différence entre deux démontages que tout oppose : un accès retiré par le
	// serveur fait partir les fichiers, parce que ce contenu ne nous appartient
	// plus ; « retirer ce dossier de la synchronisation » les garde, parce que
	// c'est la promesse écrite de F1.
	//
	// L'espace est simplement oublié : ses entrées d'état partent, son dossier
	// reste intact, et il redevient un dossier ordinaire de la machine. Rien
	// n'est retiré, donc rien n'est compté.
	if e.detaches[nom] {
		return e.detache(nom, prefixe)
	}
	retires, gardes := 0, 0
	var echecs []string
	for p, connu := range e.state.Files {
		if !strings.HasPrefix(p, prefixe) {
			continue
		}
		if !cheminSur(p) {
			e.oublie(p) // clé corrompue : la purger, jamais la suivre
			continue
		}
		if err := e.sansLien(p); err != nil {
			// Un démontage ne supprime rien à travers un lien. Le fichier reste
			// sur le disque comme n'importe quel résidu, ce qui est sans
			// conséquence : un espace déjà connu se remonte malgré ses résidus.
			e.laisseServeur(p, "non retiré du disque au démontage : "+err.Error())
			e.oublie(p)
			continue
		}
		abs := e.abs(p)
		h, err := hashFile(abs)
		switch {
		case err != nil:
			// Absent du disque, ou illisible : rien à retirer.
		case h == connu:
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				// Le fichier reste sur le disque comme un résidu ordinaire. Le
				// garder dans l'état en ferait une entrée immortelle : plus
				// scannée (espace démonté), plus réconciliée, jamais retirée.
				echecs = append(echecs, p)
				e.oublie(p)
				continue
			}
			removeEmptyParents(e.dir, abs)
			retires++
		default:
			gardes++
		}
		e.oublie(p)
	}
	// Les marque-pages de cet espace, y compris ceux dont le chemin n'a pas
	// d'entrée dans `Files`. La boucle ci-dessus itère `Files` : elle ne peut pas
	// les voir, et plus rien ne le pourra une fois l'espace démonté.
	for p := range e.state.Versions {
		if strings.HasPrefix(p, prefixe) {
			delete(e.state.Versions, p)
		}
	}
	if gardes > 0 {
		e.logf("espace %s démonté : %d fichiers retirés, %d gardés en local (modifiés et jamais poussés)", nom, retires, gardes)
	} else {
		e.logf("espace %s démonté : %d fichiers retirés", nom, retires)
	}
	// `Connus` sert à distinguer « dossier personnel qui portait déjà ce nom »
	// (jamais adopté) de « dossier que Vécu a lui-même créé ici » (des résidus,
	// qu'il faut savoir retraverser au remontage). Il n'était jamais purgé : un
	// dossier démonté, vidé, puis réutilisé pour des fichiers personnels était
	// publié au remontage sans le moindre avertissement - le correctif
	// d'ergonomie rouvrait la fuite qu'il devait fermer.
	//
	// S'il ne reste rien sur le disque, il n'y a aucun résidu à protéger : ce
	// nom redevient un nom comme un autre, et la garde d'adoption s'applique de
	// nouveau au prochain montage.
	if !e.contientDesFichiers(nom) {
		e.state.Connus = slices.DeleteFunc(e.state.Connus, func(n string) bool { return n == nom })
	}
	if len(echecs) > 0 {
		return fmt.Errorf("%d fichier(s) non retirés, dont %s", len(echecs), echecs[0])
	}
	return nil
}

// detache oublie un espace sans toucher au disque.
//
// Le pendant non destructeur de `demonte`, et l'essentiel tient en ce qu'il ne
// fait PAS : aucun `os.Remove`, aucun `removeEmptyParents`, aucun comptage de
// fichiers retirés. Le dossier et tout son contenu restent exactement là où ils
// sont, et redeviennent une affaire entre la personne et son Finder.
//
// `Connus` n'est pas purgé, et c'est délibéré. La purge de `demonte` est
// conditionnée à un dossier devenu vide ; ici il ne l'est jamais. Deux
// conséquences, toutes deux voulues : l'amorçage de `aligneMontage`
// (`len(Connus) == 0`) ne peut pas se redéclencher et monter d'un coup tous les
// espaces disponibles, et si la personne remonte cet espace un jour, la garde
// d'adoption ne le prendra pas pour un dossier personnel homonyme.
func (e *Engine) detache(nom, prefixe string) error {
	oublies := 0
	for p := range e.state.Files {
		if strings.HasPrefix(p, prefixe) {
			e.oublie(p)
			oublies++
		}
	}
	// Les marque-pages dont le chemin n'a pas d'entrée dans `Files` : la boucle
	// ci-dessus ne peut pas les voir, et plus rien ne le pourra ensuite.
	for p := range e.state.Versions {
		if strings.HasPrefix(p, prefixe) {
			delete(e.state.Versions, p)
		}
	}
	e.logf("espace %s retiré de la synchronisation : %d fichiers oubliés, AUCUN supprimé — le dossier reste sur ce poste", nom, oublies)
	return nil
}

// acquireLock pose un verrou exclusif non bloquant sur .vecu/lock : deux
// processus (daemon + `vecu sync`) sur le même dossier écraseraient
// mutuellement .vecu/state.json en last-writer-wins. Le flock est relâché
// automatiquement à la mort du processus (pas de verrou orphelin).
// Source: https://pkg.go.dev/golang.org/x/sys/unix#Flock
func acquireLock(dir string) (*os.File, error) {
	return verrouilleFichierRacine(dir)
}

// Close relâche le verrou du dossier. À appeler quand un processus a fini
// d'utiliser un moteur mais continue de vivre (l'assistant `vecu setup`, qui
// enchaîne sur le démarrage du service : sans relâchement, le daemon refuserait
// de se lancer sur un dossier déjà verrouillé).
func (e *Engine) Close() error {
	var premiere error
	for _, f := range e.locks {
		// Tous relâchés, même si l'un échoue : en garder un fermerait le dossier
		// à tout autre processus jusqu'à la mort de celui-ci.
		if err := f.Close(); err != nil && premiere == nil {
			premiere = err
		}
	}
	e.locks = nil
	return premiere
}

// Skipped : nombre de fichiers du dernier cycle qui n'existent QUE sur ce
// poste. Un import qui en laisse doit le dire au lieu d'annoncer un succès
// complet.
//
// Ne compte que le genre `fichier` : une remarque d'espace, ou un chemin qui
// est sur le serveur et n'a pas pu être appliqué ici, ne signale aucune perte.
// Les compter ferait annoncer un import partiel là où rien ne manque.
func (e *Engine) Skipped() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, l := range e.laisses {
		if l.Perdu() {
			n++
		}
	}
	return n
}

// Laisses : détail de ce que le dernier cycle n'a pas synchronisé.
func (e *Engine) Laisses() []Laisse {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Laisse(nil), e.laisses...)
}

// laisse signale un contenu qui n'existe QUE sur ce poste. Le genre qui alerte.
func (e *Engine) laisse(chemin, raison string) {
	e.laisses = append(e.laisses, Laisse{Chemin: chemin, Raison: raison, Genre: GenreFichier})
	e.logf("non synchronisé : %s (%s)", chemin, raison)
}

// laisseEspace signale une situation qui concerne un espace entier. Le contenu
// est bien sur le serveur : ce n'est pas un fichier perdu, et un import ne doit
// pas sortir en erreur pour ça.
func (e *Engine) laisseEspace(espace, raison string) {
	e.laisses = append(e.laisses, Laisse{Chemin: espace, Raison: raison, Genre: GenreEspace})
	e.logf("espace %s : %s", espace, raison)
}

// laisseServeur signale un chemin qui EST sur le serveur et n'a pas pu arriver
// ou être appliqué ici. Rien n'est perdu : le dire sans crier au loup.
func (e *Engine) laisseServeur(chemin, raison string) {
	e.laisses = append(e.laisses, Laisse{Chemin: chemin, Raison: raison, Genre: GenreServeur})
	e.logf("non appliqué ici : %s (%s)", chemin, raison)
}

// laisseHorsPerimetre signale un contenu que Vécu ne synchronisera jamais, par
// construction. Ni une perte (personne n'attendait ce fichier ailleurs), ni une
// panne (aucun cycle ne le réparera) : un fait stable. Il se compte, il ne
// s'énumère pas.
func (e *Engine) laisseHorsPerimetre(chemin, raison string) {
	e.laisses = append(e.laisses, Laisse{Chemin: chemin, Raison: raison, Genre: GenreHorsPerimetre})
	e.logf("hors périmètre : %s (%s)", chemin, raison)
}

// State expose l'état courant (lecture, pour `status` et l'app de bureau).
//
// Copie PROFONDE : le retour ne partage ni map ni slice avec l'état interne.
// Sans ça, `return e.state` ne copie que les en-têtes : un lecteur concurrent
// (l'app de bureau qui itère Files/Espaces pour le menu) accéderait à la même
// map pendant qu'un cycle y écrit sous verrou - lecture+écriture concurrente de
// map, que Go fait paniquer. Le verrou ne suffit donc pas : il faut aussi que
// la valeur rendue soit détachée.
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.clone()
}

// SyncOnce exécute un cycle complet. Idempotent : sûr à répéter.
//
// Ordre : push puis pull, tous deux ancrés sur le MÊME head de début de cycle
// (`base`). Le push envoie les changements locaux ; le pull repart de `base` et
// ramène toute la vérité serveur produite depuis - y compris l'effet des merges
// et les copies de conflit issues de nos propres push. Le pull est ainsi
// l'unique source qui met à jour le disque et l'état connu.
func (e *Engine) SyncOnce() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.laisses = nil
	motifs, err := LoadIgnores(e.dir)
	if err != nil {
		return fmt.Errorf("lecture du %s : %w", ignoreFichier, err)
	}
	e.motifs = motifs

	if _, err := e.aligneMontage(); err != nil {
		return fmt.Errorf("montage : %w", err)
	}
	// APRÈS le montage, jamais avant : les motifs d'un espace se lisent à sa
	// racine, et on ne sait où elle est qu'une fois la table alignée.
	e.chargeMotifsEspaces()
	// Un seul scan par cycle, partagé : le push décide des suppressions à partir
	// de ce qu'il a vu, et le rattrapage a besoin de la MÊME photo pour
	// distinguer « ce fichier me manque » de « ce fichier vient d'être supprimé ».
	vu, err := e.scanLocal()
	if err != nil {
		return fmt.Errorf("scan : %w", err)
	}
	base := e.state.Head
	// Avant le push, qui est le seul lecteur du marque-page.
	e.migreVersions()
	// `acceptes` traverse le cycle comme `vu` : ce que le serveur vient de
	// prendre n'est plus une édition locale non poussée, et le pull qui suit doit
	// le savoir avant de ranger quoi que ce soit en copie de conflit.
	acceptes, err := e.pushLocal(base, vu)
	if err != nil {
		return fmt.Errorf("push : %w", err)
	}
	// `reportes` traverse la fin du cycle comme `acceptes` en traverse le début.
	//
	// `reconcilePerimeter` s'en sert pour ne pas retirer du disque un chemin que
	// le cycle vient de préserver. `rattrape` s'en sert pour la raison INVERSE :
	// il va chercher les chemins reportés aux cycles ANTÉRIEURS (registre
	// `state.Reportes`), en écartant ceux de ce cycle-ci. Que `rattrape` ne
	// retélécharge pas un reporté du cycle courant tient aussi par ses propres
	// gardes (« connu et présent »), et c'est tant mieux : deux raisons valent
	// mieux qu'une sur un chemin d'écriture.
	reportes, err := e.pull(base, vu, acceptes)
	if err != nil {
		return fmt.Errorf("pull : %w", err)
	}
	// Un seul GET /tree par cycle, partagé : il sert à redescendre ce qui manque
	// et à retirer ce qui est sorti du périmètre.
	perimetre, err := e.client.Tree()
	if err != nil {
		return fmt.Errorf("périmètre : %w", err)
	}
	if err := e.rattrape(perimetre, vu, acceptes, reportes); err != nil {
		return fmt.Errorf("rattrapage : %w", err)
	}
	if err := e.reconcilePerimeter(perimetre, reportes); err != nil {
		return fmt.Errorf("réconciliation : %w", err)
	}
	e.state.Cycle = time.Now().Format(time.RFC3339)
	e.state.Laisses = e.laisses
	return e.state.Save(e.dir)
}

// Cycle : un cycle de sync suivi de la projection des skills vers l'agent local.
// C'est le point d'entrée des appelants (daemon, `vecu sync`, `vecu setup`) : il
// centralise l'enchaînement pour qu'aucun n'oublie de projeter. La projection est
// best-effort - un échec de projection n'est jamais un échec de cycle (le contenu
// est synchronisé ; seul le raccourci vers l'agent peut manquer, et il est signalé).
func (e *Engine) Cycle() error {
	if err := e.SyncOnce(); err != nil {
		// Le cycle n'a pas abouti : on n'adopte pas (l'adoption déciderait en
		// regardant un dépôt périmé), mais on dit ce qui attend. Se taire ferait
		// croire qu'il n'y a rien à ranger.
		if err := e.Recense(); err != nil {
			e.logf("recensement des skills : %v", err)
		}
		return err
	}
	// Adoption APRÈS le cycle, et c'est le point qui a demandé le plus de
	// réflexion. Avant, le skill partait au serveur dans le même cycle - une
	// latence de moins. Mais l'adoption décide en regardant ce qui existe déjà
	// dans `shared/skills/`, et avant le pull elle regarde un dépôt périmé :
	// deux postes qui ont chacun leur version d'un même slug produisaient une
	// substitution SILENCIEUSE (la version de l'autre prend le nom canonique, la
	// sienne part en copie de conflit, aucun canal ne le dit). Le garde-fou
	// « existe déjà » était écrit et court-circuité par l'ordre.
	//
	// Après le cycle, il voit l'état réel du dépôt, et le skill part au cycle
	// suivant - quinze secondes de poll. C'est le bon prix.
	if err := e.Adopte(); err != nil {
		e.logf("adoption : %v", err)
	}
	if err := e.Projette(); err != nil {
		e.logf("projection : %v", err)
	}
	// Le contexte juste après la projection, et pour la même raison : les deux
	// rendent utilisable, par l'agent local, ce que le cycle vient de
	// synchroniser. APRÈS `SyncOnce` et jamais avant - c'est ce qui laisse
	// partir la suppression d'un `CLAUDE.md` renommé en `AGENTS.md` avant qu'un
	// pointeur soit posé à sa place.
	if err := e.Contexte(); err != nil {
		e.logf("contexte portable : %v", err)
	}
	// Le signal en dernier, et best-effort comme les deux précédents : il porte
	// une information, jamais un incident.
	if err := e.Signale(); err != nil {
		e.logf("signal des skills : %v", err)
	}
	return nil
}

// absentCommeFichier : peut-on affirmer, à cet instant, que plus aucun fichier
// n'occupe ce chemin ?
//
// La question posée n'est PAS « ce chemin existe-t-il ? ». La nuance porte le
// geste le plus banal d'un vault : renommer un dossier en fichier du même nom,
// ou l'inverse. L'ancien chemin doit alors être supprimé, sinon git refuse le
// conflit dossier/fichier et la sync gèle dessus - sans que rien n'échoue.
//
// Lstat plutôt que Stat, pour ne pas suivre un lien.
// Source: https://pkg.go.dev/os#Lstat
func (e *Engine) absentCommeFichier(rel string) bool {
	info, err := os.Lstat(e.abs(rel))
	switch {
	case err == nil && !info.IsDir():
		return false // quelque chose occupe encore ce chemin
	case err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
		return false // droits, E/S, volume décroché : on n'a pas pu savoir
	}
	// Reste : le chemin n'existe pas (ENOENT), il ne PEUT pas exister parce
	// qu'un composant est devenu un fichier (ENOTDIR), ou il a laissé place à un
	// dossier. Dans les trois cas, le fichier connu est bel et bien parti.
	return true
}

// suppressionsConstatees rend les chemins connus dont la suppression a été
// CONSTATÉE pendant ce cycle, et rien d'autre.
//
// C'est le cœur du modèle. Le moteur déduisait auparavant une suppression
// d'une absence au scan ; or un fichier manque d'un scan pour cinq raisons qui
// ne sont pas des suppressions - dossier parent illisible, lien symbolique sur
// le chemin, motif d'ignore, dossier d'espace disparu, course pendant le cycle
// - et chacune envoyait un DELETE, donc supprimait chez tous les membres.
//
// Un chemin n'entre ici que si les cinq conditions tiennent. Toute autre
// situation est une absence d'information : elle est signalée comme telle et
// reposée au cycle suivant, jamais convertie en suppression.
func (e *Engine) suppressionsConstatees(vu *scan) []string {
	var constatees []string
	signale := map[string]bool{} // une remarque par dossier, jamais par fichier
	for p := range e.state.Files {
		if _, monte, _ := e.espaceDe(p); !monte {
			continue // hors des espaces montés : jamais scanné, donc jamais supprimé
		}
		if !cheminSur(p) {
			// `state.Files` est relu depuis .vecu/state.json sans validation de
			// clés, et ce chemin va être joint à la racine locale. Tous les
			// consommateurs de `state.Files` posent la même garde.
			continue
		}
		if _, present := vu.fichiers[p]; present {
			continue
		}
		if e.ignore(p) || e.ignoreSurLeChemin(p) {
			// Absent du scan parce qu'IGNORÉ, pas parce que supprimé. Le pousser
			// comme une suppression effacerait du dépôt partagé un fichier qu'un
			// autre client y a légitimement mis.
			continue
		}
		if vu.incertains[p] {
			continue // vu sur le disque, état non établi : surtout pas supprimé
		}
		parent := path.Dir(p)
		if !vu.certifie(parent) {
			// Un espace dont la racine n'a pas été parcourue est signalé par la
			// garde de `pushLocal`, EN TANT QU'ESPACE. Le redire ici le classerait
			// en fichier perdu, ce qu'il n'est pas : le contenu est sur le serveur.
			// Même prédicat des deux côtés, sur `vu` et non sur l'ordre des
			// messages : voir le couplage assumé, documenté dans `pushLocal`.
			espace, _, _ := strings.Cut(p, "/")
			dejaDit := !vu.dossiersSurs[espace]
			if !dejaDit && !signale[parent] {
				signale[parent] = true
				e.laisseServeur(parent, "dossier non parcouru entièrement ce cycle : ce qui en manque n'est PAS déclaré supprimé, "+
					"la question sera reposée au prochain cycle")
			}
			continue
		}
		// Dernière vérification, sur le disque et à l'instant présent : elle
		// ferme la course entre le scan et ici, et rattrape tout raisonnement
		// qui aurait un trou.
		if !e.absentCommeFichier(p) {
			continue
		}
		constatees = append(constatees, p)
	}
	sort.Strings(constatees)
	return constatees
}

// pushLocal envoie au serveur les créations/modifications/suppressions locales,
// détectées en comparant un scan du disque à l'état connu (jamais aux events).
// Toutes les écritures partent de `base` (le head du client) ; l'état connu et
// le disque ne sont PAS touchés ici : c'est le pull qui réconcilie.
//
// Rend les empreintes que le serveur a ACCEPTÉES pendant ce cycle. Sans cette
// mémoire, le pull qui suit quelques millisecondes plus tard ne peut pas
// distinguer « le disque porte une édition que personne n'a reçue » de « le
// disque porte ce que je viens d'envoyer » : les deux donnent le même symptôme
// (disque différent du connu ET de l'entrant), et le second partait en copie de
// conflit. C'est ce qui produisait une copie par cycle et par fichier dès que
// deux membres travaillaient en même temps, même quand le merge serveur
// réussissait parfaitement.
func (e *Engine) pushLocal(base string, vu *scan) (map[string]string, error) {
	disk := vu.fichiers
	acceptes := map[string]string{}

	// Suppressions D'ABORD : un chemin qui passe de fichier à dossier (ou
	// l'inverse) doit voir l'ancien supprimé avant que le nouveau soit poussé,
	// sinon git refuse le conflit dossier/fichier et gèlerait la sync.
	//
	// Garde anti-suppression massive. Un dossier d'espace renommé, déplacé ou
	// jeté à la corbeille depuis le Finder ferait lire « tous ces fichiers ont
	// été supprimés » - et la suppression partirait au serveur, donc chez tout le
	// monde. Un geste de rangement local ne peut pas vider un espace partagé.
	//
	// La garde compte les suppressions CONSTATÉES, pas une différence de
	// cardinalités entre deux comptages construits sur des règles différentes.
	// C'était la source de deux défauts symétriques : des ajouts simultanés
	// masquaient des suppressions (restaurer une copie plus ancienne d'un
	// dossier passait la garde), et à l'inverse un motif ajouté au .vecuignore
	// gonflait le compte des « disparus » au point de bloquer toutes les
	// suppressions de l'espace. Une seule liste, une seule règle.
	constatees := e.suppressionsConstatees(vu)
	parEspace := map[string][]string{}
	for _, p := range constatees {
		nom, _, _ := strings.Cut(p, "/")
		parEspace[nom] = append(parEspace[nom], p)
	}
	// Dénominateur construit avec la MÊME règle que le numérateur, sans quoi le
	// défaut ne serait corrigé que d'un bord. Ne comptent que les fichiers connus
	// dont ce cycle sait quelque chose : présents au scan, ou constatés
	// supprimés. Un fichier ignoré, incertain ou logé dans un dossier non
	// parcouru n'entre NI au numérateur NI au dénominateur - le mettre au
	// dénominateur seul rendrait la garde mathématiquement inatteignable dès
	// qu'une moitié de l'espace est ignorée, et un `*.png` mis au .vecuignore des
	// mois plus tôt suffirait à la désarmer.
	//
	// Deux compteurs distincts, et il ne faut pas les confondre : `connus` dit
	// si cet espace a quoi que ce soit à perdre (c'est lui qui décide si on
	// s'exprime), `comptes` est le dénominateur de la proportion.
	connus := map[string]int{}
	comptes := map[string]int{}
	for p := range e.state.Files {
		nom, monte, dansEspace := e.espaceDe(p)
		// Mêmes filtres que `suppressionsConstatees` : un espace non monté n'est
		// pas scanné, et `state.Files` est relu sans validation de clés. Une
		// entrée corrompue ne doit pas faire parler la garde pour un espace qui
		// n'a rien à perdre.
		if !dansEspace || !monte || !cheminSur(p) {
			continue
		}
		connus[nom]++
		if _, present := vu.fichiers[p]; present {
			comptes[nom]++
		}
	}
	for espace, ps := range parEspace {
		comptes[espace] += len(ps)
	}

	var deleted []string
	for _, espace := range e.state.Espaces {
		if connus[espace] == 0 {
			continue
		}
		// La racine de l'espace n'a pas été parcourue entièrement : disparue,
		// remplacée par un lien ou par un fichier, ou illisible. Aucune suppression
		// n'y est constatable - `certifie` refuse tout, son cas de base est
		// exactement cette condition - donc `parEspace[espace]` est vide, par
		// construction et non par chance. Ce qui reste à faire est de l'EXPLIQUER :
		// sans ça le dossier se reconstitue tout seul sans que personne comprenne.
		//
		// COUPLAGE ASSUMÉ : `suppressionsConstatees` tait sa remarque par dossier
		// sur exactement ce prédicat, pour ne pas classer en « fichier perdu » ce
		// qui est une remarque d'espace. Les deux lisent `vu`, pas l'ordre des
		// messages : changer l'un sans l'autre ferait taire les deux.
		//
		// `e.ignore` exclu, et ce n'est pas un détail : mettre le nom d'un espace
		// au .vecuignore pour cesser de le synchroniser ici est un geste
		// volontaire, indiscernable d'un accident au niveau de `dossiersSurs`.
		// Sans cette exception, ce message - qui énonce trois causes toutes
		// fausses - s'imprimerait à chaque cycle, indéfiniment, sans que personne
		// puisse le faire taire. Un choix de l'utilisateur n'est pas une
		// information manquante.
		if !vu.dossiersSurs[espace] && !e.ignore(espace) {
			e.laisseEspace(espace, "le dossier de cet espace n'a pas pu être parcouru (disparu, remplacé par un lien ou un fichier, ou illisible) : "+
				"aucune suppression n'est envoyée et rien n'est modifié sur le serveur. Le contenu redescendra dès que le dossier "+
				"sera de nouveau accessible. Pour s'en séparer vraiment, retirer l'accès depuis l'interface.")
			continue
		}
		// Seuil proportionnel. Un « tout ou rien » laissait passer 100 suppressions
		// sur 101 (il suffisait qu'un fichier subsiste) et interdisait à jamais de
		// supprimer le dernier fichier d'un petit espace.
		if len(parEspace[espace]) > seuilSuppressionMasse && len(parEspace[espace])*2 > comptes[espace] {
			// Demander plutôt que décider seul - mais seulement s'il y a quelqu'un
			// à qui demander. Voir SurSuppressionMassive.
			if e.confirme != nil && e.confirme(espace, len(parEspace[espace]), comptes[espace]) {
				// REVÉRIFIER. La question a fait attendre un humain pendant un temps
				// non borné, et c'est exactement le moment où il agit sur ces
				// fichiers : le geste de rattrapage naturel est de remettre le
				// dossier en place puis de valider, en croyant confirmer le nouvel
				// état. Sans ce second passage, ses fichiers restaurés partaient
				// chez tous les membres - et le pull qui suit effaçait aussi les
				// copies locales. La confirmation rouvrait la course que tout le
				// reste du modèle ferme.
				var tiennent []string
				for _, p := range parEspace[espace] {
					if e.absentCommeFichier(p) {
						tiennent = append(tiennent, p)
					}
				}
				if n := len(parEspace[espace]) - len(tiennent); n > 0 {
					e.laisseEspace(espace, fmt.Sprintf("%d des %d fichiers étaient revenus sur le disque au moment de la confirmation : "+
						"leur suppression n'est PAS envoyée.", n, len(parEspace[espace])))
				}
				e.logf("suppression massive confirmée sur %s : %d fichiers", espace, len(tiennent))
				deleted = append(deleted, tiennent...)
				continue
			}
			// « sur les %d que ce cycle a pu vérifier », et pas « sur %d » : le
			// dénominateur exclut les fichiers ignorés ou non vérifiables, donc il
			// ne correspond pas à ce que l'utilisateur compte dans son Finder.
			// Annoncer un nombre qu'il ne peut pas recouper le ferait douter du
			// message entier, au moment précis où il doit lui faire confiance.
			//
			// Deux situations très différentes derrière le même blocage : personne
			// n'a été consulté (daemon, cron), ou quelqu'un a répondu non. Dire
			// « confirmez dans un terminal » à qui vient justement de refuser dans
			// un terminal est le genre de message qui fait perdre confiance dans
			// tout le reste du rapport.
			suite := "S'ils ont été déplacés, les remettre en place - ils redescendront tout seuls au cycle suivant."
			if e.confirme == nil {
				racine := e.dir
				if abs, err := filepath.Abs(racine); err == nil {
					racine = abs // « --dir . » ne se recopie pas depuis un autre dossier
				}
				suite += " Si la suppression est voulue, la confirmer dans un terminal : arrêter le service puis « vecu sync --dir " + racine + " »."
			}
			e.laisseEspace(espace, fmt.Sprintf("%d fichiers ont disparu du disque en une fois, sur les %d que ce cycle a pu vérifier : "+
				"aucune suppression n'est envoyée, par précaution. %s", len(parEspace[espace]), comptes[espace], suite))
			continue
		}
		deleted = append(deleted, parEspace[espace]...)
	}
	sort.Strings(deleted)
	for _, p := range deleted {
		// Le marque-page de CE chemin, comme l'écriture. Partir du head faisait de
		// la sortie du refus d'écriture une porte grande ouverte : retirer
		// l'obstacle - le geste que le moteur demande lui-même - fait constater une
		// suppression, et cette suppression effaçait la modification de l'autre
		// membre sans conflit ni copie. Le serveur sait maintenant ranger avant de
		// supprimer, donc la bascule fichier -> dossier passe quand même.
		res, err := e.client.Delete(p, e.ancetreDe(p))
		if err != nil {
			return acceptes, err // erreur transport : abandonner et réessayer au cycle suivant
		}
		// Le code HTTP du DELETE n'était jamais examiné, contrairement au PUT
		// juste en dessous. Un refus était donc compté comme un succès : rien dans
		// les logs, rien dans le rapport.
		//
		// Le signaler ne suffit pas : sur un refus, l'entrée RESTE dans l'état,
		// donc la suppression se reconstate et repart au cycle suivant - toutes
		// les 15 secondes, à jamais, et rien d'autre ne peut nettoyer l'entrée.
		// On la retire donc ici, et le rattrapage fait redescendre le fichier :
		// c'est exactement le bon comportement quand on n'a pas le droit de le
		// supprimer.
		switch res.Status {
		case http.StatusOK:
			if res.Conflict {
				// Le message est enfin vrai : la copie existe et porte un nom. Le
				// serveur a rangé la version de l'autre avant d'appliquer la
				// suppression, et cette copie redescend au pull de ce cycle.
				e.logf("suppression de %s : le fichier avait changé entre-temps, copie créée (%s)", p, res.ConflictPath)
			}
		case http.StatusForbidden:
			e.laisseServeur(p, "suppression refusée : cet espace est en lecture seule pour ce compte. Le fichier va redescendre.")
			e.oublie(p)
		case http.StatusNotFound:
			// Le serveur dit qu'il n'y a rien ici : la suppression est sans objet.
			// Seule réponse qui autorise à oublier l'entrée sans rien signaler -
			// et la seule que « ce chemin est sur le serveur » décrirait mal.
			e.logf("suppression de %s sans objet : le serveur ne connaît pas ce chemin", p)
			e.oublie(p)
		default:
			// Même repli que l'écriture, et pour la même raison : un marque-page que
			// le serveur refuse bloquerait ce chemin à chaque cycle. Écrit à la
			// chaîne vide, jamais retiré - une entrée de `Files` sans marque-page
			// est le seul état que `migreVersions` confond avec un état d'avant.
			if v, connu := e.state.Versions[p]; connu && v != "" && res.Status == http.StatusBadRequest {
				e.state.Versions[p] = ""
				e.logf("marque-page abandonné sur %s : le serveur a refusé cette suppression (HTTP 400), le chemin repart sans ancêtre", p)
			}
			e.laisseServeur(p, fmt.Sprintf("suppression : réponse inattendue du serveur (HTTP %d), nouvel essai au prochain cycle", res.Status))
		}
	}

	// Le registre des hors-périmètre sort ses chemins disparus AVANT que la
	// boucle ne le consulte : une entrée qui survit à son fichier est immortelle.
	e.purgeHorsPerimetre(vu)

	// Créations et modifications : chemins présents et différents du connu.
	for _, p := range sortedKeys(disk) {
		if known, ok := e.state.Files[p]; ok && known == disk[p] {
			continue
		}
		// Déjà examiné, déjà écarté, et le contenu n'a pas bougé depuis. Consulté
		// AVANT `os.ReadFile` et pas après : sans ça, les 298 binaires du vault
		// seraient encore lus intégralement à chaque cycle, toutes les 35 secondes,
		// pour être refusés à l'identique. La garde ferme la lecture, le refus et
		// la ligne de journal d'un seul coup.
		if empreinte, ok := e.state.HorsPerimetre[p]; ok && empreinte == disk[p] {
			continue
		}
		content, err := os.ReadFile(e.abs(p))
		if err != nil {
			return acceptes, err
		}
		// v1 texte-only : le contenu transite en string JSON. Un fichier binaire
		// serait corrompu (octets non-UTF-8 → U+FFFD) : on refuse plutôt que de
		// corrompre silencieusement. Le fichier reste intact en local.
		if !utf8.Valid(content) {
			e.laisseHorsPerimetre(p, "fichier binaire, non pris en charge en v2 : il reste sur ce poste")
			// Au registre, JAMAIS dans `Files` : le serveur n'a pas ce contenu, et
			// `Files` ne décrit que ce qu'il a. Cette ligne est la seule fois où ce
			// binaire se dit - la prochaine sera à sa modification.
			e.noteHorsPerimetre(p, disk[p])
			continue
		}
		res, err := e.client.Put(p, string(content), e.ancetreDe(p))
		if err != nil {
			return acceptes, err // erreur transport : réessayer au cycle suivant
		}
		switch res.Status {
		case http.StatusOK:
			// Accepté par le serveur : ce contenu n'est plus « local et non
			// poussé », quoi qu'il advienne ensuite. Retenu MÊME en cas de
			// conflit signalé : le serveur a alors rangé le contenu dans une copie
			// à lui, qui redescend chez tous les membres. En ajouter une seconde
			// ici donnerait deux copies pour une seule divergence.
			acceptes[p] = disk[p]
			// Le chemin vient d'être accepté : il n'est plus hors périmètre. Cas
			// réel, un fichier qui passe de binaire à texte (export réenregistré,
			// `.png` remplacé par un `.md` au même nom). Laisser l'entrée ferait
			// taire le jour où il redevient binaire.
			delete(e.state.HorsPerimetre, p)
			// Le head rendu par le PUT - mais SEULEMENT quand il décrit ce que ce
			// poste a sur le disque, c'est-à-dire ni fusion ni conflit.
			//
			// Le serveur a trois issues et une seule rend un head dont le contenu
			// de `p` est celui qu'on vient d'envoyer. Sur une fusion propre, `p`
			// porte notre édition ET celle de l'autre membre ; sur un conflit, il
			// porte la SIENNE et la nôtre part dans une copie. Dans les deux cas,
			// inscrire ce head décrirait une version que ce poste n'a pas reçue -
			// exactement le mode d'échec que ce marque-page existe pour empêcher.
			// Le pull de ce cycle réécrira la bonne valeur s'il porte le chemin ;
			// s'il ne le porte pas, l'ancien marque-page reste, et un marque-page
			// trop vieux ne fait qu'une copie de conflit.
			if !res.Merged && !res.Conflict {
				e.noteVersion(p, res.Head)
			}
			if res.Conflict {
				// Le serveur a gardé SA version au nom canonique et rangé la nôtre
				// dans une copie. `Files[p]` décrit donc maintenant une version
				// périmée, et la laisser là décale simplement la perte d'un cycle :
				// le scan suivant reverrait notre contenu comme un changement à
				// pousser, avec cette fois `Versions[p]` sur le head courant - donc
				// le chemin rapide du serveur, donc l'écrasement direct de la
				// version de l'autre.
				//
				// On oublie l'entrée : le rattrapage de CE cycle redescend le
				// canonique (`acceptes` l'empêche d'en faire une seconde copie), et
				// le chemin repart aligné.
				e.oublie(p)
				e.logf("conflit sur %s : copie créée (%s)", p, res.ConflictPath)
			}
		case http.StatusForbidden, http.StatusNotFound:
			e.laisse(p, "écriture refusée par le serveur : cet espace est en lecture seule pour ce compte")
		default:
			// Statut inattendu (ex: conflit dossier/fichier transitoire) : on
			// journalise et on continue, sans geler tout le cycle sur ce fichier.
			//
			// Sur 400 SEULEMENT, on lâche le marque-page. Un commit que le dépôt ne
			// résout plus - sauvegarde restaurée, dépôt réinitialisé, serveur changé -
			// est permanent : l'envoi échouerait à chaque cycle, indéfiniment, sous
			// une alerte « ce fichier n'existe que sur ce poste » qui serait fausse.
			// Un 502 de passerelle, lui, est transitoire : abandonner le marque-page
			// dessus échangerait une panne réseau de trois secondes contre un
			// écrasement silencieux au cycle suivant.
			//
			// La chaîne vide ÉCRITE, jamais `delete`. C'est le seul endroit qui
			// retirerait un marque-page en laissant l'entrée `Files` : la map
			// pourrait alors se vider entièrement, `omitempty` l'omettrait du
			// state.json, le rechargement suivant la lirait `nil` - et `migreVersions`
			// ne peut pas distinguer ce nil d'un état écrit par une version
			// antérieure. Elle redistribuerait le head à tous les chemins, c'est-à-dire
			// reconstruirait la perte que ce marque-page existe pour empêcher.
			//
			// Le message ne prétend PAS savoir pourquoi : ce serveur rend 400 pour
			// toute erreur d'écriture, un dépôt qui ne résout plus l'ancêtre comme un
			// disque plein. La formulation reste vraie dans les deux cas, et le repli
			// est sûr dans les deux : sans ancêtre, la fusion ne peut RIEN supprimer
			// (voir `ancetreDe`), et le premier pull qui repasse réinscrit la bonne
			// valeur.
			if v, connu := e.state.Versions[p]; connu && v != "" && res.Status == http.StatusBadRequest {
				e.state.Versions[p] = ""
				e.logf("marque-page abandonné sur %s : le serveur a refusé cet envoi (HTTP 400), le chemin repart sans ancêtre", p)
			}
			e.laisse(p, fmt.Sprintf("réponse inattendue du serveur (HTTP %d), nouvel essai au prochain cycle", res.Status))
		}
	}
	return acceptes, nil
}

// reportable dit si un chemin peut être reporté, ce qui est une question
// distincte de « a-t-il changé ».
//
// Reporter n'a de sens que si le push différé pourra déclarer un ancêtre. Il le
// tire de `Versions[rel]`, et l'état connu qui va avec vient de `Files[rel]`.
// Sans les deux, `ancetreDe` rend la chaîne vide, la fusion serveur part sans
// base commune et range la divergence dans une copie : le report ne ferait que
// retarder cette copie, en promettant dans sa laisse une convergence qu'il ne
// peut pas produire.
//
// Ce que ça écarte, mesuré en revue adversariale : un fichier local tout neuf
// (`Files` n'est écrit que par le pull et le rattrapage, jamais par le push), un
// chemin que `pushLocal` vient d'oublier après un conflit serveur, et - sur un
// système de fichiers insensible à la casse - un chemin dont la clé du scan et
// celle du serveur ne diffèrent que par la casse, qui serait sinon diagnostiqué
// « créé pendant ce cycle » à chaque cycle, indéfiniment.
func (e *Engine) reportable(rel string) bool { return e.refusDeReport(rel) == "" }

// refusDeReport rend le MOTIF pour lequel ce chemin ne peut pas être reporté,
// ou la chaîne vide s'il peut l'être. `reportable` n'en est que la forme
// booléenne : une seule source de vérité, pour que le motif journalisé ne
// puisse jamais diverger de la décision réellement prise.
//
// Le motif existe parce que le journal ne disait pas POURQUOI une copie a été
// faite plutôt qu'un report. Sur les 36 préservations mesurées entre le 27/07
// et le 20/08, aucune ne permet de savoir quelle branche a tiré - donc aucune
// ne permet de décider quoi corriger.
func (e *Engine) refusDeReport(rel string) string {
	if _, suivi := e.state.Files[rel]; !suivi {
		return "chemin non suivi"
	}
	if e.state.Versions[rel] == "" {
		return "sans marque-page"
	}
	return ""
}

// changeDepuisLeScan répond à la seule question de la détection de course :
// « ce chemin a-t-il changé sur le disque depuis le scan de CE cycle ? »
//
// Rend le motif avec le verdict, parce qu'un report se SIGNALE : un renoncement
// muet du moteur est exactement ce que `laisseServeur` existe pour empêcher.
//
// Ne conclut QUE sur un changement constatable. Un chemin dont le scan n'a rien
// pu établir - lien symbolique, fichier illisible, dossier non parcouru - rend
// false. Le diagnostiquer en course le reporterait à chaque cycle sans jamais
// rien pouvoir conclure, alors qu'un report est fait pour être temporaire.
//
// Les trois formes du changement comptent, parce que la question porte sur le
// disque et pas sur l'écriture : contenu différent, fichier apparu, fichier
// disparu.
//
// PURE OBSERVATION, volontairement. Savoir si un chemin change est une chose ;
// savoir s'il est REPORTABLE en est une autre, et cette seconde question vit au
// site d'appel, à côté du `!ch.Deleted` et de la précondition de marque-page.
// Mélanger les deux ici rendrait `false` sur un disque qui a bel et bien changé,
// et le prochain appelant qui lit ce nom se ferait mentir.
//
// Le filtrage `.vecuignore` reste lui aussi à la charge de l'appelant : les deux
// filtres du pull tournent avant, cette fonction ne les refait pas.
func (e *Engine) changeDepuisLeScan(rel string, vu *scan) (bool, string) {
	if vu == nil {
		return false, ""
	}
	if vu.incertains[rel] {
		return false, "" // vu au scan, état non établi : rien à comparer
	}
	// `hashFile` fait un ReadFile, qui SUIT les liens. Sans cette garde, un lien
	// posé au chemin pendant le cycle - donc absent du scan, donc absent de
	// `incertains` - ferait lire et hacher un contenu situé hors de la racine
	// locale. C'est la convention de tout le paquet, et cette fonction était la
	// seule à toucher le disque sans la tenir.
	if err := e.sansLien(rel); err != nil {
		return false, ""
	}
	auScan, connuAuScan := vu.fichiers[rel]
	disque, err := hashFile(e.abs(rel))
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		// Illisible maintenant (droits, volume décroché), ou un fichier occupe un
		// dossier du chemin (`ENOTDIR`, nommé comme dans `absentCommeFichier` et
		// `etatDisque`) : dans les deux cas on ne constate rien.
		return false, ""
	case errors.Is(err, fs.ErrNotExist):
		if connuAuScan {
			return true, "supprimé sur ce poste pendant ce cycle"
		}
		return false, "" // absent avant, absent maintenant : rien n'a bougé
	case !connuAuScan:
		// Présent maintenant, absent du scan. Constatable seulement si le scan
		// certifiait cette absence ; sinon on ignore s'il était déjà là.
		if vu.certifie(path.Dir(rel)) {
			return true, "créé sur ce poste pendant ce cycle"
		}
		return false, ""
	case disque != auScan:
		return true, "modifié sur ce poste pendant ce cycle"
	}
	return false, ""
}

// pull applique le delta serveur depuis `base` au disque local, met à jour
// l'état connu et avance le head.
//
// Rend l'ensemble des chemins REPORTÉS : ceux sur lesquels une écriture locale
// est arrivée pendant ce cycle, qu'on a donc laissés intacts.
//
// `vu` DOIT être le scan pris avant le push de ce cycle, comme pour `rattrape` :
// c'est à cette photo-là que la détection de course compare le disque. Un scan
// frais pris ici dirait « rien n'a changé » pour tout, et désactiverait la
// détection en silence - sans erreur, sans laisse, sans aucun symptôme.
func (e *Engine) pull(base string, vu *scan, acceptes map[string]string) (map[string]bool, error) {
	reportes := map[string]bool{}
	resp, err := e.client.Sync(base)
	if err != nil {
		return reportes, err
	}
	for _, ch := range resp.Changes {
		if !cheminSur(ch.Path) {
			e.laisseServeur(ch.Path, "chemin refusé par le client : il sortirait de la racine locale")
			continue
		}
		_, monte, dansEspace := e.espaceDe(ch.Path)
		if !dansEspace {
			// Le serveur refuse ces chemins à l'écriture ; un dépôt hérité peut
			// encore en contenir. Ils n'appartiennent à aucun espace, donc à aucun
			// dossier local : le dire plutôt que les laisser tomber en silence.
			e.laisseServeur(ch.Path, "chemin hors espace côté serveur : aucun dossier local ne peut l'accueillir")
			continue
		}
		if !monte {
			continue // espace non monté sur ce poste : rien à écrire ici
		}
		if e.ignore(ch.Path) || e.ignoreSurLeChemin(ch.Path) {
			// Le pull n'avait aucun filtre, là où `rattrape` en a un depuis
			// toujours : un chemin écarté par le .vecuignore descendait quand même
			// s'il passait par le delta. Pire, il pouvait faire ranger le fichier
			// local homonyme en copie de conflit - copie dont le nom, lui, n'est
			// pas ignoré, donc poussée au serveur au cycle suivant. Le motif
			// d'ignore produisait alors exactement le contraire de ce qu'il demande.
			//
			// L'entrée sort AUSSI de l'état, pour la raison écrite dans `rattrape` :
			// la laisser avec une empreinte que le disque ne porte pas arme une
			// suppression différée, qui partirait chez tous les membres le jour où
			// le motif serait retiré.
			//
			// Aucune laisse ici : `rattrape` compte à chaque cycle les chemins
			// ignorés de TOUT le périmètre, ceux du delta compris. La poser des deux
			// côtés compterait deux fois le même chemin dans le même cycle.
			e.oublie(ch.Path)
			continue
		}
		abs := e.abs(ch.Path)
		incoming := hashContent([]byte(ch.Content))

		// COURSE, et seulement pour une ÉCRITURE entrante. Une SUPPRESSION venue
		// du serveur passe par la branche `ch.Deleted` plus bas, qui tient déjà sa
		// propre garde et le dit dans ses termes : ressusciter le fichier
		// annulerait la suppression d'un autre membre pour toute l'équipe, sans un
		// mot. Reporter ici passerait au-dessus de cette garde et referait très
		// exactement ce qu'elle interdit - mesuré en revue : la suppression de A
		// défaite, et le contenu de B dupliqué en copie par-dessus le marché.
		//
		// Une écriture locale est protégée dans ce cas-là aussi, par
		// `preserveLocalEditIfAny` : elle part sous un nom de copie, elle n'est
		// jamais détruite.
		//
		// Une suppression LOCALE arrivée pendant le cycle reste, elle, une course :
		// c'est le disque qui a changé, et `changeDepuisLeScan` la constate.
		//
		// Une écriture locale est arrivée entre le scan de ce cycle et
		// maintenant. Ce n'est pas une divergence - le push n'a pas encore eu son
		// tour de l'envoyer. On ne touche à rien : pas d'écriture, pas de copie,
		// `Files` et `Versions` inchangés. Le head, lui, avance quand même en fin
		// de boucle : c'est le marque-page du chemin, resté sur le commit d'avant,
		// que le push du cycle suivant déclarera comme ancêtre, et c'est lui qui
		// fait converger. Épingler le head à la place fabriquerait une copie
		// serveur par cycle (mesuré le 18/08, 10 copies en 12 cycles).
		// `motifRefus` porte la raison pour laquelle ce chemin n'aura PAS été
		// reporté, si on sort de ce bloc sans `continue`. Calculé ici et pas dans
		// `preserveLocalEditIfAny` : c'est ici que la décision se prend, et un
		// motif recalculé ailleurs finirait par décrire autre chose que ce qui
		// s'est passé.
		motifRefus := "suppression entrante"
		if !ch.Deleted {
			motifRefus = e.refusDeReport(ch.Path)
			if motifRefus == "" {
				motifRefus = "aucun changement constaté depuis le scan"
			}
		}
		// `chemin non suivi` est la signature de la collision de casse : le
		// serveur porte deux chemins que ce disque ne sait pas distinguer, donc
		// l'état n'en suit qu'un et l'autre paraît neuf. Le dire ici, où on tient
		// les deux noms, plutôt que de laisser relire trois empreintes.
		//
		// Le voisin n'est cherché que sur CE motif : il coûte une lecture de
		// dossier, et c'est le seul cas où la question se pose.
		if motifRefus == "chemin non suivi" {
			if motif := e.motifCollisionDeCasse(ch.Path); motif != "" {
				// REFUS, et c'est le seul geste sûr. Écrire ici ne créerait pas le
				// fichier demandé : le disque résout ce nom vers le voisin, donc
				// l'écriture ÉCRASE l'autre fichier et lui vole sa place. Le cycle
				// d'après, le voisin paraît modifié et repart en sens inverse.
				//
				// Ne rien écrire fige le disque dans un état correct - le voisin
				// garde son contenu - et rend la collision arbitrable par un humain,
				// qui est le seul à pouvoir décider lequel des deux noms garder.
				//
				// Aucune copie de conflit n'est faite pour ce motif : rien n'est
				// écrasé, donc il n'y a rien à préserver. Et `Files` ne reçoit
				// AUCUNE entrée pour ce chemin : c'est cette entrée-là qui devient
				// le fantôme, une clé qui décrit à jamais un contenu que le chemin
				// ne porte pas.
				e.laisseServeur(ch.Path, motif)
				continue
			}
		}
		if !ch.Deleted && e.reportable(ch.Path) {
			if course, motif := e.changeDepuisLeScan(ch.Path, vu); course {
				// Sur une suppression LOCALE arrivée pendant le cycle, pas de laisse
				// ici : `rattrape` en pose une, exacte, pour ce même chemin et ce
				// même cycle. En poser une seconde compterait deux fois la même
				// cause - la règle est écrite deux fois dans ce fichier, dans la
				// branche d'ignore du pull et dans `preserveLocalEditIfAny` - et
				// celle-ci promettrait en plus une redescente qui n'aura pas lieu,
				// puisque le cycle suivant enverra le DELETE.
				if !strings.HasPrefix(motif, "supprimé") {
					e.laisseServeur(ch.Path, "non appliqué ce cycle : "+motif+
						" (le changement du serveur redescendra une fois cette écriture envoyée)")
				}
				reportes[ch.Path] = true
				// Le registre persisté, en plus de la mémoire de cycle : c'est lui
				// qui fera aller CHERCHER ce contenu au rattrapage, si le push
				// différé ne le ramène pas.
				e.noteReport(ch.Path)
				continue
			}
		}

		// INVARIANT : une opération serveur ne détruit jamais une édition locale
		// non poussée. Une édition arrivée après le scan de push (course) n'a pas
		// été envoyée ; avant d'écraser ou de supprimer, on la met de côté en
		// copie locale (poussée au cycle suivant). Sûr si le disque == connu
		// (pas d'édition) ou == la version entrante (rien à préserver).
		if err := e.preserveLocalEditIfAny(ch.Path, incoming, acceptes, motifRefus); err != nil {
			return reportes, err
		}

		if ch.Deleted {
			if err := e.sansLien(ch.Path); err != nil {
				e.laisseServeur(ch.Path, "non supprimé : "+err.Error()+" (la suppression sortirait de la racine locale)")
				continue
			}
			// Capturé avant le retrait, journalisé après : une ligne « supprimé »
			// posée avant l'appel mentirait à chaque fois que le retrait échoue,
			// et c'est précisément le cas où on relira le journal.
			avant := e.etatDisque(ch.Path)
			retire := true
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				retire = false
				info, errStat := os.Lstat(abs)
				switch {
				case errors.Is(errStat, fs.ErrNotExist) || errors.Is(errStat, syscall.ENOTDIR):
					// Le chemin ne peut plus exister : un composant est devenu un
					// fichier. Il n'y a rien à retirer, et l'entrée doit sortir de
					// l'état - sans quoi elle y reste à jamais et repart en DELETE à
					// chaque cycle, indéfiniment.
				case errStat == nil && info.IsDir():
					// Un DOSSIER a pris la place du fichier supprimé (bascule
					// fichier -> dossier). Le forcer détruirait du contenu local que
					// le serveur ne décrit pas.
					e.laisseServeur(ch.Path, "supprimé sur le serveur, mais un dossier occupe maintenant ce chemin ici : "+
						"le contenu local est conservé")
				default:
					// Tout le reste - dossier parent non inscriptible, fichier
					// verrouillé, volume en lecture seule - veut dire que le fichier
					// EST TOUJOURS SUR LE DISQUE. L'oublier de l'état le ferait
					// repousser comme une création au cycle suivant : la suppression
					// faite par un autre membre serait annulée pour toute l'équipe,
					// sans un mot. Geler ce cycle est visible ; ressusciter est
					// silencieux, et le silence est le vrai danger.
					return reportes, err
				}
			}
			if retire {
				e.logf("supprimé (pull) : %s — avant %s", ch.Path, avant)
			}
			removeEmptyParents(e.dir, abs)
			e.oublie(ch.Path)
			continue
		}
		ecrit, err := e.ecris(ch.Path, ch.Content, "pull")
		if err != nil {
			return reportes, err
		}
		if !ecrit {
			continue // rien sur le disque : ne pas prétendre le contraire
		}
		e.state.Files[ch.Path] = incoming
		// Le head de CETTE réponse, pas celui d'avant : le contenu qu'on vient
		// d'écrire est celui que le serveur y décrit.
		e.noteVersion(ch.Path, resp.Head)
	}
	e.state.Head = resp.Head
	return reportes, nil
}

// ancetreDe rend l'ancêtre commun à déclarer au serveur pour pousser `rel` : son
// marque-page s'il en a un, la chaîne vide sinon.
//
// La chaîne vide n'est pas un sentinel « pas d'ancêtre » : c'est l'ancêtre du
// dépôt vide, donc un commit orphelin et une fusion sans base commune. C'est le
// repli SÛR pour un chemin que ce poste n'a jamais reçu - obstruction posée avant
// le premier passage, espace fraîchement monté, marque-page abandonné parce que
// le serveur a refusé l'envoi.
//
// CE QUI REND CE REPLI SÛR, et il faut l'énoncer correctement sous peine de le
// voir « corrigé » : une fusion depuis une base VIDE ne peut supprimer aucune
// ligne. Une ligne présente d'un côté et absente de l'autre y est un ajout d'un
// seul côté, jamais un retrait. Elle ne peut donc rien perdre - au pire elle
// range la divergence dans une copie de conflit. Ce n'est PAS « elle produit
// toujours une copie » : deux contenus identiques fusionnent proprement, et un
// dépôt vide prend même le chemin rapide du serveur. Ces cas-là sont corrects.
//
// Le head, lui, produirait une fusion propre et un écrasement muet.
//
// ASYMÉTRIE À CONNAÎTRE, côté SUPPRESSION. Une écriture sans ancêtre peut être
// comparée : deux contenus identiques fusionnent proprement, sans copie. Une
// suppression, non - il n'y a rien à comparer, et le serveur ne sait pas
// distinguer « ce membre a supprimé ce fichier » de « ce membre ne l'a jamais
// eu ». Il range donc systématiquement la version de main dans une copie avant
// de supprimer. Conséquence : sur un chemin sans marque-page, une suppression
// fait revenir le fichier chez tout le monde sous un nom de conflit, même si
// personne n'était en désaccord. C'est du bruit, et c'est le prix assumé du
// repli sûr - la seule alternative connue serait que le client dise au serveur
// quelle empreinte il détenait, ce qui est un ajout de protocole.
//
// Appelé sous `e.mu` (le verrou de cycle), comme tout ce qui lit `e.state`.
func (e *Engine) ancetreDe(rel string) string {
	return e.state.Versions[rel]
}

// oublie retire un chemin de l'état : son empreinte connue ET son marque-page.
//
// Les deux vont toujours ensemble. Un marque-page qui survit à la sortie de son
// chemin - espace démonté, droit retiré, motif ajouté au .vecuignore - devient
// immortel : plus personne ne le réconcilie, et le jour où le chemin revient il
// fait fusionner depuis un commit arbitrairement vieux, donc fabrique une copie
// de conflit là où il n'y a aucun désaccord.
func (e *Engine) oublie(rel string) {
	delete(e.state.Files, rel)
	delete(e.state.Versions, rel)
	// Le registre des reportés sort avec eux, et pour la même raison : une
	// entrée qui survit à la sortie de son chemin devient immortelle. Elle ferait
	// repêcher à chaque cycle un chemin que plus personne ne suit.
	e.oublieReport(rel)
}

// noteReport inscrit un chemin dans le registre des reportés. Idempotent : un
// chemin reporté plusieurs cycles de suite n'y figure qu'une fois.
func (e *Engine) noteReport(rel string) {
	for _, p := range e.state.Reportes {
		if p == rel {
			return
		}
	}
	e.state.Reportes = append(e.state.Reportes, rel)
}

// oublieReport retire un chemin du registre des reportés : son contenu est
// arrivé, ou le chemin a quitté l'état.
func (e *Engine) oublieReport(rel string) {
	for i, p := range e.state.Reportes {
		if p == rel {
			e.state.Reportes = append(e.state.Reportes[:i], e.state.Reportes[i+1:]...)
			return
		}
	}
}

// noteHorsPerimetre inscrit au registre un chemin que ce cycle vient d'écarter,
// avec l'empreinte EXACTE qui a été écartée. C'est l'empreinte qui porte le
// sens : un binaire modifié doit se redire une fois.
func (e *Engine) noteHorsPerimetre(rel, empreinte string) {
	if e.state.HorsPerimetre == nil {
		e.state.HorsPerimetre = map[string]string{}
	}
	e.state.HorsPerimetre[rel] = empreinte
}

// purgeHorsPerimetre retire du registre les chemins qui ont quitté le disque.
//
// Sans elle, une entrée survit à son fichier et devient immortelle : le registre
// enfle sans fin, et un binaire supprimé puis recréé à l'identique ne dirait
// plus jamais qu'il est hors périmètre.
//
// Même prudence que `suppressionsConstatees`, et pour la même raison : on ne
// retire que sur une absence CONSTATÉE. Un dossier illisible, non parcouru, ou
// dont un lien occupe le chemin ne prouve rien - purger dessus ferait
// re-journaliser les 298 au cycle suivant, c'est-à-dire rouvrir le déluge qu'on
// vient de fermer, et de la façon la plus difficile à diagnostiquer : par
// intermittence.
func (e *Engine) purgeHorsPerimetre(vu *scan) {
	for p := range e.state.HorsPerimetre {
		if _, present := vu.fichiers[p]; present {
			continue // toujours là : rien à conclure
		}
		if e.ignore(p) || e.ignoreSurLeChemin(p) {
			// Absent du scan parce qu'IGNORÉ, pas parce que disparu. Le sortir du
			// registre est le bon geste : un chemin mis au `.vecuignore` n'est plus
			// hors périmètre, il est hors de Vécu. L'y laisser le ferait compter
			// indéfiniment dans « N fichiers hors périmètre », sans que personne
			// puisse faire taire le compte - or un choix de l'utilisateur n'est pas
			// une information manquante.
			delete(e.state.HorsPerimetre, p)
			continue
		}
		if _, monte, _ := e.espaceDe(p); !monte {
			continue // espace non monté : jamais scanné, donc jamais constatable
		}
		if !cheminSur(p) {
			continue // clé corrompue : même garde que tous les lecteurs de l'état
		}
		if vu.incertains[p] || !vu.certifie(path.Dir(p)) {
			continue // vu sans conclusion, ou dossier non parcouru entièrement
		}
		// Dernière vérification sur le disque à l'instant présent : elle ferme la
		// course entre le scan et ici, comme pour les suppressions.
		if !e.absentCommeFichier(p) {
			continue
		}
		delete(e.state.HorsPerimetre, p)
	}
}

// noteVersion inscrit le commit auquel le contenu qui vient d'arriver sur le
// disque était courant.
//
// `Files` n'est écrit qu'à DEUX endroits - le pull et le rattrapage - et les
// deux appellent cette fonction juste après : c'est ce qui tient l'invariant
// « toute entrée de Files a son marque-page », et rien d'autre. Les deux autres
// appelants (l'acceptation d'un envoi, la migration) n'écrivent pas `Files` :
// ils ne peuvent que poser une valeur EN TROP, donc fausse si on n'y prend pas
// garde. Chacun porte sa propre condition, écrite sur place.
//
// Jamais appelée « parce que c'est probablement ça » : l'information est écrite
// au moment où le contenu arrive, ou elle n'existe pas.
func (e *Engine) noteVersion(rel, commit string) {
	if e.state.Versions == nil {
		e.state.Versions = map[string]string{}
	}
	e.state.Versions[rel] = commit
}

// migreVersions remplit le marque-page d'un état écrit par une version
// antérieure, qui n'en avait aucun.
//
// Sur un chemin SAIN, la migration est vraie par construction : ce que ce poste
// a sur le disque est la version de son head, et c'est ce que `Files` affirme.
//
// SAUF sur les chemins pour lesquels cette affirmation est justement fausse - et
// ce sont eux, la base installée qu'on répare. Un poste dont le head a déjà
// franchi une écriture refusée porte dans `Files` une version ANTÉRIEURE à son
// head : lui recopier le head lui ferait déclarer un ancêtre qu'il ne contient
// pas, c'est-à-dire reconstruire par la migration exactement la perte que ce
// marque-page existe pour empêcher.
//
// L'état sait déjà lesquels : `Laisses` est le rapport du dernier cycle, et
// toutes les branches de refus d'écriture y déposent leur chemin. Ces chemins-là
// reçoivent la chaîne vide - pas d'omission, une valeur : côté serveur elle
// produit une fusion sans base commune, donc une copie de conflit. Du bruit sur
// une poignée de chemins obstrués, jamais une perte, et l'invariant
// « toute entrée de Files a son marque-page » tient quand même.
//
// Gardée sur « la map est absente », et pas sur « telle entrée manque ». Une
// lacune apparue APRÈS la bascule vient d'un chemin dont on ne sait plus quoi
// dire : la remplir avec le head reproduirait le même mode d'échec. Sans entrée,
// l'envoi tombe sur la chaîne vide, donc sur une copie de conflit.
func (e *Engine) migreVersions() {
	if e.state.Versions != nil || len(e.state.Files) == 0 {
		return
	}
	// `state.Laisses`, pas `e.laisses` : le cycle courant vient de remettre ce
	// dernier à zéro, et ce qu'on cherche est le rapport du cycle PRÉCÉDENT.
	suspects := make(map[string]bool, len(e.state.Laisses))
	for _, l := range e.state.Laisses {
		suspects[l.Chemin] = true
	}
	e.state.Versions = make(map[string]string, len(e.state.Files))
	obstrues := 0
	for p := range e.state.Files {
		if !cheminSur(p) {
			continue // clé corrompue : jamais suivie ailleurs, pas migrée non plus
		}
		if suspects[p] {
			e.state.Versions[p] = ""
			obstrues++
			continue
		}
		e.state.Versions[p] = e.state.Head
	}
	e.logf("marque-page initialisé sur %d chemins à partir du head %s (%d laissés sans ancêtre : le dernier cycle les signalait)",
		len(e.state.Versions)-obstrues, court(e.state.Head), obstrues)
}

// preserveLocalEditIfAny sauvegarde le fichier local `rel` en copie de conflit
// locale si son contenu disque diffère à la fois de l'état connu (donc édité
// localement) ET de la version serveur entrante `incoming` (donc pas déjà
// aligné). Sans quoi une opération de pull écraserait une édition jamais poussée.
//
// `acceptes` porte ce que le push du MÊME cycle a réussi à envoyer. Sans lui, la
// fonction ne pouvait pas distinguer une édition que personne n'a reçue d'une
// édition acceptée par le serveur trois lignes plus haut : les deux donnent le
// même symptôme. La seconde partait en copie de conflit, ce qui produisait une
// copie par cycle et par fichier dès que deux membres travaillaient en même
// temps - même quand le merge à trois voies du serveur avait parfaitement
// fusionné les deux versions, auquel cas la copie contenait une version que le
// fichier canonique portait déjà.
func (e *Engine) preserveLocalEditIfAny(rel, incoming string, acceptes map[string]string, motifRefus string) error {
	if err := e.sansLien(rel); err != nil {
		// Sans cette garde, un espace déplacé et remplacé par un lien - geste
		// banal pour garder un vault en place - faisait RENOMMER des fichiers
		// personnels situés hors de la racine locale, sous un nom « (conflit
		// local) ». Le moteur ne touche rien qu'il ne contrôle.
		//
		// Silencieux à dessein : rien n'a été détruit ici, et l'appelant (`ecris`,
		// ou la branche de suppression du pull) signale déjà le refus pour ce même
		// chemin et cette même raison. Deux lignes pour une cause apprennent à ne
		// plus lire le rapport.
		return nil
	}
	abs := e.abs(rel)
	disk, err := hashFile(abs)
	if err != nil {
		return nil // absent : rien à préserver
	}
	known := e.state.Files[rel]
	if disk == known || disk == incoming || disk == acceptes[rel] {
		return nil
	}
	// Édition locale non poussée : la déplacer vers un nom libre plutôt que la perdre.
	dst := e.freeLocalName(localConflictName(rel))
	avant := e.etatDisque(rel)
	if err := os.Rename(abs, e.abs(dst)); err != nil {
		return err
	}
	// Les trois empreintes, parce que c'est le seul endroit du moteur où le nom
	// canonique change de contenu sans que personne ne l'ait demandé. Sans elles,
	// la ligne dit qu'un arbitrage a eu lieu mais pas lequel : impossible de
	// répondre après coup à « laquelle des deux versions ai-je sous les yeux ».
	// Deux questions, deux moitiés de ligne. Les trois empreintes répondent à
	// « laquelle des deux versions ai-je sous les yeux » ; le motif répond à
	// « pourquoi une copie plutôt qu'un report », qui est la seule question dont
	// dépend la correction du taux de copies.
	e.logf("édition locale non poussée préservée : %s -> %s — local %s, connu %s, entrant %s ; report refusé : %s",
		rel, dst, avant, court(known), court(incoming), motifRefus)
	return nil
}

// marqueCopieLocale : le fragment que `localConflictName` insère dans le nom, et
// que `ignored` reconnaît. Une seule constante pour les deux sens : produire un
// nom que la reconnaissance ne retrouve pas remettrait en circulation
// exactement ce que la slice ferme, et un test lie les deux.
const marqueCopieLocale = " (conflit local)"

// localConflictName forge « nom (conflit local).ext » (l'appareil est déjà
// identifié par son dossier ; le compteur de freeLocalName assure l'unicité).
func localConflictName(rel string) string {
	ext := path.Ext(rel)
	return strings.TrimSuffix(rel, ext) + marqueCopieLocale + ext
}

// freeLocalName renvoie `name` s'il est libre sur disque, sinon « … (n).ext ».
func (e *Engine) freeLocalName(name string) string {
	if _, err := os.Stat(e.abs(name)); os.IsNotExist(err) {
		return name
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 2; n < 10000; n++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if _, err := os.Stat(e.abs(cand)); os.IsNotExist(err) {
			return cand
		}
	}
	return name // fallback très improbable
}

// reconcilePerimeter retire les fichiers locaux sortis du périmètre (droit
// révoqué) : ils ne figurent plus dans /tree mais restent dans l'état connu.
// On ne supprime qu'un fichier propre (disque == connu), jamais une édition
// locale en attente.
func (e *Engine) reconcilePerimeter(paths []string, reportes map[string]bool) error {
	inPerimeter := make(map[string]bool, len(paths))
	for _, p := range paths {
		if _, monte, _ := e.espaceDe(p); monte {
			inPerimeter[p] = true
		}
	}
	// Les marque-pages se réconcilient sur l'UNION `Files` ∪ `Versions`, et pas
	// sur `Files` seul : une entrée dont le chemin a quitté l'état (motif
	// .vecuignore, suppression constatée) resterait sinon invisible pour la
	// boucle qui suit, donc immortelle.
	for p := range e.state.Versions {
		if inPerimeter[p] {
			continue
		}
		if _, monte, _ := e.espaceDe(p); !monte {
			continue // espace non monté : ce n'est pas une sortie de périmètre
		}
		if _, suivi := e.state.Files[p]; !suivi {
			delete(e.state.Versions, p)
			e.oublieReport(p)
		}
	}
	for p, known := range e.state.Files {
		if inPerimeter[p] {
			continue
		}
		if reportes[p] {
			// Un chemin reporté porte une écriture locale que le push n'a pas
			// encore envoyée. Le retirer du disque au motif qu'il est hors
			// périmètre détruirait la seule copie de ce contenu.
			continue
		}
		// Le filtre de PÉRIMÈTRE d'abord, les gardes de sûreté ensuite. Dans
		// l'autre ordre, une entrée héritée faisait un Lstat sur chaque composant
		// de son chemin - donc sondait les dossiers personnels de la racine, que
		// cette fonction ne touche jamais - et pouvait produire une remarque à
		// chaque cycle sur un chemin totalement hors sujet.
		if _, monte, _ := e.espaceDe(p); !monte {
			// Entrée héritée (dépôt v1, chemins hors espace) ou espace non monté :
			// ce n'est pas une sortie de périmètre. La supprimer du disque
			// effacerait un contenu que plus aucun client ne peut redescendre.
			continue
		}
		if !cheminSur(p) {
			// Clé d'état corrompue : jamais une cible d'os.Remove, et purgée comme
			// le fait `demonte` - sinon elle reste immortelle dans l'état.
			e.oublie(p)
			continue
		}
		if err := e.sansLien(p); err != nil {
			// Une sortie de périmètre ne supprime rien à travers un lien : ça
			// détruirait un fichier situé hors de la racine locale.
			e.laisseServeur(p, "non retiré du disque : "+err.Error())
			continue
		}
		abs := e.abs(p)
		if h, err := hashFile(abs); err == nil && h == known {
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				return err
			}
			removeEmptyParents(e.dir, abs)
			e.oublie(p)
			e.logf("retiré %s (hors périmètre)", p)
		}
	}
	return nil
}

// scan : ce qu'un cycle a vu du disque, et avec quelle CERTITUDE.
//
// La liste des fichiers ne suffit pas à décider d'une suppression. Une absence
// veut dire soit « ce fichier n'existe pas », soit « je n'ai pas pu regarder »,
// et confondre les deux est la cause commune de toutes les suppressions
// abusives du client (voir `spec-suppression-constatee.md`). Les trois champs
// qui suivent `fichiers` portent cette distinction.
type scan struct {
	// fichiers : chemin (slash) -> sha256 du contenu. Vu, lu, hashé.
	fichiers map[string]string
	// dossiersSurs : dossiers parcourus ENTIÈREMENT et sans erreur. Eux seuls
	// autorisent à conclure qu'un chemin absent de `fichiers` n'existe pas.
	dossiersSurs map[string]bool
	// dossiersVus : dossiers dans lesquels la marche est entrée, quel qu'ait été
	// leur sort. « Vu mais pas sûr » (illisible, ignoré) veut dire qu'on ne sait
	// rien de son contenu - à distinguer de « jamais vu », qui veut dire qu'il
	// n'existe pas, et qui est une information exploitable.
	dossiersVus map[string]bool
	// incertains : entrées rencontrées dont l'état n'a PAS pu être établi (lien
	// symbolique, fichier illisible). Elles sont sur le disque et absentes de
	// `fichiers` : sans ce champ, elles passeraient pour supprimées.
	incertains map[string]bool
}

// certifie répond à une seule question : « ce cycle permet-il de conclure
// qu'un chemin absent de ce dossier n'existe pas ? »
//
// La règle de base est « le dossier a été parcouru entièrement et sans
// erreur ». Prise au pied de la lettre elle INTERDIRAIT toute suppression de
// dossier : supprimer `shared/vieux/` fait que `shared/vieux` n'est jamais
// parcouru, donc aucun de ses fichiers ne serait jamais constatable, et le
// dossier reviendrait à chaque cycle. D'où la remontée : un dossier absent
// d'un parent lui-même certifié est une CONSTATATION, pas une ignorance.
//
// La remontée s'arrête sur tout ce qui a été vu sans être certifié - dossier
// illisible, dossier ignoré, et surtout lien symbolique. Ce dernier cas est le
// piège : `os.Lstat` ne suit pas le DERNIER composant mais résout bien les
// composants intermédiaires. Sans cet arrêt, un `equipe/photos` devenu lien
// laisserait conclure à l'absence de `equipe/photos/a.md` en allant regarder
// hors de la racine locale.
func (s *scan) certifie(d string) bool {
	for {
		if s.dossiersSurs[d] {
			return true
		}
		if s.dossiersVus[d] || s.incertains[d] {
			return false // vu, mais rien conclu sur son contenu
		}
		parent := path.Dir(d)
		if parent == "." || parent == d {
			// Racine d'espace jamais parcourue : dossier disparu, remplacé par un
			// lien, ou espace non monté. Rien n'y est certifiable.
			return false
		}
		d = parent
	}
}

// scanLocal parcourt les espaces montés et rend ce qu'il a vu, avec la
// certitude qui va avec.
//
// Ne jamais parcourir la racine entière est une exigence, pas une optimisation :
// la racine est le dossier de travail de l'utilisateur (elle peut contenir un
// `node_modules` de plusieurs milliers de fichiers, des dépôts de code, des
// dossiers personnels). Rien de tout cela n'appartient à Vécu.
func (e *Engine) scanLocal() (*scan, error) {
	s := &scan{
		fichiers:     map[string]string{},
		dossiersSurs: map[string]bool{},
		dossiersVus:  map[string]bool{},
		incertains:   map[string]bool{},
	}
	// Remplacé et non accumulé : une copie arbitrée puis supprimée doit sortir
	// de la liste au cycle suivant, sans quoi le compte ne redescend jamais et
	// cesse d'être lu.
	var copies []string
	for _, espace := range e.state.Espaces {
		racine := e.racineEspace(espace)
		err := filepath.WalkDir(racine, func(abs string, d fs.DirEntry, err error) error {
			if err != nil && os.IsNotExist(err) {
				return nil // espace pas encore créé sur le disque : le pull s'en charge
			}
			rel, e2 := e.rel(abs)
			if e2 != nil {
				return e2
			}

			if err != nil {
				// WalkDir appelle la fonction avec une erreur dans deux cas : échec
				// du Lstat de la racine, ou échec du ReadDir d'un dossier - et dans
				// ce second cas elle est appelée DEUX FOIS sur le même chemin, la
				// seconde portant l'erreur.
				// Source: https://pkg.go.dev/io/fs#WalkDirFunc
				//
				// Ce second appel est le seul signal qui existe. Vérifié dans la
				// source de Go 1.26.5 (path/filepath/path.go, walkDir) : quand
				// ReadDir échoue, la liste rendue est PARTIELLE et la marche
				// continue dessus. Un dossier lu à moitié a donc exactement
				// l'aspect d'un dossier lu en entier - c'est la mécanique par
				// laquelle un `chmod 000` supprimait chez tout le monde.
				delete(s.dossiersSurs, rel)
				s.dossiersVus[rel] = true
				// Un dossier illisible (droits, volume réseau décroché) ne doit pas
				// geler la synchronisation de tous les espaces du poste.
				e.laisse(rel, "dossier illisible sur ce poste ("+err.Error()+")")
				return fs.SkipDir
			}

			if d.IsDir() {
				s.dossiersVus[rel] = true
				if e.ignore(rel) {
					return fs.SkipDir // vu, mais pas parcouru : jamais « sûr »
				}
				// Optimiste : retiré par le second appel si le ReadDir échoue.
				s.dossiersSurs[rel] = true
				return nil
			}
			// Un lien symbolique pointe hors du contrôle du moteur : le lire
			// enverrait le contenu d'un fichier situé n'importe où, et un pull
			// écrirait À TRAVERS le lien, donc hors de l'espace.
			//
			// AVANT le test d'ignore, et c'est essentiel : `certifie` s'arrête sur
			// `incertains`, mais encore faut-il que le scan y ait inscrit le lien.
			// Un dossier-lien couvert par un motif sortait ici sans être enregistré
			// nulle part, donc invisible pour `certifie` qui le traversait - et le
			// Lstat final allait chercher hors de la racine locale de quoi conclure
			// à une suppression. Un motif d'ignore ne doit jamais faire OUBLIER un
			// lien, seulement renoncer à le synchroniser.
			if d.Type()&fs.ModeSymlink != 0 {
				s.incertains[rel] = true
				if !e.ignore(rel) {
					e.laisse(rel, "lien symbolique : non synchronisé (la cible peut être hors de la racine)")
				}
				return nil
			}
			if e.ignore(rel) {
				// Une copie de conflit locale attend un arbitrage humain. Elle est
				// ignorée depuis la slice 2 - donc invisible de tout le reste - et
				// personne ne saurait qu'elle existe sans la compter ici. Comptée
				// dans CE parcours, jamais dans un second : le scan visite déjà
				// chaque fichier de chaque espace à chaque cycle.
				if strings.Contains(path.Base(rel), marqueCopieLocale) {
					copies = append(copies, rel)
				}
				return nil
			}
			h, err := hashFile(abs)
			if err != nil {
				// Un fichier illisible ne gèle pas la synchronisation des autres.
				s.incertains[rel] = true
				e.laisse(rel, "illisible sur ce poste ("+err.Error()+")")
				return nil
			}
			s.fichiers[rel] = h
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(copies)
	e.copies = copies
	return s, nil
}

// CopiesDeConflitLocales parcourt les espaces et rend les copies locales en
// attente d'arbitrage. Exporté pour `vecu status`, qui lit l'état sans
// construire de moteur - donc sans scan.
//
// Un parcours à part, et c'est assumé : `status` ne cycle pas, il n'y a pas de
// scan dont dériver le compte. Le moteur, lui, dérive le sien du scan qu'il
// fait déjà (voir `scanLocal`). Une erreur de parcours est ignorée : un compte
// incomplet vaut mieux qu'une commande de diagnostic qui refuse de répondre.
func CopiesDeConflitLocales(dir string, montages map[string]string, espaces []string) []string {
	var out []string
	for _, espace := range espaces {
		if !espaceValide(espace) {
			continue
		}
		racine := RacineEspace(dir, montages, espace)
		filepath.WalkDir(racine, func(abs string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !strings.Contains(d.Name(), marqueCopieLocale) {
				return nil
			}
			if rel, err := filepath.Rel(dir, abs); err == nil {
				out = append(out, filepath.ToSlash(rel))
			} else {
				out = append(out, abs)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// CopiesEnAttente : les copies de conflit locales présentes sur le disque, en
// attente d'arbitrage humain.
//
// Dérivé du DISQUE au dernier scan, jamais d'un registre : aucun champ ajouté à
// `State`, donc aucune migration et rien qui puisse mentir après un ménage fait
// à la main. Vécu n'en supprime jamais aucune - une copie porte du travail
// humain, mesuré le 20/08 sur un cas réel où une édition n'existait QUE dans
// une copie.
func (e *Engine) CopiesEnAttente() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.copies...)
}

// chargeMotifsEspaces relit le `.vecuignore` de chaque espace monté.
//
// Refait à neuf à chaque cycle, comme les laisses : une règle retirée doit
// cesser de s'appliquer, et une carte qu'on complète ne perd jamais rien.
//
// Un fichier illisible ne fait pas conclure « aucune règle » : ce serait
// envoyer au serveur ce que la personne a écrit vouloir garder pour elle. On
// garde alors les motifs du cycle précédent et on le signale.
func (e *Engine) chargeMotifsEspaces() {
	neuf := make(map[string][]string, len(e.state.Espaces))
	for _, nom := range e.state.Espaces {
		motifs, err := LoadIgnores(e.racineEspace(nom))
		if err != nil {
			e.laisseEspace(nom, "le "+ignoreFichier+" de cet espace n'a pas pu être lu ("+err.Error()+") : les règles du cycle précédent restent appliquées")
			if anciens, ok := e.motifsEspace[nom]; ok {
				neuf[nom] = anciens
			}
			continue
		}
		if len(motifs) > 0 {
			neuf[nom] = motifs
		}
	}
	e.motifsEspace = neuf
}

// ignore : règles par défaut, plus les motifs du .vecuignore de cette racine.
func (e *Engine) ignore(rel string) bool {
	if ignored(rel) {
		return true
	}
	for _, motif := range e.motifs {
		if correspond(motif, rel) {
			return true
		}
	}
	// Puis les règles propres à l'espace, appliquées au chemin VU DEPUIS SA
	// RACINE : un `.vecuignore` posé dans un dossier partagé parle de ce
	// dossier, pas du dépôt. Il voyage avec lui, donc ses règles valent pour
	// tout le monde - c'est voulu, ce sont les règles du dossier et pas celles
	// d'un poste.
	espace, reste, ok := strings.Cut(rel, "/")
	if !ok {
		return false
	}
	for _, motif := range e.motifsEspace[espace] {
		if correspond(motif, reste) {
			return true
		}
	}
	return false
}

// ignoreSurLeChemin : un ANCÊTRE de ce chemin est-il ignoré ?
//
// Un motif qui désigne un dossier par son chemin (« shared/brouillons ») ne
// correspond pas aux fichiers qu'il contient : `path.Match` ne traverse pas les
// « / ». Le dossier est donc écarté du scan sans que ses fichiers soient jugés
// ignorés - ils deviennent « absents sans information ». C'est sûr (rien n'est
// supprimé), mais ça produirait une remarque à chaque cycle, indéfiniment, sur
// un dossier délibérément mis de côté. Un choix de l'utilisateur n'est pas une
// information manquante.
func (e *Engine) ignoreSurLeChemin(rel string) bool {
	for d := path.Dir(rel); d != "." && d != "/"; d = path.Dir(d) {
		if e.ignore(d) {
			return true
		}
	}
	return false
}

// correspond : un motif s'applique au chemin complet OU à l'un de ses segments.
// « node_modules » attrape donc le dossier à n'importe quelle profondeur,
// « *.png » tous les PNG, « shared/brouillons » un chemin précis. Un motif
// malformé ne fait jamais échouer un cycle : il ne correspond simplement à rien.
// Source: https://pkg.go.dev/path#Match
func correspond(motif, rel string) bool {
	if ok, err := path.Match(motif, rel); err == nil && ok {
		return true
	}
	for _, seg := range strings.Split(rel, "/") {
		if ok, err := path.Match(motif, seg); err == nil && ok {
			return true
		}
	}
	return false
}

// ignored applique la liste d'ignore par défaut à un chemin relatif à la racine.
//
// Les noms techniques sont reconnus à N'IMPORTE QUELLE profondeur : depuis la
// v2 les chemins scannés commencent par le nom de l'espace, un test de préfixe
// sur la racine ne verrait plus le `.git/` d'un dossier synchronisé.
func ignored(rel string) bool {
	segments := strings.Split(rel, "/")
	for i, seg := range segments {
		switch seg {
		case ".vecu", ".git", ".DS_Store":
			return true
		}
		// Le workspace Obsidian est propre à chaque machine (panneaux ouverts) :
		// le synchroniser ferait sauter la mise en page de l'autre à chaque
		// changement d'onglet. Le reste de `.obsidian/` reste partagé.
		if seg == ".obsidian" && i+1 < len(segments) && strings.HasPrefix(segments[i+1], "workspace") {
			return true
		}
	}
	// UNE COPIE LOCALE NE VOYAGE PAS. Elle décrit l'état non poussé d'UN poste :
	// l'envoyer aux autres ne leur apprend rien et remplit leur dossier. Surtout,
	// posée dans le dossier synchronisé elle devient un fichier partagé exposé à
	// la même course, donc capable de produire ses propres copies - mesuré le
	// 20/08 chez Achille, quatre fichiers pour une idée, dont deux pris SUR une
	// copie. Elle reste sur le disque, à côté du canonique, là où on l'arbitre.
	//
	// Les copies du SERVEUR (« nom (conflit AAAA-MM-JJ HHhMM - compte).ext »)
	// ne sont pas concernées : le modèle Dropbox veut qu'elles arrivent chez
	// tout le monde, c'est délibéré et ça ne bouge pas.
	//
	// Sur le dernier segment seulement : la marque est produite dans un nom de
	// FICHIER, et un dossier qui la porterait par hasard n'a pas à disparaître
	// du périmètre de qui que ce soit.
	return strings.Contains(segments[len(segments)-1], marqueCopieLocale)
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashFile(abs string) (string, error) {
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return hashContent(b), nil
}

// etatDisque décrit un chemin tel qu'il est à cet instant, pour le journal :
// empreinte courte et taille, ou la raison pour laquelle il n'y a pas de
// contenu à décrire.
//
// Pourquoi ça existe : les trois incidents de perte des 09-12/08 ont tous dû
// être reconstitués après coup depuis l'historique git et la taille des
// fichiers, parce que le moteur ne disait jamais QUELLE version il venait de
// remplacer. Un outil de synchronisation qui n'écrit pas cette ligne rend toute
// enquête dépendante d'un dépôt git tiers - que ses utilisateurs n'ont pas.
//
// Lstat plutôt que Stat, comme partout ailleurs ici : ne pas suivre un lien.
// Source: https://pkg.go.dev/os#Lstat
func (e *Engine) etatDisque(rel string) string {
	abs := e.abs(rel)
	info, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return "absent"
	case err != nil:
		return "illisible (" + err.Error() + ")"
	case info.IsDir():
		return "dossier"
	case info.Mode()&os.ModeSymlink != 0:
		return "lien"
	}
	h, err := hashFile(abs)
	if err != nil {
		return "illisible (" + err.Error() + ")"
	}
	return empreinteTaille(h, info.Size())
}

// empreinteTaille : « 3a1f9c2e4b7d 128456o ». Les 12 premiers caractères
// suffisent à distinguer deux versions d'un même fichier à l'œil, et la taille
// dit dans quel sens ça a bougé - c'est elle qui a fait tiquer sur les copies d'un
// dossier client (7844 contre 6528 octets), pas l'empreinte.
func empreinteTaille(h string, taille int64) string {
	return fmt.Sprintf("%s %do", court(h), taille)
}

// court abrège une empreinte pour le journal. Douze caractères suffisent à
// distinguer deux versions d'un même fichier à l'œil ; l'empreinte entière rend
// la ligne illisible, et une ligne qu'on ne lit pas ne sert à rien.
func court(h string) string {
	if h == "" {
		return "(aucune)"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// removeEmptyParents supprime les dossiers parents vides après une suppression,
// en s'arrêtant à la racine du dossier synchronisé.
func removeEmptyParents(root, abs string) {
	dir := filepath.Dir(abs)
	for {
		if dir == root || !strings.HasPrefix(dir, root) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
