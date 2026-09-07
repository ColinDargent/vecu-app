package web

// LA MESURE QUE DAR-195 EXIGE : la latence réelle de chaque geste
// d'administration, prise par la vraie porte HTTP, sur un vault à l'échelle du
// vault réel.
//
// Pourquoi un instrument plutôt qu'une estimation. Le 31/08, la spec du MCP
// écrivait que parcourir 900 fichiers sans index était « acceptable à cette
// taille » ; mesuré, c'était 9,0 s, soit au-delà du délai d'attente de la
// plupart des clients. L'inventaire d'interactions de DAR-195 doit porter des
// latences PIRE CAS, et une latence devinée n'a aucune valeur pour choisir une
// surface d'attente : c'est précisément le seuil de 140 ms qui décide s'il faut
// un indicateur, et le seuil de quelques secondes qui décide s'il faut une
// barre de progression.
//
// DES BENCHMARKS, et pas des tests : rien ne tourne sans `-bench`, donc
// `go test ./...` n'en paie pas le coût.
//
//	go test ./server/web/ -run '^$' -bench Latence -benchtime 3x
//
// L'échelle est celle du vault de Colin au 01/09 : 1186 fichiers dans l'espace
// `shared`, mesurée dans `.vecu/state.json`. Le nombre de comptes est celui
// d'un client, pas le nôtre : nous sommes deux, un client qui déploie Vécu en
// aura dix à vingt, et c'est ce cas-là que les écrans du jalon 2 doivent tenir.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// L'échelle de mesure. Les trois nombres sont des faits, pas des rondeurs :
// 1186 est le nombre de fichiers suivis par Vécu sur le poste de Colin, 12 est
// la taille d'équipe qu'un premier client aura, 40 est le nombre de règles
// qu'une arborescence de projets produit quand on pose des droits par dossier.
const (
	nFichiers = 1186
	nComptes  = 12
	nRegles   = 40
)

type bancAdmin struct {
	h       http.Handler
	cookie  *http.Cookie
	store   *storage.Store
	db      *db.DB
	cible   *db.User
	chemins []string
}

// Le banc est monté UNE FOIS pour tous les benchmarks du fichier. Le montage
// coûte 1186 écritures git, soit l'essentiel du temps du fichier : le refaire
// treize fois donnerait un instrument que personne ne relancerait, et un
// instrument qu'on ne relance pas ne mesure rien.
//
// Ce que ça coûte en retour : les benchmarks d'écriture (poser un droit, créer
// un jeton) partagent l'état. Ils sont écrits pour être idempotents ou pour
// varier leur clé, et aucun ne lit ce qu'un autre a posé.
var (
	uneFois sync.Once
	partage *bancAdmin
)

func monteBanc(b *testing.B) *bancAdmin {
	b.Helper()
	uneFois.Do(func() { partage = construitBanc(b) })
	return partage
}

