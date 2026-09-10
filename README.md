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

## Where this sits against my day job

At TransUnion I was the last checkpoint before code reached the artifact
registry -- the primary PR approver enforcing Checkmarx SAST and
SonarQube gates, blocking anything carrying a Critical, High, or Medium
vulnerability. That's a real, proven pattern: block bad artifacts before
they ship. Guardrail moves the same judgment to a different point --
instead of a human gate before the registry, it's an automated gate at
the Kubernetes API server itself, so it can't be skipped by anyone with
cluster access, pipeline or not.

A few pieces here are deliberate departures from what I did day to day,
not restatements of it:

- **Hand-rolled Go webhook, not Gatekeeper.** OPA/Gatekeeper is the
  standard way to wire admission control into Kubernetes today, and it's
  what I'd actually recommend for a production team -- policy as Rego,
  applied with `kubectl apply`, no custom server to run or maintain.
  Guardrail writes the webhook by hand instead, on purpose: it's a
  stronger demonstration of direct competency against the Kubernetes
  `admission/v1` API and Go itself than configuring an existing policy
  engine would be.
- **A Dockerfile I actually wrote.** My day-to-day work was almost
  entirely with pre-built images -- patching, validating, and publishing
  them, never authoring one from scratch. The webhook's own container
  image changes that: a real multi-stage Go build, written and
  maintained here.
- **SBOM generation and image signing.** Neither was part of my job.
  Guardrail's pipeline generates a real SBOM and signs the resulting
  image specifically to close that gap, not because a solo project
  strictly needs it.

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
      Packaged with a hand-written multi-stage Dockerfile -- a real gap
      in my day-to-day work, where I consumed pre-built images rather
      than authoring them.
- [ ] **v3 — Harness CI pipeline.** SAST, secrets scanning, and container
      image scanning on every push, ending in a manifest update -- never
      a direct cluster deploy. Also generates a Software Bill of
      Materials and signs the resulting image (cosign/Sigstore) --
      supply-chain steps my day job never required, added here on
      purpose.
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
