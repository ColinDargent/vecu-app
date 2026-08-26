package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// NewToken génère un jeton d'appareil : 32 octets aléatoires en base64url.
// C'est cette valeur qui est remise au client (bearer) ; seul son hash est
// stocké côté serveur.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken : le serveur ne stocke jamais le jeton en clair. Un jeton étant à
// haute entropie (contrairement à un mot de passe), un SHA-256 suffit et reste
// rapide à vérifier à chaque requête.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
