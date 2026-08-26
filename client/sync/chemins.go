package sync

// chemins.go : les deux sens de conversion entre un chemin du dépôt et un
// chemin sur ce poste. Rien d'autre n'a le droit de joindre `e.dir` à quoi que
// ce soit.
//
// Pourquoi ce fichier existe alors que la jointure tenait en une ligne : DT2 du
// CDC technique donne à chaque espace SON chemin local, choisi poste par poste.
// Tant que la conversion est écrite à trente-deux endroits, ce changement est
// diffus et se relit mal ; centralisée, il devient mécanique.
//
// Et c'est `engine.go` qui est en jeu, le fichier le plus délicat du produit -
// il porte l'historique de trois correctifs de perte de données. Cette étape ne
// change donc AUCUN comportement, et sa preuve est que la suite de tests reste
// verte SANS ÊTRE MODIFIÉE. Un test qu'il faut retoucher ici est le signe que la
// conversion n'était pas mécanique quelque part.

import (
	"path"
	"path/filepath"
	"strings"
)

// abs : chemin du dépôt (séparé par des slashs) -> chemin absolu sur ce poste.
//
// N'ajoute AUCUNE garde, volontairement. `cheminSur` reste la responsabilité de
// l'appelant : la déplacer ici la rendrait invisible sur les sites qui doivent
// l'exercer, et un appelant qui l'oublie doit rester repérable à la lecture.
// INVARIANT PARTAGÉ AVEC `sansLien` : les deux font le même découpage, avec le
// même repli. La garde doit marcher exactement le chemin que l'écriture prendra
// - si l'une des deux change de règle sans l'autre, la garde inspecte un chemin
// et l'écriture en emprunte un autre, ce qui est précisément le défaut corrigé
// le 21/08. Toucher ici, c'est toucher là-bas.
func (e *Engine) abs(rel string) string {
	espace, reste, _ := strings.Cut(rel, "/")
	if racine, montee := e.montages[espace]; montee {
		if reste == "" {
			return racine
		}
		return filepath.Join(racine, filepath.FromSlash(reste))
	}
	return filepath.Join(e.dir, filepath.FromSlash(rel))
}

// rel : chemin absolu sur ce poste -> chemin du dépôt.
//
// Rend toujours la forme à slashs, qui est celle de l'état et du serveur.
// Convertir d'un côté et oublier de l'autre est le genre d'asymétrie qui ne se
// voit pas sur macOS et casse ailleurs.
//
// Les montages explicites sont examinés AVANT la racine, et du plus profond au
// moins profond. Sans cet ordre, un espace monté dans un sous-dossier d'un autre
// se verrait attribuer au mauvais - le plus court gagnerait, et le chemin
// porterait le nom d'un espace qui ne le contient pas.
func (e *Engine) rel(abs string) (string, error) {
	if nom, reste, ok := e.souLeMontage(abs); ok {
		return path.Join(nom, reste), nil
	}
	r, err := filepath.Rel(e.dir, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(r), nil
}

// souLeMontage : ce chemin absolu tombe-t-il sous un espace explicitement monté ?
//
// Comparaison LEXICALE, sans toucher au disque : `rel` est appelée depuis la
// marche de `scanLocal`, sur chaque entrée. Y résoudre les liens ferait un
// `Lstat` par fichier et par cycle.
func (e *Engine) souLeMontage(abs string) (nom, reste string, ok bool) {
	meilleur := -1
	for esp, racine := range e.montages {
		r, err := filepath.Rel(racine, abs)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			continue // hors de ce montage
		}
		// Le montage le plus PROFOND gagne : un espace imbriqué dans un autre
		// appartient au plus proche, pas au premier venu de la map (dont l'ordre
		// d'itération est aléatoire en Go - s'y fier donnerait un classement qui
		// change d'un cycle à l'autre).
		if len(racine) > meilleur {
			meilleur, nom, ok = len(racine), esp, true
			if r == "." {
				reste = ""
			} else {
				reste = filepath.ToSlash(r)
			}
		}
	}
	return nom, reste, ok
}

// racineEspace : le dossier local d'un espace monté.
//
// La table d'abord, `racine + nom` en repli. Ce repli est ce qui rend l'arrivée
// de la table transparente : une installation existante n'a aucune entrée et ne
// change pas de comportement.
func (e *Engine) racineEspace(nom string) string {
	return RacineEspace(e.dir, e.montages, nom)
}

// RacineEspace : la même résolution, sans moteur. `vecu status` lit l'état sans
// en construire un, et il doit pourtant trouver les mêmes dossiers - dupliquer
// la règle ferait diverger les deux le jour où un espace vivra hors de la
// racine, c'est-à-dire précisément le jour où ça compte.
func RacineEspace(dir string, montages map[string]string, nom string) string {
	if racine, montee := montages[nom]; montee {
		return racine
	}
	return filepath.Join(dir, nom)
}
