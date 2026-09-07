package api

// mcp_lecture_test.go : S2, et surtout sa batterie adversariale.
//
// Le critère de la spec n'est pas « les outils répondent », c'est « un jeton
// dont le compte n'a pas accès à un dossier ne le voit dans AUCUN des trois,
// y compris par un chemin absolu deviné, y compris quand le motif correspond au
// contenu d'un fichier invisible ». Le troisième cas est celui qu'on rate.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
	"github.com/colindargent/vecu/server/storage"
)

// Le mot que porte chaque fichier secret. Un seul mot, cherché par `chercher` :
// s'il ressort une seule fois, la fuite est démontrée.
const motSecret = "hippocampe"

// setupLecture : un vault avec des zones que `achille` ne doit pas voir.
func setupLecture(t *testing.T) (*httptest.Server, *db.DB, *storage.Store, map[string]int64, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.Init(filepath.Join(dir, "brain.git"))
	if err != nil {
		t.Fatal(err)
	}

	store.Write("public/guide.md", "# Guide\nun contenu banal\n", "colin")
	store.Write("public/notes.md", "des notes publiques\n", "colin")
	// Les trois zones interdites à achille, chacune fermée par un mécanisme
	// DIFFÉRENT : une exception restrictive, un dossier jamais ouvert, et la
	// zone skills.
	store.Write("clients/vdf/secret.md", "le mot de passe est "+motSecret+"\n", "colin")
	store.Write("prive/interne.md", "note interne : "+motSecret+"\n", "colin")
	store.Write(skills.DefaultRoot+"/blank-page/SKILL.md", "skill privé, "+motSecret+"\n", "colin")

	colin, _ := database.CreateUser("colin", "pw", perms.Ecriture, true)
	achille, _ := database.CreateUser("achille", "pw", perms.Invisible, false)
	database.SetPermission(achille.ID, "public", perms.Lecture)
	database.SetPermission(achille.ID, "clients", perms.Lecture)
	database.SetPermission(achille.ID, "clients/vdf", perms.Invisible) // surcharge restrictive
	store.Write("clients/note.md", "note client banale\n", "colin")

	// UNE RÈGLE POSÉE SUR UN FICHIER, et pas sur un dossier. `perms.Rule` le
	// permet explicitement (« un niveau posé sur un chemin, dossier OU
	// fichier »), et sans ce cas le banc ne distingue pas un filtre appliqué au
	// chemin d'un filtre appliqué à son dossier parent : une mutation
	// `filtre(path.Dir(p))` passait toute la batterie. Trou trouvé en revue
	// adversariale, et c'est exactement la classe DAR-164.
	store.Write("clients/salaires.md", "grille des salaires : "+motSecret+"\n", "colin")
	database.SetPermission(achille.ID, "clients/salaires.md", perms.Invisible)

	ids := map[string]int64{"colin": colin.ID, "achille": achille.ID}
	jetons := map[string]string{}
	jetons["colin"], _ = database.IssueMCPToken(colin.ID, "banc", perms.Ecriture)
	jetons["achille"], _ = database.IssueMCPToken(achille.ID, "banc", perms.Lecture)

	srv := httptest.NewServer((&Server{DB: database, Store: store}).Handler())
	t.Cleanup(srv.Close)
	return srv, database, store, ids, jetons
}

// appelleOutil monte un `tools/call` conforme et rend (texte, isError).
func appelleOutil(t *testing.T, srv *httptest.Server, jeton, outil string, args map[string]any) (string, bool) {
	t.Helper()
	params := map[string]any{
		"name":      outil,
		"arguments": args,
		"_meta":     map[string]any{champMetaVersion: revisionMCP},
	}
	brut, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params,
	})
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(string(brut)))
	req.Header.Set("Authorization", "Bearer "+jeton)
	req.Header.Set("MCP-Protocol-Version", revisionMCP)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", outil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s : HTTP %d", outil, resp.StatusCode)
	}
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s : aucun result (%v)", outil, m)
	}
	if res["resultType"] != "complete" {
		t.Errorf("%s : resultType = %v", outil, res["resultType"])
	}
	isErr, _ := res["isError"].(bool)
	blocs, _ := res["content"].([]any)
	var sb strings.Builder
	for _, b := range blocs {
		if bm, ok := b.(map[string]any); ok {
			if txt, ok := bm["text"].(string); ok {
				sb.WriteString(txt)
			}
		}
	}
	return sb.String(), isErr
}

