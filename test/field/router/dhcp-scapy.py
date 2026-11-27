#!/usr/bin/env python3
"""DHCPv4 clients on one interface with chosen MACs and client IDs.

usage: dhcp-scapy.py dora IFACE CLIENTS.json [--timeout S] [--spacing S]
       dhcp-scapy.py release IFACE LEASES.json SERVER
CLIENTS.json: [{"mac": "...", "cid": "hex or null"}]; LEASES.json: [{"mac","address","cid"}].
Prints one JSON document: per client the offered and acknowledged address,
NAKs, and seconds from DISCOVER to ACK.
"""
import json, random, sys, threading, time
from scapy.all import AsyncSniffer, BOOTP, DHCP, IP, UDP, Ether, conf, sendp

conf.verb = 0
mode, iface, path = sys.argv[1:4]
args = sys.argv[4:]


def opt(name, default=None):
    return float(args[args.index(name) + 1]) if name in args else default


def chaddr(mac):
    return bytes.fromhex(mac.replace(':', '')) + b'\0' * 10


def options(base, cid):
    o = list(base)
    if cid:
        o.append(('client_id', bytes.fromhex(cid)))
    return o + ['end']


def frame(mac, xid, opts, ciaddr='0.0.0.0', src='0.0.0.0', dst='255.255.255.255'):
    return (Ether(src=mac, dst='ff:ff:ff:ff:ff:ff') / IP(src=src, dst=dst) / UDP(sport=68, dport=67) /
            BOOTP(op=1, xid=xid, ciaddr=ciaddr, chaddr=chaddr(mac), flags=0x8000) / DHCP(options=opts))


def dopt(pkt, key):
    for o in pkt[DHCP].options:
        if isinstance(o, tuple) and o[0] == key:
            return o[1]


if mode == 'release':
    server = args[0]
    for l in json.load(open(path)):
        sendp(frame(l['mac'], random.getrandbits(32), options([('message-type', 'release'), ('server_id', server)], l.get('cid')),
                    ciaddr=l['address'], src=l['address'], dst=server), iface=iface)
        time.sleep(.02)
    print(json.dumps({'released': len(json.load(open(path)))}))
    sys.exit(0)

clients = json.load(open(path)); timeout = opt('--timeout', 40); spacing = opt('--spacing', .01); retry = opt('--retry', 5)
# A real client: DISCOVER, REQUEST the offer; on NAK or no answer within --retry
# seconds start over with a new transaction ID (RFC 2131 4.1, 4.4.1).
state = {}; byxid = {}; lock = threading.Lock(); t0 = time.monotonic()
for c in clients:
    state[c['mac']] = {'mac': c['mac'], 'cid': c.get('cid'), 'offer': None, 'ack': None, 'nak': 0, 'server': None,
                       'discovers': 0, 'offers_seen': [], 'phase': 'idle'}


def discover(s):
    xid = random.getrandbits(32); byxid[xid] = s
    s.update(xid=xid, phase='selecting', offer=None, step_at=time.monotonic(), discovers=s['discovers'] + 1)
    s.setdefault('sent', time.monotonic())
    sendp(frame(s['mac'], xid, options([('message-type', 'discover')], s['cid'])), iface=iface)


def on_reply(pkt):
    if not pkt.haslayer(DHCP) or pkt[BOOTP].op != 2:
        return
    s = byxid.get(pkt[BOOTP].xid)
    if not s or pkt[BOOTP].xid != s.get('xid'):
        return
    kind = dopt(pkt, 'message-type')
    with lock:
        if kind == 2 and s['phase'] == 'selecting':
            s['offer'] = pkt[BOOTP].yiaddr; s['server'] = dopt(pkt, 'server_id'); s['offers_seen'].append(s['offer'])
            s['phase'] = 'requesting'; s['step_at'] = time.monotonic()
            sendp(frame(s['mac'], s['xid'], options([('message-type', 'request'), ('server_id', s['server']),
                                                     ('requested_addr', s['offer'])], s['cid'])), iface=iface)
        elif kind == 5 and s['phase'] == 'requesting':
            s['ack'] = pkt[BOOTP].yiaddr; s['ack_s'] = time.monotonic() - s['sent']; s['phase'] = 'bound'
        elif kind == 6 and s['phase'] == 'requesting':
            s['nak'] += 1; s['phase'] = 'idle'


sn = AsyncSniffer(iface=iface, filter='udp and (port 67 or port 68)', prn=on_reply, store=False); sn.start(); time.sleep(1)
for s in state.values():
    with lock:
        discover(s)
    time.sleep(spacing)
while time.monotonic() - t0 < timeout and any(s['phase'] != 'bound' for s in state.values()):
    now = time.monotonic()
    for s in state.values():
        with lock:
            if s['phase'] == 'idle' or (s['phase'] in ('selecting', 'requesting') and now - s['step_at'] > retry):
                discover(s)
    time.sleep(.2)
time.sleep(1); sn.stop()
res = [{k: v for k, v in s.items() if k not in ('xid', 'step_at')} for s in state.values()]
acks = [s['ack'] for s in res if s['ack']]
print(json.dumps({'clients': len(res), 'acked': len(acks), 'distinct_acked': len(set(acks)), 'naks': sum(s['nak'] for s in res),
                  'discovers': sum(s['discovers'] for s in res), 'last_ack_s': max((s.get('ack_s', 0) for s in res), default=None),
                  'wall_s': time.monotonic() - t0, 'results': res}))
