package sync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

func configDeTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: "https://exemple", Username: "colin", Token: "jeton"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestMonteEspaceEcritLaDecision : le geste du parcours A, réduit à ce qu'il
// laisse sur le disque. L'intention d'adoption DOIT être persistée : le service
// redémarre juste après, et un drapeau en mémoire ne survivrait pas.
func TestMonteEspaceEcritLaDecision(t *testing.T) {
	dir := configDeTest(t)
	cible := filepath.Join(t.TempDir(), "Clients 2026")
	if err := MonteEspace(dir, "clients-2026", cible, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Montages["clients-2026"] != cible {
		t.Errorf("montage non écrit : %v", cfg.Montages)
	}
	if !slices.Contains(cfg.Adoptes, "clients-2026") {
		t.Errorf("intention d'adoption non persistée : %v", cfg.Adoptes)
	}
	// Le reste de la configuration survit : un token perdu ici déconnecterait
	// le poste au redémarrage qui suit immédiatement le geste.
	if cfg.Token != "jeton" || cfg.Server != "https://exemple" {
		t.Errorf("le reste de la config a été écrasé : %+v", cfg)
	}
	// Rejouer le même montage ne doit ni échouer ni dupliquer l'intention.
	if err := MonteEspace(dir, "clients-2026", cible, true); err != nil {
		t.Fatalf("montage idempotent refusé : %v", err)
	}
	cfg, _ = LoadConfig(dir)
	if len(cfg.Adoptes) != 1 {
		t.Errorf("intention dupliquée : %v", cfg.Adoptes)
	}
}

// TestMonteEspaceRefuseDeDeplacer : un espace déjà monté ailleurs ne se déplace
// pas d'un changement de table. Les fichiers resteraient à l'ancien endroit et
// le cycle suivant les lirait comme des suppressions.
func TestMonteEspaceRefuseDeDeplacer(t *testing.T) {
	dir := configDeTest(t)
	un, deux := filepath.Join(t.TempDir(), "un"), filepath.Join(t.TempDir(), "deux")
	if err := MonteEspace(dir, "equipe", un, false); err != nil {
		t.Fatal(err)
	}
	err := MonteEspace(dir, "equipe", deux, false)
	if err == nil {
		t.Fatal("déplacement accepté en silence")
	}
	if !strings.Contains(err.Error(), un) {
		t.Errorf("le refus ne dit pas où l'espace est monté : %v", err)
	}
	cfg, _ := LoadConfig(dir)
	if cfg.Montages["equipe"] != un {
		t.Errorf("la table a bougé malgré le refus : %v", cfg.Montages)
	}
}

// TestMonterLeveLExclusion : refuser puis accepter est un changement d'avis
// ordinaire. Une exclusion laissée en place ferait un montage inerte.
func TestMonterLeveLExclusion(t *testing.T) {
	dir := configDeTest(t)
	if err := RefuseEspace(dir, "equipe"); err != nil {
		t.Fatal(err)
	}
	if err := RefuseEspace(dir, "equipe"); err != nil { // deux refus, une entrée
		t.Fatal(err)
	}
	cfg, _ := LoadConfig(dir)
	if len(cfg.Exclus) != 1 {
		t.Fatalf("exclusion dupliquée : %v", cfg.Exclus)
	}
	if err := MonteEspace(dir, "equipe", filepath.Join(t.TempDir(), "equipe"), false); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadConfig(dir)
	if slices.Contains(cfg.Exclus, "equipe") {
		t.Errorf("l'exclusion a survécu au montage : %v", cfg.Exclus)
	}
}

// TestMonteEspaceRefuseUnCheminRelatif : la table porte des chemins absolus.
// Un chemin relatif s'y résoudrait depuis le dossier courant du process, qui
// n'est pas celui de la personne.
func TestMonteEspaceRefuseUnCheminRelatif(t *testing.T) {
	dir := configDeTest(t)
	if err := MonteEspace(dir, "equipe", "Documents/Equipe", false); err == nil {
		t.Error("chemin relatif accepté")
	}
}

// TestAdoptesSurvitAuChargement : la boucle complète, parce que c'est elle qui
// porte la décision du 21/08 - l'intention doit traverser un redémarrage.
func TestAdoptesSurvitAuChargement(t *testing.T) {
	dir := configDeTest(t)
	cible := filepath.Join(t.TempDir(), "equipe")
	if err := MonteEspace(dir, "equipe", cible, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{}
	e.Importer(cfg.Adoptes)
	if !e.importer["equipe"] {
		t.Error("l'intention d'adoption n'arrive pas au moteur après rechargement")
	}
}

// TestParcoursADeBoutEnBout : le critère d'acceptation central du lot, moins
// les dialogues.
//
// Partager un dossier qui existe déjà, hors de la racine, sans qu'il bouge.
// Trois preuves, et il en faut trois : le dossier est toujours au même endroit
// avec le même contenu, le serveur porte les fichiers, et l'espace est bien
// monté là où la personne l'a choisi et pas sous la racine.
func TestParcoursADeBoutEnBout(t *testing.T) {
	url, token, _, _ := serveurAvecMembre(t)

	racine := t.TempDir()
	if err := SaveConfig(racine, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}

	// Le dossier de la personne, ailleurs sur le disque, déjà plein.
	ailleurs := filepath.Join(t.TempDir(), "Clients 2026")
	if err := os.MkdirAll(filepath.Join(ailleurs, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, contenu := range map[string]string{
		"lisezmoi.md":      "le dossier de l'équipe\n",
		"notes/reunion.md": "compte rendu\n",
	} {
		if err := os.WriteFile(filepath.Join(ailleurs, filepath.FromSlash(rel)), []byte(contenu), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 1. L'app crée l'espace côté serveur, avec un libellé humain.
	cree, err := NewHTTPClient(url, token).CreerEspace("Clients 2026")
	if err != nil {
		t.Fatalf("création de l'espace : %v", err)
	}
	if cree.Nom == "" {
		t.Fatal("le serveur n'a pas rendu de nom technique")
	}

	// 2. L'app inscrit la décision : où il vit ici, et qu'il faut l'adopter.
	if err := MonteEspace(racine, cree.Nom, ailleurs, true); err != nil {
		t.Fatalf("montage : %v", err)
	}

	// 3. Le service redémarre. C'est ce que fait ce NewEngine : il relit la
	//    config, reprend les verrous, et retrouve l'intention d'adoption.
	e, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatalf("moteur après montage : %v", err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e.Close() })
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("premier cycle : %v", err)
	}

	// PREUVE 1 : le dossier n'a pas bougé, et son contenu est intact.
	for rel, attendu := range map[string]string{
		"lisezmoi.md":      "le dossier de l'équipe\n",
		"notes/reunion.md": "compte rendu\n",
	} {
		b, err := os.ReadFile(filepath.Join(ailleurs, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("le fichier a disparu de son dossier d'origine : %v", err)
		}
		if string(b) != attendu {
			t.Errorf("%s a changé de contenu : %q", rel, b)
		}
	}

	// PREUVE 2 : le serveur porte les fichiers, sous le nom technique retenu.
	chemins, err := NewHTTPClient(url, token).Tree()
	if err != nil {
		t.Fatal(err)
	}
	for _, attendu := range []string{cree.Nom + "/lisezmoi.md", cree.Nom + "/notes/reunion.md"} {
		if !slices.Contains(chemins, attendu) {
			t.Errorf("le serveur ne porte pas %s : %v", attendu, chemins)
		}
	}

	// PREUVE 3 : l'espace est monté là où la personne l'a choisi. Un montage qui
	// retomberait sur « racine + nom » aurait recopié le dossier ailleurs, ce
	// que le parcours promet précisément de ne pas faire.
	if got := e.racineEspace(cree.Nom); got != ailleurs {
		t.Errorf("espace monté sur %s au lieu de %s", got, ailleurs)
	}
	if _, err := os.Stat(filepath.Join(racine, cree.Nom)); err == nil {
		t.Errorf("un dossier a été créé sous la racine (%s) alors que l'espace vit ailleurs", filepath.Join(racine, cree.Nom))
	}
}

// TestSansAdoptionLeDossierNestPasEnvoye : le pendant du précédent, et la
// garantie qui rend le geste sûr. Sans intention écrite, un dossier local déjà
// plein n'est PAS adopté en silence.
func TestSansAdoptionLeDossierNestPasEnvoye(t *testing.T) {
	url, token, _, _ := serveurAvecMembre(t)
	racine := t.TempDir()
	if err := SaveConfig(racine, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	ailleurs := filepath.Join(t.TempDir(), "perso")
	if err := os.MkdirAll(ailleurs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ailleurs, "prive.md"), []byte("à moi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cree, err := NewHTTPClient(url, token).CreerEspace("perso")
	if err != nil {
		t.Fatal(err)
	}
	if err := MonteEspace(racine, cree.Nom, ailleurs, false); err != nil { // adopte = false
		t.Fatal(err)
	}
	e, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e.Close() })
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle : %v", err)
	}
	chemins, err := NewHTTPClient(url, token).Tree()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(chemins, cree.Nom+"/prive.md") {
		t.Error("un dossier local a été envoyé sans intention d'adoption")
	}
}

// TestParcoursBUnEspaceNouveauSePropose : la promesse du parcours B. Une fois
// que ce poste a monté quelque chose, un accès accordé plus tard fait apparaître
// une PROPOSITION, jamais un dossier surgi sans prévenir.
func TestParcoursBUnEspaceNouveauSePropose(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	racine := t.TempDir()
	if err := SaveConfig(racine, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e.Close() })
	// L'amorçage : ce poste n'a rien, on lui accorde « shared », il descend.
	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(racine, "shared")); err != nil {
		t.Fatalf("prérequis : l'amorçage aurait dû descendre « shared » : %v", err)
	}
	if p := e.Propositions(); len(p) != 0 {
		t.Errorf("l'amorçage ne doit rien proposer, il descend : %v", p)
	}

	// Un accès accordé APRÈS. Ce poste a déjà un dossier : c'est un événement.
	if err := database.SetPermission(membreID, "prive", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	props := e.Propositions()
	if len(props) != 1 || props[0].Nom != "prive" {
		t.Fatalf("l'espace accordé n'est pas proposé : %v", props)
	}
	if props[0].Ecriture {
		t.Error("un espace en lecture seule est annoncé comme modifiable")
	}
	if _, err := os.Stat(filepath.Join(racine, "prive")); err == nil {
		t.Error("le dossier est apparu sans que personne ne l'ait demandé")
	}

	// Le geste : la personne choisit où le poser. Le service redémarre, donc un
	// moteur neuf, exactement comme en vrai.
	ailleurs := filepath.Join(t.TempDir(), "Archives")
	if err := MonteEspace(racine, "prive", ailleurs, false); err != nil {
		t.Fatal(err)
	}
	// Le service redémarre : l'ancien moteur relâche ses verrous, un neuf les
	// reprend et relit la table. Sans ce Close, `NewEngine` refuse - et c'est le
	// verrou qui fait son travail, pas un défaut du test.
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e2.Close() })
	if err := e2.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ailleurs, "secret.md")); err != nil {
		t.Errorf("l'espace rejoint n'est pas descendu à l'endroit choisi : %v", err)
	}
	if p := e2.Propositions(); len(p) != 0 {
		t.Errorf("l'espace rejoint est encore proposé : %v", p)
	}
}

