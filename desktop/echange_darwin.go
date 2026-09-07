package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// echangeBundle (macOS) met `nouveau` à la place de `installe`.
//
// Chemin nominal : renamex_np avec RENAME_SWAP, qui échange atomiquement les
// deux chemins. Les NOMS ne bougent pas, seuls les contenus s'échangent — c'est
// pour ça que le plist launchd, qui pointe un chemin à l'intérieur du bundle,
// continue de résoudre après coup. Après un échange réussi, `nouveau` porte
// l'ANCIEN bundle, que l'appelant supprime.
// Source: man 2 renamex_np — « it will cause the source and target to be
// atomically swapped », valable entre dossiers, sous réserve que le volume
// annonce VOL_CAP_INT_RENAME_SWAP (vrai sur APFS).
//
// Repli quand le volume ne le supporte pas : deux renames, dans
// `echangeParDoubleRename` (partagé avec les autres systèmes).
func echangeBundle(nouveau, installe string) error {
	err := unix.RenamexNp(nouveau, installe, unix.RENAME_SWAP)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return fmt.Errorf("échange atomique : %w", err)
	}
	return echangeParDoubleRename(nouveau, installe)
}
