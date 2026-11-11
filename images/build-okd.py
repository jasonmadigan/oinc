#!/usr/bin/env python3
"""Rebuild an OKD payload's MicroShift components for arm64 using pinned microshift-io tooling."""

import argparse
import json
import os
from pathlib import Path
import tempfile

from pinned import checkout_packaging, load_lock, output, replace, run

ARCH = "arm64"

# the payload builds kube-proxy from its kubernetes commit. upstream's recipe
# takes it from openshift/sdn, which stopped at kubernetes 1.30
KUBE_PROXY = '''kube_proxy_image() {
  local -r dockerfile_path="${WORKDIR}/Dockerfile.kube-proxy"

  # the clone pod_image() made, less its edits
  cd "${WORKDIR}/kubernetes" || { echo "Failed to access kubernetes repository directory"; return 1; }
  git checkout --quiet -- .
  sed -e 's|FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang|FROM registry.ci.openshift.org/openshift/release:rhel-9-release-golang|' \\
      -e "s|^FROM registry.ci.openshift.org/ocp/.*:base-rhel9|FROM ${images[base]}|" \\
      -e 's|yum install -y --setopt=tsflags=nodocs $INSTALL_PKGS|yum --disablerepo=rt install -y --setopt=tsflags=nodocs $INSTALL_PKGS|' \\
      openshift-hack/images/kube-proxy/Dockerfile.rhel > "${dockerfile_path}"

  podman build --platform "linux/${TARGET_ARCH}" -t "${images[kube-proxy]}" -f "${dockerfile_path}" .
}

'''


def release_tags(release):
    return json.loads(output("oc", "adm", "release", "info", release, "-o", "json"))["references"]["spec"]["tags"]


def payload_commits(payload):
    commits = {}
    for tag in release_tags(payload):
        annotations = tag.get("annotations") or {}
        url = annotations.get("io.openshift.build.source-location")
        commit = annotations.get("io.openshift.build.commit.id")
        if url and commit and commits.setdefault(url, commit) != commit:
            raise RuntimeError(f"Payload builds {url} from more than one commit")
    return commits


def check_anonymous(release, registry):
    # cri-o pulls these without credentials
    images = {tag["from"]["name"] for tag in release_tags(release)}
    private = []
    for image in sorted({release} | {image for image in images if image.startswith(registry + "/")}):
        try:
            run("skopeo", "inspect", "--no-creds", "--raw", f"docker://{image}", capture_output=True)
        except Exception:
            private.append(image)
    if private:
        raise RuntimeError("Not anonymously pullable:\n" + "\n".join(private))


def build(lock_path, registry):
    lock = load_lock(lock_path, ["x86_64"])
    base = lock["payloads"]["x86_64"]
    repository = f"{registry}/okd-release-{ARCH}"

    with tempfile.TemporaryDirectory(prefix="oinc-okd-") as directory:
        root = checkout_packaging(lock, directory)
        commits = root / "commits.txt"
        commits.write_text("".join(f"{url} {commit}\n" for url, commit in sorted(payload_commits(base).items())))
        sources = root / "sources.txt"
        sources.touch()

        script = root / "src/okd/build_images.sh"
        # upstream stages in a second registry and promotes after its own tests
        replace(script, 'STAGING_REGISTRY="$(dirname "${PRODUCTION_REGISTRY}")/okd-staging"',
                'STAGING_REGISTRY="${PRODUCTION_REGISTRY}"')
        # upstream builds branch heads; build what the payload was built from
        clone = '  cd "${repo_dir}" || { echo "Failed to access repository directory"; return 1; }'
        replace(script, clone, clone + '''
  local commit
  commit="$(awk -v url="${repo_url}" '$1 == url { print $2 }' "${OKD_COMMITS}")"
  if [ -n "${commit}" ] ; then
    git checkout --quiet --detach "${commit}"
  fi
  echo "${repo_url} $(git rev-parse HEAD)" >> "${OKD_SOURCES}"''')
        replace(script, '--from-release "quay.io/okd/scos-release:${OKD_VERSION}"',
                '--from-release "${OKD_BASE_RELEASE}"')
        replace(script, 'kube_proxy_image() {\n  local -r repo_url="https://github.com/openshift/sdn"',
                'kube_proxy_sdn_image() {\n  local -r repo_url="https://github.com/openshift/sdn"')
        replace(script, "# Run all the image creation procedures\n", KUBE_PROXY + "# Run all the image creation procedures\n")
        replace(script, "  router_image\n  kube_proxy_image\n", "  router_image\n")
        replace(script, "  pod_image\n  cli_image\n", "  pod_image\n  kube_proxy_image\n  cli_image\n")

        env = dict(os.environ, TARGET_REGISTRY=registry, OKD_COMMITS=str(commits),
                   OKD_SOURCES=str(sources), OKD_BASE_RELEASE=base)
        run(str(script), "staging", lock["okd_version"], f'release-{lock["microshift_minor"]}', ARCH, env=env)
        built = sources.read_text()

    release = f'{repository}:{lock["okd_version"]}'
    digest = output("skopeo", "inspect", "--override-os", "linux", "--override-arch", "amd64",
                    "--format", "{{.Digest}}", f"docker://{release}")
    check_anonymous(f"{repository}@{digest}", registry)
    print(built, end="")
    print(f"payloads.aarch64: {repository}@{digest}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("lock", type=Path)
    parser.add_argument("--registry", required=True, help="repository prefix to publish under")
    args = parser.parse_args()
    build(args.lock.resolve(), args.registry)
