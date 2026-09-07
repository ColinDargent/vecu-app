// Commande vecu-app : application de bureau menu-bar (macOS) de Vécu.
//
// Elle embarque le moteur de synchronisation (client/sync) : au lieu d'un
// LaunchAgent qui lance « vecu start » dans un terminal invisible, l'app est
// elle-même le process supervisé, avec un point d'entrée visible en barre de
// menus qui montre l'état de la sync.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/systray"

	"github.com/colindargent/vecu/client/sync"
)

// rafraichi : cadence de mise à jour des libellés du menu depuis l'état moteur.
const rafraichi = 3 * time.Second

// maxPropositions : nombre d'entrées de menu réservées aux dossiers qu'on nous
// a ouverts (parcours B).
//
// Des entrées PRÉ-CRÉÉES et masquées, pas des entrées ajoutées au fil de l'eau :
// systray ne sait pas retirer un élément, donc un menu qui en ajoute à chaque
// cycle grossit indéfiniment. Cinq, parce qu'au-delà ce n'est plus une
// proposition mais une file d'attente - et le surplus reste visible dans
// `vecu status`.
const maxPropositions = 5

// app tient les objets vivants de l'interface (moteur + éléments de menu à
// réactualiser). Un seul exemplaire, construit dans onReady.
type app struct {
	engine  *sync.Engine
	racine  string
	serveur string
	token   string // jeton d'appareil (bearer) pour l'auto-update

	// ctx/stop : cycle de vie du moteur, partagés avec l'auto-update (annulation
	// propre avant un redémarrage sur nouvelle version).
	ctx  context.Context
	stop context.CancelFunc
	// majEnCours : une seule mise à jour à la fois (boucle 6h vs clic menu).
	majEnCours atomic.Bool
	// enFermeture : l'utilisateur a cliqué « Quitter » — l'auto-update ne doit
	// alors pas relancer l'app par un os.Exit non-zéro.
	enFermeture atomic.Bool
	// dialogueEnCours : un seul parcours de dialogues à la fois, quel qu'il
	// soit. Deux clics sur « Partager » ouvriraient deux sélecteurs, donc
	// potentiellement deux espaces créés sur le serveur pour un seul geste
	// voulu - et le serveur ne sait pas défaire. Le drapeau est devenu commun
	// quand « Retirer » est arrivé : deux dialogues empilés de la même app sont
	// une façon sûre de faire cliquer quelqu'un sur la mauvaise question.
	dialogueEnCours atomic.Bool

	mStatut     *systray.MenuItem
	mFichiers   *systray.MenuItem
	mLaisses    *systray.MenuItem
	mNouveaux   *systray.MenuItem
	mCopies     *systray.MenuItem
	mCollisions *systray.MenuItem
	mSkills     *systray.MenuItem
	mSync       *systray.MenuItem
	// mPropositions : les entrées du parcours B, masquées tant qu'il n'y a rien
	// à proposer.
	mPropositions []*systray.MenuItem
	// propositions : la photo courante ([]sync.Proposition), reposée à chaque
	// rafraîchissement. Un `atomic.Value` plutôt qu'un mutex parce que le paquet
	// `sync` du dépôt occupe déjà ce nom d'import : la boucle de rafraîchissement
	// écrit, les goroutines de clic lisent.
	propositions atomic.Value
}