func TestMCPToolsListRendLeCatalogue(t *testing.T) {
	srv, _, _, _, jetons := setupLecture(t)

	resp := appelMCP(t, srv, jetons["colin"], "tools/list", nil, nil)
	res, _ := corpsMCP(t, resp)["result"].(map[string]any)
	outils, _ := res["tools"].([]any)
	if len(outils) != 5 {
		t.Fatalf("%d outils, attendu 5", len(outils))
	}
	var noms []string
	for _, o := range outils {
		om := o.(map[string]any)
		noms = append(noms, om["name"].(string))
		// La spec contraint le jeu de caractères des noms d'outils : lettres
		// ASCII, chiffres, `_`, `-`, `.`. Les descriptions, elles, sont en
		// français.
		for _, r := range om["name"].(string) {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
			if !ok {
				t.Errorf("nom d'outil hors du jeu autorisé : %q (caractère %q)", om["name"], r)
			}
		}
		if _, ok := om["inputSchema"].(map[string]any); !ok {
			t.Errorf("%v : inputSchema absent ou mal typé", om["name"])
		}
	}
	attendu := []string{"arborescence", "lire_fichier", "ecrire_fichier", "supprimer_fichier", "chercher"}
	for i, n := range attendu {
		if noms[i] != n {
			t.Errorf("outil %d = %q, attendu %q (l'ordre doit être déterministe)", i, noms[i], n)
		}
	}
}

func TestMCPLesTroisOutilsRepondentPourUnCompteQuiALeDroit(t *testing.T) {
	srv, _, _, _, jetons := setupLecture(t)

	txt, isErr := appelleOutil(t, srv, jetons["colin"], "arborescence", map[string]any{})
	if isErr || !strings.Contains(txt, "public/guide.md") {
		t.Errorf("arborescence : isError=%v txt=%q", isErr, txt)
	}
	txt, isErr = appelleOutil(t, srv, jetons["colin"], "lire_fichier", map[string]any{"chemin": "public/guide.md"})
	if isErr || !strings.Contains(txt, "un contenu banal") {
		t.Errorf("lire_fichier : isError=%v txt=%q", isErr, txt)
	}
	txt, isErr = appelleOutil(t, srv, jetons["colin"], "chercher", map[string]any{"motif": "banal"})
	if isErr || !strings.Contains(txt, "public/guide.md") {
		t.Errorf("chercher : isError=%v txt=%q", isErr, txt)
	}
}