// construitBanc construit un vault à l'échelle réelle et ouvre une session
// admin. Tout son coût est hors mesure : les benchmarks appellent
// b.ResetTimer() après.
func construitBanc(b *testing.B) *bancAdmin {
	b.Helper()
	dir, err := os.MkdirTemp("", "vecu-latence-")
	if err != nil {
		b.Fatal(err)
	}
	d, err := db.Open(dir + "/app.db")
	if err != nil {
		b.Fatal(err)
	}
	store, err := storage.Init(dir + "/brain.git")
	if err != nil {
		b.Fatal(err)
	}

	// Une note ordinaire de second cerveau : ~2 Ko, la taille médiane du vault.
	corps := strings.Repeat("une ligne de note ordinaire dans un second cerveau\n", 40)
	// Une arborescence qui ressemble à la vraie : des dossiers de projets, de
	// clients et de process, plutôt qu'un seul dossier plat. C'est ce qui rend
	// le parcours d'arbre représentatif.
	dossiers := []string{
		"shared/projects/alpha", "shared/projects/beta", "shared/projects/gamma",
		"shared/clients/un", "shared/clients/deux", "shared/clients/trois",
		"shared/process", "shared/veille", "shared/content", "shared/context",
	}
	chemins := make([]string, 0, nFichiers)
	for i := range nFichiers {
		p := fmt.Sprintf("%s/n%04d.md", dossiers[i%len(dossiers)], i)
		if _, err := store.Write(p, corps, "colin"); err != nil {
			b.Fatal(err)
		}
		chemins = append(chemins, p)
	}

	admin, err := d.CreateUser("colin", "motdepasse", perms.Ecriture, true)
	if err != nil {
		b.Fatal(err)
	}
	membres := make([]*db.User, 0, nComptes)
	for i := range nComptes {
		u, err := d.CreateUser(fmt.Sprintf("membre%02d", i), "motdepasse", perms.Lecture, false)
		if err != nil {
			b.Fatal(err)
		}
		membres = append(membres, u)
	}
	// Des règles réparties sur l'arborescence, comme un vrai réglage de droits :
	// les trois niveaux sont représentés, `Invisible` compris, parce que c'est
	// lui qui fait travailler la résolution au lieu de la court-circuiter.
	for i := range nRegles {
		niveau := perms.Lecture
		if i%3 == 0 {
			niveau = perms.Ecriture
		}
		if i%7 == 0 {
			niveau = perms.Invisible
		}
		u := membres[i%len(membres)]
		if err := d.SetPermission(u.ID, dossiers[i%len(dossiers)], niveau); err != nil {
			b.Fatal(err)
		}
	}
	cible := membres[0]
	_ = admin

	s := &Server{DB: d, Store: store}
	h := s.Handler()
	return &bancAdmin{h: h, cookie: connecte(b, h), store: store, db: d, cible: cible, chemins: chemins}
}

func connecte(b *testing.B, h http.Handler) *http.Cookie {
	b.Helper()
	form := url.Values{"username": {"colin"}, "password": {"motdepasse"}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		b.Fatalf("login : attendu 303, obtenu %d (%s)", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	b.Fatal("cookie de session absent")
	return nil
}

// voit joue un GET et échoue si le code n'est pas celui attendu. Un benchmark
// qui mesure une page d'erreur mesure la vitesse d'un 404, ce qui est le mode
// d'échec silencieux de ce genre de fichier.
func (bc *bancAdmin) voit(b *testing.B, chemin string, attendu int) {
	b.Helper()
	req := httptest.NewRequest("GET", chemin, nil)
	req.AddCookie(bc.cookie)
	rec := httptest.NewRecorder()
	bc.h.ServeHTTP(rec, req)
	if rec.Code != attendu {
		b.Fatalf("GET %s : attendu %d, obtenu %d (%s)", chemin, attendu, rec.Code, tronque(rec.Body.String()))
	}
}

func (bc *bancAdmin) poste(b *testing.B, chemin string, form url.Values, attendu int) {
	b.Helper()
	req := httptest.NewRequest("POST", chemin, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(bc.cookie)
	rec := httptest.NewRecorder()
	bc.h.ServeHTTP(rec, req)
	if rec.Code != attendu {
		b.Fatalf("POST %s : attendu %d, obtenu %d (%s)", chemin, attendu, rec.Code, tronque(rec.Body.String()))
	}
}

func tronque(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Les écrans : ce que le mainteneur OUVRE.
// ---------------------------------------------------------------------------

func BenchmarkLatenceAccueil(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/", http.StatusOK)
	}
}

func BenchmarkLatenceEcranDossiers(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/dossiers", http.StatusOK)
	}
}

func BenchmarkLatenceEcranUsers(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/users", http.StatusOK)
	}
}

// La fiche d'un compte : c'est elle qui construisait l'arbre entier du dépôt
// (DAR-164). Le pire cas des écrans de droits.
func BenchmarkLatenceFicheCompte(b *testing.B) {
	bc := monteBanc(b)
	chemin := fmt.Sprintf("/admin/users/%d", bc.cible.ID)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, chemin, http.StatusOK)
	}
}

// Le contenu d'un dossier : c'est là que le mainteneur voit les fichiers et
// leur état, `/admin/fichiers` n'étant plus qu'une redirection 301 depuis la
// refonte du 21/08. Mesuré sur le dossier le plus peuplé.
func BenchmarkLatenceContenuDossier(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/dossiers/shared/projects/alpha", http.StatusOK)
	}
}

