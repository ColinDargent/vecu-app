package api

// mcp_outils.go : le catalogue d'outils et leur invocation.
//
// TROIS OUTILS DE LECTURE, et une seule règle d'accès pour les trois :
// `db.FiltreMCP`, qui porte le droit du compte porteur ET le plafond du jeton.
// Aucun de ces outils ne touche `perms.Effective` directement, aucun ne
// consulte `IsAdmin` - un jeton MCP ne peut rien qu'un humain ne pourrait, et
// la zone `shared/skills/` reste privée même d'un admin, comme partout ailleurs.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/colindargent/vecu/server/db"
	"github.com/colindargent/vecu/server/perms"
	"github.com/colindargent/vecu/server/storage"
)

// outilMCP : une entrée de `tools/list`.
//
// Les NOMS sont en ASCII sans accent : la spec contraint le jeu de caractères
// (lettres, chiffres, `_`, `-`, `.`). Les descriptions, elles, sont en français
// - c'est un modèle qui les lit, pas un analyseur syntaxique.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/server/tools#tool-names
type outilMCP struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// Les plafonds de la recherche, nommés parce qu'ils se paieront.
//
// Il n'y a PAS D'INDEX : `chercher` parcourt les chemins visibles et lit leur
// contenu, donc une recherche est une lecture du vault entier (autour de 900
// fichiers aujourd'hui). Acceptable à cette taille, à revoir si le vault
// double. Le plafond de résultats borne la réponse, pas le parcours.
const (
	maxResultatsRecherche = 50
	maxExtraitRecherche   = 240 // octets autour de la première occurrence
	// La recherche lit par TRANCHES plutôt que d'un bloc. Le plafond de
	// résultats borne la réponse, pas le travail : sans tranches, tout le
	// périmètre visible serait chargé en mémoire avant qu'une seule
	// correspondance soit examinée. Découper borne le pic mémoire et laisse la
	// recherche s'arrêter tôt dès qu'elle a de quoi répondre.
	tailleTrancheRecherche = 200
)

// instructionsMCP : ce que `server/discover` dit au modèle.
//
// Dérivée du catalogue, jamais écrite à la main : tant qu'aucun outil n'existe,
// annoncer « les outils lisent le vault » dirait au modèle l'inverse exact de
// la garantie d'une slice inerte. Et un agent lit les instructions avant
// `tools/list`.
func instructionsMCP() string {
	if len(outilsMCP()) == 0 {
		return "Le second cerveau de Forward. Aucun outil n'est exposé pour l'instant : " +
			"ce serveur ne donne accès à aucun contenu."
	}
	return "Le second cerveau de Forward, un vault de notes en Markdown. Les outils " +
		"sont filtrés chemin par chemin par les droits du compte porteur du jeton : " +
		"un fichier que ce compte n'a pas le droit de lire n'apparaît dans aucun " +
		"résultat, et un chemin deviné ne le contourne pas. Les écritures passent " +
		"par le même chemin qu'un poste synchronisé : elles fusionnent avec ce qui " +
		"est déjà là, et redescendent chez tout le monde au cycle suivant."
}

// objet monte un schéma JSON d'objet. JSON Schema 2020-12 par défaut, ce qui est
// la valeur de repli de la spec quand `$schema` est absent.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/server/tools#tool
func objet(props map[string]any, requis ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(requis) > 0 {
		s["required"] = requis
	}
	return s
}

