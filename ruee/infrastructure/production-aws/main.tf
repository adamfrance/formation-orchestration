# La production de Ruée sur AWS : un réseau privé et un cluster EKS, avec les modules de la communauté.
# À lire seulement : l'appliquer crée des ressources payantes sur un compte AWS (cluster, passerelle NAT, nœuds).
# Même méthode que l'environnement de recette (infrastructure/recette) : seul le fournisseur change.

terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws" # le fournisseur officiel d'AWS
      version = "~> 6.0"
    }
  }
  # L'état est partagé par l'équipe plateforme : dans un bucket S3 chiffré, avec un verrou (deux « apply » en même
  # temps abîmeraient l'état). Il contient des secrets : accès réservé, jamais dans Git.
  backend "s3" {
    bucket       = "ruee-etats-infrastructure"
    key          = "production/eks.tfstate"
    region       = "eu-west-3"
    encrypt      = true
    use_lockfile = true # le verrou est un fichier dans le bucket (plus besoin de table DynamoDB)
  }
}

provider "aws" {
  region = "eu-west-3" # Paris
  default_tags {       # chaque ressource porte ces étiquettes : on sait qui paie quoi
    tags = { projet = "ruee", environnement = "production", equipe = "plateforme" }
  }
}

# Le réseau : trois zones de disponibilité (trois centres de données de la région), des sous-réseaux privés pour les
# nœuds et des sous-réseaux publics pour les équilibreurs de charge
module "reseau" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 6.0"

  name            = "ruee-production"
  cidr            = "10.0.0.0/16"
  azs             = ["eu-west-3a", "eu-west-3b", "eu-west-3c"]
  private_subnets = ["10.0.1.0/24", "10.0.2.0/24", "10.0.3.0/24"]
  public_subnets  = ["10.0.101.0/24", "10.0.102.0/24", "10.0.103.0/24"]

  enable_nat_gateway = true # les nœuds privés sortent vers Internet par une passerelle NAT
  single_nat_gateway = true # une seule pour réduire la facture (une par zone en vraie haute disponibilité)
}

# Le cluster : AWS gère le plan de contrôle (etcd, API, scheduler) ; en mode Auto, il gère aussi les nœuds
module "cluster" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 21.0"

  name               = "ruee-production"
  kubernetes_version = "1.35" # une version prise en charge par EKS (liste : aws eks describe-cluster-versions)
  vpc_id             = module.reseau.vpc_id
  subnet_ids         = module.reseau.private_subnets

  endpoint_public_access                   = true # l'API joignable d'Internet (à restreindre par adresses en vrai)
  enable_cluster_creator_admin_permissions = true # le compte qui crée le cluster en devient administrateur

  compute_config = { # EKS Auto Mode : AWS choisit, dimensionne et met à jour les nœuds selon les pods à placer
    enabled    = true
    node_pools = ["general-purpose"]
  }
}

output "cluster" {
  value = module.cluster.cluster_name
}
