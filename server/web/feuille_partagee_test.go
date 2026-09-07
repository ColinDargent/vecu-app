package web

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// LE DÉFAUT QUE CE TEST FERME, ET IL A RÉCIDIVÉ TROIS FOIS.
//
// Le style de ce back-office vit dans des blocs `<style>` : celui du gabarit,
// commun, et un par écran. Rien n'empêche un écran d'UTILISER une classe que
// seul le `<style>` d'un autre DÉCLARE - et comme chaque page est rendue seule,
// la règle n'arrive jamais. Le rendu ne casse pas : il retombe sur la forme par
// défaut du navigateur, ce qui est plausible à la lecture du code et faux à
// l'écran.
//
//   - `svg.icone` vivait dans `skills.html` ; l'écran des cohortes rendait des
//     icônes de 300 × 150 (mesuré le 02/09).
//   - `details.internes` vivait dans `dossiers.html` ; la fiche d'un compte
//     déroulait trente-cinq lignes rouges (02/09).
//   - `.resume`, `.reglage`, `.grille-partage`, `.choix`, `.liste-skills`,
//     `.large`, `.ok`, `.form-tags`, `.partage` vivaient dans `skills.html` ;
//     l'écran des cohortes ET la fiche de création d'un compte les rendaient
//     nus (04/09).
//
// Les deux premières fois, la correction a été de remonter la règle et d'écrire
// pourquoi dans un commentaire. Une règle de discipline qui vit dans une note
// ne s'applique pas : la troisième fois est arrivée quand même. Elle est ici
// maintenant.
func TestAucuneClasseNEmprunteLaFeuilleDUnAutreEcran(t *testing.T) {
	fichiers, err := filepath.Glob("templates/*.html")
	if err != nil || len(fichiers) == 0 {
		t.Fatalf("aucun gabarit trouvé : %v", err)
	}

	sources := map[string]string{}
	for _, f := range fichiers {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("lecture de %s : %v", f, err)
		}
		sources[filepath.Base(f)] = string(b)
	}
	commun, ok := sources["layout.html"]
	if !ok {
		t.Fatal("layout.html introuvable : le gabarit commun porte la feuille partagée")
	}
	declareesCommun := classesDeclarees(commun)

	for nom, src := range sources {
		if nom == "layout.html" {
			continue
		}
		disponibles := map[string]bool{}
		for c := range declareesCommun {
			disponibles[c] = true
		}
		for c := range classesDeclarees(src) {
			disponibles[c] = true
		}
		var orphelines []string
		for _, c := range classesUtilisees(src) {
			if !disponibles[c] {
				orphelines = append(orphelines, c)
			}
		}
		sort.Strings(orphelines)
		if len(orphelines) > 0 {
			t.Errorf("%s utilise des classes qu'aucune feuille à sa portée ne déclare : %s\n"+
				"Si une seule page s'en sert, déclarez-les dans son <style>. Si deux pages "+
				"s'en servent, la règle vit dans layout.html - c'est la seule feuille que "+
				"toutes les pages reçoivent.", nom, strings.Join(orphelines, ", "))
		}
	}
}

var (
	reStyle     = regexp.MustCompile(`(?s)<style>(.*?)</style>`)
	reCommCSS   = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reSelecteur = regexp.MustCompile(`\.([a-zA-Z][\w-]*)`)
	reClasse    = regexp.MustCompile(`class="([^"]*)"`)
	reAction    = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

// classesDeclarees : les classes que les blocs `<style>` d'un fichier posent.
// Les commentaires CSS sont retirés d'abord : ils citent volontiers des classes
// qu'ils ne déclarent pas, et les compter rendrait le test complaisant.
func classesDeclarees(src string) map[string]bool {
	out := map[string]bool{}
	for _, bloc := range reStyle.FindAllStringSubmatch(src, -1) {
		css := reCommCSS.ReplaceAllString(bloc[1], " ")
		for _, m := range reSelecteur.FindAllStringSubmatch(css, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// classesUtilisees : les classes posées par les attributs `class` du gabarit,
// hors blocs `<style>`. Les actions de gabarit sont retirées avant le découpage,
// pour que `class="pastille{{if .RW}} rw{{end}}"` rende bien `pastille` et `rw`.
func classesUtilisees(src string) []string {
	// LES ACTIONS PARTENT D'ABORD, et pas après le découpage : une action peut
	// contenir un guillemet (`class="pastille{{if eq .N "ecriture"}} rw{{end}}"`),
	// qui ferme l'attribut trop tôt pour l'expression d'à côté. Le test rendait
	// alors `pastille{{if` et `eq` comme des noms de classe.
	hors := reAction.ReplaceAllString(reStyle.ReplaceAllString(src, " "), " ")
	vues := map[string]bool{}
	var out []string
	for _, m := range reClasse.FindAllStringSubmatch(hors, -1) {
		for _, c := range strings.Fields(m[1]) {
			if c == "" || vues[c] {
				continue
			}
			vues[c] = true
			out = append(out, c)
		}
	}
	return out
}
