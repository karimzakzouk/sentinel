#!/usr/bin/env bash
# Proves the VM can run Sentinel's CI: Docker works for the runner user, and
# a two-node kind cluster, which needs the inotify limits and kernel modules
# that setup.sh configures, comes up, takes a locally built image, reaches the
# internet and is deleted again. Runs as root through vm-run.sh and installs
# kind, pinned as in hack/install-tools.sh, when it is missing.
set -euo pipefail
KIND_VERSION=v0.33.0

echo "--- kernel $(uname -r)"
sysctl fs.inotify.max_user_instances fs.inotify.max_user_watches \
  net.bridge.bridge-nf-call-iptables net.bridge.bridge-nf-call-ip6tables
for module in br_netfilter overlay; do
  grep -q "^$module " /proc/modules && echo "module $module loaded"
done

if [ "$(kind version 2>/dev/null | awk '{print $2}')" != "$KIND_VERSION" ]; then
  url="https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64"
  curl -fsSL --retry 3 -o /tmp/kind "$url"
  echo "$(curl -fsSL --retry 3 "$url.sha256sum" | awk '{print $1}')  /tmp/kind" | sha256sum -c --quiet -
  install -m 0755 /tmp/kind /usr/local/bin/kind
  rm -f /tmp/kind
fi

runuser -u runner -- bash -euo pipefail <<'EOF'
cd ~
trap 'kind delete cluster --name probe >/dev/null 2>&1 || true' EXIT
echo "--- docker as $(id -un), groups: $(id -Gn)"
docker run --rm --quiet hello-world | grep 'Hello from Docker!'
echo "--- $(kind version)"
cat >/tmp/probe.yaml <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
YAML
start=$(date +%s)
kind create cluster --quiet --name probe --config /tmp/probe.yaml --wait 3m
kubectl() { docker exec probe-control-plane kubectl --kubeconfig /etc/kubernetes/admin.conf "$@"; }
kubectl wait node --all --for condition=Ready --timeout 3m >/dev/null
kubectl get nodes -o wide
echo "cluster ready in $(($(date +%s) - start))s"
printf 'FROM hello-world\n' | docker build --quiet -t probe:dev - >/dev/null
kind load docker-image probe:dev --name probe >/dev/null
docker exec probe-worker crictl images | grep -E 'probe +dev'
# Any HTTP answer proves egress: the registry answers anonymous requests with 401.
docker exec probe-worker curl -sS -o /dev/null -w 'egress from a node: HTTP %{http_code} from registry.k8s.io\n' https://registry.k8s.io/v2/
kind delete cluster --name probe
docker image rm probe:dev >/dev/null
echo "clusters left: $(kind get clusters 2>/dev/null | wc -l)"
EOF
