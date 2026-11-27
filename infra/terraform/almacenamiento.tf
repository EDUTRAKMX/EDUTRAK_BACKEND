# Evidencias de justificantes (STORAGE_DIR de la API) compartidas por Page-1 y Page-2.
# Con Round Robin, un archivo subido en Page-1 debe poder descargarse desde Page-2;
# un directorio local por nodo rompería eso (mismo problema que la sesión del cap. 17).
resource "aws_efs_file_system" "evidencias" {
  count = var.efs_habilitado ? 1 : 0

  encrypted        = true
  performance_mode = "generalPurpose"
  throughput_mode  = "elastic"

  lifecycle_policy {
    transition_to_ia = "AFTER_30_DAYS"
  }

  tags = { Name = "${var.proyecto}-evidencias" }
}

resource "aws_efs_mount_target" "evidencias" {
  count = var.efs_habilitado ? 1 : 0

  file_system_id  = aws_efs_file_system.evidencias[0].id
  subnet_id       = aws_subnet.privada.id
  ip_address      = var.ip_efs
  security_groups = [aws_security_group.efs[0].id]
}