// Les deux redirections héritées de la refonte du 21/08. Mesurées parce
// qu'elles sont sur le chemin d'un signet ou d'un lien collé dans un message,
// et qu'un 301 lent se lit comme une page lente.
func BenchmarkLatenceRedirectionsHeritees(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/fichiers", http.StatusMovedPermanently)
		bc.voit(b, "/admin/conflits", http.StatusMovedPermanently)
	}
}

func BenchmarkLatenceEcranSkills(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/skills", http.StatusOK)
	}
}

func BenchmarkLatenceEcranJetons(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/jetons", http.StatusOK)
	}
}

func BenchmarkLatenceHistoriqueFichier(b *testing.B) {
	bc := monteBanc(b)
	chemin := "/admin/historique/" + bc.chemins[0]
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, chemin, http.StatusOK)
	}
}

// ---------------------------------------------------------------------------
// Les gestes : ce que le mainteneur DÉCLENCHE.
// ---------------------------------------------------------------------------

func BenchmarkLatencePoserUnDroit(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.poste(b, "/admin/dossiers/acces", url.Values{
			"chemin":  {"shared/projects/alpha"},
			"niveau":  {"lecture"},
			"user_id": {fmt.Sprint(bc.cible.ID)},
		}, http.StatusSeeOther)
	}
}

func BenchmarkLatenceDefautDossier(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.poste(b, "/admin/dossiers/defaut", url.Values{
			"chemin": {"shared/projects/beta"},
			"niveau": {"lecture"},
		}, http.StatusSeeOther)
	}
}

func BenchmarkLatenceCreerJeton(b *testing.B) {
	bc := monteBanc(b)
	i := 0
	b.ResetTimer()
	for b.Loop() {
		i++
		// 200 et pas 303 : la page rend le jeton en clair, qui ne s'affiche
		// qu'une fois. Une redirection le perdrait.
		bc.poste(b, "/admin/jetons", url.Values{
			"libelle": {fmt.Sprintf("mesure %d", i)},
			"plafond": {"lecture"},
		}, http.StatusOK)
	}
}

// L'export : le geste qui se compte en dizaines de secondes, et le seul qui
// justifie une barre de progression plutôt qu'un indicateur.
func BenchmarkLatenceExportZip(b *testing.B) {
	bc := monteBanc(b)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/export.zip", http.StatusOK)
	}
}

// LA LOI D'ÉCHELLE DE L'EXPORT, et c'est le seul geste du parc qui peut
// atteindre « plusieurs minutes ». Le mesurer à une seule taille ne dit pas si
// un vault client trois fois plus gros coûte trois fois plus ou neuf fois plus,
// et c'est cette différence qui décide entre une barre de progression et un
// travail en arrière-plan avec notification.
//
//	go test ./server/web/ -run '^$' -bench EchelleExport -benchtime 1x
func BenchmarkEchelleExport(b *testing.B) {
	for _, n := range []int{300, 1186, 3000} {
		b.Run(fmt.Sprintf("%dfichiers", n), func(b *testing.B) {
			bc := bancDeTaille(b, n)
			b.ResetTimer()
			for b.Loop() {
				bc.voit(b, "/admin/export.zip", http.StatusOK)
			}
		})
	}
}

// bancDeTaille : un vault d'une taille donnée, sans les comptes ni les règles.
// L'export ne dépend que du dépôt, donc c'est le montage minimal honnête.
func bancDeTaille(b *testing.B, n int) *bancAdmin {
	b.Helper()
	dir, err := os.MkdirTemp("", "vecu-echelle-")
	if err != nil {
		b.Fatal(err)
	}
	d, err := db.Open(dir + "/app.db")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { d.Close() })
	store, err := storage.Init(dir + "/brain.git")
	if err != nil {
		b.Fatal(err)
	}
	corps := strings.Repeat("une ligne de note ordinaire dans un second cerveau\n", 40)
	for i := range n {
		if _, err := store.Write(fmt.Sprintf("shared/notes/n%05d.md", i), corps, "colin"); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := d.CreateUser("colin", "motdepasse", perms.Ecriture, true); err != nil {
		b.Fatal(err)
	}
	s := &Server{DB: d, Store: store}
	h := s.Handler()
	return &bancAdmin{h: h, cookie: connecte(b, h), store: store, db: d}
}