// TestParcoursBLeRefusEstDurable : sans trace écrite, la proposition
// reviendrait au cycle suivant, puis au démarrage suivant, jusqu'à ce que la
// personne cède ou n'ouvre plus le menu.
func TestParcoursBLeRefusEstDurable(t *testing.T) {
	url, token, database, membreID := serveurAvecMembre(t)
	racine := t.TempDir()
	if err := SaveConfig(racine, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e.Close() })
	if err := database.SetPermission(membreID, "shared", perms.Ecriture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := database.SetPermission(membreID, "prive", perms.Lecture); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if len(e.Propositions()) != 1 {
		t.Fatalf("prérequis : une proposition attendue, %v", e.Propositions())
	}

	if err := RefuseEspace(racine, "prive"); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e2.Close() })
	if err := e2.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if p := e2.Propositions(); len(p) != 0 {
		t.Errorf("la proposition refusée est revenue : %v", p)
	}
	if _, err := os.Stat(filepath.Join(racine, "prive")); err == nil {
		t.Error("un espace refusé est descendu quand même")
	}
}

// TestDossierPropose : le nom du dossier qui apparaîtra sur le disque. Le
// libellé vient du serveur, donc de quelqu'un d'autre : il ne décide d'un nom
// de dossier que s'il en fait un acceptable.
func TestDossierPropose(t *testing.T) {
	for _, c := range []struct {
		libelle, nom, want string
	}{
		{"Clients 2026", "clients-2026", "Clients 2026"},
		{"", "equipe", "equipe"},
		{"../../etc", "equipe", "equipe"},
		{"notes/2026", "notes-2026", "notes-2026"},
		{".cache", "cache", "cache"},
		{"Réunions.app", "reunions", "reunions"},
		{"Équipe", "equipe", "Équipe"},
	} {
		if got := DossierPropose(Proposition{Nom: c.nom, Libelle: c.libelle}); got != c.want {
			t.Errorf("DossierPropose(%q, %q) = %q, want %q", c.libelle, c.nom, got, c.want)
		}
	}
}

