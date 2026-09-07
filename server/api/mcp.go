package api

// mcp.go : la surface MCP, révision 2026-07-28 SEULE.
//
// Cette révision a SUPPRIMÉ le handshake `initialize`, les sessions et le
// stream GET. On ne l'implémente donc pas « en plus » de l'ère précédente : il
// n'y a rien de l'ère `initialize` dans ce fichier, et c'est délibéré. Un
// client qui ne parle que l'ancienne ère (Cursor, Codex, un Claude Code
// antérieur à v2.1.232) échouera, et c'est le report assumé.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http
//
// JSON-RPC 2.0 à la main, bibliothèque standard seule. Une bibliothèque MCP
// serait une septième dépendance directe du dépôt, sur un protocole qui a
// changé de forme deux fois en un an.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/colindargent/vecu/server/db"
)

// revisionMCP : la SEULE révision servie. Constante unique, parce que le
// serveur doit pouvoir annoncer ce qu'il supporte quand il refuse - et qu'une
// version en dur à deux endroits finit par diverger.
const revisionMCP = "2026-07-28"

// champMetaVersion : la révision se déclare AUSSI dans le corps, et l'en-tête
// doit la refléter. C'est ce couple que la validation compare.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http#protocol-version-header
const champMetaVersion = "io.modelcontextprotocol/protocolVersion"

// versionServeurMCP : ce que `server/discover` déclare comme version du
// logiciel. Auto-déclaré, jamais vérifié par le protocole, et la spec dit
// elle-même qu'un client ne doit pas décider quoi que ce soit dessus.
const versionServeurMCP = "0.1.0"

// maxBodyMCP : la borne du corps d'un appel MCP.
//
// LE DOUBLE de la borne du PUT, et c'est une mesure, pas une marge au jugé. Le
// contenu d'un fichier voyage échappé dans du JSON, donc il pèse plus que le
// fichier. Mesuré avec `encoding/json` (et pas avec un autre langage, dont les
// règles d'échappement diffèrent) :
//
//	texte français accentué  x1,03   (Go n'échappe pas le non-ASCII)
//	markdown courant         x1,24
//	code avec < > et &       x1,81   (échappement HTML par défaut)
//	pire cas, guillemets     x2,00
//	pire cas, octets nuls    x6,00
//
// x2 couvre tout ce qui ressemble à une note ou à du code. Le pire cas
// théorique (un fichier d'octets de contrôle) reste refusé - ce n'est pas du
// texte, et le périmètre de Vécu est le texte.
//
// Sans ce doublement, un fichier que le PUT accepte pouvait être refusé par
// `ecrire_fichier` : les deux portes auraient divergé sur la taille, ce que la
// spec de S2 avait nommé comme une limite « à revoir en S3, avec une mesure ».
const maxBodyMCP = 2 * maxBodyBytes

// Les indices de cache. La spec en fait un MUST sur tout résultat
// `resultType: "complete"` de `server/discover` et `tools/list` - ce n'est pas
// un ornement, un client conforme refuse le résultat sans eux (mesuré au
// branchement : Claude Code rejette `tools/list` en disant exactement ça).
// Source: https://modelcontextprotocol.io/specification/2026-07-28/server/utilities/caching
const (
	// Les deux résultats sont servis en « private », et depuis la slice
	// d'écriture ce n'est plus une marge de sûreté : `tools/list` DÉPEND du
	// plafond du jeton, puisqu'un jeton « lecture » ne se voit pas proposer les
	// outils d'écriture. La spec avertit qu'un résultat « public » peut être
	// servi à un AUTRE porteur de jeton depuis un endpoint pourtant
	// authentifié : ce serait exactement servir à un jeton lecture le
	// catalogue calculé pour un jeton écriture.
	ttlDiscoverMS = 3_600_000 // 1 h
	// `tools/list` est « private », et c'est une décision de SÉCURITÉ, pas un
	// réglage de performance. La spec avertit qu'un résultat « public » peut
	// être partagé entre porteurs de jetons différents, y compris depuis un
	// endpoint authentifié. Notre liste dépend du plafond du jeton : la publier
	// reviendrait à laisser un cache servir à un jeton `lecture` ce qui a été
	// calculé pour un jeton `ecriture`. « private » borne le cache au même
	// contexte d'autorisation, ce qui est très exactement notre modèle.
	ttlToolsMS = 300_000 // 5 min
)