// LA BATTERIE ADVERSARIALE. Un dossier invisible ne doit apparaître dans AUCUN
// des trois outils, par aucun chemin.
//
// Rejoue, par la porte MCP, les trous fermés par DAR-164 côté web : l'arbre
// partait alors d'un `Store.List("")` non filtré.
func TestMCPUnDossierInvisibleNApparaitDansAucunOutil(t *testing.T) {
	srv, _, _, _, jetons := setupLecture(t)
	jeton := jetons["achille"]

	interdits := []string{
		"clients/vdf/secret.md", // surcharge restrictive sous un dossier lisible
		// La règle posée sur LE FICHIER, dans un dossier par ailleurs lisible.
		// C'est elle qui distingue un filtre appliqué au chemin d'un filtre
		// appliqué au dossier parent - sans elle, une mutation
		// `filtre(path.Dir(p))` passait toute la batterie.
		"clients/salaires.md",
		"prive/interne.md",                          // dossier jamais ouvert
		skills.DefaultRoot + "/blank-page/SKILL.md", // zone skills
	}

	t.Run("arborescence", func(t *testing.T) {
		txt, isErr := appelleOutil(t, srv, jeton, "arborescence", map[string]any{})
		if isErr {
			t.Fatalf("arborescence en erreur : %s", txt)
		}
		// Le témoin : ce qu'il DOIT voir. Sans lui, un outil cassé qui ne rend
		// rien passerait ce test.
		if !strings.Contains(txt, "public/guide.md") || !strings.Contains(txt, "clients/note.md") {
			t.Fatalf("le périmètre autorisé n'est pas rendu : %q", txt)
		}
		for _, p := range interdits {
			if strings.Contains(txt, p) {
				t.Errorf("l'arborescence expose %q", p)
			}
		}
		// Ni le fichier, ni le dossier qui le contient.
		for _, d := range []string{"vdf", "prive", "blank-page"} {
			if strings.Contains(txt, d) {
				t.Errorf("l'arborescence laisse deviner le dossier %q : %q", d, txt)
			}
		}
	})

	t.Run("arborescence sur le préfixe interdit lui-même", func(t *testing.T) {
		for _, prefixe := range []string{"prive", "clients/vdf", skills.DefaultRoot} {
			txt, _ := appelleOutil(t, srv, jeton, "arborescence", map[string]any{"chemin": prefixe})
			if strings.Contains(txt, motSecret) || strings.Contains(txt, "secret.md") ||
				strings.Contains(txt, "interne.md") || strings.Contains(txt, "SKILL.md") {
				t.Errorf("arborescence(%q) fuit : %q", prefixe, txt)
			}
		}
	})

	t.Run("lire_fichier par chemin absolu deviné", func(t *testing.T) {
		for _, p := range interdits {
			txt, isErr := appelleOutil(t, srv, jeton, "lire_fichier", map[string]any{"chemin": p})
			if !isErr {
				t.Errorf("lire_fichier(%q) a réussi", p)
			}
			if strings.Contains(txt, motSecret) {
				t.Errorf("lire_fichier(%q) a fuité le contenu : %q", p, txt)
			}
		}
	})

	t.Run("lire_fichier : refus et inexistant se disent pareil", func(t *testing.T) {
		// LE BON COUPLE, et la première version ne l'avait pas : elle comparait
		// deux chemins sous `prive`, tous deux invisibles, donc tous deux dans
		// LA MÊME branche. La branche « permis mais absent » n'était jamais
		// atteinte, et changer le message de refus laissait le test vert.
		//
		// Ici : un chemin qui EXISTE et qu'on n'a pas le droit de lire, contre
		// un chemin qu'on aurait le droit de lire et qui n'existe pas. Si les
		// deux messages diffèrent, l'outil devient un détecteur d'existence.
		refuse, errRefuse := appelleOutil(t, srv, jeton, "lire_fichier",
			map[string]any{"chemin": "clients/salaires.md"})
		absent, errAbsent := appelleOutil(t, srv, jeton, "lire_fichier",
			map[string]any{"chemin": "public/nexiste-pas.md"})
		if !errRefuse || !errAbsent {
			t.Errorf("les deux devraient être en isError : refusé=%v absent=%v", errRefuse, errAbsent)
		}
		gabarit := func(s, chemin string) string { return strings.ReplaceAll(s, chemin, "<CHEMIN>") }
		if gabarit(refuse, "clients/salaires.md") != gabarit(absent, "public/nexiste-pas.md") {
			t.Errorf("deux messages distincts, donc un oracle d'existence :\n  interdit-mais-existant = %q\n  permis-mais-absent     = %q", refuse, absent)
		}
	})

	t.Run("chercher : le contenu d'un fichier invisible n'est jamais parcouru", func(t *testing.T) {
		// LE CAS QUE LA SPEC DÉSIGNE COMME CELUI QU'ON RATE. Le motif
		// correspond au contenu des trois fichiers interdits, et à rien d'autre.
		txt, _ := appelleOutil(t, srv, jeton, "chercher", map[string]any{"motif": motSecret})
		if strings.Contains(txt, motSecret) && !strings.Contains(txt, "Aucun résultat") {
			t.Errorf("chercher a fuité le contenu d'un fichier invisible : %q", txt)
		}
		for _, p := range interdits {
			if strings.Contains(txt, p) {
				t.Errorf("chercher expose le chemin %q : %q", p, txt)
			}
		}
		// Le témoin, indispensable : le même outil trouve bien dans le
		// périmètre autorisé. Sans lui, un `chercher` cassé passerait.
		ok, _ := appelleOutil(t, srv, jeton, "chercher", map[string]any{"motif": "banal"})
		if !strings.Contains(ok, "public/guide.md") {
			t.Errorf("chercher ne trouve rien dans le périmètre autorisé : %q", ok)
		}
	})

	t.Run("chercher sur le nom du dossier interdit", func(t *testing.T) {
		for _, motif := range []string{"vdf", "prive", "blank-page", "secret"} {
			txt, _ := appelleOutil(t, srv, jeton, "chercher", map[string]any{"motif": motif})
			for _, p := range interdits {
				if strings.Contains(txt, p) {
					t.Errorf("chercher(%q) expose %q", motif, p)
				}
			}
		}
	})
}