// TestLeVecuignoreDUnEspaceEstLu : trouvé le 21/08 au premier partage réel.
//
// `SyncOnce` chargeait les motifs avec `LoadIgnores(e.dir)`, donc depuis la
// RACINE. Un dossier partagé qui vit ailleurs porte ses propres exclusions,
// écrites dedans, et personne ne les lisait : un `node_modules` est parti sur le
// serveur. Même famille que `sansLien` - une fonction qui travaille depuis la
// racine pendant que le contenu vit au point de montage.
//
// Deux moitiés, et il faut les deux : l'annonce doit compter le fichier comme
// écarté, et le cycle ne doit pas l'envoyer. Une seule des deux ferait mentir
// l'autre.
func TestLeVecuignoreDUnEspaceEstLu(t *testing.T) {
	url, token, _, _ := serveurAvecMembre(t)
	racine := t.TempDir()
	if err := SaveConfig(racine, Config{Server: url, Username: "achille", Token: token}); err != nil {
		t.Fatal(err)
	}
	ailleurs := filepath.Join(t.TempDir(), "projet")
	if err := os.MkdirAll(filepath.Join(ailleurs, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	ecris := func(rel, contenu string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ailleurs, filepath.FromSlash(rel)), []byte(contenu), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ecris(".vecuignore", "node_modules\n*.log\n")
	ecris("note.md", "à partager\n")
	ecris("debug.log", "pas à partager\n")
	ecris("node_modules/paquet.txt", "pas à partager non plus\n")

	e, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e.Close() })

	// MOITIÉ 1 : l'annonce, avant que l'espace n'existe.
	analyse := e.PreAnalyse(ailleurs)
	// `debug.log` est écarté et compté ; `node_modules` l'est en tant que
	// DOSSIER, donc son contenu n'est pas parcouru et ne compte pas - le choix
	// est celui de `compte`, et il est le bon : on ne descend pas dans un
	// dossier qu'on vient d'exclure.
	if analyse.Ignores != 1 {
		t.Errorf("l'annonce compte %d écarté(s), attendu 1 : les règles du dossier ne sont pas lues", analyse.Ignores)
	}
	if analyse.Fichiers != 2 { // note.md et .vecuignore
		t.Errorf("l'annonce compte %d fichier(s) à envoyer, attendu 2 (note.md, .vecuignore)", analyse.Fichiers)
	}

	// MOITIÉ 2 : le cycle.
	cree, err := NewHTTPClient(url, token).CreerEspace("projet")
	if err != nil {
		t.Fatal(err)
	}
	if err := MonteEspace(racine, cree.Nom, ailleurs, true); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2, err := NewEngine(racine, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	// Windows ne supprime pas un fichier ouvert : sans ce relâchement, le
	// nettoyage de t.TempDir() échoue sur .vecu/lock. Mesuré en CI le 06/09.
	t.Cleanup(func() { _ = e2.Close() })
	if err := e2.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	chemins, err := NewHTTPClient(url, token).Tree()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(chemins, cree.Nom+"/node_modules/paquet.txt") {
		t.Errorf("un fichier écarté par le .vecuignore du dossier est parti sur le serveur : %v", chemins)
	}
	if !slices.Contains(chemins, cree.Nom+"/note.md") {
		t.Errorf("le fichier ordinaire n'est pas parti : %v", chemins)
	}
}

// L'invariant du retrait : jamais `Detaches` sans `Exclus`. Détaché seul ferait
// un espace qui garde ses fichiers puis les redescend au cycle suivant ; exclu
// seul supprimerait les fichiers, ce que le geste promet justement de ne pas
// faire.
func TestRetireEspace_PoseLesDeuxListes(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: "https://exemple", Username: "colin", Token: "jeton"}); err != nil {
		t.Fatal(err)
	}
	if err := RetireEspace(dir, "clients"); err != nil {
		t.Fatalf("RetireEspace : %v", err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Exclus, "clients") {
		t.Fatal("sans Exclus, l'espace redescend au cycle suivant")
	}
	if !slices.Contains(cfg.Detaches, "clients") {
		t.Fatal("sans Detaches, le démontage supprimerait les fichiers")
	}
	// Idempotent : un second clic ne doit pas doubler les entrées.
	if err := RetireEspace(dir, "clients"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadConfig(dir)
	if len(cfg.Exclus) != 1 || len(cfg.Detaches) != 1 {
		t.Fatalf("entrées dupliquées : exclus=%v detaches=%v", cfg.Exclus, cfg.Detaches)
	}
}

// Remonter retire les DEUX listes. Un `Detaches` résiduel rendrait
// silencieusement non destructeur un futur démontage par retrait d'accès,
// c'est-à-dire qu'il laisserait sur le disque un contenu auquel la personne n'a
// plus droit.
func TestMonteEspace_RetireLesDeuxListes(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: "https://exemple", Username: "colin", Token: "jeton"}); err != nil {
		t.Fatal(err)
	}
	if err := RetireEspace(dir, "clients"); err != nil {
		t.Fatal(err)
	}
	if err := MonteEspace(dir, "clients", filepath.Join(dir, "Clients"), false); err != nil {
		t.Fatalf("MonteEspace : %v", err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cfg.Exclus, "clients") {
		t.Fatal("Exclus résiduel : le montage serait inerte")
	}
	if slices.Contains(cfg.Detaches, "clients") {
		t.Fatal("Detaches résiduel : un futur retrait d'accès ne supprimerait plus les fichiers")
	}
}

// Le champ traverse un aller-retour de sérialisation. `omitempty` sur une
// tranche vide est sans piège ici (contrairement à une map d'état) : absente et
// vide veulent dire la même chose, aucun espace n'est détaché.
func TestDetaches_AllerRetour(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Server: "https://x", Username: "u", Token: "t", Detaches: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Detaches, []string{"a", "b"}) {
		t.Fatalf("Detaches relu = %v", cfg.Detaches)
	}
}
