// Package auth : hachage des mots de passe (argon2id) et jetons par appareil.
// Aucun secret ni mot de passe n'est jamais stocké ni loggué en clair.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Paramètres argon2id : 2e option recommandée RFC 9106 §4 (64 MiB, 3 passes),
// raisonnable pour un serveur self-hosted modeste.
// Source: https://pkg.go.dev/golang.org/x/crypto/argon2#IDKey
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword renvoie un encodage self-describing du hash argon2id, au format
// PHC : $argon2id$v=19$m=...,t=...,p=...$<salt b64>$<hash b64>.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("mot de passe vide")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64(salt), b64(hash)), nil
}

// VerifyPassword compare un mot de passe candidat à un encodage PHC stocké.
// Comparaison à temps constant. Renvoie false sur encodage invalide (jamais
// d'erreur qui distinguerait « mauvais format » de « mauvais mot de passe »
// côté appelant : dans les deux cas, refus).
func VerifyPassword(password, encoded string) bool {
	salt, want, t, m, p, ok := parsePHC(encoded)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func parsePHC(encoded string) (salt, hash []byte, t, m uint32, p uint8, ok bool) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return nil, nil, 0, 0, 0, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, 0, 0, 0, false
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, 0, 0, 0, false
	}
	return salt, hash, t, m, p, true
}

func b64(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }
