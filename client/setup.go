package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/colindargent/vecu/client/sync"
)

// cmdSetup : installation d'un poste en une commande.
//
// C'est le seul moment où quelqu'un ouvre un terminal. Tout ce qui suit - quels
// espaces descendent, qui y a accès - se décide dans l'interface web et arrive
// ici tout seul.
func cmdSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	server := fs.String("server", "", "URL du serveur Vécu")
	user := fs.String("user", "", "nom d'utilisateur")
	password := fs.String("password", "", "mot de passe (sinon demandé)")
	dir := fs.String("dir", "", "racine locale (contiendra un dossier par espace)")
	device := fs.String("device", hostname(), "libellé de cet appareil")
	sansService := fs.Bool("sans-service", false, "ne pas installer le démarrage automatique")
	importer := fs.String("importer", "", "espaces dont le dossier local existant doit être ADOPTÉ et envoyé (séparés par une virgule)")
	agents := fs.String("agents", "", "agents d'IA sur ce poste vers lesquels projeter les skills (ex: claude ; vide = question interactive)")
	fs.Parse(args)

	// Le lecteur partagé du processus, pas un second : voir `entree` (main.go).
	in := entree
	srv := valeurOuDemande(in, *server, "URL du serveur (ex: https://vecu.example.com)", "")
	compte := valeurOuDemande(in, *user, "Nom d'utilisateur", "")
	racine := valeurOuDemande(in, *dir, "Dossier local", defautRacine())
	if srv == "" || compte == "" || racine == "" {
		fatal(fmt.Errorf("serveur, utilisateur et dossier sont requis"))
	}
	racine = cheminAbsolu(racine)

	pw := *password
	if pw == "" {
		pw = promptPassword()
	}

	fmt.Println()
	fmt.Println("1/4  Connexion au serveur")
	cfg, err := sync.Login(srv, compte, pw, *device)
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(racine, 0o755); err != nil {
		fatal(err)
	}
	if err := sync.SaveConfig(racine, cfg); err != nil {
		fatal(err)
	}
	fmt.Printf("     connecté en tant que %s\n", cfg.Username)

	fmt.Println("2/4  Espaces accessibles")
	espaces, err := sync.NewHTTPClient(cfg.Server, cfg.Token).Espaces()
	if err != nil {
		fatal(err)
	}
	if len(espaces) == 0 {
		fmt.Println("     aucun pour l'instant. Dès qu'un accès sera donné depuis")
		fmt.Println("     l'interface, le dossier apparaîtra ici tout seul.")
	}
	for _, e := range espaces {
		mode := "lecture seule"
		if e.Ecriture {
			mode = "lecture et écriture"
		}
		fmt.Printf("     - %s (%d fichiers, %s)\n", e.Nom, e.Fichiers, mode)
	}
	if len(espaces) > 0 {
		exclus := decoupeListe(demande(in, "Espaces à NE PAS descendre sur ce poste (séparés par une virgule, vide = tous)", ""))
		if len(exclus) > 0 {
			cfg.Exclus = exclus
			if err := sync.SaveConfig(racine, cfg); err != nil {
				fatal(err)
			}
		}
	}

	fmt.Println("3/5  Projection des skills vers l'agent local")
	projections := decoupeListe(*agents)
	if *agents == "" { // pas de drapeau : question interactive, défaut selon détection
		defautRep := "non"
		if detecteClaude() {
			fmt.Println("     Claude Code détecté sur ce poste.")
			defautRep = "oui"
		}
		if estOui(demande(in, "Projeter les skills vers Claude Code sur ce poste ? (oui/non)", defautRep)) {
			projections = []string{"claude"}
		}
	}
	cfg.Projections = projections
	if err := sync.SaveConfig(racine, cfg); err != nil {
		fatal(err)
	}
	if len(projections) > 0 {
		fmt.Printf("     projection active : %s\n", strings.Join(projections, ", "))
	} else {
		fmt.Println("     aucune projection (les skills restent dans shared/skills/ sans raccourci)")
	}

	fmt.Println("4/5  Première synchronisation")
	e, err := sync.NewEngine(racine, nil)
	if err != nil {
		fatal(err)
	}
	if *importer != "" {
		e.Importer(decoupeListe(*importer))
	}
	// Cycle = sync puis projection : sur ce premier passage, les skills descendus
	// sont immédiatement projetés vers l'agent local.
	if err := e.Cycle(); err != nil {
		fatal(err)
	}
	st := e.State()
	fmt.Printf("     %d fichiers synchronisés dans %s\n", len(st.Files), racine)
	for _, l := range e.Laisses() {
		if l.Genre == sync.GenreHorsPerimetre {
			continue // compté juste après, jamais énuméré : voir `vecu sync`
		}
		fmt.Printf("     ! %s\n       %s\n", l.Chemin, l.Raison)
	}
	// Un tiers qui installe Vécu sur un dossier contenant des binaires verrait
	// sinon défiler 298 lignes identiques au premier écran du produit.
	if n := len(st.HorsPerimetre); n > 0 {
		fmt.Printf("     %d fichier(s) hors périmètre (binaires) : ils restent sur ce poste, Vécu v2 ne synchronise que du texte.\n", n)
	}
	// Le moteur tient un verrou exclusif sur la racine : le relâcher avant que le
	// service ne démarre son propre daemon, sinon il refusera de se lancer.
	if err := e.Close(); err != nil {
		fatal(err)
	}

	fmt.Println("5/5  Démarrage automatique")
	switch {
	case *sansService:
		fmt.Println("     ignoré (--sans-service). Synchronisation manuelle : vecu start --dir " + racine)
	case runtime.GOOS != "darwin":
		fmt.Printf("     non pris en charge sur %s en v2. Lancer « vecu start --dir %s » au démarrage.\n", runtime.GOOS, racine)
	default:
		if err := installeAgentSysteme(racine); err != nil {
			fmt.Fprintf(os.Stderr, "     échec de l'installation du service : %v\n", err)
			fmt.Fprintln(os.Stderr, "     synchronisation manuelle : vecu start --dir "+racine)
		} else {
			fmt.Println("     service installé : la synchronisation tourne et survit aux redémarrages.")
		}
	}

	fmt.Println()
	fmt.Println("Terminé. Ce dossier se tient à jour tout seul :")
	fmt.Println("   " + racine)
	fmt.Println("Les accès se donnent et se retirent depuis l'interface web ; les dossiers")
	fmt.Println("apparaissent et disparaissent ici sans aucune commande.")
}