func main() {
	// Mode sonde : « vecu-app --check » (ou --version) imprime la version et sort,
	// SANS aucun effet de bord. Doit rester en tout premier — avant le rôle
	// SERVICE comme avant migreEtInstalle : c'est ce mode qu'exécute l'auto-update
	// pour valider un binaire téléchargé avant de l'installer. Toute autre
	// invocation manuelle déclenche migreEtInstalle (installation du service).
	if len(os.Args) > 1 && (os.Args[1] == "--check" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	// Deux rôles pour un même binaire (voir service.go). L'instance de SERVICE
	// (lancée par launchd, VECU_SERVICE=1) tient le moteur et ne touche jamais
	// launchd. L'instance MANUELLE (double-clic) installe/migre le service, puis
	// sort sans jamais lancer le moteur.
	if estInstanceService() {
		systray.Run(onReady, onExit)
		return
	}
	migreEtInstalle()
}

// migreEtInstalle : chemin de l'instance manuelle. Installe (ou met à jour) le
// service launchd, puis rend la main — le service prend le relais du process.
func migreEtInstalle() {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		fmt.Fprintln(os.Stderr, "impossible de localiser le binaire de l'app :", err)
		os.Exit(1)
	}
	if resolu, e := filepath.EvalSymlinks(bin); e == nil {
		bin = resolu
	}
	// App translocation (Gatekeeper lance un .app quarantiné depuis un chemin
	// éphémère /AppTranslocation/...) : ce chemin disparaît, l'inscrire dans le
	// plist donnerait un service mort au reboot. Exiger une installation stable.
	if strings.Contains(bin, "/AppTranslocation/") {
		fmt.Fprintln(os.Stderr, "Vécu est lancé depuis un emplacement temporaire (Gatekeeper).")
		fmt.Fprintln(os.Stderr, "Déplacer Vécu.app dans ~/Applications puis relancer.")
		os.Exit(1)
	}

	// Un seul migrateur à la fois : deux double-clics rapides ne doivent pas
	// bootout/bootstrap en parallèle. Verrou non bloquant ; si occupé, une autre
	// instance s'en charge déjà.
	verrou, err := verrouMigration()
	if err != nil {
		fmt.Println("migration déjà en cours dans une autre instance.")
		return
	}
	defer verrou.Close()

	// PREMIER LANCEMENT. Aucune source ne dit où travailler : demander, plutôt
	// qu'inventer `~/second-brain` et installer un service pour un dossier que
	// personne n'a désigné.
	//
	// C'est ici et pas dans `onReady` : ce chemin est celui du double-clic, donc
	// une session utilisateur ordinaire, où un dialogue s'affiche à coup sûr.
	// `onReady` tourne sous launchd, et un accueil qui n'apparaîtrait pas y
	// serait pire qu'une erreur.
	//
	// `accueil` écrit lui-même la racine mémorisée quand il réussit ; renoncer
	// laisse la machine intacte et ne doit donc RIEN installer.
	var racine string
	premier := premierLancement()
	if premier {
		if racine = accueil(func(f string, a ...any) { fmt.Printf(f+"\n", a...) }); racine == "" {
			fmt.Println("installation abandonnée — rien n'a été écrit sur ce Mac.")
			return
		}
	} else {
		racine = resoudRacine()
		// La racine doit être fiable AVANT d'installer le service : c'est le
		// dossier qu'il synchronisera. L'échec d'écriture n'est pas ignoré.
		if err := ecritAppConfig(appConfig{Racine: racine}); err != nil {
			fmt.Fprintln(os.Stderr, "écriture de la config app :", err)
			os.Exit(1)
		}
	}

	svc := serviceCfg{
		label:      labelService,
		binaire:    bin,
		racine:     racine,
		definition: cheminDefinition(labelService),
		journal:    cheminJournal(),
	}
	installe, err := synchroniseService(svc)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "installation du service :", err)
		os.Exit(1)
	case installe:
		fmt.Println("service Vécu installé — l'icône apparaît en barre de menus.")
	default:
		fmt.Println("service Vécu déjà en place.")
	}

	// Le premier cycle appartient au service, pas à ce processus. `cmdSetup` le
	// lance lui-même parce qu'il n'installe le service qu'après ; ici le service
	// vient de démarrer et tient déjà le verrou de la racine. En lancer un
	// second ici échouerait sur ce verrou, et l'échec n'aurait rien appris.
	if premier {
		_ = informe("Vécu est installé.\n\n" +
			"L'icône est dans la barre de menus, en haut à droite. La première synchronisation " +
			"démarre maintenant ; les dossiers qui vous sont ouverts apparaîtront dans le dossier choisi.")
	}
}

// verrouMigration : verrou exclusif non bloquant sur un fichier dédié. Rend une
// erreur si un autre process le tient déjà (une migration concurrente est en
// cours).
//
// Délègue à `sync.VerrouExclusif` depuis le port Windows. Cette fonction posait
// son propre flock, dupliquant la mécanique du moteur - donc, sur Windows, il
// aurait fallu écrire deux fois la même couture et se souvenir des deux.
func verrouMigration() (*os.File, error) {
	return sync.VerrouExclusif(filepath.Join(filepath.Dir(cheminAppConfig()), "migration.lock"))
}

