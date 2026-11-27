variable "region" {
  description = "Región de AWS donde se crean las 5 máquinas."
  type        = string
  default     = "us-east-1"
}

variable "zona_disponibilidad" {
  description = "Zona de disponibilidad. Vacío = la primera disponible de la región."
  type        = string
  default     = ""
}

variable "proyecto" {
  description = "Prefijo de nombres, etiquetas y de la ruta de secretos en SSM Parameter Store."
  type        = string
  default     = "edutrak-p3"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,30}$", var.proyecto))
    error_message = "proyecto: minúsculas, dígitos y guiones (3-31 caracteres)."
  }
}

variable "nombre_bastion" {
  description = "Nombre (etiqueta Name) de la máquina cliente SSH / bastión. En la guía era el droplet SSH-Conection."
  type        = string
  default     = "SSHD_Conexion"

  validation {
    condition     = can(regex("^[A-Za-z][A-Za-z0-9_.-]{0,62}$", var.nombre_bastion))
    error_message = "nombre_bastion: letras, dígitos, guion, punto o guion bajo; empieza con letra."
  }
}

variable "llave_publica_ssh" {
  description = "Llave pública OpenSSH derivada de la llave privada .ppk de PuTTYgen (la calcula el pipeline). Debe ser RSA de 4096 bits."
  type        = string

  validation {
    condition     = can(regex("^ssh-rsa AAAA[0-9A-Za-z+/=]+( .*)?$", trimspace(var.llave_publica_ssh)))
    error_message = "llave_publica_ssh debe ser una llave RSA en formato OpenSSH (ssh-rsa AAAA...)."
  }
}

variable "usuario_aprovisionamiento" {
  description = "Cuenta temporal que crea cloud-init para que Ansible configure las máquinas. Se bloquea al final si así se pide."
  type        = string
  default     = "ansible"
}

variable "cidrs_administracion" {
  description = "IP(s) públicas autorizadas a llegar por SSH al bastión (tu casa/escuela), p. ej. [\"201.0.113.7/32\"]."
  type        = list(string)

  validation {
    condition     = length(var.cidrs_administracion) > 0 && alltrue([for c in var.cidrs_administracion : can(cidrhost(c, 0)) && !endswith(c, "/0")])
    error_message = "cidrs_administracion: al menos un CIDR válido y ninguno /0 (no abrir SSH a todo Internet)."
  }
}

variable "cidrs_web" {
  description = "Orígenes que pueden abrir HTTP en el balanceador (clientes web autorizados)."
  type        = list(string)
  default     = ["0.0.0.0/0"]

  validation {
    condition     = alltrue([for c in var.cidrs_web : can(cidrhost(c, 0))])
    error_message = "cidrs_web: todos deben ser CIDR IPv4 válidos."
  }
}

variable "tipo_instancia" {
  description = "Tipo de instancia por defecto para las 5 máquinas."
  type        = string
  default     = "t3.micro"
}

variable "tipos_por_rol" {
  description = "Tipo de instancia por rol (bastion, balanceador, web, bd). Lo que no esté aquí usa tipo_instancia."
  type        = map(string)
  default     = {}
}

variable "tamano_disco_gb" {
  description = "Tamaño del disco raíz (gp3, cifrado) de cada máquina."
  type        = number
  default     = 20
}

variable "ami_id" {
  description = "AMI fija. Vacío = la Rocky Linux 9 oficial más reciente (cuenta 792107900819)."
  type        = string
  default     = ""
}

variable "ami_propietario" {
  description = "Cuenta dueña de las AMI oficiales de Rocky Linux."
  type        = string
  default     = "792107900819"
}

variable "ami_filtro_nombre" {
  description = "Filtro de nombre de la AMI de Rocky Linux 9 (x86_64)."
  type        = string
  default     = "Rocky-9-EC2-Base-9.*x86_64*"
}

variable "cidr_vpc" {
  description = "Red privada del laboratorio (en DigitalOcean era la VPC 10.106.0.0/20)."
  type        = string
  default     = "10.106.0.0/16"
}

variable "cidr_subred_publica" {
  description = "Subred con salida directa a Internet: bastión y balanceador."
  type        = string
  default     = "10.106.0.0/24"
}

