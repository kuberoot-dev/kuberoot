# kubepkg moved to kuberoot-dev (2026-10-12)

What changes for kuberoot, and until when the old addresses work.

| What | New | Old (works until) |
|---|---|---|
| Go module | `github.com/kuberoot-dev/kubepkg`, from **v1.2.0** | `github.com/tym83/kubepkg` up to v1.1.0 (old tags stay; no new releases there) |
| Index | https://kuberoot-dev.github.io/kubepkg-recipes/index.yaml | https://tym83.github.io/kubepkg-recipes/index.yaml — the same signed index, byte for byte, refreshed by every publish until **2027-01-10**, then removed |
| Index key | unchanged (`keys/index.pub` in kubepkg-recipes) | — |
| Docs | https://kuberoot-dev.github.io/kubepkg/ | tym83.github.io/kubepkg redirects |
| Operator image | `ghcr.io/kuberoot-dev/kubepkg-operator` | `ghcr.io/tym83/kubepkg-operator` up to 1.1.0, stays |
| Operator chart | `oci://ghcr.io/kuberoot-dev/charts/kubepkg` | `oci://ghcr.io/tym83/charts/kubepkg` up to 1.1.0, stays |
| Packages | `ghcr.io/kuberoot-dev/kubepkg-packages/...` | `ghcr.io/tym83/kubepkg-packages/...` stays for installed clusters |
| Git | github.com/kuberoot-dev/kubepkg, github.com/kuberoot-dev/kubepkg-recipes | old URLs redirect |

## What to change in kuberoot

1. Go imports: replace the path and bump, no `replace` directive needed:
   `go get github.com/kuberoot-dev/kubepkg@v1.2.0`, then
   `grep -rl github.com/tym83/kubepkg --include='*.go' . | xargs sed -i 's#github.com/tym83/kubepkg#github.com/kuberoot-dev/kubepkg#g'`.
2. kinit default repository URL: `https://kuberoot-dev.github.io/kubepkg-recipes/index.yaml`. Same public key.
3. Operator image and chart references: `ghcr.io/kuberoot-dev/...`, version 1.2.0.

Before 2027-01-10 a cluster that still points at the old index address keeps working and receives every new package version.

## What a running cluster will see

Published versions moved to the new registry unchanged (same chart digests). Their location in the index changed, so the spec digest of every installed package changes: the operator records one new revision per package with the same charts. Helm sees no change in the charts, and pods are not restarted. Checked on the kubepkg test hub on 2026-10-12: cert-manager, cdi and kubevirt each got one new revision, every pod except the operator's own kept running.

## Air-gapped mirrors

Mirror paths follow the registry, so a mirror made from the old index does not match the new one. Make a new bundle (`kubepkg bundle`) from the new index and import it.
