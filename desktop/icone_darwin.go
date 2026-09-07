package main

import _ "embed"

// L'icône de la barre de menus, en PNG.
//
// macOS attend une image « template » : monochrome à canal alpha, que le système
// teinte lui-même selon le thème clair ou sombre. Le PNG convient, et les deux
// arguments de `SetTemplateIcon` reçoivent la même image ici.

//go:embed icon.png
var iconTemplate []byte

// iconeSysteme : la même image. Sur macOS, seul le premier argument compte.
var iconeSysteme = iconTemplate
