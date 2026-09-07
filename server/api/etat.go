package api

// etat.go : la porte par laquelle un poste dit où il en est.
//
// POURQUOI UNE ROUTE A PART PLUTOT QU'UN CHAMP SUR `GET /sync` : la synchro est
// un GET, et un GET ne porte pas de corps. Y greffer l'état obligerait à le
// passer en paramètres d'URL - donc des chemins de fichiers dans une query
// string, journalisés par tout ce qui se trouve sur le trajet. Un POST séparé
// coûte un aller-retour par cycle et garde les chemins dans le corps.
//
// Le jeton d'APPAREIL seul ouvre cette porte : elle passe par `s.auth`, qui
// résout `device_tokens` et rien d'autre. Un jeton MCP n'est pas un poste, il
// n'a pas d'état de synchronisation à déclarer.

import (
	"encoding/json"
	"net/http"

	"github.com/colindargent/vecu/server/auth"
	"github.com/colindargent/vecu/server/db"
)

// etatRequest : ce qu'un poste envoie à la fin de chaque cycle.
//
// CE QU'IL N'ENVOIE PAS EST AUSSI IMPORTANT : jamais la liste de ses fichiers.
// Le serveur sait déjà ce que porte chaque commit ; avec la tête plus les
// exceptions, il déduit l'état de n'importe quel chemin. Envoyer 1200 chemins
// par poste et par cycle coûterait cent fois plus pour la même réponse.
type etatRequest struct {
	Tete           string           `json:"tete"`
	Cycle          string           `json:"cycle"`
	Laisses        []db.LaissePoste `json:"laisses"`
	ConflitsLocaux []string         `json:"conflits_locaux"`
}

// handleEtat : POST /etat.
func (s *Server) handleEtat(w http.ResponseWriter, r *http.Request) {
	// Le middleware `auth` a déjà résolu le compte, mais il ne garde pas le
	// jeton - or c'est LUI la clé, pas le compte : un humain a plusieurs postes,
	// et c'est le poste qu'on interroge. On le relit donc pour le hasher.
	token, ok := bearer(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "jeton manquant")
		return
	}
	var req etatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "corps JSON invalide")
		return
	}
	// Une tête vide est un poste qui n'a jamais reçu de delta. C'est un état
	// légitime (poste tout neuf), pas une erreur : on l'enregistre tel quel, et
	// l'écran le lira comme « rien n'est encore arrivé ici ».
	// PAS DE LISTE DE BINAIRES ICI, et c'est deliberе (Colin, 01/09) : elle
	// apprendrait au serveur le nom de fichiers qu'il n'a jamais eus. Un champ
	// `hors_perimetre` envoye par un client plus ancien est simplement ignore
	// par le decodeur - il n'est jamais stocke.
	exc := db.ExceptionsPoste{
		Laisses:        req.Laisses,
		ConflitsLocaux: req.ConflitsLocaux,
	}
	if err := s.DB.EnregistreEtatPoste(
		auth.HashToken(token), req.Tete, req.Cycle, exc, s.now(),
	); err != nil {
		writeErr(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
