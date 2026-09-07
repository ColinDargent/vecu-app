// Package storage : cœur git du serveur Vécu.
// Toute écriture est un commit sur main d'un repo bare, via le binaire git
// (plumbing : hash-object, read-tree, update-index, write-tree, commit-tree).
// Le client ne voit jamais git : ce package est le seul à lui parler.
package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Store encapsule un repo git bare.
//
// Invariant de concurrence : un seul *Store par repo par process. Le mutex
// sérialise TOUTES les écritures d'un même Store ; c'est la seule garantie
// réelle de non-corruption. L'API (slices 4-5) doit partager un Store, pas en
// créer un par requête.
//
// Le CAS de commitIndex (update-ref sur l'ancien head) est un garde-fou, pas
// une garantie multi-process : commitIndex lit l'arbre puis re-lit head, donc
// une écriture d'un AUTRE process glissée entre les deux passerait le CAS. Pour
// une vraie sécurité multi-instance sur le même repo (non visée en v1), il
// faudrait un verrou inter-process. Ne pas s'appuyer sur ce CAS pour ça.
type Store struct {
	dir string // chemin du repo bare (ex: data/brain.git)
	mu  sync.Mutex
	// creCache : la carte des créateurs et sa clé d'invalidation. Voir
	// createurs.go - son verrou est distinct de `mu`, et jamais imbriqué avec.
	creCache
	// appelsGit : nombre d'invocations du binaire git depuis l'ouverture du
	// Store. Sert UNIQUEMENT de garde de test : c'est ce qui fait échouer un
	// retour au `git log` par fichier, dont le coût (56 s contre 212 ms) ne se
	// voit dans aucune assertion de résultat.
	appelsGit atomic.Int64
}

// Commit décrit une entrée d'historique.
type Commit struct {
	OID     string
	Author  string
	Date    string // ISO 8601
	Message string
	Deleted bool // ce commit supprime le chemin (tombstone : Read y échoue)
}

