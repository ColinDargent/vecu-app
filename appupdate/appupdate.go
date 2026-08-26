// Package appupdate : socle de l'auto-update de l'app de bureau Vécu.
//
// Vocabulaire partagé par les trois côtés de la chaîne :
//   - l'outil de release (cmd/vecu-release) SCELLE le manifeste et publie les binaires ;
//   - le serveur SERT le manifeste scellé et les binaires tels quels ;
//   - l'app OUVRE le manifeste (vérifie la signature) puis lie le binaire téléchargé.
//
// # Ce que la signature protège
//
// La signature ed25519 (bibliothèque standard) porte sur le manifeste ENTIER,
// pas sur les octets nus d'un binaire. Le manifeste transporte le numéro de
// version et, pour chaque cible, l'empreinte SHA-256 du binaire attendu. Ainsi
// version, architecture et octets sont tous liés par une seule signature :
//   - un binaire trafiqué a une empreinte qui ne correspond plus à celle scellée ;
//   - un attaquant ne peut pas resservir un vieux binaire authentique sous un
//     faux numéro de version (la version est scellée avec l'empreinte), ni
//     substituer le binaire d'une autre architecture (la cible est la clé de map).
//
// La monotonie de version (refuser une version <= installée, contre le rejeu
// d'un ancien manifeste pourtant valablement signé) est une politique du client,
// pas de ce paquet.
//
// La clé privée vit hors dépôt ; seule la clé publique (32 octets) est compilée
// dans l'app.
package appupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Manifest : contenu SCELLÉ décrivant la dernière release. Une entrée par cible
// (« darwin-arm64 », « darwin-amd64 ») : l'app ne lie que l'asset de sa propre
// architecture.
// Deux jeux d'empreintes coexistent, et c'est volontaire :
//
//   - Assets : le binaire nu, ce que remplace l'auto-update jusqu'à v0.8.0 incluse ;
//   - Bundles : l'archive du bundle scellé, ce que remplace l'auto-update à partir
//     de v0.9.0.
//
// Assets DOIT rester peuplé tant qu'un poste peut se trouver en v0.8.0 ou
// antérieur (voir la fenêtre de coexistence du CDC technique, section 5). Un
// client ancien lisant un manifeste enrichi ne casse pas : Open vérifie la
// signature sur les octets BRUTS du payload puis démarshale, et encoding/json
// ignore les champs qu'il ne connaît pas. La compatibilité en avant est donc une
// propriété du format, pas une précaution à reconduire à chaque release.
//
// Pas d'`omitempty` sur Bundles, par cohérence avec Assets et parce qu'une map
// absente et une map vide doivent rester discernables à la lecture du manifeste.
type Manifest struct {
	Version string           `json:"version"`
	Assets  map[string]Asset `json:"assets"`
	Bundles map[string]Asset `json:"bundles"`
}

// Asset : empreinte du binaire attendu pour une cible. Scellée dans le manifeste,
// c'est ELLE qui lie les octets téléchargés à la signature — pas un contrôle
// d'intégrité décoratif.
type Asset struct {
	SHA256 string `json:"sha256"`         // empreinte hex des octets du binaire
	Size   int64  `json:"size,omitempty"` // taille en octets (borne de téléchargement, indicatif)
}

// SignedManifest : le manifeste tel que transporté, signature comprise. La
// signature porte sur les octets EXACTS du payload (le manifeste JSON encodé) ;
// le client vérifie ce qu'il a reçu, sans re-sérialiser (donc sans dépendre
// d'une forme canonique du JSON).
type SignedManifest struct {
	Payload string `json:"payload"` // base64 des octets JSON du Manifest
	Sig     string `json:"sig"`     // base64 de la signature ed25519 du payload décodé
}

// TargetName : nom de cible pour un couple OS/arch (« darwin-arm64 »). Sert de
// clé dans Manifest.Assets, côté release comme côté client (runtime.GOOS/GOARCH).
func TargetName(goos, goarch string) string { return goos + "-" + goarch }

