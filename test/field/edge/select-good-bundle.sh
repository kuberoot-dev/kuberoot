#!/bin/bash
# Source to select an existing genuine, signed, non-faulty release bundle.
# The server exposes hardlinks; signature files remain byte-for-byte original.
case "${1:?main|release|release-testkey}" in
 main) bundle=/work/field-edge/build-main/out/amd64/bundles/kuberoot-0.1.0-field-main.1b29543-amd64.tar ;;
 release-testkey) bundle=/work/www/kuberoot-0.1.0-capture.2-amd64.tar ;;
 release) bundle=/work/field-edge/downloads/kuberoot-0.1.0-amd64.tar ;;
 *) return 2 ;;
esac
# Missing official release must be downloaded from its published release asset;
# this helper never re-signs it or silently substitutes the laboratory key.
test -s "$bundle" && test -s "$bundle.sig" || { printf 'Missing genuine signed bundle: %s\n' "$bundle" >&2; return 2; }
mkdir -p /work/field-edge/www
for extension in '' .sig; do
 target=/work/field-edge/www/$(basename "$bundle")$extension
 if ! test -e "$target"; then ln "$bundle$extension" "$target";else cmp "$bundle$extension" "$target";fi
done
export GOOD_BUNDLE_URL=http://10.0.2.2:15004/$(basename "$bundle")
export GOOD_BUNDLE_SHA256=$(sha256sum "$bundle" | cut -d' ' -f1)
