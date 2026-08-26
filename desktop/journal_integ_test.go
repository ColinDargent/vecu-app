package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestFiltre_SurUnVraiJournal : la passe mesurée sur un VRAI journal, à sa vraie
// taille, avec une référence indépendante.
//
// Les tests unitaires prouvent la règle sur quelques lignes. Ils ne prouvent
// rien sur 3,3 Go et 17,6 millions de lignes : ni que le flux tient sans
// exploser la mémoire, ni que le compte final est le bon. Or c'est exactement là
// que la boundary de la spec porte - on ne remplace l'original qu'après avoir
// vérifié le compte de lignes, parce que les ~1 200 lignes utiles sont les
// seules traces qu'on ait depuis le 26/07, dont la fenêtre de la perte du 10/08.
//
// Se skip proprement quand aucun journal n'est désigné : la preuve n'a pas de
// sens sur un fichier inventé.
//
//	VECU_JOURNAL_REEL=/chemin/vers/une/COPIE.log go test ./desktop/ -run TestFiltre_SurUnVraiJournal -v
//
// Toujours une COPIE : la passe réécrit le fichier qu'on lui donne.
func TestFiltre_SurUnVraiJournal(t *testing.T) {
	chemin := os.Getenv("VECU_JOURNAL_REEL")
	if chemin == "" {
		t.Skip("VECU_JOURNAL_REEL non défini : rien à mesurer")
	}
	if _, err := os.Stat(chemin); err != nil {
		t.Skipf("journal introuvable : %v", err)
	}

	// Référence INDÉPENDANTE, prise avant la passe avec un autre outil que le
	// nôtre. Se comparer à soi-même ne prouve rien.
	total := compteAvecGrep(t, chemin, "-c", "")
	bruitAttendu := compteAvecGrep(t, chemin, "-c", motifBruitJournal)
	utilesAttendues := total - bruitAttendu
	tailleAvant := tailleFichier(chemin)
	t.Logf("avant : %s, %d lignes dont %d de bruit et %d utiles",
		tailleLisible(tailleAvant), total, bruitAttendu, utilesAttendues)

	garde, jete, err := filtreEnPlace(chemin, motifBruitJournal)
	if err != nil {
		t.Fatalf("la passe a échoué : %v", err)
	}
	t.Logf("après : %s, %d lignes conservées, %d écartées", tailleLisible(tailleFichier(chemin)), garde, jete)

	if jete != bruitAttendu {
		t.Errorf("bruit écarté = %d, attendu %d", jete, bruitAttendu)
	}
	// LE critère de la spec : aucune ligne autre que le motif de bruit n'a
	// disparu.
	if garde != utilesAttendues {
		t.Errorf("lignes conservées = %d, attendu %d : %d ligne(s) utile(s) ont été emportées avec le bruit",
			garde, utilesAttendues, utilesAttendues-garde)
	}
	// Et vérifié sur le fichier résultant, pas seulement sur ce que la passe
	// dit d'elle-même.
	if reste := compteAvecGrep(t, chemin, "-c", motifBruitJournal); reste != 0 {
		t.Errorf("%d lignes de bruit sont restées", reste)
	}
	if apres := compteAvecGrep(t, chemin, "-c", ""); apres != utilesAttendues {
		t.Errorf("le fichier final porte %d lignes, attendu %d", apres, utilesAttendues)
	}
}

// compteAvecGrep : compte via grep/wc, jamais via notre propre code.
func compteAvecGrep(t *testing.T, chemin, flag, motif string) int {
	t.Helper()
	var sortie []byte
	var err error
	if motif == "" {
		f, e := os.Open(chemin)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		cmd := exec.Command("wc", "-l")
		cmd.Stdin = f
		sortie, err = cmd.Output()
	} else {
		sortie, err = exec.Command("grep", flag, motif, chemin).Output()
		// grep sort en 1 quand il ne trouve rien : ce n'est pas une erreur ici.
		if err != nil && len(sortie) > 0 {
			err = nil
		}
	}
	if err != nil {
		t.Fatalf("comptage : %v", err)
	}
	n, e := strconv.Atoi(strings.TrimSpace(string(sortie)))
	if e != nil {
		t.Fatalf("comptage illisible %q : %v", sortie, e)
	}
	return n
}
