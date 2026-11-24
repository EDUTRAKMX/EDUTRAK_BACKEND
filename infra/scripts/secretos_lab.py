#!/usr/bin/env python3
"""Secretos del laboratorio en AWS SSM Parameter Store (SecureString, cifrados con KMS).

Nada sensible pasa por las entradas del workflow ni por los logs: las contraseñas,
semillas TOTP, frases de paso y claves de la API se generan aquí UNA sola vez y se
reutilizan en cada ejecución (así no tienes que volver a registrar el TOTP).

  asegurar  crea lo que falte y escribe un JSON para Ansible (-e @archivo)
  mostrar   (en tu PC) imprime contraseñas, semillas TOTP, URI otpauth y QR
  borrar    elimina todos los parámetros del proyecto

Ejemplos:
  python3 secretos_lab.py asegurar --proyecto edutrak-p3 --region us-east-1 \
      --usuarios coriander,celery,asparagus,pumpkin,lettuce,guava --salida /tmp/s.json
  python3 secretos_lab.py mostrar --proyecto edutrak-p3 --region us-east-1 --qr
"""
from __future__ import annotations

import argparse
import base64
import json
import os
import secrets
import string
import sys
import urllib.parse

try:
    import boto3
    from botocore.exceptions import ClientError
except ImportError:  # pragma: no cover
    print("Instala boto3: pip install boto3", file=sys.stderr)
    sys.exit(2)

SIMBOLOS = "-_.:+=@%"
EMISOR = "EDUTRAK-P3"


# --- Generadores -------------------------------------------------------------------
def contrasena(longitud: int = 20) -> str:
    """Contraseña con las 4 clases (pwquality del laboratorio) y sin 4 caracteres repetidos."""
    alfabeto = string.ascii_letters + string.digits + SIMBOLOS
    while True:
        c = "".join(secrets.choice(alfabeto) for _ in range(longitud))
        if (
            any(ch.islower() for ch in c)
            and any(ch.isupper() for ch in c)
            and any(ch.isdigit() for ch in c)
            and any(ch in SIMBOLOS for ch in c)
            and c[0].isalnum()
            and not any(c[i] == c[i + 1] == c[i + 2] == c[i + 3] for i in range(len(c) - 3))
        ):
            return c


def alfanumerica(longitud: int = 40) -> str:
    """Para cadenas de conexión: sin caracteres que haya que escapar en una URL."""
    alfabeto = string.ascii_letters + string.digits
    return "".join(secrets.choice(alfabeto) for _ in range(longitud))


def semilla_totp() -> str:
    """160 bits en Base32 (lo que genera google-authenticator)."""
    return base64.b32encode(secrets.token_bytes(20)).decode().rstrip("=")


def codigos_emergencia() -> str:
    return " ".join(str(secrets.randbelow(90_000_000) + 10_000_000) for _ in range(5))


def jwt() -> str:
    return base64.urlsafe_b64encode(secrets.token_bytes(48)).decode().rstrip("=")


# --- SSM -----------------------------------------------------------------------------
class Almacen:
    def __init__(self, proyecto: str, region: str):
        self.raiz = f"/{proyecto}"
        self.proyecto = proyecto
        self.ssm = boto3.client("ssm", region_name=region)

    def ruta(self, relativa: str) -> str:
        return f"{self.raiz}/{relativa}"

    def asegurar(self, relativa: str, generador, descripcion: str) -> str:
        nombre = self.ruta(relativa)
        try:
            return self.ssm.get_parameter(Name=nombre, WithDecryption=True)["Parameter"]["Value"]
        except ClientError as e:
            if e.response["Error"]["Code"] != "ParameterNotFound":
                raise
        valor = generador()
        try:
            self.ssm.put_parameter(
                Name=nombre,
                Value=valor,
                Type="SecureString",
                Description=descripcion[:1024],
                Overwrite=False,
                Tier="Standard",
                Tags=[{"Key": "Proyecto", "Value": self.proyecto}],
            )
            print(f"creado {nombre}", file=sys.stderr)
        except ClientError as e:
            # Otra ejecución lo creó al mismo tiempo: se usa el valor guardado.
            if e.response["Error"]["Code"] != "ParameterAlreadyExists":
                raise
            return self.ssm.get_parameter(Name=nombre, WithDecryption=True)["Parameter"]["Value"]
        return valor

    def todos(self) -> dict[str, str]:
        valores: dict[str, str] = {}
        paginador = self.ssm.get_paginator("get_parameters_by_path")
        for pagina in paginador.paginate(Path=self.raiz, Recursive=True, WithDecryption=True):
            for p in pagina["Parameters"]:
                valores[p["Name"][len(self.raiz) + 1:]] = p["Value"]
        return valores

    def borrar(self) -> int:
        nombres = list(self.todos().keys())
        for i in range(0, len(nombres), 10):
            self.ssm.delete_parameters(Names=[self.ruta(n) for n in nombres[i:i + 10]])
        return len(nombres)


