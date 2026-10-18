#!/usr/bin/env python3
"""An OPC UA server playing a PLC, for trying the iot distribution against
an implementation other than the client's own (asyncua, not gopcua).

It serves the variables of a press under ns=2 by string node IDs:
Press7.Temperature (Double, tenths of a degree), Press7.Cycles (UInt32) and
Press7.Running (Boolean), with no security and with Basic256Sha256 (Sign
and SignAndEncrypt), anonymous or by username and password. A change of
the values file applies within half a second, so a demo can raise the
temperature from outside.

    pip install asyncua
    opcua_sim.py --listen opc.tcp://0.0.0.0:4840/plc --cert server.der \
        --key server.pem --user op:secret --values values.json
    echo '{"Temperature": 853}' > values.json
"""

import argparse
import asyncio
import json
import logging
import os

from asyncua import Server, ua
from asyncua.crypto.permission_rules import SimpleRoleRuleset, User, UserRole
from asyncua.server.user_managers import UserManager

VARIABLES = {
    "Temperature": (700.0, ua.VariantType.Double),
    "Cycles": (42, ua.VariantType.UInt32),
    "Running": (True, ua.VariantType.Boolean),
}


class Passwords(UserManager):
    """Lets in anonymous clients when allowed, and users by password."""

    def __init__(self, users, anonymous):
        self.users, self.anonymous = users, anonymous

    def get_user(self, iserver, username=None, password=None, certificate=None):
        if username is None:
            return User(role=UserRole.User) if self.anonymous else None
        if self.users.get(username) == password:
            return User(role=UserRole.User, name=username)
        logging.warning("refused user %s: wrong password", username)
        return None


async def follow(path, nodes):
    """Applies the values file whenever it changes."""
    seen = None
    while True:
        await asyncio.sleep(0.5)
        try:
            stamp = os.stat(path).st_mtime_ns
        except FileNotFoundError:
            continue
        if stamp == seen:
            continue
        seen = stamp
        try:
            with open(path) as f:
                values = json.load(f)
        except (OSError, ValueError) as e:
            logging.warning("values file: %s", e)
            continue
        for name, value in values.items():
            if name in nodes:
                _, vtype = VARIABLES[name]
                await nodes[name].write_value(ua.Variant(value, vtype))
                logging.info("%s = %s", name, value)


async def main():
    p = argparse.ArgumentParser()
    p.add_argument("--listen", default="opc.tcp://0.0.0.0:4840/plc")
    p.add_argument("--cert", help="the server's certificate, DER; Basic256Sha256 with it")
    p.add_argument("--key", help="its private key, PEM")
    p.add_argument("--user", action="append", default=[], help="user:password, repeated")
    p.add_argument("--no-anonymous", action="store_true")
    p.add_argument("--values", default="values.json")
    a = p.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
    logging.getLogger("asyncua").setLevel(logging.WARNING)

    users = dict(u.split(":", 1) for u in a.user)
    server = Server(user_manager=Passwords(users, not a.no_anonymous))
    await server.init()
    server.set_endpoint(a.listen)
    server.set_server_name("kuberoot plc-sim")
    # The application URI the certificate names, as clients check.
    await server.set_application_uri("urn:kuberoot:plc-sim")
    policies = [ua.SecurityPolicyType.NoSecurity]
    if a.cert:
        await server.load_certificate(a.cert)
        await server.load_private_key(a.key)
        policies += [ua.SecurityPolicyType.Basic256Sha256_Sign, ua.SecurityPolicyType.Basic256Sha256_SignAndEncrypt]
    server.set_security_policy(policies, permission_ruleset=SimpleRoleRuleset())
    tokens = [ua.UserNameIdentityToken]
    if not a.no_anonymous:
        tokens.append(ua.AnonymousIdentityToken)
    server.set_identity_tokens(tokens)

    ns = await server.register_namespace("urn:kuberoot:plc-sim:press")
    press = await server.nodes.objects.add_object(ua.NodeId("Press7", ns), "Press7")
    nodes = {}
    for name, (value, vtype) in VARIABLES.items():
        nodes[name] = await press.add_variable(ua.NodeId(f"Press7.{name}", ns), name, ua.Variant(value, vtype))
    logging.info("serving %s, ns=%d, policies %s", a.listen, ns, [x.name for x in policies])
    async with server:
        await follow(a.values, nodes)


if __name__ == "__main__":
    asyncio.run(main())
