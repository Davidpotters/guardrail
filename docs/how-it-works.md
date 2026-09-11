# How Guardrail actually works, explained

This is a walkthrough, not a reference doc -- it exists so you can read it
once and actually understand every piece of what got built, not just
recognize the vocabulary. `architecture.md`, `threat-model.md`, and
`incident-runbook.md` are still the detailed reference material; this is
what to read first. Every term gets explained the first time it shows up.

## The one-sentence version, tied to something you already did

At TransUnion, you were the last checkpoint before code reached the
artifact registry -- Checkmarx and SonarQube ran, and if something carried
a Critical/High/Medium finding, it got blocked, full stop, before it could
become a real deployment artifact. Guardrail does the same *kind* of
thing -- block bad stuff before it ships -- but moves the checkpoint to a
different, more stubborn place: instead of a gate in front of the artifact
registry, it's a gate built into the Kubernetes control plane itself, so
it can't be routed around by anyone who has cluster access, pipeline or
not.

## Part 1: GitOps, and what Argo CD actually does

You already know the deploy side of CI/CD from Azure DevOps and Harness --
a pipeline runs, and at some point it reaches out and pushes a change onto
a target (your Blue/Green cutovers, orchestrated through Azure DevOps and
PowerShell). That's called a **push model**: the pipeline initiates the
deploy.

**GitOps flips that.** Nothing ever pushes into the cluster. Instead, a
tool running *inside* the cluster (Argo CD) periodically looks at a git
repository and asks "does the cluster currently match what's declared in
git?" If not, it pulls the difference in and applies it. This is a **pull
model**. The practical reason it matters for a security story: since
nothing external ever gets credentials to push into the cluster, a
compromised pipeline has nothing to push *with* -- it can, at absolute
worst, write a bad commit to a git repo, which is a much smaller blast
radius than "has a working kubeconfig for production."

**Two Kubernetes objects Argo CD introduces**, both just YAML like anything
else you'd `kubectl apply`:

- An **`Application`** tells Argo CD three things: which git repo to
  watch, which folder in it holds the manifests, and which
  namespace/cluster to apply them to. `argocd/application.yaml` is
  Guardrail's.
- An **`AppProject`** is a permissions boundary *around* one or more
  Applications -- which repos they're allowed to pull from, which
  namespaces they're allowed to touch, whether they can create
  cluster-wide resources at all. Argo CD ships a `default` AppProject with
  no restrictions; Guardrail replaced that with a custom one
  (`argocd/appproject.yaml`) scoped to exactly this repo and the `demo`
  namespace. Think of it the same way you'd think of an IAM policy scoped
  down from `*` to exactly what a role needs.

**Self-heal** is Argo CD continuously re-checking, not just syncing once.
If someone manually runs `kubectl edit` on something Argo CD manages, it
notices the drift and reverts it back to whatever git says -- verified
directly in this project by scaling a deployment by hand and watching Argo
CD put it back within about a second.

## Part 2: what an admission webhook actually is

This is the part that's genuinely new territory, so slower explanation
here.

Every single thing that happens in Kubernetes -- creating a pod, updating
a deployment, anything -- goes through the **API server** first. The API
server has a feature called an **admission webhook**: before it finalizes
a request, it can be configured to pause and ask an external HTTP service
"should I actually allow this?" That external service is a **webhook** --
just a small HTTP server you write yourself, same idea as a webhook from
any SaaS product, except the caller is the Kubernetes API server itself
and the stakes are "does this resource get created or not."

There are two kinds: a **ValidatingAdmissionWebhook** can only say yes or
no. A **MutatingAdmissionWebhook** can also *change* the object before it's
saved (e.g., auto-inject a sidecar container). Guardrail only validates --
it never modifies anything, which is a smaller, easier-to-reason-about
contract: this webhook cannot make your deployment different than what you
asked for, only refuse it.

**The request/response shape.** When the API server calls the webhook, it
sends an `AdmissionReview` JSON object containing the resource under
review (a Pod, in Guardrail's case) plus metadata (who's creating it, what
operation). The webhook has to respond with another `AdmissionReview`
containing `allowed: true` or `allowed: false`, and if false, an optional
human-readable reason. That's the entire contract -- everything in
`webhook/admission.go` is decoding that JSON in, and encoding a decision
back out.

