package sync

import "testing"

// TestStateCloneIndependant : la copie rendue par clone ne partage aucune
// structure mutable avec l'original. Muter l'un ne doit jamais toucher l'autre
// (c'est la garantie qui rend State() sûr en lecture concurrente).
func TestStateCloneIndependant(t *testing.T) {
	orig := State{
		Head:           "h",
		Files:          map[string]string{"shared/a.md": "x"},
		Versions:       map[string]string{"shared/a.md": "oid-1"},
		HorsPerimetre:  map[string]string{"shared/img.png": "sha-1"},
		Espaces:        []string{"shared"},
		Connus:         []string{"shared"},
		Projetes:       []string{"skill-a"},
		ProjetesAgents: []string{"skill-a"},
		Laisses:        []Laisse{{Chemin: "shared/z.md", Genre: GenreFichier}},
	}
	c := orig.clone()

	// Muter la copie.
	c.Files["shared/a.md"] = "MUTÉ"
	c.Files["shared/b.md"] = "nouveau"
	c.Versions["shared/a.md"] = "MUTÉ"
	c.Versions["shared/b.md"] = "oid-2"
	c.HorsPerimetre["shared/img.png"] = "MUTÉ"
	c.HorsPerimetre["shared/autre.png"] = "sha-2"
	c.Espaces[0] = "MUTÉ"
	c.Laisses[0].Chemin = "MUTÉ"
	c.Projetes[0] = "MUTÉ"
	c.ProjetesAgents[0] = "MUTÉ"

	if orig.Files["shared/a.md"] != "x" {
		t.Errorf("la map d'origine a été mutée : %q", orig.Files["shared/a.md"])
	}
	if _, ok := orig.Files["shared/b.md"]; ok {
		t.Errorf("un ajout à la copie a fui vers l'origine")
	}
	if orig.Versions["shared/a.md"] != "oid-1" {
		t.Errorf("la map Versions d'origine a été mutée : %q", orig.Versions["shared/a.md"])
	}
	if _, ok := orig.Versions["shared/b.md"]; ok {
		t.Errorf("un ajout à Versions sur la copie a fui vers l'origine")
	}
	// Sans cette copie profonde, la barre de menus lirait la map pendant qu'un
	// cycle y écrit sous verrou - lecture+écriture concurrente de map, que Go
	// fait paniquer. Le verrou ne suffit pas : la valeur rendue doit être détachée.
	if orig.HorsPerimetre["shared/img.png"] != "sha-1" {
		t.Errorf("la map HorsPerimetre d'origine a été mutée : %q", orig.HorsPerimetre["shared/img.png"])
	}
	if _, ok := orig.HorsPerimetre["shared/autre.png"]; ok {
		t.Errorf("un ajout à HorsPerimetre sur la copie a fui vers l'origine")
	}
	if orig.Espaces[0] != "shared" {
		t.Errorf("le slice Espaces d'origine a été muté : %q", orig.Espaces[0])
	}
	if orig.Laisses[0].Chemin != "shared/z.md" {
		t.Errorf("le slice Laisses d'origine a été muté : %q", orig.Laisses[0].Chemin)
	}
	if orig.Projetes[0] != "skill-a" {
		t.Errorf("le slice Projetes d'origine a été muté : %q", orig.Projetes[0])
	}
	if orig.ProjetesAgents[0] != "skill-a" {
		t.Errorf("le slice ProjetesAgents d'origine a été muté : %q", orig.ProjetesAgents[0])
	}
}
