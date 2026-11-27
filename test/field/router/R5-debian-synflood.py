#!/usr/bin/env python3
"""R5 SYN flood through the Debian router (NAT profile, stock conntrack).

The client sends raw SYNs from 192.168.10.2 to 198.19.0.1, ports 20000-29999,
with new source ports each time, at a fixed rate for 60 s; the sink drops
those ports silently, so every SYN leaves a SYN_SENT conntrack entry on the
router, as a flood toward an unresponsive target does. Meanwhile: legitimate
connections client -> sink port 9100 (tcp-flows.py server), management SSH
latency to the router every second, conntrack count and CPU from /proc.
usage: R5-debian-synflood.py RATE [RATE ...]
"""
import base64, json, subprocess, sys, threading, time
from pathlib import Path

raw = Path('/work/field/raw'); out = {}

FLOOD = r'''
import socket, struct, sys, time, random
rate, seconds = int(sys.argv[1]), float(sys.argv[2])
src, dst = socket.inet_aton('192.168.10.2'), socket.inet_aton('198.19.0.1')
s = socket.socket(socket.AF_INET, socket.SOCK_RAW, socket.IPPROTO_RAW)
def csum(b):
    if len(b) % 2: b += b'\0'
    t = sum(struct.unpack('!%dH' % (len(b) // 2), b)); t = (t >> 16) + (t & 0xffff); t += t >> 16
    return ~t & 0xffff
sent = 0; t0 = time.monotonic(); sport = 1024
while time.monotonic() - t0 < seconds:
    batch_end = t0 + (sent + 500) / rate
    for _ in range(500):
        sport = sport + 1 if sport < 65000 else 1024; dport = 20000 + sent % 10000
        tcp = struct.pack('!HHIIBBHHH', sport, dport, random.getrandbits(32), 0, 5 << 4, 0x02, 64240, 0, 0)
        ph = src + dst + struct.pack('!BBH', 0, 6, len(tcp))
        tcp = tcp[:16] + struct.pack('!H', csum(ph + tcp)) + tcp[18:]
        ip = struct.pack('!BBHHHBBH4s4s', 0x45, 0, 40, random.getrandbits(16), 0, 64, 6, 0, src, dst)
        s.sendto(ip + tcp, ('198.19.0.1', 0)); sent += 1
    d = batch_end - time.monotonic()
    if d > 0: time.sleep(d)
print(sent, time.monotonic() - t0)
'''


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def guest(vm, cmd, quiet=False, data=None, timeout=120):
    if not quiet:
        say('$ guest-command', vm, cmd[:150])
    t = time.monotonic()
    p = subprocess.run(['/work/field/guest-command', vm, cmd], input=data, capture_output=True, text=True, timeout=timeout)
    p.seconds = time.monotonic() - t
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-400:], flush=True)
    return p


guest('client', 'cat >/tmp/synflood.py', data=FLOOD, quiet=True)
code = base64.b64encode(Path('/work/field/tcp-flows.py').read_bytes()).decode()
for vm in ('client', 'sink'):
    guest(vm, f'echo {code} | base64 -d >/tmp/tcp-flows.py', quiet=True)
guest('sink', "nft add table inet synsink; nft 'add chain inet synsink in { type filter hook input priority -10; policy accept; }'; "
      "nft add rule inet synsink in tcp dport 20000-29999 drop")
guest('sink', "pkill -f '[t]cp-flows.py server'; sleep 1; true")  # own session: the pattern must not match the shell that starts the server
guest('sink', "nohup python3 /tmp/tcp-flows.py server --seconds 1200 >/tmp/R5-syn-server.json 2>&1 </dev/null & sleep 2; ss -ltn | grep -c ':91'")
guest('client', 'iptables -C OUTPUT -p tcp --tcp-flags RST RST --dport 20000:29999 -j DROP 2>/dev/null || '
      'iptables -A OUTPUT -p tcp --tcp-flags RST RST --dport 20000:29999 -j DROP; true', quiet=True)
guest('debian', 'cat /proc/sys/net/netfilter/nf_conntrack_max /proc/sys/net/netfilter/nf_conntrack_tcp_timeout_syn_sent; conntrack -F 2>/dev/null; true')
for rate in [int(x) for x in sys.argv[1:]] or [5000, 20000]:
    tag = f'R5-debian-syn-{rate}'
    stop = threading.Event(); samples = []
    def sampler():
        while not stop.is_set():
            p = guest('debian', 'cat /proc/sys/net/netfilter/nf_conntrack_count; head -1 /proc/stat; grep -c . /proc/net/nf_conntrack 2>/dev/null | head -1; dmesg | grep -c "table full"', quiet=True, timeout=30)
            v = p.stdout.split()
            samples.append({'at': time.time(), 'ssh_s': round(p.seconds, 3), 'exit': p.returncode,
                            'conntrack': int(v[0]) if v and v[0].isdigit() else None, 'cpu': v[1:12] if len(v) > 11 else None,
                            'table_full_msgs': v[-1] if v else None})
            time.sleep(1)
    th = threading.Thread(target=sampler); th.start(); time.sleep(5)
    legit = subprocess.Popen(['/work/field/guest-command', 'client', 'python3 /tmp/tcp-flows.py client --count 3000 --rate 50 --seconds 70 --hold 5'],
                             stdout=(raw / f'{tag}-legit.jsonl').open('w'), stderr=subprocess.STDOUT)
    time.sleep(2)
    f = guest('client', f'python3 /tmp/synflood.py {rate} 60', timeout=120)
    legit.wait(timeout=180); time.sleep(5); stop.set(); th.join()
    (raw / f'{tag}-samples.jsonl').write_text(''.join(json.dumps(s) + '\n' for s in samples))
    res = [json.loads(l) for l in (raw / f'{tag}-legit.jsonl').read_text().splitlines() if l.startswith('{')]
    ssh = sorted(s['ssh_s'] for s in samples if s['exit'] == 0)
    sent = f.stdout.split()
    out[tag] = {'offered_syn_per_s': rate, 'syn_sent': int(sent[0]) if sent else None, 'syn_seconds': float(sent[1]) if len(sent) > 1 else None,
                'conntrack_peak': max((s['conntrack'] or 0) for s in samples), 'ssh_failures': sum(s['exit'] != 0 for s in samples),
                'ssh_p99_s': ssh[int(len(ssh) * .99) - 1] if ssh else None, 'ssh_max_s': ssh[-1] if ssh else None,
                'table_full_msgs': samples[-1]['table_full_msgs'] if samples else None, 'legit': res[-1] if res else None}
    say(tag, json.dumps(out[tag])[:800])
    (raw / 'R5-debian-synflood.summary.json').write_text(json.dumps(out, indent=2) + '\n')
    # wait for the SYN_SENT entries to expire before the next rate
    t0 = time.monotonic()
    while time.monotonic() - t0 < 200:
        c = guest('debian', 'cat /proc/sys/net/netfilter/nf_conntrack_count', quiet=True).stdout.strip()
        if c.isdigit() and int(c) < 200:
            out[tag]['recovered_to_baseline_s'] = round(time.monotonic() - t0, 1); break
        time.sleep(5)
    (raw / 'R5-debian-synflood.summary.json').write_text(json.dumps(out, indent=2) + '\n')
guest('sink', 'nft delete table inet synsink; true', quiet=True)
guest('client', 'iptables -D OUTPUT -p tcp --tcp-flags RST RST --dport 20000:29999 -j DROP; true', quiet=True)
say('done')
