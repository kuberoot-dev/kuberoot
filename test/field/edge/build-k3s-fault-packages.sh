#!/bin/bash
# The upstream k3s B binary is unchanged and checksum-verified. A lab Debian
# package adds the intentional systemd fault and installs B for the next boot.
# This is declared test packaging, not an upstream k3s .deb distribution.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
A=$FIELD_ROOT/k3s-upgrade-a
D=$FIELD_ROOT/k3s-fault-build
mkdir "$D"
mkdir -p /work/apt/archives/partial /work/apt/lists/partial
apt-get -o Dir::Cache::archives=/work/apt/archives -o Dir::State::lists=/work/apt/lists update
apt-get -o Dir::Cache::archives=/work/apt/archives -o Dir::State::lists=/work/apt/lists install -y --no-install-recommends apt-utils gnupg
for signer in trusted foreign; do
 mkdir -m700 "$D/$signer"
 GNUPGHOME=$D/$signer gpg --batch --passphrase '' --quick-generate-key "Field laboratory $signer <field@example.invalid>" ed25519 sign 0
 GNUPGHOME=$D/$signer gpg --export > "$D/$signer-public.gpg"
done
for mode in init-crash pod-network wrong-content incompatible-state; do
 P=$D/$mode/package
 R=$D/public/$mode
 mkdir -p "$P/DEBIAN" "$P/usr/local/bin" "$P/usr/local/libexec" "$P/lib/systemd/system" "$R/pool"
 cp "$A/k3s-target" "$P/usr/local/bin/k3s"
 cp "$A/fixture-agent" "$P/usr/local/libexec/field-fault-agent"
 cat > "$P/DEBIAN/control" <<EOF
Package: field-edge-trial
Version: 0.1.0
Architecture: amd64
Maintainer: Field laboratory <field@example.invalid>
Description: TEST ONLY upstream k3s v1.37.2+k3s1 plus deliberate $mode fault
EOF
 cat > "$P/lib/systemd/system/field-fault.service" <<EOF
[Unit]
Description=Intentionally faulty field-test service ($mode)
After=local-fs.target network-online.target
Wants=network-online.target
[Service]
ExecStart=/usr/local/libexec/field-fault-agent $mode
Restart=always
RestartSec=1
[Install]
WantedBy=multi-user.target
EOF
 cat > "$P/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
systemctl daemon-reload
systemctl enable field-fault.service
# The installed fault and new k3s version start only at the requested reboot.
EOF
 chmod 755 "$P/DEBIAN/postinst"
 dpkg-deb --threads-max=2 --build -Zxz -z3 "$P" "$R/pool/field-edge-trial_0.1.0_amd64.deb"
 # Reproducible staging payload only; final .deb and binary inputs remain.
 rm -rf -- "$P"
done
for mode in tampered untrusted; do
 mkdir -p "$D/public/$mode/pool"
 cp "$D/public/wrong-content/pool/field-edge-trial_0.1.0_amd64.deb" "$D/public/$mode/pool/"
done
for mode in init-crash pod-network wrong-content incompatible-state tampered untrusted; do
 R=$D/public/$mode
 (cd "$R"; apt-ftparchive packages pool > Packages; apt-ftparchive release . > Release)
 signer=trusted;[[ $mode != untrusted ]] || signer=foreign
 GNUPGHOME=$D/$signer gpg --batch --yes --clearsign --output "$R/InRelease" "$R/Release"
done
# Corrupt the published package after signing its original hash in the index.
python3 - "$D/public/tampered/pool/field-edge-trial_0.1.0_amd64.deb" <<'PY'
import pathlib,sys
p=pathlib.Path(sys.argv[1])
with p.open('r+b') as f:f.seek(-1,2);b=f.read(1);f.seek(-1,2);f.write(bytes([b[0]^1]))
PY
find "$D/public" -type f -exec sha256sum {} + > "$D/public-sha256.txt"
nohup python3 -m http.server 15007 --bind 0.0.0.0 --directory "$D/public" > "$D/http.log" 2>&1 </dev/null &
echo "$!" > "$D/http.pid"
sleep 1
curl --fail --silent http://127.0.0.1:15007/init-crash/InRelease >/dev/null
