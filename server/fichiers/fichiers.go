package fichiers

// fichiers.go : la déduction de DAR-198.
//
// « Est-ce que ce fichier est bien arrivé chez tout le monde » se répond en
// comparant, pour chaque poste, l'arbre du serveur et l'arbre à la tête de ce
// poste - PUIS en soustrayant ce que le poste a déclaré en exception.
//
// AUCUN GIT, AUCUNE BASE ICI. Ce paquet prend des cartes déjà construites et
// rend des états. C'est ce qui permet de le mettre sous mutation sans monter un
// dépôt : la logique qui décide de la couleur d'une case est exactement ce
// qu'il faut pouvoir casser volontairement pour vérifier que le banc tombe.
//
// DEUX SENS, ET C'EST L'ERREUR QUE LA PREMIERE SPEC A FAITE. Une laisse va du
// serveur vers le poste (le serveur l'a, le poste ne l'a pas). Une copie de
// conflit locale va dans l'AUTRE sens : elle existe sur le poste et le serveur
// ne l'a jamais eue. La liste affichée est donc l'UNION des chemins du serveur
// et des chemins d'exception, pas la liste du serveur.

import "sort"

// Etat : ce qu'une case dit d'un chemin sur un poste.
type Etat string

const (
	// SansNouvelles : ce poste n'a jamais parlé. LE PLUS IMPORTANT DES SEPT.
	// Un poste muet et un poste sain ne doivent JAMAIS rendre la même case :
	// afficher zéro exception pour un poste silencieux se lit « tout est
	// arrivé » alors que la lecture juste est « je n'en sais rien ».
	SansNouvelles Etat = "sans-nouvelles"
	// AJour : le poste porte la même empreinte que le serveur.
	AJour Etat = "a-jour"
	// EnRetard : le poste porte une version antérieure.
	EnRetard Etat = "en-retard"
	// PasArrive : le serveur l'a, l'arbre du poste ne le porte pas.
	PasArrive Etat = "pas-arrive"
	// Laisse : le poste a explicitement dit pourquoi il ne l'a pas.
	Laisse Etat = "laisse"
	// ArbitrageEnAttente : une copie de conflit locale. Elle ne se synchronise
	// JAMAIS par construction, donc rien d'autre que cet écran ne peut la
	// montrer à distance.
	ArbitrageEnAttente Etat = "arbitrage-en-attente"
)

// Exceptions : les trois listes qu'un poste déclare. Miroir de
// `db.ExceptionsPoste`, en types nus pour que ce paquet ne dépende de rien.
// LES BINAIRES N'Y SONT PAS (Colin, 01/09). Un binaire écarté n'existe que sur
// le poste, donc le nommer ici apprendrait au serveur - et au mainteneur d'un
// client - le nom d'un fichier personnel posé dans un espace partagé. Les deux
// listes qui restent portent des chemins que le serveur porte déjà.
type Exceptions struct {
	Laisses        map[string]string // chemin -> motif
	ConflitsLocaux map[string]bool
}

// Poste : ce qu'il faut savoir d'un poste pour remplir sa colonne.
type Poste struct {
	Libelle string
	// AParle : false = jamais eu de nouvelles. Décide AVANT tout le reste.
	AParle bool
	// RecuLe : l'horloge DU SERVEUR au dernier contact. Celle du poste ne fait
	// pas foi - un poste qui dérive rendrait la colonne absurde.
	RecuLe string
	// Empreintes : l'arbre à la tête de ce poste. Vide et AParle=true est un
	// état légitime : un poste tout neuf n'a encore rien reçu.
	Empreintes map[string]string
	Exceptions Exceptions
}

// Case : l'état d'un chemin sur un poste, tel qu'il s'affiche.
type Case struct {
	Etat  Etat
	Motif string // rempli pour Laisse seulement
}

// EtatDuChemin : la règle, premier cas qui correspond.
//
// L'ORDRE EST LE CONTRAT. Les exceptions passent avant la comparaison d'arbres
// parce que LA TETE MENT PAR CONCEPTION : le curseur de lecture doit avancer
// même quand une écriture a été refusée, sinon le même delta redescend
// indéfiniment pour tous les espaces à cause d'un seul chemin (voir `Head` dans
// `client/sync/config.go`). Un arbre à jour ne prouve donc pas qu'un fichier
// est sur le disque, et les exceptions sont exactement ce qui le dit.
func EtatDuChemin(chemin string, serveur map[string]string, p Poste) Case {
	if !p.AParle {
		return Case{Etat: SansNouvelles}
	}
	if motif, ok := p.Exceptions.Laisses[chemin]; ok {
		return Case{Etat: Laisse, Motif: motif}
	}
	if p.Exceptions.ConflitsLocaux[chemin] {
		return Case{Etat: ArbitrageEnAttente}
	}
	chezLePoste, present := p.Empreintes[chemin]
	if !present {
		return Case{Etat: PasArrive}
	}
	if chezLePoste != serveur[chemin] {
		return Case{Etat: EnRetard}
	}
	return Case{Etat: AJour}
}

// Chemins : l'UNION des chemins du serveur et des chemins d'exception de tous
// les postes, triée.
//
// Partir de la seule liste du serveur cacherait précisément ce que le
// mainteneur cherche : un binaire jamais monté et une copie de conflit locale
// n'existent QUE sur un poste. Ce sont les deux cas où l'écran apprend quelque
// chose qu'aucune autre surface ne montre.
func Chemins(serveur map[string]string, postes []Poste) []string {
	vus := make(map[string]bool, len(serveur))
	for c := range serveur {
		vus[c] = true
	}
	for _, p := range postes {
		for c := range p.Exceptions.Laisses {
			vus[c] = true
		}
		for c := range p.Exceptions.ConflitsLocaux {
			vus[c] = true
		}
	}
	out := make([]string, 0, len(vus))
	for c := range vus {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Ligne : un chemin et son état sur chaque poste.
type Ligne struct {
	Chemin string
	// SurLeServeur : false = ce chemin n'existe que sur un poste (binaire ou
	// copie de conflit). La colonne du serveur doit le dire, sinon la ligne
	// ressemble à un fichier que tout le monde aurait perdu.
	SurLeServeur bool
	Cases        []Case // même ordre que les postes passés
}

// Tableau : la vue complète. Une passe sur l'union, une case par poste.
func Tableau(serveur map[string]string, postes []Poste) []Ligne {
	chemins := Chemins(serveur, postes)
	out := make([]Ligne, 0, len(chemins))
	for _, c := range chemins {
		_, surServeur := serveur[c]
		l := Ligne{Chemin: c, SurLeServeur: surServeur, Cases: make([]Case, len(postes))}
		for i := range postes {
			l.Cases[i] = EtatDuChemin(c, serveur, postes[i])
		}
		out = append(out, l)
	}
	return out
}

// ToutArrive : ce chemin est-il arrivé partout ?
//
// LA REPONSE EST A TROIS VALEURS, PAS DEUX, et c'est tout le sujet du lot. Un
// « non » et un « je ne sais pas » ne se confondent pas : le premier appelle une
// action, le second appelle un poste à rallumer.
func ToutArrive(l Ligne) (oui bool, certain bool) {
	oui, certain = true, true
	for _, c := range l.Cases {
		switch c.Etat {
		case SansNouvelles:
			certain = false
		case AJour, ArbitrageEnAttente:
			// ArbitrageEnAttente n'est pas un manque : le fichier EST sur ce
			// poste, sous un autre nom. C'est le serveur qui ne l'a pas.
		default:
			oui = false
		}
	}
	return oui, certain
}
