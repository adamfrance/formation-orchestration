#!/usr/bin/env bash
# Installe Ruée dans le cluster de recette, en une commande :
#   1. les espaces de noms des équipes ;
#   2. les secrets, tirés au hasard et rangés seulement dans le cluster (jamais dans un fichier) ;
#   3. les systèmes extérieurs, simulés par des conteneurs hors de Kubernetes : la banque, le serveur SFTP des
#      stades, le courriel ;
#   4. les Services qui donnent un nom, dans le cluster, à ces systèmes extérieurs ;
#   5. la plateforme elle-même (kubectl apply -k), puis l'attente que tout soit prêt.
# Prérequis : le cluster de la formation (code/environnement/creer-cluster.sh) et les images dans le registre local.
#   ./deploiement/installer.sh
set -euo pipefail
cd "$(dirname "$0")/.."

hasard() { od -An -tx1 -N16 /dev/urandom | tr -d ' \n'; }        # 32 caractères aléatoires
existe() { kubectl get "$@" >/dev/null 2>&1; }                    # la ressource existe-t-elle ?
lire() { kubectl get secret "$2" -n "$1" -o jsonpath="{.data.$3}" | base64 -d; }   # une valeur d'un Secret

# 1. Les espaces de noms
kubectl apply -f deploiement/base/espaces.yaml

# 2. Les secrets, créés une seule fois : un nouveau tirage casserait ce qui tourne déjà
if ! existe secret jeton -n salle-attente; then
  eval "$(docker run --rm localhost:5001/ruee/salle-attente:1.0.0 cles)"   # CLE_PRIVEE et CLE_PUBLIQUE
  kubectl create secret generic jeton -n salle-attente --from-literal=CLE_PRIVEE="$CLE_PRIVEE"
  kubectl create configmap jeton-public -n vente --from-literal=CLE_PUBLIQUE="$CLE_PUBLIQUE"   # publique : pas un secret
fi
existe secret banque -n paiement ||
  kubectl create secret generic banque -n paiement --from-literal=BANQUE_CLE="$(hasard)"
existe secret base-vente-lecture -n vente ||
  kubectl create secret generic base-vente-lecture -n vente --type=kubernetes.io/basic-auth \
    --from-literal=username=lecture --from-literal=password="$(hasard)"
existe secret stades -n vente ||
  kubectl create secret generic stades -n vente --from-literal=STADES_MOT_DE_PASSE="$(hasard)"

# 3. Les systèmes extérieurs : des conteneurs sur le réseau Docker du cluster (« kind »), hors de Kubernetes, chacun à
# une adresse fixe (en haut de la plage du réseau, que Docker n'attribue pas lui-même) : sans cela, un redémarrage
# leur donne d'autres adresses, et le cluster appelle dans le vide
reseau=$(docker network inspect kind -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' | tr ' ' '\n' | grep -m1 '\.')  # 172.18.0.0/16
prefixe=$(echo "$reseau" | cut -d. -f1-2)
ip_banque="$prefixe.255.10"; ip_stades="$prefixe.255.11"; ip_courriel="$prefixe.255.12"
lancer() {   # lancer <nom> <adresse> <options de docker run…> : seulement s'il ne tourne pas déjà à cette adresse
  local nom=$1 adresse=$2; shift 2
  if [ "$(docker inspect -f '{{.State.Running}} {{.NetworkSettings.Networks.kind.IPAddress}}' "$nom" 2>/dev/null)" != "true $adresse" ]; then
    docker rm -f "$nom" >/dev/null 2>&1 || true
    docker run -d --name "$nom" --network kind --ip "$adresse" --restart unless-stopped "$@" >/dev/null
  fi
}
lancer ruee-banque "$ip_banque" -e CLE_API="$(lire paiement banque BANQUE_CLE)" localhost:5001/ruee/banque:1.0.0
lancer ruee-stades "$ip_stades" -e USER_NAME=stades -e USER_PASSWORD="$(lire vente stades STADES_MOT_DE_PASSE)" \
  -e PASSWORD_ACCESS=true lscr.io/linuxserver/openssh-server:10.3_p1-r1-ls240
lancer ruee-courriel "$ip_courriel" -p 127.0.0.1:8025:8025 axllent/mailpit:v1.33.0   # courriels : http://localhost:8025

# L'empreinte du serveur SFTP des stades, relevée une fois : l'export refusera tout autre serveur
cle_publique=/config/ssh_host_keys/ssh_host_ed25519_key.pub
until docker exec ruee-stades test -f "$cle_publique"; do sleep 1; done
empreinte=$(docker exec ruee-stades cat "$cle_publique" | ssh-keygen -lf - | awk '{print $2}')
kubectl create configmap stades -n vente --dry-run=client -o yaml \
  --from-literal=STADES_ADRESSE=stades:2222 --from-literal=STADES_UTILISATEUR=stades \
  --from-literal=STADES_EMPREINTE="$empreinte" | kubectl apply -f -

# 4. Un nom dans le cluster pour chaque système extérieur : un Service sans sélecteur, et la liste de ses adresses
# (EndpointSlice) écrite à la main. Les pods appellent « banque:8080 » comme n'importe quel Service.
externe() {   # externe <espace> <nom> <port> <adresse>
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: $2
  namespace: $1
spec:
  ports:
    - { name: tcp, port: $3, targetPort: $3 }
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: $2-externe
  namespace: $1
  labels:
    kubernetes.io/service-name: $2
    endpointslice.kubernetes.io/managed-by: installer.ruee
addressType: IPv4
ports:
  - { name: tcp, port: $3, protocol: TCP }
endpoints:
  - addresses: ["$4"]
EOF
}
externe paiement banque 8080 "$ip_banque"
externe vente stades 2222 "$ip_stades"
externe vente courriel 1025 "$ip_courriel"

# 5. La plateforme, puis l'attente : la base d'abord (les autres en dépendent), puis chaque Deployment
kubectl apply -k deploiement/recette
kubectl wait cluster/base-vente -n vente --for=condition=Ready --timeout=600s   # 10 min : le premier téléchargement de PostgreSQL est long
kubectl rollout status statefulset/valkey-file -n salle-attente --timeout=180s
kubectl rollout status statefulset/valkey-billets -n vente --timeout=180s
for d in salle-attente/salle-attente vente/vente paiement/paiement; do
  kubectl rollout status deployment/"${d#*/}" -n "${d%/*}" --timeout=180s
done
echo "Ruée : http://localhost:8080   courriels : http://localhost:8025"