// Les codes d'erreur JSON-RPC utilisés ici. Les deux derniers sortent de la
// sous-plage que la spec MCP réserve à ses erreurs de protocole.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/basic/index#error-codes
const (
	codeParseErreur         = -32700 // corps illisible
	codeRequeteInvalide     = -32600 // enveloppe JSON-RPC non conforme
	codeMethodeInconnue     = -32601 // Method not found -> HTTP 404
	codeParamsInvalides     = -32602 // outil inconnu, arguments malformés
	codeErreurInterne       = -32603 // panne du serveur, pas une faute du client
	codeHeaderMismatch      = -32020 // en-têtes != corps -> HTTP 400
	codeVersionNonSupportee = -32022 // révision non servie -> HTTP 400
)

// requeteMCP : l'enveloppe JSON-RPC 2.0 telle qu'on la lit.
//
// `ID` est un RawMessage et pas un type concret : la spec autorise un nombre
// comme une chaîne, et on ne fait que le renvoyer tel quel. Le décoder en
// `any` le ferait ressortir en flottant, donc `1` deviendrait `1` ou `1.0`
// selon l'humeur de l'encodeur - un client qui apparie ses réponses par id ne
// s'en remettrait pas.
type requeteMCP struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  paramsMCP       `json:"params"`
}

type paramsMCP struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      map[string]any  `json:"_meta"`
}

// version rend la révision déclarée dans le corps, ou la chaîne vide.
func (p paramsMCP) version() string {
	v, _ := p.Meta[champMetaVersion].(string)
	return v
}

// notification : une requête SANS `id` est une notification, et n'appelle
// aucune réponse. `null` compte comme absent : c'est un id qu'aucune réponse
// ne pourrait apparier, donc le traiter comme une requête fabriquerait une
// réponse orpheline.
func (r requeteMCP) notification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

type erreurMCP struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type reponseMCP struct {
	JSONRPC string `json:"jsonrpc"`
	// PRESENT MEME QUAND ON NE LE CONNAIT PAS. JSON-RPC 2.0 §5 exige un `id`
	// dans toute reponse, valant `null` quand la requete n'a pas permis de le
	// determiner (erreur de parsing, requete invalide). `omitempty` l'omettait,
	// ce qu'un client a parseur strict rejette. `idInconnu` porte ce `null`.
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *erreurMCP      `json:"error,omitempty"`
}

// idInconnu : le `null` de JSON-RPC, pour les refus qui tombent avant qu'on
// ait pu lire l'id de la requete.
var idInconnu = json.RawMessage("null")

func ecritResultatMCP(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, reponseMCP{JSONRPC: "2.0", ID: id, Result: result})
}

func ecritErreurMCP(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data any) {
	if len(id) == 0 {
		id = idInconnu
	}
	writeJSON(w, status, reponseMCP{
		JSONRPC: "2.0", ID: id,
		Error: &erreurMCP{Code: code, Message: message, Data: data},
	})
}

