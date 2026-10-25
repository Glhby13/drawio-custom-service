#!/usr/bin/env bash
# Lab dev helper (Claude Code, 2026-10-25): build a multi-arch image locally and push it to the lab Harbor through the
# bastion (crane). Used only until Jenkins builds the repo; the official path is Jenkinsfile -> Kaniko -> Harbor.
# Usage: scripts/dev-image.sh <service> <tag>      e.g. scripts/dev-image.sh gateway dev2
set -euo pipefail
SVC=${1:?service}; TAG=${2:?tag}
cd "$(dirname "$0")/.."
OUT=$(mktemp -d)
docker buildx build --platform linux/arm64,linux/amd64 --build-arg SERVICE_NAME="$SVC" \
  --output "type=oci,dest=$OUT/img.tar,name=$SVC:$TAG" . >&2
scp -q "$OUT/img.tar" lab-bastion:/tmp/dev-img.tar
ssh lab-bastion "rm -rf /tmp/dev-oci && mkdir /tmp/dev-oci && tar -xf /tmp/dev-img.tar -C /tmp/dev-oci && \
  crane push --insecure /tmp/dev-oci harbor.lab.local/drawio-custom-service/$SVC:$TAG >&2 && \
  crane digest --insecure harbor.lab.local/drawio-custom-service/$SVC:$TAG; rm -rf /tmp/dev-oci /tmp/dev-img.tar"
rm -rf "$OUT"
