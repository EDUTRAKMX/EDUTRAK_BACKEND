data "aws_ami" "rocky9" {
  count = var.ami_id == "" ? 1 : 0

  most_recent = true
  owners      = [var.ami_propietario]

  filter {
    name   = "name"
    values = [var.ami_filtro_nombre]
  }

  filter {
    name   = "architecture"
    values = ["x86_64"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

locals {
  ami = var.ami_id != "" ? var.ami_id : data.aws_ami.rocky9[0].id
}

# Par de llaves de AWS con la parte pública de tu llave de PuTTYgen. cloud-init la
# instala en la cuenta temporal de aprovisionamiento (no se crea el usuario "rocky").
resource "aws_key_pair" "puttygen" {
  key_name   = "${var.proyecto}-puttygen"
  public_key = trimspace(var.llave_publica_ssh)
}

resource "aws_instance" "nodo" {
  for_each = local.nodos

  ami                         = local.ami
  instance_type               = lookup(var.tipos_por_rol, each.value.rol, var.tipo_instancia)
  subnet_id                   = each.value.subred == "publica" ? aws_subnet.publica.id : aws_subnet.privada.id
  private_ip                  = each.value.ip
  associate_public_ip_address = each.value.subred == "publica"
  vpc_security_group_ids      = [aws_security_group.rol[each.value.rol].id]
  key_name                    = aws_key_pair.puttygen.key_name

  user_data = templatefile("${path.module}/plantillas/cloud-init.yaml.tftpl", {
    hostname      = each.value.hostname
    puerto_ssh    = each.value.puerto_ssh
    usuario       = var.usuario_aprovisionamiento
    llave_publica = trimspace(var.llave_publica_ssh)
  })
  user_data_replace_on_change = true

  # IMDSv2 obligatorio y sin etiquetas en los metadatos.
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "disabled"
  }

  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.tamano_disco_gb
    encrypted             = true
    delete_on_termination = true
    tags                  = { Name = "${each.value.nombre}-raiz" }
  }

  # En familias T, "standard" evita cobros extra por créditos de CPU.
  dynamic "credit_specification" {
    for_each = startswith(lookup(var.tipos_por_rol, each.value.rol, var.tipo_instancia), "t") ? [1] : []
    content {
      cpu_credits = "standard"
    }
  }

  tags = {
    Name = each.value.nombre
    Rol  = each.value.rol
  }

  lifecycle {
    # Una AMI nueva publicada por Rocky no debe recrear las máquinas.
    ignore_changes = [ami]
  }

  # cloud-init instala paquetes al arrancar: la salida a Internet debe existir antes.
  depends_on = [
    aws_route_table_association.publica,
    aws_route_table_association.privada,
    aws_nat_gateway.nat,
  ]
}
