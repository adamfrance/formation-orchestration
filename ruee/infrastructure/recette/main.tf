# L'environnement de recette de Ruée, décrit en code : un registre d'images et un cluster Kubernetes (kind), créés sur
# le Docker du poste. Même logique qu'en production (infrastructure/production-aws) : on décrit ce qu'on veut, l'outil
# calcule ce qu'il faut créer, modifier ou détruire.
#   tofu init      télécharge les fournisseurs (une fois)
#   tofu plan      montre ce qui va changer, sans rien toucher
#   tofu apply     applique
#   tofu destroy   détruit tout ce que ce code a créé
# Les mêmes commandes marchent avec terraform à la place de tofu.

terraform {
  required_version = ">= 1.6" # Terraform 1.6 ou plus, OpenTofu 1.6 ou plus
  required_providers {
    docker = {
      source  = "kreuzwerker/docker" # le fournisseur : il traduit ce code en appels à l'API de Docker
      version = "~> 4.6"             # 4.6 ou plus, mais pas 5.0 (une version majeure peut tout changer)
    }
    kind = {
      source  = "tehcyx/kind" # crée des clusters Kubernetes dans des conteneurs Docker
      version = "~> 0.11"
    }
  }
}

provider "docker" {} # utilise DOCKER_HOST, comme la commande docker
provider "kind" {}

# Les réglages, modifiables sans toucher au code : tofu apply -var noeuds_de_travail=2
variable "noeuds_de_travail" {
  description = "Nombre de nœuds de travail du cluster (en plus du plan de contrôle)"
  type        = number
  default     = 0
}

variable "kubeconfig" {
  description = "Fichier où écrire les identifiants du cluster (hors du dépôt : c'est un secret)"
  type        = string
  default     = "~/.kube/ruee-recette"
}

# Le registre d'images : l'image officielle « registry », publiée sur le port 5002 du poste
resource "docker_image" "registre" {
  name         = "registry:3.1.2"
  keep_locally = true # au destroy, on garde l'image téléchargée (seul le conteneur disparaît)
}

resource "docker_container" "registre" {
  name    = "ruee-recette-registre"
  image   = docker_image.registre.image_id # dépendance : l'image est téléchargée avant le conteneur
  restart = "always"
  ports {
    internal = 5000
    external = 5002
    ip       = "127.0.0.1" # joignable depuis le poste seulement
  }
}

# Le cluster : un plan de contrôle, et autant de nœuds de travail que demandé
resource "kind_cluster" "recette" {
  name            = "ruee-recette"
  node_image      = "kindest/node:v1.37.0" # la version de Kubernetes
  kubeconfig_path = pathexpand(var.kubeconfig)
  wait_for_ready  = true # apply ne rend la main que quand le cluster répond

  kind_config {
    kind        = "Cluster"
    api_version = "kind.x-k8s.io/v1alpha4"
    node {
      role = "control-plane"
    }
    dynamic "node" { # un bloc « node » par nœud de travail
      for_each = range(var.noeuds_de_travail)
      content {
        role = "worker"
      }
    }
  }
}

# Ce que l'outil affiche à la fin, et que d'autres outils peuvent lire (tofu output -json)
output "registre" {
  value = "localhost:5002"
}

output "api_kubernetes" {
  value = kind_cluster.recette.endpoint
}