func onReady() {
	// Deux icônes et non une, et ce n'est pas une coquetterie : sur macOS le
	// premier argument est l'icône « template » (monochrome, teintée par le
	// système) ; sur Windows, systray ignore le premier et charge le second.
	// Les formats diffèrent - voir icone_darwin.go et icone_windows.go.
	systray.SetTemplateIcon(iconTemplate, iconeSysteme)
	systray.SetTooltip("Vécu")

	racine := resoudRacine()
	logfile := ouvreJournal()

	// La config donne l'URL du serveur (pour « Ouvrir l'admin web »). Une config
	// absente ou illisible n'est pas fatale : l'entrée admin restera muette.
	cfg, _ := sync.LoadConfig(racine)

	a := &app{racine: racine, serveur: cfg.Server, token: cfg.Token}

	// Retry sur l'acquisition du moteur : juste après la migration, l'ancien
	// daemon vient d'être booté out et peut mettre un instant à relâcher son
	// verrou flock (il peut finir un cycle en cours avant de sortir sur SIGTERM).
	// Fenêtre large avant d'abandonner ; au-delà, un autre process tient
	// vraiment le dossier et l'utilisateur devra intervenir (quit + relance).
	logf := log.New(logfile, "", log.LstdFlags).Printf

	// La passe unique sur vecu-app.log : ici, au démarrage, AVANT que le moteur
	// ne commence à cycler. C'est la fenêtre où le fichier est le plus calme.
	if compte := filtreJournalApplicatif(); compte != "" {
		logf("%s", compte)
	}

	var eng *sync.Engine
	var err error
	for i := 0; i < 30; i++ {
		if eng, err = sync.NewEngine(racine, logf); err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		// Le verrou reste tenu par un autre process après la fenêtre de retry.
		// On le dit, sans planter, et on laisse Quitter.
		mErr := systray.AddMenuItem("Vécu indisponible", err.Error())
		mErr.Disable()
		mErr.SetTooltip(err.Error())
		ajouteQuitter()
		return
	}
	a.engine = eng

	// Éléments d'information (désactivés : non cliquables, ce sont des libellés).
	a.mStatut = systray.AddMenuItem("Démarrage…", "")
	a.mStatut.Disable()
	a.mFichiers = systray.AddMenuItem("", "")
	a.mFichiers.Disable()
	a.mLaisses = systray.AddMenuItem("", "")
	a.mLaisses.Disable()
	a.mLaisses.Hide()
	a.mSkills = systray.AddMenuItem("", "")
	a.mSkills.Disable()
	a.mSkills.Hide()
	// Les copies de conflit locales ne se synchronisent plus : cette ligne est
	// la SEULE surface où elles apparaissent. Sans elle, on ne les découvre
	// qu'en tombant dessus dans un dossier. Non cliquable : Vécu n'en supprime
	// jamais aucune, l'arbitrage se fait dans l'éditeur.
	a.mCopies = systray.AddMenuItem("", "")
	a.mCopies.Disable()
	a.mCopies.Hide()
	a.mCollisions = systray.AddMenuItem("", "")
	a.mCollisions.Disable()
	a.mCollisions.Hide()
	// Le seul libellé d'information qui soit CLIQUABLE : le lire est un geste,
	// et c'est ce geste qui le fait taire. Un signal qui s'efface tout seul au
	// cycle suivant n'aurait aucune chance d'être vu.
	a.mNouveaux = systray.AddMenuItem("", "")
	a.mNouveaux.Hide()
	// Les dossiers qu'on nous a ouverts. Ils sont en haut du menu, avec les
	// autres événements : ce sont des choses arrivées depuis la dernière fois
	// qu'on a regardé, pas des commandes.
	for i := 0; i < maxPropositions; i++ {
		item := systray.AddMenuItem("", "Choisir où poser ce dossier")
		item.Hide()
		a.mPropositions = append(a.mPropositions, item)
		go func(rang int, ch <-chan struct{}) {
			for range ch {
				p, ok := a.propositionAu(rang)
				if !ok {
					continue // la liste a changé entre l'affichage et le clic
				}
				go a.rejoindreDossier(p, logf)
			}
		}(i, item.ClickedCh)
	}

	systray.AddSeparator()
	a.mSync = systray.AddMenuItem("Synchroniser maintenant", "Lancer un cycle de synchronisation")
	// Le geste central de la V1 : partager un dossier qui existe déjà, là où il
	// est. Il vit dans le menu et nulle part ailleurs - le mettre derrière une
	// commande reviendrait à ne pas l'avoir livré.
	mPartage := systray.AddMenuItem("Partager un dossier…", "Partager un dossier de ce Mac, sans le déplacer")
	if a.serveur == "" {
		// Sans serveur connu, le parcours irait jusqu'au choix du dossier pour
		// échouer à la création. Refuser d'entrée vaut mieux que promettre.
		mPartage.Disable()
	}
	mRetrait := systray.AddMenuItem("Retirer un dossier…", "Retirer un dossier de la synchronisation, en gardant son contenu sur ce Mac")
	mOuvrir := systray.AddMenuItem("Ouvrir le dossier", a.racine)
	mAdmin := systray.AddMenuItem("Ouvrir l'admin web", a.serveur)
	if a.serveur == "" {
		mAdmin.Disable()
	}
	systray.AddSeparator()
	mVersion := systray.AddMenuItem("Version "+version, "")
	mVersion.Disable()
	mMaj := systray.AddMenuItem("Vérifier les mises à jour", "Chercher une nouvelle version")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quitter", "Quitter Vécu")

	go func() {
		for range a.mNouveaux.ClickedCh {
			if err := a.engine.MarqueSkillsVus(); err != nil {
				logf("marquage des nouveaux skills : %v", err)
			}
			a.refresh()
		}
	}()

	// Le moteur tourne en tâche de fond : cycle initial + fsnotify + poll. Le
	// contexte s'annule à la sortie pour arrêter proprement le daemon.
	ctx, stop := context.WithCancel(context.Background())
	a.ctx, a.stop = ctx, stop
	go func() {
		if err := a.engine.Run(ctx); err != nil && err != context.Canceled {
			log.New(logfile, "", log.LstdFlags).Printf("daemon : %v", err)
		}
	}()

	a.refresh()
	go a.boucleRefresh(ctx)
	// Auto-update : vérifie au démarrage (après un délai) puis périodiquement.
	go a.boucleMaj(ctx, log.New(logfile, "", log.LstdFlags).Printf)

	go func() {
		for {
			select {
			case <-a.mSync.ClickedCh:
				a.syncMaintenant()
			case <-mPartage.ClickedCh:
				// Dans sa propre goroutine : le parcours attend un humain à
				// chaque dialogue, et cette boucle doit rester vivante pour que
				// le reste du menu réponde pendant ce temps.
				go a.partageDossier(logf)
			case <-mRetrait.ClickedCh:
				go a.retireDossier(logf)
			case <-mOuvrir.ClickedCh:
				_ = ouvreDossier(a.racine)
			case <-mAdmin.ClickedCh:
				if a.serveur != "" {
					go a.ouvreAdmin(logf)
				}
			case <-mMaj.ClickedCh:
				go a.verifieMaintenant()
			case <-mQuit.ClickedCh:
				// Marquer la fermeture AVANT d'annuler : une mise à jour en cours
				// verra le flag et ne relancera pas l'app par os.Exit non-zéro.
				a.enFermeture.Store(true)
				stop()
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {}

// ouvreAdmin ouvre le web admin dans le navigateur, déjà connecté.
//
// Le billet à usage unique évite de ressaisir un mot de passe, et c'est tout le
// gain : le jeton d'appareil ne quitte pas l'app. Si quoi que ce soit échoue -
// serveur injoignable, version de serveur antérieure à cette route - on ouvre
// l'admin comme avant, et la personne se connecte. Un raccourci qui tombe en
// panne doit rendre le chemin long, pas une erreur.
func (a *app) ouvreAdmin(logf func(string, ...any)) {
	url := a.serveur + "/admin/"
	if a.token != "" {
		if avecSession, err := sync.NewHTTPClient(a.serveur, a.token).SessionWeb(); err == nil {
			url = avecSession
		} else {
			logf("ouverture de l'admin sans session (%v)", err)
		}
	}
	if err := ouvreURL(url); err != nil {
		logf("ouverture de l'admin : %v", err)
	}
}

// redemarre relance le service par une sortie en échec.
//
// LE CODE DE SORTIE EST LE CONTRAT, et c'est pour ça que ce geste vit à un seul
// endroit : le plist porte `KeepAlive {SuccessfulExit=false}` (service.go), donc
// launchd relance sur une sortie NON-ZÉRO et pas sur une sortie propre - qui
// est ce qu'il faut pour que « Quitter » quitte vraiment. Sortir en 0 ici ne
// redémarrerait rien et laisserait la personne sans barre de menus.
//
// Deux appelants, une seule règle : la mise à jour automatique (relancer sur la
// nouvelle version) et le montage d'un espace (reprendre les verrous et relire
// la table). Le moteur est crash-safe - son état est écrit à chaque cycle - donc
// couper un cycle en cours ne perd rien ; on lui laisse quand même le temps de
// s'arrêter proprement.
func (a *app) redemarre(logf func(string, ...any), motif string) {
	// L'utilisateur est-il en train de quitter ? Ne pas relancer une app qu'il
	// vient de fermer volontairement.
	if a.enFermeture.Load() {
		logf("redémarrage annulé (fermeture en cours) : %s", motif)
		return
	}
	logf("redémarrage du service : %s", motif)
	if a.stop != nil {
		a.stop()
		time.Sleep(drainAvantExit)
	}
	os.Exit(1) // sortie non-zéro → launchd relance
}

// refresh relit l'état du moteur et repose les libellés du menu.
func (a *app) refresh() {
	l := menuLabels(a.engine.State(), a.engine.Laisses(), a.engine.NouveauxSkills(), a.engine.CopiesEnAttente(), time.Now())
	a.mStatut.SetTitle(l.statut)
	a.mFichiers.SetTitle(l.fichiers)
	a.mFichiers.SetTooltip(l.espacesInfobul)
	if l.laissesVisible {
		a.mLaisses.SetTitle(l.laisses)
		a.mLaisses.Show()
	} else {
		a.mLaisses.Hide()
	}
	if l.skillsVisible {
		a.mSkills.SetTitle(l.skills)
		a.mSkills.Show()
	} else {
		a.mSkills.Hide()
	}
	if l.nouveauxVisible {
		a.mNouveaux.SetTitle(l.nouveaux)
		a.mNouveaux.SetTooltip(l.nouveauxInfo)
		a.mNouveaux.Show()
	} else {
		a.mNouveaux.Hide()
	}
	if l.copiesVisible {
		a.mCopies.SetTitle(l.copies)
		a.mCopies.SetTooltip(l.copiesInfo)
		a.mCopies.Show()
	} else {
		a.mCopies.Hide()
	}
	if l.collisionsVisible {
		a.mCollisions.SetTitle(l.collisions)
		a.mCollisions.SetTooltip(l.collisionsInfo)
		a.mCollisions.Show()
	} else {
		a.mCollisions.Hide()
	}

	// Les propositions : la photo est reposée AVANT les libellés, pour qu'un clic
	// arrivé entre les deux lise la liste qui correspond à ce qui est affiché.
	props := a.engine.Propositions()
	a.propositions.Store(props)
	libelles := libellePropositions(props)
	for i, item := range a.mPropositions {
		if i < len(libelles) {
			item.SetTitle(libelles[i])
			item.Show()
		} else {
			item.Hide()
		}
	}
}

// propositionAu rend la proposition affichée à ce rang du menu.
//
// Le rang n'est pas une identité : entre l'affichage et le clic, un cycle a pu
// retirer une proposition et décaler les autres. Le `ok` à faux est le cas
// nominal de cette course, pas une anomalie - mieux vaut ne rien faire que
// rejoindre un dossier qu'on n'a pas montré à cette place.
func (a *app) propositionAu(rang int) (sync.Proposition, bool) {
	props, _ := a.propositions.Load().([]sync.Proposition)
	if rang < 0 || rang >= len(props) {
		return sync.Proposition{}, false
	}
	return props[rang], true
}

func (a *app) boucleRefresh(ctx context.Context) {
	t := time.NewTicker(rafraichi)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.refresh()
		}
	}
}

// syncMaintenant force un cycle à la demande. Il se sérialise avec les cycles du
// daemon via le verrou interne du moteur (e.mu), donc jamais deux en parallèle.
func (a *app) syncMaintenant() {
	a.mSync.Disable()
	a.mStatut.SetTitle("Synchronisation…")
	if err := a.engine.Cycle(); err != nil {
		a.mStatut.SetTitle("Échec : " + err.Error())
	}
	a.refresh()
	a.mSync.Enable()
}

// resoudRacine : dossier local à superviser. Priorité : VECU_DIR (dev/tests) >
// app-config (posé à la migration) > --dir de l'ancien LaunchAgent CLI (appris
// à la première migration) > ~/second-brain.
func resoudRacine() string {
	if d := os.Getenv("VECU_DIR"); d != "" {
		return d
	}
	if d := racineAppConfig(); d != "" {
		return d
	}
	if d, ok := racineDeclaree(cheminDefinition(labelService)); ok {
		return d
	}
	// Dernier recours, et il ne devrait plus jamais servir : `premierLancement`
	// intercepte le cas où aucune source ne répond, et l'accueil demande où
	// travailler au lieu d'inventer un dossier. Ce repli reste comme filet, mais
	// il porte le nom du dossier de Colin, hérité de l'époque où le produit
	// n'avait que deux utilisateurs - inventer ça chez un tiers serait bizarre.
	maison, _ := os.UserHomeDir()
	return filepath.Join(maison, "second-brain")
}

// premierLancement : aucune des trois sources ne dit où travailler, donc ce
// poste n'a jamais été installé.
//
// Les trois mêmes sources que `resoudRacine`, dans le même ordre, et c'est
// voulu : la question « sait-on où travailler ? » et la question « où
// travaille-t-on ? » doivent avoir la même réponse, sans quoi l'accueil
// s'ouvrirait sur un poste installé ou l'inverse.
//
// Le plist compte comme une installation même si l'app.json manque : c'est le
// poste venu de l'ancien daemon CLI, qui a une racine parfaitement valide.
func premierLancement() bool {
	if os.Getenv("VECU_DIR") != "" {
		return false
	}
	if racineAppConfig() != "" {
		return false
	}
	if _, ok := racineDeclaree(cheminDefinition(labelService)); ok {
		return false
	}
	return true
}

func ajouteQuitter() {
	mQuit := systray.AddMenuItem("Quitter", "Quitter Vécu")
	go func() {
		<-mQuit.ClickedCh
		systray.Quit()
	}()
}

// Deux journaux, et la séparation se fait par le MOTEUR.
//
// Le fichier avait deux écrivains sur le même chemin : le moteur en O_APPEND,
// et launchd qui y redirige stdout/stderr via le plist. Un seul des deux peut
// être tourné par renommage - l'autre garderait son descripteur ouvert sur
// l'inode renommé, et continuerait d'écrire dans un fichier devenu invisible,
// qui grossirait sans que personne le voie. Les panics, précisément.
//
// C'est donc le moteur qui déménage, et pas la redirection de launchd. La
// direction envisagée à l'interview était l'inverse ; elle a été écartée sur
// une vérification : `synchroniseService` n'est appelée que par l'instance
// MANUELLE (voir main), jamais par le service. Un changement de plist ne
// prendrait donc effet qu'au prochain lancement à la main de Vécu.app - donc
// PAS après une mise à jour automatique, qui passe par launchd. Déplacer le
// moteur atteint le même résultat sans toucher au plist, sans bootout d'un
// service en train de synchroniser, et prend effet dès le premier démarrage de
// la version.

// cheminJournal : ~/Library/Logs/vecu-app.log. Destination stdout/stderr du
// service launchd, et RIEN D'AUTRE depuis le 20/08 : des panics et les messages
// du runtime Go, donc un fichier minuscule. Le chemin est INCHANGÉ, c'est ce qui
// permet de ne pas toucher au plist.
func cheminJournal() string { return dansLesLogs("vecu-app.log") }

// cheminJournalSync : ~/Library/Logs/vecu-sync.log. Propriété du moteur, reçoit
// tout ce que `logf` écrit, et lui seul est tourné par taille.
func cheminJournalSync() string { return dansLesLogs("vecu-sync.log") }

func dansLesLogs(nom string) string { return filepath.Join(sync.DossierJournaux(), nom) }

// ouvreJournal : le moteur logue via logf, dans SON journal, tourné par taille.
// Repli sur stderr en cas d'échec - stderr étant vecu-app.log sous launchd,
// rien n'est perdu.
func ouvreJournal() io.Writer {
	j, err := ouvreJournalTournant(cheminJournalSync(), seuilJournal, journauxRetenus)
	if err != nil {
		return os.Stderr
	}
	return j
}
