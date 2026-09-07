package web

// La mesure de l'écran du mainteneur contre le contrat de latence (DAR-195).
//
// CE QUE CE BANC CHERCHE : le coût de l'écran croît-il avec le nombre de
// POSTES, et de combien. C'est la seule inconnue du lot - la déduction fait une
// invocation git par poste, et rien ne disait ce que ça vaut à l'échelle réelle
// du vault (1186 fichiers).
//
// `go test ./server/web/ -run xxx -bench BenchmarkLatenceEtat -benchtime 20x`

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/colindargent/vecu/server/db"
)

// posteQuiParle : inscrit n postes ayant tous rapporté la tête courante.
// Retourne les jetons pour que l'appelant puisse les révoquer.
func posteQuiParle(b *testing.B, bc *bancAdmin, n int) []string {
	b.Helper()
	tete, err := bc.store.Head()
	if err != nil {
		b.Fatal(err)
	}
	quand := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	jetons := make([]string, 0, n)
	for i := range n {
		jeton, err := bc.db.IssueToken(bc.cible.ID, fmt.Sprintf("banc-%02d", i))
		if err != nil {
			b.Fatal(err)
		}
		jetons = append(jetons, jeton)
		// Une exception par poste : le cas réaliste, et il force le passage par
		// l'union des chemins plutôt que par la seule liste du serveur.
		exc := db.ExceptionsPoste{
			Laisses: []db.LaissePoste{{
				Chemin: fmt.Sprintf("shared/process/laisse-%02d.md", i),
				Raison: "un dossier porte déjà ce nom sur ce poste",
			}},
		}
		if err := bc.db.EnregistreEtatPoste(hashJeton(jeton), tete, "2026-09-01T11:00:00Z", exc, quand); err != nil {
			b.Fatal(err)
		}
	}
	return jetons
}

func nettoiePostes(b *testing.B, bc *bancAdmin, jetons []string) {
	b.Helper()
	for _, j := range jetons {
		if err := bc.db.RevokeToken(j); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLatenceEtat1Poste / 3 / 8 : la courbe.
//
// Trois points plutôt qu'un seul, parce que la question n'est pas « combien ça
// coûte » mais « comment ça croît ». Un écran à 40 ms sur un poste qui passerait
// à 400 ms sur dix serait un défaut qu'un chiffre unique cacherait.
func BenchmarkLatenceEtat1Poste(b *testing.B)  { mesureEtat(b, 1) }
func BenchmarkLatenceEtat3Postes(b *testing.B) { mesureEtat(b, 3) }
func BenchmarkLatenceEtat8Postes(b *testing.B) { mesureEtat(b, 8) }

func mesureEtat(b *testing.B, n int) {
	bc := monteBanc(b)
	jetons := posteQuiParle(b, bc, n)
	defer nettoiePostes(b, bc, jetons)
	b.ResetTimer()
	for b.Loop() {
		bc.voit(b, "/admin/etat", http.StatusOK)
	}
}
