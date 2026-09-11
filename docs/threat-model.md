# Threat model

Methodology: diagram the components and data flows, mark the trust
boundaries (points where data crosses from a less-trusted zone into a
more-trusted one), then apply [STRIDE](https://en.wikipedia.org/wiki/STRIDE_model)
(Spoofing, Tampering, Repudiation, Information disclosure, Denial of
service, Elevation of privilege) against each boundary. This finds threats
by category instead of by guessing, and forces a stated answer for
categories that don't obviously apply, instead of silently skipping them.

Every item below is marked **Mitigated** (with how) or **Residual risk**
(accepted, with why) -- never left ambiguous. A threat model that only
lists what's handled isn't one; the honest version names what isn't.

## Trust boundaries

```
 [Developer's machine] ---push--> [GitHub repo] (BOUNDARY 1)
                                        │
                          [GitHub Actions CI] (BOUNDARY 2: builds + signs)
                                        │ commits manifest update
                                        ▼
                                  [GitHub repo]
                                        │ pull (read-only deploy key)
                                        ▼
                          [Argo CD, in-cluster] (BOUNDARY 3)
                                        │ admission review
                                        ▼
                    [Kubernetes API server] <--webhook call-- [Guardrail webhook] (BOUNDARY 4)
                                        │
                                        ▼
                                  [Running pods]
```

Four boundaries, in order of how much authority crosses them:

1. **Developer → GitHub repo.** Anyone who can push here controls
   everything downstream.
2. **GitHub repo → CI, and CI → GitHub repo.** CI reads source and writes
   back exactly one thing (an image tag in one file).
3. **GitHub repo → Argo CD.** Argo CD reads manifests and applies them
   inside the cluster.
4. **Argo CD/kubectl → API server → webhook → API server.** The one
   boundary every path converges on, regardless of which of the three
   above was used or bypassed.

## Boundary 1: Developer → GitHub repo

| STRIDE category | Analysis | Status |
|---|---|---|
| Spoofing | Could someone push commits as if they were me? | **Residual risk.** Standard GitHub account security (2FA etc.) is the only control; nothing project-specific. |
| Tampering | Could a pushed commit be altered in transit or at rest? | **Mitigated.** Git's content-addressed commit hashing makes silent tampering detectable; HTTPS/SSH transport is encrypted. |
| Repudiation | Is there a record of who committed what, when? | **Mitigated.** Git history itself, plus GitHub's own audit log for account-level actions. |
| Information disclosure | Could the repo's contents leak to someone who shouldn't see them? | **Mitigated (mostly).** Repo is private; the one thing shared outside it is the deploy key's *public* half (harmless by design) and, once this goes public eventually, all of it deliberately. |
| Denial of service | Could someone lock me out or spam the repo? | **Residual risk, low.** GitHub-platform-level; nothing project-specific added. |
| Elevation of privilege | Could a repo compromise reach the cluster directly? | **Mitigated architecturally.** The repo holds no cluster credentials at all -- only Argo CD's read-only deploy key exists, and that's read-only *of the repo*, not a path *into* the cluster from the repo's side. |

**Not mitigated, stated plainly:** no branch protection on `main` (see
`architecture.md` -- this is a platform-plan limitation, verified via the
GitHub API, not an oversight left unaddressed) and no required review before
merge, since this is a solo project. A team version of this repo would need
both before boundary 1 could be called adequately controlled.

## Boundary 2: GitHub repo ↔ CI (GitHub Actions)

| STRIDE category | Analysis | Status |
|---|---|---|
| Spoofing | Could a build/signature be attributed to this pipeline falsely? | **Mitigated.** Cosign's keyless signing ties every signature to this exact repo+workflow's GitHub OIDC identity, verified against Sigstore's Fulcio CA -- independently re-verified with `cosign verify` from a separate machine, not just trusted from the job's own exit code. |
| Tampering | Could the built image be altered between build and sign? | **Mitigated within the run.** Build, scan, and sign happen sequentially on the same ephemeral runner, in one job, with no external write access in between. **Residual risk:** a compromised transitive Go dependency could tamper with the binary *before* the build step ever runs -- `go.sum` checksum verification is the only current defense, and it wasn't independently audited beyond what `go mod` already does by default. |
| Repudiation | Is there a record of what was built, scanned, and signed? | **Mitigated.** Sigstore's Rekor transparency log (append-only, publicly checkable) plus the GitHub Actions run log and the resulting git commit, three independent records that all have to agree. |
| Information disclosure | Could secrets leak via the pipeline? | **Mitigated.** No long-lived secret exists to leak -- `GITHUB_TOKEN` is ephemeral and scoped per-job via explicit `permissions:`, and cosign's keyless flow means there is no signing key, stored or otherwise, at all. |
| Denial of service | Could the pipeline be spammed to block legitimate deploys? | **Residual risk, low.** Trigger is path-filtered and push-only on a private repo only I can write to. |
| Elevation of privilege | Could a compromised CI job reach the cluster? | **Mitigated architecturally, the load-bearing one.** CI holds zero Kubernetes credentials of any kind. Its only write access anywhere is a git commit to its own repo. |

## Boundary 3: GitHub repo → Argo CD

| STRIDE category | Analysis | Status |
|---|---|---|
| Spoofing | Could something impersonate the real repo to Argo CD? | **Mitigated.** SSH host-key verification on the git connection; the deploy key is scoped to this one exact repo by GitHub itself. |
| Tampering | Could manifests be altered between the repo and what Argo CD applies? | **Mitigated.** Same git integrity guarantees as boundary 1; Argo CD diffs against the exact commit it synced. |
| Repudiation | Is there a record of what Argo CD synced and when? | **Mitigated.** Argo CD's own sync history (`.status.history`), correlated against git commit hashes. |
| Information disclosure | Could the repo credential (deploy key) leak and be misused? | **Mitigated to read-only.** The key is read-only at GitHub's enforcement layer, confirmed via the API response (`"read_only":true`) when it was registered -- even a full leak of the private key only grants read access to one already-private repo, not write. |
| Denial of service | Could Argo CD be starved or blocked from syncing? | **Residual risk, low.** No specific mitigation beyond Kubernetes' own resource scheduling for the Argo CD pods themselves. |
| Elevation of privilege | Could Argo CD's cluster access be used beyond its intended scope? | **Mitigated.** The `guardrail` AppProject restricts it to one source repo, the `demo` namespace, and zero cluster-scoped resources (`clusterResourceWhitelist: []`) -- confirmed as an actual constraint, not just written intent, since it replaced Argo CD's own unrestricted `default` project. **Residual risk:** Argo CD's own admin-level access to itself (its UI/API) was never hardened past the default install -- see `architecture.md`. |

## Boundary 4: the admission webhook (where every path converges)

This is the boundary the whole project exists to build, so it gets the
most scrutiny.

| STRIDE category | Analysis | Status |
|---|---|---|
| Spoofing | Could something impersonate the webhook to the API server, and get a fake "allow" accepted? | **Mitigated.** The API server only trusts responses from whatever presents a cert matching the `caBundle` it was registered with -- TLS, not an assumption. |
| Tampering | Could the policy logic be altered without it showing up as a new, signed, scanned image? | **Mitigated by the pipeline (boundary 2), with a gap.** The image itself is signed and scanned. **Residual risk:** the deployment manifest references the image by mutable tag, not immutable digest (see `architecture.md`) -- so tampering with *what the tag points to* after the fact isn't cryptographically prevented at the deploy-manifest level, only made unlikely by convention. |
| Repudiation | If a deploy gets rejected, is there a record of exactly why? | **Mitigated.** Every rejection returns the full list of violations in the API error itself, which lands in `kubectl`'s output or, for the GitOps path, in `kubectl get events` -- both verified directly with real captured transcripts in `docs/incident-runbook.md`, not assumed to work this way. |
| Information disclosure | Could the webhook leak information about the cluster to a requester? | **Mitigated by design.** The webhook is a pure function of the object it's asked to review -- it holds no cluster credentials, calls back to nothing, and its own ServiceAccount (confirmed directly: `default`, in `guardrail-system`) has zero RBAC bindings at all. There is nothing for it to leak beyond what was already in the request. |
| Denial of service | Could the webhook's own availability become the cluster's availability? | **Yes, by deliberate design choice -- the most important tradeoff in this whole project.** `failurePolicy: Fail` means a webhook outage blocks all pod creation cluster-wide, reproduced live by scaling it to zero and watching a *compliant* pod get refused. This is a chosen, documented tradeoff (see `architecture.md`'s reasoning and the runbook's Scenario C), not an oversight -- but it is real, and it's the one item on this whole list where "mitigated" would be the wrong word. It's *handled* (there's a runbook, a diagnosis path, and a documented emergency escape hatch), not eliminated. |
| Elevation of privilege | Could a malicious object under review trick the webhook into granting more than it should? | **Mitigated.** The webhook only ever returns `allowed: true` or `allowed: false` -- it cannot mutate the object, grant permissions, or take any action beyond that one boolean. There's no privilege for a malicious payload to escalate *to*. |

## Summary

Every **Elevation of privilege** row across all four boundaries is
Mitigated -- that's not a coincidence, it's the actual design goal of
keeping CI, GitOps, and admission control as three separate trust levels
with authority flowing one direction. The real residual risks cluster
somewhere else entirely: availability (the `failurePolicy: Fail` tradeoff),
a few Denial-of-service rows accepted as low-risk-and-out-of-scope for a
solo project, and two concrete hardening gaps (digest pinning, branch
protection) named specifically enough to act on rather than gestured at.