// handleMCP : le point d'accès unique. POST seulement.
//
// GET et DELETE rendent 405 sans une ligne de code ici : `ServeMux` de Go 1.22+
// répond lui-même 405 quand un chemin est enregistré mais pas pour cette
// méthode. C'est aussi ce que la spec demande pour un client de l'ère
// précédente qui tenterait d'ouvrir un stream GET ou de fermer une session.
// Source: https://pkg.go.dev/net/http#ServeMux
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	// 1. Origin. La garde existe contre le DNS rebinding : un site web ne doit
	// pas pouvoir parler au serveur depuis le navigateur de quelqu'un. Absent,
	// la requête passe - les clients non navigateurs n'en envoient pas, et
	// exiger l'en-tête fermerait la porte à tous nos usages.
	// Source: https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http#security-endpoint
	if r.Header.Get("Origin") != "" {
		ecritErreurMCP(w, http.StatusForbidden, nil, codeRequeteInvalide,
			"origine refusée", nil)
		return
	}

	// 2. Le jeton. Aucune découverte n'est possible (pas de WWW-Authenticate,
	// pas de .well-known) : c'est le prix assumé du choix « jeton d'abord », le
	// jeton se pose à la main.
	jeton, ok := bearer(r)
	if !ok {
		ecritErreurMCP(w, http.StatusUnauthorized, nil, codeRequeteInvalide,
			"jeton manquant", nil)
		return
	}
	porteur, err := s.DB.PorteurParJetonMCP(jeton)
	if errors.Is(err, db.ErrNotFound) {
		// Inconnu et révoqué rendent le MÊME refus, et ne se distinguent pas de
		// l'extérieur : dire « ce jeton a été révoqué » confirmerait qu'il a
		// existé.
		ecritErreurMCP(w, http.StatusUnauthorized, nil, codeRequeteInvalide,
			"jeton invalide", nil)
		return
	}
	if err != nil {
		ecritErreurMCP(w, http.StatusInternalServerError, nil, codeRequeteInvalide,
			"erreur interne", nil)
		return
	}
	// Décodage en DEUX TEMPS, et chaque temps ferme un défaut mesuré.
	//
	// D'abord un objet générique : il rend l'`id` récupérable même quand la
	// suite du corps est mal typée. Sans lui, une requête parfaitement
	// appariable dont seul `params` a la mauvaise forme repartait avec un
	// `id: null`, et le client ne pouvait plus rattacher le refus à son appel.
	//
	// `dec.More()` ensuite, parce que `Decode` s'arrête au premier objet et
	// ignore tout ce qui suit : `{...} n_importe_quoi` passait en 200. Ce
	// fichier consacre une section entière à empêcher qu'un intermédiaire et le
	// serveur lisent deux vérités différentes ; laisser cette divergence entrer
	// par le corps annulerait l'exercice.
	brut, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyMCP))
	if err != nil {
		ecritErreurMCP(w, http.StatusBadRequest, idInconnu, codeParseErreur, "corps illisible", nil)
		return
	}
	// `json.Unmarshal` sur le corps ENTIER, et surtout pas un `json.Decoder`.
	// Mesuré : `Decoder.Decode` s'arrête au premier objet, et son `More()` rend
	// `false` devant un `}` ou un `]` en trop - donc `{...}}` passait en 200,
	// alors que `Unmarshal` sur les mêmes octets le refuse. Le serveur acceptait
	// un document que son propre analyseur juge invalide, ce qui est très
	// exactement la divergence de lecture que la validation d'en-têtes existe
	// pour fermer.
	if !json.Valid(brut) {
		ecritErreurMCP(w, http.StatusBadRequest, idInconnu, codeParseErreur, "corps JSON invalide", nil)
		return
	}
	// Le lot JSON-RPC n'existe pas sur ce transport : « The body of the HTTP
	// POST MUST be a single JSON-RPC request or notification ». Le refuser en
	// -32600 plutôt qu'en -32700 dit la vérité - le JSON est lisible, c'est la
	// requête qui n'est pas conforme.
	if premierOctetJSON(brut) == '[' {
		ecritErreurMCP(w, http.StatusBadRequest, idInconnu, codeRequeteInvalide,
			"les lots JSON-RPC ne sont pas acceptés sur ce transport", nil)
		return
	}
	if premierOctetJSON(brut) != '{' {
		ecritErreurMCP(w, http.StatusBadRequest, idInconnu, codeRequeteInvalide,
			"le corps doit être un objet JSON-RPC", nil)
		return
	}
	var enveloppe struct {
		ID json.RawMessage `json:"id"`
	}
	json.Unmarshal(brut, &enveloppe) // best-effort : sert seulement à rendre le refus appariable
	// JSON-RPC 2.0 §4 : un `id` est String, Number ou Null. Un objet ou un
	// tableau réfléchi tel quel ferait porter à notre réponse un id que la
	// spec interdit.
	if !idJSONRPCValide(enveloppe.ID) {
		ecritErreurMCP(w, http.StatusBadRequest, idInconnu, codeRequeteInvalide,
			"l'id d'une requête JSON-RPC doit être une chaîne, un nombre ou null", nil)
		return
	}
	var req requeteMCP
	if err := json.Unmarshal(brut, &req); err != nil {
		ecritErreurMCP(w, http.StatusBadRequest, enveloppe.ID, codeParamsInvalides,
			"requête JSON-RPC mal formée : "+err.Error(), nil)
		return
	}
	// La sonde de négociation, et c'est elle qui répond au point laissé ouvert
	// par la spec : « la sonde exacte que Claude Code envoie, à observer dans
	// les logs plutôt qu'à déduire ». Une ligne par appel, avant toute
	// validation, sinon un refus de forme ne se verrait jamais.
	// Chaque valeur est BORNÉE : elles viennent toutes du client, jusqu'au
	// budget d'en-têtes de Go. `%q` protège de l'injection dans le journal, pas
	// du volume - sans troncature, un porteur de jeton inonde le disque à
	// raison d'une ligne par requête, refusées comprises.
	s.logf("mcp : method=%q version-entete=%q version-corps=%q mcp-method=%q mcp-name=%q agent=%q",
		borne(req.Method), borne(r.Header.Get("MCP-Protocol-Version")), borne(req.Params.version()),
		borne(r.Header.Get("Mcp-Method")), borne(r.Header.Get("Mcp-Name")), borne(r.Header.Get("User-Agent")))

	if req.JSONRPC != "2.0" {
		ecritErreurMCP(w, http.StatusBadRequest, req.ID, codeRequeteInvalide,
			`champ "jsonrpc" attendu à "2.0"`, nil)
		return
	}

	// 3. `initialize` : la révision l'a supprimé, donc c'est une méthode
	// inconnue. On ne l'IMPLÉMENTE pas - il n'y a pas une ligne de l'ère des
	// sessions ici - mais on refuse en NOMMANT ce qu'on sert.
	//
	// La spec le demande explicitement d'un serveur qui ne parle que l'ère
	// moderne : « legacy clients have no fall-forward mechanism, and this
	// message may be the only diagnostic they can surface to users ». Sans ça,
	// un client trop ancien affiche « en-tête MCP-Protocol-Version manquant »,
	// ce qui est vrai et n'apprend rien à personne.
	//
	// AVANT la validation d'en-têtes, parce qu'une requête `initialize` n'en
	// porte aucun : la laisser passer par là rendrait justement le message que
	// ce bloc existe pour remplacer.
	// Source: https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning#backward-compatibility-with-initialization-based-versions
	if req.Method == "initialize" {
		ecritErreurMCP(w, http.StatusNotFound, req.ID, codeMethodeInconnue,
			"ce serveur ne parle que la révision "+revisionMCP+" du protocole MCP, "+
				"qui a supprimé le handshake initialize. Client trop ancien.",
			map[string]any{"supported": []string{revisionMCP}})
		return
	}

	// 4. Les en-têtes miroirs. Le transport recopie certains champs du corps
	// dans des en-têtes pour que les intermédiaires routent sans lire le corps ;
	// le serveur DOIT vérifier que les deux disent la même chose, sinon un
	// répartiteur et le serveur travaillent sur deux vérités différentes.
	//
	// AVANT la branche des notifications, et c'est un correctif : les faire
	// sauter pour une notification laissait passer un `Mcp-Method: tools/call`
	// menteur sur un corps `notifications/x`, acquitté en 202. Un répartiteur
	// qui autorise sur l'en-tête voyait une écriture là où le serveur ne voyait
	// rien - le scénario des deux vérités, offert sur le seul chemin qui ne
	// coûte rien à envoyer. La révision ne DÉFINIT pas d'exigence d'en-tête pour
	// un POST de notification ; elle ne l'interdit pas non plus, et elle ne
	// définit aucune notification client -> serveur sur ce transport. Valider
	// est donc le choix sûr sur un chemin qui n'a, de toute façon, aucun usage.
	if motif := ecartDEntetes(r, req); motif != "" {
		ecritErreurMCP(w, http.StatusBadRequest, req.ID, codeHeaderMismatch, motif, nil)
		return
	}

	// 5. La révision. Refusée avec la liste de ce qu'on sert : c'est ce qui
	// permet à un client moderne de se corriger au lieu de se rabattre sur
	// l'ère `initialize`.
	if v := req.Params.version(); v != revisionMCP {
		ecritErreurMCP(w, http.StatusBadRequest, req.ID, codeVersionNonSupportee,
			"révision de protocole non supportée", map[string]any{
				"supported": []string{revisionMCP},
				"requested": v,
			})
		return
	}

	// 6. Requête ou notification ? La distinction décide de tout ce qui suit.
	//
	// Une méthode de REQUÊTE envoyée sans `id` n'est pas une notification,
	// c'est une requête malformée. L'acquitter en 202 serait le pire des deux
	// mondes : le client lit « accepté » et le travail n'a jamais lieu -
	// silencieux, donc invisible, donc le mode de perte que ce dépôt refuse
	// partout ailleurs.
	if req.notification() {
		if !strings.HasPrefix(req.Method, "notifications/") {
			ecritErreurMCP(w, http.StatusBadRequest, idInconnu, codeRequeteInvalide,
				"la méthode "+req.Method+" est une requête et exige un id ; "+
					"seules les méthodes notifications/* peuvent être envoyées sans id", nil)
			return
		}
		// `dernier_usage` avance : c'est le seul chemin où le serveur répond
		// explicitement « accepté ». L'oublier ferait paraître « jamais servi »
		// un jeton qui travaille, ce qui annule la raison d'être de la colonne.
		s.toucheJeton(porteur)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Une méthode qu'on ne sert pas s'arrête ici, AVANT toute écriture en base.
	// Deux raisons, et la seconde n'est pas cosmétique : le contrat dit
	// « dernier_usage avance à chaque appel ACCEPTÉ », et `db.Open` fixe
	// `SetMaxOpenConns(1)`, donc chaque écriture prend l'unique connexion,
	// partagée avec le web admin et les PUT de fichiers. Un porteur qui boucle
	// sur des noms de méthode inventés produirait sinon une écriture SQLite par
	// requête, et sérialiserait les écritures de tout le serveur.
	//
	// 404 et pas 400 : le corps JSON-RPC distingue ce cas d'un 404 rendu par un
	// serveur d'une ère antérieure qui n'héberge simplement pas ce chemin.
	if !methodeServie(req.Method) {
		ecritErreurMCP(w, http.StatusNotFound, req.ID, codeMethodeInconnue,
			"méthode inconnue : "+req.Method, nil)
		return
	}
	s.toucheJeton(porteur)

	switch req.Method {
	case "server/discover":
		// MUST de la révision, et il manquait à la spec du vault : elle
		// n'énumérait que `tools/list`. C'est le branchement d'un vrai client
		// qui l'a sorti - Claude Code sonde `server/discover` en PREMIER, et
		// notre 404 le faisait retomber sur l'ère `initialize`, donc échouer.
		// Source: https://modelcontextprotocol.io/specification/2026-07-28/server/discover
		ecritResultatMCP(w, req.ID, map[string]any{
			"resultType":        "complete",
			"supportedVersions": []string{revisionMCP},
			// Seuls les outils. Ni `resources` ni `prompts` : ils ajouteraient
			// une seconde façon de lire la même chose, et ils sont hors
			// périmètre du jalon.
			"capabilities": map[string]any{"tools": map[string]any{}},
			"instructions": instructionsMCP(),
			"ttlMs":        ttlDiscoverMS,
			"cacheScope":   "private",
			"_meta": map[string]any{
				"io.modelcontextprotocol/serverInfo": map[string]any{
					"name": "vecu", "version": versionServeurMCP,
				},
			},
		})
	case "tools/list":
		ecritResultatMCP(w, req.ID, map[string]any{
			"resultType": "complete",
			// LE CATALOGUE DU PLAFOND : un jeton « lecture » ne se voit pas
			// proposer les outils d'écriture. La spec l'autorise explicitement
			// (« The set MAY vary by the authorization presented on the
			// request »), et c'est ce qui rend `cacheScope: private`
			// nécessaire plutôt que prudent.
			"tools":      catalogueMCP(porteur.NiveauMax),
			"ttlMs":      ttlToolsMS,
			"cacheScope": "private",
		})
	case "tools/call":
		s.appelleOutilMCP(w, req, porteur)
	}
}

