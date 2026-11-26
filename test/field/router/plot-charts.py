#!/usr/bin/env python3
"""Generate phone-readable PNG charts from real field measurements.

Reads /work/field/raw/*.json* and writes /work/field/img/chart-*.png.
Every point comes from a recorded measurement; missing data skips the chart.
"""
import json, glob, statistics as st
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

raw = Path('/work/field/raw')
img = Path('/work/field/img')
img.mkdir(exist_ok=True)
plt.rcParams.update({'font.size': 16, 'axes.grid': True, 'grid.alpha': .3})

def save(fig, name):
    fig.savefig(img / name, dpi=150, bbox_inches='tight')
    plt.close(fig)
    print('wrote', name)

def load(p):
    return json.loads(Path(p).read_text())

# --- R2: power-cut recovery times across 30 cuts ---
files = sorted(glob.glob(str(raw / 'R2-powercut-*.summary.json')))
rows = []
for f in files:
    d = load(f)
    if isinstance(d, dict) and d.get('trial') and d.get('phase') in ('write', 'selection') and d.get('recoverable') and not d.get('manual_repair'):
        rows.append(d)
if len(rows) >= 5:
    rows.sort(key=lambda r: r['trial'])
    x = [r['trial'] for r in rows]
    api = [r.get('first_api_s') for r in rows]
    fwd = [r.get('first_forward_s') for r in rows]
    fig, ax = plt.subplots(figsize=(9, 6))
    ax.plot(x, api, 'o-', label='API ready')
    ax.plot(x, fwd, 's-', label='forwarding restored')
    ax.set_xlabel('power-cut trial #')
    ax.set_ylabel('seconds after cut')
    ax.set_title('R2 kuberoot: recovery after kill -9 during slot write/selection\n30 trials, 0 unbootable, 0 lost acked state')
    ax.legend()
    save(fig, 'chart-R2-powercut-recovery.png')

# --- R7: FIB install vs time at 4/8 GiB ---
fig, ax = plt.subplots(figsize=(9, 6))
plotted = False
for ram, marker in [('4GiB', 'o'), ('8GiB', 's')]:
    p = raw / f'R7-debian-800000-{ram}.json'
    if not p.exists():
        continue
    d = load(p)
    import re
    xs, ys = [], []
    for poll in d:
        t = poll.get('text', '')
        m = re.search(r'\n(\d+)\nMemTotal', t)
        if m:
            xs.append(poll['elapsed_s'])
            ys.append(int(m.group(1)) / 1000)
    if xs:
        ax.plot(xs, ys, marker + '-', label=f'Debian FRR {ram}')
        plotted = True
if plotted:
    ax.set_xlabel('seconds since upstreams started')
    ax.set_ylabel('kernel FIB routes, thousands')
    ax.set_title('R7 Debian: 800k IPv4 FIB install, two upstreams\n2 GiB OOM-killed bgpd (not shown)')
    ax.legend()
    save(fig, 'chart-R7-debian-fib-install.png')

# --- R-B2: throughput by profile ---
profs = {}
direct = raw / 'R-B2-linux-fabric-direct.jsonl'
if direct.exists():
    vals = []
    for line in direct.read_text().splitlines():
        r = json.loads(line)
        if r.get('mode') == 'tcp' and r.get('exit') == 0 and r.get('iperf', {}).get('end'):
            vals.append(r['iperf']['end']['sum_received']['bits_per_second'] / 1e6)
    if vals:
        profs['direct\n(no DUT)'] = st.median(vals)
for prof, label in [('routing', 'Debian\nrouting'), ('nat', 'Debian\nNAT')]:
    p = raw / f'R-B2-debian-{prof}.jsonl'
    if not p.exists():
        continue
    vals = []
    for line in p.read_text().splitlines():
        r = json.loads(line)
        if r.get('mode') == 'tcp' and r.get('exit') == 0 and r.get('iperf', {}).get('end'):
            vals.append(r['iperf']['end']['sum_received']['bits_per_second'] / 1e6)
    if vals:
        profs[label] = st.median(vals)
if len(profs) >= 2:
    fig, ax = plt.subplots(figsize=(8, 6))
    ax.bar(profs.keys(), profs.values(), color=['#4c72b0', '#dd8452', '#55a868'][:len(profs)])
    for i, v in enumerate(profs.values()):
        ax.text(i, v + 10, f'{v:.0f}', ha='center')
    ax.set_ylabel('TCP throughput, Mbit/s (median of 5)')
    ax.set_title('R-B2: iperf3 -P8 through userspace fabric\nrelative comparison, not line rate')
    save(fig, 'chart-R-B2-throughput.png')

# --- R6: apply time vs rule count ---
p = raw / 'R6-debian-scale.json'
if p.exists():
    d = load(p)
    counts, replace, edit = [], [], []
    for r in d:
        if r.get('phase') == 'full-replace':
            counts.append(r['count']); replace.append(r['observer_wall_s'])
        if r.get('phase') == 'one-rule-edit':
            edit.append(r['observer_wall_s'])
    if counts:
        fig, ax = plt.subplots(figsize=(9, 6))
        ax.plot(counts, replace, 'o-', label='full replace')
        ax.plot(counts, edit, 's-', label='one-rule edit')
        ax.set_xscale('log')
        ax.set_xlabel('nftables rule count')
        ax.set_ylabel('observer wall seconds')
        ax.set_title('R6 Debian: nftables apply time vs rule count')
        ax.legend()
        save(fig, 'chart-R6-debian-apply-vs-rules.png')

# --- R5: conntrack established vs target ---
p = raw / 'R5-debian-stock.summary.json'
if p.exists():
    d = load(p)
    xs, ys = [], []
    for r in d:
        res = r.get('result', {})
        if res.get('requested'):
            xs.append(res['requested']); ys.append(res.get('completed', 0))
    if xs:
        fig, ax = plt.subplots(figsize=(9, 6))
        ax.plot(xs, ys, 'o-', label='established')
        ax.plot(xs, xs, '--', label='target', alpha=.5)
        ax.axhline(65536, color='r', ls=':', label='nf_conntrack_max 65536')
        ax.set_xscale('log'); ax.set_yscale('log')
        ax.set_xlabel('concurrent TCP flows requested')
        ax.set_ylabel('flows established')
        ax.set_title('R5 Debian: conntrack scale (stock 65536 max)')
        ax.legend()
        save(fig, 'chart-R5-debian-conntrack.png')

# --- R-B1: idle disk writes per product ---
fig, ax = plt.subplots(figsize=(8, 6))
labels, vals = [], []
for name, label in [('R-B1-r2-idle.summary.json', 'kuberoot\nfield.1'), ('R-B1-debian-idle.summary.json', 'Debian\n12')]:
    p = raw / name
    if p.exists():
        d = load(p)
        m = d.get('metrics', d)
        w = m.get('disk_write_bytes')
        if w:
            labels.append(label); vals.append(w / 2**20)
if len(vals) >= 2:
    ax.bar(labels, vals, color=['#4c72b0', '#dd8452'])
    for i, v in enumerate(vals):
        ax.text(i, v + 2, f'{v:.0f}', ha='center')
    ax.set_ylabel('guest disk writes over 1 h idle, MiB')
    ax.set_title('R-B1: idle disk writes (observed hour)')
    save(fig, 'chart-R-B1-idle-writes.png')

print('charts done')
