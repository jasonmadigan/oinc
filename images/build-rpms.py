#!/usr/bin/env python3
"""Build release-aligned RPMs using pinned microshift-io packaging."""

import argparse
from pathlib import Path
import shutil
import tempfile
import uuid

from pinned import checkout_packaging, load_lock, replace, run


def build(lock_path, output, runtime):
    if output.exists() and any(path.name != ".gitkeep" for path in output.iterdir()):
        raise RuntimeError(f"RPM output directory must be empty: {output}")
    lock = load_lock(lock_path, ["x86_64", "aarch64"])
    source = lock["microshift_commit"]
    suffix = f'{lock["microshift_minor"]}-{source[:12]}'
    srpm_image = f"localhost/oinc-srpm:{suffix}"
    rpm_image = f"localhost/oinc-rpms:{suffix}"

    with tempfile.TemporaryDirectory(prefix="oinc-rpms-") as directory:
        root = checkout_packaging(lock, directory)

        srpm = root / "packaging/srpm.Containerfile"
        # Upstream defaults the other architecture to latest and clones a branch.
        # Both inputs must stay fixed when reproducing a release.
        for arch in ("amd64", "arm64"):
            replace(srpm, f'"${{OKD_GET_VERSION_SCRIPT}}" latest-{arch}', 'echo "${OKD_VERSION_TAG}"')
        replace(srpm,
                'git clone --branch "${USHIFT_GITREF}" --single-branch "${USHIFT_GIT_URL}" "${HOME}/microshift"',
                'git init "${HOME}/microshift" && '
                'git -C "${HOME}/microshift" fetch --depth 1 "${USHIFT_GIT_URL}" "${USHIFT_GITREF}" && '
                'git -C "${HOME}/microshift" checkout --detach FETCH_HEAD && '
                'test "$(git -C "${HOME}/microshift" rev-parse HEAD)" = "${USHIFT_GITREF}"')
        # Pass digest-qualified payloads to oc rather than resolving mutable tags.
        prebuild = root / "src/image/prebuild.sh"
        text = prebuild.read_text()
        if text.count('${okd_url}:${okd_releaseTag}') != 3:
            raise RuntimeError("Unexpected payload lookup in pinned packaging")
        prebuild.write_text(text.replace('${okd_url}:${okd_releaseTag}', '${okd_url}'))

        rpm = root / "packaging/rpm.Containerfile"
        replace(rpm, "FROM localhost/microshift-okd-srpm:latest AS srpm", f"FROM {srpm_image} AS srpm")
        replace(rpm, "GO_VER=$(curl -sL 'https://go.dev/VERSION?m=text' | head -1 | sed 's/^go//')",
                f'GO_VER={lock["go_version"]}')

        args = []
        for name, value in {
            "USHIFT_GITREF": source,
            "OKD_VERSION_TAG": lock["okd_version"],
            "OKD_RELEASE_IMAGE_X86_64": lock["payloads"]["x86_64"],
            "OKD_RELEASE_IMAGE_AARCH64": lock["payloads"]["aarch64"],
            "BUILD_TIMESTAMP": lock["build_timestamp"],
            # upstream's Makefile defaults, for the topolvm package oinc leaves out
            "VG_NAME": "myvg1",
            "SPARE_GB": "0",
        }.items():
            args += ["--build-arg", f"{name}={value}"]
        run(runtime, "build", "-t", srpm_image, *args, "-f", str(srpm), directory)
        run(runtime, "build", "--ulimit", "nofile=524288:524288",
            "-t", rpm_image, "-f", str(rpm), directory)

        container = "oinc-rpm-export-" + uuid.uuid4().hex
        run(runtime, "create", "--name", container, rpm_image)
        try:
            output.mkdir(parents=True, exist_ok=True)
            run(runtime, "cp", f"{container}:/home/microshift/microshift/_output/rpmbuild/RPMS/.", str(output))
            shutil.copyfile(lock_path, output / "oinc-release.json")
        finally:
            run(runtime, "rm", container)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("lock", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--runtime", choices=("docker", "podman"), default="docker")
    args = parser.parse_args()
    build(args.lock.resolve(), args.output.resolve(), args.runtime)
