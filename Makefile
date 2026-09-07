# Vécu - cibles de build et de release.
export PATH := /opt/homebrew/bin:$(PATH)

.PHONY: test keygen release verif-sceau build-app build-exe verif-windows help

help:
	@echo "make test                 - lance toute la suite"
	@echo "make build-app VERSION=v0.3.0 - construit dist/Vécu.app (install manuelle)"
	@echo "make build-exe VERSION=v0.3.0 - construit dist/vecu-app.exe (Windows)"
	@echo "make verif-windows        - atteste que l'arbre compile pour windows/amd64"
	@echo "make release VERSION=v0.3.0   - construit + scelle appdist/ (à commit + push)"
	@echo "make verif-sceau          - relit le sceau AVEC le code du produit, avant de publier"
	@echo "make keygen               - génère la paire de clés de release (une fois)"

test:
	go test ./...

# Génère la paire de clés ed25519 de release. La clé privée est écrite hors dépôt
# (~/.config/vecu/), la clé publique est à coller dans desktop/update.go. À ne
# faire qu'une fois : régénérer invaliderait la capacité de signer les versions
# déjà distribuées.
keygen:
	go run ./cmd/vecu-release keygen

# Construit le bundle Vécu.app (pour l'installation manuelle initiale d'un poste).
build-app:
	@test -n "$(VERSION)" || (echo "usage : make build-app VERSION=vX.Y.Z" && exit 1)
	./desktop/build-app.sh dist "$(VERSION)"

# Construit vecu-app.exe (installation manuelle d'un poste Windows).
build-exe:
	@test -n "$(VERSION)" || (echo "usage : make build-exe VERSION=vX.Y.Z" && exit 1)
	./desktop/build-exe.sh dist "$(VERSION)"

# Vérifie que l'arbre compile pour Windows. Cible à part et non branchée sur
# `test` : elle n'atteste QUE la compilation. Ce qui casse sur Windows casse
# surtout à l'exécution (verrous, chemins, superviseur), et aucune cible make
# lancée depuis un Mac ne peut le dire.
verif-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./...
	@echo "l'arbre compile et passe vet pour windows/amd64"

# Prépare une release : cross-compile les binaires, scelle le manifeste avec la
# clé privée, et dépose le tout dans appdist/. La publication se termine par
# `git add appdist && git commit && git push` → le serveur redéployé (Railway)
# sert la nouvelle version, que les postes récupèrent en auto-update.
release:
	@test -n "$(VERSION)" || (echo "usage : make release VERSION=vX.Y.Z" && exit 1)
	go run ./cmd/vecu-release build -version "$(VERSION)"
	@$(MAKE) --no-print-directory verif-sceau

# Relit le manifeste scellé AVEC LE CODE DU PRODUIT : `appupdate.Open` pour la
# signature, `Asset.Check` pour les empreintes.
#
# Pourquoi c'est une cible et plus une consigne. Le 20/08, une vérification
# maison a rendu un FAUX NÉGATIF en comparant du base64 aux octets décodés, et
# la release a été retenue pour rien. La leçon a été écrite dans le message de
# commit de v0.9.1 - c'est-à-dire à un endroit que personne ne relit avant de
# publier. Elle est ici maintenant, et `release` l'appelle toute seule : une
# règle de discipline qui vit dans une note ne s'applique pas.
verif-sceau:
	go run ./cmd/verifsceau