# --- Comandos ----------------------------------------------------------------------
def cmd_asegurar(a: argparse.Namespace) -> int:
    alm = Almacen(a.proyecto, a.region)
    usuarios = [u.strip() for u in a.usuarios.split(",") if u.strip()]
    datos: dict = {"usuarios": {}}
    for u in usuarios:
        datos["usuarios"][u] = {
            "password": alm.asegurar(f"usuarios/{u}/password", contrasena, f"Contrasena Linux de {u}"),
            "totp": alm.asegurar(f"usuarios/{u}/totp", semilla_totp, f"Semilla TOTP (Base32) de {u}"),
            "codigos": alm.asegurar(
                f"usuarios/{u}/codigos_emergencia", codigos_emergencia, f"Codigos de emergencia TOTP de {u}"
            ).split(),
        }
    datos["celery_llaves_passphrase"] = alm.asegurar(
        "bastion/llaves_passphrase", lambda: contrasena(32), "Frase de paso de las llaves de celery"
    )
    datos["bd_migrador_password"] = alm.asegurar("bd/migrador_password", alfanumerica, "PostgreSQL edutrak_migrator")
    datos["bd_app_password"] = alm.asegurar("bd/app_password", alfanumerica, "PostgreSQL edutrak_app")
    datos["app_jwt_secret"] = alm.asegurar("app/jwt_secret", jwt, "JWT_SECRET de la API (igual en ambos Pages)")
    datos["app_admin_password"] = alm.asegurar("app/admin_password", contrasena, "Contrasena del primer ADMIN")

    if a.mascara_gha:
        # GitHub Actions oculta estos valores en cualquier log posterior.
        for valor in valores_planos(datos):
            print(f"::add-mask::{valor}")

    descriptor = os.open(a.salida, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as f:
        json.dump({"p3_secretos": datos}, f)
    print(f"Secretos listos en {a.salida} ({len(usuarios)} usuarios)", file=sys.stderr)
    return 0


def valores_planos(obj):
    if isinstance(obj, dict):
        for v in obj.values():
            yield from valores_planos(v)
    elif isinstance(obj, list):
        for v in obj:
            yield from valores_planos(v)
    elif isinstance(obj, str) and obj:
        yield obj


def cmd_mostrar(a: argparse.Namespace) -> int:
    alm = Almacen(a.proyecto, a.region)
    v = alm.todos()
    if not v:
        print("No hay secretos para este proyecto/región.", file=sys.stderr)
        return 1
    qr = None
    if a.qr:
        try:
            import qrcode  # type: ignore

            qr = qrcode
        except ImportError:
            print("(Para ver los QR: pip install qrcode)\n", file=sys.stderr)

    usuarios = sorted({k.split("/")[1] for k in v if k.startswith("usuarios/")})
    for u in usuarios:
        semilla = v.get(f"usuarios/{u}/totp", "")
        etiqueta = urllib.parse.quote(f"{EMISOR}:{u}@{a.proyecto}")
        uri = f"otpauth://totp/{etiqueta}?secret={semilla}&issuer={EMISOR}&algorithm=SHA1&digits=6&period=30"
        print(f"=== {u} ===")
        print(f"  contraseña Linux : {v.get(f'usuarios/{u}/password', '')}")
        print(f"  semilla TOTP     : {semilla}")
        print(f"  códigos emergencia: {v.get(f'usuarios/{u}/codigos_emergencia', '')}")
        print(f"  otpauth          : {uri}")
        if qr:
            q = qr.QRCode(border=1)
            q.add_data(uri)
            q.print_ascii(invert=True)
        print()
    print("=== otros ===")
    for k in sorted(v):
        if not k.startswith("usuarios/"):
            print(f"  {k}: {v[k]}")
    return 0


def cmd_borrar(a: argparse.Namespace) -> int:
    n = Almacen(a.proyecto, a.region).borrar()
    print(f"Eliminados {n} parámetros de /{a.proyecto}", file=sys.stderr)
    return 0


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="comando", required=True)
    for nombre in ("asegurar", "mostrar", "borrar"):
        s = sub.add_parser(nombre)
        s.add_argument("--proyecto", required=True)
        s.add_argument("--region", required=True)
        if nombre == "asegurar":
            s.add_argument("--usuarios", required=True, help="lista separada por comas")
            s.add_argument("--salida", required=True, help="JSON para ansible-playbook -e @archivo")
            s.add_argument("--mascara-gha", action="store_true", help="emitir ::add-mask:: de GitHub Actions")
        if nombre == "mostrar":
            s.add_argument("--qr", action="store_true", help="dibujar el QR en la terminal (requiere qrcode)")
    a = p.parse_args()
    return {"asegurar": cmd_asegurar, "mostrar": cmd_mostrar, "borrar": cmd_borrar}[a.comando](a)


if __name__ == "__main__":
    sys.exit(main())
