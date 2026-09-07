//go:build !darwin

package main

// echangeBundle (Windows et le reste) : le double rename, sans échange atomique.
//
// `renamex_np` est une extension d'Apple, sans équivalent ailleurs. Windows a
// bien `MoveFileEx(MOVEFILE_REPLACE_EXISTING)`, mais il REMPLACE au lieu
// d'échanger, et il refuse les dossiers non vides - donc il ne rend pas le
// service qu'on attend ici.
//
// Le repli qui existait déjà pour les volumes macOS sans VOL_CAP_INT_RENAME_SWAP
// fait exactement l'affaire, et il est sûr sur Windows : il écarte la cible
// AVANT de renommer la source dessus, donc il ne bute jamais sur la règle
// Windows qui interdit de renommer sur un chemin existant.
//
// La fenêtre où le chemin installé n'existe pas reste ouverte quelques
// millisecondes, et c'est la même que sur un volume macOS non-APFS : un défaut
// connu, pas un défaut neuf introduit par le port.
func echangeBundle(nouveau, installe string) error {
	return echangeParDoubleRename(nouveau, installe)
}
