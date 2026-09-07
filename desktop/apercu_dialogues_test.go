package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApercuDialogues écrit les sept scripts de dialogue sur le disque, pour
// qu'une machine puisse les AFFICHER et les photographier.
//
// Pourquoi un test et pas un outil : les constructeurs de scripts vivent dans le
// `package main` de `desktop`, donc rien d'extérieur ne peut les appeler. Un
// test est le seul point d'entrée qui les voit, et il ne coûte rien puisqu'il ne
// fait rien sans la variable.
//
// Ce que ça permet, et qui manquait : la CI vérifiait la SYNTAXE des scripts
// (parseur PowerShell) et pas leur RENDU. Un dialogue peut être syntaxiquement
// parfait et s'afficher avec un bouton hors du cadre, un texte coupé ou une
// fenêtre derrière les autres. C'est la différence entre « ça compile » et
// « ça se voit », et elle demandait jusqu'ici un humain devant un écran.
func TestApercuDialogues(t *testing.T) {
	dest := os.Getenv("VECU_APERCU")
	if dest == "" {
		t.Skip("aperçu non demandé (poser VECU_APERCU=<dossier>)")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}

	// Des textes RÉELS, repris des appelants, et non du lorem : c'est la longueur
	// et les accents qui font déborder un libellé d'un cadre.
	scripts := map[string]string{
		"1-choisir-dossier": psScriptChoisirDossier(
			"Choisir le dossier à partager. Il restera où il est."),
		"2-informe": psScriptInforme(
			"Vécu est installé. L'icône est dans la zone de notification, en bas à droite. " +
				"La première synchronisation démarre maintenant."),
		"3-confirme": psScriptBoutons(
			"Retirer « équipe » de ce poste ? Le dossier reste sur le serveur et chez les autres membres.",
			[]string{"Retirer", "Annuler"}),
		"4-choix-trois": psScriptBoutons(
			"Ce dossier est déjà suivi par un autre synchroniseur.",
			[]string{"Continuer quand même", "Choisir un autre dossier", "Annuler"}),
		"5-demande-texte": psScriptDemandeTexte(
			"Sous quel nom ce dossier apparaîtra-t-il pour les autres ?", "notes-équipe"),
		"6-choisir-liste": psScriptChoisirDansListe(
			"Quel dossier voulez-vous retirer de ce poste ?",
			[]string{"shared", "équipe", "clients", "knowledge", "planning"}),
		"7-mot-de-passe": psScriptDemandeMotDePasse(
			"Mot de passe de votre compte sur ce serveur."),
	}

	for nom, corps := range scripts {
		chemin := filepath.Join(dest, nom+".ps1")
		if err := os.WriteFile(chemin, []byte(preambulePS+corps), 0o644); err != nil {
			t.Fatalf("%s : %v", nom, err)
		}
	}
	t.Logf("%d scripts écrits dans %s", len(scripts), dest)
}
