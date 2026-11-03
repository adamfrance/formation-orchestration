#!/usr/bin/env bash
# Rancher pour la démonstration du module 4 : un serveur Rancher dans un conteneur, qui prend en main le cluster de
# recette de Ruée (un cluster « importé » : Rancher le pilote sans l'avoir créé).
# Rancher dans Docker est réservé aux essais : en production, on l'installe avec Helm dans un petit cluster dédié, à
# trois nœuds, séparé des clusters qu'il pilote.
#   ./plateforme/rancher.sh demarrer   lance Rancher, importe le cluster, affiche l'adresse et le mot de passe
#   ./plateforme/rancher.sh arreter    arrête Rancher et retire son agent du cluster
set -euo pipefail
VERSION=v2.15.3
URL_POSTE=https://localhost:8443   # l'adresse de Rancher depuis le navigateur du poste

# Rancher sur le réseau Docker du cluster, à une adresse fixe : c'est par elle que l'agent installé dans le cluster
# le joindra (comme les systèmes extérieurs de installer.sh)
reseau=$(docker network inspect kind -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' | tr ' ' '\n' | grep -m1 '\.')
ip_rancher="$(echo "$reseau" | cut -d. -f1-2).255.20"

supervision() {   # supervision <0|1> : met Prometheus et Grafana en pause, ou les relance (par Helm, qui en reste maître)
  helm upgrade supervision prometheus-community/kube-prometheus-stack --version 92.16.1 -n supervision --reuse-values \
    --set prometheus.prometheusSpec.replicas="$1" --set grafana.replicas="$1" >/dev/null
}

api() {   # api <méthode> <chemin> [corps JSON] : un appel à l'API de Rancher, avec le jeton de session
  curl -sk -X "$1" "$URL_POSTE$2" -H "Authorization: Bearer ${jeton:-}" -H 'Content-Type: application/json' ${3:+-d "$3"}
}

case "${1:-}" in
  demarrer)
    # 1. De la place en mémoire : la supervision s'arrête le temps de la démonstration (Rancher prend 2 à 3 Go)
    supervision 0

    # 2. Le serveur Rancher. Le mot de passe de démarrage est tiré au hasard et n'existe que dans ce terminal
    demarrage=$(od -An -tx1 -N12 /dev/urandom | tr -d ' \n')
    docker rm -f rancher >/dev/null 2>&1 || true
    docker run -d --name rancher --network kind --ip "$ip_rancher" --restart unless-stopped --privileged \
      -p 127.0.0.1:8443:443 -e CATTLE_BOOTSTRAP_PASSWORD="$demarrage" rancher/rancher:$VERSION >/dev/null
    echo "Rancher démarre (2 à 3 minutes)…"
    until curl -sk -m 3 "$URL_POSTE/ping" 2>/dev/null | grep -q pong; do sleep 5; done

    # 3. La première connexion, faite par l'API : se connecter, changer le mot de passe, donner l'adresse du serveur
    until jeton=$(curl -sk "$URL_POSTE/v3-public/localProviders/local?action=login" -H 'Content-Type: application/json' \
        -d "{\"username\":\"admin\",\"password\":\"$demarrage\",\"responseType\":\"json\"}" | jq -er .token); do sleep 5; done
    mot_de_passe=$(od -An -tx1 -N12 /dev/urandom | tr -d ' \n')
    api POST /v3/users?action=changepassword "{\"currentPassword\":\"$demarrage\",\"newPassword\":\"$mot_de_passe\"}" >/dev/null
    api PUT /v3/settings/server-url "{\"name\":\"server-url\",\"value\":\"https://$ip_rancher\"}" >/dev/null

    # 4. L'import du cluster de recette : Rancher fournit un manifeste qui installe son agent dans le cluster
    id=$(api POST /v3/clusters '{"type":"cluster","name":"ruee-recette"}' | jq -r .id)
    until manifeste=$(api GET "/v3/clusterregistrationtokens?clusterId=$id" | jq -er '.data[0].manifestUrl'); do
      api POST /v3/clusterregistrationtokens "{\"type\":\"clusterRegistrationToken\",\"clusterId\":\"$id\"}" >/dev/null
      sleep 2
    done
    # Le manifeste se lit sur le poste (par localhost:8443) ; l'agent, lui, joindra Rancher par son adresse sur le réseau
    curl -sk "${manifeste/https:\/\/$ip_rancher/$URL_POSTE}" | kubectl apply -f -
    echo "Rancher : $URL_POSTE   compte : admin   mot de passe : $mot_de_passe"
    echo "Le cluster « ruee-recette » apparaît comme actif au bout d'une à deux minutes."
    ;;
  arreter)
    # D'abord les contrôles d'admission (webhooks) installés par Rancher : laissés en place, ils refuseraient ensuite
    # la création des espaces de noms et des limites, en appelant un service qui n'existe plus
    kubectl delete validatingwebhookconfiguration,mutatingwebhookconfiguration rancher.cattle.io --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete namespace cattle-system cattle-fleet-system cattle-impersonation-system --ignore-not-found --wait=false >/dev/null 2>&1 || true
    docker rm -f rancher >/dev/null 2>&1 || true
    supervision 1
    echo "Rancher arrêté, supervision relancée"
    ;;
  *)
    echo "usage : $0 demarrer | arreter" >&2
    exit 2
    ;;
esac
