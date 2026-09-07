package sync

// rapport.go : le poste dit au serveur où il en est (DAR-198, slice 2).
//
// POURQUOI CE FICHIER EXISTE. Le serveur n'a AUCUNE position par poste :
// `device_tokens` porte trois colonnes et rien qui dise ce que ce poste a
// vraiment sur son disque. Trois des quatre états que le mainteneur doit voir -
// un fichier laissé et son motif, un binaire écarté, une copie de conflit en
// attente - ne vivent QUE dans l'état local, et aucune surface distante ne les
// montre. C'est ce trou que ce rapport ferme.
//
// CE QU'IL N'ENVOIE PAS. Jamais la liste des fichiers du poste. Le serveur sait
// déjà ce que porte chaque commit ; avec la tête plus les exceptions il déduit
// l'état de n'importe quel chemin. Envoyer 1200 chemins par cycle coûterait
// cent fois plus pour la même réponse.
//
// BEST-EFFORT, ET C'EST UN CHOIX DUR. Un rapport qui échoue ne fait JAMAIS
// échouer le cycle. Le cas qui l'impose est un client neuf devant un serveur
// qui n'a pas encore la route : il recevrait 404 à chaque cycle, et faire
// remonter cette erreur casserait la synchronisation de tout un poste pour un
// renseignement d'affichage. La synchronisation est la fonction, le rapport est
// un commentaire dessus.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// rapportEtat : le corps de POST /etat. Doit rester aligné sur `etatRequest`
// côté serveur ; le contrat entre les deux est ce JSON, pas un type partagé.
type rapportEtat struct {
	Tete           string   `json:"tete"`
	Cycle          string   `json:"cycle"`
	Laisses        []Laisse `json:"laisses"`
	ConflitsLocaux []string `json:"conflits_locaux"`
}

// RapporteEtat : POST /etat. Rend une erreur, que l'appelant journalise sans
// s'y arrêter.
func (c *HTTPClient) RapporteEtat(r rapportEtat) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", c.Server+"/etat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 204 attendu. On accepte toute 2xx : un serveur qui répondrait 200 avec un
	// corps vide fait le travail, et refuser serait du zèle.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("POST /etat : HTTP %d", resp.StatusCode)
	}
	return nil
}

// Rapporte : ce que ce poste sait de lui-même, envoyé au serveur.
//
// DEUX LISTES, ET DEUX SEULEMENT :
//   - `e.laisses` est le cycle qui vient de finir (mémoire) ;
//   - `e.copies` est dérivé du disque au dernier scan.
//
// LES BINAIRES NE REMONTENT PAS (Colin, 01/09). Le registre `HorsPerimetre` du
// poste porte des chemins que le serveur n'a JAMAIS eus - un PDF, une photo
// posés dans un espace partagé. Les envoyer apprendrait au serveur, donc au
// mainteneur d'un client, le nom de fichiers personnels que personne n'a choisi
// de partager. Les deux listes qui restent ne posent pas ce problème : leurs
// chemins sont ceux de fichiers que le serveur porte déjà.
//
// CE QUE CA COUTE, ET C'EST ASSUME : l'écran ne répond plus à « pourquoi mon
// PNG ne se synchronise pas ». La réponse reste dans `vecu status`, sur le
// poste concerné.
func (e *Engine) Rapporte() error {
	if e.client == nil {
		return nil // poste non authentifié : rien à dire, et personne à qui.
	}
	// Les laisses PERDUES seulement : une remarque qui ne signale pas un contenu
	// absent du serveur n'est pas un manque, et la remonter ferait clignoter
	// l'écran sur le fonctionnement normal.
	var laisses []Laisse
	for _, l := range e.laisses {
		if l.Perdu() {
			laisses = append(laisses, l)
		}
	}
	copies := append([]string(nil), e.copies...)

	return e.client.RapporteEtat(rapportEtat{
		Tete:           e.state.Head,
		Cycle:          e.state.Cycle,
		Laisses:        laisses,
		ConflitsLocaux: copies,
	})
}
