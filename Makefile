# Vécu - cibles de build et de release.
export PATH := /opt/homebrew/bin:$(PATH)

.PHONY: test keygen release build-app help

help:
	@echo "make test                 - lance toute la suite"
	@echo "make build-app VERSION=v0.3.0 - construit dist/Vécu.app (install manuelle)"
	@echo "make release VERSION=v0.3.0   - construit + scelle appdist/ (à commit + push)"
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

# Prépare une release : cross-compile les binaires, scelle le manifeste avec la
# clé privée, et dépose le tout dans appdist/. La publication se termine par
# `git add appdist && git commit && git push` → le serveur redéployé (Railway)
# sert la nouvelle version, que les postes récupèrent en auto-update.
release:
	@test -n "$(VERSION)" || (echo "usage : make release VERSION=vX.Y.Z" && exit 1)
	go run ./cmd/vecu-release build -version "$(VERSION)"
