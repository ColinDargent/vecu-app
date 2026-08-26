package perms

import "testing"

func TestDefaultWhenNoRule(t *testing.T) {
	if got := Effective("a/b.md", Lecture, nil); got != Lecture {
		t.Errorf("attendu Lecture par défaut, obtenu %v", got)
	}
}

func TestDeepestAncestorWins(t *testing.T) {
	rules := []Rule{
		{Path: "projets", Level: Ecriture},
		{Path: "projets/secret", Level: Invisible},
	}
	// La règle profonde (projets/secret) l'emporte sur la peu profonde (projets).
	if got := Effective("projets/secret/plan.md", Invisible, rules); got != Invisible {
		t.Errorf("attendu Invisible, obtenu %v", got)
	}
	// Un frère non couvert par la règle profonde garde le niveau du parent.
	if got := Effective("projets/public/plan.md", Invisible, rules); got != Ecriture {
		t.Errorf("attendu Ecriture, obtenu %v", got)
	}
}

func TestOverrideBothDirections(t *testing.T) {
	// Défaut invisible (utilisateur restreint), étendu sur un dossier,
	// puis re-restreint sur un sous-dossier, puis ré-étendu plus profond.
	rules := []Rule{
		{Path: "clients", Level: Lecture},             // étend
		{Path: "clients/vdf", Level: Invisible},       // restreint
		{Path: "clients/vdf/public", Level: Ecriture}, // ré-étend
	}
	cases := []struct {
		target string
		want   Level
	}{
		{"autre.md", Invisible},                   // défaut racine
		{"clients/note.md", Lecture},              // règle clients
		{"clients/vdf/prive.md", Invisible},       // restriction vdf
		{"clients/vdf/public/brief.md", Ecriture}, // ré-extension public
	}
	for _, c := range cases {
		if got := Effective(c.target, Invisible, rules); got != c.want {
			t.Errorf("Effective(%q) = %v, attendu %v", c.target, got, c.want)
		}
	}
}

func TestExplicitRootRuleOverridesDefault(t *testing.T) {
	// Une Rule{Path:""} explicite remplace le defaultLevel.
	rules := []Rule{{Path: "", Level: Ecriture}}
	if got := Effective("a/b.md", Invisible, rules); got != Ecriture {
		t.Errorf("règle racine explicite ignorée : %v", got)
	}
}

func TestSegmentBoundary(t *testing.T) {
	// "a/b" ne doit pas couvrir "a/bc.md" (préfixe de chaîne, pas d'ancêtre).
	rules := []Rule{{Path: "a/b", Level: Ecriture}}
	if got := Effective("a/bc.md", Invisible, rules); got != Invisible {
		t.Errorf("faux ancêtre par préfixe de chaîne : %v", got)
	}
	// Mais "a/b" couvre bien "a/b" lui-même et ses descendants.
	if got := Effective("a/b", Invisible, rules); got != Ecriture {
		t.Errorf("le chemin exact devrait être couvert : %v", got)
	}
	if got := Effective("a/b/deep.md", Invisible, rules); got != Ecriture {
		t.Errorf("le descendant devrait être couvert : %v", got)
	}
}

func TestRuleOnFileItself(t *testing.T) {
	rules := []Rule{{Path: "notes/prive.md", Level: Invisible}}
	if got := Effective("notes/prive.md", Ecriture, rules); got != Invisible {
		t.Errorf("règle sur le fichier même ignorée : %v", got)
	}
}

func TestCanReadCanWrite(t *testing.T) {
	rules := []Rule{{Path: "ro", Level: Lecture}, {Path: "hidden", Level: Invisible}}
	if !CanRead("ro/f.md", Ecriture, rules) || CanWrite("ro/f.md", Ecriture, rules) {
		t.Error("ro/f.md devrait être lisible mais pas inscriptible")
	}
	if CanRead("hidden/f.md", Ecriture, rules) {
		t.Error("hidden/f.md ne devrait pas être lisible")
	}
}

func TestCanonicalFormResolution(t *testing.T) {
	// Cibles non canoniques : slash initial, slash final, segments redondants.
	// La restriction doit tenir quelle que soit la forme de la cible.
	rules := []Rule{{Path: "clients", Level: Invisible}}
	for _, target := range []string{
		"clients/x.md", "/clients/x.md", "clients/./x.md", "clients//x.md", "clients/sub/../x.md",
	} {
		if got := Effective(target, Lecture, rules); got != Invisible {
			t.Errorf("cible %q contourne la restriction : %v", target, got)
		}
	}
}

func TestEqualDepthFailClosed(t *testing.T) {
	// Deux règles au même chemin canonique (source non canonique en amont) :
	// le départage prend le plus restrictif, quel que soit l'ordre.
	a := []Rule{{Path: "clients", Level: Invisible}, {Path: "clients/", Level: Ecriture}}
	b := []Rule{{Path: "clients/", Level: Ecriture}, {Path: "clients", Level: Invisible}}
	if Effective("clients/x.md", Lecture, a) != Invisible {
		t.Error("ordre A : le plus permissif l'a emporté")
	}
	if Effective("clients/x.md", Lecture, b) != Invisible {
		t.Error("ordre B : le plus permissif l'a emporté")
	}
}

func TestCanon(t *testing.T) {
	cases := map[string]string{
		"": "", "/": "", ".": "", "a": "a", "/a": "a", "a/": "a",
		"a/b": "a/b", "a//b": "a/b", "a/./b": "a/b", "a/c/../b": "a/b",
	}
	for in, want := range cases {
		if got := Canon(in); got != want {
			t.Errorf("Canon(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestParseAndString(t *testing.T) {
	for _, s := range []string{"invisible", "lecture", "ecriture"} {
		l, ok := ParseLevel(s)
		if !ok || l.String() != s {
			t.Errorf("round-trip cassé pour %q : %v %v", s, l, ok)
		}
	}
	if _, ok := ParseLevel("admin"); ok {
		t.Error("niveau inconnu accepté")
	}
}
