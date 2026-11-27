locals {
  # Un hostname Linux no admite "_": SSHD_Conexion -> sshd-conexion.
  hostname_bastion = trim(replace(lower(var.nombre_bastion), "/[^a-z0-9-]+/", "-"), "-")

  nodos = {
    for k, n in var.nodos : k => merge(n, {
      nombre   = n.rol == "bastion" ? var.nombre_bastion : n.nombre
      hostname = n.rol == "bastion" ? local.hostname_bastion : n.hostname
    })
  }

  bastion     = one([for n in values(local.nodos) : n if n.rol == "bastion"])
  balanceador = one([for n in values(local.nodos) : n if n.rol == "balanceador"])
  bd          = one([for n in values(local.nodos) : n if n.rol == "bd"])
  web         = [for k in sort(keys(local.nodos)) : local.nodos[k] if local.nodos[k].rol == "web"]

  roles = {
    bastion     = "Bastion SSH: unico punto de entrada administrativo"
    balanceador = "Balanceador NGINX Round Robin"
    web         = "Servidores web Page-1 y Page-2 (NGINX + API EDUTRAK)"
    bd          = "Base de datos PostgreSQL"
  }
}