// Init crée (ou rouvre) le repo bare avec une branche main et un commit racine vide.
func Init(dir string) (*Store, error) {
	s := &Store{dir: dir}
	if _, err := os.Stat(path.Join(dir, "HEAD")); err == nil {
		return s, nil // repo déjà initialisé
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if _, err := s.git(nil, "init", "--bare", "--initial-branch=main"); err != nil {
		return nil, err
	}
	// Commit racine sur l'arbre vide : main existe toujours, les merges ont une base.
	tree, err := s.git(nil, "mktree")
	if err != nil {
		return nil, err
	}
	commit, err := s.git(commitEnv("vecu"), "commit-tree", tree, "-m", "init")
	if err != nil {
		return nil, err
	}
	if _, err := s.git(nil, "update-ref", "refs/heads/main", commit); err != nil {
		return nil, err
	}
	return s, nil
}

// Head renvoie l'OID du commit courant de main.
func (s *Store) Head() (string, error) {
	return s.git(nil, "rev-parse", "refs/heads/main")
}

// Write commit le contenu à ce chemin sur main et renvoie l'OID du nouveau commit.
func (s *Store) Write(p, content, author string) (string, error) {
	return s.WriteWithMessage(p, content, author, fmt.Sprintf("update %s", p))
}

// WriteWithMessage : comme Write, avec un message de commit explicite
// (restauration : « restore p @ rev », pour que l'historique porte la trace).
func (s *Store) WriteWithMessage(p, content, author, message string) (string, error) {
	if err := validatePath(p); err != nil {
		return "", err
	}
	if err := validateAuthor(author); err != nil {
		return "", err
	}
	if hasControlChar(message) {
		return "", fmt.Errorf("message de commit invalide")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(p, content, author, message)
}

// writeLocked : cœur d'écriture directe sur main (verrou déjà tenu).
func (s *Store) writeLocked(p, content, author, message string) (string, error) {
	blob, err := s.gitStdin(content, nil, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return s.commitIndex(author, message, func(env []string) error {
		_, err := s.git(env, "update-index", "--add",
			"--cacheinfo", "100644,"+blob+","+p)
		return err
	})
}

// Delete commit la suppression du chemin (tombstone naturel de git :
// restaurable depuis l'historique, aucune résurrection possible).
func (s *Store) Delete(p, author string) (string, error) {
	if err := validatePath(p); err != nil {
		return "", err
	}
	if err := validateAuthor(author); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.git(nil, "cat-file", "-e", "refs/heads/main:"+p); err != nil {
		return "", fmt.Errorf("fichier inconnu : %s", p)
	}
	return s.commitIndex(author, fmt.Sprintf("delete %s", p), func(env []string) error {
		// Mode 0 = retrait du chemin ; seule forme qui marche en repo bare.
		// Source: https://git-scm.com/docs/git-update-index#_using_index_info
		_, err := s.gitStdin("0 0000000000000000000000000000000000000000\t"+p+"\n",
			env, "update-index", "--index-info")
		return err
	})
}

// commitIndex : charge l'arbre de main dans un index temporaire, applique
// mutate, écrit l'arbre, commit sur main. Toute écriture passe par ici.
func (s *Store) commitIndex(author, message string, mutate func(env []string) error) (string, error) {
	idx, err := os.CreateTemp("", "vecu-index-*")
	if err != nil {
		return "", err
	}
	idx.Close()
	defer os.Remove(idx.Name())
	env := []string{"GIT_INDEX_FILE=" + idx.Name()}

	if _, err := s.git(env, "read-tree", "refs/heads/main"); err != nil {
		return "", err
	}
	if err := mutate(env); err != nil {
		return "", err
	}
	tree, err := s.git(env, "write-tree")
	if err != nil {
		return "", err
	}
	head, err := s.Head()
	if err != nil {
		return "", err
	}
	// ARBRE IDENTIQUE AU PARENT : il n'y a rien à committer, et committer quand
	// même fabriquerait un commit vide sur `main`.
	//
	// La garde existait déjà pour un DELETE sur un chemin absent, mais elle
	// vivait plus haut et ne couvrait pas toutes les formes de no-op : mesuré,
	// un DELETE sur un chemin de DOSSIER retirait une entrée d'index qui
	// n'existe pas, produisait le même arbre, et faisait quand même avancer le
	// head - donc un appelant qui compare le head avant/après pour savoir si
	// quelque chose a bougé se faisait mentir. Ici, la garde couvre toutes les
	// mutations parce qu'elle porte sur le RÉSULTAT.
	if head != "" {
		if parent, err := s.git(nil, "rev-parse", head+"^{tree}"); err == nil && parent == tree {
			return head, nil
		}
	}
	commit, err := s.git(commitEnv(author), "commit-tree", tree, "-p", head, "-m", message)
	if err != nil {
		return "", err
	}
	// CAS sur l'ancienne valeur : échoue si main a bougé sous nos pieds.
	if _, err := s.git(nil, "update-ref", "refs/heads/main", commit, head); err != nil {
		return "", err
	}
	return commit, nil
}

// BaseHeadCourant : le `baseOID` d'un appelant qui n'a pas de marque-page et
// veut écrire « comme un pair parfaitement à jour ».
//
// Pourquoi ce sentinel plutôt qu'un `Head()` fait par l'appelant. La surface
// MCP lisait le head, puis appelait `WriteMerge`, qui prend le verrou APRÈS.
// Rien ne couvrait l'intervalle - et il n'est pas microscopique : `Head()`
// forke `git rev-parse`, puis le verrou peut attendre derrière une écriture en
// cours. Mesuré : une base périmée d'un seul commit sur le même chemin produit
// une copie de conflit. Ici, la base est lue DANS la section critique, donc
// l'écriture est réellement atomique.
const BaseHeadCourant = "@head"

func resoutBaseHead(baseOID, head string) string {
	if baseOID == BaseHeadCourant {
		return head
	}
	return baseOID
}

// WriteResult décrit l'issue d'une écriture avec base_oid (modèle Dropbox).
type WriteResult struct {
	Head         string // head de main après l'opération
	Merged       bool   // true si une fusion 3-way a été appliquée
	Conflict     bool   // true si une copie de conflit a été créée
	ConflictPath string // chemin de la copie de conflit (si Conflict)
}

// WriteMerge écrit `content` à `p` en partant de la version `baseOID` que le
// client détenait, et fusionne avec main (modèle validé) :
//   - baseOID == head : main n'a pas bougé, commit direct.
//   - sinon, fusion 3-way (git merge-tree) entre main et l'édition du client :
//   - propre : commit de merge (les changements des autres sont préservés).
//   - conflit : main garde sa version de `p` ; le contenu du client est
//     committé à côté sous `conflictName` (copie de conflit synchronisée).
//
// `conflictName` est calculé par l'appelant (il porte l'horodatage et l'auteur)
// et n'est utilisé qu'en cas de conflit.
//
// `baseOID` peut valoir `BaseHeadCourant` : l'ancêtre est alors le head lu SOUS
// LE VERROU. Voir la constante.
// WriteMerge écrit `p`, en fusionnant si la base du client est derrière.
//
// `empreinteAttendue` est la GARDE D'ÉCHO de DAR-114, et elle est facultative.
// Voir `echoRompu` juste en dessous pour ce qu'elle ferme.
func (s *Store) WriteMerge(p, content, author, baseOID, conflictName string) (WriteResult, error) {
	return s.WriteMergeGardee(p, content, author, baseOID, conflictName, "")
}

// echoRompu : le contenu que le serveur porte à `p` n'est pas celui que le
// client croit y remplacer.
//
// POURQUOI CETTE GARDE EXISTE (DAR-114, banc du 02/09). Le chemin rapide de
// cette fonction - `baseOID == head`, donc « le client est à jour, aucune
// fusion nécessaire » - fait confiance à l'ANCÊTRE DÉCLARÉ par le client, et
// ne vérifie jamais que le contenu envoyé en descend. Un poste qui déclare une
// base à jour avec un contenu qui ne l'est pas écrase donc la version de
// quelqu'un d'autre, DIRECTEMENT : pas de fusion, pas de conflit, pas de copie.
//
// C'est le mode d'échec du 10/08, celui que trois semaines d'analyse n'avaient
// pas expliqué : tout `shared/` revenu à l'état du pair distant, dix-huit
// cartes perdues, et AUCUNE copie de conflit. Le garde-fou du vault - « rien
// n'est jamais écrasé silencieusement » - ne pouvait pas jouer, puisque le
// serveur ne se posait pas la question.
//
// Comment un poste arrive dans cet état : avant le marque-page PAR FICHIER
// (v0.5.0, 19/08), le client déclarait sa tête GLOBALE comme ancêtre de chaque
// chemin. Un poste dont la tête avait avancé sans que ce chemin-là ne
// redescende était donc dans cet état par construction - et le 10/08 est
// antérieur au 19/08. Le marque-page par fichier a fermé cette porte-là ; cette
// garde ferme la CLASSE, quelle qu'en soit la cause future - état restauré
// depuis une sauvegarde, `state.json` édité à la main, client d'une version
// qu'on ne contrôle pas, bug à venir.
//
// FACULTATIVE PAR CONSTRUCTION. Une empreinte vide désactive la garde, donc un
// client qui ne l'envoie pas retrouve exactement le comportement d'avant. C'est
// ce qui rend le déploiement sûr : les postes en v0.9.1 ne l'envoient pas, ils
// ne cassent pas. Ils ne sont pas protégés non plus, et c'est la raison pour
// laquelle cette garde appelle une release.
func (s *Store) echoRompu(p, empreinteAttendue string) bool {
	if empreinteAttendue == "" {
		return false // le client ne dit rien : on ne conclut rien
	}
	courant, err := s.Read(p, "")
	if err != nil {
		// Le chemin n'existe pas côté serveur. Le client croyait y remplacer
		// quelque chose : c'est déjà une divergence, mais l'écriture ne détruit
		// rien. On laisse passer plutôt que de bloquer une création.
		return false
	}
	return hashContenu(courant) != empreinteAttendue
}

// hashContenu : la MÊME empreinte que le client, sinon la comparaison n'a aucun
// sens. `client/sync.hashContent` est un sha256 hexadécimal du contenu brut.
func hashContenu(contenu string) string {
	somme := sha256.Sum256([]byte(contenu))
	return hex.EncodeToString(somme[:])
}

// WriteMergeGardee : `WriteMerge`, plus la garde d'écho.
func (s *Store) WriteMergeGardee(p, content, author, baseOID, conflictName, empreinteAttendue string) (WriteResult, error) {
	if err := validatePath(p); err != nil {
		return WriteResult{}, err
	}
	if err := validateAuthor(author); err != nil {
		return WriteResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	head, err := s.Head()
	if err != nil {
		return WriteResult{}, err
	}
	baseOID = resoutBaseHead(baseOID, head)

	// Fast path : le client est à jour, aucune fusion nécessaire - ET il porte
	// bien ce que le serveur porte. La seconde moitié est la garde d'écho.
	if baseOID == head {
		if s.echoRompu(p, empreinteAttendue) {
			// LE MODÈLE DROPBOX, ET RIEN D'AUTRE. Le serveur garde sa version,
			// celle du client atterrit à côté. C'est le même geste que sur un
			// conflit de fusion, pour la même raison : on ne sait pas laquelle
			// des deux est la bonne, donc on ne choisit pas - on rend la
			// divergence VISIBLE au lieu de la trancher en silence.
			if err := validatePath(conflictName); err != nil {
				return WriteResult{}, fmt.Errorf("nom de copie de conflit invalide : %w", err)
			}
			freeName, err := s.freeConflictPath(conflictName)
			if err != nil {
				return WriteResult{}, err
			}
			newHead, err := s.writeLocked(freeName, content, author, fmt.Sprintf("update %s", freeName))
			if err != nil {
				return WriteResult{}, err
			}
			return WriteResult{Head: newHead, Conflict: true, ConflictPath: freeName}, nil
		}
		newHead, err := s.writeLocked(p, content, author, fmt.Sprintf("update %s", p))
		if err != nil {
			return WriteResult{}, err
		}
		return WriteResult{Head: newHead}, nil
	}

	// Édition du client matérialisée en commit éphémère sur sa base.
	clientCommit, err := s.ephemeralWrite(p, content, author, baseOID)
	if err != nil {
		return WriteResult{}, err
	}

	tree, conflict, err := s.mergeTree(head, clientCommit)
	if err != nil {
		return WriteResult{}, err
	}
	if conflict {
		// Modèle Dropbox : main garde sa version ; le client atterrit à côté.
		if err := validatePath(conflictName); err != nil {
			return WriteResult{}, fmt.Errorf("nom de copie de conflit invalide : %w", err)
		}
		// INVARIANT : jamais d'écrasement. Si le nom est déjà pris (deux conflits
		// dans la même minute, retry, ou collision avec un fichier existant), on
		// désambiguïse jusqu'à un chemin libre. Une copie n'efface jamais rien.
		freeName, err := s.freeConflictPath(conflictName)
		if err != nil {
			return WriteResult{}, err
		}
		newHead, err := s.writeLocked(freeName, content, author, fmt.Sprintf("update %s", freeName))
		if err != nil {
			return WriteResult{}, err
		}
		return WriteResult{Head: newHead, Conflict: true, ConflictPath: freeName}, nil
	}

	// Fusion propre : commit de merge à deux parents.
	mergeCommit, err := s.git(commitEnv(author),
		"commit-tree", tree, "-p", head, "-p", clientCommit,
		"-m", fmt.Sprintf("merge update %s", p))
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := s.git(nil, "update-ref", "refs/heads/main", mergeCommit, head); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Head: mergeCommit, Merged: true}, nil
}

// DeleteMerge supprime `p` en partant de `baseOID`, et fusionne avec main.
//   - baseOID == head : suppression directe.
//   - sinon, fusion 3-way : propre → commit de merge ; conflit (quelqu'un a
//     modifié `p` entre-temps) → la version de main part dans une copie de
//     conflit ET la suppression s'applique.
//
// Ce dernier point est la « deuxième porte ». Avant, main gardait sa version et
// la suppression était abandonnée : la bascule fichier → dossier, le geste le
// plus banal d'un vault, ne passait alors jamais - le fichier n'étant pas
// supprimé, git refuse un dossier du même nom, et le chemin gèle indéfiniment.
// Et surtout, c'est la sortie du refus d'écriture : retirer l'obstacle - le geste
// que le moteur demande lui-même - fait constater une suppression. Ranger d'abord
// puis supprimer fait passer les deux sans que rien ne disparaisse.
//
// `conflictName` est calculé par l'appelant (il porte l'horodatage et l'auteur)
// et n'est utilisé qu'en cas de divergence, exactement comme pour WriteMerge.
func (s *Store) DeleteMerge(p, author, baseOID, conflictName string) (WriteResult, error) {
	if err := validatePath(p); err != nil {
		return WriteResult{}, err
	}
	if err := validateAuthor(author); err != nil {
		return WriteResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	head, err := s.Head()
	if err != nil {
		return WriteResult{}, err
	}
	baseOID = resoutBaseHead(baseOID, head)

	if baseOID == head {
		if _, err := s.git(nil, "cat-file", "-e", head+":"+p); err != nil {
			return WriteResult{Head: head}, nil // déjà absent : no-op idempotent
		}
		newHead, err := s.deleteLocked(p, author)
		if err != nil {
			return WriteResult{}, err
		}
		return WriteResult{Head: newHead}, nil
	}

	// Sans base commune, la fusion à trois voies ne peut PAS porter cette
	// suppression, et elle ne le dit pas. Le côté client est alors un commit
	// orphelin sur l'arbre vide, dont retirer `p` laisse l'arbre vide : pour
	// `merge-tree --allow-unrelated-histories`, ce côté n'apporte rien, la fusion
	// est propre et rend l'arbre de main À L'IDENTIQUE. La réponse annonce donc un
	// succès (`Merged: true`) pendant que le chemin survit - et le client, qui ne
	// voit plus le fichier sur son disque, reconstate la suppression au cycle
	// suivant, indéfiniment, sans qu'aucune trace ne relie la cause à l'effet.
	//
	// On la traite comme une divergence, ce qu'elle est : on ne sait pas
	// distinguer « ce membre a supprimé ce fichier » de « ce membre ne l'a jamais
	// eu ». La version de main part en copie, la suppression s'applique. Rien ne
	// disparaît, et le chemin ne gèle pas.
	if baseOID != "" {
		clientCommit, err := s.ephemeralDelete(p, author, baseOID)
		if err != nil {
			return WriteResult{}, err
		}
		tree, conflict, err := s.mergeTree(head, clientCommit)
		if err != nil {
			return WriteResult{}, err
		}
		if !conflict {
			return s.mergeDelete(p, author, head, tree, clientCommit)
		}
	}
	return s.rangeEnCopiePuisSupprime(p, author, head, conflictName)
}

// rangeEnCopiePuisSupprime : la version que main porte à `p` est mise à l'abri
// sous `conflictName`, PUIS la suppression est appliquée.
//
// Deux commits et non un : le verrou du store est tenu pendant les deux, donc
// aucune écriture concurrente ne s'y glisse. Un lecteur qui tomberait entre les
// deux verrait la copie sans la suppression - un état transitoire qui converge au
// cycle suivant, contre une plomberie d'index nettement plus lourde.
func (s *Store) rangeEnCopiePuisSupprime(p, author, head, conflictName string) (WriteResult, error) {
	// Deux raisons TRÈS différentes de ne rien avoir à mettre à l'abri, et les
	// confondre sur une erreur de `Read` mettrait une panne d'I/O dans le même sac
	// qu'un no-op. On pose la question qui tranche - le chemin existe-t-il sur
	// main - avant de lire.
	if !s.existsOnMain(p) {
		return WriteResult{Head: head}, nil // suppression sans objet
	}
	contenu, err := s.Read(p, head)
	if err != nil {
		// Le chemin existe mais n'est pas un fichier : un autre membre en a fait un
		// DOSSIER. Sa bascule vaut déjà suppression du fichier, il n'y a aucune
		// version de fichier à préserver, et le delta la portera au client.
		return WriteResult{Head: head}, nil
	}
	if err := validatePath(conflictName); err != nil {
		return WriteResult{}, fmt.Errorf("nom de copie de conflit invalide : %w", err)
	}
	// INVARIANT, le même que pour WriteMerge : une copie n'efface jamais rien.
	freeName, err := s.freeConflictPath(conflictName)
	if err != nil {
		return WriteResult{}, err
	}
	// Message explicite : ces deux commits sont un seul geste, et rien d'autre ne
	// le dit. Dans l'historique, « update X (conflit).md » suivi de « delete X.md »
	// laisse celui qui arbitre reconstruire le lien tout seul.
	if _, err := s.writeLocked(freeName, contenu, author,
		fmt.Sprintf("copie de conflit %s : %s mis à l'abri avant suppression", freeName, p)); err != nil {
		return WriteResult{}, err
	}
	newHead, err := s.deleteLocked(p, author)
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Head: newHead, Conflict: true, ConflictPath: freeName}, nil
}

// mergeDelete scelle une suppression fusionnée proprement.
func (s *Store) mergeDelete(p, author, head, tree, clientCommit string) (WriteResult, error) {
	// Fusion sans effet : ne rien committer. Le no-op idempotent en tête de
	// `DeleteMerge` ne couvre que `baseOID == head` ; depuis qu'un marque-page
	// périmé amène ici, un DELETE répété sur un chemin déjà absent produisait un
	// commit de merge ET son parent éphémère, à chaque appel. Un cycle dont le
	// pull n'aboutit pas (erreur transport) renvoie exactement ce même DELETE au
	// cycle suivant : c'était un commit poubelle toutes les 15 secondes sur le
	// dépôt de production.
	if headTree, err := s.git(nil, "rev-parse", head+"^{tree}"); err == nil && headTree == tree {
		return WriteResult{Head: head, Merged: true}, nil
	}
	mergeCommit, err := s.git(commitEnv(author),
		"commit-tree", tree, "-p", head, "-p", clientCommit,
		"-m", fmt.Sprintf("merge delete %s", p))
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := s.git(nil, "update-ref", "refs/heads/main", mergeCommit, head); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Head: mergeCommit, Merged: true}, nil
}

// freeConflictPath renvoie `name` s'il est libre sur main, sinon la première
// variante « … (n).ext » libre. Appelé sous verrou : le test d'existence et
// l'écriture qui suit sont atomiques vis-à-vis des autres écritures.
func (s *Store) freeConflictPath(name string) (string, error) {
	if !s.existsOnMain(name) {
		return name, nil
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 2; n < 10000; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if !s.existsOnMain(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("impossible de trouver un nom de copie de conflit libre pour %q", name)
}

// existsOnMain indique si un chemin existe dans le commit courant de main.
// (cat-file -e : exit 0 = présent, non nul = absent ; le code de sortie porte
// le verdict, gitStatus ne renvoie err que si git n'a pas pu se lancer.)
func (s *Store) existsOnMain(p string) bool {
	_, code, err := s.gitStatus(nil, "cat-file", "-e", "refs/heads/main:"+p)
	return err == nil && code == 0
}

// Exists indique si un chemin - fichier ou dossier - existe sur main. Test
// exact (sensible à la casse), en O(1) : ne remplace pas un parcours quand la
// question porte sur des homonymes de casse différente.
func (s *Store) Exists(p string) bool { return s.existsOnMain(p) }

// deleteLocked : cœur de suppression directe (verrou déjà tenu).
func (s *Store) deleteLocked(p, author string) (string, error) {
	return s.commitIndex(author, fmt.Sprintf("delete %s", p), func(env []string) error {
		_, err := s.gitStdin("0 0000000000000000000000000000000000000000\t"+p+"\n",
			env, "update-index", "--index-info")
		return err
	})
}

// ephemeralWrite construit (sans toucher main) un commit sur `baseOID` où `p`
// porte `content`. C'est le côté « client » de la fusion 3-way.
func (s *Store) ephemeralWrite(p, content, author, baseOID string) (string, error) {
	if _, err := resolveRev(baseOID); err != nil {
		return "", fmt.Errorf("base_oid invalide : %q", baseOID)
	}
	blob, err := s.gitStdin(content, nil, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return s.ephemeralCommit(baseOID, author, "client edit", func(env []string) error {
		_, err := s.git(env, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+p)
		return err
	})
}

// ephemeralDelete construit (sans toucher main) un commit sur `baseOID` où `p`
// est supprimé.
func (s *Store) ephemeralDelete(p, author, baseOID string) (string, error) {
	if _, err := resolveRev(baseOID); err != nil {
		return "", fmt.Errorf("base_oid invalide : %q", baseOID)
	}
	return s.ephemeralCommit(baseOID, author, "client delete", func(env []string) error {
		_, err := s.gitStdin("0 0000000000000000000000000000000000000000\t"+p+"\n",
			env, "update-index", "--index-info")
		return err
	})
}

// ephemeralCommit : commit sur `parent`, index temporaire, sans mise à jour de
// main. `parent` vide = commit orphelin sur l'arbre vide : un client neuf
// (base_oid inconnu) pousse alors en add-or-conflict contre main, jamais en
// écrasement (pas de base commune → merge-tree traite chaque côté en ajout).
func (s *Store) ephemeralCommit(parent, author, message string, mutate func(env []string) error) (string, error) {
	idx, err := os.CreateTemp("", "vecu-index-*")
	if err != nil {
		return "", err
	}
	idx.Close()
	defer os.Remove(idx.Name())
	env := []string{"GIT_INDEX_FILE=" + idx.Name()}

	// read-tree initialise l'index (le fichier temp à 0 octet n'est pas un index
	// git valide) : arbre du parent, ou index vide pour un commit orphelin.
	readTreeArg := parent
	if parent == "" {
		readTreeArg = "--empty"
	}
	if _, err := s.git(env, "read-tree", readTreeArg); err != nil {
		return "", err
	}
	if err := mutate(env); err != nil {
		return "", err
	}
	tree, err := s.git(env, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = []string{"commit-tree", tree, "-p", parent, "-m", message}
	}
	return s.git(commitEnv(author), args...)
}

// mergeTree exécute une fusion 3-way entre `ours` et `theirs` (merge-base
// automatique). Verdict par exit code (jamais deviné depuis l'arbre) :
// 0 = propre (renvoie l'arbre fusionné), 1 = conflit.
// Source: https://git-scm.com/docs/git-merge-tree
func (s *Store) mergeTree(ours, theirs string) (tree string, conflict bool, err error) {
	// --allow-unrelated-histories : un client neuf (base_oid vide → commit
	// orphelin) n'a pas d'ancêtre commun avec main ; le merge doit alors
	// procéder en add-or-conflict au lieu de refuser. Sans effet si liés.
	stdout, code, err := s.gitStatus(nil, "merge-tree", "--write-tree",
		"--allow-unrelated-histories", ours, theirs)
	if err != nil {
		return "", false, err
	}
	switch code {
	case 0:
		// Première ligne = OID de l'arbre fusionné.
		tree = strings.SplitN(strings.TrimRight(stdout, "\n"), "\n", 2)[0]
		return tree, false, nil
	case 1:
		return "", true, nil
	default:
		return "", false, fmt.Errorf("merge-tree exit %d : %s", code, stdout)
	}
}

// Read renvoie le contenu exact du chemin à la révision donnée ("" = main).
func (s *Store) Read(p, rev string) (string, error) {
	if err := validatePath(p); err != nil {
		return "", err
	}
	rev, err := resolveRev(rev)
	if err != nil {
		return "", err
	}
	out, err := s.gitRaw("", nil, "cat-file", "blob", rev+":"+p)
	if err != nil {
		return "", fmt.Errorf("fichier inconnu : %s", p)
	}
	return out, nil
}

// ReadBatch lit PLUSIEURS blobs en UN SEUL appel à git.
//
// Pourquoi ça existe, et c'est une mesure et pas une intuition : `Read` lance
// un `git cat-file` par fichier. Mesuré le 31/08 sur un vault de 900 notes,
// c'est ~10 ms par fichier, donc **9,4 s** pour une recherche qui doit lire
// tout le périmètre visible - au-delà du délai d'attente de la plupart des
// clients, donc un outil inutilisable. Le même parcours par `--batch` tient en
// une fraction de seconde, parce qu'on paie un processus au lieu de neuf cents.
//
// Un chemin absent de la révision est simplement absent de la map rendue : ce
// n'est pas une erreur, un fichier peut disparaître entre le listage et la
// lecture.
//
// Le protocole de `--batch` : une requête par ligne sur stdin, et pour chaque
// requête connue un en-tête « <oid> <type> <taille> » suivi de <taille> octets
// puis d'un saut de ligne. Une requête inconnue rend « <requête> missing ».
// Source: https://git-scm.com/docs/git-cat-file#_batch_output
func (s *Store) ReadBatch(paths []string, rev string) (map[string]string, error) {
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	rev, err := resolveRev(rev)
	if err != nil {
		return nil, err
	}
	var requetes strings.Builder
	ordre := make([]string, 0, len(paths))
	for _, p := range paths {
		if err := validatePath(p); err != nil {
			continue // un chemin refusé n'est pas demandé, et n'est pas une erreur de lot
		}
		requetes.WriteString(rev + ":" + p + "\n")
		ordre = append(ordre, p)
	}
	if len(ordre) == 0 {
		return map[string]string{}, nil
	}
	brut, err := s.gitRaw(requetes.String(), nil, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(ordre))
	reste := brut
	for _, p := range ordre {
		saut := strings.IndexByte(reste, '\n')
		if saut < 0 {
			break // sortie tronquée : on rend ce qu'on a pu lire
		}
		entete := reste[:saut]
		reste = reste[saut+1:]

		// LA LIGNE D'ERREUR SE RECONNAÎT SUR LA REQUÊTE, jamais en comptant les
		// champs. Pour un objet absent, git réécrit la requête suivie de
		// « missing » - et une requête contient un CHEMIN, qui peut porter des
		// espaces. `strings.Fields` rendait alors trois champs sur
		// « main:a/mon fichier.md missing », `Atoi("missing")` échouait, et la
		// boucle s'arrêtait : tout ce qui suivait alphabétiquement disparaissait
		// du lot, sans erreur. Mesuré en revue - et atteignable par la simple
		// course entre le listage et la lecture.
		requete := rev + ":" + p
		if strings.HasPrefix(entete, requete+" ") {
			continue // « missing », « ambiguous » : aucun contenu ne suit
		}

		// « <oid> <type> <taille> ». Le TYPE est vérifié : `Read` demande
		// explicitement un blob, et sans ce contrôle un chemin de DOSSIER
		// rendrait l'objet tree brut, donc les noms de ses enfants. Inatteignable
		// aujourd'hui (`List -r` ne rend que des blobs), mais c'est une primitive
		// de fuite d'existence posée dans une API partagée.
		champs := strings.Fields(entete)
		if len(champs) != 3 {
			break // en-tête inattendu : on s'arrête plutôt que de découper au hasard
		}
		taille, err := strconv.Atoi(champs[2])
		if err != nil || taille < 0 || taille > len(reste) {
			break
		}
		if champs[1] == "blob" {
			out[p] = reste[:taille]
		}
		reste = reste[taille:]
		reste = strings.TrimPrefix(reste, "\n") // le saut de ligne qui suit le contenu
	}
	return out, nil
}

// List renvoie tous les chemins de fichiers à la révision donnée ("" = main).
func (s *Store) List(rev string) ([]string, error) {
	rev, err := resolveRev(rev)
	if err != nil {
		return nil, err
	}
	out, err := s.git(nil, "ls-tree", "-r", "--name-only", "-z", rev)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00"), nil
}

// Empreintes : chemin -> empreinte du blob, à la révision `rev`.
//
// C'EST LA PRIMITIVE DE DAR-198, et le choix de l'empreinte plutôt que d'un
// rang dans l'historique n'est pas une préférence de style. L'historique de
// production N'EST PAS LINEAIRE - mesuré le 01/09 sur `/data/brain.git` : 8669
// commits dont 3003 fusions, soit 35 %. Comparer des positions dans un
// `rev-list` donnerait des réponses fausses sur un tiers de l'historique, et
// fausses DU BON COTE : « arrivé » là où rien n'est arrivé. L'arbre à une
// révision, lui, dit ce que cette révision porte vraiment.
//
// Le coût est d'une invocation par révision interrogée - donc par poste, jamais
// par fichier.
//
// `ls-tree -r -z` sans `--name-only` rend « <mode> <type> <oid>\t<chemin> »
// terminé par NUL, ce qui est la seule forme sûre : un chemin peut contenir un
// espace, et l'échappement des chemins accentués dépend de `core.quotePath`.
// Source: https://git-scm.com/docs/git-ls-tree
func (s *Store) Empreintes(rev string) (map[string]string, error) {
	rev, err := resolveRev(rev)
	if err != nil {
		return nil, err
	}
	out, err := s.git(nil, "ls-tree", "-r", "-z", rev)
	if err != nil {
		return nil, err
	}
	empreintes := map[string]string{}
	if out == "" {
		return empreintes, nil
	}
	for _, ligne := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		tab := strings.IndexByte(ligne, '\t')
		if tab < 0 {
			continue
		}
		champs := strings.Fields(ligne[:tab])
		if len(champs) < 3 {
			continue
		}
		empreintes[ligne[tab+1:]] = champs[2]
	}
	return empreintes, nil
}

// Change décrit une modification d'un fichier entre deux révisions.
type Change struct {
	Path    string
	Deleted bool // true = supprimé (tombstone) ; false = ajouté ou modifié
}

// Diff renvoie le head courant de main et les changements depuis `since`.
// Le head est aussi le curseur à lire pour un contenu cohérent : lire les blobs
// à ce head (et non à main flottant) rend le sync insensible à une écriture
// concurrente et aligne le contenu sur le curseur renvoyé au client.
//
// Robustesse (modèle Dropbox) : tout `since` qui n'est pas un delta calculable
// - vide, malformé, absent du repo, ou ne pointant pas sur un commit - déclenche
// un resync complet du périmètre, jamais une erreur qui bloque le client.
func (s *Store) Diff(since string) (head string, changes []Change, err error) {
	head, err = s.Head()
	if err != nil {
		return "", nil, err
	}
	if since == head {
		return head, nil, nil // rien de neuf
	}
	// Un `since` non résoluble ou inconnu → resync complet plutôt qu'erreur.
	if _, err := resolveRev(since); err != nil {
		changes, err = s.fullState(head)
		return head, changes, err
	}
	if _, err := s.git(nil, "cat-file", "-e", since); err != nil {
		changes, err = s.fullState(head)
		return head, changes, err
	}
	// --name-status -z : statut + chemin, séparés par NUL.
	// --no-renames : le renommage v1 est modélisé delete+add ; sans ce flag git
	// fusionne un delete+add de contenu identique en un statut R (rename) à deux
	// chemins, ce qui casse le parsing par paires ET le modèle du produit.
	// Source: https://git-scm.com/docs/git-diff#Documentation/git-diff.txt---no-renames
	out, gitErr := s.git(nil, "diff", "--no-renames", "--name-status", "-z", since, head)
	if gitErr != nil {
		// `since` résoluble mais pas un commit (blob/tree forgé) → resync.
		changes, err = s.fullState(head)
		return head, changes, err
	}
	changes, err = parseNameStatus(out)
	return head, changes, err
}

// fullState : tous les fichiers du commit `rev` présentés comme des ajouts.
func (s *Store) fullState(rev string) ([]Change, error) {
	paths, err := s.List(rev)
	if err != nil {
		return nil, err
	}
	changes := make([]Change, len(paths))
	for i, p := range paths {
		changes[i] = Change{Path: p}
	}
	return changes, nil
}

// parseNameStatus décode la sortie -z de `git diff --name-status`.
// Format : status\x00path\x00status\x00path\x00...
func parseNameStatus(out string) ([]Change, error) {
	if out == "" {
		return nil, nil
	}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var changes []Change
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		if status == "" {
			return nil, fmt.Errorf("statut de diff vide pour %q", path)
		}
		changes = append(changes, Change{Path: path, Deleted: status[0] == 'D'})
	}
	return changes, nil
}

// Log renvoie l'historique d'un chemin (du plus récent au plus ancien).
func (s *Store) Log(p string) ([]Commit, error) {
	if err := validatePath(p); err != nil {
		return nil, err
	}
	// L'historique d'un fichier est la chaîne first-parent de main : la suite
	// linéaire des états serveur. Sans --first-parent, la simplification
	// d'historique de git suit le parent éphémère « client edit » des commits
	// de merge (TREESAME) et perd des écritures. --diff-merges=first-parent
	// fait porter --name-status des merges sur le delta vu de main.
	// %x1f = unit separator, %aI = date ISO 8601 stricte ; --name-status ajoute
	// « <statut>\t<chemin> » (D = suppression).
	// Source: https://git-scm.com/docs/git-log#Documentation/git-log.txt---first-parent
	// Source: https://git-scm.com/docs/git-log#Documentation/git-log.txt---diff-mergesltformatgt
	// Source: https://git-scm.com/docs/git-log#_pretty_formats
	out, err := s.git(nil, "log", "--first-parent", "--diff-merges=first-parent",
		"--format=%H%x1f%an%x1f%aI%x1f%s", "--name-status",
		"refs/heads/main", "--", p)
	if err != nil {
		return nil, err
	}
	// Un pathspec git couvre aussi les répertoires : `p` répertoire renverrait
	// les commits de tous ses enfants. Log est un historique de FICHIER : on ne
	// garde que les commits dont une ligne de statut porte exactement `p`
	// (sinon fuite d'existence de chemins hors périmètre via /log, et flag
	// Deleted écrasé par la dernière ligne d'un commit multi-fichiers).
	var commits []Commit
	var touche []bool
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "\x1f") {
			f := strings.SplitN(line, "\x1f", 4)
			if len(f) != 4 {
				return nil, fmt.Errorf("ligne de log inattendue : %q", line)
			}
			commits = append(commits, Commit{OID: f[0], Author: f[1], Date: f[2], Message: f[3]})
			touche = append(touche, false)
			continue
		}
		// Ligne de statut « <statut>\t<chemin>[\t<chemin>] » du commit courant.
		if len(commits) == 0 {
			return nil, fmt.Errorf("statut sans commit : %q", line)
		}
		f := strings.Split(line, "\t")
		for _, chemin := range f[1:] {
			if chemin == p {
				i := len(commits) - 1
				touche[i] = true
				commits[i].Deleted = f[0][0] == 'D'
			}
		}
	}
	out2 := make([]Commit, 0, len(commits))
	for i, c := range commits {
		if touche[i] {
			out2 = append(out2, c)
		}
	}
	return out2, nil
}

// validatePath rejette les chemins hors du dépôt, réservés, ou porteurs de
// caractères de contrôle. Les octets de contrôle casseraient le protocole
// texte de git plumbing (index-info délimité par lignes, séparateurs de log) :
// on les refuse en entrée plutôt que de les neutraliser à chaque appel.
// ValidPath expose la validation de chemin aux handlers HTTP : une entrée
// impossible (vide, contrôle, échappement racine) doit être classée 404
// « introuvable » en amont, pas remonter en erreur interne distinguable.
func ValidPath(p string) error { return validatePath(p) }

func validatePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") {
		return fmt.Errorf("chemin invalide : %q", p)
	}
	if hasControlChar(p) {
		return fmt.Errorf("chemin invalide (caractère de contrôle) : %q", p)
	}
	clean := path.Clean(p)
	if clean != p || clean == "." || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("chemin invalide : %q", p)
	}
	return nil
}

