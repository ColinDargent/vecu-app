// Commande vecu-release : outillage de publication de l'app de bureau Vécu.
//
//	vecu-release keygen [-out CHEMIN]
//	    Génère une paire de clés ed25519. Écrit la clé PRIVÉE (base64) dans un
//	    fichier hors dépôt (0600) et imprime la clé PUBLIQUE base64 à coller dans
//	    desktop/update.go (const clePubliqueReleaseB64).
//
//	vecu-release build -version vX.Y.Z [-key CHEMIN] [-targets ...] [-out appdist]
//	    Cross-compile le binaire desktop pour chaque cible, calcule les
//	    empreintes, SCELLE le manifeste avec la clé privée, et dépose manifeste +
//	    binaires dans -out (défaut « appdist »). La publication se termine par un
//	    git add/commit/push : le serveur redéployé sert la nouvelle version.
//
// La clé privée ne doit JAMAIS entrer dans le dépôt.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/colindargent/vecu/appupdate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "build":
		build(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage : vecu-release <keygen|build> [options]")
	os.Exit(2)
}

func cheminCleDefaut() string {
	maison, _ := os.UserHomeDir()
	return filepath.Join(maison, ".config", "vecu", "release_ed25519.key")
}

func keygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", cheminCleDefaut(), "chemin du fichier de clé privée (hors dépôt)")
	fs.Parse(args)

	if _, err := os.Stat(*out); err == nil {
		fatal("une clé existe déjà à %s (la remplacer effacerait la capacité de signer les versions déjà publiées) — supprimer manuellement pour régénérer", *out)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal("génération : %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		fatal("dossier de clé : %v", err)
	}
	privB64 := base64.StdEncoding.EncodeToString(priv)
	if err := os.WriteFile(*out, []byte(privB64+"\n"), 0o600); err != nil {
		fatal("écriture de la clé : %v", err)
	}
	fmt.Printf("clé privée écrite : %s (0600, hors dépôt)\n", *out)
	fmt.Printf("\nColler cette clé publique dans desktop/update.go :\n\n")
	fmt.Printf("const clePubliqueReleaseB64 = %q\n", base64.StdEncoding.EncodeToString(pub))
}

func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	version := fs.String("version", "", "version à publier, format vX.Y.Z (obligatoire)")
	keyPath := fs.String("key", cheminCleDefaut(), "chemin de la clé privée")
	targetsCSV := fs.String("targets", "darwin-arm64,darwin-amd64", "cibles à construire, séparées par des virgules")
	out := fs.String("out", "appdist", "dossier de sortie (servi par le serveur)")
	strict := fs.Bool("strict", false, "échouer si une cible ne se construit pas (défaut : avertir et l'omettre)")
	fs.Parse(args)

	if *version == "" {
		fatal("-version obligatoire (format vX.Y.Z)")
	}
	if _, ok := parseVersionRelease(*version); !ok {
		fatal("version %q invalide : attendu vX.Y.Z", *version)
	}
	priv, err := chargeClePrivee(*keyPath)
	if err != nil {
		fatal("clé privée : %v", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal("dossier de sortie : %v", err)
	}

	m := appupdate.Manifest{
		Version: *version,
		Assets:  map[string]appupdate.Asset{},
		Bundles: map[string]appupdate.Asset{},
	}
	for _, cible := range strings.Split(*targetsCSV, ",") {
		cible = strings.TrimSpace(cible)
		if cible == "" {
			continue
		}
		// Deux constructions par cible, et c'est assumé. Le binaire nu suit
		// exactement le chemin d'avant, octet pour octet, ce qui garantit que la
		// mise à jour d'un poste en v0.8.0 ne change pas d'un iota. Le bundle est
		// construit à part, parce que le sceau MODIFIE le binaire qu'il contient :
		// extraire l'un de l'autre coupterait un aller-retour de raisonnement à
		// chaque release pour économiser quelques secondes de compilation.
		bin, err := compile(cible, *version)
		if err != nil {
			if *strict {
				fatal("construction %s : %v", cible, err)
			}
			fmt.Fprintf(os.Stderr, "! %s omis (échec de construction) : %v\n", cible, err)
			continue
		}
		dest := filepath.Join(*out, "vecu-app-"+cible)
		if err := os.WriteFile(dest, bin, 0o755); err != nil {
			fatal("écriture %s : %v", dest, err)
		}
		m.Assets[cible] = appupdate.Asset{SHA256: appupdate.SumHex(bin), Size: int64(len(bin))}
		fmt.Printf("✓ %s binaire (%d octets)\n", cible, len(bin))

		// Pas de bundle sur Windows : il n'y a pas de `.app` à emballer, le `.exe`
		// est la livraison. `desktop/update.go` demande déjà le bundle puis
		// retombe sur le binaire nu, donc un poste Windows suit ce second chemin
		// sans rien de spécial à écrire côté client.
		if goosDe(cible) == "windows" {
			fmt.Printf("· %s sans bundle (le binaire nu est la livraison)\n", cible)
			continue
		}

		arch, err := construitBundle(cible, *version)
		if err != nil {
			if *strict {
				fatal("bundle %s : %v", cible, err)
			}
			fmt.Fprintf(os.Stderr, "! bundle %s omis : %v\n", cible, err)
			continue
		}
		destB := filepath.Join(*out, "vecu-app-"+cible+".tgz")
		if err := os.WriteFile(destB, arch, 0o644); err != nil {
			fatal("écriture %s : %v", destB, err)
		}
		m.Bundles[cible] = appupdate.Asset{SHA256: appupdate.SumHex(arch), Size: int64(len(arch))}
		fmt.Printf("✓ %s bundle  (%d octets)\n", cible, len(arch))
	}
	if len(m.Assets) == 0 {
		fatal("aucune cible construite — rien à publier")
	}
	// Assets peuplé sans Bundles est une release de repli, pas une erreur : les
	// postes récents attendront la suivante plutôt que d'installer n'importe quoi.
	// Le dire fort, parce que c'est silencieux autrement.
	if len(m.Bundles) == 0 && contientDarwin(m.Assets) {
		fmt.Fprintln(os.Stderr, "! aucun bundle construit : les postes macOS en v0.9.0+ ne trouveront rien à installer")
	}

	sm, err := appupdate.Seal(priv, m)
	if err != nil {
		fatal("scellement du manifeste : %v", err)
	}
	data, err := json.MarshalIndent(sm, "", "  ")
	if err != nil {
		fatal("sérialisation : %v", err)
	}
	manifPath := filepath.Join(*out, "manifest.json")
	if err := os.WriteFile(manifPath, append(data, '\n'), 0o644); err != nil {
		fatal("écriture du manifeste : %v", err)
	}
	fmt.Printf("✓ manifeste scellé : %s (%s)\n", manifPath, *version)
	fmt.Printf("\nPublier : git add %s && git commit -m \"release app %s\" && git push\n", *out, *version)
}

