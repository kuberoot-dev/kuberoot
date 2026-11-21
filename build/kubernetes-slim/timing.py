#!/usr/bin/env python3
"""Reversible, exact-match timing overlay for the pinned Kubernetes release.

Hooks receive the server lifecycle context already. Their inner poll deadlines
must be extended at the call site; wrapping the outer context cannot do that.
The helper reads only a service environment variable, never /proc/cmdline, so
kubectl and user workloads retain their normal semantics.
"""
from pathlib import Path
import sys

root = Path(sys.argv[1])
helper = root / 'staging/src/k8s.io/client-go/util/kuberootclock/clock.go'
patches = {
 'pkg/kubelet/kubelet.go': [
  ('kl.clock.Since(start) >= nodeReadyGracePeriod', 'kl.clock.Since(start) >= kuberootclock.Scale(nodeReadyGracePeriod)'),
 ],
 'cmd/kubelet/app/server.go': [
  ('net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}', 'net.Dialer{Timeout: kuberootclock.Scale(30 * time.Second), KeepAlive: 30 * time.Second}'),
 ],
 'staging/src/k8s.io/client-go/transport/cache.go': [
  ('Timeout:   30 * time.Second,', 'Timeout:   kuberootclock.Scale(30 * time.Second),'),
  ('TLSHandshakeTimeout: 10 * time.Second,', 'TLSHandshakeTimeout: kuberootclock.Scale(10 * time.Second),'),
 ],
 'staging/src/k8s.io/apiserver/pkg/server/secure_serving.go': [
  ('ReadHeaderTimeout: 32 * time.Second,', 'ReadHeaderTimeout: kuberootclock.Scale(32 * time.Second),'),
 ],
 'staging/src/k8s.io/apiserver/pkg/storage/storagebackend/factory/etcd3.go': [
  ('DialTimeout:          dialTimeout,', 'DialTimeout:          kuberootclock.Scale(dialTimeout),'),
  ('DialKeepAliveTime:    keepaliveTime,', 'DialKeepAliveTime:    kuberootclock.Scale(keepaliveTime),'),
  ('DialKeepAliveTimeout: keepaliveTimeout,', 'DialKeepAliveTimeout: kuberootclock.Scale(keepaliveTimeout),'),
  ('blockUntilReady(dialTimeout, kClient)', 'blockUntilReady(kuberootclock.Scale(dialTimeout), kClient)'),
 ],
 'pkg/registry/core/rest/storage_core.go': [
  ('case <-time.After(time.Minute):', 'case <-time.After(kuberootclock.Scale(time.Minute)):'),
 ],
 'pkg/registry/flowcontrol/rest/storage_flowcontrol.go': [
  ('contextFromChannelAndMaxWaitDuration(hookContext.Done(), 5*time.Minute)', 'contextFromChannelAndMaxWaitDuration(hookContext.Done(), kuberootclock.Scale(5*time.Minute))'),
 ],
 'pkg/registry/rbac/rest/storage_rbac.go': [
  ('wait.Poll(1*time.Second, 30*time.Second,', 'wait.Poll(1*time.Second, kuberootclock.Scale(30*time.Second),'),
 ],
 'pkg/registry/scheduling/rest/storage_scheduling.go': [
  ('wait.Poll(1*time.Second, 30*time.Second,', 'wait.Poll(1*time.Second, kuberootclock.Scale(30*time.Second),'),
 ],
 'pkg/registry/core/service/portallocator/controller/repair.go': [
  ('time.Second, 30*time.Second, true,', 'time.Second, kuberootclock.Scale(30*time.Second), true,'),
 ],
 'pkg/registry/core/service/ipallocator/controller/repair.go': [
  ('time.Second, 30*time.Second, true,', 'time.Second, kuberootclock.Scale(30*time.Second), true,'),
 ],
 'pkg/controlplane/apiserver/options/options.go': [
  ('context.WithTimeout(ctx, 10*time.Second)', 'context.WithTimeout(ctx, kuberootclock.Scale(10*time.Second))'),
 ],
}
# Mutating handlers impose a second 34s ceiling under --request-timeout.
# Keep explicit shorter client contexts authoritative, but scale this ceiling.
for operation in ('create', 'update', 'patch', 'delete'):
 patches[f'staging/src/k8s.io/apiserver/pkg/endpoints/handlers/{operation}.go'] = [
  ('context.WithTimeout(ctx, requestTimeoutUpperBound)',
   'context.WithTimeout(ctx, kuberootclock.Scale(requestTimeoutUpperBound))'),
 ]

if len(sys.argv) == 3 and sys.argv[2] == '--restore':
 for name in patches:
  p = root / name
  backup = p.with_suffix(p.suffix + '.kuberoot-original')
  if backup.exists():
   p.write_bytes(backup.read_bytes())
   backup.unlink()
 if helper.exists():
  helper.unlink()
  helper.parent.rmdir()
 sys.exit(0)

# Validate the entire patch before touching any source.
outputs = {}
for name, changes in patches.items():
 p = root / name
 if p.with_suffix(p.suffix + '.kuberoot-original').exists():
  raise SystemExit(f'unrestored overlay: {p}')
 s = p.read_text()
 for old, new in changes:
  if s.count(old) != 1:
   raise SystemExit(f'timing overlay no longer matches {name}: {old!r}')
  s = s.replace(old, new)
 if s.count('import (') != 1:
  raise SystemExit(f'timing overlay import no longer matches {name}')
 outputs[p] = s.replace('import (', 'import (\n kuberootclock "k8s.io/client-go/util/kuberootclock"')
if helper.exists():
 raise SystemExit(f'helper already exists: {helper}')
for p, s in outputs.items():
 p.with_suffix(p.suffix + '.kuberoot-original').write_bytes(p.read_bytes())
 p.write_text(s)
helper.parent.mkdir(parents=True)
helper.write_text('''package kuberootclock
import ("os"; "strconv"; "time")
var factor = func() time.Duration {
 n, err := strconv.Atoi(os.Getenv("KUBEROOT_TIMESCALE"))
 if err != nil || n < 1 || n > 1000 { return 1 }
 return time.Duration(n)
}()
func Scale(d time.Duration) time.Duration { return d * factor }
''')
