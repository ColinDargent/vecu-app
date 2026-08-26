// Vécu - client `vecu` : CLI + daemon de sync.
//
//	vecu setup  [--server URL] [--user NAME] [--dir DIR] [--sans-service]
//	vecu login  --server URL --user NAME [--dir DIR] [--device NOM]
//	vecu sync   [--dir DIR]
//	vecu start  [--dir DIR]
//	vecu status [--dir DIR]
//
// `setup` fait tout ce que les quatre autres font, en une commande. C'est le
// seul moment où quelqu'un a besoin d'un terminal : ensuite, les espaces
// apparaissent et disparaissent selon ce qui se décide dans l'interface web.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/colindargent/vecu/client/sync"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "setup":
		cmdSetup(os.Args[2:])
	case "login":
		cmdLogin(os.Args[2:])
	case "sync":
		cmdSync(os.Args[2:])
	case "start":
		cmdStart(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "skills":
		cmdSkills(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage : vecu <setup|login|sync|start|status|skills> [options]")
	fmt.Fprintln(os.Stderr, "  setup : installation complète d'un poste (recommandé)")
	os.Exit(1)
}

// cmdSkills : les gestes manuels sur les skills. Le rangement automatique se
// fait tout seul à chaque cycle sur le `.claude/skills/` du dossier synchronisé ;
// cette commande est là pour ce que le cycle ne voit pas - un skill créé dans le
// `~/.claude/skills/` personnel, ou n'importe où ailleurs sur le disque.
func cmdSkills(args []string) {
	if len(args) == 0 || args[0] != "adopt" {
		fmt.Fprintln(os.Stderr, "usage : vecu skills adopt <chemin du dossier du skill> [--dir <dossier local>]")
		fmt.Fprintln(os.Stderr, "  range le skill dans shared/skills/ et laisse un lien à sa place")
		os.Exit(1)
	}
	// flag.Parse s'arrête au premier argument positionnel, et personne n'écrit
	// « adopt --dir X chemin » spontanément : on sépare nous-mêmes, pour que
	// l'ordre soit libre.
	options, chemins := separeOptions(args[1:])
	fs := flag.NewFlagSet("skills adopt", flag.ExitOnError)
	dir := fs.String("dir", ".", "dossier local")
	fs.Parse(options)
	if len(chemins) != 1 || strings.TrimSpace(chemins[0]) == "" {
		fatal(fmt.Errorf("un seul chemin attendu : vecu skills adopt <chemin du dossier du skill>"))
	}
	res, sansTrace, err := sync.AdoptionManuelle(*dir, developpeTilde(chemins[0]))
	if err != nil {
		fatal(fmt.Errorf("%s non adopté : %v", res.Slug, err))
	}
	fmt.Printf("%s rangé dans shared/skills/%s, un lien reste à sa place.\n", res.Slug, res.Slug)
	if res.Avertissement != "" {
		fmt.Printf("attention : %s\n", res.Avertissement)
	}
	if sansTrace != "" {
		// Dire la cause CONSTATÉE. Annoncer un service qui tourne alors que l'état
		// est illisible envoie chercher au mauvais endroit.
		fmt.Printf("Le skill partira au prochain cycle, mais l'adoption n'est pas inscrite dans l'état (elle n'apparaîtra pas dans `vecu status`) : %s.\n", sansTrace)
	}
}

// separeOptions coupe une ligne de commande en options (et leurs valeurs) d'un
// côté, arguments positionnels de l'autre. Un « --dir=X » porte sa valeur ; un
// « --dir X » prend le mot suivant.
func separeOptions(args []string) (options, positionnels []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" { // fin des options : tout le reste est un chemin, meme s'il commence par un tiret
			positionnels = append(positionnels, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			positionnels = append(positionnels, a)
			continue
		}
		options = append(options, a)
		if !strings.Contains(a, "=") && i+1 < len(args) {
			i++
			options = append(options, args[i])
		}
	}
	return options, positionnels
}

// developpeTilde : « ~/x » désigne le dossier personnel. Le shell le fait pour
// un argument non quoté, pas pour un argument quoté ni pour un appel programmé.
func developpeTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
}

