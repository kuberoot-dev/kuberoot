#!/bin/bash
# Unmodified official binary packaged for the explicitly declared APT lane.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/k3s-fault-build
P=$D/good-package
R=$D/public/good
mkdir -p "$P/DEBIAN" "$P/usr/local/bin" "$R/pool"
cp "$FIELD_ROOT/k3s-upgrade-a/k3s-target" "$P/usr/local/bin/k3s"
printf '265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda  %s\n' "$P/usr/local/bin/k3s" | sha256sum -c -
cat > "$P/DEBIAN/control" <<'EOF'
Package: field-edge-upgrade
Version: 1.37.2
Architecture: amd64
Maintainer: Field laboratory <field@example.invalid>
Description: Laboratory APT packaging of unchanged official k3s v1.37.2+k3s1
EOF
dpkg-deb --threads-max=2 --build -Zxz -z3 "$P" "$R/pool/field-edge-upgrade_1.37.2_amd64.deb"
(cd "$R";apt-ftparchive packages pool > Packages;apt-ftparchive release . > Release)
GNUPGHOME=$D/trusted gpg --batch --yes --clearsign --output "$R/InRelease" "$R/Release"
rm -rf -- "$P"
sha256sum "$R/pool/field-edge-upgrade_1.37.2_amd64.deb" > "$D/good-package.sha256"