func chaine(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// outilsMCP : le catalogue, dans un ordre déterministe.
//
// La spec demande un ordre stable d'un appel à l'autre : un client met la liste
// en cache, et un ordre qui bouge invalide le cache pour rien.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/server/tools#capabilities
func outilsMCP() []outilMCP {
	return catalogueMCP(perms.Ecriture)
}

// catalogueMCP : les outils qu'un jeton de ce plafond peut réellement appeler.
//
// Un jeton « lecture » ne se voit PAS proposer les outils d'écriture. C'est ce
// que la spec autorise explicitement (« The set MAY vary by the authorization
// presented on the request »), et ça évite qu'un agent en lecture seule brûle
// des tours à appeler un outil qui le refusera. C'est aussi ce qui rend
// nécessaire le `cacheScope: private` de `tools/list`, au lieu d'une simple
// marge de sûreté.
func catalogueMCP(plafond perms.Level) []outilMCP {
	tous := []outilMCP{
		{
			Name:        "arborescence",
			Title:       "Arborescence",
			Description: "Liste les chemins de fichiers visibles, éventuellement sous un préfixe. Ne rend que ce que le compte porteur du jeton a le droit de lire.",
			InputSchema: objet(map[string]any{
				"chemin": chaine("Préfixe de dossier, par exemple « shared/projets ». Vide ou absent = tout le périmètre visible."),
			}),
		},
		{
			Name:        "lire_fichier",
			Title:       "Lire un fichier",
			Description: "Rend le contenu d'un fichier du vault. Un chemin non lisible par le compte porteur rend une erreur, qu'il ait été deviné ou non.",
			InputSchema: objet(map[string]any{
				"chemin": chaine("Chemin complet du fichier, par exemple « shared/tasks.md »."),
			}, "chemin"),
		},
		{
			Name:        "ecrire_fichier",
			Title:       "Écrire un fichier",
			Description: "Écrit ou remplace un fichier du vault. Le contenu est fusionné avec ce que le serveur porte déjà, exactement comme une écriture venue d'un poste. Exige le droit d'écriture sur le chemin, et un jeton dont le plafond est « ecriture ».",
			InputSchema: objet(map[string]any{
				"chemin":  chaine("Chemin complet du fichier, par exemple « knowledge/meetings/2026-08-31.md »."),
				"contenu": chaine("Le contenu complet du fichier. Il remplace l'existant ; il n'y a pas d'écriture partielle."),
			}, "chemin", "contenu"),
		},
		{
			Name:        "supprimer_fichier",
			Title:       "Supprimer un fichier",
			Description: "Supprime un fichier du vault. Exige le droit d'écriture sur le chemin, et un jeton dont le plafond est « ecriture ».",
			InputSchema: objet(map[string]any{
				"chemin": chaine("Chemin complet du fichier à supprimer."),
			}, "chemin"),
		},
		{
			Name:        "chercher",
			Title:       "Chercher",
			Description: "Cherche un texte dans les chemins et le contenu des fichiers visibles. Insensible à la casse. Le contenu d'un fichier non lisible n'est jamais parcouru.",
			InputSchema: objet(map[string]any{
				"motif":  chaine("Le texte à trouver."),
				"chemin": chaine("Préfixe de dossier pour restreindre la recherche. Vide ou absent = tout le périmètre visible."),
			}, "motif"),
		},
	}
	if plafond >= perms.Ecriture {
		return tous
	}
	var lecture []outilMCP
	for _, o := range tous {
		if !ecritureMCP[o.Name] {
			lecture = append(lecture, o)
		}
	}
	return lecture
}

// ecritureMCP : les outils que le plafond « lecture » retire.
var ecritureMCP = map[string]bool{"ecrire_fichier": true, "supprimer_fichier": true}

// appelleOutilMCP : `tools/call`.
//
// Un outil inconnu est une erreur de PROTOCOLE (-32602), pas un résultat en
// `isError` : le modèle ne peut pas s'en corriger, il a demandé quelque chose
// qui n'existe pas. Les refus de DROIT, eux, sont des résultats `isError` -
// ceux-là, un modèle peut les comprendre et changer de chemin.
// Source: https://modelcontextprotocol.io/specification/2026-07-28/server/tools#error-handling
func (s *Server) appelleOutilMCP(w http.ResponseWriter, req requeteMCP, porteur *db.PorteurMCP) {
	var args struct {
		Chemin string `json:"chemin"`
		Motif  string `json:"motif"`
		// POINTEUR, et c'est la seule façon de distinguer « argument absent »
		// de « chaîne vide ». Vider un fichier délibérément est légitime ;
		// l'omettre par accident ne doit PAS le vider. Mesuré en revue : un
		// appel sans `contenu` effaçait un fichier et rendait « Écrit »,
		// isError:false, sans copie de conflit ni la moindre trace. L'appelant
		// est un modèle, dont la génération peut être coupée.
		Contenu *string `json:"contenu"`
	}
	if err := argumentsVers(req, &args); err != nil {
		ecritErreurMCP(w, http.StatusOK, req.ID, codeParamsInvalides,
			"arguments illisibles : "+err.Error(), nil)
		return
	}

	// LE FILTRE, monté une fois par appel et partagé par les trois outils. Les
	// règles sont chargées ici, et nulle part ailleurs.
	filtre, err := s.DB.FiltreMCP(porteur)
	if err != nil {
		ecritErreurMCP(w, http.StatusOK, req.ID, codeErreurInterne, "erreur interne", nil)
		return
	}

	switch req.Params.Name {
	case "arborescence":
		s.outilArborescence(w, req, filtre, args.Chemin)
	case "lire_fichier":
		s.outilLireFichier(w, req, filtre, args.Chemin)
	case "chercher":
		s.outilChercher(w, req, filtre, args.Motif, args.Chemin)
	case "ecrire_fichier":
		s.outilEcrireFichier(w, req, porteur, filtre, args.Chemin, args.Contenu)
	case "supprimer_fichier":
		s.outilSupprimerFichier(w, req, porteur, filtre, args.Chemin)
	default:
		ecritErreurMCP(w, http.StatusOK, req.ID, codeParamsInvalides,
			"outil inconnu : "+req.Params.Name, nil)
	}
}

// cheminsVisibles : les chemins du vault que ce jeton peut lire, sous `prefixe`.
//
// LE FILTRE EST APPLIQUÉ CHEMIN PAR CHEMIN, jamais au dossier. C'est ce que
// DAR-164 a fermé côté web : l'arbre partait d'un `Store.List("")` non filtré,
// et un admin au périmètre restreint voyait des dossiers qu'il ne pouvait pas
// lire. La même erreur par cette porte-ci donnerait le même trou.
func (s *Server) cheminsVisibles(filtre func(string) perms.Level, prefixe string) ([]string, error) {
	tous, err := s.Store.List("")
	if err != nil {
		return nil, err
	}
	prefixe = perms.Canon(prefixe)
	var out []string
	for _, p := range tous {
		if !sousPrefixe(p, prefixe) {
			continue
		}
		if filtre(p) >= perms.Lecture {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// sousPrefixe : `p` est-il le préfixe lui-même, ou dessous ?
//
// Comparaison PAR SEGMENT et pas par chaîne : sans ça, le préfixe « clients/a »
// attraperait « clients/acme/secret.md », donc un dossier voisin dont le nom
// commence pareil.
func sousPrefixe(p, prefixe string) bool {
	if prefixe == "" {
		return true
	}
	return p == prefixe || strings.HasPrefix(p, prefixe+"/")
}

func (s *Server) outilArborescence(w http.ResponseWriter, req requeteMCP, filtre func(string) perms.Level, prefixe string) {
	visibles, err := s.cheminsVisibles(filtre, prefixe)
	if err != nil {
		ecritResultatMCP(w, req.ID, refusOutil("lecture du dépôt impossible"))
		return
	}
	if len(visibles) == 0 {
		// `isError: false`, ET c'est un choix qu'il faut justifier puisque
		// `lire_fichier` tranche l'inverse.
		//
		// Une liste vide n'est pas un REFUS, c'est un résultat : ici on ne
		// refuse rien, on répond qu'il n'y a rien à voir. Surtout, « vide » et
		// « pas le droit » se disent PAREIL, délibérément : les distinguer
		// ferait de l'arborescence un détecteur de dossiers, et une fuite
		// d'existence compte autant qu'une fuite de contenu.
		//
		// `lire_fichier` diverge parce que sa question est différente : on lui a
		// demandé UN fichier nommé, et ne pas le rendre est bien un échec de
		// l'appel. Là aussi ses deux causes de refus se disent pareil.
		ecritResultatMCP(w, req.ID, resultatOutil("Aucun fichier visible sous ce chemin."))
		return
	}
	ecritResultatMCP(w, req.ID, resultatOutil(strings.Join(visibles, "\n")))
}

func (s *Server) outilLireFichier(w http.ResponseWriter, req requeteMCP, filtre func(string) perms.Level, chemin string) {
	chemin = perms.Canon(chemin)
	if chemin == "" {
		ecritResultatMCP(w, req.ID, refusOutil("argument « chemin » manquant."))
		return
	}
	// LE DROIT AVANT LA LECTURE, et le même refus dans les deux cas. Un chemin
	// deviné hors périmètre ne se distingue pas d'un chemin qui n'existe pas :
	// deux messages différents feraient de cet outil un détecteur d'existence.
	if filtre(chemin) < perms.Lecture {
		ecritResultatMCP(w, req.ID, refusOutil("Aucun fichier lisible à ce chemin : "+chemin))
		return
	}
	contenu, err := s.Store.Read(chemin, "")
	if err != nil {
		ecritResultatMCP(w, req.ID, refusOutil("Aucun fichier lisible à ce chemin : "+chemin))
		return
	}
	ecritResultatMCP(w, req.ID, resultatOutil(contenu))
}

func (s *Server) outilChercher(w http.ResponseWriter, req requeteMCP, filtre func(string) perms.Level, motif, prefixe string) {
	if motif == "" {
		ecritResultatMCP(w, req.ID, refusOutil("argument « motif » manquant."))
		return
	}
	// Les chemins visibles D'ABORD : le contenu d'un fichier invisible n'est
	// jamais lu, donc jamais mis en correspondance, donc jamais cité dans un
	// extrait. C'est le cas que la spec désigne comme celui qu'on rate - un
	// motif qui correspond au contenu d'un fichier qu'on n'a pas le droit de
	// voir ne doit rien rendre.
	visibles, err := s.cheminsVisibles(filtre, prefixe)
	if err != nil {
		ecritResultatMCP(w, req.ID, refusOutil("lecture du dépôt impossible"))
		return
	}
	// LECTURE PAR LOT, et c'est une mesure qui l'impose. `Store.Read` lance un
	// `git cat-file` par fichier : mesuré le 31/08 sur 900 notes, ~10 ms par
	// fichier, donc **9,4 s** pour une recherche qui doit parcourir tout le
	// périmètre visible - au-delà du délai d'attente de la plupart des clients
	// MCP, donc un outil inutilisable. La spec supposait « acceptable à cette
	// taille » ; la mesure dit le contraire, et c'est bien pour ça qu'elle la
	// demandait dès cette slice.
	//
	// On ne demande QUE les chemins visibles : le contenu d'un fichier que ce
	// jeton ne peut pas lire n'est jamais chargé, donc jamais mis en
	// correspondance. Filtrer les résultats après coup irait aussi vite et
	// serait fragile - un défaut du filtre fuirait au lieu de ne rien rendre.
	// RECHERCHE INSENSIBLE À LA CASSE PAR EXPRESSION RÉGULIÈRE, et c'est un
	// correctif de PANIQUE. La première version faisait
	// `strings.Index(strings.ToLower(contenu), motif)` puis découpait le
	// contenu ORIGINAL à cet index : or `ToLower` peut ALLONGER une chaîne en
	// octets (« Ⱥ » U+023A, 2 octets, devient « ⱥ » U+2C65, 3 octets). L'index
	// sortait alors de l'original, et le serveur paniquait - un seul fichier de
	// ce genre dans le périmètre lisible cassait `chercher` pour tout jeton qui
	// le voit.
	//
	// `(?i)` fait le repli de casse Unicode et rend un index dans la chaîne
	// D'ORIGINE, ce qui supprime la classe d'erreur au lieu de la déplacer.
	// `QuoteMeta` parce que le motif vient du modèle : il est cherché
	// littéralement, jamais interprété comme une expression.
	// Source: https://pkg.go.dev/regexp/syntax
	re, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(motif))
	if err != nil {
		ecritResultatMCP(w, req.ID, refusOutil("motif de recherche invalide."))
		return
	}
	var lignes []string
	tronque := false
	for debut := 0; debut < len(visibles); debut += tailleTrancheRecherche {
		if len(lignes) >= maxResultatsRecherche {
			tronque = true
			break
		}
		tranche := visibles[debut:min(len(visibles), debut+tailleTrancheRecherche)]
		contenus, err := s.Store.ReadBatch(tranche, "")
		if err != nil {
			ecritResultatMCP(w, req.ID, refusOutil("lecture du dépôt impossible"))
			return
		}
		for _, p := range tranche {
			if len(lignes) >= maxResultatsRecherche {
				tronque = true
				break
			}
			if re.MatchString(p) {
				lignes = append(lignes, p+" — (le chemin correspond)")
				continue
			}
			contenu, present := contenus[p]
			if !present {
				continue // listé mais absent du lot : disparu entre les deux
			}
			bornes := re.FindStringIndex(contenu)
			if bornes == nil {
				continue
			}
			lignes = append(lignes, p+" — "+extrait(contenu, bornes[0], bornes[1]))
		}
	}
	if len(lignes) == 0 {
		ecritResultatMCP(w, req.ID, resultatOutil("Aucun résultat."))
		return
	}
	texte := strings.Join(lignes, "\n")
	if tronque {
		texte += fmt.Sprintf("\n\n(%d premiers résultats ; affinez le motif ou le chemin pour voir la suite)", maxResultatsRecherche)
	}
	ecritResultatMCP(w, req.ID, resultatOutil(texte))
}

// extrait rend le voisinage de l'occurrence, sur une ligne.
//
// `a` et `b` sont des bornes en OCTETS dans `contenu`, telles que les rend
// `regexp.FindStringIndex`. La fenêtre est ensuite élargie puis recalée sur des
// frontières de runes : couper au milieu d'un caractère accentué fabriquerait
// un « ￼ » dans le seul rendu que le modèle lira, et le vault est en français.
func extrait(contenu string, a, b int) string {
	marge := (maxExtraitRecherche - (b - a)) / 2
	if marge < 0 {
		marge = 0
	}
	debut := reculeSurUneRune(contenu, max(0, a-marge))
	fin := avanceSurUneRune(contenu, min(len(contenu), b+marge))
	bout := nettoie(contenu[debut:fin])
	if debut > 0 {
		bout = "…" + bout
	}
	if fin < len(contenu) {
		bout += "…"
	}
	return bout
}

// reculeSurUneRune / avanceSurUneRune ramènent un offset d'octet sur la
// frontière de rune la plus proche.
func reculeSurUneRune(s string, i int) int {
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

func avanceSurUneRune(s string, i int) int {
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// nettoie aplatit l'extrait sur une ligne. Tous les blancs de contrôle, pas
// seulement `\n` : un fichier en CRLF ou un octet nul casserait autrement la
// promesse « sur une ligne » du rendu.
func nettoie(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	espace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == 0 {
			espace = true
			continue
		}
		if espace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		espace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// resultatOutil : un résultat d'outil réussi, en un seul bloc de texte.
func resultatOutil(texte string) map[string]any {
	return map[string]any{
		"resultType": "complete",
		"content":    []any{map[string]any{"type": "text", "text": texte}},
		"isError":    false,
	}
}

// refusOutil : un refus, de droit ou d'argument.
//
// `isError: true` dans un RÉSULTAT, et pas une erreur JSON-RPC. La spec réserve
// les erreurs de protocole aux requêtes malformées et aux outils inconnus ; un
// refus d'accès est une issue d'exécution, que le modèle doit pouvoir lire et
// sur laquelle il peut se corriger.
func refusOutil(texte string) map[string]any {
	return map[string]any{
		"resultType": "complete",
		"content":    []any{map[string]any{"type": "text", "text": texte}},
		"isError":    true,
	}
}

// argumentsVers décode `params.arguments` vers `cible`.
func argumentsVers(req requeteMCP, cible any) error {
	if len(req.Params.Arguments) == 0 {
		return nil // aucun argument fourni : les champs restent à leur zéro
	}
	return json.Unmarshal(req.Params.Arguments, cible)
}

// outilEcrireFichier et outilSupprimerFichier passent par la MÊME fonction que
// le PUT et le DELETE de l'API - `s.ecrisFichier` et `s.supprimeFichier`, dans
// `ecriture.go`. Mêmes droits, même fusion, même journal, mêmes copies de
// conflit serveur. Il n'y a pas de second chemin d'écriture, et c'est la
// propriété qui rend cette surface défendable.

func (s *Server) outilEcrireFichier(w http.ResponseWriter, req requeteMCP, porteur *db.PorteurMCP, filtre func(string) perms.Level, chemin string, contenu *string) {
	// PAS de `perms.Canon` ici, et c'est un correctif. La porte HTTP ne
	// canonicalise pas : `equipe/trail.md/` y est refusé par `validatePath`.
	// Canonicaliser côté MCP faisait diverger les deux portes sur un cas
	// mesuré - l'une refusait, l'autre écrivait dans `equipe/trail.md`. Le
	// chemin est jugé par la fonction partagée, pour les deux portes.
	if chemin == "" {
		ecritResultatMCP(w, req.ID, refusOutil("argument « chemin » manquant."))
		return
	}
	if contenu == nil {
		ecritResultatMCP(w, req.ID, refusOutil(
			"argument « contenu » manquant. Il est obligatoire : une écriture remplace "+
				"le fichier entier, donc l'omettre le viderait. Pour vider un fichier "+
				"délibérément, passer une chaîne vide."))
		return
	}
	// Pas d'empreinte attendue : la surface MCP écrit « comme un pair
	// parfaitement à jour » et ne tient aucun état local, donc elle n'a rien à
	// opposer au serveur. La garde d'écho ne la concerne pas.
	res, err := s.ecrisFichier(ecrivainMCP(porteur, filtre), chemin, *contenu, storage.BaseHeadCourant, "")
	if err != nil {
		ecritResultatMCP(w, req.ID, refusOutil(messageDeRefus(err, chemin, porteur)))
		return
	}
	// LA TRACE, et elle manquait. `ecriture.go` promet « même journal » que la
	// porte HTTP - or il n'y a PAS de journal d'écriture côté serveur, et une
	// écriture d'agent était indiscernable d'une écriture humaine : même auteur
	// de commit (le compte), aucun signal du jeton. Après coup, « qu'a écrit la
	// routine cloud ? » n'avait pas de réponse.
	//
	// Le libellé du jeton, pas son identifiant : c'est ce qu'un humain lit.
	s.journaliseEcritureMCP(porteur, "écrit", chemin, res)
	ecritResultatMCP(w, req.ID, resultatOutil(compteRenduEcriture("Écrit", chemin, res)))
}

// journaliseEcritureMCP pose la seule trace qui relie une écriture à son jeton.
func (s *Server) journaliseEcritureMCP(porteur *db.PorteurMCP, verbe, chemin string, res writeResponse) {
	issue := "direct"
	switch {
	case res.Conflict:
		issue = "CONFLIT -> " + res.ConflictPath
	case res.Merged:
		issue = "fusionné"
	}
	s.logf("mcp %s : %s par le jeton %q (#%d, compte %q) — %s",
		verbe, chemin, borne(porteur.Libelle), porteur.JetonID, borne(porteur.Username()), issue)
}

func (s *Server) outilSupprimerFichier(w http.ResponseWriter, req requeteMCP, porteur *db.PorteurMCP, filtre func(string) perms.Level, chemin string) {
	// Pas de `Canon` ici non plus, pour la raison écrite dans `outilEcrireFichier`.
	if chemin == "" {
		ecritResultatMCP(w, req.ID, refusOutil("argument « chemin » manquant."))
		return
	}
	avant, err := s.Store.Head()
	if err != nil {
		ecritErreurMCP(w, http.StatusOK, req.ID, codeErreurInterne, "erreur interne", nil)
		return
	}
	res, err := s.supprimeFichier(ecrivainMCP(porteur, filtre), chemin, storage.BaseHeadCourant)
	if err != nil {
		ecritResultatMCP(w, req.ID, refusOutil(messageDeRefus(err, chemin, porteur)))
		return
	}
	// Le head N'A PAS BOUGÉ et rien n'est parti en copie : il n'y avait rien à
	// cet endroit. Dire « Supprimé » serait un mensonge au modèle, qui
	// enchaînerait en croyant avoir nettoyé quelque chose.
	//
	// Le VERDICT ne change pas pour autant - la porte HTTP rend 200 sur ce cas
	// depuis toujours, et les deux portes doivent concorder. Seul le compte
	// rendu est honnête. Aucune lecture supplémentaire : `base` est le head
	// qu'on a déclaré, on le compare à celui que la fusion rend.
	if res.Head == avant && !res.Conflict {
		s.logf("mcp suppression sans objet : %s par le jeton %q (#%d) — rien à ce chemin",
			chemin, borne(porteur.Libelle), porteur.JetonID)
		ecritResultatMCP(w, req.ID, resultatOutil(
			"Aucun fichier à ce chemin : "+chemin+". Rien n'a été supprimé."))
		return
	}
	s.journaliseEcritureMCP(porteur, "supprimé", chemin, res)
	ecritResultatMCP(w, req.ID, resultatOutil(compteRenduEcriture("Supprimé", chemin, res)))
}

// messageDeRefus traduit un refus de la porte partagée en phrase pour le modèle.
//
// APRÈS le refus, jamais à sa place. Une version antérieure contrôlait le
// plafond du jeton AVANT d'appeler la porte partagée, pour donner un meilleur
// message - et le résultat mesuré était qu'aucun test d'API n'exerçait plus le
// vrai contrôle : retirer le plafond de `db.FiltreMCP` laissait toute la suite
// verte. Un garde-fou déclaré redondant qui devient le seul chemin réellement
// testé est pire qu'absent. Ici, l'autorisation a UN seul chemin, et ceci n'en
// explique que l'issue.
//
// Le 404 garde le MÊME message que `lire_fichier` : un chemin qu'on n'a pas le
// droit de voir ne se distingue pas d'un chemin qui n'existe pas, sinon
// l'écriture devient un détecteur d'existence par la bande.
func messageDeRefus(err error, chemin string, porteur *db.PorteurMCP) string {
	status, msg := statutDe(err)
	switch status {
	case http.StatusNotFound:
		return "Aucun fichier lisible à ce chemin : " + chemin
	case http.StatusForbidden:
		if porteur.NiveauMax < perms.Ecriture {
			return "Écriture refusée : ce jeton MCP est plafonné en lecture seule, " +
				"quels que soient les droits du compte."
		}
		return "Écriture non autorisée sur ce chemin : " + chemin
	default:
		return msg
	}
}

// compteRenduEcriture dit ce que la fusion a fait, y compris quand elle a rangé
// la version du serveur dans une copie. Un agent qui écrit doit pouvoir le
// savoir : c'est la seule trace qu'il aura.
func compteRenduEcriture(verbe, chemin string, res writeResponse) string {
	switch {
	case res.Conflict:
		// À L'ENVERS dans la première version, et c'est le genre d'erreur qui
		// enverrait un agent dans le mur : `WriteMerge` garde la version du
		// SERVEUR au nom canonique et range LA NÔTRE dans la copie (modèle
		// Dropbox, écrit dans `storage.go`). Dire l'inverse ferait croire au
		// modèle que son contenu est en place.
		return "Le contenu n'a PAS été écrit à " + chemin + " : le serveur portait une " +
			"version divergente, qu'il a gardée. Ce qui a été envoyé est déposé à côté, " +
			"dans « " + res.ConflictPath + " ». Relire " + chemin + " puis réécrire si besoin."
	case res.Merged:
		return verbe + " : " + chemin + " — fusionné avec les modifications déjà présentes sur le serveur."
	default:
		return verbe + " : " + chemin + "."
	}
}
