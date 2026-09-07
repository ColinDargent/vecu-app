#!/usr/bin/env bash
# Construit vecu-app.exe pour Windows (install manuelle d'un poste).
#
# Le pendant de build-app.sh, et il est bien plus court - pour une raison de
# fond : Windows n'a pas de bundle. Là où macOS demande une arborescence
# `.app/Contents/{MacOS,Resources}`, un Info.plist et un sceau ad-hoc, ici la
# livraison EST le binaire. Il n'y a donc rien à emballer et rien à sceller au
# niveau du système ; l'intégrité reste portée par le manifeste signé ed25519,
# exactement comme sur macOS.
#
#   usage : ./desktop/build-exe.sh <dossier-de-sortie> <version>
#
# CGO_ENABLED=0 : `fyne.io/systray` a besoin de cgo sur macOS (il appelle Cocoa)
# mais pas sur Windows, où il passe par des appels système. Ça rend la
# construction possible depuis un Mac, sans chaîne mingw.
#
# -H windowsgui : sans ce drapeau, Windows considère le binaire comme une
# application console et lui attache une fenêtre noire au lancement. C'est
# l'équivalent du LSUIElement=1 de l'Info.plist côté macOS.
# Source: https://pkg.go.dev/cmd/link
set -euo pipefail

OUT="${1:-dist}"
VERSION="${2:-}"
if [ -z "$VERSION" ]; then
	echo "usage : $0 <dossier-de-sortie> <version>" >&2
	exit 2
fi

cd "$(dirname "$0")/.."
mkdir -p "$OUT"
EXE="$OUT/vecu-app.exe"

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	go build -ldflags "-X main.version=$VERSION -H windowsgui" -o "$EXE" ./desktop

echo "construit : $EXE ($VERSION)"
echo
echo "Ce binaire n'est PAS signé Authenticode : au premier lancement, Windows"
echo "affichera « Windows a protégé votre ordinateur » (SmartScreen). Passer par"
echo "« Informations complémentaires » puis « Exécuter quand même ». Faire"
echo "disparaître cet écran demande un certificat, pas une ligne de code."
