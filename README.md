# Guardrail

A self-hosted policy-enforcement platform for Kubernetes: Harness CI builds
and scans a container image, GitOps (Argo CD) deploys it, and a
hand-written Go admission webhook enforces security policy inside the
cluster itself -- before anything unsafe can run, not after.

**Status: early development.** Local toolchain verified (Docker, kind,
kubectl, Argo CD CLI, Go); no cluster or pipeline built yet.

## Why this exists

Most CI/CD security stops at "scan the image and hope someone reads the
report." Guardrail enforces policy at the one point it can't be
bypassed: the Kubernetes API server itself. Even a deployment pushed by
hand with `kubectl apply`, bypassing the pipeline entirely, still has to
pass the admission webhook -- because the webhook is a property of the
cluster, not of the pipeline that happened to be used this time.

The pipeline (Harness) and the deploy mechanism (Argo CD) are kept
strictly separate on purpose: CI never holds cluster credentials. It only
ever updates a Git repository; Argo CD, running inside the cluster,
pulls from that repository itself. If CI is ever compromised, the
attacker still can't touch the cluster directly.

## Architecture

```
 ┌──────────────┐   build + scan    ┌───────────────────┐
 │  Harness CI   │ ────────────────▶ │  Container image    │
 │  (SAST,       │                   │  + scan results      │
 │  secrets scan,│                   └─────────┬─────────┘
 │  image scan)  │                             │ push
 └──────┬───────┘                             ▼
        │ update manifest              ┌──────────────┐
        │ (image tag only --           │  Registry     │
        │  no cluster access)          └──────────────┘
        ▼
 ┌──────────────┐
 │  Git repo     │   (manifests: Deployment, Service, etc.)
 │  (GitOps       │
 │  source of     │
 │  truth)        │
 └──────┬───────┘
        │ polled/watched
        ▼
 ┌──────────────────────────────────────────────┐
 │  Kubernetes cluster (kind, local)              │
 │                                                  │
 │   ┌───────────┐        admission review          │
 │   │  Argo CD   │ ──────────────────────────▶ ┌────────────────┐
 │   │ (pulls from│                              │  Go admission    │
 │   │  Git, syncs│ ◀────────────────────────── │  webhook          │
 │   │  cluster)  │      allow / deny + reason    │  (policy engine)  │
 │   └───────────┘                              └────────────────┘
 │                                                                    │
 │  Every resource creation/update -- from Argo CD OR a manual        │
 │  kubectl apply -- passes through the webhook. There's no bypass.    │
 └──────────────────────────────────────────────────────────────────┘
```

## Roadmap

- [x] **v0 — Toolchain verified.** Docker (via colima), `kind`, `kubectl`,
      the Argo CD CLI, and Go all installed and confirmed working
      locally.
- [ ] **v1 — Cluster + GitOps loop.** A `kind` cluster running a trivial
      demo app, deployed via Argo CD pulling from this repo's own
      manifests -- prove the GitOps loop works end to end before adding
      policy on top.
- [ ] **v2 — Go admission webhook.** A `ValidatingAdmissionWebhook`
      written in Go, registered with the cluster, enforcing a real
      policy set (required resource limits, no root containers, no
      `:latest` image tags, images only from an allow-listed registry).
- [ ] **v3 — Harness CI pipeline.** SAST, secrets scanning, and container
      image scanning on every push, ending in a manifest update -- never
      a direct cluster deploy.
- [ ] **v4 — Failure-mode demo.** A deliberately non-compliant manifest
      submitted both via the pipeline and via direct `kubectl apply`,
      showing the webhook blocks it either way, plus a written incident
      runbook for what an operator does when a deploy gets rejected.

## Tech stack

- **CI:** [Harness](https://harness.io) (free tier — self-hosted runner,
  no cost for a solo project)
- **GitOps / CD:** [Argo CD](https://argo-cd.readthedocs.io)
- **Policy enforcement:** Go, using `k8s.io/api` and the Kubernetes
  `admission/v1` API
- **Cluster:** [kind](https://kind.sigs.k8s.io) (Kubernetes-in-Docker,
  local, free)
- **Container runtime:** Docker via [colima](https://github.com/abiosoft/colima)

## Setup

```bash
# Toolchain (already done as of this build)
brew install kind kubernetes-cli argocd go colima lima docker
colima start

# Next: create the cluster (v1)
kind create cluster --name guardrail
```
