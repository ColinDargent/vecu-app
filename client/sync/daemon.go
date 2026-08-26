package sync

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Réglages de la boucle de sync (spec).
const (
	debounce     = 2 * time.Second  // laisser retomber une rafale d'events
	pollInterval = 15 * time.Second // filet : réconciliation périodique
)

// Run lance la boucle de synchronisation jusqu'à annulation du contexte.
// Un cycle au démarrage (réconciliation), puis à chaque event fsnotify
// (debouncé) ou tous les pollInterval - un cycle étant un scan complet, le poll
// garantit la sync même si des events sont manqués (la sync ne repose jamais
// sur les events seuls).
func (e *Engine) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Cycle initial (réconciliation au démarrage), puis pose des watches : les
	// dossiers à surveiller sont les espaces montés, que seul un cycle connaît.
	if err := e.Cycle(); err != nil {
		e.logf("cycle initial : %v", err)
	}
	e.addWatchesEspaces(watcher)

	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	var debounceC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			// fsnotify n'est pas récursif : suivre les nouveaux dossiers à chaud.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					e.addWatchesRecursive(watcher, ev.Name)
				}
			}
			// Ignorer les events purement Chmod (ex: Spotlight) et .vecu/.
			if ev.Op == fsnotify.Chmod || e.isInternal(ev.Name) {
				continue
			}
			debounceC = time.After(debounce) // (re)armer le debounce

		case <-debounceC:
			debounceC = nil
			if err := e.Cycle(); err != nil {
				e.logf("cycle (event) : %v", err)
			}
			e.addWatchesEspaces(watcher)

		case <-poll.C:
			if err := e.Cycle(); err != nil {
				e.logf("cycle (poll) : %v", err)
			}
			e.addWatchesEspaces(watcher)

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			e.logf("fsnotify : %v", err)
		}
	}
}

// addWatchesEspaces surveille les espaces montés, et eux seuls. Rappelé après
// chaque cycle : un espace fraîchement monté doit être surveillé, et fsnotify
// ignore un Add sur un dossier déjà suivi.
func (e *Engine) addWatchesEspaces(w *fsnotify.Watcher) {
	for _, espace := range e.Espaces() {
		e.addWatchesRecursive(w, e.racineEspace(espace))
	}
}

// addWatchesRecursive ajoute un watch sur `root` et tous ses sous-dossiers non
// ignorés (fsnotify ne surveille pas récursivement : walk initial + ajout à
// chaud des dossiers créés).
func (e *Engine) addWatchesRecursive(w *fsnotify.Watcher, root string) {
	filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, err := e.rel(abs)
		if err == nil && rel != "." && e.ignore(rel) {
			return filepath.SkipDir
		}
		_ = w.Add(abs)
		return nil
	})
}

// isInternal indique si un chemin absolu est dans .vecu/ (à ignorer).
func (e *Engine) isInternal(abs string) bool {
	rel, err := e.rel(abs)
	if err != nil {
		return false
	}
	return e.ignore(rel)
}