// cmdLogin authentifie et configure le dossier (login + init de la spec).
func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", "", "URL du serveur Vécu (ex: https://vecu.example.com)")
	user := fs.String("user", "", "nom d'utilisateur")
	dir := fs.String("dir", ".", "dossier local à synchroniser")
	device := fs.String("device", hostname(), "libellé de l'appareil")
	password := fs.String("password", "", "mot de passe (sinon demandé de façon interactive)")
	fs.Parse(args)

	if *server == "" || *user == "" {
		fmt.Fprintln(os.Stderr, "login : --server et --user sont requis")
		os.Exit(1)
	}
	pw := *password
	if pw == "" {
		pw = promptPassword()
	}
	cfg, err := sync.Login(*server, *user, pw, *device)
	if err != nil {
		fatal(err)
	}
	if err := sync.SaveConfig(*dir, cfg); err != nil {
		fatal(err)
	}
	fmt.Printf("connecté en tant que %s, dossier %s configuré.\n", cfg.Username, *dir)
	fmt.Println("synchronisation unique (import) : vecu sync --dir " + *dir)
	fmt.Println("synchronisation continue        : vecu start --dir " + *dir)
}

// cmdSync exécute un cycle de synchronisation unique puis rend la main.
// Usage type : import initial d'un vault existant - copier les fichiers dans
// le dossier puis `vecu sync`. Précondition d'un import propre : serveur vide
// (ou contenu identique), sinon chaque divergence produit une copie de
// conflit. Même moteur que le daemon ; ne pas lancer pendant qu'un daemon
// tourne sur le même dossier (verrou : la commande refuse).
func cmdSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	dir := fs.String("dir", ".", "dossier local à synchroniser")
	importer := fs.String("importer", "", "espaces dont le dossier local existant doit être ADOPTÉ et envoyé (séparés par une virgule)")
	fs.Parse(args)
	if fs.NArg() > 0 {
		// `vecu sync ~/vault` sans --dir synchroniserait silencieusement `.`.
		fmt.Fprintln(os.Stderr, "sync : argument inattendu, utiliser --dir DOSSIER")
		os.Exit(1)
	}

	e, err := sync.NewEngine(*dir, nil)
	if err != nil {
		fatal(err)
	}
	if *importer != "" {
		e.Importer(decoupeListe(*importer))
	}
	// La seule voie par laquelle une suppression massive peut partir, et elle
	// exige un humain devant le clavier. Le daemon ne branche jamais rien : sans
	// terminal (service, cron, sortie redirigée), la garde bloque, point.
	//
	// Les DEUX descripteurs sont testés : la question s'écrit sur stderr et se
	// lit sur stdin. Ne tester que stdin faisait attendre le processus sur une
	// question que personne ne voyait, dès un « vecu sync 2> journal.txt ».
	// Source: https://pkg.go.dev/golang.org/x/term#IsTerminal
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) {
		e.SurSuppressionMassive(confirmeSuppressionMassive)
	}
	if err := e.Cycle(); err != nil {
		fatal(err)
	}
	st := e.State()
	fmt.Printf("synchronisé : %d fichiers suivis dans %d espace(s), version %s\n",
		len(st.Files), len(st.Espaces), st.Head)
	// Un import partiel doit se voir. Pas seulement un compte : la liste et le
	// motif, sinon personne n'ira les chercher.
	var perdus, serveur []sync.Laisse
	// Compté depuis le REGISTRE, pas depuis les laisses. Une laisse hors
	// périmètre ne se produit qu'une fois par empreinte - c'est tout l'objet du
	// registre - donc la compter donnerait 298 au premier cycle puis 0 ensuite.
	// Le registre, lui, porte la photo complète et stable.
	horsPerimetre := len(st.HorsPerimetre)
	for _, l := range e.Laisses() {
		switch {
		case l.Genre == sync.GenreEspace:
			fmt.Fprintf(os.Stderr, "\nespace %s : %s\n", l.Chemin, l.Raison)
		case l.Genre == sync.GenreHorsPerimetre:
			// Déjà compté par le registre. Le case existe pour l'empêcher de
			// tomber dans « sur le serveur », ce qui serait faux.
		case l.Perdu():
			perdus = append(perdus, l)
		default:
			serveur = append(serveur, l)
		}
	}
	if horsPerimetre > 0 {
		fmt.Fprintf(os.Stderr, "\n%d fichier(s) hors périmètre (binaires) : ils restent sur ce poste, Vécu v2 ne synchronise que du texte.\n", horsPerimetre)
	}
	// Les copies de conflit locales. Elles ne se synchronisent plus, donc rien
	// d'autre ne peut les montrer. Jamais une erreur : rien n'est perdu, c'est
	// l'inverse - un arbitrage attend.
	if copies := e.CopiesEnAttente(); len(copies) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d copie(s) de conflit en attente d'arbitrage. Rien n'est perdu : chacune porte une édition locale que Vécu a mise de côté plutôt que de l'écraser.\n", len(copies))
		for _, c := range copies {
			fmt.Fprintf(os.Stderr, "  - %s\n", c)
		}
	}
	if len(serveur) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d chemin(s) non appliqué(s) sur ce poste. Rien n'est perdu : ils sont sur le serveur.\n", len(serveur))
		for _, l := range serveur {
			fmt.Fprintf(os.Stderr, "  - %s\n    %s\n", l.Chemin, l.Raison)
		}
	}
	if len(perdus) > 0 {
		fmt.Fprintf(os.Stderr, "\nattention : %d fichier(s) NON synchronisé(s), ils n'existent que sur ce poste :\n", len(perdus))
		for _, l := range perdus {
			fmt.Fprintf(os.Stderr, "  - %s\n    %s\n", l.Chemin, l.Raison)
		}
		// Seul un contenu qui n'existe QUE sur ce poste fait échouer la commande.
		// Une remarque d'espace, ou un chemin qui est sur le serveur, ne signale
		// aucune perte : sortir en erreur pour ça apprend à ignorer le rapport.
		os.Exit(1)
	}
}

