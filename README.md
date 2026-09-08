# Vécu

**Le dossier de contexte de ton activité, partagé avec ton équipe, et lisible
par n'importe quel outil d'IA.**

Tes notes, tes procédures, tes comptes rendus vivent dans des fichiers texte sur
ta machine. Vécu les synchronise entre plusieurs postes, avec des droits par
dossier décidés depuis une interface web, et un historique complet côté serveur.
Comme ce sont des fichiers ordinaires dans un dossier ordinaire, Claude Code,
Cursor, Codex ou n'importe quel outil qui lit le disque y accèdent directement,
sans copie, sans import et sans conversion.

C'est un logiciel **auto-hébergé** : tu fais tourner le serveur, les fichiers
restent chez toi. Git travaille sous le capot, côté serveur uniquement, sans que
personne ait à en connaître une commande.

**Pour qui.** Une petite équipe qui veut que le contexte de son activité cesse
de vivre dans le compte d'un fournisseur, et qui accepte d'héberger un serveur
pour ça.

## Ce qui n'est pas couvert

Avant d'aller plus loin, pour que tu décides en une minute.

- **macOS et Windows, pas Linux.** Le client et l'application de bureau
  tournent sur Mac et sur Windows ; le serveur tourne partout où Docker tourne.
  Deux réserves sur Windows : le binaire n'est **pas signé Authenticode**, donc
  SmartScreen affiche « Windows a protégé votre ordinateur » au premier
  lancement, et l'auto-update ne sert encore que macOS - un poste Windows
  s'installe et se met à jour à la main.
- **Contenu texte seulement.** Markdown, HTML, code. Les images, les PDF et tout
  autre binaire sont refusés visiblement et listés un par un, jamais corrompus,
  et ils ne circulent donc pas. Prévoir un autre canal pour eux.
- **Une seule organisation par instance.** Pas de cloisonnement multi-clients.
- **Pas d'édition depuis l'application.** L'interface web lit, montre
  l'historique et restaure. On écrit dans ses fichiers avec ses propres outils.
- **Pas de support.** Le projet est publié parce qu'il sert tous les jours à son
  auteur. Personne n'est de garde, aucun délai n'est promis, et une question
  peut rester sans réponse. Les rapports de bug restent les bienvenus.

## Prérequis

| Pour | Ce qu'il faut |
|---|---|
| Le serveur | Go 1.26 et git ≥ 2.38 dans le `PATH` (le rapprochement des versions passe par `git merge-tree`) |
| Un poste | Go 1.26, et **une chaîne de compilation C sur Mac uniquement** |

Le serveur n'a pas besoin de chaîne C : son SQLite est en Go pur. Elle n'est
requise que sur un poste Mac, où l'application de barre de menus passe par
`systray`, qui appelle Cocoa et exige donc `CGO_ENABLED=1`. Sur un Mac sans
Xcode, `xcode-select --install` suffit. Sur Windows, `systray` passe par des
appels système : `CGO_ENABLED=0` suffit, et Go seul construit tout l'arbre.

## Faire tourner le serveur

```bash
git clone https://github.com/ColinDargent/vecu-app.git
cd vecu-app
VECU_ADMIN_USER=colin VECU_ADMIN_PASSWORD='un-mot-de-passe-solide' go run ./server
```

`VECU_ADMIN_USER` et `VECU_ADMIN_PASSWORD` créent le premier compte
administrateur, au tout premier démarrage et seulement si la base est vide
(mot de passe : 8 caractères minimum). Ensuite ils ne servent plus.

L'interface d'administration est sur <http://localhost:8080/admin/>. L'état du
serveur, base et fichiers, vit dans le dossier `data` (`VECU_DATA` pour en
changer). **C'est la seule chose à sauvegarder.**

**Vécu ne termine pas TLS.** Le mot de passe et le jeton d'appareil passent dans
la connexion HTTP : pour joindre le serveur depuis Internet, mettre un proxy
inverse devant qui termine TLS (Caddy et Traefik obtiennent un certificat tout
seuls). Ouvrir le port en clair sur une adresse publique est le seul déploiement
franchement mauvais.

Variables d'environnement :

| Variable | Rôle | Défaut |
|---|---|---|
| `VECU_ADDR` | adresse d'écoute | `:8080` |
| `VECU_DATA` | dossier des données (SQLite + dépôt git bare) | `data` |
| `VECU_ADMIN_USER` / `VECU_ADMIN_PASSWORD` | premier administrateur, appliqué uniquement sur une base vide. Les changer ensuite ne réinitialise rien. | (aucun) |

### Avec Docker

Un `Dockerfile` et un `docker-compose.yml` sont fournis, et c'est par le
`Dockerfile` que tourne l'instance de production. Le fichier compose lie le port
sur `127.0.0.1` par défaut et documente les trois cas de déploiement en
commentaire.

```bash
cp env.exemple .env   # y mettre VECU_ADMIN_USER et VECU_ADMIN_PASSWORD
docker compose up -d
```

> Le parcours `docker compose up` sur une machine neuve n'est pas encore passé
> par la recette de test décrite plus bas. En attendant, `go run ./server`
> ci-dessus est le chemin vérifié.

