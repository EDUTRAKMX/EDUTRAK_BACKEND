data "aws_availability_zones" "disponibles" {
  state = "available"
}

locals {
  az = var.zona_disponibilidad != "" ? var.zona_disponibilidad : data.aws_availability_zones.disponibles.names[0]
}

resource "aws_vpc" "lab" {
  cidr_block           = var.cidr_vpc
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = "${var.proyecto}-vpc" }
}

# El grupo de seguridad "default" de la VPC queda sin reglas: nada lo usa y así no
# puede abrir tráfico por accidente.
resource "aws_default_security_group" "default" {
  vpc_id = aws_vpc.lab.id
  tags   = { Name = "${var.proyecto}-default-sin-reglas" }
}

resource "aws_internet_gateway" "igw" {
  vpc_id = aws_vpc.lab.id
  tags   = { Name = "${var.proyecto}-igw" }
}

resource "aws_subnet" "publica" {
  vpc_id                  = aws_vpc.lab.id
  cidr_block              = var.cidr_subred_publica
  availability_zone       = local.az
  map_public_ip_on_launch = false

  tags = { Name = "${var.proyecto}-publica" }
}

resource "aws_subnet" "privada" {
  vpc_id                  = aws_vpc.lab.id
  cidr_block              = var.cidr_subred_privada
  availability_zone       = local.az
  map_public_ip_on_launch = false

  tags = { Name = "${var.proyecto}-privada" }
}

resource "aws_route_table" "publica" {
  vpc_id = aws_vpc.lab.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.igw.id
  }

  tags = { Name = "${var.proyecto}-rt-publica" }
}

resource "aws_route_table_association" "publica" {
  subnet_id      = aws_subnet.publica.id
  route_table_id = aws_route_table.publica.id
}

# Salida a Internet de la subred privada SOLO para instalar paquetes (dnf).
# Nadie puede iniciar conexiones desde Internet hacia la subred privada.
resource "aws_eip" "nat" {
  domain = "vpc"
  tags   = { Name = "${var.proyecto}-nat" }
}

resource "aws_nat_gateway" "nat" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.publica.id
  tags          = { Name = "${var.proyecto}-nat" }

  depends_on = [aws_internet_gateway.igw]
}

resource "aws_route_table" "privada" {
  vpc_id = aws_vpc.lab.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.nat.id
  }

  tags = { Name = "${var.proyecto}-rt-privada" }
}

resource "aws_route_table_association" "privada" {
  subnet_id      = aws_subnet.privada.id
  route_table_id = aws_route_table.privada.id
}
