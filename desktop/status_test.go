package main

import (
	"strings"
	"testing"
	"time"

	"github.com/colindargent/vecu/client/sync"
)

func TestMenuLabels(t *testing.T) {
	now := time.Date(2026, 7, 26, 18, 0, 0, 0, time.UTC)

	t.Run("jamais synchronisé", func(t *testing.T) {
		l := menuLabels(sync.State{}, nil, nil, nil, now)
		if l.statut != "Jamais synchronisé" {
			t.Errorf("statut = %q", l.statut)
		}
		if l.fichiers != "0 fichiers · 0 espaces" {
			t.Errorf("fichiers = %q", l.fichiers)
		}
		if l.laissesVisible {
			t.Errorf("laisses ne doit pas être visible")
		}
	})

	t.Run("synchronisé, deux espaces", func(t *testing.T) {
		st := sync.State{
			Head:    "abc123",
			Cycle:   now.Add(-3 * time.Minute).Format(time.RFC3339),
			Espaces: []string{"clients", "shared"},
			Files: map[string]string{
				"shared/a.md":    "h1",
				"shared/b.md":    "h2",
				"clients/x/c.md": "h3",
			},
		}
		l := menuLabels(st, nil, nil, nil, now)
		if l.statut != "Synchronisé · il y a 3 minutes" {
			t.Errorf("statut = %q", l.statut)
		}
		if l.fichiers != "3 fichiers · 2 espaces" {
			t.Errorf("fichiers = %q", l.fichiers)
		}
		// Infobulle triée : clients (1) avant shared (2).
		want := "clients : 1 fichiers\nshared : 2 fichiers"
		if l.espacesInfobul != want {
			t.Errorf("infobulle = %q, veut %q", l.espacesInfobul, want)
		}
	})

	t.Run("un espace au singulier", func(t *testing.T) {
		st := sync.State{Head: "x", Cycle: now.Format(time.RFC3339), Espaces: []string{"shared"}, Files: map[string]string{"shared/a.md": "h"}}
		l := menuLabels(st, nil, nil, nil, now)
		if l.fichiers != "1 fichiers · 1 espace" {
			t.Errorf("fichiers = %q", l.fichiers)
		}
	})

	t.Run("fichier laissé = avertissement", func(t *testing.T) {
		laisses := []sync.Laisse{
			{Chemin: "shared/perso.md", Raison: "n'existe que sur ce poste", Genre: sync.GenreFichier},
			{Chemin: "shared/z.md", Raison: "sur le serveur", Genre: sync.GenreServeur},
		}
		l := menuLabels(sync.State{Head: "x"}, laisses, nil, nil, now)
		if !l.laissesVisible {
			t.Fatalf("laisses doit être visible")
		}
		if l.laisses != "⚠ 1 fichier seulement sur ce poste" {
			t.Errorf("laisses = %q", l.laisses)
		}
	})

	// Le cas du vault de Colin : 298 binaires, et rien d'autre. L'avertissement
	// était allumé en permanence depuis le 26/07, donc il ne voulait plus rien
	// dire. Un binaire est hors périmètre, pas en danger.
	t.Run("des hors-périmètre seuls n'allument pas l'avertissement", func(t *testing.T) {
		var laisses []sync.Laisse
		for _, c := range []string{"shared/a.png", "shared/b.jpg", "shared/c.pdf"} {
			laisses = append(laisses, sync.Laisse{Chemin: c, Raison: "fichier binaire", Genre: sync.GenreHorsPerimetre})
		}
		l := menuLabels(sync.State{Head: "x"}, laisses, nil, nil, now)
		if l.laissesVisible {
			t.Errorf("avertissement allumé sur des hors-périmètre : %q", l.laisses)
		}
		// Mais un vrai fichier perdu au milieu reste visible, et lui seul est compté.
		l = menuLabels(sync.State{Head: "x"}, append(laisses,
			sync.Laisse{Chemin: "shared/perso.md", Raison: "n'existe que sur ce poste", Genre: sync.GenreFichier}), nil, nil, now)
		if !l.laissesVisible || l.laisses != "⚠ 1 fichier seulement sur ce poste" {
			t.Errorf("un vrai perdu doit rester visible et seul compté : visible=%v %q", l.laissesVisible, l.laisses)
		}
	})
}

