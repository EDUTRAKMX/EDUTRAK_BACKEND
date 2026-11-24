# Firewall de nube (equivalente al Cloud Firewall de DigitalOcean que la guía dejó
# "no verificado"). Replica la "Política de acceso esperada" del capítulo 03, de
# entrada Y de salida. firewalld repite las mismas reglas dentro de cada máquina.
# Nota: las descripciones de AWS solo aceptan ASCII (sin acentos).

resource "aws_security_group" "rol" {
  for_each = local.roles

  name        = "${var.proyecto}-${each.key}"
  description = each.value
  vpc_id      = aws_vpc.lab.id

  tags = { Name = "${var.proyecto}-${each.key}" }
}

resource "aws_security_group" "efs" {
  count = var.efs_habilitado ? 1 : 0

  name        = "${var.proyecto}-efs"
  description = "EFS de evidencias: NFS solo desde los Pages"
  vpc_id      = aws_vpc.lab.id

  tags = { Name = "${var.proyecto}-efs" }
}

locals {
  reglas_entrada = concat(
    [for c in var.cidrs_administracion : {
      clave     = "bastion-ssh-${c}"
      sg        = "bastion"
      puerto    = local.bastion.puerto_ssh
      cidr      = c
      origen_sg = null
      desc      = "SSH de la IP de administracion"
    }],
    [for c in var.cidrs_web : {
      clave     = "balanceador-http-${c}"
      sg        = "balanceador"
      puerto    = 80
      cidr      = c
      origen_sg = null
      desc      = "HTTP de clientes web autorizados"
    }],
    [{
      clave     = "balanceador-ssh"
      sg        = "balanceador"
      puerto    = local.balanceador.puerto_ssh
      cidr      = null
      origen_sg = "bastion"
      desc      = "SSH solo desde el bastion"
    }],
    [{
      clave     = "web-http"
      sg        = "web"
      puerto    = 80
      cidr      = null
      origen_sg = "balanceador"
      desc      = "HTTP solo desde el balanceador"
    }],
    [for n in local.web : {
      clave     = "web-ssh-${n.hostname}"
      sg        = "web"
      puerto    = n.puerto_ssh
      cidr      = null
      origen_sg = "bastion"
      desc      = "SSH ${n.nombre} solo desde el bastion"
    }],
    [{
      clave     = "bd-sql"
      sg        = "bd"
      puerto    = var.puerto_bd
      cidr      = null
      origen_sg = "web"
      desc      = "PostgreSQL solo desde los Pages"
    }],
    [{
      clave     = "bd-ssh"
      sg        = "bd"
      puerto    = local.bd.puerto_ssh
      cidr      = null
      origen_sg = "bastion"
      desc      = "SSH solo desde el bastion"
    }],
  )

  # Salida: solo lo necesario. 80/443 a Internet es para dnf (repos de Rocky, EPEL y
  # PGDG). DNS de la VPC y Amazon Time Sync (169.254.169.123) no pasan por los SG.
  reglas_salida = concat(
    flatten([for sg in keys(local.roles) : [for p in [80, 443] : {
      clave      = "${sg}-internet-${p}"
      sg         = sg
      puerto     = p
      cidr       = "0.0.0.0/0"
      destino_sg = null
      desc       = "Repositorios de paquetes"
    }]]),
    [{
      clave      = "bastion-ssh-balanceador"
      sg         = "bastion"
      puerto     = local.balanceador.puerto_ssh
      cidr       = null
      destino_sg = "balanceador"
      desc       = "SSH al balanceador"
    }],
    [for n in local.web : {
      clave      = "bastion-ssh-${n.hostname}"
      sg         = "bastion"
      puerto     = n.puerto_ssh
      cidr       = null
      destino_sg = "web"
      desc       = "SSH a ${n.nombre}"
    }],
    [{
      clave      = "bastion-ssh-bd"
      sg         = "bastion"
      puerto     = local.bd.puerto_ssh
      cidr       = null
      destino_sg = "bd"
      desc       = "SSH a la base de datos"
    }],
    [{
      clave      = "balanceador-http-web"
      sg         = "balanceador"
      puerto     = 80
      cidr       = null
      destino_sg = "web"
      desc       = "Round Robin hacia los Pages"
    }],
    [{
      clave      = "web-sql-bd"
      sg         = "web"
      puerto     = var.puerto_bd
      cidr       = null
      destino_sg = "bd"
      desc       = "API hacia PostgreSQL"
    }],
  )
}

resource "aws_vpc_security_group_ingress_rule" "entrada" {
  for_each = { for r in local.reglas_entrada : r.clave => r }

  security_group_id            = aws_security_group.rol[each.value.sg].id
  description                  = each.value.desc
  ip_protocol                  = "tcp"
  from_port                    = each.value.puerto
  to_port                      = each.value.puerto
  cidr_ipv4                    = each.value.cidr
  referenced_security_group_id = each.value.origen_sg == null ? null : aws_security_group.rol[each.value.origen_sg].id

  tags = { Name = each.key }
}

resource "aws_vpc_security_group_egress_rule" "salida" {
  for_each = { for r in local.reglas_salida : r.clave => r }

  security_group_id            = aws_security_group.rol[each.value.sg].id
  description                  = each.value.desc
  ip_protocol                  = "tcp"
  from_port                    = each.value.puerto
  to_port                      = each.value.puerto
  cidr_ipv4                    = each.value.cidr
  referenced_security_group_id = each.value.destino_sg == null ? null : aws_security_group.rol[each.value.destino_sg].id

  tags = { Name = each.key }
}

resource "aws_vpc_security_group_ingress_rule" "efs_desde_web" {
  count = var.efs_habilitado ? 1 : 0

  security_group_id            = aws_security_group.efs[0].id
  description                  = "NFS desde los Pages"
  ip_protocol                  = "tcp"
  from_port                    = 2049
  to_port                      = 2049
  referenced_security_group_id = aws_security_group.rol["web"].id
}

resource "aws_vpc_security_group_egress_rule" "web_a_efs" {
  count = var.efs_habilitado ? 1 : 0

  security_group_id            = aws_security_group.rol["web"].id
  description                  = "NFS hacia el EFS de evidencias"
  ip_protocol                  = "tcp"
  from_port                    = 2049
  to_port                      = 2049
  referenced_security_group_id = aws_security_group.efs[0].id
}
