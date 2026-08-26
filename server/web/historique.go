// Historique d'un fichier, restauration en un clic, export ZIP.
// Mêmes règles que l'API : lecture pour consulter, écriture pour restaurer,
// hors droit = 404 indistinguable de l'inexistant.
package web

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/colindargent/vecu/server/api"
	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

type versionVM struct {
	OID     string
	Date    string
	Auteur  string
	Message string
	Deleted bool
}

type historiqueData struct {
	baseData
	Chemin        string
	Fil           []segmentVM
	HrefRestaurer string
	PeutRestaurer bool
	Versions      []versionVM
	Erreur        string
}

// dateFR : ISO 8601 → « 2026-07-23 17h30 » (heure locale du serveur).
func dateFR(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.Local().Format("2006-01-02 15h04")
}

func (s *Server) renderHistorique(w http.ResponseWriter, status int, u *db.User, p string, erreur string) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	commits, err := s.Store.Log(p)
	if err != nil {
		log.Printf("web: historique %q : %v", p, err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	// Jamais écrit (ou répertoire) : indistinguable de l'inexistant.
	if len(commits) == 0 {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}
	data := historiqueData{
		baseData:      base(u, "dossiers"),
		Chemin:        p,
		Fil:           fil(p),
		HrefRestaurer: hrefAdmin("/admin/restaurer", p),
		PeutRestaurer: perms.CanWrite(p, u.DefaultLevel, rules),
		Erreur:        erreur,
	}
	for _, c := range commits {
		data.Versions = append(data.Versions, versionVM{
			OID: c.OID, Date: dateFR(c.Date), Auteur: c.Author, Message: c.Message, Deleted: c.Deleted,
		})
	}
	render(w, status, "historique", data)
}

// canReadOr404 : évalue la lecture, écrit le 404 indistinguable sinon.
func (s *Server) canReadOr404(w http.ResponseWriter, u *db.User, p string) bool {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return false
	}
	if !perms.CanRead(p, u.DefaultLevel, rules) {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return false
	}
	return true
}

// GET /admin/historique/{path...} : versions d'un fichier (qui, quand).
// Un chemin impossible (vide, caractère de contrôle, ..) est un 404 comme un
// fichier inexistant - jamais un 500 distinguable.
func (s *Server) handleHistorique(w http.ResponseWriter, r *http.Request, u *db.User) {
	p := perms.Canon(r.PathValue("path"))
	if storage.ValidPath(p) != nil {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}
	if !s.canReadOr404(w, u, p) {
		return
	}
	s.renderHistorique(w, http.StatusOK, u, p, "")
}

// POST /admin/restaurer/{path...} : restaure la version `rev` (un clic).
// Même sémantique que l'API : commit ordinaire « restore p @ rev », écrasement
// volontaire du head, récupérable dans l'historique.
func (s *Server) handleRestaurer(w http.ResponseWriter, r *http.Request, u *db.User) {
	p := perms.Canon(r.PathValue("path"))
	if storage.ValidPath(p) != nil {
		erreurHTTP(w, "introuvable", http.StatusNotFound)
		return
	}
	if !s.canReadOr404(w, u, p) {
		return
	}
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	if !perms.CanWrite(p, u.DefaultLevel, rules) {
		erreurHTTP(w, "écriture non autorisée", http.StatusForbidden)
		return
	}
	rev := r.PostFormValue("rev")
	if rev == "" {
		s.renderHistorique(w, http.StatusBadRequest, u, p, "révision manquante")
		return
	}
	content, err := s.Store.Read(p, rev)
	if err != nil {
		s.renderHistorique(w, http.StatusBadRequest, u, p,
			"cette version ne contient pas le fichier (suppression) ou la révision est invalide")
		return
	}
	if _, err := s.Store.WriteWithMessage(p, content, u.Username, fmt.Sprintf("restore %s @ %.12s", p, rev)); err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, hrefAdmin("/admin/historique", p), http.StatusSeeOther)
}

// GET /admin/export.zip : ZIP du périmètre (complet pour un admin).
// Réutilise api.ExportZIP : un seul code de filtrage.
func (s *Server) handleExportZip(w http.ResponseWriter, r *http.Request, u *db.User) {
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := api.ExportZIP(s.Store, u, rules, &buf); err != nil {
		log.Printf("web: export : %v", err)
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Content-Disposition",
		`attachment; filename="vecu-export-`+time.Now().Format("2006-01-02")+`.zip"`)
	w.Write(buf.Bytes())
}
