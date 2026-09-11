# Architecture: design decisions and tradeoffs

The README covers what Guardrail does. This covers *why* it's built the way
it is -- the alternatives considered and why they weren't the right ones
here. Every decision below cost something; the point of this document is to
be honest about the cost, not just the benefit.

## The core structural decision: three components, three trust levels, one direction of authority

CI, GitOps, and the admission webhook are three separate systems on purpose,
and authority flows one way between them:

- **CI (GitHub Actions)** can only ever write to this git repository. It has
  no Kubernetes credentials of any kind -- not a kubeconfig, not a service
  account token, nothing. If a GitHub Actions job were fully compromised
  (a malicious dependency, a supply-chain attack on an action), the
  attacker's reach stops at "can push a commit to this repo," which is the
  same reach any collaborator already has.
- **Argo CD** holds real Kubernetes credentials, but it's scoped by the
  `guardrail` AppProject to exactly one namespace (`demo`) and zero
  cluster-scoped resources. It also only trusts one repository, over a
  read-only deploy key -- it cannot push anywhere, only pull.
- **The admission webhook** is the only component with authority over what
  the cluster will actually accept, and it doesn't trust either of the
  other two. Section "Why validate Pods" below is the concrete proof of
  that: Argo CD's own sync requests get rejected exactly the same way a
  bypassing `kubectl apply` does.

The alternative most teams reach for is giving CI direct cluster access
(a kubeconfig secret in CI, `kubectl apply` at the end of the pipeline).
That's simpler to set up and was rejected specifically because it collapses
the three trust levels into one: a compromised pipeline becomes a
compromised cluster.

## Why a hand-rolled Go webhook instead of Gatekeeper

