# shellcheck shell=bash
# Ce que la machine sait faire (à sourcer) : système, processeurs, mémoire, disque, Docker, et le profil qui en découle.
#   poste    moins de 3,5 Go de mémoire ou un seul processeur : Ruée sur le poste (Docker Compose), rien de plus
#   leger    de 3,5 à 7,5 Go : en plus, un cluster Kubernetes d'un seul nœud, sans supervision
#   complet  à partir de 7,5 Go : le cluster des démonstrations (trois nœuds), avec Prometheus et Grafana
# Le profil peut être imposé : PROFIL=leger ./demarrer.sh cluster
export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$PATH"   # les outils installés par outils.sh passent avant ceux du système

systeme=$(uname -s)   # Linux, ou Darwin pour un Mac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) arch=$(uname -m) ;;
esac
if [ "$systeme" = Linux ]; then
  memoire_mo=$(awk '/^MemTotal:/ {print int($2 / 1024)}' /proc/meminfo)
  processeurs=$(nproc)
else
  memoire_mo=$(($(sysctl -n hw.memsize) / 1048576))
  processeurs=$(sysctl -n hw.ncpu)
fi

# Docker : absent, installé sans les droits pour l'utilisateur, arrêté, ou prêt
if ! command -v docker >/dev/null 2>&1; then
  docker_etat=absent
elif docker info >/dev/null 2>&1; then
  docker_etat=ok
elif docker info 2>&1 | grep -qi "permission denied"; then
  docker_etat=droits
else
  docker_etat=arrete
fi

# La place libre là où Docker range ses images (sur Linux, /var/lib/docker), en Go
racine=$(docker info -f '{{.DockerRootDir}}' 2>/dev/null || true)
[ -d "${racine:-/nulle-part}" ] || racine=/
disque_go=$(df -Pk "$racine" | awk 'NR == 2 {print int($4 / 1048576)}')

if [ -n "${PROFIL:-}" ]; then
  profil=$PROFIL
elif [ "$memoire_mo" -lt 3500 ] || [ "$processeurs" -lt 2 ]; then
  profil=poste
elif [ "$memoire_mo" -lt 7500 ]; then
  profil=leger
else
  profil=complet
fi

go_de() { awk -v mo="$1" 'BEGIN {s = sprintf("%.1f", mo / 1024); sub(/\./, ",", s); print s}'; }   # 3891 → 3,8

resume_machine() {
  echo "Machine : $systeme $arch, $processeurs processeur(s), $(go_de "$memoire_mo") Go de mémoire, $disque_go Go libres pour Docker"
  case $profil in
    poste) echo "Profil « poste » : Ruée avec Docker Compose ; le cluster se suit sur l'écran du formateur" ;;
    leger) echo "Profil « léger » : Ruée avec Docker Compose, puis un cluster Kubernetes d'un nœud, sans supervision" ;;
    complet) echo "Profil « complet » : le cluster des démonstrations (trois nœuds), avec Prometheus et Grafana" ;;
  esac
  [ "$disque_go" -ge 15 ] || echo "⚠️  moins de 15 Go libres pour Docker : les images risquent de ne pas tenir"
}

conseil_docker() {   # ce qu'il faut faire quand Docker n'est pas prêt
  case $docker_etat in
    absent)
      echo "Docker n'est pas installé. Sur Ubuntu ou Debian (le mot de passe administrateur est demandé) :"
      echo "  curl -fsSL https://get.docker.com | sudo sh"
      echo "  sudo usermod -aG docker \$USER"
      echo "Puis fermer la session, la rouvrir (le groupe docker est lu à la connexion) et relancer ./demarrer.sh"
      ;;
    droits)
      echo "Docker est installé, mais cet utilisateur n'a pas le droit de s'en servir :"
      echo "  sudo usermod -aG docker \$USER"
      echo "Puis fermer la session, la rouvrir et relancer ./demarrer.sh"
      ;;
    arrete)
      echo "Docker est installé mais ne répond pas : sudo systemctl start docker, puis relancer ./demarrer.sh"
      ;;
  esac
}