// Pourquoi la garde Origin est un REFUS SEC, et pas une liste blanche.
//
// Première version : comparer l'Origin au `Host` de la requête. Elle ne
// protégeait de rien - dans un DNS rebinding, le navigateur dérive l'Origin de
// l'URL de l'attaquant ET envoie ce même nom en Host, donc la comparaison
// réussit toujours.
//
// Deuxième version : une liste blanche configurable. Mesurée inutile : `/mcp`
// n'émet aucun en-tête CORS, et un POST de JSON portant `Authorization`
// déclenche toujours un preflight. Ce preflight reçoit 405 sans
// `Access-Control-Allow-*`, donc le navigateur n'envoie jamais le POST. Une
// liste blanche ne pouvait admettre que des clients NON navigateurs qui
// envoient un Origin - exactement ceux pour qui la garde n'est pas faite. Un
// réglage de production qui ne débloque rien est pire qu'absent : il fait
// croire à une capacité.
//
// Donc : toute Origin PRÉSENTE est refusée. C'est conforme (« if the Origin
// header is present and invalid, servers MUST respond with 403 »), c'est la
// protection maximale contre le rebinding, et ça reste transparent pour nos
// clients, qui n'envoient pas cet en-tête. Admettre un navigateur un jour
// demandera d'ajouter CORS, et ce sera un chantier, pas une variable
// d'environnement.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http#security-endpoint