// TestMenuSkillsParCible : le menu dit combien de skills sont utilisables et par
// quels outils. Un skill descendu mais non projeté est invisible pour l'agent,
// donc inutile - c'est ce qui s'est produit chez Achille en juillet sans que
// rien ne le dise.
func TestMenuSkillsParCible(t *testing.T) {
	base := func() sync.State {
		return sync.State{
			Files: map[string]string{
				"shared/skills/daily/SKILL.md":     "x",
				"shared/skills/dev-scope/SKILL.md": "y",
			},
			Espaces: []string{"shared"},
		}
	}
	now := time.Now()

	t.Run("les deux cibles", func(t *testing.T) {
		st := base()
		st.Projetes = []string{"daily", "dev-scope"}
		st.ProjetesAgents = []string{"daily", "dev-scope"}
		l := menuLabels(st, nil, nil, nil, now)
		if !l.skillsVisible || l.skills != "2/2 skills → Claude Code, Cursor et Codex" {
			t.Errorf("skills = %q", l.skills)
		}
	})

	t.Run("Claude seul", func(t *testing.T) {
		st := base()
		st.Projetes = []string{"daily", "dev-scope"}
		l := menuLabels(st, nil, nil, nil, now)
		if l.skills != "2/2 skills → Claude Code" {
			t.Errorf("skills = %q", l.skills)
		}
	})

	t.Run("aucune projection", func(t *testing.T) {
		st := base()
		l := menuLabels(st, nil, nil, nil, now)
		if !l.skillsVisible || l.skills != "2 skills, aucun projeté" {
			t.Errorf("skills = %q", l.skills)
		}
	})

	t.Run("aucun skill monté", func(t *testing.T) {
		l := menuLabels(sync.State{Files: map[string]string{}, Espaces: []string{"shared"}}, nil, nil, nil, now)
		if l.skillsVisible {
			t.Errorf("ligne affichée sans skill monté : %q", l.skills)
		}
	})
}

// TestMenuSignalDeNaissance : F4 se lit dans l'app, c'est le critère du CDC -
// « le signal se lit dans l'app, pas dans une commande que personne ne lance ».
// Le menu porte un COMPTE, le détail va en infobulle : la leçon de F6, prise le
// matin même, est qu'une liste dans un rapport cesse d'être lue.
func TestMenuSignalDeNaissance(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	st := sync.State{Head: "x"}

	t.Run("rien à annoncer, rien à afficher", func(t *testing.T) {
		l := menuLabels(st, nil, nil, nil, now)
		if l.nouveauxVisible {
			t.Errorf("entrée affichée sans nouveau skill : %q", l.nouveaux)
		}
	})

	t.Run("un seul skill, au singulier", func(t *testing.T) {
		l := menuLabels(st, nil, []sync.NouveauSkill{
			{Slug: "revue-de-code", Auteur: "achille", CreeLe: "2026-08-20T10:00:00Z"},
		}, nil, now)
		if !l.nouveauxVisible {
			t.Fatal("l'entrée devrait être visible")
		}
		if !strings.Contains(l.nouveaux, "1 nouveau skill déposé") {
			t.Errorf("libellé = %q", l.nouveaux)
		}
		if l.nouveauxInfo != "revue-de-code - par achille" {
			t.Errorf("infobulle = %q", l.nouveauxInfo)
		}
	})

	t.Run("plusieurs skills : un compte, jamais une liste dans le menu", func(t *testing.T) {
		l := menuLabels(st, nil, []sync.NouveauSkill{
			{Slug: "pdf", Auteur: "achille"},
			{Slug: "revue-de-code", Auteur: "achille"},
			{Slug: "commit", Auteur: "bob"},
		}, nil, now)
		if !strings.Contains(l.nouveaux, "3 nouveaux skills déposés") {
			t.Errorf("libellé = %q", l.nouveaux)
		}
		if strings.Contains(l.nouveaux, "revue-de-code") {
			t.Errorf("le menu énumère les slugs au lieu de les compter : %q", l.nouveaux)
		}
		// Le détail existe quand même, et il est trié pour être stable.
		lignes := strings.Split(l.nouveauxInfo, "\n")
		if len(lignes) != 3 {
			t.Fatalf("infobulle = %q", l.nouveauxInfo)
		}
		if lignes[0] != "commit - par bob" {
			t.Errorf("infobulle non triée : %q", l.nouveauxInfo)
		}
		// L'auteur est porté, jamais la description ni le contenu.
		for _, ligne := range lignes {
			if !strings.Contains(ligne, " - par ") {
				t.Errorf("ligne sans auteur : %q", ligne)
			}
		}
	})

	t.Run("le signal n'allume pas l'avertissement de perte", func(t *testing.T) {
		// F4 est une information, F5/F6 sont des alertes. Les confondre
		// referait exactement ce que F6 vient de corriger.
		l := menuLabels(st, nil, []sync.NouveauSkill{{Slug: "pdf", Auteur: "achille"}}, nil, now)
		if l.laissesVisible {
			t.Errorf("le signal a allumé l'avertissement de perte : %q", l.laisses)
		}
	})
}