// entree : le SEUL lecteur de l'entrée standard du processus, partagé par
// l'assistant `setup` et par la confirmation de suppression. Un bufio.Scanner
// lit en avance : en créer un second ferait avaler par le premier ce que le
// second devait lire, et la question resterait sans réponse.
var entree = bufio.NewScanner(os.Stdin)

// confirmeSuppressionMassive pose la question, dans le terminal, avant qu'une
// suppression massive parte chez tous les membres.
//
// Retaper le nom de l'espace plutôt que « o/n » : c'est un geste délibéré, et
// c'est la preuve que la personne a lu DE QUEL espace il s'agit. Un « o »
// réflexe n'est pas une confirmation.
func confirmeSuppressionMassive(espace string, constatees, verifies int) bool {
	fmt.Fprintf(os.Stderr, "\nATTENTION : %d fichiers ont disparu du dossier « %s » (sur %d vérifiés ce cycle).\n", constatees, espace, verifies)
	fmt.Fprintln(os.Stderr, "Les supprimer les fera disparaître pour TOUS les membres qui ont accès à cet espace.")
	fmt.Fprintln(os.Stderr, "S'ils ont seulement été déplacés ou renommés, répondre non : ils redescendront tout seuls.")
	fmt.Fprintf(os.Stderr, "\nPour confirmer, retaper le nom de l'espace (%s). Entrée seule pour annuler : ", espace)
	if !entree.Scan() {
		fmt.Fprintln(os.Stderr, "\nannulé : aucune suppression n'est envoyée.")
		return false
	}
	if strings.TrimSpace(entree.Text()) != espace {
		fmt.Fprintln(os.Stderr, "annulé : aucune suppression n'est envoyée.")
		return false
	}
	return true
}

// cmdStart lance le daemon de synchronisation.
func cmdStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	dir := fs.String("dir", ".", "dossier local à synchroniser")
	fs.Parse(args)

	e, err := sync.NewEngine(*dir, nil)
	if err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("synchronisation de %s (Ctrl-C pour arrêter)\n", *dir)
	if err := e.Run(ctx); err != nil && err != context.Canceled {
		fatal(err)
	}
	fmt.Println("\narrêt.")
}

