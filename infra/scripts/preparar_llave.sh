#!/usr/bin/env bash
# Convierte la llave privada de PuTTYgen (.ppk v2/v3) a OpenSSH para Terraform/Ansible
# y comprueba que sea RSA de 4096 bits (guía, cap. 03).
#
# Uso: preparar_llave.sh <llave_entrada> <dir_salida> [archivo_con_frase_de_paso]
#   <llave_entrada>  .ppk de PuTTYgen, o una llave OpenSSH (BEGIN OPENSSH PRIVATE KEY)
#   <dir_salida>     recibe id_rsa (sin frase, 0600) e id_rsa.pub
#
# La copia sin frase de paso vive solo en el runner efímero de GitHub Actions
# ($RUNNER_TEMP) y se borra con él. Requiere: puttygen (putty-tools) y ssh-keygen.
set -euo pipefail

entrada=${1:?uso: preparar_llave.sh <llave> <dir_salida> [frase]}
salida=${2:?falta dir_salida}
frase=${3:-}
bits_minimos=4096

umask 077
mkdir -p "$salida"
trabajo=$(mktemp -d)
trap 'rm -rf "$trabajo"' EXIT

# Las llaves copiadas desde Windows suelen traer CRLF.
tr -d '\r' < "$entrada" > "$trabajo/llave"
# Un archivo vacío para "sin frase de paso" en puttygen.
: > "$trabajo/vacia"

if head -n 1 "$trabajo/llave" | grep -q '^PuTTY-User-Key-File-'; then
  args=(--new-passphrase "$trabajo/vacia")
  if [[ -n $frase && -s $frase ]]; then
    args+=(--old-passphrase "$frase")
  fi
  if ! puttygen "$trabajo/llave" "${args[@]}" -O private-openssh -o "$salida/id_rsa" </dev/null 2>"$trabajo/err"; then
    echo "::error::puttygen no pudo leer la llave .ppk (¿frase de paso incorrecta o faltante en SSH_KEY_PASSPHRASE?)" >&2
    sed 's/^/  /' "$trabajo/err" >&2
    exit 1
  fi
elif grep -q -- '-----BEGIN \(OPENSSH\|RSA\) PRIVATE KEY-----' "$trabajo/llave"; then
  cp "$trabajo/llave" "$salida/id_rsa"
  if [[ -n $frase && -s $frase ]]; then
    ssh-keygen -q -p -P "$(cat "$frase")" -N '' -f "$salida/id_rsa" >/dev/null
  fi
else
  echo "::error::SSH_PRIVATE_KEY no es una llave .ppk de PuTTYgen ni una llave OpenSSH." >&2
  exit 1
fi

chmod 600 "$salida/id_rsa"
if ! ssh-keygen -y -f "$salida/id_rsa" > "$salida/id_rsa.pub" 2>/dev/null; then
  echo "::error::La llave sigue cifrada: define el secreto SSH_KEY_PASSPHRASE." >&2
  exit 1
fi

read -r bits huella _ <<<"$(ssh-keygen -l -f "$salida/id_rsa.pub")"
tipo=$(ssh-keygen -l -f "$salida/id_rsa.pub" | awk '{print $NF}')
if [[ $tipo != "(RSA)" || $bits -lt $bits_minimos ]]; then
  echo "::error::La guía exige RSA de ${bits_minimos} bits y la llave es ${tipo} de ${bits} bits. En PuTTYgen elige RSA y 'Number of bits' = ${bits_minimos}." >&2
  exit 1
fi

echo "Llave aceptada: RSA ${bits} bits, huella ${huella}"
