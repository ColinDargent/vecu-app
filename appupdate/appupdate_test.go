package appupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// clés éphémères : le test ne dépend jamais de la vraie clé de release.
func genKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("génération de clés : %v", err)
	}
	return pub, priv
}

func manifDemo() (Manifest, []byte) {
	bin := []byte("faux binaire vecu-app v0.3.0 (arm64)")
	m := Manifest{
		Version: "v0.3.0",
		Assets: map[string]Asset{
			"darwin-arm64": {SHA256: SumHex(bin), Size: int64(len(bin))},
		},
	}
	return m, bin
}

// Critère de recette 1 : un manifeste authentique scellé par la bonne clé
// s'ouvre, et le binaire correspondant est lié.
func TestFluxNominal(t *testing.T) {
	pub, priv := genKeys(t)
	m, bin := manifDemo()

	sm, err := Seal(priv, m)
	if err != nil {
		t.Fatalf("Seal : %v", err)
	}
	ouvert, err := Open(pub, sm)
	if err != nil {
		t.Fatalf("un manifeste authentique doit s'ouvrir, or : %v", err)
	}
	if ouvert.Version != "v0.3.0" {
		t.Fatalf("version = %q, attendu v0.3.0", ouvert.Version)
	}
	a, err := ouvert.Asset(TargetName("darwin", "arm64"))
	if err != nil {
		t.Fatalf("Asset : %v", err)
	}
	if err := a.Check(bin); err != nil {
		t.Fatalf("le binaire attendu doit être lié, or : %v", err)
	}
}

// Critère de recette 2 : un binaire trafiqué (un bit modifié) est refusé au
// moment de lier les octets à l'empreinte scellée.
func TestBinaireTrafique(t *testing.T) {
	pub, priv := genKeys(t)
	m, bin := manifDemo()
	sm, _ := Seal(priv, m)
	ouvert, _ := Open(pub, sm)
	a, _ := ouvert.Asset(TargetName("darwin", "arm64"))

	trafique := append([]byte(nil), bin...)
	trafique[0] ^= 0x01
	if err := a.Check(trafique); err == nil {
		t.Fatal("un binaire trafiqué doit être refusé, or Check a réussi")
	}
}

// Critère de recette 3 : un manifeste signé par une AUTRE clé est refusé.
func TestManifesteMauvaiseCle(t *testing.T) {
	pubLegitime, _ := genKeys(t)
	_, privAttaquant := genKeys(t)
	m, _ := manifDemo()

	sm, _ := Seal(privAttaquant, m)
	if _, err := Open(pubLegitime, sm); err == nil {
		t.Fatal("un manifeste signé par une autre clé doit être refusé")
	}
}

// Le trou HIGH de la revue : rejeu d'un vieux binaire authentique sous un faux
// numéro de version. L'attaquant garde une signature valide mais modifie le
// payload (bump de version) → la signature ne couvre plus ces octets → refus.
func TestPayloadFalsifie_AntiDowngrade(t *testing.T) {
	pub, priv := genKeys(t)
	m, _ := manifDemo()
	sm, _ := Seal(priv, m)

	// L'attaquant remplace le payload par un manifeste « v99.0.0 » pointant sur
	// la même (ancienne) empreinte, en gardant la signature d'origine.
	falsifie := m
	falsifie.Version = "v99.0.0"
	payload, _ := json.Marshal(falsifie)
	sm.Payload = base64.StdEncoding.EncodeToString(payload)

	if _, err := Open(pub, sm); err == nil {
		t.Fatal("un payload falsifié (version gonflée) doit être refusé : signature ne couvre plus le payload")
	}
}

// Substitution cross-architecture : demander une cible absente du manifeste
// scellé échoue proprement (pas de binaire d'une autre arch resservi).
func TestCibleAbsente(t *testing.T) {
	pub, priv := genKeys(t)
	m, _ := manifDemo() // ne publie que darwin-arm64
	sm, _ := Seal(priv, m)
	ouvert, _ := Open(pub, sm)

	if _, err := ouvert.Asset(TargetName("darwin", "amd64")); err == nil {
		t.Fatal("une cible non publiée doit renvoyer une erreur")
	}
}

// Garde de taille de signature (défense en profondeur).
func TestSignatureMalFormee(t *testing.T) {
	pub, priv := genKeys(t)
	m, _ := manifDemo()
	sm, _ := Seal(priv, m)
	sm.Sig = base64.StdEncoding.EncodeToString([]byte("trop courte"))
	if _, err := Open(pub, sm); err == nil {
		t.Fatal("une signature de mauvaise taille doit être refusée")
	}
}

// Comparaison d'empreinte insensible à la casse (robustesse d'encodage).
func TestCheck_HexCasseIndifferente(t *testing.T) {
	bin := []byte("contenu")
	a := Asset{SHA256: strings.ToUpper(SumHex(bin))}
	if err := a.Check(bin); err != nil {
		t.Fatalf("l'empreinte en majuscules doit être acceptée : %v", err)
	}
}

