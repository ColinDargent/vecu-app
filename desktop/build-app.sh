#!/usr/bin/env bash
# Emballe le binaire desktop en bundle macOS « Vécu.app ».
#
# Un bundle .app minimal : Info.plist (LSUIElement=1 → agent menu-bar, pas
# d'icône Dock) + le binaire Go dans Contents/MacOS. systray exige CGO_ENABLED=1
# et un vrai bundle pour présenter l'UI en barre de menus.
#
# Usage : desktop/build-app.sh [dossier-de-sortie] [version] [cible]
#         (défauts : dist/, dev, l'architecture de l'hôte)
#
# La cible (« darwin-arm64 », « darwin-amd64 ») sert à `vecu-release`, qui
# construit les deux bundles d'une release depuis la même machine.
#
# La version (2e argument, ex. v0.3.0) est injectée par ldflags. Sans elle le
# binaire est « dev » et ne s'auto-update jamais — normal pour un build local,
# mais une vraie release doit passer une version (via `make build-app VERSION=…`).
set -euo pipefail

cd "$(dirname "$0")/.."
export PATH=/opt/homebrew/bin:$PATH

OUT="${1:-dist}"
VERSION="${2:-dev}"
CIBLE="${3:-}"
APP="$OUT/Vécu.app"
BIN="$APP/Contents/MacOS/vecu-app"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

# Cible de compilation. Vide = l'hote, ce qui est le cas d'un build local.
# `vecu-release` passe « darwin-arm64 » ou « darwin-amd64 » pour construire les
# deux bundles d'une release depuis la meme machine. Le reglage de clang
# reproduit celui de cmd/vecu-release : la cross-compilation C sur macOS exige
# de forcer l'architecture, le SDK etant universel.
if [ -n "$CIBLE" ]; then
	GOOS="${CIBLE%%-*}"
	GOARCH="${CIBLE##*-}"
	export GOOS GOARCH
	if [ "$GOOS" = "darwin" ]; then
		case "$GOARCH" in
			amd64) export CC="clang -arch x86_64" ;;
			arm64) export CC="clang -arch arm64" ;;
		esac
	fi
fi

echo "→ build (CGO_ENABLED=1, version=$VERSION${CIBLE:+, cible=$CIBLE})"
CGO_ENABLED=1 go build -ldflags "-X main.version=$VERSION" -o "$BIN" ./desktop

# CFBundleShortVersionString veut 1 a 3 entiers separes par des points. Nos
# versions s'ecrivent « v0.9.0 » et un build local s'appelle « dev » : ni l'un
# ni l'autre ne passe tel quel. On retire le « v », et tout ce qui ne ressemble
# pas a X.Y.Z retombe sur 0.0.0 plutot que d'ecrire une valeur invalide.
COURTE="${VERSION#v}"
case "$COURTE" in
	# D'abord le rejet : tout ce qui contient autre chose qu'un chiffre ou un
	# point degage. Sans cette ligne, « v0.9.0-rc1 » passerait le motif suivant
	# (le dernier [0-9]* absorbe « 0-rc1 ») et ecrirait une version invalide.
	*[!0-9.]*) COURTE="0.0.0" ;;
	[0-9]*.[0-9]*.[0-9]*) ;;
	*) COURTE="0.0.0" ;;
esac

# NON VERIFIE : les trois cles NS*FolderUsageDescription. La page Apple ne rend
# pas en fetch et le SDK des Command Line Tools n'en porte aucune definition
# (verifie le 21/08, 0 occurrence). Les noms viennent de la memoire du modele.
# Ce qui les validera est l'increment 7 de la spec, le compte macOS neuf : si
# l'invite systeme sort avec le texte ci-dessous, les cles sont bonnes ; si elle
# sort sans texte, elles sont mal nommees. Le parcours A rend le cas courant,
# puisqu'il fait choisir un dossier dans Documents ou sur le Bureau.
#
# Heredoc NON quote a partir d'ici : $COURTE doit s'interpoler. Le plist ne
# contient ni « $ » ni backtick, donc rien d'autre ne s'interpole par accident.
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Vécu</string>
	<key>CFBundleDisplayName</key>
	<string>Vécu</string>
	<key>CFBundleIdentifier</key>
	<string>fr.vecu.app</string>
	<key>CFBundleExecutable</key>
	<string>vecu-app</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$COURTE</string>
	<key>CFBundleVersion</key>
	<string>$COURTE</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSDocumentsFolderUsageDescription</key>
	<string>Vécu lit et écrit les fichiers des dossiers que vous choisissez de synchroniser.</string>
	<key>NSDesktopFolderUsageDescription</key>
	<string>Vécu lit et écrit les fichiers des dossiers que vous choisissez de synchroniser.</string>
	<key>NSDownloadsFolderUsageDescription</key>
	<string>Vécu lit et écrit les fichiers des dossiers que vous choisissez de synchroniser.</string>
</dict>
</plist>
PLIST

# Le sceau ad-hoc, APRES l'Info.plist et jamais avant : le sceau couvre le
# plist, donc le reecrire ensuite casserait la signature a la seconde. C'est
# aussi de la que codesign tire l'identifiant.
#
# Sans sceau, le bundle porte la signature que le linker Go pose sur le Mach-O
# plat (`Identifier=a.out`, `Sealed Resources=none`), donc une signature
# INCOHERENTE : `codesign --verify --strict` echoue avec « code has no
# resources but signature indicates they must be present ».
#
# Forme minimale, mesuree le 21/08 sur trois variantes : `--identifier` est
# inutile (codesign reprend le CFBundleIdentifier, soit fr.vecu.app) et
# `--deep` aussi (aucun code imbrique ici, et Apple le deconseille pour la
# signature). Le `rm -rf "$APP"` en tete garantit qu'aucun binaire residuel ne
# se fait sceller comme ressource.
#
# CE QUE CA NE FAIT PAS, et il faut le dire : l'exigence designee reste un
# cdhash nu (mesure du 21/08), donc aucune identite de code ne survit a un
# changement de version. Sans Developer ID, ni Gatekeeper ni TCC n'y gagnent
# une identite stable. Le gain est la coherence de la signature, rien de plus.
# Source: man 1 codesign
echo "→ sceau ad-hoc"
codesign --force --sign - "$APP"
codesign --verify --strict "$APP"

echo "✓ $APP"
