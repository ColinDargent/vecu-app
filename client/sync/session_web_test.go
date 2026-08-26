package sync

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSessionWebRefuseUnCheminQuiChangeDHote : le serveur décide du chemin, pas
// de l'hôte.
//
// Une réponse contenant une URL absolue, ou un « //autre.example » que le
// navigateur lit comme un hôte, enverrait la personne ailleurs AVEC un billet
// valide. Le serveur reste maître de ce qu'il sert ; il n'a pas à pouvoir
// rediriger vers un autre domaine par ce chemin-là.
func TestSessionWebRefuseUnCheminQuiChangeDHote(t *testing.T) {
	for _, mauvais := range []string{
		"https://ailleurs.example/admin/entrer?jeton=x",
		"//ailleurs.example/admin/entrer?jeton=x",
		"admin/entrer?jeton=x",
		"",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"chemin":"` + mauvais + `"}`))
		}))
		_, err := NewHTTPClient(srv.URL, "jeton").SessionWeb()
		if err == nil {
			t.Errorf("chemin accepté alors qu'il change d'hôte ou n'est pas absolu : %q", mauvais)
		}
		srv.Close()
	}
}

// TestSessionWebJointLHoteDeLaConfiguration : le cas nominal. L'adresse vient
// de la configuration de ce poste, pas de la réponse.
func TestSessionWebJointLHoteDeLaConfiguration(t *testing.T) {
	var recu string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recu = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"chemin":"/admin/entrer?jeton=abc"}`))
	}))
	defer srv.Close()

	url, err := NewHTTPClient(srv.URL, "jeton-appareil").SessionWeb()
	if err != nil {
		t.Fatal(err)
	}
	if url != srv.URL+"/admin/entrer?jeton=abc" {
		t.Errorf("URL construite : %q", url)
	}
	if !strings.Contains(recu, "POST /session-web") || !strings.Contains(recu, "Bearer jeton-appareil") {
		t.Errorf("requête inattendue : %q", recu)
	}
}
