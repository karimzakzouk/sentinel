#!/usr/bin/env bash
# Installs the pinned development toolchain into /usr/local on Ubuntu x86_64.
# Safe to rerun: each tool is skipped when the pinned version is already there.
set -euo pipefail

GO_VERSION=1.27.4
KUBECTL_VERSION=v1.37.2
KIND_VERSION=v0.33.0
HELM_VERSION=v4.3.1
GOLANGCI_LINT_VERSION=v2.15.0
ARCH=amd64

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { curl -fsSL --retry 8 --retry-max-time 300 -o "$2" "$1"; }
# Checksums come through the same retries. GitHub's release downloads answer
# 504 for minutes at a time now and then; curl doubles its wait between
# tries, so eight tries span about four minutes.
get() { curl -fsSL --retry 8 --retry-max-time 300 "$1"; }
verify() { echo "$1  $2" | sha256sum -c --quiet -; }

# CI provides Go through actions/setup-go; anywhere else, install it.
if [ "$(go env GOVERSION 2>/dev/null)" != "go${GO_VERSION}" ]; then
  go_file="go${GO_VERSION}.linux-${ARCH}.tar.gz"
  go_sum=$(get 'https://go.dev/dl/?mode=json&include=all' |
    jq -r --arg f "$go_file" '.[].files[] | select(.filename == $f) | .sha256')
  fetch "https://go.dev/dl/${go_file}" "$tmp/go.tgz"
  verify "$go_sum" "$tmp/go.tgz"
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "$tmp/go.tgz"
  sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go
  sudo ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi

if [ "$(kubectl version --client -o json 2>/dev/null | jq -r .clientVersion.gitVersion)" != "$KUBECTL_VERSION" ]; then
  url="https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl"
  fetch "$url" "$tmp/kubectl"
  verify "$(get "${url}.sha256")" "$tmp/kubectl"
  sudo install -m 0755 "$tmp/kubectl" /usr/local/bin/kubectl
fi

if [ "$(kind version 2>/dev/null | awk '{print $2}')" != "$KIND_VERSION" ]; then
  url="https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-${ARCH}"
  fetch "$url" "$tmp/kind"
  verify "$(get "${url}.sha256sum" | awk '{print $1}')" "$tmp/kind"
  sudo install -m 0755 "$tmp/kind" /usr/local/bin/kind
fi

if [ "$(helm version --short 2>/dev/null | cut -d+ -f1)" != "$HELM_VERSION" ]; then
  url="https://get.helm.sh/helm-${HELM_VERSION}-linux-${ARCH}.tar.gz"
  fetch "$url" "$tmp/helm.tgz"
  verify "$(get "${url}.sha256sum" | awk '{print $1}')" "$tmp/helm.tgz"
  tar -C "$tmp" -xzf "$tmp/helm.tgz"
  sudo install -m 0755 "$tmp/linux-${ARCH}/helm" /usr/local/bin/helm
fi

lint_version=${GOLANGCI_LINT_VERSION#v}
if ! golangci-lint version 2>/dev/null | grep "version ${lint_version} " >/dev/null; then
  base="https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}"
  file="golangci-lint-${lint_version}-linux-${ARCH}.tar.gz"
  fetch "${base}/${file}" "$tmp/lint.tgz"
  verify "$(get "${base}/golangci-lint-${lint_version}-checksums.txt" | awk -v f="$file" '$2 == f {print $1}')" "$tmp/lint.tgz"
  tar -C "$tmp" -xzf "$tmp/lint.tgz"
  sudo install -m 0755 "$tmp/golangci-lint-${lint_version}-linux-${ARCH}/golangci-lint" /usr/local/bin/golangci-lint
fi

if ! command -v dockerd >/dev/null; then
  sudo install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
  codename=$(. /etc/os-release && echo "$VERSION_CODENAME")
  echo "deb [arch=${ARCH} signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu ${codename} stable" |
    sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
  sudo apt-get update -qq
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin
fi
sudo usermod -aG docker "$(id -un)"

go version
kubectl version --client
kind version
helm version --short
golangci-lint version
dockerd --version
