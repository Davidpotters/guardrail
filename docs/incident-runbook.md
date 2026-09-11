# Incident runbook: a deploy got rejected

Three scenarios, each actually reproduced against the running cluster --
every command and error message below is a real transcript, not a
prediction of what would happen.

## Scenario A: someone bypassed the pipeline with a direct `kubectl apply`

**Symptom:** `kubectl apply` exits non-zero immediately. No pod, no
ReplicaSet, nothing enters the cluster at all.

```
$ kubectl apply -f docs/examples/noncompliant-pod.yaml
Error from server: error when creating "docs/examples/noncompliant-pod.yaml": admission webhook "guardrail.davidpotters.dev" denied the request: guardrail policy violation:
  - container "app" must set resource limits for both cpu and memory
  - container "app" must explicitly set securityContext.runAsNonRoot: true (pod or container level)
  - container "app" image "nginx" must not use the :latest tag (or no tag, which means :latest) -- pin a version or digest
```

**Diagnosis:** none needed -- the error names every violation directly.
This is the simple case: the webhook did its job, the fix is to correct
the manifest and re-apply. No investigation required.

**This is also the proof of the project's actual thesis:** this pod was
never touched by CI, Argo CD, or any pipeline. A person ran `kubectl`
against the cluster directly, and it was rejected anyway, because the
webhook is a property of the cluster's API server, not of whatever
deployment mechanism happened to be used.

## Scenario B: a noncompliant change went through the trusted GitOps path

This one is less obvious, and worth understanding precisely because it
*isn't* a flat rejection.

**Symptom:** Argo CD's sync operation itself reports `Succeeded`. The
Application's health goes to `Progressing` and stays there instead of
settling on `Healthy`. Nothing looks broken from `argocd app get` alone.

**Why:** the webhook validates `pods`, not `deployments`. When a commit
changes a Deployment's pod template, Argo CD's sync updates the
Deployment object -- and that update succeeds, because the webhook never
sees it. Kubernetes' own Deployment controller then creates a *new*
ReplicaSet to roll the change out, and *that* controller's attempts to
create the new pods are what actually hit the webhook.

**Diagnosis:**

```
$ kubectl get rs -n demo
NAME                  DESIRED   CURRENT   READY   AGE
demo-app-69ff9cb5b4   3         3         3       21h
demo-app-b4d8d76f5    1         0         0       22s

$ kubectl get events -n demo --sort-by='.lastTimestamp' | tail -3
LAST SEEN   TYPE      REASON              OBJECT                          MESSAGE
22s         Normal    ScalingReplicaSet   deployment/demo-app             Scaled up replica set demo-app-b4d8d76f5 from 0 to 1
1s          Warning   FailedCreate        replicaset/demo-app-b4d8d76f5   Error creating: admission webhook "guardrail.davidpotters.dev" denied the request: guardrail policy violation:...
```

The new ReplicaSet is stuck at `0/1` ready. `kubectl get events`, not
`argocd app get`, is where the real reason lives.

**What actually happened to traffic:** nothing. The old ReplicaSet's
three compliant pods were never touched -- Kubernetes' rolling-update
strategy won't scale down the old ReplicaSet until the new one is ready,
and the new one can never become ready. The bad rollout is stuck, not
live. This was verified directly, not assumed: the same three original
pods (confirmed by name) were still `Running` throughout the whole
incident.

**Resolution:** revert the offending commit. Argo CD picks up the revert
on its next refresh, the bad ReplicaSet's desired count drops back to 0,
and Kubernetes removes it on its own -- no manual cleanup needed.

## Scenario C: the webhook itself is down

This is the scenario that actually matters for an on-call operator,
because it's the one where the security control becomes an availability
problem instead of a compliance one.

`failurePolicy: Fail` (set deliberately in
`manifests/webhook/validatingwebhookconfiguration.yaml`) means: if the
API server can't reach the webhook at all, it fails closed -- every pod
creation is blocked, not just noncompliant ones. That's the philosophically
correct choice for a policy that claims "can't be bypassed" (fail *open*
would mean a webhook outage silently disables all enforcement), but it
means a bug or outage in this webhook is a cluster-wide incident, not a
contained one. Worth knowing before it happens at 2am, not during.

**Reproduced directly** by scaling the webhook to zero and trying to
create a pod that violates nothing at all:

```
$ kubectl scale deployment/guardrail-webhook -n guardrail-system --replicas=0
$ kubectl apply -f webhook-down-test.yaml
Error from server (InternalError): error when creating "STDIN": Internal error occurred: failed calling webhook "guardrail.davidpotters.dev": failed to call webhook: Post "https://guardrail-webhook.guardrail-system.svc:443/validate?timeout=5s": dial tcp 10.96.209.8:443: connect: connection refused
```

Note the error itself: `dial tcp ... connect: connection refused`, not
a policy violation. That distinction is the entire diagnosis -- a
"guardrail policy violation" message means the webhook is up and doing
its job; a connection/timeout error means the webhook itself is the
problem, and the fix is to the webhook's deployment, not the pod that
was trying to get created.

**Diagnosis checklist:**
1. `kubectl get pods -n guardrail-system` -- are both replicas `Running`?
2. `kubectl get events -n guardrail-system` -- crash loop, image pull
   failure, OOM?
3. `kubectl logs -n guardrail-system deploy/guardrail-webhook` -- did it
   even start listening?

**Resolution, in order of preference:**
1. Fix and restart the webhook (`kubectl rollout restart`, or roll back
   to the previous image tag in `manifests/webhook/deployment.yaml` if a
   bad version was the cause). This is the real fix and restores
   enforcement immediately once it lands.
2. **Emergency-only escape hatch:** temporarily patch
   `failurePolicy` to `Ignore` on the `ValidatingWebhookConfiguration`
   so the cluster can accept deploys again while the webhook is being
   fixed. This is a deliberate, documented tradeoff, not a hidden one --
   flipping it means *zero* policy enforcement cluster-wide until it's
   flipped back to `Fail`, so it should never be left in that state
   longer than the active incident.

**Verified recovered:** scaled the webhook back to 2 replicas, waited
for the rollout, and re-applied the exact same compliant pod that had
just been rejected -- it was created successfully on the first retry,
no other changes needed.
