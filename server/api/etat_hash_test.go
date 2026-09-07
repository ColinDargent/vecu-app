package api

import (
	"testing"

	"github.com/colindargent/vecu/server/auth"
	"github.com/colindargent/vecu/server/db"
)

// hashDe : le hash d'un jeton clair, comme la porte le calcule. Passe par
// `auth.HashToken` plutôt que de réimplémenter SHA-256 : un test qui recalcule
// à sa façon valide sa propre arithmétique, pas celle du code.
func hashDe(t *testing.T, _ *db.DB, jeton string) string {
	t.Helper()
	return auth.HashToken(jeton)
}
