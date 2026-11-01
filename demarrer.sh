#!/usr/bin/env bash
# Prépare la machine de la formation et y lance Ruée, avec ce que la machine peut porter.
#   ./demarrer.sh            vérifie la machine, installe les outils, lance Ruée sur le poste (Docker Compose)
#   ./demarrer.sh cluster    crée le cluster Kubernetes adapté à la machine et y installe Ruée
# Le profil (poste, léger, complet) se choisit d'après la mémoire : voir environnement/machine.sh.
# Le dépôt se complète au fil de la formation : après chaque « git pull », relancer la commande indiquée.
set -euo pipefail
cd "$(dirname "$0")"
source environnement/machine.sh
resume_machine
if [ "$docker_etat" != ok ]; then
  conseil_docker
  exit 1
fi
manquants=$(for c in git curl tar ssh-keygen; do command -v "$c" >/dev/null || echo "$c"; done | tr '\n' ' ')
if [ -n "$manquants" ]; then
  echo "Commandes du système absentes : $manquants(sur Ubuntu : sudo apt install -y git curl tar openssh-client)"
  exit 1
fi
environnement/outils.sh

# Les images de base, téléchargées d'avance : les étapes suivantes n'attendent plus le réseau
noeud=kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
avance=(golang:1.27-alpine gcr.io/distroless/static-debian13:nonroot postgres:18-alpine valkey/valkey:9.0-alpine
  nginx:1.31-alpine axllent/mailpit:v1.33.0)
[ "$profil" = poste ] || avance+=("$noeud" registry:3.1.2)

case "${1:-poste}" in
  poste)
    echo "Téléchargement des images de base (quelques minutes la première fois)…"
    for image in "${avance[@]}"; do docker pull -q "$image" >/dev/null; done
    if [ -f ruee/poste.sh ]; then
      (cd ruee && go mod download)   # les bibliothèques du code, pour le test de charge (go run ./cmd/charge)
      ruee/poste.sh demarrer
    else
      echo "La machine est prête. Le code de Ruée arrive dans la journée : git pull, puis ./demarrer.sh"
    fi
    ;;
  cluster)
    if [ "$profil" = poste ]; then
      echo "Pas assez de mémoire pour un cluster ici : les démonstrations Kubernetes se suivent sur l'écran du formateur."
      exit 0
    fi
    # Les nœuds kind partagent le noyau de la machine : on relève la limite de surveillance des fichiers, comme le
    # demande la documentation de kind (sinon « too many open files » et des pods qui redémarrent en boucle)
    if [ "$systeme" = Linux ] && [ "$(cat /proc/sys/fs/inotify/max_user_instances)" -lt 512 ]; then
      echo "Relève de la limite inotify du noyau (mot de passe administrateur demandé) :"
      sudo sysctl -q -w fs.inotify.max_user_watches=524288 fs.inotify.max_user_instances=512 ||
        echo "⚠️  limite non relevée : le cluster peut être instable"
    fi
    # Ruée sur le poste laisse la place au cluster : la mémoire, et le port 8025 des courriels, que les deux utilisent
    if [ -f ruee/compose.yaml ] && [ -n "$(cd ruee && docker compose ps -q)" ]; then
      ruee/poste.sh arreter
    fi
    # Un cluster mis en pause par ./arreter.sh repart tel qu'il était
    pause=$(docker ps -aq --filter label=io.x-k8s.kind.cluster=formation --filter status=exited)
    if [ -n "$pause" ]; then
      docker start $pause >/dev/null
      for c in kind-registry ruee-banque ruee-stades ruee-courriel; do docker start "$c" >/dev/null 2>&1 || true; done
      until kubectl get nodes >/dev/null 2>&1; do sleep 2; done
    fi
    PROFIL=$profil environnement/creer-cluster.sh
    if [ -f ruee/deploiement/installer.sh ]; then
      echo "Construction des images de Ruée et envoi au registre du cluster…"
      for brique in salle-attente vente paiement billets banque; do
        docker build -q --build-arg SERVICE=$brique --build-arg VERSION=1.0.0 -t localhost:5001/ruee/$brique:1.0.0 ruee >/dev/null
        docker push -q localhost:5001/ruee/$brique:1.0.0 >/dev/null
      done
      docker build -q --build-arg SERVICE=vente --build-arg VERSION=1.1.0 -t localhost:5001/ruee/vente:1.1.0 ruee >/dev/null
      docker push -q localhost:5001/ruee/vente:1.1.0 >/dev/null   # la version « verte » de la bascule du jour 2
      # Sur un réseau lent, la première installation peut dépasser les délais d'attente (la base télécharge
      # PostgreSQL, la vente l'attend) : on relance les pods restés bloqués, puis l'installation, qui reprend où elle en était
      if ! ruee/deploiement/installer.sh; then
        echo "Première installation lente : relance des pods restés bloqués, puis nouvelle vérification…"
        ./verifier.sh >/dev/null
        ruee/deploiement/installer.sh
      fi
    else
      echo "Le cluster est prêt. Ruée s'y installera plus tard : git pull, puis ./demarrer.sh cluster"
    fi
    ;;
  *)
    echo "usage : $0 [cluster]" >&2
    exit 2
    ;;
esac
./verifier.sh