// Les copies de conflit locales ne se synchronisent plus : la barre de menus
// est la seule surface où elles apparaissent dans l'app.
func TestMenuCopiesDeConflit(t *testing.T) {
	now := time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC)
	st := sync.State{Head: "x", Files: map[string]string{}, Espaces: []string{"shared"}}

	t.Run("cachée quand il n'y en a aucune", func(t *testing.T) {
		l := menuLabels(st, nil, nil, nil, now)
		if l.copiesVisible || l.copies != "" {
			t.Errorf("rien à dire sans copie : visible=%v %q", l.copiesVisible, l.copies)
		}
	})

	t.Run("compte au singulier et au pluriel, détail en infobulle", func(t *testing.T) {
		l := menuLabels(st, nil, nil, []string{"shared/a (conflit local).md"}, now)
		if !l.copiesVisible || l.copies != "1 copie de conflit à arbitrer" {
			t.Errorf("singulier attendu, obtenu %q", l.copies)
		}
		l = menuLabels(st, nil, nil, []string{"shared/a (conflit local).md", "shared/b (conflit local).md"}, now)
		if l.copies != "2 copies de conflit à arbitrer" {
			t.Errorf("pluriel attendu, obtenu %q", l.copies)
		}
		if !strings.Contains(l.copiesInfo, "shared/a (conflit local).md") ||
			!strings.Contains(l.copiesInfo, "shared/b (conflit local).md") {
			t.Errorf("l'infobulle doit porter les chemins : %q", l.copiesInfo)
		}
	})

	t.Run("n'est jamais un avertissement de perte", func(t *testing.T) {
		l := menuLabels(st, nil, nil, []string{"shared/a (conflit local).md"}, now)
		// L'avertissement `laisses` est réservé au contenu qui n'existe QUE sur
		// ce poste ET que rien ne protège. Une copie est l'inverse : c'est la
		// protection qui a fonctionné.
		if l.laissesVisible {
			t.Errorf("une copie ne doit pas allumer l'avertissement de perte : %q", l.laisses)
		}
	})
}

// Les collisions de casse en barre de menus.
//
// Arbitrage du 23/08 : Colin ne tape jamais `vecu`, et aucune release ne
// remplace le CLI installé (24/07). Une surface utilisateur qui ne vit que
// dans la ligne de commande n'est lue par personne, donc elle vit ici.
func TestMenuLabelsCollisionsDeCasse(t *testing.T) {
	st := sync.State{
		Head:    "abc",
		Espaces: []string{"shared"},
		Files: map[string]string{
			"shared/process/AGENTS.md":          "0b7d130ed990aaaaaaaa",
			"shared/process/agents.md":          "87a0536d8916aaaaaaaa",
			"shared/process/registre-agents.md": "87a0536d8916aaaaaaaa",
			"shared/tasks.md":                   "636f857edb5eaaaaaaaa",
		},
	}
	l := menuLabels(st, nil, nil, nil, time.Now())

	if !l.collisionsVisible {
		t.Fatal("la ligne devrait être visible")
	}
	if l.collisions != "1 collision de casse à arbitrer" {
		t.Errorf("titre = %q", l.collisions)
	}
	// Le menu porte un COMPTE, l'infobulle porte le détail : la leçon de F6.
	for _, attendu := range []string{
		"shared/process/AGENTS.md (état : 0b7d130ed990)",
		"shared/process/agents.md (état : 87a0536d8916)",
	} {
		if !strings.Contains(l.collisionsInfo, attendu) {
			t.Errorf("l'infobulle ne porte pas %q :\n  %s", attendu, l.collisionsInfo)
		}
	}
	// L'homonyme de CONTENU n'est pas une collision : la question porte sur le
	// nom. Sans cette garde, tout doublon de fichier remonterait au menu.
	if strings.Contains(l.collisionsInfo, "registre-agents.md") {
		t.Errorf("un homonyme de contenu est remonté :\n  %s", l.collisionsInfo)
	}
	// Et ce n'est JAMAIS un avertissement : rien n'est perdu, tout est sur le
	// serveur. Un rapport qui crie au loup n'est plus lu.
	if strings.Contains(l.collisions, "⚠") {
		t.Errorf("la ligne s'affiche en avertissement : %q", l.collisions)
	}
}

// Un état sain ne montre rien. C'est ce qui rend la ligne lisible le jour où
// elle porte quelque chose.
func TestMenuLabelsAucuneCollisionSurEtatSain(t *testing.T) {
	st := sync.State{
		Head:    "abc",
		Espaces: []string{"shared"},
		Files: map[string]string{
			"shared/process/AGENTS.md": "a",
			"shared/process/CLAUDE.md": "b",
			"shared/tasks.md":          "c",
		},
	}
	l := menuLabels(st, nil, nil, nil, time.Now())
	if l.collisionsVisible {
		t.Errorf("rien ne devrait s'afficher : %q / %q", l.collisions, l.collisionsInfo)
	}
}

// Plusieurs collisions : le menu compte, il n'énumère pas.
func TestMenuLabelsPlusieursCollisionsSeComptent(t *testing.T) {
	st := sync.State{
		Head:    "abc",
		Espaces: []string{"shared"},
		Files: map[string]string{
			"shared/a/Notes.md": "1", "shared/a/notes.md": "2",
			"shared/b/Plan.md": "3", "shared/b/plan.md": "4",
		},
	}
	l := menuLabels(st, nil, nil, nil, time.Now())
	if l.collisions != "2 collisions de casse à arbitrer" {
		t.Errorf("titre = %q", l.collisions)
	}
}
