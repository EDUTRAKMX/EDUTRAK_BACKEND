terraform {
  # 1.10+ es necesario para el bloqueo nativo del estado en S3 (use_lockfile).
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.20"
    }
  }

  # El pipeline completa bucket, key, region y use_lockfile con -backend-config.
  # Para usarlo en tu PC: terraform init -backend-config=backend.hcl (ver README).
  backend "s3" {}
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Proyecto      = var.proyecto
      Practica      = "3-infraestructura-web-segura"
      GestionadoPor = "terraform"
    }
  }
}
