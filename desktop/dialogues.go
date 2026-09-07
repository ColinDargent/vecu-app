package main

import "errors"

// dialogues.go : ce que les dialogues ont en commun, quel que soit le système.
//
// L'implémentation vit dans dossier_darwin.go (osascript) et dossier_windows.go
// (PowerShell). Les APPELANTS, eux, ne savent rien de tout ça : ils voient sept
// fonctions et une sentinelle, identiques partout. C'est ce qui permet à
// accueil.go, partage.go, rejoindre.go et retrait.go de ne pas contenir une
// seule ligne propre à un système.

// errAnnule : la personne a fermé le dialogue. Ce n'est pas une panne, et
// l'appelant ne doit rien afficher. Une sentinelle plutôt qu'un booléen de
// retour : le cas nominal (un chemin) et les deux cas d'échec se lisent alors
// avec le même `if err != nil` que partout ailleurs.
var errAnnule = errors.New("choix annulé")
