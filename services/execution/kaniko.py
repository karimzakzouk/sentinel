"""Kaniko builder — builds Docker images inside Kubernetes without a daemon.

Kaniko builds images by extracting layers directly from the Dockerfile,
without needing a Docker daemon. It runs as a regular pod, pushes the
resulting image to a registry, then exits.

Usage:
    builder = KanikoBuilder(settings, k8s_client)
    image_tag = await builder.build(
        repo="karimzakzoukz/sentinel-sim",
        branch="sentinel/sentinel-sim-abc12345",
        fixed_files={"app/main.py": "new content..."},
        image_name="karimzakzouk/sentinel-sim",
        image_tag="sentinel-abc12345",
    )
    # image_tag = "karimzakzouk/sentinel-sim:sentinel-abc12345"
    # Ready to deploy to sandbox

The builder:
  1. Creates a ConfigMap with the fixed files
  2. Launches a Kaniko pod that:
     - Clones the repo at the fix branch
     - Overlays the fixed files from the ConfigMap
     - Builds the Docker image
     - Pushes to Docker Hub
     - Exits
  3. Waits for the pod to complete (timeout: 5 min)
  4. Returns the full image tag on success, raises on failure
"""

from __future__ import annotations

import asyncio
import logging
import os
from typing import Any

log = logging.getLogger(__name__)