// ecartDEntetes rend le motif de l'écart, ou la chaîne vide si tout concorde.
//
// Trois en-têtes sont exigés. `Mcp-Name` seulement là où la spec le demande -
// `tools/call`, `resources/read`, `prompts/get` - et nous ne servons que le
// premier.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http#standard-request-headers
func ecartDEntetes(r *http.Request, req requeteMCP) string {
	// Les NOMS d'en-tête sont insensibles à la casse (`Header.Get` canonise
	// déjà) ; les VALEURS ne le sont pas, d'où des comparaisons strictes.
	version := r.Header.Get("MCP-Protocol-Version")
	if version == "" {
		return "en-tête MCP-Protocol-Version manquant"
	}
	if version != req.Params.version() {
		return "MCP-Protocol-Version (" + version + ") ne correspond pas à la révision du corps (" + req.Params.version() + ")"
	}
	methode := r.Header.Get("Mcp-Method")
	if methode == "" {
		return "en-tête Mcp-Method manquant"
	}
	if methode != req.Method {
		return "Mcp-Method (" + methode + ") ne correspond pas à la méthode du corps (" + req.Method + ")"
	}
	if req.Method != "tools/call" {
		// Présent sur une méthode QU'ON SERT et qui ne porte aucun nom dans son
		// corps : il n'y a rien à quoi le comparer, donc il ment par
		// construction, et un intermédiaire qui route dessus déciderait sur une
		// valeur que le serveur n'a jamais validée.
		//
		// Restreint aux méthodes servies, et c'est un correctif : la première
		// version refusait l'en-tête sur TOUTE méthode autre que `tools/call`,
		// donc sur `resources/read` et `prompts/get` - où la spec l'EXIGE
		// justement. Un client conforme recevait -32020 là où le contrat promet
		// 404 / -32601. Sur une méthode qu'on ne sert pas, on ne juge pas ses
		// en-têtes : on répond « inconnue ».
		if sansNom[req.Method] && r.Header.Get("Mcp-Name") != "" {
			return "Mcp-Name est présent sur " + req.Method + ", qui ne porte pas de nom dans son corps"
		}
		return ""
	}
	nom, err := decodeSentinelle(r.Header.Get("Mcp-Name"))
	if err != nil {
		return "Mcp-Name : " + err.Error()
	}
	if nom == "" {
		return "en-tête Mcp-Name manquant"
	}
	if nom != req.Params.Name {
		return "Mcp-Name (" + nom + ") ne correspond pas au nom du corps (" + req.Params.Name + ")"
	}
	return ""
}