// compile cross-compile le binaire desktop pour une cible « os-arch » avec CGO
// (systray l'exige), en injectant la version via ldflags. Sur macOS, la cible
// amd64 depuis un hôte arm64 passe par « clang -arch x86_64 » (SDK universel).
func compile(cible, version string) ([]byte, error) {
	parts := strings.SplitN(cible, "-", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("cible %q mal formée (attendu os-arch)", cible)
	}
	goos, goarch := parts[0], parts[1]

	tmp, err := os.CreateTemp("", "vecu-app-build-*")
	if err != nil {
		return nil, err
	}
	tmpNom := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpNom)

	cmd := exec.Command("go", "build",
		"-ldflags", ldflags(goos, version),
		"-o", tmpNom, "./desktop")

	// CGO PAR CIBLE, et c'est ce qui rend la cible Windows constructible depuis
	// un Mac. `fyne.io/systray` a besoin de cgo sur macOS (il appelle Cocoa),
	// mais PAS sur Windows, où il passe par des appels système. Laisser
	// CGO_ENABLED=1 pour Windows exigerait une chaîne mingw sur la machine de
	// release ; à 0, un `go build` suffit. Mesuré le 06/09 : l'arbre entier
	// compile pour windows/amd64 avec CGO_ENABLED=0.
	cgo := "0"
	if goos == "darwin" {
		cgo = "1"
	}
	env := append(os.Environ(),
		"CGO_ENABLED="+cgo,
		"GOOS="+goos,
		"GOARCH="+goarch,
	)
	// Cross-compilation C sur macOS : forcer l'architecture de clang.
	if goos == "darwin" {
		switch goarch {
		case "amd64":
			env = append(env, "CC=clang -arch x86_64")
		case "arm64":
			env = append(env, "CC=clang -arch arm64")
		}
	}
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%v : %s", err, strings.TrimSpace(string(out)))
	}
	return os.ReadFile(tmpNom)
}

func chargeClePrivee(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("base64 illisible : %w", err)
	}
	if len(dec) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("clé de %d octets, attendu %d", len(dec), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(dec), nil
}

func parseVersionRelease(s string) ([3]int, bool) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n := 0
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "erreur : "+format+"\n", a...)
	os.Exit(1)
}

// goosDe rend le système d'une cible « os-arch ».
func goosDe(cible string) string {
	os, _, _ := strings.Cut(cible, "-")
	return os
}

// contientDarwin : l'avertissement « aucun bundle » ne concerne que macOS. Sans
// ce filtre, une release Windows seule crierait sur un manque qui n'existe pas.
func contientDarwin(assets map[string]appupdate.Asset) bool {
	for cible := range assets {
		if goosDe(cible) == "darwin" {
			return true
		}
	}
	return false
}

// ldflags : la version, plus le mode fenêtré sur Windows.
//
// `-H windowsgui` empêche Windows d'ouvrir une console noire derrière l'app au
// lancement. Sans lui, un binaire Go est marqué « application console » et le
// système lui en attache une - visible, et impossible à fermer sans tuer l'app.
// C'est l'équivalent du LSUIElement=1 de l'Info.plist sur macOS : dire au
// système que ce programme n'a pas d'interface en ligne de commande.
// Source: https://pkg.go.dev/cmd/link
func ldflags(goos, version string) string {
	f := "-X main.version=" + version
	if goos == "windows" {
		f += " -H windowsgui"
	}
	return f
}
