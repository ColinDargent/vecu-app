package web

// markdown.go : le rendu d'une note dans l'écran de lecture.
//
// L'écran d'un fichier montrait `<pre>{{.Contenu}}</pre>` : le texte source,
// exact et illisible. Un vault est écrit en markdown ; le lire dans l'admin
// doit ressembler à le lire.
//
// LE RENDU BRUT NE DISPARAIT PAS, il devient l'autre vue (voir `?source`). Un
// mainteneur qui se demande ce qu'il y a VRAIMENT dans un fichier - un
// caractère invisible, un frontmatter cassé, une fin de ligne - a besoin des
// octets, pas d'une mise en forme.

import (
	"bytes"
	"html/template"
	"path"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// moteurMarkdown : une seule instance, partagée par toutes les requêtes.
//
// AUCUN `html.WithUnsafe()`, ET C'EST LE POINT DE SECURITE DU LOT. Cité de la
// doc : « By default, goldmark does not render raw HTML or potentially-
// dangerous URLs. » Le rendu de HTML brut demande donc un geste EXPLICITE, et
// ce geste est interdit ici : le contenu rendu est écrit par les membres et par
// des agents, et il s'affiche dans une page d'administration où l'on gère des
// droits. Un `<script>` déposé dans une note s'exécuterait dans la session d'un
// administrateur. Un test le fige (voir `markdown_test.go`).
//
// `extension.GFM` apporte les tableaux, le barré, l'auto-lien et les cases à
// cocher - c'est-à-dire ce qu'un vault écrit sous Obsidian contient
// réellement. Sans lui, un tableau se lit comme une bouillie de barres
// verticales.
//
// Une instance partagée plutôt qu'une par requête : `parser.Parse` s'initialise
// derrière un `sync.Once` puis ne mute plus sa configuration, et le paquet
// expose lui-même un `defaultMarkdown` global. La doc ne le PROMET pas, donc un
// test sous `-race` convertit en parallèle et le vérifie.
// Source: https://pkg.go.dev/github.com/yuin/goldmark#section-readme
var moteurMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// estMarkdown : ce fichier se met-il en forme ?
//
// L'extension, et rien d'autre. Deviner au contenu ferait rendre en markdown un
// `.txt` qui contient un tiret en début de ligne, et le produit prétendrait
// mettre en forme ce qu'il ne sait pas lire. `EqualFold` parce que le disque
// des postes est insensible à la casse : « NOTES.MD » est un fichier markdown.
func estMarkdown(chemin string) bool {
	return strings.EqualFold(path.Ext(chemin), ".md")
}

// marqueurHTMLOmis : ce que goldmark écrit à la place du HTML brut qu'il
// refuse de rendre. C'est un COMMENTAIRE, donc invisible dans la page : sans le
// chercher, la note perd du contenu sans que rien ne le dise. Un `<br>` entre
// deux lignes les fusionne, un `<mark>` disparaît, et le lecteur croit lire la
// note telle qu'elle est.
// Source: renderer/html/html.go de goldmark v1.8.5
const marqueurHTMLOmis = "<!-- raw HTML omitted -->"

// noteMiseEnForme : ce que l'écran d'un fichier a besoin de savoir d'une note.
type noteMiseEnForme struct {
	// Entete : le frontmatter brut, sans ses délimiteurs. Il est SORTI du
	// markdown avant rendu - voir separeFrontmatter - et réaffiché tel quel.
	Entete string
	// Corps : le markdown mis en forme.
	Corps template.HTML
	// HTMLOmis : goldmark a jeté du HTML brut. La page le DIT, parce qu'un
	// écran dont le métier est de lire une note fidèlement n'a pas le droit
	// d'en retirer un morceau en silence.
	HTMLOmis bool
}

// metEnForme : le markdown d'une note, prêt à insérer.
//
// Rend `template.HTML`, ce qui COURT-CIRCUITE l'échappement de html/template.
// C'est exactement pourquoi le moteur ci-dessus ne doit jamais recevoir
// `WithUnsafe` : à partir d'ici, la sûreté du HTML ne tient plus qu'à goldmark.
//
// L'erreur rendue ne peut aujourd'hui pas se produire : `Convert` n'échoue que
// si un renderer de nœud échoue, or ceux de `html.Renderer` rendent tous nil et
// le writer est un `bytes.Buffer`. On la propage quand même plutôt que de
// l'avaler - une version future, ou une extension ajoutée ici, la rendrait
// vivante sans prévenir - mais sans lui prêter une robustesse qu'elle n'a pas.
func metEnForme(source string) (noteMiseEnForme, error) {
	entete, corps := separeFrontmatter(source)
	var buf bytes.Buffer
	if err := moteurMarkdown.Convert([]byte(corps), &buf); err != nil {
		return noteMiseEnForme{}, err
	}
	rendu := buf.String()
	return noteMiseEnForme{
		Entete:   entete,
		Corps:    template.HTML(rendu),
		HTMLOmis: strings.Contains(rendu, marqueurHTMLOmis),
	}, nil
}

// separeFrontmatter : l'entête « --- ... --- » d'une note, et le reste.
//
// SANS CETTE SEPARATION, LE CAS NOMINAL EST FAUX. Toute note d'un vault
// Obsidian ouvre sur un frontmatter ; rendu comme du markdown, le premier
// « --- » devient une barre horizontale et le second SOULIGNE les métadonnées,
// qui sortent alors en `<h2>` au-dessus du vrai titre et plus gros que lui. La
// première chose que voit le mainteneur est une donnée machine, mise en avant.
//
// L'entête est RENDU quand même, en clair : le sortir du markdown ne veut pas
// dire le cacher. Un écran de lecture qui escamote une partie du fichier ment
// aussi sûrement qu'un écran qui le déforme.
//
// La reconnaissance est étroite, et c'est voulu : le fichier DOIT commencer par
// une ligne « --- », et l'entête s'arrête à la première ligne « --- »
// suivante. Sans fermeture, il n'y a pas d'entête - une note qui ouvre sur une
// barre horizontale reste une note avec une barre horizontale.
func separeFrontmatter(source string) (entete, corps string) {
	ligne, reste, ok := strings.Cut(source, "\n")
	if !ok || strings.TrimRight(ligne, "\r") != "---" {
		return "", source
	}
	var lignes []string
	for {
		ligne, apres, encore := strings.Cut(reste, "\n")
		if strings.TrimRight(ligne, "\r") == "---" {
			return strings.Join(lignes, "\n"), apres
		}
		if !encore {
			// Pas de ligne de fermeture : ce n'était pas un entête, mais une
			// note qui commence par une barre horizontale.
			return "", source
		}
		lignes = append(lignes, strings.TrimRight(ligne, "\r"))
		reste = apres
	}
}
