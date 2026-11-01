#!/usr/bin/env bash
# Crée (ou complète) l'environnement des démonstrations : registre local, cluster kind, métriques, passerelle HTTP,
# opérateurs (CloudNativePG, KEDA) et supervision (Prometheus, Grafana). Chaque étape peut être rejouée sans risque.
#   PROFIL=complet (par défaut)  trois nœuds et la supervision : le cluster des démonstrations
#   PROFIL=leger                 un seul nœud, sans supervision : pour une machine de 3,5 à 7,5 Go
set -euo pipefail
cd "$(dirname "$0")"
PROFIL="${PROFIL:-complet}"
config=kind-formation.yaml leger_eg=() leger_keda=()
if [ "$PROFIL" = leger ]; then   # un seul nœud de 2 processeurs : les composants réservent le minimum
  config=kind-leger.yaml leger_eg=(-f valeurs/leger-envoy-gateway.yaml) leger_keda=(-f valeurs/leger-keda.yaml)
fi
sur_le_plan_de_controle='{"spec":{"template":{"spec":{"nodeSelector":{"node-role.kubernetes.io/control-plane":""},"tolerations":[{"key":"node-role.kubernetes.io/control-plane","operator":"Exists","effect":"NoSchedule"}]}}}}'

# 1. Le registre local : localhost:5001 depuis le poste, kind-registry:5000 depuis le cluster
if [ "$(docker inspect -f '{{.State.Running}}' kind-registry 2>/dev/null || true)" != true ]; then
  docker run -d --restart=always -p 127.0.0.1:5001:5000 --network bridge --name kind-registry registry:3.1.2
fi

# 2. Le cluster (Kubernetes 1.37) : un plan de contrôle et deux nœuds de travail, ou un seul nœud en profil léger
if ! kind get clusters 2>/dev/null | grep -qx formation; then
  kind create cluster --config "$config" \
    --image kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
fi
# Au redémarrage de la machine, l'API du cluster met quelques dizaines de secondes à répondre : on l'attend (3 min au
# plus ; constaté le 01/11/2026 : sans cette attente, la commande suivante échoue juste après le réveil)
for _ in $(seq 90); do kubectl get --raw /readyz >/dev/null 2>&1 && break; sleep 2; done

# 3. Chaque nœud tire les images localhost:5001/… depuis le conteneur du registre
for noeud in $(kind get nodes --name formation); do
  docker exec "$noeud" mkdir -p /etc/containerd/certs.d/localhost:5001
  echo '[host."http://kind-registry:5000"]' |
    docker exec -i "$noeud" cp /dev/stdin /etc/containerd/certs.d/localhost:5001/hosts.toml
done
if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' kind-registry)" = null ]; then
  docker network connect kind kind-registry
fi

# 4. Les mesures de consommation (kubectl top, la montée en charge), sur le nœud du plan de contrôle
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.9.1/components.yaml
if ! kubectl -n kube-system get deployment metrics-server -o jsonpath='{.spec.template.spec.containers[0].args}' |
  grep -q kubelet-insecure-tls; then
  kubectl -n kube-system patch deployment metrics-server --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
fi
kubectl -n kube-system patch deployment metrics-server --type=merge -p "$sur_le_plan_de_controle"
if [ "$PROFIL" = leger ]; then   # profil léger : un seul serveur DNS, et le service des mesures réserve moins
  kubectl -n kube-system scale deployment coredns --replicas=1
  kubectl -n kube-system patch deployment metrics-server --type=json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/resources/requests/cpu","value":"20m"}]'
fi

# 5. La passerelle HTTP (Gateway API, implémentée par Envoy Gateway) : http://localhost:8080
helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm --version v1.9.3 \
  --namespace envoy-gateway-system --create-namespace -f valeurs/envoy-gateway.yaml ${leger_eg[@]+"${leger_eg[@]}"} --wait
kubectl apply -f passerelle.yaml
if [ "$PROFIL" = leger ]; then   # profil léger : un seul proxy Envoy au lieu de deux
  kubectl -n passerelle patch envoyproxy port-de-noeud --type=merge \
    -p '{"spec":{"provider":{"kubernetes":{"envoyDeployment":{"replicas":1}}}}}'
fi
kubectl -n passerelle wait gateway/commune --for=condition=Programmed --timeout=600s   # 10 min : le premier téléchargement d'Envoy

# 6. Les opérateurs : PostgreSQL (CloudNativePG) et la montée en charge sur événements (KEDA)
helm repo add cnpg https://cloudnative-pg.github.io/charts >/dev/null
helm repo add kedacore https://kedacore.github.io/charts >/dev/null
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null
helm repo update >/dev/null
helm upgrade --install cnpg cnpg/cloudnative-pg --version 0.29.1 -n cnpg-system --create-namespace \
  -f valeurs/systeme.yaml --wait
helm upgrade --install keda kedacore/keda --version 2.21.0 -n keda --create-namespace -f valeurs/systeme.yaml \
  ${leger_keda[@]+"${leger_keda[@]}"} --wait

# 7. La supervision (profil complet) ; le mot de passe de Grafana est tiré au hasard et rangé dans un Secret, jamais
# dans un fichier
if [ "$PROFIL" = complet ]; then
  kubectl create namespace supervision --dry-run=client -o yaml | kubectl apply -f -
  kubectl get secret grafana-admin -n supervision >/dev/null 2>&1 ||
    kubectl create secret generic grafana-admin -n supervision --from-literal=admin-user=admin \
      --from-literal=admin-password="$(od -An -tx1 -N12 /dev/urandom | tr -d ' \n')"
  helm upgrade --install supervision prometheus-community/kube-prometheus-stack --version 92.16.1 -n supervision \
    -f supervision.yaml --wait --timeout 10m
fi

kubectl get nodes
