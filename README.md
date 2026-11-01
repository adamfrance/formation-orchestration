# État de l'art Orchestration : la machine de la formation

Ce dépôt prépare votre machine pour le séminaire « État de l'art Orchestration » : les outils, puis **Ruée**, la
billetterie qui sert de fil rouge, sur le poste du développeur (Docker Compose) et sur Kubernetes. Il se complète au
fil des trois jours : quand le formateur le dit, `git pull`, puis la commande qu'il indique.

## Démarrer

```bash
git clone https://github.com/adamfrance/formation-orchestration.git
cd formation-orchestration
./demarrer.sh
```

`./demarrer.sh` regarde la machine (processeurs, mémoire, disque, Docker), puis installe dans `~/.local/bin` les
outils qui manquent, aux versions de la formation. Il n'a pas besoin des droits d'administrateur, et chaque
téléchargement est contrôlé par son empreinte SHA-256. Enfin, il lance ce que la machine peut porter. La première
fois, comptez 5 à 15 minutes selon le réseau. Ouvrez ensuite un nouveau terminal : les outils y sont dans le chemin.

## Ce que la machine peut porter

Le profil se choisit tout seul, d'après la mémoire de la machine :

| Profil | Mémoire de la machine | Ce qui tourne |
|---|---|---|
| poste | moins de 3,5 Go | Ruée avec Docker Compose ; les démonstrations Kubernetes se suivent sur l'écran du formateur |
| léger | de 3,5 à 7,5 Go | en plus, un cluster Kubernetes d'un seul nœud, sans supervision |
| complet | 7,5 Go et plus | le cluster des démonstrations : trois nœuds, Prometheus et Grafana ; Rancher à partir de 11,5 Go |

Pour imposer un profil : `PROFIL=leger ./demarrer.sh cluster`.

## Les commandes

| Commande | Ce qu'elle fait |
|---|---|
| `./demarrer.sh` | Vérifie la machine, installe les outils, lance Ruée sur le poste : http://localhost:8000 |
| `./demarrer.sh cluster` | Crée le cluster adapté à la machine et y installe Ruée : http://localhost:8080 |
| `./verifier.sh` | Vérifie tout, une ligne par point (✅ ou ⚠️), et relance ce qui est resté bloqué |
| `./arreter.sh` | Libère la mémoire : arrête Ruée sur le poste et met le cluster en pause |
| `./arreter.sh tout` | Supprime aussi le cluster et son registre |

Les courriels envoyés par Ruée (les billets) se lisent sur http://localhost:8025.

## Prérequis

- **Linux** (Ubuntu, Debian ou équivalent), 2 processeurs, 15 Go libres sur le disque.
- **Docker**. S'il manque, `./demarrer.sh` donne les commandes pour l'installer (mot de passe administrateur demandé).
- **git**, **curl** et **ssh-keygen** (paquet `openssh-client`), présents sur la plupart des distributions.
- Sous **Windows** (non testé) : un terminal Ubuntu (WSL 2) où Docker répond, puis les mêmes commandes.

Machine sans navigateur, jointe par SSH : ajouter `-L 8000:localhost:8000 -L 8025:localhost:8025 -L 8080:localhost:8080`
à la commande `ssh` habituelle, puis ouvrir les mêmes adresses dans le navigateur de votre ordinateur.
