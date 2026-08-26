package tickets

import (
	"testing"
	"time"
)

func storeFige(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	instant := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	s := New()
	s.maintenant = func() time.Time { return instant }
	return s, &instant
}

// TestBilletNeVautQuUneFois : la propriété centrale. Une URL rejouée - depuis
// un historique de navigateur, un journal de serveur mandataire, une capture
// d'écran - ne doit ouvrir aucune session.
func TestBilletNeVautQuUneFois(t *testing.T) {
	s, _ := storeFige(t)
	jeton, err := s.Emet(42)
	if err != nil {
		t.Fatal(err)
	}
	uid, ok := s.Consomme(jeton)
	if !ok || uid != 42 {
		t.Fatalf("premier usage refusé : %d %v", uid, ok)
	}
	if _, ok := s.Consomme(jeton); ok {
		t.Error("le billet a servi deux fois")
	}
}

// TestBilletExpire : la borne de temps, qui est l'autre moitié de la garantie.
func TestBilletExpire(t *testing.T) {
	s, instant := storeFige(t)
	jeton, err := s.Emet(7)
	if err != nil {
		t.Fatal(err)
	}
	*instant = instant.Add(duree + time.Second)
	if _, ok := s.Consomme(jeton); ok {
		t.Error("un billet périmé a été accepté")
	}
}

// TestJetonInconnuNeDonneRien : un billet inventé ne doit rien ouvrir, et ne
// doit pas non plus faire tomber le serveur.
func TestJetonInconnuNeDonneRien(t *testing.T) {
	s, _ := storeFige(t)
	if _, ok := s.Consomme("pas-un-billet"); ok {
		t.Error("un jeton inconnu a été accepté")
	}
	if _, ok := s.Consomme(""); ok {
		t.Error("un jeton vide a été accepté")
	}
}

// TestPurgeBorneLaTable : les billets non consommés sont la population
// normale (on clique, on ne va pas au bout). Sans purge, la table grossirait
// pour la durée de vie du serveur.
func TestPurgeBorneLaTable(t *testing.T) {
	s, instant := storeFige(t)
	for i := 0; i < 10; i++ {
		if _, err := s.Emet(int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.m) != 10 {
		t.Fatalf("dix billets attendus, %d", len(s.m))
	}
	*instant = instant.Add(duree + time.Second)
	if _, err := s.Emet(99); err != nil { // l'émission purge
		t.Fatal(err)
	}
	if len(s.m) != 1 {
		t.Errorf("les billets périmés n'ont pas été purgés : %d restants", len(s.m))
	}
}
