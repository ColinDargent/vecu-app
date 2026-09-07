package main

import (
	"strings"
	"testing"
)

// TestLePlistPointeSurLeJournalApplicatif : le plist est le seul endroit qui
// décide où vont stdout/stderr. La slice 3 ne le touche pas - c'est tout son
// intérêt - et ce test le vérifie plutôt que de le supposer.
func TestLePlistPointeSurLeJournalApplicatif(t *testing.T) {
	c := serviceCfg{
		label:   "com.exemple.vecu",
		binaire: "/Applications/Vécu.app/Contents/MacOS/vecu",
		racine:  "/Users/x/vault",
		journal: cheminJournal(),
	}
	plist := c.contenuPlist()
	if !strings.Contains(plist, cheminJournal()) {
		t.Errorf("le plist ne redirige pas vers le journal applicatif :\n%s", plist)
	}
	if strings.Contains(plist, cheminJournalSync()) {
		t.Errorf("le plist redirige vers le journal du MOTEUR : launchd et la rotation se disputeraient le même fichier\n%s", plist)
	}
}
