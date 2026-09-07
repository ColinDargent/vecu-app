package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/colindargent/vecu/appupdate"
)

// version : version de l'app, injectée au build via
// -ldflags "-X main.version=v0.3.0". « dev » pour un build local : un build de
// dev ne s'auto-update jamais (le développeur itère).
var version = "dev"

// clePubliqueReleaseB64 : clé publique ed25519 de release, en base64. Compilée
// dans l'app ; c'est l'ancre de confiance de l'auto-update. La clé privée
// correspondante vit hors dépôt (voir `make release` / `vecu-release keygen`).
// Vide = auto-update désactivée (aucune release signable).
const clePubliqueReleaseB64 = "YE7KIjYWI7MkVZD7a7546llZF26UQoCF7mPpJ8P8sWA="

const (
	// intervalleMaj : cadence de vérification des mises à jour.
	intervalleMaj = 6 * time.Hour
	// delaiPremierMaj : laisser le moteur démarrer avant la 1re vérification.
	delaiPremierMaj = 30 * time.Second
	// maxBinaire : borne de sécurité sur la taille d'un binaire téléchargé.
	maxBinaire = 200 << 20 // 200 Mio
	// drainAvantExit : court délai pour laisser le moteur s'arrêter avant os.Exit.
	drainAvantExit = 500 * time.Millisecond
)

// clientMaj : client HTTP dédié à l'update (télécharge un binaire, timeout large).
var clientMaj = &http.Client{Timeout: 3 * time.Minute}

// Les deux formats d'artefact. « bundle » remplace le bundle entier et est ce
// que publie toute release à partir de v0.9.0 ; « bin » remplace le binaire à
// l'intérieur du bundle, et n'existe plus que pour les releases antérieures.
const (
	formatBin    = "bin"
	formatBundle = "bundle"
)

// majPrete : une mise à jour vérifiée, prête à installer. Version vide = rien à
// faire. Le format est porté ici plutôt que redéduit par l'appelant : c'est le
// manifeste qui l'a décidé, et lui seul le sait.
type majPrete struct {
	Version string
	Format  string
	Octets  []byte
}

// chercheMaj interroge le serveur et rend l'artefact vérifié à installer.
//
// Retour :
//   - (majPrete{Version, Format, Octets}, nil) : mise à jour authentique et plus récente ;
//   - (majPrete{}, nil) : rien à faire (pas plus récent, ou build de dev) ;
//   - (majPrete{}, err) : échec (réseau, signature, empreinte) — ne rien installer.
//
// Toute la sécurité passe ici : appupdate.Open vérifie la signature du manifeste
// (version + empreintes liées), puis Asset.Check lie les octets téléchargés à
// l'empreinte scellée. Un manifeste ou un binaire trafiqué renvoie une erreur.
func chercheMaj(ctx context.Context, client *http.Client, serveur, token string, pub ed25519.PublicKey, actuel, cible string) (majPrete, error) {
	var rien majPrete
	sm, err := fetchManifeste(ctx, client, serveur, token)
	if err != nil {
		return rien, err
	}
	m, err := appupdate.Open(pub, sm)
	if err != nil {
		return rien, fmt.Errorf("manifeste non vérifié : %w", err)
	}
	// Monotonie : n'installer que du strictement plus récent. Ferme le
	// downgrade par rejeu d'un ancien manifeste pourtant valablement signé.
	if !plusRecent(m.Version, actuel) {
		return rien, nil
	}
	// Le format se décide sur ce que le manifeste PUBLIE, jamais sur ce qui
	// réussit à se télécharger. Si la release publie un bundle, c'est bundle ou
	// rien : se replier sur le binaire nu remplacerait le contenu d'un bundle
	// scellé sans toucher à son sceau, ce qui laisserait un CodeResources qui
	// scelle un binaire qui n'est plus là — un état pire que les deux modes purs.
	// Un échec réessaie au cycle suivant, ce qui est sans conséquence.
	format, asset := formatBin, appupdate.Asset{}
	if a, err := m.Bundle(cible); err == nil {
		format, asset = formatBundle, a
	} else if a, err := m.Asset(cible); err == nil {
		asset = a
	} else {
		return rien, err
	}
	octets, err := telechargeArtefact(ctx, client, serveur, token, cible, format)
	if err != nil {
		return rien, err
	}
	if err := asset.Check(octets); err != nil {
		return rien, fmt.Errorf("artefact refusé (%s) : %w", format, err)
	}
	return majPrete{Version: m.Version, Format: format, Octets: octets}, nil
}