// decodeSentinelle décode la forme `=?base64?…?=`, que le client DOIT utiliser
// quand la valeur ne tient pas en ASCII visible - et aussi quand une valeur
// ASCII ressemble par malchance à la sentinelle. Le serveur DOIT décoder avant
// de comparer, sans quoi tout nom accentué serait refusé à tort.
//
// N'est appliquée qu'à `Mcp-Name` (et, le jour venu, à `Mcp-Param-{Nom}`) :
// c'est la portée exacte que la spec donne à la sentinelle. `Mcp-Method` et
// `MCP-Protocol-Version` portent des valeurs ASCII par construction et se
// comparent brutes.
//
// Alphabet standard avec remplissage : les exemples de la spec contiennent `/`
// et `=`, donc ce n'est pas la variante URL.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http#value-encoding
func decodeSentinelle(v string) (string, error) {
	const prefixe, suffixe = "=?base64?", "?="
	if !strings.HasPrefix(v, prefixe) || !strings.HasSuffix(v, suffixe) {
		return v, nil // valeur en clair, rendue telle quelle
	}
	brut := strings.TrimSuffix(strings.TrimPrefix(v, prefixe), suffixe)
	dec, err := base64.StdEncoding.DecodeString(brut)
	if err != nil {
		return "", errors.New("valeur base64 illisible")
	}
	return string(dec), nil
}