// SumHex : empreinte SHA-256 des octets, en hexadécimal minuscule.
func SumHex(bin []byte) string {
	h := sha256.Sum256(bin)
	return hex.EncodeToString(h[:])
}

// Seal marshale le manifeste et le signe. Appelé au moment de la release, avec
// la clé privée chargée depuis un fichier hors dépôt.
func Seal(priv ed25519.PrivateKey, m Manifest) (SignedManifest, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return SignedManifest{}, fmt.Errorf("clé privée de %d octets, attendu %d", len(priv), ed25519.PrivateKeySize)
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return SignedManifest{}, fmt.Errorf("sérialisation du manifeste : %w", err)
	}
	sig := ed25519.Sign(priv, payload)
	return SignedManifest{
		Payload: base64.StdEncoding.EncodeToString(payload),
		Sig:     base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// Open vérifie la signature d'un manifeste scellé puis en rend le contenu. Toute
// altération du payload (version, empreintes, architectures) ou une signature
// émise par une autre clé fait échouer Open. AUCUNE update ne doit être engagée
// si Open renvoie une erreur.
func Open(pub ed25519.PublicKey, sm SignedManifest) (Manifest, error) {
	var zero Manifest
	if len(pub) != ed25519.PublicKeySize {
		return zero, fmt.Errorf("clé publique invalide (%d octets, attendu %d)", len(pub), ed25519.PublicKeySize)
	}
	payload, err := base64.StdEncoding.DecodeString(sm.Payload)
	if err != nil {
		return zero, fmt.Errorf("payload illisible : %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(sm.Sig)
	if err != nil {
		return zero, fmt.Errorf("signature illisible : %w", err)
	}
	// Garde explicite : ed25519.Verify rend false (ne panique pas) sur une
	// signature de mauvaise taille, mais l'invariant mérite d'être écrit.
	if len(sig) != ed25519.SignatureSize {
		return zero, fmt.Errorf("signature de %d octets, attendu %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, payload, sig) {
		return zero, errors.New("signature invalide")
	}
	var m Manifest
	if err := json.Unmarshal(payload, &m); err != nil {
		return zero, fmt.Errorf("manifeste illisible après vérification : %w", err)
	}
	return m, nil
}

// Asset rend l'empreinte scellée pour une cible donnée, ou une erreur si la
// release ne publie pas cette architecture.
func (m Manifest) Asset(target string) (Asset, error) {
	a, ok := m.Assets[target]
	if !ok {
		return Asset{}, fmt.Errorf("aucun binaire pour la cible %q dans le manifeste", target)
	}
	return a, nil
}

// Bundle rend l'empreinte scellée de l'archive du bundle pour une cible, ou une
// erreur si la release n'en publie pas. Une release antérieure à v0.9.0 n'en
// publie aucune : c'est le cas nominal d'un repli vers le chemin binaire, pas
// une anomalie.
func (m Manifest) Bundle(target string) (Asset, error) {
	a, ok := m.Bundles[target]
	if !ok {
		return Asset{}, fmt.Errorf("aucun bundle pour la cible %q dans le manifeste", target)
	}
	return a, nil
}

// Check confronte les octets d'un binaire téléchargé à l'empreinte scellée. Comme
// l'empreinte vient du manifeste signé, une correspondance prouve que ces octets
// sont exactement ceux qu'a signés la release. Comparaison insensible à la casse
// de l'hex, pour ne pas refuser un binaire authentique sur une simple différence
// de casse d'encodage.
func (a Asset) Check(bin []byte) error {
	if a.SHA256 == "" {
		return errors.New("empreinte absente du manifeste")
	}
	if got := SumHex(bin); !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("empreinte incohérente : %s != %s", got, a.SHA256)
	}
	return nil
}

// DecodePublicKey lit une clé publique ed25519 encodée en base64 (la forme
// compilée dans l'app). Copie défensive : la clé de confiance ne doit jamais
// aliaser un buffer que l'appelant pourrait muter.
func DecodePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("clé publique base64 illisible : %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("clé publique de %d octets, attendu %d", len(raw), ed25519.PublicKeySize)
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(pub, raw)
	return pub, nil
}