// validateAuthor : l'auteur devient un champ de commit puis un champ de log
// séparé par \x1f ; tout caractère de contrôle décalerait le parsing.
func validateAuthor(author string) error {
	if author == "" {
		return fmt.Errorf("auteur vide")
	}
	if hasControlChar(author) {
		return fmt.Errorf("auteur invalide (caractère de contrôle) : %q", author)
	}
	return nil
}

func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// resolveRev borne les révisions acceptées : "" (= main) ou un OID hexadécimal
// (SHA-1/SHA-256). On refuse toute autre gitrevision (refs, reflog, :index:)
// pour ne jamais exposer d'objets ou d'historique hors du modèle Vécu.
func resolveRev(rev string) (string, error) {
	if rev == "" {
		return "refs/heads/main", nil
	}
	if !isHex(rev) || len(rev) < 7 || len(rev) > 64 {
		return "", fmt.Errorf("révision invalide : %q", rev)
	}
	return rev, nil
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// commitEnv fixe l'identité auteur/committeur d'un commit.
func commitEnv(author string) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + author,
		"GIT_AUTHOR_EMAIL=" + author + "@vecu.local",
		"GIT_COMMITTER_NAME=" + author,
		"GIT_COMMITTER_EMAIL=" + author + "@vecu.local",
	}
}