// borne tronque une valeur venue du client avant de la journaliser.
func borne(v string) string {
	const max = 120
	if len(v) <= max {
		return v
	}
	// Découpe sur les RUNES : couper sur les octets fabriquerait un rune
	// tronqué en fin de ligne de journal, donc du texte invalide dans le seul
	// canal dont on dispose pour enquêter.
	return string([]rune(v)[:min(max, len([]rune(v)))]) + "…(tronqué)"
}

// methodeServie : les seules méthodes de cette surface. `server/discover` est un
// MUST de la révision ; les deux autres sont le jalon. Tout le reste - y compris
// `resources/*` et `prompts/*`, délibérément hors périmètre - est inconnu.
func methodeServie(m string) bool {
	switch m {
	case "server/discover", "tools/list", "tools/call":
		return true
	}
	return false
}

// sansNom : parmi les méthodes servies, celles dont le corps ne porte ni `name`
// ni `uri`, donc pour lesquelles un `Mcp-Name` n'a rien à refléter.
var sansNom = map[string]bool{"server/discover": true, "tools/list": true}

// premierOctetJSON rend le premier octet non blanc, ou 0. Sert à distinguer un
// objet d'un lot ou d'un scalaire sans redécoder.
func premierOctetJSON(b []byte) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c
		}
	}
	return 0
}

// idJSONRPCValide : String, Number ou Null, et rien d'autre (JSON-RPC 2.0 §4).
func idJSONRPCValide(id json.RawMessage) bool {
	c := premierOctetJSON(id)
	return c == 0 || c == '"' || c == '-' || (c >= '0' && c <= '9') || c == 'n'
}

// toucheJeton avance `dernier_usage`, best-effort : un échec ici ne refuse pas
// un appel par ailleurs valide. Appelé sur les seuls chemins acceptés.
func (s *Server) toucheJeton(porteur *db.PorteurMCP) {
	if err := s.DB.ToucheJetonMCP(porteur.JetonID); err != nil {
		s.logf("mcp : dernier_usage non mis à jour (jeton %d) : %v", porteur.JetonID, err)
	}
}
