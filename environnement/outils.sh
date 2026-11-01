#!/usr/bin/env bash
# Installe les outils de la formation, aux versions testées, dans ~/.local/bin : sans droits d'administrateur et sans
# toucher aux outils du système. Chaque fichier téléchargé est contrôlé par son empreinte SHA-256, relevée le
# 01/11/2026 sur la page de publication officielle de l'outil : un fichier modifié en route est refusé.
# Un outil déjà là, à la bonne version, n'est pas retéléchargé : le script se relance sans risque.
set -euo pipefail

if [ "$(uname -s)" != Linux ]; then
  echo "Ce script installe les versions Linux. Sur un Mac : brew install kind kubectl helm opentofu trivy jq go" >&2
  exit 1
fi
case "$(uname -m)" in   # chaque projet nomme les architectures à sa façon
  x86_64 | amd64) A=amd64 X=x86_64 T=64bit ;;
  aarch64 | arm64) A=arm64 X=aarch64 T=ARM64 ;;
  *) echo "architecture non prise en charge : $(uname -m)" >&2; exit 1 ;;
esac
BIN="$HOME/.local/bin"
PLUGINS="$HOME/.docker/cli-plugins"   # les extensions de la commande docker (compose, buildx)
mkdir -p "$BIN" "$PLUGINS"
export PATH="$BIN:$HOME/.local/go/bin:$PATH"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# telecharger <adresse> <empreinte amd64> <empreinte arm64> : le fichier arrive dans $tmp/f, ou le script s'arrête
telecharger() {
  local attendue=$2
  [ "$A" = arm64 ] && attendue=$3
  curl -fsSL --retry 3 -o "$tmp/f" "$1"
  if [ "$(sha256sum "$tmp/f" | cut -d' ' -f1)" != "$attendue" ]; then
    echo "empreinte incorrecte, fichier refusé : $1" >&2
    exit 1
  fi
}
# manque <version> <commande qui affiche la version…> : vrai si l'outil est absent ou à une autre version
manque() { local v=$1; shift; ! "$@" 2>/dev/null | grep -qF -- "$v"; }

if manque v0.33.0 kind version; then   # kind : un cluster Kubernetes dans des conteneurs Docker
  telecharger "https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-linux-$A" \
    aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d \
    20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2
  install -m 0755 "$tmp/f" "$BIN/kind" && echo "installé : kind 0.33.0"
fi
if manque v1.37.1 kubectl version --client; then   # kubectl : le client de Kubernetes
  telecharger "https://dl.k8s.io/release/v1.37.1/bin/linux/$A/kubectl" \
    65691ff77eb6fa44c908b77a1082c9f092c3b9733b5cefabec0d1104890e21a8 \
    ff749f4b78d9c4f1ec87307df9b50119ed819e2094aa9810cb9acffc3286c8c7
  install -m 0755 "$tmp/f" "$BIN/kubectl" && echo "installé : kubectl 1.37.1"
fi
if manque v4.3.1 helm version --short; then   # Helm : les logiciels tiers installés dans le cluster
  telecharger "https://get.helm.sh/helm-v4.3.1-linux-$A.tar.gz" \
    0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b \
    8cf8f66ecbc252ac8f7f06cca68aba88508ddfde3a6a34feb1678ad1694b4f88
  tar -xzf "$tmp/f" -C "$tmp" "linux-$A/helm"
  install -m 0755 "$tmp/linux-$A/helm" "$BIN/helm" && echo "installé : helm 4.3.1"
fi
if manque v1.13.2 tofu version; then   # OpenTofu : l'infrastructure décrite en code (jour 1)
  telecharger "https://github.com/opentofu/opentofu/releases/download/v1.13.2/tofu_1.13.2_linux_$A.tar.gz" \
    ac05d9952662c7db28dd57e93f0a68018837bfdf9342384ec0987977c16a9645 \
    88e2d9e3f94f97b76ab2faf55ade87578f56aed19c5e45fa139d70e994366d1f
  tar -xzf "$tmp/f" -C "$tmp" tofu
  install -m 0755 "$tmp/tofu" "$BIN/tofu" && echo "installé : tofu 1.13.2"
fi
if manque 0.76.1 trivy --version; then   # Trivy : l'analyse des images et des manifestes (jours 1 et 3)
  telecharger "https://github.com/aquasecurity/trivy/releases/download/v0.76.1/trivy_0.76.1_Linux-$T.tar.gz" \
    c7cbe3137fb8a48a17574bede3dd6beb16742d1e9772929940fc90082c39d9c0 \
    e174dff2a6dba5e3ae41f8d0537a6324405bd08132497340304c6b20e0f16a1f
  tar -xzf "$tmp/f" -C "$tmp" trivy
  install -m 0755 "$tmp/trivy" "$BIN/trivy" && echo "installé : trivy 0.76.1"
fi
if manque jq-1.7.1 jq --version; then   # jq : lire du JSON dans le terminal (démonstration Rancher)
  telecharger "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-linux-$A" \
    5942c9b0934e510ee61eb3e30273f1b3fe2590df93933a93d7c58b81d19c8ff5 \
    4dd2d8a0661df0b22f1bb9a1f9830f06b6f3b8f7d91211a1ef5d7c4f06a8b4a5
  install -m 0755 "$tmp/f" "$BIN/jq" && echo "installé : jq 1.7.1"
fi
if manque go1.26.4 go version; then   # Go : le test de charge (go run ./cmd/charge) et les tests du code
  telecharger "https://go.dev/dl/go1.26.4.linux-$A.tar.gz" \
    1153d3d50e0ac764b447adfe05c2bcf08e889d42a02e0fe0259bd47f6733ad7f \
    ef758ae7c6cf9267c9c0ef080b8965f453d89ab2d25d9eb22de4405925238768
  rm -rf "$HOME/.local/go"
  tar -xzf "$tmp/f" -C "$HOME/.local" && echo "installé : go 1.26.4"
fi

# Les extensions de Docker, seulement si elles manquent : celles du système restent en place
if ! docker compose version >/dev/null 2>&1; then
  telecharger "https://github.com/docker/compose/releases/download/v5.1.4/docker-compose-linux-$X" \
    33b208d7e76639db742fae84b966cc01dacae58ca3fc4dabbc907045aefdf0c4 \
    d4fb48b72857810314d3ee77123c89954101844efa4788031221f4c370495946
  install -m 0755 "$tmp/f" "$PLUGINS/docker-compose" && echo "installé : docker compose 5.1.4"
fi
if ! docker buildx version >/dev/null 2>&1; then   # BuildKit, le moteur de construction des images
  telecharger "https://github.com/docker/buildx/releases/download/v0.33.0/buildx-v0.33.0.linux-$A" \
    9426a15411f35f635afef3f5d3bae53155c3e30d26dee430cc968e13d34be49f \
    204dc28447d3bb48f42ed1ce5747e0885cd57e306506a39029311becdb1ef786
  install -m 0755 "$tmp/f" "$PLUGINS/docker-buildx" && echo "installé : docker buildx 0.33.0"
fi

# Les outils dans le chemin des prochains terminaux : une ligne ajoutée une seule fois à ~/.bashrc (et à ~/.zshrc)
for f in "$HOME/.bashrc" "$HOME/.zshrc"; do
  [ -f "$f" ] || [ "$f" = "$HOME/.bashrc" ] || continue
  grep -qs "# formation-orchestration" "$f" ||
    echo 'export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$PATH"   # formation-orchestration' >>"$f"
done
echo "Outils prêts (ouvrir un nouveau terminal pour les avoir dans le chemin)"
