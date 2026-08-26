package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	enc, err := HashPassword("correct-horse-café")
	if err != nil {
		t.Fatalf("HashPassword : %v", err)
	}
	if !VerifyPassword("correct-horse-café", enc) {
		t.Error("le bon mot de passe est refusé")
	}
	if VerifyPassword("mauvais", enc) {
		t.Error("un mauvais mot de passe est accepté")
	}
}

func TestHashNeverPlaintext(t *testing.T) {
	pw := "s3cr3t-plaintext"
	enc, _ := HashPassword(pw)
	if strings.Contains(enc, pw) {
		t.Error("le mot de passe apparaît en clair dans le hash")
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$") {
		t.Errorf("format PHC inattendu : %q", enc)
	}
}

func TestDistinctSalts(t *testing.T) {
	a, _ := HashPassword("même")
	b, _ := HashPassword("même")
	if a == b {
		t.Error("deux hachages du même mot de passe sont identiques (sel non aléatoire)")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "pas-du-phc", "$argon2id$v=19$bad", "$bcrypt$x$y$z$w$v"} {
		if VerifyPassword("x", bad) {
			t.Errorf("encodage invalide accepté : %q", bad)
		}
	}
}

func TestEmptyPasswordRejected(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Error("mot de passe vide accepté")
	}
}

func TestTokenHashDeterministicAndOpaque(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken : %v", err)
	}
	if HashToken(tok) != HashToken(tok) {
		t.Error("HashToken non déterministe")
	}
	if strings.Contains(HashToken(tok), tok) {
		t.Error("le jeton apparaît dans son hash")
	}
	tok2, _ := NewToken()
	if tok == tok2 {
		t.Error("deux jetons identiques")
	}
}
