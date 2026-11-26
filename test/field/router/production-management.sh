#!/bin/bash
# Create private SSH configuration; keys remain on the stand PVC.
set -euo pipefail
umask 077
cat > /work/field/private/production-ssh.conf <<'CONFIG'
Host field-observer
 HostName 127.0.0.1
 Port 2233
 User root
Host debian-production openwrt-production
 HostName 198.18.0.1
 Port 22
 User root
 ProxyJump field-observer
Host vyos-production
 HostName 198.18.0.1
 Port 22
 User vyos
 ProxyJump field-observer
Host *
 IdentityFile /work/field/private/id_ed25519
 IdentitiesOnly yes
 BatchMode yes
 ConnectTimeout 3
 StrictHostKeyChecking no
 UserKnownHostsFile /dev/null
 LogLevel ERROR
CONFIG
ssh -F /work/field/private/production-ssh.conf debian-production 'date -u; printf "SSH peer: %s\n" "$SSH_CONNECTION"; ip route get 198.18.34.2'
