#!/usr/bin/env python3
"""Genera el inventario de Ansible y el ssh_config a partir de `terraform output -json`.

Uso:
  terraform -chdir=infra/terraform output -json > salidas.json
  python3 infra/scripts/generar_inventario.py salidas.json \
      --llave $RUNNER_TEMP/llave/id_rsa --destino infra/ansible/inventario

Todo el tráfico de Ansible entra por el bastión (ProxyCommand), igual que el
acceso administrativo de la guía: los nodos internos solo aceptan SSH desde él.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

# rol de Terraform -> grupo de Ansible ("balanceadores" en plural para no chocar
# con el nombre del equipo "balanceador").
GRUPOS = {"bastion": "bastion", "balanceador": "balanceadores", "web": "web", "bd": "bd"}


def cargar(ruta: str) -> dict:
    with open(ruta, encoding="utf-8") as f:
        datos = json.load(f)
    # Acepta tanto `terraform output -json` completo como solo el valor.
    return {k: (v["value"] if isinstance(v, dict) and "value" in v else v) for k, v in datos.items()}


def yaml_escalar(valor) -> str:
    """Serializa escalares de forma segura para YAML (json es un subconjunto válido)."""
    return json.dumps(valor, ensure_ascii=False)


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("salidas", help="JSON de `terraform output -json`")
    p.add_argument("--llave", required=True, help="llave privada OpenSSH (convertida desde la .ppk)")
    p.add_argument("--destino", required=True, help="directorio del inventario")
    p.add_argument("--usuario", default="ansible", help="cuenta temporal de aprovisionamiento")
    args = p.parse_args()

    salidas = cargar(args.salidas)
    nodos = salidas["inventario"]
    destino = Path(args.destino).resolve()
    destino.mkdir(parents=True, exist_ok=True)
    llave = str(Path(args.llave).resolve())
    ssh_config = destino / "ssh_config"
    known_hosts = destino / "known_hosts"

    bastion = [n for n in nodos.values() if n["rol"] == "bastion"]
    if len(bastion) != 1 or not bastion[0].get("ip_publica"):
        print("El inventario debe tener exactamente un bastión con IP pública.", file=sys.stderr)
        return 1
    bastion = bastion[0]
    internos = [n for n in nodos.values() if n["rol"] != "bastion"]

    # --- ssh_config -------------------------------------------------------------
    lineas = [
        "# Generado por generar_inventario.py: no editar.",
        "Host p3-bastion",
        f"    HostName {bastion['ip_publica']}",
        f"    Port {bastion['puerto_ssh']}",
        "",
        "Host " + " ".join(n["ip_privada"] for n in internos),
        f"    ProxyCommand ssh -F {ssh_config} -W %h:%p p3-bastion",
        "",
        "Host *",
        f"    User {args.usuario}",
        f"    IdentityFile {llave}",
        "    IdentitiesOnly yes",
        "    IdentityAgent none",
        "    HostKeyAlgorithms ssh-ed25519",
        "    StrictHostKeyChecking accept-new",
        f"    UserKnownHostsFile {known_hosts}",
        "    ForwardAgent no",
        "    ConnectTimeout 15",
        "    ServerAliveInterval 15",
        "    ServerAliveCountMax 4",
        "",
    ]
    ssh_config.write_text("\n".join(lineas), encoding="utf-8")
    os.chmod(ssh_config, 0o600)

    # --- hosts.yml ----------------------------------------------------------------
    y = [
        "# Generado por generar_inventario.py a partir de Terraform: no editar.",
        "all:",
        "  vars:",
        f"    ansible_user: {yaml_escalar(args.usuario)}",
        f"    ansible_ssh_private_key_file: {yaml_escalar(llave)}",
        f"    ansible_ssh_common_args: {yaml_escalar('-F ' + str(ssh_config))}",
        f"    p3_ssh_config: {yaml_escalar(str(ssh_config))}",
        f"    p3_efs_ip: {yaml_escalar(salidas.get('efs_ip', ''))}",
        f"    p3_puerto_bd: {int(salidas.get('puerto_bd', 33609))}",
        "  children:",
    ]
    for rol, grupo in GRUPOS.items():
        y += [f"    {grupo}:", "      hosts:"]
        for clave, n in sorted(nodos.items()):
            if n["rol"] != rol:
                continue
            host = "p3-bastion" if rol == "bastion" else n["ip_privada"]
            y += [
                f"        {n['hostname']}:",
                f"          ansible_host: {yaml_escalar(host)}",
                f"          ansible_port: {int(n['puerto_ssh'])}",
                f"          p3_clave: {yaml_escalar(clave)}",
                f"          p3_nombre: {yaml_escalar(n['nombre'])}",
                f"          p3_rol: {yaml_escalar(n['rol'])}",
                f"          p3_ip: {yaml_escalar(n['ip_privada'])}",
                f"          p3_ip_publica: {yaml_escalar(n.get('ip_publica') or '')}",
                f"          p3_puerto_ssh: {int(n['puerto_ssh'])}",
                f"          p3_usuario_admin: {yaml_escalar(n['usuario_admin'])}",
                f"          p3_instancia: {yaml_escalar(n['instancia_id'])}",
            ]
    y += [
        "    internos:",
        "      children:",
        "        balanceadores: {}",
        "        web: {}",
        "        bd: {}",
        "",
    ]
    (destino / "hosts.yml").write_text("\n".join(y), encoding="utf-8")
    print(f"Inventario: {destino / 'hosts.yml'}")
    print(f"ssh_config: {ssh_config}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