// git : exécute une commande git et renvoie stdout sans le \n final
// (pratique pour les OIDs). Pour du contenu exact, utiliser gitRaw.
func (s *Store) git(env []string, args ...string) (string, error) {
	out, err := s.gitRaw("", env, args...)
	return strings.TrimRight(out, "\n"), err
}

func (s *Store) gitStdin(stdin string, env []string, args ...string) (string, error) {
	out, err := s.gitRaw(stdin, env, args...)
	return strings.TrimRight(out, "\n"), err
}

// gitRaw : stdout brut, stderr séparé (jamais mélangé au contenu).
func (s *Store) gitRaw(stdin string, env []string, args ...string) (string, error) {
	cmd := s.gitCmd(stdin, env, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s : %v : %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// gitStatus : comme gitRaw mais renvoie le code de sortie plutôt qu'une erreur
// sur exit non nul. `err` n'est renvoyé que si la commande n'a pas pu être
// lancée. Utilisé par merge-tree, dont exit 1 = conflit (cas normal).
func (s *Store) gitStatus(env []string, args ...string) (stdout string, code int, err error) {
	cmd := s.gitCmd("", env, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()
	if runErr == nil {
		return out.String(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return out.String(), ee.ExitCode(), nil
	}
	return "", -1, fmt.Errorf("git %s : %v : %s", strings.Join(args, " "), runErr, errb.String())
}

// gitCmd construit une commande git avec l'environnement durci partagé.
func (s *Store) gitCmd(stdin string, env []string, args ...string) *exec.Cmd {
	// Un compteur atomique sur un lancement de process : le coût est nul devant
	// le fork/exec qui suit. Voir le champ `appelsGit`.
	s.appelsGit.Add(1)
	// core.quotePath=off : par défaut git CITE les chemins non-ASCII dans ses
	// sorties texte (ls-tree, diff, log --name-status) : « notes/idées.md »
	// devient « "notes/id\303\251es.md" », ce qui casse List/Diff/Log pour
	// tout nom accentué - le cas nominal d'un vault français. Off = octets
	// bruts, nos chemins sont déjà validés (pas de caractères de contrôle).
	// Source: https://git-scm.com/docs/git-config#Documentation/git-config.txt-corequotePath
	cmd := exec.Command("git", append([]string{"-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = s.dir
	// GIT_DIR explicite : un GIT_DIR ambiant ne peut pas détourner cmd.Dir.
	// GIT_CONFIG_NOSYSTEM/GIT_CONFIG_GLOBAL : ignore les config hôte (autocrlf,
	// hooks, alias) qui altéreraient le contenu ou le comportement.
	base := []string{
		"GIT_DIR=" + s.dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	cmd.Env = append(append(os.Environ(), base...), env...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd
}
