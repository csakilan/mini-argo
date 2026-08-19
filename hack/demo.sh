#!/usr/bin/env bash
#
# Brings up mini-argo from nothing: a cluster, the operator running inside it,
# and a GitSource syncing from a public repository.
#
# Safe to re-run. Every build gets a unique image tag, because kind caches
# images by tag and a rebuilt :dev will silently keep running the old binary.
set -euo pipefail

CLUSTER="${CLUSTER:-miniargo}"
NAMESPACE="${NAMESPACE:-demo}"
SAMPLE="${SAMPLE:-config/samples/gitops_v1alpha1_gitsource.yaml}"
TAG="mini-argo:$(git rev-parse --short HEAD 2>/dev/null || echo dev)-$(date +%H%M%S)"

step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

step "Checking Docker"
docker info >/dev/null 2>&1 || {
  echo "Docker is not running. Start Docker Desktop and try again." >&2
  exit 1
}

step "Cluster '$CLUSTER'"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "already exists"
else
  kind create cluster --name "$CLUSTER"
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

step "Building the image as $TAG"
docker build -t "$TAG" .

step "Loading it into the cluster"
kind load docker-image "$TAG" --name "$CLUSTER"

step "Installing the GitSource CRD"
make install

step "Deploying the operator"
make deploy IMG="$TAG"
kubectl -n mini-argo-system rollout status deployment/mini-argo-controller-manager --timeout=300s

# rollout status can report success while an old image is still running, so
# compare what the pod actually pulled against what was asked for.
step "Verifying the running image"
kubectl get pod -n mini-argo-system -l control-plane=controller-manager \
  -o jsonpath='{.items[0].status.containerStatuses[0].imageID}{"\n"}'

step "Creating namespace '$NAMESPACE'"
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

step "Applying the GitSource"
kubectl apply -f "$SAMPLE"
kubectl wait --for=jsonpath='{.status.phase}'=Synced gitsource/gitsource-sample --timeout=180s

step "Synced"
kubectl get gitsources
echo
kubectl get deploy,svc,cm -n "$NAMESPACE"

cat <<'NEXT'

==> Try it

  Watch the status page:

    kubectl port-forward -n mini-argo-system deployment/mini-argo-controller-manager 8082:8082

  then open http://localhost:8082

  Break something by hand and watch it come back:

    kubectl scale deployment web -n demo --replicas=7
    kubectl get deployment web -n demo -w

  Tear it all down:

    make demo-clean

NEXT
