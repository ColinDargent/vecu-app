package sync

import (
	"fmt"
	"strings"
	"testing"
)

// Le banc de DAR-166 : un chemin CRÉÉ SUR CE POSTE, réécrit entre le push et le
// pull du même cycle, doit être REPORTÉ et pas copié.
//
// Ce que la production a mesuré le 31/08, six fois entre 12:17 et 12:25 :
//
//	édition locale non poussée préservée : shared/projects/AGENTS.md
//	  -> shared/projects/AGENTS (conflit local).md
//	  — local 2f7d03c66795 6980o, connu (aucune), entrant 3a88d3202db6
//	  ; report refusé : chemin non suivi
//
// La cause lue dans le code : `pushLocal` inscrit `Versions[p]` sur un envoi
// propre et n'inscrit PAS `Files[p]`, or `refusDeReport` teste `Files` en
// premier. Le chemin est donc « non suivi » jusqu'à ce qu'un pull le
// redescende, et tant qu'il l'est, la garde de course n'est jamais évaluée.
//
// La fenêtre est étroite - entre le push et le pull du MÊME cycle - et c'est
// pour ça qu'elle frappe une refonte d'arborescence et pas l'usage courant.
// C'est aussi pourquoi ce test a besoin de la couture `entrePushEtPull` : aucun
// enchaînement d'appels publics ne l'atteint.
func TestCheminNeufReecritPendantLeCycleEstReporte(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)
	var journal []string
	e.logf = func(f string, args ...any) { journal = append(journal, fmt.Sprintf(f, args...)) }

	// Un premier cycle pour monter l'espace. Le chemin du banc n'existe pas
	// encore : il doit être neuf au cycle instrumenté, sans quoi le pull l'aura
	// déjà inscrit dans `Files` et le défaut ne peut pas se produire.
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle de montage : %v", err)
	}

	const rel = "equipe/neuf.md"
	writeFile(t, dir, rel, "v1\n")
	if _, suivi := e.state.Files[rel]; suivi {
		t.Fatalf("précondition du banc : %s est déjà suivi, le chemin n'est pas neuf", rel)
	}

	// La seconde version arrive APRÈS que le serveur a pris la première, et
	// AVANT que le pull du même cycle ne la redescende. C'est exactement le
	// geste de l'agent qui réécrit un fichier qu'il vient de créer.
	e.entrePushEtPull = func() { writeFile(t, dir, rel, "v2\n") }
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle instrumenté : %v", err)
	}
	e.entrePushEtPull = nil

	if copies := copiesDeConflit(t, dir, "equipe"); len(copies) != 0 {
		t.Errorf("copie de conflit sur un chemin neuf réécrit pendant le cycle : %v", copies)
	}
	for _, l := range journal {
		if strings.Contains(l, "report refusé : chemin non suivi") {
			t.Errorf("report refusé sur un chemin que ce poste vient de pousser : %q", l)
		}
	}
	if local, _ := readFile(t, dir, rel); local != "v2\n" {
		t.Errorf("le contenu local n'a pas été laissé intact : %q", local)
	}

	// Le bon geste, celui qui existe déjà et que la garde court-circuitait.
	var reporte bool
	for _, l := range journal {
		if strings.Contains(l, rel) && strings.Contains(l, "non appliqué ce cycle") {
			reporte = true
		}
	}
	if !reporte {
		t.Errorf("le chemin n'a pas été reporté ; journal : %v", journal)
	}
}

