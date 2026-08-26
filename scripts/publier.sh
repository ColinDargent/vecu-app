#!/usr/bin/env bash
# publier.sh : porter une version du dépôt de travail privé vers le dépôt public.
#
# Le dépôt public ne reçoit PAS l'historique interne. Il reçoit, à chaque
# release, une copie complète de l'arbre à une référence donnée, en un commit.
# C'est la décision du 24/08 (voir le vault) et c'est ce script qui la tient :
# une règle de publication qui vit dans une note ne s'applique pas.
#
#   usage : scripts/publier.sh <ref> [chemin-du-clone-public]
#           scripts/publier.sh v0.9.1 ~/code/vecu-app
#
# Le script NE POUSSE JAMAIS. Il prépare le commit dans le clone public et
# s'arrête là, avec la commande de push à lancer à la main. La mise en public
# d'une version est une décision, pas une conséquence.
set -euo pipefail

REF="${1:-}"
PUBLIC="${2:-$HOME/code/vecu-app}"

if [ -z "$REF" ]; then
	echo "usage : scripts/publier.sh <ref> [chemin-du-clone-public]" >&2
	echo "        scripts/publier.sh v0.9.1 ~/code/vecu-app" >&2
	exit 2
fi

cd "$(dirname "$0")/.."
PRIVE="$(pwd)"

# --- Ce qui ne sort jamais -----------------------------------------------
#
# appdist/ et dist/ sont la cause des 202 Mo du dépôt privé : une douzaine de
# binaires à ~10 Mo empilés au fil des releases. appdist/ RESTE suivi dans le
# dépôt privé, c'est par lui que Railway sert le manifeste d'auto-update. Il
# n'a rien à faire en public : les binaires y sont signés avec la clé privée de
# release, donc inutilisables pour un tiers, et un fork qui les servirait ferait
# auto-updater ses postes depuis la prod d'ici.
EXCLUS=(
	appdist
	dist
	bin
	data
	.env
	scripts/publier.deny
)

# --- Vérifications avant de toucher quoi que ce soit ----------------------

if ! git rev-parse --verify --quiet "$REF^{commit}" >/dev/null; then
	echo "erreur : la référence '$REF' n'existe pas dans $PRIVE" >&2
	exit 1
fi

if [ ! -d "$PUBLIC/.git" ]; then
	echo "erreur : '$PUBLIC' n'est pas un dépôt git." >&2
	echo "         Cloner d'abord le dépôt public à cet endroit, ou passer son" >&2
	echo "         chemin en second argument." >&2
	exit 1
fi

if [ -n "$(git -C "$PUBLIC" status --porcelain)" ]; then
	echo "erreur : le clone public '$PUBLIC' a des modifications non commitées." >&2
	echo "         Les traiter d'abord : ce script écrase l'arbre." >&2
	exit 1
fi

SHA="$(git rev-parse --short "$REF")"
echo "==> source   : $PRIVE @ $REF ($SHA)"
echo "==> cible    : $PUBLIC"

# --- Export de l'arbre ----------------------------------------------------

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

git archive "$REF" | tar -x -C "$TMP"

for chemin in "${EXCLUS[@]}"; do
	rm -rf "${TMP:?}/$chemin"
done

# --- Contrôle anti-fuite --------------------------------------------------
#
# Deux passes. Les motifs techniques attrapent un secret oublié ; le fichier
# de mots interdits attrape ce qui est propre à la maison (noms de clients,
# adresses, chemins internes), et il se remplit au fil du temps.
echo "==> contrôle anti-fuite"

FUITE=0

# Fichiers qui ne doivent jamais exister dans l'export.
while IFS= read -r f; do
	echo "    FICHIER SUSPECT : $f" >&2
	FUITE=1
done < <(cd "$TMP" && find . \
	\( -name '.env' -o -name '.env.*' -o -name '*.pem' -o -name '*.key' \
	-o -name 'id_rsa*' -o -name '*.p12' \) -not -path './.git/*')

# Motifs de secrets dans le contenu.
MOTIFS='gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----'
while IFS= read -r hit; do
	echo "    SECRET POSSIBLE : $hit" >&2
	FUITE=1
done < <(cd "$TMP" && grep -rIEn "$MOTIFS" . 2>/dev/null | grep -v '^\./scripts/publier\.sh:' || true)

# Mots interdits, un par ligne, '#' commente. Optionnel.
DENY="$PRIVE/scripts/publier.deny"
if [ -f "$DENY" ]; then
	while IFS= read -r mot; do
		[ -z "$mot" ] && continue
		case "$mot" in \#*) continue ;; esac
		while IFS= read -r hit; do
			echo "    MOT INTERDIT ($mot) : $hit" >&2
			FUITE=1
		done < <(cd "$TMP" && grep -rIin -- "$mot" . 2>/dev/null || true)
	done < "$DENY"
else
	echo "    (pas de $DENY, contrôle des noms internes non joué)"
fi

if [ "$FUITE" -ne 0 ]; then
	echo "" >&2
	echo "PUBLICATION ARRÊTÉE : l'export ci-dessus contient ce qui est listé." >&2
	echo "Rien n'a été écrit dans $PUBLIC." >&2
	exit 1
fi
echo "    rien à signaler"

# --- Le .gitignore du dépôt public ---------------------------------------
#
# Différent de celui du privé, et c'est voulu : ici appdist/ est exclu pour que
# les 202 Mo ne reviennent pas par la porte de derrière.
cat > "$TMP/.gitignore" <<'IGNORE'
bin/
data/
.env
.DS_Store

# Sorties de build. Les binaires d'une version passent par les releases GitHub,
# jamais par l'arbre : c'est ce qui a fait grossir le dépôt d'origine à 202 Mo.
/dist/
/appdist/

# Binaire de l'outil de release (passer par `make release`).
/vecu-release
IGNORE

# --- Pose dans le clone public -------------------------------------------

echo "==> écriture de l'arbre"
rsync -a --delete --exclude '.git/' "$TMP/" "$PUBLIC/"

cd "$PUBLIC"
git add -A

if git diff --cached --quiet; then
	echo "==> aucun changement par rapport à la version publique actuelle."
	exit 0
fi

git commit -q -m "Vécu $REF

Copie de l'arbre de travail à $REF ($SHA), sans l'historique interne.
Publié sous AGPL-3.0."

echo ""
echo "==> commit préparé dans $PUBLIC :"
git --no-pager show --stat --oneline HEAD | head -30
echo ""
echo "Rien n'est poussé. Pour publier :"
echo "    git -C $PUBLIC push"
