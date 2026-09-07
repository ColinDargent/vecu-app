// Verification du sceau AVEC LE CODE DU PRODUIT : `appupdate.Open` pour la
// signature, `Asset.Check` pour les empreintes. Jamais une comparaison maison -
// c'est une verification maison qui a rendu un faux negatif le 20/08, en
// comparant du base64 au lieu des octets decodes.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/colindargent/vecu/appupdate"
)

// La MEME cle publique que celle compilee dans desktop/update.go : si elles
// divergent, la verification ne prouve rien sur ce que les postes accepteront.
const clePubliqueReleaseB64 = "YE7KIjYWI7MkVZD7a7546llZF26UQoCF7mPpJ8P8sWA="

func main() {
	pub, err := appupdate.DecodePublicKey(clePubliqueReleaseB64)
	if err != nil {
		fmt.Println("CLE ILLISIBLE :", err)
		os.Exit(1)
	}
	brut, err := os.ReadFile("appdist/manifest.json")
	if err != nil {
		fmt.Println("MANIFESTE ILLISIBLE :", err)
		os.Exit(1)
	}
	var sm appupdate.SignedManifest
	if err := json.Unmarshal(brut, &sm); err != nil {
		fmt.Println("MANIFESTE INVALIDE :", err)
		os.Exit(1)
	}
	m, err := appupdate.Open(pub, sm)
	if err != nil {
		fmt.Println("SIGNATURE INVALIDE :", err)
		os.Exit(1)
	}
	fmt.Println("signature valide, version scellee :", m.Version)

	echecs := 0
	// LES CIBLES SE LISENT DANS LE MANIFESTE, elles ne sont plus écrites ici.
	//
	// C'était une liste en dur (« darwin-arm64, darwin-amd64 »), et le port
	// Windows a montré ce que ça coûte : y ajouter « windows-amd64 » aurait fait
	// échouer la vérification de toutes les releases antérieures, et l'oublier
	// aurait laissé la cible Windows passer sans être relue. Une liste qui
	// décrit ce qu'on croit avoir publié ne vérifie rien - elle vérifie sa
	// propre mise à jour. Le manifeste, lui, dit ce qui a réellement été scellé.
	cibles := make([]string, 0, len(m.Assets))
	for cible := range m.Assets {
		cibles = append(cibles, cible)
	}
	sort.Strings(cibles)
	if len(cibles) == 0 {
		fmt.Println("MANIFESTE VIDE : aucune cible scellée")
		os.Exit(1)
	}

	for _, cible := range cibles {
		// Windows n'a pas de bundle : le binaire nu est la livraison. Exiger un
		// bundle ferait échouer une release parfaitement valide.
		lots := []struct {
			nom     string
			get     func(string) (appupdate.Asset, error)
			fichier string
		}{
			{"binaire", m.Asset, "appdist/vecu-app-" + cible},
		}
		if !strings.HasPrefix(cible, "windows-") {
			lots = append(lots, struct {
				nom     string
				get     func(string) (appupdate.Asset, error)
				fichier string
			}{"bundle", m.Bundle, "appdist/vecu-app-" + cible + ".tgz"})
		}
		for _, lot := range lots {
			a, err := lot.get(cible)
			if err != nil {
				fmt.Printf("  %-13s %-8s ABSENT DU MANIFESTE : %v\n", cible, lot.nom, err)
				echecs++
				continue
			}
			bin, err := os.ReadFile(lot.fichier)
			if err != nil {
				fmt.Printf("  %-13s %-8s FICHIER ABSENT : %v\n", cible, lot.nom, err)
				echecs++
				continue
			}
			if err := a.Check(bin); err != nil {
				fmt.Printf("  %-13s %-8s EMPREINTE FAUSSE : %v\n", cible, lot.nom, err)
				echecs++
				continue
			}
			fmt.Printf("  %-13s %-8s OK (%d octets, %s)\n", cible, lot.nom, len(bin), a.SHA256[:12])
		}
	}
	if echecs > 0 {
		fmt.Printf("\n%d CONTROLE(S) EN ECHEC : ne pas publier\n", echecs)
		os.Exit(1)
	}
	fmt.Println("\nles 4 empreintes du manifeste correspondent aux fichiers construits")
}
