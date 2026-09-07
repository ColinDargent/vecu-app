package sync

import (
	"strings"
	"testing"
)

// LE BANC DE DAR-114 : le revert d'un arbre entier SANS copie de conflit.
//
// Ce que la production a montré le 10/08 à 10:26 : tout le dossier `shared/`
// est revenu à l'état du pair distant. Perdu d'un coup, dix-huit cartes taguées,
// quatre cartes créées la veille, une règle posée dans un skill. Tout ce qui
// vivait HORS du dossier synchronisé a survécu intact.
//
// Le point qui distingue ce ticket de DAR-166, et qui a résisté trois semaines :
// AUCUNE copie de conflit n'a été produite. Il n'y a donc eu ni préservation, ni
// motif journalisé - l'instrumentation de DAR-166 n'aide pas ici. Le garde-fou
// annoncé par le vault (« rien n'est jamais écrasé silencieusement ») n'a pas
// joué, et c'est ça qu'il faut expliquer.
//
// L'hypothèse que ce banc met à l'épreuve, formulée dans le ticket : « un pair
// qui se resynchronise depuis un état ancien gagne l'arbitrage sur tout le
// dossier, au lieu de produire des copies fichier par fichier ».
//
// Ce que la lecture du serveur y ajoute, et qui en fait une hypothèse testable :
// `storage.Write` a un CHEMIN RAPIDE. Quand `baseOID == head`, il n'y a ni
// fusion ni détection de conflit, seulement un `writeLocked` qui remplace. Le
// serveur fait donc confiance à l'ancêtre déclaré par le client, sans jamais
// vérifier que le contenu envoyé en descend. Un poste qui déclare un ancêtre à
// jour avec un contenu qui ne l'est pas écrase silencieusement - exactement le
// symptôme, exactement l'absence de copie.
func TestRevertGlobalSansCopieDeConflit(t *testing.T) {
	url, token := testServer(t)
	a, dirA := newEngine(t, url, token)
	b, dirB := newEngine(t, url, token)

	const rel = "equipe/tasks.md"
	writeFile(t, dirA, rel, "ligne 1\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A : %v", err)
	}
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B : %v", err)
	}
	if got, _ := readFile(t, dirB, rel); got != "ligne 1\n" {
		t.Fatalf("précondition : B n'a pas reçu le fichier (%q)", got)
	}

	// A écrit, pousse, et VÉRIFIE que son écriture a pris. C'est le « l'édition
	// s'applique, l'outil confirme l'écriture » du ticket.
	writeFile(t, dirA, rel, "ligne 1\nédition de A\n")
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A (édition) : %v", err)
	}
	teteApresA := a.state.Head
	if teteApresA == "" {
		t.Fatal("précondition : A n'a pas de tête après son push")
	}

	// B N'A PAS VU L'ÉDITION DE A - son disque porte encore l'état d'avant -
	// MAIS son marque-page dit qu'il est à jour.
	//
	// C'est l'état exact que ce banc met à l'épreuve, et il n'est pas
	// hypothétique : avant le marque-page PAR FICHIER (v0.5.0, 19/08), le client
	// déclarait sa tête globale comme ancêtre de CHAQUE chemin. Un poste dont la
	// tête avait avancé sans que ce chemin-là ne redescende était donc dans cet
	// état par construction - et le 10/08 est antérieur au 19/08.
	b.state.Versions[rel] = teteApresA
	writeFile(t, dirB, rel, "ligne 1\nédition de B\n")

	var journal []string
	b.logf = func(f string, args ...any) { journal = append(journal, f) }
	if err := b.SyncOnce(); err != nil {
		t.Fatalf("cycle B (édition) : %v", err)
	}

	// A resynchronise et relit son fichier. C'est le « relecture plus tard :
	// l'édition n'y est plus » du ticket.
	if err := a.SyncOnce(); err != nil {
		t.Fatalf("cycle A (relecture) : %v", err)
	}
	// LE CONTRAT, ET IL A DEUX MOITIÉS. Perdre l'arbitrage n'est pas le défaut
	// de DAR-114 - deux pairs qui écrivent le même fichier, l'un des deux doit
	// céder. Le défaut est de perdre du travail SANS TRACE.
	//
	// Après la garde d'écho, les deux moitiés doivent tenir ensemble :
	// la version canonique est celle que le serveur portait, et celle du poste
	// qui parlait dans le vide atterrit à côté, lisible.
	final, _ := readFile(t, dirA, rel)
	if !strings.Contains(final, "édition de A") {
		t.Errorf("l'écriture confirmée de A a été écrasée : %q\n  journal de B : %v", final, journal)
	}

	// Et l'écriture de B n'est pas perdue non plus : elle est quelque part, sous
	// un nom de copie, sur l'un des deux postes.
	var retrouvee bool
	for _, dir := range []string{dirA, dirB} {
		for _, c := range copiesDeConflit(t, dir, "equipe") {
			if contenu, _ := readFile(t, dir, "equipe/"+c); strings.Contains(contenu, "édition de B") {
				retrouvee = true
			}
		}
	}
	if !retrouvee {
		t.Errorf("l'écriture de B a disparu sans copie : le garde-fou du vault "+
			"(« rien n'est jamais écrasé silencieusement ») ne tient que d'un côté.\n"+
			"  copies chez A : %v\n  copies chez B : %v",
			copiesDeConflit(t, dirA, "equipe"), copiesDeConflit(t, dirB, "equipe"))
	}
}
