#!/usr/bin/env bash
# Builds, loads, and registers the webhook against a local kind cluster
# named "guardrail". Safe to re-run: the cert is only generated once (delete
# webhook/certs/ to force a new one), and the k8s objects are all applied,
# not created, so re-running just reconciles state.
#
# Known gotcha if you're iterating on the webhook's own code: kind loads an
# image by tag, and a running pod won't re-pull an unchanged tag on its own
# (imagePullPolicy defaults to IfNotPresent for anything that isn't
# :latest). After a code change, this script's rebuild+reload alone won't
# update already-running pods -- run
# `kubectl rollout restart deployment/guardrail-webhook -n guardrail-system`
# afterward to actually pick up the new image content.
set -euo pipefail
cd "$(dirname "$0")"

NAMESPACE=guardrail-system
IMAGE=ghcr.io/davidpotters/guardrail-webhook:v0.1.0
MANIFESTS=../manifests/webhook

if [ ! -f certs/tls.crt ]; then
  echo "==> generating TLS cert"
  go run ./cmd/gencert
else
  echo "==> reusing existing TLS cert (delete webhook/certs/ to force regeneration)"
fi

echo "==> building image"
docker build -t "$IMAGE" .

echo "==> loading image into kind"
kind load docker-image "$IMAGE" --name guardrail

echo "==> ensuring namespace exists"
kubectl get namespace "$NAMESPACE" >/dev/null 2>&1 || kubectl create namespace "$NAMESPACE"

echo "==> applying TLS secret"
kubectl create secret tls guardrail-webhook-tls -n "$NAMESPACE" \
  --cert=certs/tls.crt --key=certs/tls.key \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> applying deployment + service"
kubectl apply -f "$MANIFESTS/deployment.yaml"
kubectl apply -f "$MANIFESTS/service.yaml"

echo "==> waiting for the webhook to become ready"
kubectl rollout status deployment/guardrail-webhook -n "$NAMESPACE" --timeout=60s

echo "==> registering ValidatingWebhookConfiguration with the current caBundle"
CA_BUNDLE=$(base64 < certs/tls.crt | tr -d '\n')
sed "s|\${CA_BUNDLE}|$CA_BUNDLE|" "$MANIFESTS/validatingwebhookconfiguration.yaml" | kubectl apply -f -

echo "==> done"
