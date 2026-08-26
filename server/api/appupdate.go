package api

import (
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ciblesApp : allowlist stricte des cibles téléchargeables. Sert de garde
// anti-traversée de chemin : le paramètre `target` de /app/download n'est jamais
// joint tel quel à un chemin de fichier — il doit d'abord figurer dans cette map.
var ciblesApp = map[string]bool{
	"darwin-arm64": true,
	"darwin-amd64": true,
}

// formatsApp : même principe que ciblesApp, pour le paramètre `format`. La
// valeur n'est pas concaténée telle quelle à un chemin — elle sert de CLÉ, et
// c'est le suffixe associé qui est concaténé. Un format inconnu ne peut donc
// rien construire, pas même un chemin refusé plus loin.
//
//   - « bin »    : le binaire nu, ce que demandent les postes jusqu'à v0.8.0 ;
//   - « bundle » : l'archive tar.gz du bundle scellé, à partir de v0.9.0.
var formatsApp = map[string]string{
	"bin":    "",
	"bundle": ".tgz",
}

// handleAppManifest : GET /app/manifest — sert le manifeste scellé (SignedManifest
// JSON) de la dernière release de l'app de bureau. Le contenu est déjà signé côté
// release ; le serveur ne fait que le relayer.
func (s *Server) handleAppManifest(w http.ResponseWriter, r *http.Request) {
	if s.AppDist == "" {
		writeErr(w, http.StatusNotFound, "aucune release publiée")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.AppDist, "manifest.json"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "aucune release publiée")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleAppDownload : GET /app/download?target=darwin-arm64[&format=bundle] —
// sert l'artefact de l'app pour une cible. `target` ET `format` sont validés
// contre leurs allowlists avant toute construction de chemin.
//
// `format` absent vaut « bin », le binaire nu : c'est ce que demandent les
// postes jusqu'à v0.8.0 incluse, et leur requête ne doit pas changer de sens
// parce que le serveur, lui, a évolué.
func (s *Server) handleAppDownload(w http.ResponseWriter, r *http.Request) {
	if s.AppDist == "" {
		writeErr(w, http.StatusNotFound, "aucune release publiée")
		return
	}
	target := r.URL.Query().Get("target")
	if !ciblesApp[target] {
		writeErr(w, http.StatusBadRequest, "cible inconnue")
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "bin"
	}
	suffixe, ok := formatsApp[format]
	if !ok {
		writeErr(w, http.StatusBadRequest, "format inconnu")
		return
	}
	nom := "vecu-app-" + target + suffixe
	f, err := os.Open(filepath.Join(s.AppDist, nom))
	if err != nil {
		// Cas nominal, pas une panne : une release antérieure à v0.9.0 ne publie
		// aucun bundle. Le client en tire un repli vers le format binaire.
		writeErr(w, http.StatusNotFound, "artefact absent pour cette cible et ce format")
		return
	}
	defer f.Close()
	// Content-Type posé explicitement pour que ServeContent ne le devine pas ;
	// ServeContent gère taille et requêtes Range depuis le *os.File (ReadSeeker).
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, nom, time.Time{}, f)
}
