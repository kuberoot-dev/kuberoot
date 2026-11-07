#!/usr/bin/env python3
"""Export the reviewed provider sources without requiring a moving Git ref."""

import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    directories = (
        "cmd/cluster-api-provider-kuberoot",
        "pkg/capikuberoot",
        "pkg/apis/infrastructure/v1alpha1",
        "pkg/apis/vm/v1alpha2",
    )
    names = [
        "go.mod",
        "go.sum",
        "LICENSE",
        "config/capi/Dockerfile",
        # Retain the generated fixtures used by the exported API tests.
        "distros/hypervisor/addons/vm.kuberoot.dev_virtualmachines.yaml",
    ]
    for kind in ("kuberootclusters", "kuberootmachines", "kuberootmachinetemplates"):
        names.append(f"config/capi/crd/infrastructure.cluster.x-k8s.io_{kind}.yaml")
    for directory in directories:
        files = sorted((root / directory).glob("*.go"))
        if not files:
            raise SystemExit(f"missing provider sources: {directory}")
        names.extend(str(path.relative_to(root)) for path in files)
    contents = {}
    for name in sorted(names):
        path = root / name
        if path.is_symlink() or not path.is_file():
            raise SystemExit(f"source must be a regular file: {name}")
        contents[name] = path.read_bytes()
    for api in (directories[2], directories[3]):
        if f"{api}/zz_generated.deepcopy.go" not in contents:
            raise SystemExit(f"run code generation before exporting {api}")
    provenance = {
        "format": 1,
        "repository": "github.com/kuberoot-dev/kuberoot",
        "base_commit": subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=root, text=True
        ).strip(),
        # The hashes identify the exact checkout contents, including local work.
        # A base commit alone must never imply that these bytes were committed.
        "source_files_sha256": {
            name: hashlib.sha256(data).hexdigest() for name, data in contents.items()
        },
    }
    contents["SOURCE-PROVENANCE.json"] = (
        json.dumps(provenance, sort_keys=True, indent=2) + "\n"
    ).encode()
    archive = io.BytesIO()
    with gzip.GzipFile(fileobj=archive, mode="wb", filename="", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as tar:
            for name, data in sorted(contents.items()):
                info = tarfile.TarInfo(name)
                info.size, info.mode, info.mtime = len(data), 0o644, 0
                info.uid, info.gid, info.uname, info.gname = 0, 0, "", ""
                tar.addfile(info, io.BytesIO(data))
    data = archive.getvalue()
    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.with_name(output.name + ".tmp")
    with temporary.open("xb") as stream:
        stream.write(data)
    temporary.replace(output)
    digest = hashlib.sha256(data).hexdigest()
    output.with_name(output.name + ".sha256").write_text(f"{digest}  {output.name}\n")
    print(f"{digest}  {output}")


if __name__ == "__main__":
    main()
