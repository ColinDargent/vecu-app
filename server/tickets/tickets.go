// Package tickets : les billets d'entrée à usage unique qui ouvrent une session
// web depuis l'application de bureau.
//
// LE PROBLÈME QU'IL RÉSOUT. L'app détient un jeton d'appareil (Bearer) ; les
// écrans web s'authentifient par cookie de session. Ouvrir l'admin depuis le
// menu demandait donc de ressaisir un mot de passe. La solution refusée le
// 29/07 était d'exposer le jeton d'appareil au navigateur - un jeton
// permanent, dans une URL, dans un historique. Le billet est l'inverse : il ne
// vaut qu'une fois, quelques minutes, et le jeton d'appareil ne quitte jamais
// l'app.
//
// EN MÉMOIRE, PAS EN BASE, et c'est un choix. Un billet est éphémère par
// nature : un redémarrage du serveur doit les invalider tous, ce qui est
// exactement le comportement voulu. Le garder en base demanderait une colonne
// d'horodatage (la table `device_tokens` n'en a pas), donc une migration, pour
// stocker durablement ce qui ne doit surtout pas durer.
package tickets

import (
	"sync"
	"time"

	"github.com/colindargent/vecu/server/auth"
)

// duree : la fenêtre entre le clic dans le menu et l'ouverture du navigateur.
// Assez large pour un navigateur qui démarre froid, assez courte pour qu'un
// billet oublié dans un historique ne vaille plus rien.
const duree = 2 * time.Minute

type billet struct {
	userID int64
	expire time.Time
}

// Store garde les billets vivants. Sûr en usage concurrent.
type Store struct {
	mu sync.Mutex
	m  map[string]billet
	// maintenant : injecté pour que les tests n'attendent pas deux minutes.
	maintenant func() time.Time
}

func New() *Store {
	return &Store{m: map[string]billet{}, maintenant: time.Now}
}

// Emet crée un billet pour ce compte et rend sa valeur en clair, une seule fois.
//
// Seul le HACHÉ est conservé, comme pour les jetons d'appareil : ce qui est en
// mémoire ne doit pas suffire à entrer.
func (s *Store) Emet(userID int64) (string, error) {
	jeton, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	s.m[auth.HashToken(jeton)] = billet{userID: userID, expire: s.maintenant().Add(duree)}
	return jeton, nil
}

// Consomme échange un billet contre l'identité de son porteur. Le billet est
// retiré, qu'il soit valide ou expiré : rejouer une URL ne donne jamais rien.
func (s *Store) Consomme(jeton string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cle := auth.HashToken(jeton)
	b, ok := s.m[cle]
	delete(s.m, cle)
	if !ok || s.maintenant().After(b.expire) {
		return 0, false
	}
	return b.userID, true
}

// purge retire les billets périmés. Appelée à l'émission : c'est le seul moment
// où la table grossit, donc le seul où elle doit être bornée.
func (s *Store) purge() {
	now := s.maintenant()
	for cle, b := range s.m {
		if now.After(b.expire) {
			delete(s.m, cle)
		}
	}
}
