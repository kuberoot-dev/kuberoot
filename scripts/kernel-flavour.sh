# Sourced by the kernel and module builds: the kernel a distribution boots.
# A distribution with distros/<name>/kernel.config has a flavour of its own,
# named after it, built in a directory of its own; the others share the
# base kernel.
KFLAVOUR=""
if [ -n "${KUBEROOT_DISTRO:-}" ] && [ -f "$ROOT/distros/$KUBEROOT_DISTRO/kernel.config" ]; then
  KFLAVOUR=$KUBEROOT_DISTRO
fi

# kernel_build_dir <work> <kernel arch>: where the flavour's objects live.
kernel_build_dir() { echo "$1/build-$2${KFLAVOUR:+-$KFLAVOUR}"; }

# kernel_cross <kernel arch>: builds for another architecture than the
# builder's go through a cross compiler, installed on first use.
kernel_cross() {
  local pkg=""
  case "$1:$(uname -m)" in
    x86_64:x86_64 | arm64:aarch64) return 0 ;;
    arm64:*) export CROSS_COMPILE=aarch64-linux-gnu-; pkg=gcc-aarch64-linux-gnu ;;
    x86_64:*) export CROSS_COMPILE=x86_64-linux-gnu-; pkg=gcc-x86-64-linux-gnu ;;
  esac
  command -v "${CROSS_COMPILE}gcc" >/dev/null || { apt-get -qq update && apt-get -qq install -y "$pkg" >/dev/null; }
}
