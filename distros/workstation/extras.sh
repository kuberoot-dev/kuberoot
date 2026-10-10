# Sourced by build-rootfs.sh: what the hypervisor distribution brings
# (cloud-hypervisor and its firmware), and noVNC, the browser client the
# gateway serves.
. "$ROOT/distros/hypervisor/extras.sh"
fetch "https://github.com/novnc/noVNC/archive/refs/tags/$NOVNC_VERSION.tar.gz" "novnc-$NOVNC_VERSION.tgz"
mkdir -p $r/usr/share/kuberoot/novnc
tar -xzf "$CACHE/novnc-$NOVNC_VERSION.tgz" -C $r/usr/share/kuberoot/novnc --strip-components=1 \
  "noVNC-${NOVNC_VERSION#v}/vnc.html" "noVNC-${NOVNC_VERSION#v}/app" "noVNC-${NOVNC_VERSION#v}/core" \
  "noVNC-${NOVNC_VERSION#v}/vendor" "noVNC-${NOVNC_VERSION#v}/LICENSE.txt"