// Colin est ADMIN et lit tout le reste : la zone skills lui reste privée quand
// même. Aucune exemption admin n'est ajoutée par cette porte - c'est la
// propriété que le modèle créateur/privé (F3) tient partout ailleurs.
func TestMCPLaZoneSkillsResteFermeeMemeAUnAdmin(t *testing.T) {
	srv, database, _, ids, jetons := setupLecture(t)

	// Précondition : le skill n'a pas été revendiqué, donc le masque
	// `shared/skills = invisible` posé à la création du compte s'applique, admin
	// ou pas.
	rules, _ := database.Rules(ids["colin"])
	colin, _ := database.UserByID(ids["colin"])
	if !colin.IsAdmin {
		t.Fatal("précondition : colin doit être admin")
	}
	if lvl := perms.Effective(skills.DefaultRoot+"/blank-page/SKILL.md", colin.DefaultLevel, rules); lvl != perms.Invisible {
		t.Fatalf("précondition : le skill devrait être invisible pour colin, obtenu %v", lvl)
	}

	chemin := skills.DefaultRoot + "/blank-page/SKILL.md"
	txt, isErr := appelleOutil(t, srv, jetons["colin"], "lire_fichier", map[string]any{"chemin": chemin})
	if !isErr || strings.Contains(txt, motSecret) {
		t.Errorf("un admin a lu un skill privé par la porte MCP : isError=%v txt=%q", isErr, txt)
	}
	txt, _ = appelleOutil(t, srv, jetons["colin"], "arborescence", map[string]any{})
	if strings.Contains(txt, "blank-page") {
		t.Errorf("l'arborescence expose un skill privé à un admin : %q", txt)
	}
	txt, _ = appelleOutil(t, srv, jetons["colin"], "chercher", map[string]any{"motif": motSecret})
	if strings.Contains(txt, "blank-page") {
		t.Errorf("chercher expose un skill privé à un admin : %q", txt)
	}
}

// Le préfixe se compare PAR SEGMENT. Sinon « clients/v » attraperait
// « clients/vdf/… », donc un dossier voisin fermé.
func TestMCPLePrefixeNeDebordePasSurUnVoisin(t *testing.T) {
	srv, _, _, _, jetons := setupLecture(t)

	txt, _ := appelleOutil(t, srv, jetons["colin"], "arborescence", map[string]any{"chemin": "clients/v"})
	if strings.Contains(txt, "clients/vdf") {
		t.Errorf("le préfixe « clients/v » a débordé sur « clients/vdf » : %q", txt)
	}
	// Et le vrai préfixe fonctionne, lui.
	txt, _ = appelleOutil(t, srv, jetons["colin"], "arborescence", map[string]any{"chemin": "clients"})
	if !strings.Contains(txt, "clients/note.md") {
		t.Errorf("le préfixe « clients » ne rend rien : %q", txt)
	}
}