// La garde de DAR-166 : la reconnaissance par chemin compare des CONTENUS, et
// pas seulement l'appartenance à `acceptes`.
//
// Le cas est celui d'un conflit serveur : ce poste a poussé sa version, le
// serveur a gardé celle de l'autre membre et rangé la nôtre dans une copie à
// lui. Le chemin est donc bien dans `acceptes` - il est parti - mais ce que le
// serveur renvoie n'est PAS ce qu'on a envoyé. Le reconnaître ici le rendrait
// reportable, et le report ferait rester la version locale à la place de la
// version canonique : la divergence deviendrait permanente au lieu d'être
// arbitrée.
//
// C'est la mutation qui compte sur ce lot. Retirer `envoye == incoming` de la
// condition laisse passer tous les autres tests ; celui-ci tombe.
func TestConflitServeurNEstPasUneReconnaissance(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	const rel = "equipe/duel.md"

	// B crée le chemin et le pousse : le serveur porte « de B ».
	writeFile(t, dirB, rel, "de B\n")
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle de B : %v", err)
	}

	// A crée le MÊME chemin sans l'avoir jamais vu, puis le réécrit entre le
	// push et le pull. Son push part donc en conflit, et la course est ouverte.
	writeFile(t, dirA, rel, "de A\n")
	a.entrePushEtPull = func() { writeFile(t, dirA, rel, "de A v2\n") }
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle instrumenté de A : %v", err)
	}
	a.entrePushEtPull = nil

	// Le canonique gagne sa place : c'est le contenu du serveur qui doit être
	// sur le disque de A à la fin du cycle.
	if local, _ := readFile(t, dirA, rel); local != "de B\n" {
		t.Errorf("A n'a pas reçu la version canonique du serveur : %q", local)
	}
	// Et rien de local n'est détruit pour autant. Deux copies arrivent ici, et
	// les deux sont justes - c'est le modèle Dropbox appliqué aux deux étages :
	//   - celle du SERVEUR, qui porte le « de A » poussé et refusé au nom
	//     canonique, et qui redescend par le pull ;
	//   - celle du POSTE, qui porte le « de A v2 » écrit pendant le cycle, que
	//     `preserveLocalEditIfAny` met de côté avant d'écrire le canonique.
	var local, serveur string
	for _, c := range copiesDeConflit(t, dirA, "equipe") {
		contenu, _ := readFile(t, dirA, "equipe/"+c)
		switch contenu {
		case "de A v2\n":
			local = c
		case "de A\n":
			serveur = c
		default:
			t.Errorf("copie au contenu inattendu (%s) : %q", c, contenu)
		}
	}
	if local == "" {
		t.Error("l'édition arrivée pendant le cycle n'a pas été préservée")
	}
	if serveur == "" {
		t.Error("la version poussée et refusée n'est pas redescendue en copie serveur")
	}
}

// L'invariant que les deux écritures de la reconnaissance doivent tenir : toute
// entrée de `Files` a un marque-page non vide dans `Versions`.
//
// `migreVersions` en dépend pour distinguer un état neuf d'un état écrit par une
// version antérieure du client. Une entrée de `Files` sans marque-page est le
// seul état que cette migration confond avec un état d'avant - et elle y répond
// en redistribuant le head à tous les chemins, c'est-à-dire en reconstruisant la
// perte que le marque-page existe pour empêcher.
func TestReconnaissanceTientLInvariantFilesVersions(t *testing.T) {
	url, token := testServer(t)
	e, dir := newEngine(t, url, token)

	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle de montage : %v", err)
	}
	const rel = "equipe/neuf.md"
	writeFile(t, dir, rel, "v1\n")
	e.entrePushEtPull = func() { writeFile(t, dir, rel, "v2\n") }
	if err := e.SyncOnce(); err != nil {
		t.Fatalf("cycle instrumenté : %v", err)
	}
	e.entrePushEtPull = nil

	// La précondition du test : le chemin a bien été reconnu par ce cycle. Sans
	// elle, l'invariant tiendrait trivialement sur un état où rien n'a été écrit.
	if _, suivi := e.state.Files[rel]; !suivi {
		t.Fatalf("précondition : %s n'a pas été reconnu, l'invariant ne prouve rien", rel)
	}
	for p := range e.state.Files {
		if e.state.Versions[p] == "" {
			t.Errorf("entrée de Files sans marque-page : %s", p)
		}
	}
}
