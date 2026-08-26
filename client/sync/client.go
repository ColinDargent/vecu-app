package sync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClient parle à l'API Vécu. Ne connaît que fichiers/versions, jamais git.
type HTTPClient struct {
	Server string
	Token  string
	HTTP   *http.Client
}

// NewHTTPClient construit un client avec un timeout raisonnable.
func NewHTTPClient(server, token string) *HTTPClient {
	return &HTTPClient{
		Server: strings.TrimRight(server, "/"),
		Token:  token,
		HTTP:   &http.Client{Timeout: 30 * time.Second},
	}
}

// SyncChange : une entrée du delta serveur.
type SyncChange struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted"`
	Content string `json:"content"`
}

// SyncResponse : réponse de GET /sync.
type SyncResponse struct {
	Head    string       `json:"head"`
	Changes []SyncChange `json:"changes"`
}

// WriteResponse : réponse d'un PUT/DELETE.
type WriteResponse struct {
	Head         string `json:"head"`
	Merged       bool   `json:"merged"`
	Conflict     bool   `json:"conflict"`
	ConflictPath string `json:"conflict_path"`
	// Status : code HTTP, exposé pour distinguer 200 / 403 / 404 côté moteur.
	Status int `json:"-"`
}

// Login échange identifiants contre un jeton d'appareil (POST /login).
func Login(server, username, password, device string) (Config, error) {
	server = strings.TrimRight(server, "/")
	body, _ := json.Marshal(map[string]string{
		"username": username, "password": password, "device": device,
	})
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Post(
		server+"/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return Config{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Config{}, fmt.Errorf("login refusé (HTTP %d)", resp.StatusCode)
	}
	var out struct{ Token, Username string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Config{}, err
	}
	return Config{Server: server, Username: out.Username, Token: out.Token}, nil
}

// Sync récupère le delta depuis `since` (vide = état complet du périmètre).
func (c *HTTPClient) Sync(since string) (SyncResponse, error) {
	var out SyncResponse
	u := c.Server + "/sync"
	if since != "" {
		u += "?since=" + url.QueryEscape(since)
	}
	return out, c.doJSON("GET", u, nil, &out)
}

// Espace : un espace visible par le compte, tel que renvoyé par le serveur.
type Espace struct {
	Nom string `json:"nom"`
	// Libelle : ce que l'humain lit, distinct du nom technique (DT3). Vide face
	// à un serveur d'avant la feature, ou pour un espace qui n'en a pas : dans
	// les deux cas on retombe sur le nom technique, et c'est lisible.
	Libelle  string `json:"libelle,omitempty"`
	Fichiers int    `json:"fichiers"`
	Ecriture bool   `json:"ecriture"`
}

// Affichage : ce qu'on montre à l'humain pour cet espace.
func (e Espace) Affichage() string {
	if e.Libelle != "" {
		return e.Libelle
	}
	return e.Nom
}

// Espaces récupère les espaces auxquels le compte a accès. Cette liste EST
// l'autorisation de monter : le client aligne ses dossiers locaux dessus à
// chaque cycle, sans jamais rien demander à l'utilisateur.
func (c *HTTPClient) Espaces() ([]Espace, error) {
	var out struct {
		Espaces []Espace `json:"espaces"`
	}
	return out.Espaces, c.doJSON("GET", c.Server+"/espaces", nil, &out)
}

// EspaceCree : ce que le serveur retient de la demande.
type EspaceCree struct {
	// Nom : le nom TECHNIQUE retenu, qui n'est pas forcément dérivé du libellé
	// demandé - deux personnes qui partagent chacune un dossier « notes »
	// produisent le même nom, et le serveur désambiguïse (DT3).
	Nom     string `json:"nom"`
	Libelle string `json:"libelle"`
	// Avertissement : l'espace existe, mais son libellé n'a pas pu être écrit.
	// Utilisable, affiché sous son nom technique.
	Avertissement string `json:"avertissement"`
}

// CreerEspace crée un espace sur le serveur à partir d'un libellé humain.
//
// N'a PAS son propre `doJSON` par goût de la duplication : cette route est la
// seule dont le message d'erreur doit atteindre la personne. « HTTP 400 » sur
// un libellé refusé ou un nom en collision est une impasse ; le serveur écrit
// exactement ce qui ne va pas, et le geste consiste à le lire.
func (c *HTTPClient) CreerEspace(libelle string) (EspaceCree, error) {
	var out EspaceCree
	corps, err := json.Marshal(map[string]string{"libelle": libelle})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequest("POST", c.Server+"/espaces", bytes.NewReader(corps))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e) == nil && e.Error != "" {
			return out, errors.New(e.Error)
		}
		return out, fmt.Errorf("création de l'espace : HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	if out.Nom == "" {
		return out, errors.New("le serveur n'a pas rendu de nom d'espace")
	}
	return out, nil
}