// Le plafond du jeton n'a pas d'effet sur la LECTURE - il ne soustrait qu'au
// niveau écriture. Un jeton `lecture` lit ce que son compte lit.
func TestMCPUnJetonLectureLitCeQueSonCompteLit(t *testing.T) {
	srv, database, _, ids, _ := setupLecture(t)

	jeton, _ := database.IssueMCPToken(ids["colin"], "lecture seule", perms.Lecture)
	txt, isErr := appelleOutil(t, srv, jeton, "lire_fichier", map[string]any{"chemin": "public/guide.md"})
	if isErr || !strings.Contains(txt, "un contenu banal") {
		t.Errorf("jeton lecture : isError=%v txt=%q", isErr, txt)
	}
}

// Un outil inconnu est une erreur de PROTOCOLE, pas un `isError` : le modèle ne
// peut pas s'en corriger, il a demandé ce qui n'existe pas.
func TestMCPOutilInconnuEstUneErreurDeProtocole(t *testing.T) {
	srv, _, _, _, jetons := setupLecture(t)

	params := map[string]any{
		"name": "outil_invente", "arguments": map[string]any{},
		"_meta": map[string]any{champMetaVersion: revisionMCP},
	}
	brut, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(string(brut)))
	req.Header.Set("Authorization", "Bearer "+jetons["colin"])
	req.Header.Set("MCP-Protocol-Version", revisionMCP)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "outil_invente")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if code := codeErreurMCP(t, resp); code != codeParamsInvalides {
		t.Errorf("outil inconnu : code %d, attendu %d", code, codeParamsInvalides)
	}
}

// Un contenu où `strings.ToLower` ALLONGE la chaîne en octets faisait paniquer
// le serveur - pas d'erreur, pas de réponse : la connexion tombait.
//
// « Ⱥ » (U+023A) pèse 2 octets, sa minuscule « ⱥ » (U+2C65) en pèse 3. La
// première version cherchait l'index dans la version minuscule puis découpait
// l'ORIGINAL à cet index, donc hors bornes. Un seul fichier de ce genre dans le
// périmètre lisible cassait `chercher` pour tout jeton qui le voit.
func TestMCPChercherNePaniquePasSurUnContenuQuiSAllongeEnMinuscules(t *testing.T) {
	srv, _, store, _, jetons := setupLecture(t)

	// Assez de caractères piégeux pour que l'écart d'octets dépasse largement
	// la position de la cible.
	appelleOutil(t, srv, jetons["colin"], "arborescence", map[string]any{}) // réchauffe

	for _, cas := range []struct{ nom, contenu, motif string }{
		{"majuscule qui s'allonge", strings.Repeat("Ⱥ", 300) + " cible", "cible"},
		{"le motif lui-même", strings.Repeat("Ⱦ", 200), "Ⱦ"},
		{"casse mélangée et accents", "ÉLÉPHANT et Éléphant et éléphant", "éléphant"},
	} {
		t.Run(cas.nom, func(t *testing.T) {
			// Le contenu est déposé par la porte HTTP existante, comme un vrai
			// fichier - pas injecté dans le store à la main.
			ecritFixture(t, store, "public/piege.md", cas.contenu)
			txt, isErr := appelleOutil(t, srv, jetons["colin"], "chercher", map[string]any{"motif": cas.motif})
			if isErr {
				t.Fatalf("chercher en erreur : %s", txt)
			}
			if !strings.Contains(txt, "public/piege.md") {
				t.Errorf("le fichier piégé n'est pas trouvé : %q", txt)
			}
			// Et l'extrait rendu est de l'UTF-8 valide : découper sur les
			// octets fabriquerait un caractère tronqué.
			if !utf8.ValidString(txt) {
				t.Errorf("l'extrait n'est pas de l'UTF-8 valide : %q", txt)
			}
		})
	}
}

