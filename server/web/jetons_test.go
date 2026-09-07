package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/perms"
)

// poste joue un POST authentifié et rend l'enregistreur.
func poste(t *testing.T, h http.Handler, c *http.Cookie, chemin string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", chemin, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func recupere(t *testing.T, h http.Handler, c *http.Cookie, chemin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", chemin, nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// LE CYCLE COMPLET : créer, nommer, choisir le plafond, voir le dernier usage,
// révoquer.
func TestJetonsCreerVoirRevoquer(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	colin, _ := s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	c := login(t, h, "colin", "motdepasse")

	// Au départ, aucun jeton.
	rec := recupere(t, h, c, "/admin/jetons")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/jetons : %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Aucun jeton") {
		t.Errorf("l'écran ne dit pas qu'il n'y a aucun jeton")
	}

	// Création.
	rec = poste(t, h, c, "/admin/jetons", url.Values{
		"libelle": {"meeting-processor"}, "plafond": {"ecriture"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("création : %d (%s)", rec.Code, rec.Body.String())
	}
	corps := rec.Body.String()
	if !strings.Contains(corps, "meeting-processor") {
		t.Error("le libellé n'apparaît pas")
	}
	if !strings.Contains(corps, "ne sera plus jamais affiché") {
		t.Error("l'écran ne prévient pas que le jeton ne sera plus affiché")
	}
	clair := extraitJeton(t, corps)

	// Il fonctionne réellement : c'est un jeton MCP résoluble.
	if _, err := s.DB.PorteurParJetonMCP(clair); err != nil {
		t.Fatalf("le jeton affiché n'est pas résoluble : %v", err)
	}

	// ET IL N'EST JAMAIS RÉAFFICHÉ. Le serveur n'en garde qu'une empreinte.
	rec = recupere(t, h, c, "/admin/jetons")
	if strings.Contains(rec.Body.String(), clair) {
		t.Error("le jeton en clair est réaffiché après rechargement")
	}
	if !strings.Contains(rec.Body.String(), "meeting-processor") {
		t.Error("le jeton n'apparaît plus dans la liste")
	}
	if !strings.Contains(rec.Body.String(), "jamais servi") {
		t.Error("un jeton neuf devrait se dire « jamais servi »")
	}

	// Révocation.
	liste, _ := s.DB.ListJetonsMCP(colin.ID)
	rec = poste(t, h, c, "/admin/jetons/revoquer", url.Values{"id": {itoa(liste[0].ID)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("révocation : %d (%s)", rec.Code, rec.Body.String())
	}
	rec = recupere(t, h, c, "/admin/jetons")
	if !strings.Contains(rec.Body.String(), "révoqué le") {
		t.Errorf("l'écran ne montre pas le jeton comme révoqué : %s", rec.Body.String())
	}
}

// LA RÉVOCATION COUPE AU PREMIER APPEL SUIVANT, mesuré et pas supposé.
func TestRevocationCoupeAuPremierAppelSuivant(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	u, _ := s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	c := login(t, h, "colin", "motdepasse")

	rec := poste(t, h, c, "/admin/jetons", url.Values{
		"libelle": {"à couper"}, "plafond": {"lecture"}})
	clair := extraitJeton(t, rec.Body.String())

	// AVANT : il résout.
	if _, err := s.DB.PorteurParJetonMCP(clair); err != nil {
		t.Fatalf("avant révocation, le jeton devrait résoudre : %v", err)
	}

	liste, _ := s.DB.ListJetonsMCP(u.ID)
	if rec := poste(t, h, c, "/admin/jetons/revoquer", url.Values{"id": {itoa(liste[0].ID)}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("révocation : %d", rec.Code)
	}

	// APRÈS, au PREMIER appel : il ne résout plus. Pas de cache, pas de délai.
	if p, err := s.DB.PorteurParJetonMCP(clair); err == nil {
		t.Errorf("le jeton résout encore après révocation : %+v", p)
	}
}

// LA GARDE DE PROPRIÉTÉ : on ne révoque pas le jeton d'un autre.
//
// Les identifiants sont séquentiels, donc sans cette garde n'importe quel
// compte connecté coupe l'accès de n'importe qui en devinant un entier.
func TestOnNeRevoquePasLeJetonDunAutre(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	colin, _ := s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	s.DB.CreateUser("achille", "motdepasse", perms.Lecture, false)

	// Colin crée un jeton.
	cColin := login(t, h, "colin", "motdepasse")
	rec := poste(t, h, cColin, "/admin/jetons", url.Values{
		"libelle": {"à colin"}, "plafond": {"ecriture"}})
	clair := extraitJeton(t, rec.Body.String())
	liste, _ := s.DB.ListJetonsMCP(colin.ID)
	idDeColin := liste[0].ID

	// Achille essaie de le révoquer.
	cAchille := login(t, h, "achille", "motdepasse")
	rec = poste(t, h, cAchille, "/admin/jetons/revoquer", url.Values{"id": {itoa(idDeColin)}})
	if rec.Code == http.StatusSeeOther {
		t.Error("achille a révoqué le jeton de colin")
	}
	// Et le jeton de colin fonctionne toujours.
	if _, err := s.DB.PorteurParJetonMCP(clair); err != nil {
		t.Errorf("le jeton de colin a été coupé par achille : %v", err)
	}

	// Achille ne voit pas non plus le jeton de colin sur son écran.
	rec = recupere(t, h, cAchille, "/admin/jetons")
	if strings.Contains(rec.Body.String(), "à colin") {
		t.Error("achille voit le jeton de colin")
	}
}

// L'écran vit sous la session web existante : pas de nouveau modèle d'auth.
func TestJetonsExigeUneSession(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)

	for _, cas := range []struct{ methode, chemin string }{
		{"GET", "/admin/jetons"},
		{"POST", "/admin/jetons"},
		{"POST", "/admin/jetons/revoquer"},
	} {
		req := httptest.NewRequest(cas.methode, cas.chemin, strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s %s sans session : %d, attendu une redirection vers la connexion",
				cas.methode, cas.chemin, rec.Code)
		}
	}
}

// Un compte NON admin gère ses propres jetons : c'est le modèle retenu, et il
// est le seul qui n'accorde aucun droit nouveau à personne.
func TestUnCompteNonAdminGereSesJetons(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	achille, _ := s.DB.CreateUser("achille", "motdepasse", perms.Lecture, false)
	c := login(t, h, "achille", "motdepasse")

	rec := poste(t, h, c, "/admin/jetons", url.Values{
		"libelle": {"le mien"}, "plafond": {"lecture"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("un membre ne peut pas créer son jeton : %d", rec.Code)
	}
	liste, _ := s.DB.ListJetonsMCP(achille.ID)
	if len(liste) != 1 || liste[0].NiveauMax != perms.Lecture {
		t.Errorf("jeton mal créé : %+v", liste)
	}
}

// Les entrées invalides sont refusées, et l'écran le dit.
func TestJetonsRefuseLesEntreesInvalides(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	colin, _ := s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	c := login(t, h, "colin", "motdepasse")

	cas := []struct {
		nom  string
		form url.Values
	}{
		{"libellé vide", url.Values{"libelle": {""}, "plafond": {"lecture"}}},
		{"plafond invalide", url.Values{"libelle": {"x"}, "plafond": {"toutpuissant"}}},
		// `invisible` est un niveau valide, mais un jeton qui ne peut rien est
		// un réglage sans usage : refusé à la création plutôt qu'expliqué après.
		{"plafond invisible", url.Values{"libelle": {"x"}, "plafond": {"invisible"}}},
	}
	for _, cas := range cas {
		t.Run(cas.nom, func(t *testing.T) {
			rec := poste(t, h, c, "/admin/jetons", cas.form)
			if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "ne sera plus jamais affiché") {
				t.Errorf("un jeton a été créé sur une entrée invalide")
			}
		})
	}
	liste, _ := s.DB.ListJetonsMCP(colin.ID)
	if len(liste) != 0 {
		t.Errorf("%d jeton(s) créé(s) malgré des entrées invalides", len(liste))
	}
}

// extraitJeton lit le clair dans le <pre> de la page de création.
func extraitJeton(t *testing.T, corps string) string {
	t.Helper()
	// Le premier <pre> porte le jeton nu ; le second porte la commande.
	m := regexp.MustCompile(`(?s)<pre>([A-Za-z0-9_-]{40,})</pre>`).FindStringSubmatch(corps)
	if m == nil {
		t.Fatalf("aucun jeton trouvé dans la page :\n%s", corps)
	}
	return m[1]
}

// Les dates se lisent. La base stocke du RFC3339 UTC, ce qui est bon pour
// trier et illisible pour un humain - et le serveur tourne en UTC alors que
// ceux qui lisent l'écran n'y sont pas.
func TestLesDatesDeJetonsSeLisent(t *testing.T) {
	maintenant := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

	cas := []struct {
		iso, attendu string
	}{
		{"", ""},
		{"2026-08-31T11:59:30Z", "à l'instant"},
		{"2026-08-31T11:30:00Z", "il y a 30 min"},
		{"2026-08-31T06:00:00Z", "il y a 6 h"},
		{"2026-08-28T12:00:00Z", "il y a 3 j"},
		// Au-delà de deux mois, l'écart cesse d'aider : la date absolue reprend.
		{"2026-01-15T09:30:00Z", "15/01/2026 à 09:30 UTC"},
		// Une valeur illisible se rend telle quelle, sans inventer.
		{"pas une date", "pas une date"},
	}
	for _, c := range cas {
		if got, _ := quand(c.iso, maintenant); got != c.attendu {
			t.Errorf("quand(%q) = %q, attendu %q", c.iso, got, c.attendu)
		}
	}

	// Et l'exact reste disponible pour l'infobulle.
	if _, exact := quand("2026-08-31T11:30:00Z", maintenant); exact != "31/08/2026 à 11:30 UTC" {
		t.Errorf("date exacte = %q", exact)
	}
}

// Le rendu de l'écran porte bien la forme lisible, pas le RFC3339 brut.
func TestLEcranNAffichePasDeRFC3339Brut(t *testing.T) {
	s := newServer(t)
	s.Now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	h := s.Handler()
	s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	c := login(t, h, "colin", "motdepasse")

	poste(t, h, c, "/admin/jetons", url.Values{"libelle": {"frais"}, "plafond": {"lecture"}})
	corps := recupere(t, h, c, "/admin/jetons").Body.String()

	if regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`).MatchString(corps) {
		t.Errorf("une date RFC3339 brute est affichée :\n%s", corps)
	}
	if !strings.Contains(corps, "jamais servi") {
		t.Error("un jeton neuf devrait se dire « jamais servi »")
	}
}

// `Cache-Control: no-store` sur la page qui porte le secret.
//
// L'en-tête existait, mais RIEN ne le tenait : le supprimer de `render`
// laissait toute la suite verte, et aucun test du dépôt n'assertait dessus.
// C'est pourtant le seul chose qui sépare un jeton en clair d'un cache proxy,
// du cache disque et du bfcache.
func TestLaPageQuiPorteLeJetonNEstJamaisMiseEnCache(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	c := login(t, h, "colin", "motdepasse")

	rec := poste(t, h, c, "/admin/jetons", url.Values{"libelle": {"secret"}, "plafond": {"lecture"}})
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("page de création : Cache-Control = %q, attendu no-store", got)
	}
	if rec2 := recupere(t, h, c, "/admin/jetons"); rec2.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("liste : Cache-Control = %q", rec2.Header().Get("Cache-Control"))
	}
}

// LE JETON EN CLAIR N'EST PAS DANS LA COMMANDE À COLLER.
//
// Une commande collée telle quelle part dans l'historique du terminal et dans
// `argv`, lisible par `ps` : une copie durable du secret, là où la révocation
// ne va pas la chercher. Et la première version n'était même pas exécutable -
// `<url-du-serveur>` est de la syntaxe de redirection shell.
func TestLaCommandeACollerNeContientPasLeJeton(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	c := login(t, h, "colin", "motdepasse")

	corps := poste(t, h, c, "/admin/jetons",
		url.Values{"libelle": {"x"}, "plafond": {"lecture"}}).Body.String()
	clair := extraitJeton(t, corps)

	if n := strings.Count(corps, clair); n != 1 {
		t.Errorf("le jeton apparaît %d fois dans la page, attendu 1", n)
	}
	// La commande d'exemple est là, avec un emplacement à remplir.
	if !strings.Contains(corps, "claude mcp add") {
		t.Error("la commande d'exemple a disparu")
	}
	if !strings.Contains(corps, "LE_JETON") {
		t.Error("la commande ne montre pas où coller le jeton")
	}
	// Et l'URL est entre guillemets : sans eux, « <serveur> » est lu par le
	// shell comme une redirection, et la commande ne s'exécute pas.
	if strings.Contains(corps, "&lt;url-du-serveur&gt;") {
		t.Error("la commande porte des chevrons non protégés : le shell les lit comme des redirections")
	}
}

// Le PLAFOND et le DERNIER USAGE s'affichent réellement.
//
// Les deux étaient des critères d'acceptation, et les mettre à la chaîne vide
// dans le rendu laissait la suite verte : aucun test ne rendait jamais un
// jeton qui avait servi.
func TestLEcranAfficheLePlafondEtLUsageReel(t *testing.T) {
	s := newServer(t)
	s.Now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	h := s.Handler()
	colin, _ := s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	c := login(t, h, "colin", "motdepasse")

	poste(t, h, c, "/admin/jetons", url.Values{"libelle": {"en lecture"}, "plafond": {"lecture"}})
	poste(t, h, c, "/admin/jetons", url.Values{"libelle": {"en ecriture"}, "plafond": {"ecriture"}})

	// On fait SERVIR un des deux, pour que la colonne ait quelque chose à dire.
	liste, _ := s.DB.ListJetonsMCP(colin.ID)
	if err := s.DB.ToucheJetonMCP(liste[0].ID); err != nil {
		t.Fatal(err)
	}

	corps := recupere(t, h, c, "/admin/jetons").Body.String()
	// Le vocabulaire du plafond est celui d'une ACTION, pas celui du coffre :
	// un jeton n'est pas « ouvert », il peut lire ou lire et ecrire (01/09).
	for _, attendu := range []string{"lecture seule", "lecture et écriture"} {
		if !strings.Contains(corps, "<td>"+attendu+"</td>") {
			t.Errorf("le plafond %q n'est pas affiché", attendu)
		}
	}
	// Le jeton qui a servi ne dit plus « jamais servi », et porte l'infobulle.
	if strings.Count(corps, "jamais servi") != 1 {
		t.Errorf("un seul jeton a servi, mais %d se disent « jamais servi »", strings.Count(corps, "jamais servi"))
	}
	// La date du jour, PAS une date littérale. La première version comparait à
	// `title="31/08/2026`, le jour où ce test a été écrit : il est passé rouge
	// tout seul à minuit, sans qu'aucun code ne change. Un test qui pourrit à
	// une date fixe ne mesure plus le produit, il mesure le calendrier.
	aujourdhui := time.Now().UTC().Format("02/01/2006")
	if !strings.Contains(corps, `title="`+aujourdhui) {
		t.Errorf("l'infobulle de date exacte manque (attendu title=%q) :\n%s", aujourdhui, corps)
	}
}

// Le libellé est validé, comme tous les autres champs libres du dépôt - et
// pour une raison de plus : il part dans le journal d'écriture du serveur.
func TestLeLibelleEstValide(t *testing.T) {
	cas := []struct {
		nom, brut, attenduLibelle string
		refuse                    bool
	}{
		{"normal", "meeting-processor", "meeting-processor", false},
		{"espaces autour", "  routine  ", "routine", false},
		{"espaces seuls", "     ", "", true},
		{"vide", "", "", true},
		{"retour à la ligne", "a\nb", "", true},
		{"échappement ANSI", "a\x1b[31mb", "", true},
		{"trop long", strings.Repeat("x", 65), "", true},
		{"juste à la limite", strings.Repeat("x", 64), strings.Repeat("x", 64), false},
		{"accents", "réunion août", "réunion août", false},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			libelle, motif := libelleValide(c.brut)
			if (motif != "") != c.refuse {
				t.Errorf("libelleValide(%q) : motif=%q, refus attendu=%v", c.brut, motif, c.refuse)
			}
			if !c.refuse && libelle != c.attenduLibelle {
				t.Errorf("libelleValide(%q) = %q, attendu %q", c.brut, libelle, c.attenduLibelle)
			}
		})
	}
}

// Un libellé hostile ressort échappé, jamais brut.
func TestUnLibelleHostileEstEchappe(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	s.DB.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	c := login(t, h, "colin", "motdepasse")

	poste(t, h, c, "/admin/jetons", url.Values{
		"libelle": {`<img src=x onerror=alert(1)>`}, "plafond": {"lecture"}})
	corps := recupere(t, h, c, "/admin/jetons").Body.String()
	if strings.Contains(corps, "<img src=x") {
		t.Errorf("le libellé est rendu brut :\n%s", corps)
	}
	if !strings.Contains(corps, "&lt;img src=x") {
		t.Errorf("le libellé n'apparaît pas échappé :\n%s", corps)
	}
}
