package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/colindargent/vecu/appupdate"
)

func cleTest(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("clés : %v", err)
	}
	return pub, priv
}

// serveurMaj : faux serveur d'update. Sert un manifeste scellé (version + asset
// arm64) et le binaire. `bin` est ce que /app/download rend réellement — permet
// de simuler un binaire trafiqué en le désynchronisant de l'empreinte scellée.
func serveurMaj(t *testing.T, priv ed25519.PrivateKey, ver string, empreinteDe, bin []byte) *httptest.Server {
	t.Helper()
	m := appupdate.Manifest{
		Version: ver,
		Assets: map[string]appupdate.Asset{
			"darwin-arm64": {SHA256: appupdate.SumHex(empreinteDe), Size: int64(len(empreinteDe))},
		},
	}
	sm, err := appupdate.Seal(priv, m)
	if err != nil {
		t.Fatalf("Seal : %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/app/manifest", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sm)
	})
	mux.HandleFunc("/app/download", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bin)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestChercheMaj_Nominal(t *testing.T) {
	pub, priv := cleTest(t)
	bin := []byte("nouveau binaire v0.3.0")
	srv := serveurMaj(t, priv, "v0.3.0", bin, bin)

	maj, err := chercheMaj(context.Background(), srv.Client(), srv.URL, "tok", pub, "v0.2.0", "darwin-arm64")
	if err != nil {
		t.Fatalf("chercheMaj : %v", err)
	}
	if maj.Version != "v0.3.0" {
		t.Fatalf("version = %q, attendu v0.3.0", maj.Version)
	}
	if string(maj.Octets) != string(bin) {
		t.Fatalf("binaire inattendu")
	}
	if maj.Format != formatBin {
		t.Fatalf("format = %q, attendu %q : ce manifeste ne publie aucun bundle", maj.Format, formatBin)
	}
}

func TestChercheMaj_PasPlusRecent(t *testing.T) {
	pub, priv := cleTest(t)
	bin := []byte("même version")
	srv := serveurMaj(t, priv, "v0.3.0", bin, bin)

	maj, err := chercheMaj(context.Background(), srv.Client(), srv.URL, "tok", pub, "v0.3.0", "darwin-arm64")
	if err != nil || maj.Version != "" || maj.Octets != nil {
		t.Fatalf("version égale = pas d'update ; or ver=%q octets=%v err=%v", maj.Version, maj.Octets, err)
	}
}

// Binaire trafiqué : le serveur annonce l'empreinte de `attendu` mais sert
// `malveillant`. Asset.Check doit refuser.
func TestChercheMaj_BinaireTrafique(t *testing.T) {
	pub, priv := cleTest(t)
	attendu := []byte("binaire légitime")
	malveillant := []byte("binaire malveillant")
	srv := serveurMaj(t, priv, "v0.3.0", attendu, malveillant)

	if _, err := chercheMaj(context.Background(), srv.Client(), srv.URL, "tok", pub, "v0.2.0", "darwin-arm64"); err == nil {
		t.Fatal("un binaire ne correspondant pas à l'empreinte scellée doit être refusé")
	}
}

// Manifeste signé par une autre clé que la clé compilée : Open doit refuser.
func TestChercheMaj_MauvaiseCle(t *testing.T) {
	pubLegit, _ := cleTest(t)
	_, privAttaquant := cleTest(t)
	bin := []byte("binaire")
	srv := serveurMaj(t, privAttaquant, "v0.3.0", bin, bin)

	if _, err := chercheMaj(context.Background(), srv.Client(), srv.URL, "tok", pubLegit, "v0.2.0", "darwin-arm64"); err == nil {
		t.Fatal("un manifeste signé par une autre clé doit être refusé")
	}
}

// Build de dev : version « dev » non parsable → jamais d'auto-update.
func TestChercheMaj_BuildDev(t *testing.T) {
	pub, priv := cleTest(t)
	bin := []byte("release")
	srv := serveurMaj(t, priv, "v9.9.9", bin, bin)

	if maj, err := chercheMaj(context.Background(), srv.Client(), srv.URL, "tok", pub, "dev", "darwin-arm64"); err != nil || maj.Version != "" {
		t.Fatalf("un build de dev ne doit pas s'auto-update ; ver=%q err=%v", maj.Version, err)
	}
}

func TestChercheMaj_CibleAbsente(t *testing.T) {
	pub, priv := cleTest(t)
	bin := []byte("release")
	srv := serveurMaj(t, priv, "v0.3.0", bin, bin) // ne publie que darwin-arm64

	if _, err := chercheMaj(context.Background(), srv.Client(), srv.URL, "tok", pub, "v0.2.0", "darwin-amd64"); err == nil {
		t.Fatal("une cible non publiée doit renvoyer une erreur")
	}
}

