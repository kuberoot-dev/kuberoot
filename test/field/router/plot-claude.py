#!/usr/bin/env python3
"""Charts from measured raw files only (missing data -> missing bars, never invented)."""
import glob, json, statistics
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

raw = Path('/work/field/raw'); img = Path('/work/field/img')
plt.rcParams.update({'font.size': 17, 'axes.titlesize': 18})
C = {'kuberoot': '#2a6fdb', 'vyos': '#d9822b', 'openwrt': '#2f9e44', 'debian': '#7048e8'}


def load(p):
    try:
        return json.loads(Path(p).read_text())
    except Exception:
        return None


# R1: recovery per fault
fig, ax = plt.subplots(figsize=(11, 6.5), layout='constrained')
faults = ['address', 'return-route', 'vlan', 'firewall']
kr = {'address': 'R1-address.summary.json', 'return-route': 'R1-kuberoot-return-route-normal.summary.json',
      'vlan': 'R1-kuberoot-vlan-tagged-normal.summary.json', 'firewall': 'R1-kuberoot-firewall-normal.summary.json'}
series = {'kuberoot': [(load(raw / kr[f]) or {}).get('recovered_s') for f in faults]}
for prod in ('vyos', 'openwrt'):
    series[prod] = [(load(raw / f'R1-{prod}-{f}.json') or {}).get('automatic_recovery_s') for f in faults]
w = 0.27
for i, (prod, vals) in enumerate(series.items()):
    xs = [j + (i - 1) * w for j in range(len(faults))]
    ax.bar([x for x, v in zip(xs, vals) if v], [v for v in vals if v], w, label=prod, color=C[prod])
ax.axhline(90, color='grey', ls='--', lw=1); ax.text(3.45, 92, '90 s', color='grey', fontsize=13)
ax.set_xticks(range(len(faults)), faults); ax.set_ylabel('management back, s'); ax.legend(loc='upper left')
ax.set_title('R1: lockout without confirmation, 60 s trial (Debian: no mechanism)')
fig.savefig(img / 'chart-R1-lockout-recovery.png', dpi=130); plt.close(fig)

# R6: apply and edit time vs rules
fig, axs = plt.subplots(1, 2, figsize=(13, 6.5), layout='constrained')
k6 = load(raw / 'R6-kuberoot-scale.json') or []  # field.6 run (named field6-* in the article copy)
k5 = load(raw / 'field5-20k-R6-kuberoot-scale.json') or []
v6 = load(raw / 'R6-vyos-scale.json') or []
o6 = load(raw / 'R6-openwrt-scale.json') or []
for name, rows, ak, ek in [('kuberoot (API accept)', k6 + k5, 'api_accept_s', 'edit_kernel_s'), ('vyos', v6, 'apply_s', 'edit_kernel_s'),
                           ('openwrt', o6, 'apply_s', 'edit_kernel_s')]:
    rows = sorted(rows, key=lambda r: r['count'])
    col = C[name.split()[0]]
    pts = [(r['count'], r.get(ak)) for r in rows if r.get(ak)]
    if pts:
        axs[0].plot(*zip(*pts), marker='o', label=name, color=col)
    pts = [(r['count'], r.get(ek)) for r in rows if r.get(ek)]
    if pts:
        axs[1].plot(*zip(*pts), marker='o', label=name.split()[0], color=col)
for ax, t in zip(axs, ['full replace, s', 'one-rule edit to kernel, s']):
    ax.set_xscale('log'); ax.set_yscale('log'); ax.set_xlabel('rules'); ax.set_ylabel(t); ax.grid(alpha=.3); ax.legend()
axs[1].axhline(5, color='grey', ls='--', lw=1)
fig.suptitle('R6: firewall scale (kuberoot field.5/field.6)')
fig.savefig(img / 'chart-R6-rules-apply-edit.png', dpi=130); plt.close(fig)

# R11: bytes/day
fig, ax = plt.subplots(figsize=(11, 6.5), layout='constrained')
bars = []
k = load(raw / 'R11-kuberoot.summary.json')
if k:
    a = k['A-10-per-s']; bars.append((f"kuberoot\n{a['achieved_per_s']:.1f} upd/s", a['extrapolated_bytes_per_day'] / 1e9, C['kuberoot']))
d = load(raw / 'R11-debian-write-hour-v4-partial.summary.json')
if d:
    bars.append(('debian\n10 upd/s', d['extrapolated_bytes_per_day'] / 1e9, C['debian']))
for prod in ('vyos', 'openwrt'):
    v = load(raw / f'R11-{prod}.summary.json')
    if v and 'commit_loop' in v:
        c = v['commit_loop']; bars.append((f"{prod}\n{c['rate_per_s']:.2f} commit/s", c['extrapolated_bytes_per_day_at_achieved_rate'] / 1e9, C[prod]))
if bars:
    ax.bar([b[0] for b in bars], [b[1] for b in bars], color=[b[2] for b in bars])
    for i, b in enumerate(bars):
        ax.text(i, b[1], f'{b[1]:.0f}', ha='center', va='bottom')
ax.set_ylabel('guest disk writes, GB/day (extrapolated)'); ax.set_title('R11: writes at the highest config-update rate each product took')
fig.savefig(img / 'chart-R11-writes-per-day.png', dpi=130); plt.close(fig)

# R-B2: TCP throughput
rows = {}
for f in glob.glob(str(raw / 'R-B2-*.jsonl')):
    for l in open(f):
        try:
            x = json.loads(l)
        except Exception:
            continue
        e = (x.get('iperf') or {}).get('end', {})
        bps = (e.get('sum_received') or {}).get('bits_per_second')
        if x.get('mode') != 'tcp' or not bps or x['product'] == 'linux-fabric':
            continue
        prof = x['profile'].replace('-resumed', '')
        rows.setdefault((x['product'], prof), []).append(bps / 1e6)
fig, ax = plt.subplots(figsize=(12, 6.5), layout='constrained')
profs = ['routing', 'nat', 'stateful']; prods = [p for p in ('kuberoot', 'r2', 'vyos', 'openwrt', 'debian') if any(k[0] == p for k in rows)]
w = 0.8 / max(1, len(prods))
for i, p in enumerate(prods):
    vals = [statistics.median(rows[(p, pr)]) if (p, pr) in rows else 0 for pr in profs]
    name = 'kuberoot' if p == 'r2' else p
    ax.bar([j + i * w for j in range(3)], vals, w, label=name, color=C.get(name))
ax.axhline(1000, color='grey', ls='--', lw=1); ax.text(0, 1010, 'direct client-sink ceiling ~1000', color='grey', fontsize=13)
ax.set_xticks([j + w * (len(prods) - 1) / 2 for j in range(3)], profs); ax.set_ylabel('TCP P8, Mbit/s (median, both directions)')
ax.set_title('R-B2: relative throughput in the same QEMU socket fabric (not line rate)'); ax.legend()
fig.savefig(img / 'chart-R-B2-products.png', dpi=130); plt.close(fig)
print('charts written')