## Installer un poste

Trois étapes, une seule fois par poste.

**1. Construire et installer le client.**

```bash
go build -o /usr/local/bin/vecu ./client
```

**2. Raccorder le poste au serveur.**

```bash
vecu setup
```

L'assistant demande l'adresse du serveur, le compte, un dossier local, puis fait
la première synchronisation. Tout est scriptable :

```bash
vecu setup --server https://vecu.example.com --user achille --dir ~/second-cerveau
```

**3. Installer l'application de barre de menus.**

```bash
make build-app VERSION=dev
cp -R "dist/Vécu.app" /Applications/
open "/Applications/Vécu.app"
```

Sur Windows, il n'y a pas de bundle à emballer : la livraison est le binaire.

```bash
make build-exe VERSION=dev
```

`dist/vecu-app.exe` se copie sur le poste Windows et se lance. Au premier
lancement, SmartScreen affiche « Windows a protégé votre ordinateur » : passer
par « Informations complémentaires » puis « Exécuter quand même ». Faire
disparaître cet écran demande un certificat, pas une ligne de code.

L'application embarque le moteur de synchronisation et devient le processus
supervisé par le système - launchd sur macOS, le Planificateur de tâches sur
Windows : elle se relance au démarrage de la session et survit aux redémarrages.
C'est par elle que passe l'usage courant, une fois installée.

**Ensuite, plus aucune commande.** Les dossiers apparaissent et disparaissent
selon ce qui se décide dans l'interface web.

### Si tu as fait « Quitter » et que rien ne repart

Volontaire et normal. Le service est déclaré à launchd avec
`KeepAlive {SuccessfulExit=false}`, donc launchd le relance après un plantage et
**pas** après une sortie propre, ce qui est la condition pour que « Quitter »
quitte vraiment. Rouvrir l'application depuis le dossier Applications suffit.
Pour le relancer sans passer par le Finder :

```bash
launchctl kickstart -k gui/$(id -u)/fr.vecu.sync
```

Sur Windows, le pendant est la tâche `fr.vecu.sync` du Planificateur :

```
schtasks /Run /TN fr.vecu.sync
```

## Le modèle en cinq minutes

### Un espace, et où il vit sur ton disque

Un **espace** est un dossier partagé : `shared`, `clients`, `projets`. Il se crée
dans `/admin/espaces`, et se distribue depuis la même page (un clic par personne
et par espace).

**Chaque poste choisit où l'espace atterrit chez lui**, par un chemin absolu qui
lui est propre. Le même espace `shared` peut vivre dans `~/second-cerveau/shared`
chez l'un et dans `~/Documents/Équipe` chez l'autre. Le serveur ne le sait pas et
n'a pas à le savoir.

Deux conséquences qui surprennent si on ne les a pas lues :

- **Créer un dossier sur ton disque ne crée pas un espace.** Ça se décide dans
  l'interface. Tout ce qui n'est pas un espace monté est invisible pour Vécu :
  rien n'en est lu, poussé ni supprimé.
- **Déplacer un espace déjà monté est refusé.** Le faire à moitié laisserait les
  fichiers derrière, et le cycle suivant les lirait comme des suppressions.

Noms d'espace en ASCII (lettres non accentuées, chiffres, `. _ -`) et uniques à
la casse près. macOS normalise l'Unicode et ignore la casse : deux espaces qui ne
diffèrent que par un accent finiraient dans un seul dossier local, avec deux
périmètres de droits.

### Les droits

- Trois niveaux : `invisible`, `lecture`, `écriture`.
- Une règle pose un niveau sur un dossier ou un fichier. Le droit effectif d'un
  chemin est la règle de l'ancêtre le plus profond qui le couvre, à défaut le
  niveau par défaut du compte.
- Surcharge dans les deux sens : restreindre un sous-dossier d'une zone
  accessible, ou ouvrir un fichier précis dans une zone invisible.
- Évalué côté serveur à chaque requête. Un chemin hors droit répond 404,
  indistinguable d'un chemin qui n'existe pas.

### Les conflits

Deux écritures concurrentes sur le même fichier : l'original garde la version du
premier, celle du second est committée à côté sous
`nom (conflit AAAA-MM-JJ HHhMM - utilisateur).md`, synchronisée chez tout le monde
et listée dans l'interface. Une copie de conflit n'écrase jamais rien.
Ce mécanisme couvre les écritures concurrentes que le client détecte. Un cas
où il n'a pas produit de copie est décrit dans « Limites connues ».

### L'historique

Toute écriture est un commit. Chaque fichier a son historique dans l'interface
(qui, quand, suppression), avec restauration d'une version antérieure en un clic.
La restauration est un commit ordinaire, rien n'est réécrit. Une suppression
reste donc toujours restaurable, et ne ressuscite jamais d'elle-même.

### Ce qui n'est jamais synchronisé

Par défaut `.git`, `.vecu`, `.DS_Store` et `.obsidian/workspace*`, à n'importe
quelle profondeur. Un fichier `.vecuignore` à la racine ajoute des motifs, un par
ligne, appliqués au chemin complet ou à l'un de ses segments :

