# Sourced by build-rootfs.sh: llama-server, the model server, which
# build-llama.sh built.
. "$ROOT/build/components.env"
install -m 0755 "$OUT/llama-server-$LLAMA_CPP_VERSION" $r/usr/bin/llama-server
# The NVIDIA driver, which build-nvidia.sh built: signed modules, firmware,
# the CUDA driver library, NVML and nvidia-smi. amd64 only: build-nvidia.sh
# builds nothing for arm64.
if [ -d "$OUT/nvidia" ]; then cp -a "$OUT/nvidia/." $r/; fi