// cmdStatus affiche l'état de synchronisation local.
func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("dir", ".", "dossier local")
	fs.Parse(args)

	cfg, err := sync.LoadConfig(*dir)
	if err != nil {
		fatal(err)
	}
	st, err := sync.LoadState(*dir)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("serveur      : %s\n", cfg.Server)
	fmt.Printf("compte       : %s\n", cfg.Username)
	fmt.Printf("dossier      : %s\n", cheminAbsolu(*dir))
	head := st.Head
	if head == "" {
		head = "(jamais synchronisé)"
	}
	fmt.Printf("version      : %s\n", head)
	fmt.Printf("dernier cycle: %s\n", depuis(st.Cycle))

	// Compte par espace : « 659 fichiers » ne dit pas si un dossier attendu est
	// vide. Le détail par espace, si.
	fmt.Println("espaces      :")
	if len(st.Espaces) == 0 {
		fmt.Println("  (aucun - aucun accès accordé, ou tous exclus sur ce poste)")
	}
	for _, espace := range st.Espaces {
		n := 0
		for p := range st.Files {
			if strings.HasPrefix(p, espace+"/") {
				n++
			}
		}
		exclu := ""
		for _, x := range cfg.Exclus {
			if x == espace {
				exclu = "  (exclu sur ce poste)"
			}
		}
		fmt.Printf("  - %-24s %d fichiers%s\n", espace, n, exclu)
	}
	fmt.Printf("fichiers     : %d suivis au total\n", len(st.Files))

	// Skills : montés (dans shared/skills/) et projetés vers l'agent local. Un
	// skill présent mais non utilisable par l'agent ne doit jamais rester muet.
	if montes := sync.SkillsMontes(st); len(montes) > 0 {
		// Un skill peut être projeté vers plusieurs outils, ou vers aucun. Le
		// rapport doit distinguer les deux cibles : « projeté » tout court était
		// vrai pour Claude et masquait un blocage côté Cursor et Codex.
		cibles := map[string][]string{}
		for _, slug := range st.Projetes {
			cibles[slug] = append(cibles[slug], "claude")
		}
		for _, slug := range st.ProjetesAgents {
			cibles[slug] = append(cibles[slug], "cursor+codex")
		}
		// Raisons de non-projection : elles vivent dans les Laisses, dont le
		// chemin est le skill (shared/skills/<slug>). Deux cibles bloquées sur le
		// même skill produisent DEUX Laisses au même chemin : les accumuler, sans
		// quoi la dernière lue écrase l'autre et un blocage disparaît de l'écran.
		raisons := map[string][]string{}
		for _, l := range st.Laisses {
			if slug, ok := strings.CutPrefix(l.Chemin, "shared/skills/"); ok && !strings.Contains(slug, "/") {
				raisons[slug] = append(raisons[slug], l.Raison)
			}
		}
		fmt.Println("skills       :")
		if len(st.Projetes) == 0 && len(st.ProjetesAgents) == 0 {
			fmt.Println("  (aucune projection active sur ce poste - relancer « vecu setup »,")
			fmt.Println("   ou créer .agents/skills/ pour servir Cursor et Codex)")
		}
		for _, slug := range montes {
			if vers := cibles[slug]; len(vers) > 0 {
				fmt.Printf("  - %-24s projeté (%s)\n", slug, strings.Join(vers, ", "))
			} else {
				fmt.Printf("  - %-24s non projeté\n", slug)
			}
			// Les accrocs se lisent MÊME quand le skill est projeté ailleurs :
			// c'est tout l'intérêt, un blocage sur une cible ne doit pas être
			// masqué par le succès de l'autre.
			for _, r := range raisons[slug] {
				fmt.Printf("      %s\n", r)
			}
		}
	}

	// Adoptions : Vécu a DÉPLACÉ des fichiers sans que personne le lui demande.
	// C'est la trace de ce geste, et elle reste lisible longtemps après le cycle
	// qui l'a produit - les cinq dernières suffisent à l'écran, le compte dit le
	// reste.
	if n := len(st.Adoptes); n > 0 {
		fmt.Printf("\nskills adoptés : %d au total\n", n)
		debut := max(0, n-5)
		for _, a := range st.Adoptes[debut:] {
			fmt.Printf("  - %-24s déplacé de %s (%s)\n", a.Slug, a.Source, depuis(a.Date))
			if a.Avertissement != "" {
				fmt.Printf("    à faire : %s\n", a.Avertissement)
			}
		}
		if debut > 0 {
			fmt.Printf("  (et %d plus anciens)\n", debut)
		}
	}
	// Candidats refusés : des skills que l'adoption n'a pas rangés. Jamais des
	// Laisses - un dossier tiers ferait sinon échouer tous les cycles.
	if len(st.Candidats) > 0 {
		fmt.Printf("\nskills non adoptés : %d\n", len(st.Candidats))
		for _, c := range st.Candidats {
			fmt.Printf("  - %s\n    %s\n", c.Chemin, c.Raison)
		}
		fmt.Println("Ces skills restent où ils sont : personne d'autre ne les a.")
	}
	// Le `~/.claude/skills/` personnel : listé, jamais touché. C'est le poste et
	// pas la boîte, et c'est là que vivent les installs tierces - rien n'en part
	// sans une commande tapée à la main.
	if perso := sync.CandidatsPersonnels(*dir); len(perso) > 0 {
		fmt.Printf("\nskills personnels adoptables : %d (dans ~/.claude/skills/, jamais déplacés tout seuls)\n", len(perso))
		for _, chemin := range perso {
			fmt.Printf("  - %s\n    vecu skills adopt %q\n", filepath.Base(chemin), chemin)
		}
	}

	var perdus, serveur []sync.Laisse
	horsPerimetre := len(st.HorsPerimetre) // le registre, jamais les laisses : voir `sync`
	for _, l := range st.Laisses {
		switch {
		case l.Genre == sync.GenreEspace:
			fmt.Printf("\nespace %s : %s\n", l.Chemin, l.Raison)
		case l.Genre == sync.GenreHorsPerimetre:
			// Déjà compté par le registre.
		case l.Perdu():
			perdus = append(perdus, l)
		default:
			serveur = append(serveur, l)
		}
	}
	if horsPerimetre > 0 {
		fmt.Printf("\n%d fichier(s) hors périmètre (binaires) : ils restent sur ce poste, Vécu v2 ne synchronise que du texte.\n", horsPerimetre)
	}
	if copies := sync.CopiesDeConflitLocales(*dir, cfg.Montages, st.Espaces); len(copies) > 0 {
		fmt.Printf("\n%d copie(s) de conflit en attente d'arbitrage. Rien n'est perdu : chacune porte une édition locale que Vécu a mise de côté plutôt que de l'écraser.\n", len(copies))
		for _, c := range copies {
			fmt.Printf("  - %s\n", c)
		}
	}
	// Les collisions de casse déjà dans l'état. Vécu n'en crée plus, il n'efface
	// pas celles qui existent : elles portent du contenu, et trancher lequel des
	// deux noms garder est un geste humain. Les nommer est tout ce que la machine
	// peut faire honnêtement.
	if groupes := sync.FantomesDeCasse(st.Files); len(groupes) > 0 {
		fmt.Printf("\n%d collision(s) de casse dans l'état : des chemins que ce disque ne sait pas distinguer.\n"+
			"Rien n'est perdu, tout est sur le serveur. Un seul de ces chemins a un fichier ici ; les autres décrivent un contenu qu'ils ne portent pas.\n", len(groupes))
		for _, groupe := range groupes {
			for _, chemin := range groupe {
				fmt.Printf("  - %s\n    état : %s\n", chemin, sync.Court(st.Files[chemin]))
			}
			fmt.Println("    pour trancher : renommer l'un des deux depuis un poste, ou retirer celui qui fait doublon.")
		}
	}
	if len(serveur) > 0 {
		fmt.Printf("\nnon appliqués sur ce poste au dernier cycle : %d chemin(s)\n", len(serveur))
		for _, l := range serveur {
			fmt.Printf("  - %s\n    %s\n", l.Chemin, l.Raison)
		}
		fmt.Println("Rien n'est perdu : ces chemins sont sur le serveur.")
	}
	if len(perdus) > 0 {
		fmt.Printf("\nnon synchronisés au dernier cycle : %d fichier(s)\n", len(perdus))
		for _, l := range perdus {
			fmt.Printf("  - %s\n    %s\n", l.Chemin, l.Raison)
		}
		fmt.Println("Ces fichiers n'existent que sur ce poste : personne d'autre ne les a.")
	}
}

// depuis rend un horodatage RFC 3339 en durée lisible.
func depuis(iso string) string {
	if iso == "" {
		return "jamais"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return "il y a moins d'une minute"
	case d < time.Hour:
		return fmt.Sprintf("il y a %d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("il y a %d heures", int(d.Hours()))
	}
	return fmt.Sprintf("le %s", t.Local().Format("02/01/2006 à 15h04"))
}

func promptPassword() string {
	fmt.Fprint(os.Stderr, "mot de passe : ")
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fatal(err)
	}
	return string(b)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "vecu-client"
	}
	return h
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "erreur :", err)
	os.Exit(1)
}
