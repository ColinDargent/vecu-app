package main

import (
	"github.com/colindargent/vecu/client/sync"
	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	vecusync "github.com/colindargent/vecu/client/sync"
)

// maisonJetable isole HOME : `racineAppConfig` et `cheminPlist` passent tous les
// deux par `os.UserHomeDir()`. Sans ça, le test lirait l'installation réelle du
// poste et son résultat dépendrait de qui le lance.
func maisonJetable(t *testing.T) string {
	t.Helper()
	maison := t.TempDir()
	t.Setenv("HOME", maison)
	// os.UserHomeDir lit %USERPROFILE% sur Windows et non HOME : sans cette
	// ligne, le test lisait le VRAI dossier personnel. Mesuré en CI le 06/09.
	t.Setenv("USERPROFILE", maison)
	// Sur Windows, os.UserConfigDir lit %AppData% et non HOME : sans ces deux
	// lignes, les tests écrivaient dans le VRAI profil de l'utilisateur et se
	// contaminaient entre eux. Mesuré en CI le 06/09.
	t.Setenv("AppData", filepath.Join(maison, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(maison, "AppData", "Local"))
	t.Setenv("VECU_DIR", "")
	return maison
}

// Les trois sources, une par une. La quatrième ligne de `resoudRacine` - le
// repli sur ~/second-brain - ne doit JAMAIS décider qu'un poste est installé :
// c'est un nom hérité de l'époque à deux, et l'inventer chez un tiers ferait
// apparaître un dossier que personne n'a demandé.
func TestPremierLancement_LesTroisSources(t *testing.T) {
	t.Run("rien nulle part : c'est un premier lancement", func(t *testing.T) {
		maisonJetable(t)
		if !premierLancement() {
			t.Fatal("aucune source ne répond, l'accueil doit s'ouvrir")
		}
	})

	t.Run("VECU_DIR posé : poste déjà cadré", func(t *testing.T) {
		maisonJetable(t)
		t.Setenv("VECU_DIR", t.TempDir())
		if premierLancement() {
			t.Fatal("VECU_DIR désigne une racine : ce n'est pas un premier lancement")
		}
	})

	t.Run("app.json présent : poste installé", func(t *testing.T) {
		maisonJetable(t)
		// Chemin DÉRIVÉ du produit : « ~/Library/Application Support/Vecu » sur
		// macOS, « %AppData%\Vecu » sur Windows. Écrit en dur, ce test posait
		// app.json là où le produit ne le cherche pas sur Windows, et concluait
		// donc à un premier lancement sur un poste installé.
		p := vecusync.DossierApplication()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "app.json"), []byte(`{"racine":"/tmp/ailleurs"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if premierLancement() {
			t.Fatal("app.json donne la racine : ce n'est pas un premier lancement")
		}
	})

	t.Run("plist seul, sans app.json : le poste venu de l'ancien daemon", func(t *testing.T) {
		// Un plist launchd : il n'y en a pas sur Windows, où la racine déclarée se
		// lit dans le magasin du Planificateur de tâches. Le pendant Windows de
		// cette lecture est couvert par service_windows_test.go.
		if runtime.GOOS == "windows" {
			t.Skip("plist launchd : concept macOS")
		}
		maison := maisonJetable(t)
		agents := filepath.Join(maison, "Library", "LaunchAgents")
		if err := os.MkdirAll(agents, 0o755); err != nil {
			t.Fatal(err)
		}
		plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + labelService + `</string>
<key>ProgramArguments</key><array>
<string>/opt/homebrew/bin/vecu</string><string>start</string><string>--dir</string><string>/Users/quelquun/notes</string>
</array></dict></plist>`
		if err := os.WriteFile(filepath.Join(agents, labelService+".plist"), []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
		if premierLancement() {
			t.Fatal("un poste migré depuis le daemon CLI a une racine valide : l'accueil ne doit pas s'ouvrir")
		}
	})
}

// La question « sait-on où travailler ? » et la question « où travaille-t-on ? »
// doivent avoir la même réponse. Si `premierLancement` rend faux, `resoudRacine`
// doit rendre autre chose que le repli, sans quoi l'app travaillerait dans un
// dossier que personne n'a désigné.
func TestPremierLancement_EtResoudRacineSontDAccord(t *testing.T) {
	maison := maisonJetable(t)
	repli := filepath.Join(maison, "second-brain")

	if !premierLancement() {
		t.Fatal("prérequis : rien n'est installé")
	}
	if got := resoudRacine(); got != repli {
		t.Fatalf("sans source, le repli reste %q, or resoudRacine rend %q", repli, got)
	}

	// Une source apparaît : les deux basculent ensemble.
	ailleurs := t.TempDir()
	t.Setenv("VECU_DIR", ailleurs)
	if premierLancement() {
		t.Fatal("une source répond : ce n'est plus un premier lancement")
	}
	if got := resoudRacine(); got != ailleurs {
		t.Fatalf("resoudRacine devrait suivre la source : %q != %q", got, ailleurs)
	}
}

// serveurAccueil : un vrai serveur avec un compte, pour éprouver appliqueAccueil
// sans dialogue.
func serveurAccueil(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("db : %v", err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatalf("store : %v", err)
	}
	if _, err := database.CreateUser("colin", "motdepasse123", perms.Ecriture, true); err != nil {
		t.Fatalf("compte : %v", err)
	}
	srv := httptest.NewServer((&api.Server{DB: database, Store: store}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// Le parcours nominal écrit les trois choses, et rien d'autre.
func TestAppliqueAccueil_Nominal(t *testing.T) {
	maison := maisonJetable(t)
	url := serveurAccueil(t)
	racine := filepath.Join(maison, "Documents", "Vécu")

	cfg, err := appliqueAccueil(reglagesAccueil{
		Serveur: url, Compte: "colin", MotDePasse: "motdepasse123", Racine: racine, Claude: true,
	})
	if err != nil {
		t.Fatalf("appliqueAccueil : %v", err)
	}
	if cfg.Username != "colin" || cfg.Token == "" {
		t.Fatalf("configuration incomplète : %+v", cfg)
	}
	relu, err := sync.LoadConfig(racine)
	if err != nil {
		t.Fatalf("la configuration doit être lisible dans la racine : %v", err)
	}
	if relu.Server != url || relu.Username != "colin" || relu.Token == "" {
		t.Fatalf("configuration mal écrite : %+v", relu)
	}
	if !slices.Contains(relu.Projections, "claude") {
		t.Errorf("la projection demandée n'a pas été écrite : %v", relu.Projections)
	}
	// Et la racine est mémorisée, donc le prochain démarrage ne rouvrira pas
	// l'accueil.
	if got := racineAppConfig(); got != racine {
		t.Errorf("racine mémorisée = %q, attendu %q", got, racine)
	}
	if premierLancement() {
		t.Error("après l'accueil, ce n'est plus un premier lancement")
	}
}

// LE test de la promesse : un identifiant refusé ne laisse RIEN derrière lui.
// Ni dossier, ni configuration, ni racine mémorisée.
func TestAppliqueAccueil_RefusNeLaisseRien(t *testing.T) {
	maison := maisonJetable(t)
	url := serveurAccueil(t)
	racine := filepath.Join(maison, "Documents", "Vécu")

	_, err := appliqueAccueil(reglagesAccueil{
		Serveur: url, Compte: "colin", MotDePasse: "mauvais-mot-de-passe", Racine: racine,
	})
	if err == nil {
		t.Fatal("un mot de passe faux doit échouer")
	}
	if !estRefusDIdentifiants(err) {
		t.Errorf("le refus doit être reconnu comme tel, pour reposer la question : %v", err)
	}
	if _, err := os.Stat(racine); !os.IsNotExist(err) {
		t.Errorf("un dossier a été créé malgré le refus : %v", err)
	}
	if got := racineAppConfig(); got != "" {
		t.Errorf("une racine a été mémorisée malgré le refus : %q", got)
	}
	if !premierLancement() {
		t.Error("après un refus, ce doit toujours être un premier lancement")
	}
}

// Un serveur injoignable n'est PAS un refus d'identifiants : reposer le mot de
// passe n'y changerait rien, et le faire ferait taper trois fois pour rien.
func TestEstRefusDIdentifiants_DistingueLeReseau(t *testing.T) {
	maison := maisonJetable(t)
	_, err := appliqueAccueil(reglagesAccueil{
		Serveur: "http://127.0.0.1:1", Compte: "colin", MotDePasse: "x",
		Racine: filepath.Join(maison, "r"),
	})
	if err == nil {
		t.Fatal("un serveur injoignable doit échouer")
	}
	if estRefusDIdentifiants(err) {
		t.Errorf("une panne réseau prise pour un mot de passe faux : %v", err)
	}
}
