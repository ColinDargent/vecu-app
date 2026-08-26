# Contribuer à Vécu

Merci de l'intérêt. Ce fichier dit trois choses : comment le projet est développé,
ce qui est utile aujourd'hui, et l'accord de contribution qu'une pull request
implique.

## Comment ce dépôt est développé

Vécu est construit dans un dépôt de travail privé. **Ce dépôt public reçoit une
copie complète du code à chaque release**, en un commit, sans l'historique
interne. Il porte donc toujours la dernière version publiée et jamais le travail
en cours.

La conséquence pratique compte pour qui veut contribuer : une pull request n'est
pas fusionnée telle quelle. Elle est relue ici, puis reportée dans le dépôt de
travail, et elle réapparaît dans la release suivante. Ta contribution garde son
attribution dans le message de commit, et le fil de la pull request reste la
trace publique de la discussion.

C'est un modèle qui convient au stade actuel du projet, développé par une seule
personne. Il changera le jour où il gênera plus qu'il ne sert.

## Ce qui aide vraiment aujourd'hui

Par ordre décroissant d'utilité.

1. **Un rapport de bug avec de quoi le reproduire.** La version (menu de la barre
   de menus, ou `vecu status --dir DIR`), le système, la séquence exacte, ce qui
   était attendu et ce qui s'est produit. Les journaux si tu les as.
2. **Un retour d'installation.** Chaque endroit du README où tu as dû deviner,
   ouvrir un autre fichier ou chercher ailleurs est un défaut. Le dire coûte une
   issue et vaut plus qu'un correctif.
3. **Un correctif ciblé**, avec le test qui échouait avant.
4. **Le portage Linux ou Windows.** Aujourd'hui macOS seulement, et l'essentiel
   de ce qui manque est le service de démarrage et le bundle applicatif.

Avant d'ouvrir une pull request qui ajoute une fonction, ouvre une issue.
Le projet a un périmètre volontairement étroit, décrit dans les limites connues
du README, et une bonne fonction hors périmètre est refusée quand même.

## Faire tourner le projet

Prérequis, tests et build : la section « Développement » du README.

Deux règles de forme dans ce dépôt :

- **Tout le contenu français est accentué**, y compris sur les majuscules,
  y compris dans les commentaires, les messages d'erreur et l'interface.
- **Un commentaire dit pourquoi**, pas ce que le code fait déjà lisiblement.

`go test ./...` doit passer avant toute pull request.

## Accord de contribution (CLA)

**En ouvrant une pull request sur ce dépôt, tu déclares et acceptes ce qui suit.**

1. Tu es l'auteur de ta contribution, ou tu as le droit de la soumettre sous ces
   termes. Si ton employeur détient des droits sur ton travail, tu as son
   autorisation.
2. Tu accordes à Colin Dargent une licence **perpétuelle, mondiale, non
   exclusive, irrévocable et gratuite** de reproduire, modifier, distribuer et
   exploiter ta contribution, **y compris sous d'autres termes de licence** que
   l'AGPL-3.0, et d'accorder ces mêmes droits à des tiers.
3. Tu conserves l'intégralité de tes droits d'auteur sur ta contribution et tu
   restes libre de l'utiliser ailleurs comme bon te semble.
4. Ta contribution est fournie en l'état, sans garantie d'aucune sorte.

**Pourquoi cet accord existe.** Vécu est publié sous AGPL-3.0. Le copyright est
aujourd'hui détenu par une seule personne, ce qui laisse ouverte la possibilité
d'accorder une exception commerciale à un client que le copyleft empêche
d'utiliser le produit. Cette possibilité disparaît dès la première contribution
externe acceptée sans accord de ce type, et elle disparaît sans que personne s'en
aperçoive : il faudrait ensuite retrouver et obtenir le consentement de chaque
contributeur.

Le point 2 est le seul qui vous demande quelque chose, et il est le prix de cette
possibilité. Si ces termes ne te conviennent pas, dis-le dans l'issue : un
rapport de bug détaillé n'emporte aucune cession et reste très utile.

## Sécurité

Une faille ne s'ouvre pas en issue publique. La marche à suivre est dans
[SECURITY.md](SECURITY.md).

## Licence

Toute contribution est publiée sous [AGPL-3.0](LICENSE), comme le reste du
dépôt.
