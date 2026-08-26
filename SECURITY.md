# Politique de sécurité

Vécu est un serveur qui arbitre des droits d'accès sur les fichiers de plusieurs
personnes. Une faille y donne accès à des documents, donc elle se signale en
privé avant d'être publique.

## Signaler une faille

**Écrire à colin@dargent-pro.com**, avec « Vécu » dans l'objet.

N'ouvre pas d'issue publique et ne poste pas de démonstration tant que le
correctif n'est pas publié.

Ce qui aide à traiter vite :

- la version concernée (menu de la barre de menus, ou `vecu status --dir DIR`) ;
- la marche à suivre pour reproduire, aussi précise que possible ;
- ce que la faille permet d'obtenir, concrètement ;
- ton évaluation de la gravité, si tu en as une.

## Ce que tu peux attendre en retour

| Étape | Délai visé |
|---|---|
| Accusé de réception | 5 jours ouvrés |
| Première évaluation, avec la gravité retenue | 10 jours ouvrés |
| Correctif publié, pour une faille confirmée et exploitable | selon la gravité, et annoncé dans la réponse |

Ce sont des délais visés, pas un engagement contractuel. Le projet est développé
par une seule personne, sans astreinte. S'ils sont dépassés, une relance sur le
même fil est légitime.

**Pas de programme de récompense.** Aucune prime n'est versée. Toute personne qui
signale une faille est créditée dans les notes de la version qui la corrige, sauf
si elle préfère ne pas l'être.

## Versions couvertes

La **dernière version publiée** uniquement. Le projet n'a pas de branche de
maintenance et ne rétroporte pas de correctif.

## Ce qui est déjà connu

Les limites de sécurité connues et assumées sont écrites en clair dans la section
**Limites connues** du [README](README.md), avec la position à tenir en face.
Elles ne sont pas des découvertes, et un signalement qui les redit n'apprend rien.

Deux points structurels qui n'y changeront pas :

- **Vécu ne termine pas TLS.** Il parle HTTP en clair et se met derrière un proxy
  inverse. Exposer le port directement sur Internet met le mot de passe et le
  jeton d'appareil en clair sur le réseau. C'est un déploiement fautif, pas une
  faille du produit, et `docker-compose.yml` le lie par défaut sur `127.0.0.1`
  pour rendre l'erreur difficile à commettre par inadvertance.
- **Un administrateur voit tout.** Le modèle de droits protège les comptes les uns
  des autres, pas contre l'administrateur de l'instance, qui a par ailleurs accès
  au volume de données.

## Hors périmètre

- Les failles d'un composant tiers, à signaler à son projet. Signale-les ici
  seulement si Vécu en fait un usage qui aggrave l'exposition.
- Les rapports issus d'un scanner automatique, sans démonstration d'un impact
  réel.
- L'absence d'un durcissement qui n'est pas une faille : en-têtes manquants,
  version divulguée, algorithme jugé trop faible sans scénario d'exploitation.
- Le déni de service par saturation.
- Les instances de tiers. Écris à celui qui les héberge.