// La casse est repliée dans les deux sens, en Unicode : chercher « éléphant »
// trouve « ÉLÉPHANT ». Le vault est en français, c'est le cas courant.
func TestMCPChercherReplieLaCasseUnicode(t *testing.T) {
	srv, _, store, _, jetons := setupLecture(t)
	ecritFixture(t, store, "public/casse.md", "Le DOSSIER ÉTÉ 2026\n")

	for _, motif := range []string{"été", "ÉTÉ", "Été", "dossier"} {
		txt, _ := appelleOutil(t, srv, jetons["colin"], "chercher", map[string]any{"motif": motif})
		if !strings.Contains(txt, "public/casse.md") {
			t.Errorf("chercher(%q) ne trouve pas le fichier : %q", motif, txt)
		}
	}
}

// Le motif est cherché LITTÉRALEMENT. Sans échappement, un motif venu du modèle
// serait compilé comme une expression régulière : « . » trouverait tout, et un
// motif malformé ferait échouer l'outil au lieu de ne rien trouver.
func TestMCPChercherTraiteLeMotifLitteralement(t *testing.T) {
	srv, _, store, _, jetons := setupLecture(t)
	ecritFixture(t, store, "public/litteral.md", "un point . et une etoile *\n")

	// Un motif qui, interprété, correspondrait à tout.
	txt, isErr := appelleOutil(t, srv, jetons["colin"], "chercher", map[string]any{"motif": ".*"})
	if isErr {
		t.Fatalf("chercher en erreur : %s", txt)
	}
	if strings.Contains(txt, "public/guide.md") {
		t.Errorf("« .* » a été interprété comme une expression : %q", txt)
	}
	// Un motif syntaxiquement invalide en regexp ne casse rien.
	if _, isErr := appelleOutil(t, srv, jetons["colin"], "chercher", map[string]any{"motif": "[("}); isErr {
		t.Error("un motif « [( » a fait échouer l'outil au lieu de ne rien trouver")
	}
}

// Un fichier explicitement ouvert DANS un dossier invisible reste lisible, et
// son chemin apparaît - donc il révèle le nom du dossier.
//
// C'est le comportement correct d'un modèle de droits par chemin (une règle
// peut ouvrir aussi bien que fermer, à n'importe quelle profondeur), et il est
// figé ici parce qu'il n'est pas évident : le lire dans un test vaut mieux que
// le redécouvrir devant un client.
func TestMCPUnFichierOuvertDansUnDossierFermeResteLisible(t *testing.T) {
	srv, database, store, ids, jetons := setupLecture(t)

	ecritFixture(t, store, "prive/partage.md", "ouvert exprès\n")
	if err := database.SetPermission(ids["achille"], "prive/partage.md", perms.Lecture); err != nil {
		t.Fatal(err)
	}

	txt, isErr := appelleOutil(t, srv, jetons["achille"], "lire_fichier", map[string]any{"chemin": "prive/partage.md"})
	if isErr || !strings.Contains(txt, "ouvert exprès") {
		t.Errorf("le fichier explicitement ouvert n'est pas lisible : isError=%v txt=%q", isErr, txt)
	}
	arbo, _ := appelleOutil(t, srv, jetons["achille"], "arborescence", map[string]any{})
	if !strings.Contains(arbo, "prive/partage.md") {
		t.Errorf("l'arborescence ne rend pas le fichier ouvert : %q", arbo)
	}
	// Mais le VOISIN fermé du même dossier reste invisible.
	if strings.Contains(arbo, "prive/interne.md") {
		t.Errorf("l'arborescence expose le voisin fermé : %q", arbo)
	}
}

// ecritFixture dépose un fichier dans le dépôt du harnais.
//
// Directement dans le store, et pas par la porte HTTP : le PUT de l'API exige
// un jeton d'APPAREIL, que ces tests n'ont pas - ils portent des jetons MCP,
// qui sont un magasin distinct. Ce que ces tests exercent est la LECTURE.
func ecritFixture(t *testing.T, store *storage.Store, chemin, contenu string) {
	t.Helper()
	if _, err := store.Write(chemin, contenu, "colin"); err != nil {
		t.Fatalf("fixture %q : %v", chemin, err)
	}
}
