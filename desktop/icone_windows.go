package main

import _ "embed"

// icone_windows.go : l'icône de la zone de notification, en ICO.
//
// LE FORMAT N'EST PAS UN DÉTAIL, ET C'EST CE QUI RENDAIT L'APP MUETTE.
//
// `systray.SetTemplateIcon(a, b)` sur Windows ignore le premier argument et
// appelle `SetIcon(b)`. Celui-ci écrit les octets dans un fichier temporaire,
// puis les charge avec `LoadImageW(..., IMAGE_ICON, LR_LOADFROMFILE)`. Cet appel
// n'accepte QUE le format ICO : donné un PNG, il rend 0, l'icône n'est jamais
// créée, et rien n'apparaît dans la zone de notification.
//
// Rien ne le signale. L'application démarre, `onReady` s'exécute, le moteur
// synchronise - et la personne n'a aucune icône, donc aucun moyen d'ouvrir le
// menu. Sur un produit dont l'icône EST l'interface, c'est une panne totale qui
// se présente comme un silence. Mesuré le 06/09 sur un runner Windows : rien
// dans la zone de notification alors que le processus tournait.
//
// L'ICO embarque le même dessin : une entrée unique compressée en PNG, ce que
// Windows accepte depuis Vista. Le fichier est donc généré depuis icon.png et
// reste un seul dessin à maintenir.
// Source: https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-loadimagew

//go:embed icon.png
var iconTemplate []byte

//go:embed icon.ico
var iconeSysteme []byte
