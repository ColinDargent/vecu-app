package web

// etat.go : l'écran du mainteneur (DAR-198).
//
// Il répond à UNE question : « est-ce que ce fichier est bien arrivé chez tout
// le monde ». C'est la première fonction du chapitre 2 qui ne vient pas de
// l'étude d'un concurrent, et la seule qui ait un utilisateur nommé - la
// personne qui maintient le second cerveau chez le client, après notre départ.
//
// TROIS REPONSES, PAS DEUX. Oui, non, et « je n'en sais rien ». La troisième
// est celle qui fait le lot : un poste éteint depuis trois semaines et un poste
// parfaitement à jour ne doivent jamais rendre la même case. Sans elle, l'écran
// affiche du vert rassurant sur une flotte dont il ignore tout.

import (
	"net/http"
	"sort"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/fichiers"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/skills"
)

// colonnePoste : un poste, tel qu'il titre une colonne.
type colonnePoste struct {
	Libelle string
	Compte  string
	AParle  bool
	RecuLe  string
	// TeteIllisible : le poste a déclaré une tête que le dépôt ne connaît pas.
	// Traité comme « sans nouvelles » plutôt que comme une flotte à zéro : une
	// carte d'empreintes vide se lirait « ce poste n'a aucun fichier », donc
	// tout en rouge, alors que la vérité est « je ne sais pas lire sa position ».
	TeteIllisible bool
}

// ligneFichier : un chemin, son état par poste, et qui y a accès.
type ligneFichier struct {
	Chemin       string
	SurLeServeur bool
	Cases        []fichiers.Case
	// Arrive / Certain : le résumé à trois valeurs de la ligne.
	Arrive  bool
	Certain bool
	// Acces : qui peut lire ce chemin, et d'où vient le droit.
	Acces []accesCompte
}

type accesCompte struct {
	Compte string
	Niveau string
	// Source : « posé sur ce compte », « cohorte X », « défaut du dossier »,
	// « défaut du compte ». Réutilise la résolution livrée par DAR-196.
	Source string
}

type etatData struct {
	baseData
	Postes []colonnePoste
	Lignes []ligneFichier
	// Muets : combien de postes n'ont rien dit. Affiché en tête, parce qu'un
	// tableau plein de « sans nouvelles » doit s'expliquer d'un coup d'oeil.
	Muets int
	// AucunNeParle : AUCUN poste connu n'a jamais rapporté. C'est un état
	// différent de « certains sont muets », et il a une cause unique et
	// nommable : les clients installés sont antérieurs à la version qui sait
	// rapporter (02/09, retour de Colin - « je ne comprends pas cette
	// section »). Tant qu'il dure, le tableau ne peut RIEN dire, et le dire
	// vaut mieux que de laisser lire une grille vide comme une panne.
	AucunNeParle bool
}

