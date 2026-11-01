#!/usr/bin/env bash
# Vérifie la machine de la formation, une ligne par point (✅ ou ⚠️) ; relance les pods restés bloqués
cd "$(dirname "$0")" || exit 1
source environnement/machine.sh
ok() { printf '✅ %s\n' "$1"; }
ko() { printf '⚠️  %s\n' "$1"; }
resume_machine

if [ "$docker_etat" = ok ]; then ok "Docker $(docker version -f '{{.Server.Version}}')"; else ko "Docker pas prêt : ./demarrer.sh dit quoi faire"; fi
for outil in "kind version" "kubectl version --client" "helm version --short" "tofu version" "trivy --version" "jq --version" "go version"; do
  if version=$($outil 2>/dev/null | head -1) && [ -n "$version" ]; then ok "$version"; else ko "${outil%% *} absent : ./demarrer.sh l'installe"; fi
done

# Ruée sur le poste (Docker Compose)
if [ -f ruee/compose.yaml ] && [ -n "$(cd ruee && docker compose ps -q 2>/dev/null)" ]; then
  if curl -fs -m 5 -o /dev/null http://localhost:8000/; then ok "Ruée sur le poste : http://localhost:8000 (courriels : http://localhost:8025)"; else ko "Ruée sur le poste ne répond pas sur http://localhost:8000"; fi
fi

# Le cluster
if kind get clusters 2>/dev/null | grep -qx formation; then
  attendus=$(kind get nodes --name formation | wc -l | tr -d ' ')
  prets=$(kubectl get nodes --no-headers 2>/dev/null | grep -c ' Ready ')
  if [ "$prets" = "$attendus" ]; then ok "cluster : $prets nœud(s) prêt(s)"; else ko "cluster : $prets nœud(s) prêt(s) sur $attendus (./demarrer.sh cluster le relance)"; fi
  if curl -fs -m 5 -o /dev/null http://localhost:5001/v2/; then ok "registre localhost:5001"; else ko "registre injoignable"; fi
  code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://localhost:8080/)   # 404 tant que Ruée n'est pas installée
  if [ "$code" != 000 ]; then ok "passerelle http://localhost:8080 (HTTP $code)"; else ko "passerelle injoignable sur http://localhost:8080"; fi
  if kubectl top nodes >/dev/null 2>&1; then ok "métriques (kubectl top)"; else ko "métriques pas encore disponibles (1 min après le démarrage)"; fi

  # Après une pause, un pod qui démarre avant le réseau du cluster échoue, et Kubernetes espace ensuite ses relances
  # (jusqu'à 5 minutes) : on le supprime, son Deployment en recrée aussitôt un neuf
  bloques=$(kubectl get pods -A --no-headers 2>/dev/null | awk '{split($3, n, "/")}
    $4 == "CrashLoopBackOff" || $4 == "Error" || ($4 == "Running" && n[1] != n[2] && $5 + 0 >= 3) {print $1 "/" $2}')
  for p in $bloques; do kubectl delete pod -n "${p%/*}" "${p#*/}" --wait=false >/dev/null && ko "pod relancé : $p"; done
  pas_prets=$(kubectl get pods -A --no-headers 2>/dev/null | awk '$4 != "Completed" {split($3, n, "/"); if (n[1] != n[2]) print}' | wc -l | tr -d ' ')
  if [ "$pas_prets" = 0 ]; then ok "tous les pods sont prêts"; else ko "$pas_prets pod(s) pas encore prêt(s) : relancer ./verifier.sh dans une minute"; fi
elif [ "$profil" != poste ]; then
  echo "Pas encore de cluster : ./demarrer.sh cluster le crée (profil $profil)"
fi

if [ "$systeme" = Linux ]; then
  libre=$(awk '/^MemAvailable:/ {print int($2 / 1024)}' /proc/meminfo)
  if [ "$libre" -gt 700 ]; then ok "mémoire disponible : $libre Mo"; else ko "mémoire disponible : $libre Mo (./arreter.sh libère la mémoire)"; fi
fi