variable "cidr_subred_privada" {
  description = "Subred sin IP pública: Page-1, Page-2, base de datos y EFS (salen a Internet por NAT solo para paquetes)."
  type        = string
  default     = "10.106.1.0/24"
}

variable "puerto_bd" {
  description = "Puerto de PostgreSQL (la guía usó 33609 para MariaDB; se conserva)."
  type        = number
  default     = 33609
}

variable "efs_habilitado" {
  description = "Crear un EFS cifrado compartido por Page-1 y Page-2 para las evidencias de justificantes (STORAGE_DIR)."
  type        = bool
  default     = true
}

variable "ip_efs" {
  description = "IP fija del punto de montaje EFS en la subred privada."
  type        = string
  default     = "10.106.1.20"
}

# Inventario de la guía (capítulo 02). AWS reserva las primeras 4 IP de cada subred
# (.0 a .3), así que las IP de DigitalOcean (.2 a .6) se trasladan sumando 10 y las
# máquinas sin exposición directa pasan a la subred privada.
variable "nodos" {
  description = "Máquinas del laboratorio: rol, subred, IP privada, puerto SSH y usuario administrador."
  type = map(object({
    nombre        = string
    hostname      = string
    rol           = string
    subred        = string
    ip            = string
    puerto_ssh    = number
    usuario_admin = string
  }))

  default = {
    bastion = {
      nombre        = "SSHD_Conexion" # se reemplaza por var.nombre_bastion
      hostname      = "sshd-conexion"
      rol           = "bastion"
      subred        = "publica"
      ip            = "10.106.0.13"
      puerto_ssh    = 24783
      usuario_admin = "coriander"
    }
    balanceador = {
      nombre        = "Balanceador"
      hostname      = "balanceador"
      rol           = "balanceador"
      subred        = "publica"
      ip            = "10.106.0.14"
      puerto_ssh    = 22170
      usuario_admin = "asparagus"
    }
    page1 = {
      nombre        = "Page-1"
      hostname      = "page-1"
      rol           = "web"
      subred        = "privada"
      ip            = "10.106.1.12"
      puerto_ssh    = 38236
      usuario_admin = "pumpkin"
    }
    page2 = {
      nombre        = "Page-2"
      hostname      = "page-2"
      rol           = "web"
      subred        = "privada"
      ip            = "10.106.1.15"
      puerto_ssh    = 42157
      usuario_admin = "lettuce"
    }
    bd = {
      nombre        = "BD-PostgreSQL"
      hostname      = "bd-postgresql"
      rol           = "bd"
      subred        = "privada"
      ip            = "10.106.1.16"
      puerto_ssh    = 24170
      usuario_admin = "guava"
    }
  }

  validation {
    condition = (
      length([for k, n in var.nodos : k if n.rol == "bastion"]) == 1 &&
      length([for k, n in var.nodos : k if n.rol == "balanceador"]) == 1 &&
      length([for k, n in var.nodos : k if n.rol == "web"]) >= 2 &&
      length([for k, n in var.nodos : k if n.rol == "bd"]) == 1
    )
    error_message = "nodos: se requieren exactamente 1 bastion, 1 balanceador, 1 bd y al menos 2 web."
  }

  validation {
    condition     = alltrue([for n in var.nodos : contains(["publica", "privada"], n.subred)])
    error_message = "nodos: subred debe ser \"publica\" o \"privada\"."
  }

  validation {
    condition     = alltrue([for n in var.nodos : tonumber(split(".", n.ip)[3]) >= 4 && tonumber(split(".", n.ip)[3]) <= 254])
    error_message = "nodos: AWS reserva .0-.3 y .255 de cada subred; usa IP entre .4 y .254."
  }

  validation {
    condition     = alltrue([for n in var.nodos : n.puerto_ssh > 1024 && n.puerto_ssh < 65536])
    error_message = "nodos: usa puertos SSH no privilegiados (1025-65535), como en la guía."
  }

  validation {
    condition     = alltrue([for n in var.nodos : can(regex("^[a-z][a-z0-9-]{0,62}$", n.hostname))])
    error_message = "nodos: hostname en minúsculas, dígitos y guiones (sin guion bajo)."
  }
}