```
node_modules
*.png
shared/brouillons
```

### Importer un dossier existant

Créer l'espace dans l'interface, raccorder le poste, puis y déposer les fichiers :

```bash
vecu setup --server https://vecu.example.com --user colin --dir ~/second-cerveau
rsync -a --exclude .git /chemin/vers/mon-vault/ ~/second-cerveau/shared/
vecu sync --dir ~/second-cerveau
```

Les binaires restent en local, listés un par un avec leur motif, et la commande
sort en code 1 : un import partiel ne s'annonce jamais comme un succès.

## Limites connues

Ce qui est écrit plus haut dans « Ce qui n'est pas couvert » tient toujours. S'y
ajoute ce qui suit.

**Convergence de la synchronisation**

- **Un revert local sans copie de conflit a été observé une fois, et sa cause
  racine n'est pas établie.** Le 10 août 2026, sur l'installation de
  développement, un espace entier est revenu sur un poste à l'état du serveur,
  en emportant des éditions locales jamais poussées, et **sans** produire la
  copie de conflit que le mécanisme décrit plus haut garantit dans les autres
  cas. Le défaut voisin, qui produisait des copies en série dès que deux
  personnes travaillaient en même temps, a été trouvé et corrigé depuis : le
  résultat de l'envoi était ignoré, si bien qu'un contenu tout juste accepté par
  le serveur était repris pour une édition que personne n'avait reçue. Celui-ci
  reste ouvert, deux hypothèses ayant été éliminées au banc sans le reproduire.
  Ce qui est exposé est **l'édition locale pas encore poussée** : tout ce que le
  serveur a reçu vit dans son historique git et reste restaurable depuis
  l'interface. Tant que ce point est ouvert, ne traite pas un poste comme la
  seule copie d'un travail en cours.

**Accès et sécurité**

- **Les sessions web n'expirent pas, et le login n'est pas limité en débit.**
  À placer derrière un proxy de confiance. La position sur TLS est décrite dans
  la section serveur.
- **Un administrateur voit tout**, y compris ce que les droits masquent aux
  autres comptes.

**Fonctionnement**

- **Le premier raccordement d'un poste passe par le terminal.** `vecu setup` n'a
  pas d'équivalent dans l'application. C'est la seule fois.
- **Pas de pilotage à distance des postes** depuis l'interface : ni état des
  machines, ni pause, ni synchronisation forcée. Le seul levier est l'accès aux
  espaces.
- **Un changement d'accès resynchronise tout le périmètre** du compte concerné,
  le dépôt n'ayant qu'un seul historique.
- **Renommer un fichier vaut suppression puis ajout.** L'historique du nouveau
  nom repart de zéro.
- **Une seule instance de serveur par dépôt.** Pas de haute disponibilité.

## Développement

```bash
go build ./...
go test ./...
go run ./server   # écoute sur :8080, VECU_ADDR pour changer
```

La ligne de commande `vecu` est un **outil de développement et de diagnostic**.
`vecu status --dir DIR` rend l'état complet d'un poste : serveur, compte, version,
dernier cycle, fichiers par espace, et la liste de ce qui n'a pas pu être
synchronisé avec le motif. Les autres commandes (`sync`, `start`, `login`)
servent au même usage. Un seul processus par racine locale, protégé par un
verrou : arrêter le service avant un `vecu sync` manuel.

### Structure

- `server/` : le serveur (API HTTP JSON, interface web, SQLite, dépôt git bare)
  - `server/espaces/` : la notion d'espace, sa dérivation et la validation des noms
  - `server/perms/` : l'évaluation des droits
- `client/` : le binaire `vecu` (commandes et moteur)
  - `client/sync/` : le moteur de synchronisation, monté aussi par l'application
- `desktop/` : l'application de barre de menus, macOS et Windows
- `Dockerfile`, `docker-compose.yml` : le serveur auto-hébergé

### Comment ce dépôt est mis à jour

Vécu est développé dans un dépôt de travail privé. Ce dépôt public reçoit une
copie complète du code à chaque release, en un commit. Il porte donc toujours la
dernière version publiée, et jamais l'historique interne ni le travail en cours.
Ce que ça change pour une contribution est expliqué dans
[CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

**[AGPL-3.0](LICENSE)**, Copyright (C) 2026 Colin Dargent.

**La licence porte sur le code de Vécu, jamais sur les fichiers que tu
synchronises avec.** Tes notes, tes documents et tout ce que Vécu transporte
restent entièrement les tiens, sous les termes que tu veux. Aucune licence
logicielle ne peut s'appliquer aux données d'un utilisateur, et l'AGPL ne fait
pas exception.

Ce que l'AGPL demande concerne celui qui **modifie** Vécu et le **propose à
d'autres par le réseau** : il doit publier ses modifications sous la même
licence. Installer Vécu tel quel pour ton équipe, même en le rendant accessible
depuis Internet, ne demande rien de plus que de pouvoir pointer vers ce dépôt.

Une faille se signale en privé : [SECURITY.md](SECURITY.md).
