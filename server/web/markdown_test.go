package web

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/colindargent/vecu/server/perms"
)

// LE TEST QUI FIGE L'INTERDICTION DE `html.WithUnsafe()`.
//
// Le rendu est inséré dans la page SANS échappement (`template.HTML`) : à
// partir de là, la sûreté du HTML ne tient plus qu'à goldmark. Le contenu rendu
// est écrit par les membres et par des agents, et il s'affiche dans une page où
// l'on gère des droits - un `<script>` déposé dans une note s'exécuterait dans
// la session d'un administrateur.
//
// Les vecteurs vont au-delà de `<script>` : l'attribut d'événement, l'URL de
// protocole, et le `data:` qui porte du HTML sont les trois autres portes.
func TestLeHTMLBrutDUneNoteNEstJamaisExecute(t *testing.T) {
	cas := []struct {
		nom, source string
		interdits   []string
	}{
		{"balise script", "# Note\n\n<script>alert(1)</script>\n",
			[]string{"<script>alert(1)", "alert(1)"}},
		{"attribut d'événement", "<img src=x onerror=alert(1)>\n",
			[]string{"onerror", "<img src=x"}},
		{"HTML inline", "Texte <b onmouseover=alert(1)>gras</b>.\n",
			[]string{"onmouseover", "<b "}},
		{"lien javascript:", "[clique](javascript:alert(1))\n",
			[]string{"javascript:"}},
		{"image data: HTML", "![x](data:text/html;base64,PHNjcmlwdD4=)\n",
			[]string{"data:text/html"}},
		{"iframe", "<iframe src=\"https://ailleurs.fr\"></iframe>\n",
			[]string{"<iframe"}},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			s := newServer(t)
			s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
			if _, err := s.Store.Write("notes/piège.md", c.source, "achille"); err != nil {
				t.Fatal(err)
			}
			h := s.Handler()
			rec := get(h, "/admin/dossiers/notes/pi%C3%A8ge.md", login(t, h, "colin", "mdp"))
			if rec.Code != http.StatusOK {
				t.Fatalf("attendu 200, obtenu %d", rec.Code)
			}
			// LA ZONE RENDUE, pas la page : le layout porte son propre
			// <script>, une assertion sur tout le corps serait fausse en
			// permanence ou verte par accident.
			zone := zoneRendue(t, rec.Body.String())
			for _, interdit := range c.interdits {
				if strings.Contains(zone, interdit) {
					t.Errorf("%q se retrouve dans la zone rendue :\n%s", interdit, zone)
				}
			}
			// Précondition : la zone rendue existe et n'est pas vide, sinon
			// l'assertion ci-dessus serait verte pour la mauvaise raison.
			if strings.TrimSpace(zone) == "" {
				t.Error("la zone rendue est vide : le test ne prouve rien")
			}
		})
	}
}