// script écrit un exécutable shell minimal qui imprime `sortie` et sort en `code`.
// Sert de faux « binaire téléchargé » pour tester la sonde de santé sans dépendre
// d'un vrai build.
func script(t *testing.T, dir, sortie string, code int) string {
	t.Helper()
	p := filepath.Join(dir, "faux-binaire")
	corps := "#!/bin/sh\necho " + sortie + "\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(p, []byte(corps), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEcritTempEtBascule(t *testing.T) {
	dir := t.TempDir()
	cible := filepath.Join(dir, "vecu-app")
	if err := os.WriteFile(cible, []byte("ANCIEN"), 0o755); err != nil {
		t.Fatal(err)
	}
	nouveau := []byte("NOUVEAU BINAIRE")

	tmp, err := ecritTemp(dir, nouveau)
	if err != nil {
		t.Fatalf("ecritTemp : %v", err)
	}
	if info, _ := os.Stat(tmp); info.Mode().Perm() != 0o755 {
		t.Fatalf("temp mode = %v, attendu 0755", info.Mode().Perm())
	}
	if err := bascule(tmp, cible); err != nil {
		t.Fatalf("bascule : %v", err)
	}
	if got, _ := os.ReadFile(cible); string(got) != string(nouveau) {
		t.Fatalf("contenu = %q, attendu %q", got, nouveau)
	}
	// L'ancien binaire est conservé en filet de recovery.
	if old, _ := os.ReadFile(cible + ".old"); string(old) != "ANCIEN" {
		t.Fatalf(".old = %q, attendu ANCIEN", old)
	}
	// Aucun fichier temporaire orphelin (cible, cible.old, et rien d'autre).
	entrees, _ := os.ReadDir(dir)
	if len(entrees) != 2 {
		t.Fatalf("%d fichiers restants, attendu 2 (cible + .old, pas de .tmp)", len(entrees))
	}
}

func TestSondeSante(t *testing.T) {
	dir := t.TempDir()

	// Bon binaire : imprime la version attendue, sort 0.
	bon := script(t, t.TempDir(), "v0.3.0", 0)
	if err := sondeSante(bon, "v0.3.0"); err != nil {
		t.Fatalf("un binaire sain doit passer la sonde : %v", err)
	}
	// Mauvaise version : la sonde refuse (protège d'un binaire mal étiqueté).
	if err := sondeSante(bon, "v0.4.0"); err == nil {
		t.Fatal("une version inattendue doit être refusée")
	}
	// Binaire qui plante au démarrage : refusé AVANT toute installation.
	casse := script(t, dir, "v0.3.0", 1)
	if err := sondeSante(casse, "v0.3.0"); err == nil {
		t.Fatal("un binaire qui sort non-zéro doit être refusé")
	}
}

// remplaceBinaire complet : un « binaire » sain (script imprimant la version)
// passe la sonde et remplace la cible.
func TestRemplaceBinaire_Sonde(t *testing.T) {
	dir := t.TempDir()
	cible := filepath.Join(dir, "vecu-app")
	if err := os.WriteFile(cible, []byte("#!/bin/sh\necho ANCIEN\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nouveau := []byte("#!/bin/sh\necho v0.3.0\n")
	if err := remplaceBinaire(cible, nouveau, "v0.3.0"); err != nil {
		t.Fatalf("remplaceBinaire : %v", err)
	}
	if got, _ := os.ReadFile(cible); string(got) != string(nouveau) {
		t.Fatalf("cible non remplacée")
	}

	// Un nouveau binaire dont la sonde échoue ne doit PAS remplacer la cible.
	mauvais := []byte("#!/bin/sh\necho AUTRE\n")
	if err := remplaceBinaire(cible, mauvais, "v0.4.0"); err == nil {
		t.Fatal("un binaire échouant à la sonde ne doit pas être installé")
	}
	if got, _ := os.ReadFile(cible); string(got) != string(nouveau) {
		t.Fatalf("la cible a été modifiée malgré l'échec de la sonde : %q", got)
	}
}

func TestPlusRecent(t *testing.T) {
	cas := []struct {
		candidat, actuel string
		veut             bool
	}{
		{"v0.3.0", "v0.2.0", true},
		{"v0.10.0", "v0.3.0", true}, // comparaison numérique, pas lexicale
		{"v1.0.0", "v0.9.9", true},
		{"v0.3.0", "v0.3.0", false}, // égal
		{"v0.2.0", "v0.3.0", false}, // plus ancien
		{"v0.3.1", "v0.3.0", true},
		{"dev", "v0.3.0", false},    // build de dev
		{"v0.3.0", "dev", false},    // actuel non parsable
		{"v0.3", "v0.2.0", false},   // format invalide
		{"vX.Y.Z", "v0.2.0", false}, // non numérique
	}
	for _, c := range cas {
		if got := plusRecent(c.candidat, c.actuel); got != c.veut {
			t.Errorf("plusRecent(%q, %q) = %v, attendu %v", c.candidat, c.actuel, got, c.veut)
		}
	}
}

func TestClePubliqueDepuis_AbsenteDesactive(t *testing.T) {
	// Une clé vide désactive proprement l'auto-update, sans panique.
	if _, err := clePubliqueDepuis(""); err == nil {
		t.Fatal("sans clé, clePubliqueDepuis doit renvoyer une erreur")
	}
}

// Une vraie clé base64 se décode et reste utilisable pour vérifier.
func TestClePubliqueDepuis_FormatValide(t *testing.T) {
	pub, _ := cleTest(t)
	if _, err := clePubliqueDepuis(base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("une clé valide doit se décoder : %v", err)
	}
}

// La clé compilée dans l'app est valide (garde contre un copier-coller cassé).
func TestClePubliqueCompilee(t *testing.T) {
	if _, err := clePublique(); err != nil {
		t.Fatalf("la clé de release compilée doit être valide : %v", err)
	}
}