func fetchManifeste(ctx context.Context, client *http.Client, serveur, token string) (appupdate.SignedManifest, error) {
	var sm appupdate.SignedManifest
	req, err := http.NewRequestWithContext(ctx, "GET", serveur+"/app/manifest", nil)
	if err != nil {
		return sm, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return sm, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return sm, fmt.Errorf("manifest : statut %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&sm); err != nil {
		return sm, fmt.Errorf("manifest illisible : %w", err)
	}
	return sm, nil
}

// telechargeArtefact récupère le binaire nu ou l'archive du bundle selon
// `format`. Les valeurs partent en paramètre de requête encodé, le serveur les
// valide contre sa propre allowlist, et les octets rendus sont ensuite liés à
// l'empreinte scellée par l'appelant.
func telechargeArtefact(ctx context.Context, client *http.Client, serveur, token, cible, format string) ([]byte, error) {
	u := serveur + "/app/download?" + url.Values{"target": {cible}, "format": {format}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download : statut %d", resp.StatusCode)
	}
	// Lire une borne + 1 : si on atteint maxBinaire+1, le binaire dépasse le
	// plafond → erreur explicite (sinon LimitReader tronquerait en silence et
	// Asset.Check échouerait en accusant à tort un binaire trafiqué).
	bin, err := io.ReadAll(io.LimitReader(resp.Body, maxBinaire+1))
	if err != nil {
		return nil, fmt.Errorf("téléchargement : %w", err)
	}
	if len(bin) > maxBinaire {
		return nil, fmt.Errorf("artefact trop volumineux (> %d octets)", maxBinaire)
	}
	return bin, nil
}

// ecritTemp écrit `bin` dans un fichier temporaire du dossier `dir` (même système
// de fichiers que la cible, pour un rename atomique ensuite), le rend exécutable
// et le fsync (durabilité : après une coupure de courant, le rename ne doit pas
// pointer un fichier au contenu non flushé). Rend le chemin du temporaire.
func ecritTemp(dir string, bin []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, ".vecu-app-*.tmp"+suffixeExecutable)
	if err != nil {
		return "", fmt.Errorf("fichier temporaire : %w", err)
	}
	tmpNom := tmp.Name()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		os.Remove(tmpNom)
		return "", fmt.Errorf("écriture : %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpNom)
		return "", fmt.Errorf("sync : %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpNom)
		return "", fmt.Errorf("fermeture : %w", err)
	}
	if err := os.Chmod(tmpNom, 0o755); err != nil {
		os.Remove(tmpNom)
		return "", fmt.Errorf("chmod : %w", err)
	}
	return tmpNom, nil
}

// sondeSante exécute `bin --check` et exige une sortie en code 0 dont la sortie
// standard vaut exactement `versionAttendue`. C'est le filet anti-brick : un
// binaire d'une mauvaise architecture, corrompu, ou qui plante au démarrage
// échoue ici — AVANT de remplacer le binaire en place. Sans cette sonde, une
// release authentique mais bancale mettrait launchd (KeepAlive acharné) en
// boucle de crash sans recovery.
func sondeSante(bin, versionAttendue string) error {
	out, err := exec.Command(bin, "--check").CombinedOutput()
	if err != nil {
		return fmt.Errorf("--check a échoué : %v (%s)", err, strings.TrimSpace(string(out)))
	}
	got := strings.TrimSpace(string(out))
	if got != versionAttendue {
		return fmt.Errorf("version inattendue de la sonde : %q != %q", got, versionAttendue)
	}
	return nil
}

// remplaceBinaire enchaîne écriture temporaire → sonde santé → bascule atomique.
// La signature a été vérifiée AVANT (dans chercheMaj) : on n'écrit jamais un
// binaire non authentique. La sonde garantit en plus qu'il DÉMARRE sur cette
// machine avant de l'installer.
func remplaceBinaire(cible string, bin []byte, versionAttendue string) error {
	// Viser le vrai fichier (pas un lien) : rename doit remplacer le binaire,
	// pas un symlink, et le temporaire doit être sur le même volume.
	if resolu, err := filepath.EvalSymlinks(cible); err == nil {
		cible = resolu
	}
	tmpNom, err := ecritTemp(filepath.Dir(cible), bin)
	if err != nil {
		return err
	}
	if err := sondeSante(tmpNom, versionAttendue); err != nil {
		os.Remove(tmpNom)
		return fmt.Errorf("binaire non validé, mise à jour abandonnée : %w", err)
	}
	return bascule(tmpNom, cible)
}

// plusRecent : `candidat` est-il une version strictement postérieure à `actuel` ?
// Versions au format vX.Y.Z. Toute version non parsable (« dev », build local)
// rend false : on ne descend jamais vers l'inconnu, et un build de dev ne
// s'auto-update pas.
func plusRecent(candidat, actuel string) bool {
	c, ok1 := parseVersion(candidat)
	a, ok2 := parseVersion(actuel)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if c[i] != a[i] {
			return c[i] > a[i]
		}
	}
	return false
}