func TestUnMarkdownSAfficheMisEnForme(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	source := "# Décisions\n\nUn paragraphe.\n\n- une puce\n- une autre\n\n" +
		"| colonne | valeur |\n|---|---|\n| a | 1 |\n\n" +
		"```go\nfmt.Println(\"x\")\n```\n\n[le lien](https://exemple.fr)\n"
	if _, err := s.Store.Write("notes/décisions.md", source, "colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	rec := get(h, "/admin/dossiers/notes/d%C3%A9cisions.md", login(t, h, "colin", "mdp"))
	if rec.Code != http.StatusOK {
		t.Fatalf("attendu 200, obtenu %d", rec.Code)
	}
	zone := zoneRendue(t, rec.Body.String())
	for _, attendu := range []string{
		"<h1>Décisions</h1>", "<li>une puce</li>", "<table>", "<th>colonne</th>",
		"<code", "fmt.Println", `<a href="https://exemple.fr"`,
	} {
		if !strings.Contains(zone, attendu) {
			t.Errorf("%q absent du rendu :\n%s", attendu, zone)
		}
	}
}

// CE QU'ON NE SAIT PAS LIRE, ON NE PREND PAS L'AIR DE LE METTRE EN FORME. Un
// `.json` dont une ligne commence par un tiret n'est pas une liste à puces.
func TestUnNonMarkdownGardeLeRenduBrut(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	// Un contenu qui RESSEMBLE à du markdown, pour que le test tombe si la
	// décision se prenait au contenu plutôt qu'à l'extension.
	source := "# pas un titre\n\n- pas une puce\n"
	for _, chemin := range []string{"notes/données.json", "notes/brut.txt", "notes/LICENSE"} {
		if _, err := s.Store.Write(chemin, source, "colin"); err != nil {
			t.Fatal(err)
		}
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	for _, url := range []string{
		"/admin/dossiers/notes/donn%C3%A9es.json",
		"/admin/dossiers/notes/brut.txt",
		"/admin/dossiers/notes/LICENSE",
	} {
		rec := get(h, url, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s : attendu 200, obtenu %d", url, rec.Code)
		}
		corps := rec.Body.String()
		if !strings.Contains(corps, `<pre class="contenu">`) {
			t.Errorf("%s : le rendu brut a disparu :\n%s", url, corps)
		}
		if strings.Contains(corps, "<h1>pas un titre</h1>") {
			t.Errorf("%s : mis en forme alors que ce n'est pas du markdown", url)
		}
		if strings.Contains(corps, "Voir le source") {
			t.Errorf("%s : la bascule est proposée sur un fichier qui n'a qu'une vue", url)
		}
	}
}

// LE SOURCE RESTE A UN GESTE. Quelqu'un qui se demande ce qu'il y a VRAIMENT
// dans un fichier a besoin des octets ; la mise en forme les lui cache par
// construction.
func TestLeSourceDUnMarkdownResteAtteignable(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	if _, err := s.Store.Write("notes/a.md", "# Titre\n", "colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	rendu := get(h, "/admin/dossiers/notes/a.md", c).Body.String()
	if !strings.Contains(rendu, "<h1>Titre</h1>") {
		t.Fatalf("précondition : la vue par défaut devrait être mise en forme :\n%s", rendu)
	}
	if !strings.Contains(rendu, "?source") {
		t.Errorf("aucun lien vers le source depuis la vue mise en forme :\n%s", rendu)
	}

	rec := get(h, "/admin/dossiers/notes/a.md?source", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("vue source : attendu 200, obtenu %d", rec.Code)
	}
	source := rec.Body.String()
	if !strings.Contains(source, `<pre class="contenu"># Titre`) {
		t.Errorf("la vue source ne montre pas les octets du fichier :\n%s", source)
	}
	if strings.Contains(source, "<h1>Titre</h1>") {
		t.Error("la vue source met quand même en forme")
	}
}

// La doc de goldmark ne PROMET pas qu'une instance se partage entre
// goroutines ; le serveur en partage une seule. On le vérifie plutôt que de le
// croire.
//
// DES SOURCES DIFFERENTES, et c'est ce qui fait la sonde. Trente-deux
// goroutines qui convertissent LE MEME texte rendraient le même résultat même
// avec un état de parseur partagé : c'est sur des entrées distinctes qu'une
// corruption se voit, chacune comparée à ce que la même source rend seule.
//
// Le dépôt ne lance pas `-race` dans `go test ./...` : ce test attrape donc les
// corruptions VISIBLES dans le résultat, et devient un détecteur de course
// quand on lui ajoute `-race` à la main.
func TestLeMoteurMarkdownSePartageEntreRequetes(t *testing.T) {
	sources := make([]string, 32)
	for i := range sources {
		sources[i] = fmt.Sprintf(
			"# Titre %d\n\n| col%d | b |\n|---|---|\n| %d | 2 |\n\n- puce %d\n\n```go\nx := %d\n```\n",
			i, i, i, i, i)
	}
	// La référence : chaque source convertie SEULE, avant toute concurrence.
	seuls := make([]string, len(sources))
	for i, src := range sources {
		note, err := metEnForme(src)
		if err != nil {
			t.Fatal(err)
		}
		seuls[i] = string(note.Corps)
	}
	if !strings.Contains(seuls[0], "<table>") {
		t.Fatalf("précondition : le rendu devrait contenir un tableau :\n%s", seuls[0])
	}

	ensemble := make([]string, len(sources))
	erreurs := make([]error, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			note, err := metEnForme(src)
			erreurs[i], ensemble[i] = err, string(note.Corps)
		}()
	}
	wg.Wait()
	for i := range sources {
		if erreurs[i] != nil {
			t.Fatalf("conversion %d : %v", i, erreurs[i])
		}
		if ensemble[i] != seuls[i] {
			t.Fatalf("conversion %d en parallèle diffère de la même seule :\n%s\n---\n%s",
				i, ensemble[i], seuls[i])
		}
	}
}

// zoneRendue : le contenu de <article class="rendu">, c'est-à-dire ce que
// goldmark a produit. Le layout de la page porte son propre <script> et son
// propre <style> : une assertion sur tout le corps serait fausse en permanence.
func zoneRendue(t *testing.T, corps string) string {
	t.Helper()
	const ouvre = `<article class="rendu">`
	i := strings.Index(corps, ouvre)
	if i < 0 {
		t.Fatalf("aucune zone rendue dans la page :\n%s", corps)
	}
	reste := corps[i+len(ouvre):]
	j := strings.Index(reste, "</article>")
	if j < 0 {
		t.Fatalf("zone rendue non fermée :\n%s", corps)
	}
	return reste[:j]
}

// AUCUNE REQUETE RESEAU SORTANTE DEPUIS UNE PAGE SERVIE. Le rendu markdown
// donne à l'auteur d'une note le pouvoir de choisir une URL que le navigateur
// de l'administrateur ira chercher tout seul, sans une ligne de script :
// accusé de lecture horodaté, IP et agent de l'administrateur, et sondage du
// réseau interne depuis l'intérieur du périmètre.
func TestUneNoteNeFaitPasSortirDeRequeteDeLaPage(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	source := "![p](https://tracker.exemple.tld/p.png)\n\n![i](http://192.168.1.1/x.png)\n"
	if _, err := s.Store.Write("notes/piège.md", source, "achille"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	rec := get(h, "/admin/dossiers/notes/pi%C3%A8ge.md", login(t, h, "colin", "mdp"))
	if rec.Code != http.StatusOK {
		t.Fatalf("attendu 200, obtenu %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'none'") {
		t.Errorf("aucune directive qui bloque le chargement d'une image distante : %q", csp)
	}
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("aucun défaut fermé dans la directive : %q", csp)
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, « no-referrer » attendu",
			rec.Header().Get("Referrer-Policy"))
	}
	// Précondition : c'est bien le cas dangereux qui est servi, pas une page
	// où goldmark aurait déjà retiré les images.
	if !strings.Contains(zoneRendue(t, rec.Body.String()), "tracker.exemple.tld") {
		t.Skip("goldmark a retiré l'URL distante : le test ne prouve rien ici")
	}
}

// La directive vaut pour TOUTE la surface web, pas seulement l'écran d'une
// note : une seule porte qui ne la pose pas suffit à rouvrir la question.
func TestLesEntetesDurciesSontSurToutesLesReponses(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	for _, cas := range []struct {
		url    string
		statut int
	}{
		{"/admin/", http.StatusOK},
		{"/admin/dossiers", http.StatusOK},
		{"/admin/users", http.StatusOK},
		{"/admin/dossiers/nexiste/pas.md", http.StatusNotFound},
	} {
		rec := get(h, cas.url, c)
		if rec.Code != cas.statut {
			t.Errorf("%s : attendu %d, obtenu %d", cas.url, cas.statut, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "img-src 'none'") {
			t.Errorf("%s : directive absente ou incomplète (%q)",
				cas.url, rec.Header().Get("Content-Security-Policy"))
		}
	}
}

// LE CAS NOMINAL DU VAULT : toute note Obsidian ouvre sur un frontmatter.
// Rendu comme du markdown, ses deux « --- » donnent une barre horizontale puis
// un SOULIGNEMENT de titre : les métadonnées sortent en gros titre, au-dessus
// du vrai titre de la note.
func TestLeFrontmatterNeDevientPasUnTitre(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	source := "---\nentity: client/x\ndate: 2026-09-03\n---\n\n# Compte rendu\n\nLe corps.\n"
	if _, err := s.Store.Write("notes/cr.md", source, "colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	rec := get(h, "/admin/dossiers/notes/cr.md", login(t, h, "colin", "mdp"))
	if rec.Code != http.StatusOK {
		t.Fatalf("attendu 200, obtenu %d", rec.Code)
	}
	zone := zoneRendue(t, rec.Body.String())
	if strings.Contains(zone, "<h2>") || strings.Contains(zone, "<hr>") {
		t.Errorf("le frontmatter s'est rendu comme du markdown :\n%s", zone)
	}
	if !strings.Contains(zone, "<h1>Compte rendu</h1>") {
		t.Errorf("le vrai titre de la note manque :\n%s", zone)
	}
	// Sorti du markdown ne veut pas dire caché : un écran de lecture qui
	// escamote une partie du fichier ment aussi sûrement qu'un qui le déforme.
	corps := rec.Body.String()
	if !strings.Contains(corps, "entity: client/x") {
		t.Errorf("le frontmatter a disparu de la page :\n%s", corps)
	}
}

func TestSepareFrontmatter(t *testing.T) {
	cas := []struct{ nom, source, entete, corps string }{
		{"note ordinaire", "---\na: 1\nb: 2\n---\n# T\n", "a: 1\nb: 2", "# T\n"},
		{"sans saut final", "---\na: 1\n---", "a: 1", ""},
		{"entête vide", "---\n---\n# T\n", "", "# T\n"},
		{"fins de ligne Windows", "---\r\na: 1\r\n---\r\n# T\n", "a: 1", "# T\n"},
		// Les trois formes qu'il ne faut PAS prendre pour un entête.
		{"barre horizontale seule", "---\ndu texte\n", "", "---\ndu texte\n"},
		{"pas en tête", "# T\n\n---\na: 1\n---\n", "", "# T\n\n---\na: 1\n---\n"},
		{"tiret de plus", "----\na: 1\n----\n", "", "----\na: 1\n----\n"},
		{"fichier vide", "", "", ""},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			entete, corps := separeFrontmatter(c.source)
			if entete != c.entete || corps != c.corps {
				t.Errorf("separeFrontmatter(%q)\n = (%q, %q)\nattendu (%q, %q)",
					c.source, entete, corps, c.entete, c.corps)
			}
		})
	}
}

// CE QUI A ETE RETIRE SE DIT. goldmark remplace le HTML brut par un
// COMMENTAIRE, donc par rien de visible : deux lignes séparées par un <br>
// fusionnent, et le lecteur croit lire la note telle qu'elle est.
func TestLaPageDitQuElleARetireDuHTML(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	if _, err := s.Store.Write("notes/avec.md", "Ligne A<br>Ligne B\n", "colin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Write("notes/sans.md", "# Titre\n\nRien de spécial.\n", "colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")

	avec := get(h, "/admin/dossiers/notes/avec.md", c).Body.String()
	if !strings.Contains(avec, "contient du HTML, qui n'est pas affiché ici") {
		t.Errorf("la page retire du HTML sans le dire :\n%s", avec)
	}
	// Et l'avertissement ne s'affiche PAS quand il n'y a rien à signaler,
	// sinon il devient un décor qu'on n'apprend plus à lire.
	sans := get(h, "/admin/dossiers/notes/sans.md", c).Body.String()
	if strings.Contains(sans, "contient du HTML") {
		t.Errorf("avertissement affiché sur une note sans HTML :\n%s", sans)
	}
}

// UNE NOTE MARKDOWN VIDE. « On ne rend pas » et « le rendu est vide » sont deux
// choses : les confondre fait annoncer « source brut » sur la vue par défaut,
// avec un lien de sortie qui pointe sur l'URL courante - on clique sans fin.
func TestUneNoteVideNAnnoncePasLaVueSource(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	for _, cas := range []struct{ chemin, contenu string }{
		{"notes/vide.md", ""},
		{"notes/blanc.md", "   \n\t\n"},
	} {
		if _, err := s.Store.Write(cas.chemin, cas.contenu, "colin"); err != nil {
			t.Fatal(err)
		}
	}
	h := s.Handler()
	c := login(t, h, "colin", "mdp")
	for _, url := range []string{"/admin/dossiers/notes/vide.md", "/admin/dossiers/notes/blanc.md"} {
		corps := get(h, url, c).Body.String()
		if strings.Contains(corps, "Source brut.") {
			t.Errorf("%s : la vue par défaut s'annonce comme la vue source :\n%s", url, corps)
		}
		if !strings.Contains(corps, "Voir le source") {
			t.Errorf("%s : la bascule vers le source a disparu :\n%s", url, corps)
		}
	}
}

// LE GABARIT GLOBAL NE REECRIT PAS LE TEXTE DE LA NOTE. Les règles `h2` et
// `th` du layout imposent `text-transform: uppercase` : sans surcharge, un
// titre écrit « Décisions » s'affiche « DÉCISIONS » et un en-tête de tableau
// écrit « Approche » s'affiche « APPROCHE ». Sur l'écran dont le contrat est de
// montrer la note telle qu'elle est, c'est un mensonge - et il ne se voit dans
// aucune assertion sur le HTML, seulement à l'œil.
//
// Le test porte sur la FEUILLE DE STYLE de la page, seul endroit vérifiable
// sans navigateur : chaque sélecteur qui réécrit du texte doit être neutralisé
// pour la zone rendue.
func TestLeStyleDeLaPageNeReecritPasLeTexteDeLaNote(t *testing.T) {
	s := newServer(t)
	s.DB.CreateUser("colin", "mdp", perms.Ecriture, true)
	source := "# Titre\n\n## Décisions\n\n| Approche | Coût |\n|---|---|\n| a | 1 |\n"
	if _, err := s.Store.Write("notes/a.md", source, "colin"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	corps := get(h, "/admin/dossiers/notes/a.md", login(t, h, "colin", "mdp")).Body.String()

	// Précondition : le gabarit met bien en capitales, sinon le test garde un
	// problème qui n'existe plus et personne ne s'en apercevra.
	if !strings.Contains(corps, "text-transform: uppercase") {
		t.Skip("le gabarit ne met plus rien en capitales : la surcharge n'a plus d'objet")
	}
	for _, selecteur := range []string{".rendu h1, .rendu h2, .rendu h3", ".rendu th, .rendu td"} {
		i := strings.Index(corps, selecteur)
		if i < 0 {
			t.Errorf("aucune surcharge pour %q : le gabarit réécrira le texte de la note", selecteur)
			continue
		}
		bloc := corps[i:]
		if j := strings.Index(bloc, "}"); j >= 0 {
			bloc = bloc[:j]
		}
		if !strings.Contains(bloc, "text-transform: none") {
			t.Errorf("%q ne neutralise pas la mise en capitales :\n%s", selecteur, bloc)
		}
	}
}
