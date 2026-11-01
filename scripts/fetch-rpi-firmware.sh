#!/bin/sh
# Fetch the two Raspberry Pi 4 firmware blobs the boot medium carries.
#
# Usage: scripts/fetch-rpi-firmware.sh <arch>
#
# The Pi 4's EEPROM is only a first stage: it then loads start4.elf and
# fixup4.dat off the boot partition, so a medium without them stops at a blank
# screen with nothing on the console. The Pi 5 keeps its whole firmware in
# EEPROM and needs neither, but one medium serves both boards, so both travel.
#
# These are not ours to build and not open source -- they are redistributable
# binaries from raspberrypi/firmware. Pinned to a commit rather than a branch,
# because the firmware is the one piece of the boot path this tree cannot
# reproduce: if it changed under us, nothing here would say so, and a board
# that stopped booting would look like a kernel problem.
#
# Fetched once and kept: a build that reaches for the network every time is a
# build that fails when the network, or someone else's server, says no.
set -eu

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH/firmware

# raspberrypi/firmware, release 1.20260915.
COMMIT=6f0881cba8bea8ec24956a5718714730e9936ec5
BASE="https://raw.githubusercontent.com/raspberrypi/firmware/$COMMIT/boot"

mkdir -p "$OUT"
for f in start4.elf fixup4.dat; do
  if [ -s "$OUT/$f" ]; then
    echo "have $f"
    continue
  fi
  echo "fetching $f"
  curl -fsSL "$BASE/$f" -o "$OUT/$f.new"
  # A proxy or a captive portal answers with a page, not a refusal, and a
  # zero-length or HTML "firmware" file fails much later and far less legibly.
  [ -s "$OUT/$f.new" ] || { echo "$f came back empty" >&2; exit 1; }
  case "$(head -c 1 "$OUT/$f.new" | od -An -tx1 | tr -d ' ')" in
    3c) echo "$f came back as markup, not a binary" >&2; exit 1 ;;
  esac
  mv "$OUT/$f.new" "$OUT/$f"
done
ls -l "$OUT"