// labelAgent : identifiant du service auprès de launchd. Un service par poste
// (une seule racine locale par machine en v2).
const labelAgent = "fr.vecu.sync"

// echappeXML protège les caractères XML d'un chemin (un dossier peut contenir
// « & » ou « < » ; un plist mal formé rend le service inchargeable).
func echappeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// detecteClaude indique si Claude Code semble installé sur ce poste : binaire
// `claude` dans le PATH, ou dossier `~/.claude`. Sert à proposer « oui » par
// défaut à la question de projection, sans jamais l'imposer.
// detecteClaude : délègue à sync, pour que l'app et le CLI ne puissent pas
// diverger sur ce que « Claude Code est installé » veut dire.
func detecteClaude() bool { return sync.DetecteClaude() }

// estOui : lecture tolérante d'une réponse affirmative.
func estOui(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "oui", "o", "yes", "y":
		return true
	}
	return false
}

// defautRacine propose un dossier de départ raisonnable.
func defautRacine() string {
	if maison, err := os.UserHomeDir(); err == nil {
		return filepath.Join(maison, "vecu")
	}
	return "vecu"
}

func cheminAbsolu(p string) string {
	if strings.HasPrefix(p, "~") {
		if maison, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(maison, strings.TrimPrefix(p, "~"))
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// valeurOuDemande renvoie la valeur du drapeau si elle est fournie, sinon la
// demande. Les drapeaux rendent la commande scriptable ; sans eux elle est
// conversationnelle.
func valeurOuDemande(in *bufio.Scanner, valeur, question, defaut string) string {
	if valeur != "" {
		return valeur
	}
	return demande(in, question, defaut)
}

func demande(in *bufio.Scanner, question, defaut string) string {
	if defaut != "" {
		fmt.Printf("%s [%s] : ", question, defaut)
	} else {
		fmt.Printf("%s : ", question)
	}
	if !in.Scan() {
		return defaut
	}
	reponse := strings.TrimSpace(in.Text())
	if reponse == "" {
		return defaut
	}
	return reponse
}

func decoupeListe(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
