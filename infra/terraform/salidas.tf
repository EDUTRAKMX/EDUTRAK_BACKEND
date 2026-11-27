output "bastion_ip_publica" {
  description = "IP pública del bastión (PuTTY: Host Name)."
  value       = aws_instance.nodo[one([for k, n in local.nodos : k if n.rol == "bastion"])].public_ip
}

output "bastion_puerto_ssh" {
  description = "Puerto SSH del bastión (PuTTY: Port)."
  value       = local.bastion.puerto_ssh
}

output "balanceador_ip_publica" {
  description = "IP pública del balanceador."
  value       = aws_instance.nodo[one([for k, n in local.nodos : k if n.rol == "balanceador"])].public_ip
}

output "url_aplicacion" {
  description = "Entrada web (HTTP) del balanceador."
  value       = "http://${aws_instance.nodo[one([for k, n in local.nodos : k if n.rol == "balanceador"])].public_ip}/"
}

output "sg_bastion_id" {
  description = "Grupo de seguridad del bastión (el pipeline abre ahí, solo durante la ejecución, la IP del runner)."
  value       = aws_security_group.rol["bastion"].id
}

output "efs_ip" {
  description = "IP del punto de montaje EFS (vacío si está deshabilitado)."
  value       = var.efs_habilitado ? aws_efs_mount_target.evidencias[0].ip_address : ""
}

output "puerto_bd" {
  value = var.puerto_bd
}

output "ami" {
  description = "AMI usada."
  value       = local.ami
}

# Lo consume infra/scripts/generar_inventario.py para crear el inventario de Ansible.
output "inventario" {
  description = "Nodos con IP, puerto SSH, rol y usuario administrador."
  value = {
    for k, n in local.nodos : k => {
      nombre        = n.nombre
      hostname      = n.hostname
      rol           = n.rol
      ip_privada    = n.ip
      ip_publica    = aws_instance.nodo[k].public_ip
      puerto_ssh    = n.puerto_ssh
      usuario_admin = n.usuario_admin
      instancia_id  = aws_instance.nodo[k].id
    }
  }
}
