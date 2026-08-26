// Historique, restauration et export ZIP. Une restauration est un commit
// comme un autre (aucune réécriture d'historique) ; l'export est figé sur un
// head unique et filtré au périmètre (complet pour un admin).
package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// logCommit : une entrée d'historique renvoyée au client.
type logCommit struct {
	OID     string `json:"oid"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Message string `json:"message"`
	Deleted bool   `json:"deleted"`
}

// GET /log/{path...} : historique d'un fichier (qui, quand, suppression ?).
// Hors droit = 404 ; un chemin jamais écrit (ou un répertoire : Log est
// strictement par fichier) = 404 aussi.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	p := r.PathValue("path")
	// Un chemin impossible est un 404 comme un inexistant, pas un 500.
	if storage.ValidPath(p) != nil {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	if !perms.CanRead(p, u.DefaultLevel, rules) {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	commits, err := s.Store.Log(p)
	if err != nil {
		log.Printf("api: log %q : %v", p, err)
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	// Jamais écrit = introuvable (cohérent avec GET /files ; un fichier
	// supprimé garde un historique et reste donc consultable/restaurable).
	if len(commits) == 0 {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	out := make([]logCommit, 0, len(commits))
	for _, c := range commits {
		out = append(out, logCommit{OID: c.OID, Author: c.Author, Date: c.Date, Message: c.Message, Deleted: c.Deleted})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "commits": out})
}

type restoreRequest struct {
	Rev string `json:"rev"` // OID du commit dont on restaure la version
}

// POST /restore/{path...} : restaure la version d'un fichier telle qu'elle
// était à une révision antérieure. C'est un commit ordinaire signé de
// l'utilisateur (message « restore p @ rev ») : jamais de réécriture
// d'historique. Sémantique assumée v1 : écrasement volontaire du head par un
// humain qui regarde l'historique - pas de CAS base_oid, une écriture
// concurrente glissée entre la consultation et le clic est recouverte (elle
// reste récupérable dans l'historique). `rev` n'est pas vérifié appartenir à
// main : les OID éphémères ne sont jamais exposés par l'API, non exploitable.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	p := r.PathValue("path")

	var req restoreRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "corps JSON invalide")
		return
	}
	// Un rev vide résoudrait vers main (défaut de Read) : commit fantôme. Refusé.
	if req.Rev == "" {
		writeErr(w, http.StatusBadRequest, "rev requis")
		return
	}
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	if !perms.CanRead(p, u.DefaultLevel, rules) {
		writeErr(w, http.StatusNotFound, "introuvable")
		return
	}
	if !perms.CanWrite(p, u.DefaultLevel, rules) {
		writeErr(w, http.StatusForbidden, "écriture non autorisée")
		return
	}
	content, err := s.Store.Read(p, req.Rev)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "révision invalide ou fichier absent de cette version")
		return
	}
	head, err := s.Store.WriteWithMessage(p, content, u.Username,
		fmt.Sprintf("restore %s @ %.12s", p, req.Rev))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "restauration refusée")
		return
	}
	writeJSON(w, http.StatusOK, writeResponse{Head: head})
}

// ExportZIP écrit dans w un ZIP du contenu au head courant : tout le dépôt
// pour un admin, le périmètre lisible sinon. Partagé avec le web admin.
// Source: https://pkg.go.dev/archive/zip
func ExportZIP(store *storage.Store, u *db.User, rules []perms.Rule, w io.Writer) error {
	head, err := store.Head()
	if err != nil {
		return err
	}
	files, err := store.List(head)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	for _, p := range files {
		// L'exemption admin ne couvre PAS les skills : un admin n'exporte que les
		// siens et ceux qu'on lui a partagés (privé par défaut, y compris de lui).
		if !(u.IsAdmin && !sousSkills(p)) && !perms.CanRead(p, u.DefaultLevel, rules) {
			continue
		}
		content, err := store.Read(p, head)
		if err != nil {
			return err
		}
		f, err := zw.Create(p)
		if err != nil {
			return err
		}
		if _, err := f.Write([]byte(content)); err != nil {
			return err
		}
	}
	return zw.Close()
}

// GET /export : ZIP du périmètre (complet pour un admin), figé sur un head.
// Bufferisé en mémoire (v1 texte-only, tailles de vault) : une erreur donne
// un vrai 500 au lieu d'un ZIP tronqué en 200.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	rules, err := s.DB.Rules(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	var buf bytes.Buffer
	if err := ExportZIP(s.Store, u, rules, &buf); err != nil {
		log.Printf("api: export : %v", err)
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Content-Disposition",
		`attachment; filename="vecu-export-`+s.now().Format("2006-01-02")+`.zip"`)
	w.Write(buf.Bytes())
}