[OPA/Gatekeeper](https://open-policy-agent.github.io/gatekeeper/) is the
standard, production-recommended way to do Kubernetes admission control --
policy written as Rego, applied as ordinary Kubernetes custom resources, no
webhook server to write or run yourself. For a real team, it's very likely
the better choice: less code to maintain, a large ecosystem of existing
policy libraries, and Rego is purpose-built for this exact problem.

Guardrail doesn't use it, on purpose: writing the webhook by hand against
the raw `admission/v1` API is a direct demonstration of Kubernetes-native Go
competency that configuring an existing policy engine wouldn't be. That's a
legitimate reason for a portfolio project and a bad reason for a production
system -- if this were a real team's policy platform, Gatekeeper (or
[Kyverno](https://kyverno.io), which uses plain Kubernetes YAML instead of
Rego and would be the pick for a team less comfortable with a new DSL) would
be the recommendation, not this.

## Why the webhook validates Pods, not Deployments

The four policies (resource limits, non-root, no `:latest`, registry
allow-list) are all container-level properties, and the only object that
actually reflects "here is a container about to run" is a Pod --
Deployments, StatefulSets, DaemonSets, Jobs, and CronJobs are all just
templates that eventually produce Pods through their own controllers.

Validating at the Pod level catches every one of those paths through a
single rule, which is deliberately simpler than writing (and maintaining)
a separate rule per workload kind. The v4 failure-mode demo's Scenario B is
the direct, observed consequence of this choice: a Deployment update that
violates policy is *accepted* by the webhook (it never saw a Deployment,
only later gets to see the Pods that Deployment's ReplicaSet tries to
create), and the actual rejection shows up one layer down, on the
ReplicaSet's `FailedCreate` event, not on the Deployment sync itself. That's
not a bug -- it's the direct shape of validating at the right layer -- but
it does mean "the sync succeeded" is not the same question as "did the pods
come up," and `docs/incident-runbook.md` exists specifically because that
distinction is easy to miss under real incident pressure.

## Why `failurePolicy: Fail`, not `Ignore`

Kubernetes lets a `ValidatingWebhookConfiguration` choose what happens if
the webhook can't be reached at all: `Fail` blocks the request, `Ignore`
lets it through unvalidated. Guardrail uses `Fail`.

The tradeoff is real and is spelled out in the incident runbook's Scenario
C, reproduced live: an outage in the webhook itself becomes a cluster-wide
inability to create *any* pod, not just noncompliant ones -- verified by
scaling the webhook to zero and watching a fully compliant pod get refused
with a connection error. `Ignore` would avoid that availability risk
entirely, at the cost of silently disabling every policy in this project
the moment the webhook has a bad day. Given the project's whole thesis is
"policy that can't be bypassed," choosing the option that can be bypassed
by an outage would have undercut the premise. The emergency escape hatch
(temporarily patching to `Ignore` mid-incident) is documented in the
runbook rather than treated as something that should never happen.

## Why the webhook's own namespace is excluded from its own policy

`namespaceSelector` excludes `guardrail-system` and `kube-system`, using the
`kubernetes.io/metadata.name` label the API server stamps on every
namespace automatically. Without this, the webhook would try to validate
its own pods -- and if it were ever crash-looping or mid-rollout with a bad
image, its own replacement pod could be rejected by the broken instance
trying (and failing) to answer, permanently locking the cluster out of ever
fixing it. Verified directly (not just reasoned about): a completely
noncompliant pod applied inside `guardrail-system` was allowed through,
confirming the exclusion actually works, not just that it looks right on
paper.

## Why the TLS cert is generated once, out-of-band, not at server startup

`cmd/gencert` is a separate, one-time step from `main.go`'s own startup.
The `ValidatingWebhookConfiguration`'s `caBundle` field is a static copy of
the certificate's public bytes -- it doesn't get refreshed automatically.
If the server generated a fresh self-signed cert on every restart (a common
shortcut in quick webhook tutorials), the very next pod restart would
silently break trust between the API server and the webhook, and thanks to
`failurePolicy: Fail`, that would take down all pod creation cluster-wide
with it. A real production setup would use
[cert-manager](https://cert-manager.io)'s CA injector to automate this
properly; for a `kind` cluster, a deploy script that regenerates the
Kubernetes Secret and re-substitutes the `caBundle` (`webhook/deploy.sh`)
is the pragmatic middle ground -- explicit and scriptable rather than fully
automated.

## Known gaps, stated rather than hidden

Consistent with how the rest of this portfolio is written: a "why this is
secure" document that only lists what's mitigated isn't credible. These are
real, current gaps, not resolved by anything in this repo:

- **The webhook image is pinned by tag (`sha-<commit>`), not digest.**
  Registry tags are mutable by default -- nothing currently stops
  `ghcr.io/davidpotters/guardrail-webhook:sha-8640e350b273` from being
  overwritten to point at different image bytes later, even though the
  commit-sha naming convention makes that unlikely in practice. Pinning
  `manifests/webhook/deployment.yaml` to the image's digest
  (`@sha256:...`) instead of its tag would close this outright; not done
  here because `webhook/deploy.sh` would need to capture and pass the
  digest through explicitly rather than relying on the tag alone.
- **No branch protection on `main`.** Checked directly, not assumed:
  GitHub's branch protection API refuses to even report status on this
  repo without GitHub Pro or making it public -- it's a platform
  constraint of the plan this repo is on, not a choice made and left
  unaddressed. Worth real branch protection (required reviews, no direct
  pushes) the moment this either goes public or a second person ever gets
  write access.
- **Argo CD's own admin access is unhardened.** Default install, default
  auto-generated admin secret, never rotated or replaced with SSO/RBAC in
  this setup. Low real risk today -- nothing here exposes the Argo CD API
  outside the local machine -- but it's a gap, not a mitigated risk, and
  would need real attention before this pattern went anywhere multi-user.
- **No rate limiting on the admission webhook itself.** A pathological
  client creating pods in a tight loop could drive load against the
  webhook that legitimate traffic would then compete with. Kubernetes'
  own API server rate limiting provides some backstop; nothing
  webhook-specific exists here.

See [`docs/threat-model.md`](threat-model.md) for the systematic version of
this analysis (STRIDE against each trust boundary) rather than this ad hoc
list.
