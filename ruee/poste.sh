#!/usr/bin/env bash
# Le poste du développeur : toute la plateforme Ruée sur un ordinateur, avec Docker Compose.
#   ./poste.sh demarrer    construit les images et lance tout : http://localhost:8000 (courriels : http://localhost:8025)
#   ./poste.sh arreter     arrête et supprime les conteneurs (la base de développement repart vide au prochain démarrage)
# Les secrets de développement sont tirés au hasard à chaque démarrage : ils ne sont écrits dans aucun fichier.
set -euo pipefail
cd "$(dirname "$0")"

hasard() { od -An -tx1 -N16 /dev/urandom | tr -d ' \n'; }

case "${1:-demarrer}" in
  demarrer)
    docker compose build
    eval "$(docker run --rm ruee/salle-attente:dev cles)"   # CLE_PRIVEE et CLE_PUBLIQUE du jeton d'entrée
    export CLE_PRIVEE CLE_PUBLIQUE
    export BASE_MOT_DE_PASSE="$(hasard)" BANQUE_CLE="$(hasard)"
    docker compose up -d --wait --force-recreate   # tout repart de zéro : nouveaux secrets, base et files vides
    docker compose ps --format 'table {{.Service}}\t{{.Status}}'
    echo "Ruée : http://localhost:8000   courriels : http://localhost:8025"
    ;;
  arreter)
    docker compose down
    ;;
  *)
    echo "usage : $0 demarrer | arreter" >&2
    exit 2
    ;;
esac