**Why it has to be Go's actual `net/http` and TLS, not a shortcut.**
Kubernetes requires the webhook to be served over HTTPS -- no plain HTTP
option exists. That's why `webhook/cmd/gencert` and the whole
certificate-handling story exists: the API server needs a TLS certificate
it can verify, and it verifies it against a `caBundle` value baked into
the `ValidatingWebhookConfiguration` (the Kubernetes object that actually
registers "hey API server, call this webhook for these resource types").
If you've ever debugged a service that wouldn't accept a self-signed cert
because the client didn't trust the CA -- same underlying mechanism, just
Kubernetes' API server is the "client" here instead of a browser.

**Why Go, mechanically.** Every policy check in `webhook/policy.go` is a
plain function that takes a Kubernetes object (a `corev1.Pod`, imported
from Kubernetes' own official Go libraries -- the same libraries `kubectl`
and every controller are built from) and returns a list of human-readable
violation strings. If you're reading the Go for the first time: `func
checkResourceLimits(c corev1.Container) string` is a function named
`checkResourceLimits`, taking one argument `c` of type `corev1.Container`,
returning a `string`. Go doesn't have exceptions the way Python or C# do --
functions that can fail just return an extra value (often an `error`), and
the caller is expected to actually check it, not have it silently
propagate up. That style shows up everywhere in `admission.go`.

## Part 3: the CI pipeline, and the two genuinely new security concepts

**SBOM (Software Bill of Materials).** Exactly what it sounds like -- a
generated, machine-readable list of every package and library baked into
the container image, the software equivalent of an ingredients label. The
point: if a new CVE gets announced next month for some library buried
three dependencies deep, you can grep the SBOM instead of re-auditing the
whole image from scratch to find out if you're affected.

**Cosign / Sigstore keyless signing.** This is the one worth slowing down
on, because "keyless signing" sounds like a contradiction. Traditionally,
signing something (proving "this really was built by me, unaltered")
means generating a private key, keeping it secret forever, and signing
with it -- which means you now have a long-lived secret to protect, and if
it ever leaks, every signature it ever made is suspect. Sigstore's keyless
flow replaces that permanent key with a *very short-lived* one, issued on
the spot: the CI job proves its identity to Sigstore's certificate
authority (called **Fulcio**) using a token GitHub Actions already
generates automatically for every job (**OIDC** -- an identity-proof
token, same underlying idea as "log in with Google" on some other site,
just machine-to-machine instead of human-to-website). Fulcio checks that
token, issues a certificate valid for a few minutes, cosign signs with it,
and the fact that this signing event happened gets recorded permanently in
a public, append-only log called **Rekor** (the "transparency log"). The
net effect: no secret ever exists to leak, and anyone can later verify
"yes, this exact image was really signed by this exact GitHub Actions
run" by checking Rekor -- which is exactly what got done in this project,
independently, from a separate machine, rather than just trusting the
pipeline's own green checkmark.

**Trivy** is more familiar territory -- it's a vulnerability scanner for
container images, playing the same role Checkmarx/SonarQube played in your
actual pipeline, just checking a built image's OS packages and libraries
against known-CVE databases instead of checking source code.

## Part 4: STRIDE, briefly -- the full analysis is in threat-model.md

**STRIDE** is just a checklist, nothing more exotic than that: **S**poofing,
**T**ampering, **R**epudiation, **I**nformation disclosure, **D**enial of
service, **E**levation of privilege. The value isn't the acronym, it's
what it forces: instead of threat-modeling by "think really hard about
what could go wrong" (which tends to only surface the threats you already
happened to think of), you deliberately ask all six questions against
every boundary in the system, which catches categories you wouldn't have
thought to ask about on your own. `threat-model.md` is that exercise done
against Guardrail's four trust boundaries, with every single answer marked
either "here's the actual mitigation" or "here's the real residual risk,"
never left vague.

## What to actually read next

If a specific piece still doesn't click, that's the doc to open:
`architecture.md` for *why* each decision was made the way it was (not
just what it does), `threat-model.md` for the systematic security
analysis, `incident-runbook.md` for what actually happens, with real
captured transcripts, when something goes wrong.