func parseVersion(s string) ([3]int, bool) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// clePublique : décode la clé de release compilée. Erreur si absente ou mal
// formée → l'auto-update se désactive proprement (jamais de panique).
func clePublique() (ed25519.PublicKey, error) {
	return clePubliqueDepuis(clePubliqueReleaseB64)
}

// clePubliqueDepuis : cœur testable de clePublique. Une clé vide désactive
// l'auto-update (aucune release signable) ; une clé mal formée aussi.
func clePubliqueDepuis(b64 string) (ed25519.PublicKey, error) {
	if b64 == "" {
		return nil, fmt.Errorf("clé de release absente")
	}
	return appupdate.DecodePublicKey(b64)
}

// verifieEtApplique : un tour complet. Sérialisé (une seule mise à jour à la
// fois, que l'appel vienne de la boucle ou du menu). Si une mise à jour vérifiée
// et validée est prête, remplace le binaire et sort en code NON-ZÉRO — le
// KeepAlive du launchd (SuccessfulExit=false) relance alors la nouvelle version.
// Une sortie 0 ne serait PAS relancée (c'est le contrat de « Quitter »).
func (a *app) verifieEtApplique(ctx context.Context, pub ed25519.PublicKey, logf func(string, ...any)) {
	// Une seule maj à la fois : la boucle 6h et le clic menu ne doivent pas
	// télécharger/patcher en parallèle.
	if !a.majEnCours.CompareAndSwap(false, true) {
		return
	}
	defer a.majEnCours.Store(false)

	if a.serveur == "" || a.token == "" {
		return
	}
	cible := appupdate.TargetName(runtime.GOOS, runtime.GOARCH)
	maj, err := chercheMaj(ctx, clientMaj, a.serveur, a.token, pub, version, cible)
	if err != nil {
		logf("auto-update : %v", err)
		return
	}
	if maj.Version == "" {
		return // à jour
	}
	exe, err := os.Executable()
	if err != nil {
		logf("auto-update : binaire introuvable : %v", err)
		return
	}
	// Deux mécanismes, choisis par ce que la release publie. Le chemin binaire
	// ne sert plus qu'à installer une release antérieure à v0.9.0 ; il reste
	// parce qu'un poste ne doit jamais se retrouver sans chemin de mise à jour.
	switch maj.Format {
	case formatBundle:
		err = remplaceBundle(exe, maj.Octets, maj.Version)
	default:
		err = remplaceBinaire(exe, maj.Octets, maj.Version)
	}
	if err != nil {
		logf("auto-update : %v", err)
		return
	}
	logf("mise à jour %s installée (%s)", maj.Version, maj.Format)
	a.redemarre(logf, "nouvelle version "+maj.Version)
}

// boucleMaj : vérification au démarrage (après un délai) puis toutes les
// intervalleMaj. S'arrête avec le contexte.
func (a *app) boucleMaj(ctx context.Context, logf func(string, ...any)) {
	pub, err := clePublique()
	if err != nil {
		logf("auto-update désactivée : %v", err)
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(delaiPremierMaj):
	}
	a.verifieEtApplique(ctx, pub, logf)

	t := time.NewTicker(intervalleMaj)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.verifieEtApplique(ctx, pub, logf)
		}
	}
}

// verifieMaintenant : vérification à la demande (menu « Vérifier les mises à
// jour »). Même chemin que la boucle ; loggue si aucune clé n'est compilée.
func (a *app) verifieMaintenant() {
	pub, err := clePublique()
	if err != nil {
		log.Printf("auto-update indisponible : %v", err)
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	a.verifieEtApplique(ctx, pub, log.Printf)
}