class KanikoBuilder:
    """Builds Docker images using Kaniko (rootless, no daemon)."""

    KANIKO_IMAGE = "gcr.io/kaniko-project/executor:v1.24.0"
    BUILD_TIMEOUT = 300  # 5 minutes
    POLL_INTERVAL = 5  # seconds

    def __init__(self, settings: Any, k8s_apps: Any, k8s_core: Any):
        self.settings = settings
        self.k8s_apps = k8s_apps
        self.k8s_core = k8s_core

    @staticmethod
    def _encode_path(path: str) -> str:
        """Encode a file path as a valid ConfigMap key.

        K8s ConfigMap keys can only contain alphanumeric, '-', '_', and '.'.
        Slashes are NOT allowed. We replace '/' with '__' (double underscore).
        e.g. 'app/main.py' → 'app__main.py'
        """
        return path.replace("/", "__")

    @staticmethod
    def _decode_path(key: str) -> str:
        """Decode a ConfigMap key back to a file path."""
        return key.replace("__", "/")

    async def build(
        self,
        repo: str,
        branch: str,
        fixed_files: dict[str, str],
        image_name: str,
        image_tag: str,
        dockerfile_path: str = "Dockerfile",
    ) -> str:
        """Build a Docker image with the fixed files applied.

        Args:
            repo: GitHub repo in "owner/repo" format (e.g. "karimzakzoukz/sentinel-sim")
            branch: Git branch to clone (e.g. "sentinel/sentinel-sim-abc12345")
            fixed_files: Dict of {path: content} for files to override after clone
            image_name: Full image name (e.g. "karimzakzouk/sentinel-sim")
            image_tag: Tag for the built image (e.g. "sentinel-abc12345")
            dockerfile_path: Path to Dockerfile in the repo (default: "Dockerfile")

        Returns:
            Full image reference (e.g. "karimzakzouk/sentinel-sim:sentinel-abc12345")

        Raises:
            Exception if the build fails or times out.
        """

        from kubernetes import client

        namespace = os.getenv("SENTINEL_NAMESPACE", "sentinel")
        build_id = image_tag.replace("sentinel-", "")[:8]
        configmap_name = f"kaniko-fixed-files-{build_id}"
        pod_name = f"kaniko-build-{build_id}"
        # Push to Docker Hub. Works on any cluster without configuration.
        full_image = f"docker.io/{image_name}:{image_tag}"

        log.info("Starting Kaniko build: %s (repo=%s, branch=%s)", full_image, repo, branch)

        try:
            # 1. Create ConfigMap with the fixed files
            configmap = client.V1ConfigMap(
                metadata=client.V1ObjectMeta(
                    name=configmap_name,
                    namespace=namespace,
                    labels={"app": "kaniko-build", "sentinel.io/build-id": build_id},
                ),
                data={self._encode_path(k): v for k, v in fixed_files.items()},
            )
            await asyncio.to_thread(
                self.k8s_core.create_namespaced_config_map,
                namespace=namespace,
                body=configmap,
            )
            log.info("Created ConfigMap %s with %d fixed files", configmap_name, len(fixed_files))

            # 2. Build the Kaniko pod spec
            # The pod:
            #   - initContainer: clones the repo + applies fixed files
            #   - main container: runs Kaniko to build + push
            pod_spec = self._build_pod_spec(
                pod_name=pod_name,
                repo=repo,
                branch=branch,
                configmap_name=configmap_name,
                full_image=full_image,
                dockerfile_path=dockerfile_path,
                build_id=build_id,
            )

            pod = client.V1Pod(
                metadata=client.V1ObjectMeta(
                    name=pod_name,
                    namespace=namespace,
                    labels={"app": "kaniko-build", "sentinel.io/build-id": build_id},
                ),
                spec=pod_spec,
            )

            await asyncio.to_thread(
                self.k8s_core.create_namespaced_pod,
                namespace=namespace,
                body=pod,
            )
            log.info("Created Kaniko pod %s", pod_name)

            # 3. Wait for the pod to complete
            success = await self._wait_for_completion(pod_name, namespace, self.BUILD_TIMEOUT)
            if not success:
                # Get the logs for debugging
                try:
                    logs = await asyncio.to_thread(
                        self.k8s_core.read_namespaced_pod_log,
                        name=pod_name,
                        namespace=namespace,
                        container="kaniko",
                    )
                    log.error("Kaniko build failed. Last 500 chars of logs:\n%s", logs[-500:])
                except Exception:
                    pass
                raise Exception(f"Kaniko build failed for {full_image}")

            log.info("Kaniko build succeeded: %s", full_image)
            return full_image

        finally:
            # Cleanup: delete the ConfigMap + Pod (best-effort)
            await self._cleanup(configmap_name, pod_name, namespace)

    def _build_pod_spec(
        self,
        pod_name: str,
        repo: str,
        branch: str,
        configmap_name: str,
        full_image: str,
        dockerfile_path: str,
        build_id: str,
    ) -> Any:
        """Build the Kaniko pod spec with initContainer + main container."""
        from kubernetes import client

        # The initContainer clones the repo + applies the fixed files
        # The main container runs Kaniko to build + push
        #
        # We use a shared volume (workspace) to pass the cloned+patched repo
        # from the initContainer to the Kaniko container.
        #
        # The fixed files are in a ConfigMap, mounted into the initContainer.
        # The initContainer copies them over the cloned files.

        # Docker Hub credentials secret name — created by the Helm chart
        # The secret should have a .dockerconfigjson key
        docker_secret_name = "kaniko-docker-hub-creds"

        init_container = client.V1Container(
            name="clone-and-patch",
            image="alpine/git:latest",
            working_dir="/workspace",
            command=["/bin/sh", "-c"],
            args=[
                f"""
set -ex
# alpine/git:latest has git pre-installed — no apk needed
git clone --depth 1 --branch {branch} https://github.com/{repo}.git /workspace/repo
cd /workspace/repo

# Apply the fixed files from the ConfigMap.
# ConfigMap keys can't contain '/', so they were encoded with '__' as
# separator (e.g. 'app/main.py' → 'app__main.py').
# We decode each key back to the full path and copy the file.
for f in /fixed-files/*; do
    key=$(basename "$f")
    # Decode: replace '__' with '/'
    rel_path=$(echo "$key" | sed 's/__/\\//g')
    dest="/workspace/repo/$rel_path"
    mkdir -p "$(dirname "$dest")"
    cp "$f" "$dest"
    echo "Applied fix: $key → $dest"
done

# Verify the files were applied
ls -la /workspace/repo/
"""
            ],
            volume_mounts=[
                client.V1VolumeMount(name="workspace", mount_path="/workspace"),
                client.V1VolumeMount(name="fixed-files", mount_path="/fixed-files", read_only=True),
            ],
        )

        # Kaniko container — builds the image and pushes to Docker Hub
        # --destination is the full image:tag
        # --dockerfile is the path within /workspace/repo
        # --context is the build context (the repo root)
        kaniko_args = [
            f"--dockerfile={dockerfile_path}",
            f"--destination={full_image}",
            "--context=/workspace/repo",
            "--force",  # skip TLS verify for Docker Hub
            "--cache=false",
            "--snapshotMode=redo",
        ]

        kaniko_container = client.V1Container(
            name="kaniko",
            image=self.KANIKO_IMAGE,
            args=kaniko_args,
            working_dir="/workspace",
            volume_mounts=[
                client.V1VolumeMount(name="workspace", mount_path="/workspace"),
                client.V1VolumeMount(
                    name="docker-config",
                    mount_path="/kaniko/.docker",
                    read_only=True,
                ),
            ],
        )

        pod_spec = client.V1PodSpec(
            restart_policy="Never",
            service_account_name=os.getenv("SENTINEL_RELEASE_NAME", "sentinel") + "-sentinel",
            init_containers=[init_container],
            containers=[kaniko_container],
            volumes=[
                # Shared workspace between init + main container
                client.V1Volume(
                    name="workspace",
                    empty_dir=client.V1EmptyDirVolumeSource(),
                ),
                # Fixed files from ConfigMap
                client.V1Volume(
                    name="fixed-files",
                    config_map=client.V1ConfigMapVolumeSource(name=configmap_name),
                ),
                # Docker Hub credentials for Kaniko to push
                client.V1Volume(
                    name="docker-config",
                    secret=client.V1SecretVolumeSource(
                        secret_name=docker_secret_name,
                        items=[client.V1KeyToPath(key=".dockerconfigjson", path="config.json")],
                    ),
                ),
            ],
        )

        return pod_spec

    async def _wait_for_completion(self, pod_name: str, namespace: str, timeout: int) -> bool:
        """Wait for a pod to reach Succeeded or Failed phase."""
        import time

        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                pod = await asyncio.to_thread(
                    self.k8s_core.read_namespaced_pod,
                    name=pod_name,
                    namespace=namespace,
                )
                phase = pod.status.phase
                if phase == "Succeeded":
                    return True
                if phase == "Failed":
                    return False
                log.info("Kaniko pod %s phase: %s (waiting...)", pod_name, phase)
            except Exception as e:
                log.warning("Error checking pod status: %s", e)
            await asyncio.sleep(self.POLL_INTERVAL)
        log.error("Kaniko build timed out after %ds", timeout)
        return False

    async def _cleanup(self, configmap_name: str, pod_name: str, namespace: str) -> None:
        """Delete the ConfigMap and Pod (best-effort)."""
        from kubernetes import client

        try:
            await asyncio.to_thread(
                self.k8s_core.delete_namespaced_config_map,
                name=configmap_name,
                namespace=namespace,
            )
        except Exception:
            pass
        try:
            await asyncio.to_thread(
                self.k8s_core.delete_namespaced_pod,
                name=pod_name,
                namespace=namespace,
                body=client.V1DeleteOptions(grace_period_seconds=0),
            )
        except Exception:
            pass