// handleEtatFichiers : GET /admin/etat.
//
// L'ADRESSE N'EST PAS /admin/fichiers, DELIBEREMENT. Cette adresse-là sert une
// redirection 301 vers /admin/dossiers depuis le 21/08, posée pour les favoris
// et l'historique des navigateurs. Quelqu'un qui l'a en favori voulait
// PARCOURIR des fichiers ; le renvoyer sur un tableau de santé serait un
// mauvais service. Deux besoins, deux adresses.
func (s *Server) handleEtatFichiers(w http.ResponseWriter, r *http.Request, u *db.User) {
	serveur, err := s.Store.Empreintes("")
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	// Le périmètre du mainteneur : la zone skills a son propre écran et ses
	// propres règles, elle ne se mélange pas ici.
	for chemin := range serveur {
		if skills.SousRacine(chemin) {
			delete(serveur, chemin)
		}
	}

	etats, err := s.DB.EtatsPostes()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	comptes, err := s.DB.ListUsers()
	if err != nil {
		erreurHTTP(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	nomDuCompte := map[int64]string{}
	for i := range comptes {
		nomDuCompte[comptes[i].ID] = comptes[i].Username
	}

	arbres := &memoArbres{store: s.Store, vus: map[string]resultatArbre{}}
	postes := make([]fichiers.Poste, 0, len(etats))
	colonnes := make([]colonnePoste, 0, len(etats))
	muets := 0
	for _, e := range etats {
		col := colonnePoste{Libelle: e.Libelle, Compte: nomDuCompte[e.UserID],
			AParle: e.AParle(), RecuLe: e.RecuLe}
		p := fichiers.Poste{Libelle: e.Libelle, AParle: e.AParle(), RecuLe: e.RecuLe,
			Exceptions: convertitExceptions(e.Exceptions)}
		if p.AParle {
			// UNE invocation git par TETE DISTINCTE, pas par poste. Des postes à
			// jour partagent la même tête, donc le même arbre : sur une flotte
			// saine - le cas normal - c'est UN `ls-tree` quel que soit le nombre
			// de machines. Mesuré : 11,4 ms par tête distincte sur 1186
			// fichiers, donc sans ce cache le seuil de 140 ms de DAR-195 tombait
			// vers neuf postes.
			emp, err := arbres.a(e.Tete)
			if err != nil {
				// Tête illisible : on retombe sur « sans nouvelles », la lecture
				// prudente. Un poste peut envoyer n'importe quoi.
				p.AParle, col.AParle, col.TeteIllisible = false, false, true
			} else {
				p.Empreintes = emp
			}
		}
		if !col.AParle {
			muets++
		}
		postes = append(postes, p)
		colonnes = append(colonnes, col)
	}

	resolus := s.resolveurs(comptes)
	tableau := fichiers.Tableau(serveur, postes)
	lignes := make([]ligneFichier, 0, len(tableau))
	for _, l := range tableau {
		arrive, certain := fichiers.ToutArrive(l)
		lignes = append(lignes, ligneFichier{
			Chemin: l.Chemin, SurLeServeur: l.SurLeServeur, Cases: l.Cases,
			Arrive: arrive, Certain: certain,
			Acces: s.accesDuChemin(l.Chemin, comptes, resolus),
		})
	}

	render(w, http.StatusOK, "etat", etatData{
		baseData: base(u, "etat"), Postes: colonnes, Lignes: lignes, Muets: muets,
		AucunNeParle: len(colonnes) > 0 && muets == len(colonnes),
	})
}

// convertitExceptions : du type de la base vers celui du paquet de déduction.
// Les deux existent séparément pour que `fichiers` ne dépende ni de la base ni
// de git - c'est ce qui le rend testable sous mutation sans monter un dépôt.
func convertitExceptions(e db.ExceptionsPoste) fichiers.Exceptions {
	out := fichiers.Exceptions{
		Laisses:        make(map[string]string, len(e.Laisses)),
		ConflitsLocaux: make(map[string]bool, len(e.ConflitsLocaux)),
	}
	for _, l := range e.Laisses {
		out.Laisses[l.Chemin] = l.Raison
	}
	for _, c := range e.ConflitsLocaux {
		out.ConflitsLocaux[c] = true
	}
	return out
}

// accesDuChemin : qui peut lire ce chemin, et d'où vient le droit.
//
// Réutilise la résolution livrée par DAR-196 le 01/09 plutôt que d'en
// réécrire une : l'écran des droits et celui-ci doivent dire la même chose,
// et deux implémentations divergeraient au premier changement de modèle.
// resolveurs : les règles de chaque compte, chargées UNE FOIS pour tout l'écran.
//
// Sans ça, l'écran fait une requête SQL par (fichier, compte) : 14 232 sur le
// banc de DAR-195, soit 645 ms contre un seuil de 140. Le coût n'était pas dans
// git, il était dans la répétition d'une requête minuscule.
func (s *Server) resolveurs(comptes []db.User) []*db.Resolveur {
	out := make([]*db.Resolveur, len(comptes))
	for i := range comptes {
		r, err := s.DB.ResolveurDe(&comptes[i])
		if err != nil {
			continue // ce compte ne figurera dans aucune liste d'accès.
		}
		out[i] = r
	}
	return out
}

func (s *Server) accesDuChemin(chemin string, comptes []db.User, resolus []*db.Resolveur) []accesCompte {
	var out []accesCompte
	for i := range comptes {
		if resolus[i] == nil {
			continue
		}
		niveau, origine := resolus[i].Droit(chemin)
		if niveau == perms.Invisible {
			// Un compte qui ne voit pas le fichier n'a pas à figurer dans la
			// liste de ceux qui y ont accès. « Privé » est l'absence de ligne,
			// pas une ligne qui dit « privé ».
			continue
		}
		out = append(out, accesCompte{Compte: comptes[i].Username,
			Niveau: niveauFR(niveau), Source: origineEnClair(origine)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Compte < out[b].Compte })
	return out
}

// memoArbres : un arbre par tête distincte, le temps d'un rendu.
//
// La mémoire ne survit pas à la requête, délibérément : un cache qui traverse
// les requêtes devrait s'invalider à chaque écriture, et une invalidation
// oubliée afficherait un état périmé - exactement le défaut que cet écran
// existe pour attraper.
type memoArbres struct {
	store interface {
		Empreintes(rev string) (map[string]string, error)
	}
	vus map[string]resultatArbre
}

type resultatArbre struct {
	empreintes map[string]string
	err        error
}

// a : l'arbre à cette tête. Les ECHECS sont mémoïsés aussi - sans ça, huit
// postes partageant une même tête illisible relanceraient huit fois un git qui
// échoue, ce qui est le pire des deux mondes.
func (m *memoArbres) a(tete string) (map[string]string, error) {
	if r, ok := m.vus[tete]; ok {
		return r.empreintes, r.err
	}
	emp, err := m.store.Empreintes(tete)
	m.vus[tete] = resultatArbre{empreintes: emp, err: err}
	return emp, err
}