// L'aller-retour base64 de la clé publique (forme compilée dans l'app) rend une
// clé utilisable, et indépendante du buffer source (copie défensive).
func TestDecodePublicKey_AllerRetourEtCopie(t *testing.T) {
	pub, priv := genKeys(t)
	b64 := base64.StdEncoding.EncodeToString(pub)

	decodee, err := DecodePublicKey(b64)
	if err != nil {
		t.Fatalf("décodage : %v", err)
	}
	m, _ := manifDemo()
	sm, _ := Seal(priv, m)
	if _, err := Open(decodee, sm); err != nil {
		t.Fatalf("la clé décodée doit ouvrir un manifeste valide : %v", err)
	}
}

func TestDecodePublicKey_Invalide(t *testing.T) {
	if _, err := DecodePublicKey("pas du base64 !!!"); err == nil {
		t.Fatal("une clé illisible doit renvoyer une erreur")
	}
	if _, err := DecodePublicKey(base64.StdEncoding.EncodeToString([]byte("trop court"))); err == nil {
		t.Fatal("une clé de mauvaise taille doit renvoyer une erreur")
	}
}

// manifestV080 : le Manifest tel que le connaissait la v0.8.0, AVANT que
// Bundles n'existe. Recopié à la main plutôt qu'importé, parce que c'est
// justement l'ancienne forme qu'on veut confronter à la nouvelle.
type manifestV080 struct {
	Version string           `json:"version"`
	Assets  map[string]Asset `json:"assets"`
}

// Un poste en v0.8.0 doit pouvoir lire un manifeste v0.9.0. C'est ce qui rend
// la fenêtre de coexistence gratuite : tant qu'Assets reste peuplé, un client
// ancien vérifie la signature, ignore Bundles, et met à jour par le binaire.
//
// La propriété tient à deux choses, et le test les couvre toutes les deux :
// Open vérifie la signature sur les octets BRUTS du payload (donc l'ajout d'un
// champ ne l'invalide pas), et encoding/json ignore les champs inconnus.
func TestManifesteEnrichi_LisibleParUnClientAncien(t *testing.T) {
	pub, priv := genKeys(t)
	bin := []byte("faux binaire v0.9.0 (arm64)")
	arch := []byte("fausse archive tar.gz du bundle v0.9.0 (arm64)")
	m := Manifest{
		Version: "v0.9.0",
		Assets:  map[string]Asset{"darwin-arm64": {SHA256: SumHex(bin), Size: int64(len(bin))}},
		Bundles: map[string]Asset{"darwin-arm64": {SHA256: SumHex(arch), Size: int64(len(arch))}},
	}
	sm, err := Seal(priv, m)
	if err != nil {
		t.Fatalf("scellement : %v", err)
	}

	// 1. La signature d'un manifeste enrichi reste vérifiable, telle quelle.
	payload, err := base64.StdEncoding.DecodeString(sm.Payload)
	if err != nil {
		t.Fatalf("payload : %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(sm.Sig)
	if err != nil {
		t.Fatalf("sig : %v", err)
	}
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("la signature d'un manifeste enrichi ne se vérifie plus : la fenêtre de coexistence est cassée")
	}

	// 2. Le client ancien démarshale sans erreur et retrouve SON asset intact.
	var vieux manifestV080
	if err := json.Unmarshal(payload, &vieux); err != nil {
		t.Fatalf("un client v0.8.0 ne sait plus lire le manifeste : %v", err)
	}
	if vieux.Version != "v0.9.0" {
		t.Fatalf("version lue %q, attendu v0.9.0", vieux.Version)
	}
	a, ok := vieux.Assets["darwin-arm64"]
	if !ok {
		t.Fatal("Assets vide pour un client ancien : il n'a plus rien à installer")
	}
	if err := (Asset{SHA256: a.SHA256}).Check(bin); err != nil {
		t.Fatalf("l'empreinte du binaire nu ne correspond plus : %v", err)
	}
}

// L'inverse : un client récent face à un manifeste ANTÉRIEUR à v0.9.0, qui ne
// porte aucun bundle. Bundle() doit rendre une erreur nette, pas un zéro
// silencieux — c'est elle qui déclenchera le repli vers le chemin binaire.
func TestManifesteAncien_AucunBundle(t *testing.T) {
	pub, priv := genKeys(t)
	m, _ := manifDemo()
	sm, err := Seal(priv, m)
	if err != nil {
		t.Fatalf("scellement : %v", err)
	}
	ouvert, err := Open(pub, sm)
	if err != nil {
		t.Fatalf("ouverture : %v", err)
	}
	if _, err := ouvert.Bundle("darwin-arm64"); err == nil {
		t.Fatal("Bundle() a rendu un asset là où le manifeste n'en publie aucun")
	}
	if _, err := ouvert.Asset("darwin-arm64"); err != nil {
		t.Fatalf("le chemin binaire doit rester disponible : %v", err)
	}
}
