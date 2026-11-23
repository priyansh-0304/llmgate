#!/usr/bin/env bash
# Generates random local-only secrets, once, into two git-ignored files:
#   .env                              (for docker-compose)
#   deploy/k8s/secret.local.yaml      (Kubernetes Secret manifest for kind)
# Existing files are never overwritten, so re-running is safe.
set -euo pipefail
cd "$(dirname "$0")/.."

rand() { openssl rand -hex 16; }

GATEWAY_API_KEY="sk-local-$(rand)"
REDIS_PASSWORD="$(rand)"
GRAFANA_ADMIN_PASSWORD="$(rand)"

if [ ! -f .env ]; then
  umask 077
  cat > .env <<EOF
GATEWAY_API_KEY=${GATEWAY_API_KEY}
REDIS_PASSWORD=${REDIS_PASSWORD}
GRAFANA_ADMIN_PASSWORD=${GRAFANA_ADMIN_PASSWORD}
EOF
  echo "wrote .env"
else
  echo ".env already exists, leaving it alone"
fi

if [ ! -f deploy/k8s/secret.local.yaml ]; then
  umask 077
  cat > deploy/k8s/secret.local.yaml <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: llmgate-secrets
  namespace: llmgate
type: Opaque
stringData:
  GATEWAY_API_KEY: "${GATEWAY_API_KEY}"
  REDIS_PASSWORD: "${REDIS_PASSWORD}"
  GRAFANA_ADMIN_PASSWORD: "${GRAFANA_ADMIN_PASSWORD}"
EOF
  echo "wrote deploy/k8s/secret.local.yaml"
else
  echo "deploy/k8s/secret.local.yaml already exists, leaving it alone"
fi
