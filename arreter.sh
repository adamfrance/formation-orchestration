#!/usr/bin/env bash
# Libère la mémoire de la machine de la formation.
#   ./arreter.sh         arrête Ruée sur le poste et met le cluster en pause (il garde ses données et ses images ;
#                        ./demarrer.sh cluster le fait repartir)
#   ./arreter.sh tout    supprime aussi le cluster, son registre et les systèmes extérieurs simulés
cd "$(dirname "$0")" || exit 1
source environnement/machine.sh
[ -f ruee/compose.yaml ] && (cd ruee && docker compose down)
noeuds=$(docker ps -q --filter label=io.x-k8s.kind.cluster=formation)
[ -z "$noeuds" ] || docker stop $noeuds >/dev/null
docker stop kind-registry ruee-banque ruee-stades ruee-courriel rancher >/dev/null 2>&1
if [ "${1:-}" = tout ]; then
  kind delete cluster --name formation
  kind delete cluster --name ruee-recette 2>/dev/null   # la démonstration OpenTofu, si elle n'a pas été détruite
  docker rm -f kind-registry ruee-recette-registre ruee-banque ruee-stades ruee-courriel rancher >/dev/null 2>&1
fi
echo "Arrêté."