// SessionWeb demande un billet d'entrée à usage unique et rend le CHEMIN à
// ouvrir dans le navigateur.
//
// Le serveur rend un chemin relatif, jamais une URL absolue : il ne connaît son
// adresse publique que par l'en-tête `Host`. C'est ici qu'on la joint, parce que
// c'est ici qu'on la connaît - elle est dans la configuration de ce poste.
func (c *HTTPClient) SessionWeb() (string, error) {
	var out struct {
		Chemin string `json:"chemin"`
	}
	if err := c.doJSON("POST", c.Server+"/session-web", nil, &out); err != nil {
		return "", err
	}
	// Le serveur décide du chemin, mais pas de l'hôte : une réponse qui
	// contiendrait une URL absolue enverrait le navigateur ailleurs, avec un
	// billet valide. Un serveur compromis reste un serveur compromis, mais il
	// n'a pas à pouvoir rediriger vers un autre domaine par ce chemin-là.
	if !strings.HasPrefix(out.Chemin, "/") || strings.HasPrefix(out.Chemin, "//") {
		return "", fmt.Errorf("chemin d'ouverture inattendu : %q", out.Chemin)
	}
	return c.Server + out.Chemin, nil
}

// NouveauSkill : ce que le signal de naissance porte, et rien de plus.
type NouveauSkill struct {
	Slug   string `json:"slug"`
	Auteur string `json:"auteur"`
	CreeLe string `json:"cree_le"`
}

// SkillsNouveaux récupère les skills déposés par les AUTRES comptes.
//
// Rend une existence, jamais un contenu : un skill listé ici peut très bien
// rester illisible sur ce poste, et c'est le cas nominal - il naît privé.
func (c *HTTPClient) SkillsNouveaux() ([]NouveauSkill, error) {
	var out struct {
		Nouveaux []NouveauSkill `json:"nouveaux"`
	}
	return out.Nouveaux, c.doJSON("GET", c.Server+"/skills/nouveaux", nil, &out)
}

// Tree récupère la liste complète des chemins visibles (réconciliation périmètre).
func (c *HTTPClient) Tree() ([]string, error) {
	var out struct {
		Paths []string `json:"paths"`
	}
	return out.Paths, c.doJSON("GET", c.Server+"/tree", nil, &out)
}

// Read récupère le contenu d'un fichier du périmètre ET le commit d'où il sort.
// Sert à l'hydratation d'un espace fraîchement monté : son contenu n'est pas
// dans le delta depuis notre head, puisqu'il était hors périmètre jusqu'ici.
//
// `commit` est vide face à un serveur qui ne le rend pas encore (fenêtre de
// bascule) : c'est à l'appelant de décider quoi en faire, pas à cette couche.
func (c *HTTPClient) Read(p string) (contenu, commit string, err error) {
	var out struct {
		Content string `json:"content"`
		Commit  string `json:"commit"`
	}
	err = c.doJSON("GET", c.fileURL(p), nil, &out)
	return out.Content, out.Commit, err
}

// Put écrit un fichier avec base_oid. Renvoie la réponse (dont Status).
func (c *HTTPClient) Put(path, content, baseOID string) (WriteResponse, error) {
	body, _ := json.Marshal(map[string]string{"content": content, "base_oid": baseOID})
	return c.doWrite("PUT", c.fileURL(path), body)
}

// Delete supprime un fichier avec base_oid.
func (c *HTTPClient) Delete(path, baseOID string) (WriteResponse, error) {
	u := c.fileURL(path) + "?base_oid=" + url.QueryEscape(baseOID)
	return c.doWrite("DELETE", u, nil)
}

// fileURL construit l'URL de /files/{path} en encodant les segments (espaces
// des copies de conflit, accents des chemins FR).
func (c *HTTPClient) fileURL(p string) string {
	u := &url.URL{Path: "/files/" + p}
	return c.Server + u.String()
}

func (c *HTTPClient) doWrite(method, u string, body []byte) (WriteResponse, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, reader)
	if err != nil {
		return WriteResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return WriteResponse{}, err
	}
	defer resp.Body.Close()
	out := WriteResponse{Status: resp.StatusCode}
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return WriteResponse{}, err
		}
		out.Status = http.StatusOK
	}
	return out, nil
}

func (c *HTTPClient) doJSON(method, u string, body io.Reader, out any) error {
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s : HTTP %d", method, u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
