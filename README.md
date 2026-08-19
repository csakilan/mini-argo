# mini-argo

A small GitOps operator for Kubernetes. You point it at a public Git repository
full of YAML, and it continuously makes your cluster match that repository,
including undoing changes somebody makes by hand.

It is a deliberately scoped-down reimplementation of the core of ArgoCD and
Flux, built to understand how they work.

---

## The demo

```console
$ kubectl get deployment web -n demo
NAME   READY   UP-TO-DATE   AVAILABLE   AGE
web    3/3     3            3           2m

$ kubectl scale deployment web -n demo --replicas=9
deployment.apps/web scaled

$ kubectl get deployment web -n demo -o jsonpath='{.spec.replicas}'
9

# ...wait one interval...

$ kubectl get deployment web -n demo -o jsonpath='{.spec.replicas}'
3
```

**Nothing told the controller that happened.** No event fired, no webhook was
called, no notification was delivered. mini-argo watches `GitSource` objects
and nothing else, so a Deployment changing produces no signal it can hear.

It woke up on a timer, read the live object, compared it to Git, and closed the
gap. That is *level-triggered reconciliation*, and it is the whole point.

Run above: scaled at 15:01:25, corrected by 15:01:50.

---

## Try it

Needs Docker, [kind](https://kind.sigs.k8s.io), and Go 1.26.

```bash
make demo
```

That creates a cluster if you have not got one, builds the image, loads it into
the cluster, installs the CRD, deploys the operator, and points a `GitSource`
at [csakilan/argo-test](https://github.com/csakilan/argo-test).

Then watch it work:

```bash
kubectl port-forward -n mini-argo-system deployment/mini-argo-controller-manager 8082:8082
```

Open <http://localhost:8082> and break something:

```bash
kubectl scale deployment web -n demo --replicas=7
```

Tear it down with `make demo-clean`.

---

## What it does

You create one of these:

```yaml
apiVersion: gitops.miniargo.dev/v1alpha1
kind: GitSource
metadata:
  name: demo-app
spec:
  repo: https://github.com/csakilan/argo-test
  branch: main
  path: ./manifests
  interval: 30s
  prune: false
```

and mini-argo will:

| | |
|---|---|
| Clone that repository every `interval` | Shallow, single-branch, thrown away afterwards |
| Read every `.yaml` and `.yml` under `path` | Recursively, in a stable order, multi-document files included |
| Apply everything to the cluster | Server-side apply, under the field owner `mini-argo` |
| Revert anything changed by hand | Because apply owns those fields |
| Report what it corrected | `status.driftedObjects`, down to the field path |
| Remember what it owns | `status.inventory` |
| Delete objects removed from Git | Opt-in per source, off by default |
| Clean up when the GitSource is deleted | Via a finalizer, so it is guaranteed rather than best-effort |
| Show all of it on a page | Read-only, served by the same binary |

---

## What it deliberately does not do

This list matters as much as the one above. Every item is a real feature of the
real tools, left out on purpose.

| Not built | Why it is genuinely hard |
|---|---|
| **Helm or Kustomize rendering** | Turns "read files" into running a template engine with values, dependencies, and CRD ordering. A different project. |
| **Private repository auth** | SSH keys and tokens mean secret management, rotation, and a credential model. Orthogonal to reconciliation. |
| **Multi-cluster** | Needs credentials per cluster, connection health tracking, and a story for partial failure. |
| **Sync waves and ordering** | Requires a dependency graph and knowing when an object is "done", which needs the next row. |
| **Health assessment** | "Is this Deployment healthy?" has a different answer per resource kind. ArgoCD ships per-kind Lua scripts for exactly this. |
| **Per-source RBAC** | Requires impersonation so a GitSource cannot apply things its owner could not. Real security design. |
| **Webhook-triggered sync** | Needs a public endpoint, signature verification, and delivery guarantees. And you still need the polling loop as a backstop, or a dropped delivery means drift persists forever. |
| **Creating target namespaces** | A missing namespace is reported as an error and retried. Auto-creating is how a GitOps tool quietly sprawls across a cluster. ArgoCD makes it opt-in (`CreateNamespace=true`) for the same reason. |

It is about 900 lines of Go, and it does not do half of what Flux does. But I
understand exactly why those parts are hard now.

---

## How it works

### One loop

```
every spec.interval, for each GitSource:

    fetch     ──►  clone the repo, read the YAML          (Source interface)
    parse     ──►  bytes → Kubernetes objects             (pure function)
    label     ──►  stamp each object as owned by this source
    compare   ──►  read what's live, note what differs     (→ driftedObjects)
    apply     ──►  server-side apply each object           (this is the fix)
    inventory ──►  record what was applied                 (→ status)
    prune     ──►  delete what we used to own but no longer (if spec.prune)
    status    ──►  write it down, if it changed
    requeue   ──►  "call me again in spec.interval"
```

### Apply *is* the correction

There is no `if different, patch it` branch anywhere in this codebase. Apply
declares *these fields should hold these values, and this manager owns them*,
and the API server enforces it. Someone else's manual change to an owned field
simply loses.

```bash
kubectl get deployment web -n demo -o jsonpath='{.metadata.managedFields[*].manager}'
```

### Detection and correction are different jobs

Apply corrects without ever reading. The controller reads the live object
anyway, purely so status can *report* what changed. Delete `internal/drift`
entirely and drift is still corrected, silently. That separation is why the
read before the apply is not redundant.

### The inventory is what makes deletion safe

The naive rule, "delete whatever is in the cluster but not in Git", would
destroy objects the controller never created. And the ownership label cannot
answer "what do I own?" on its own: Kubernetes has no cross-type label query,
so answering it would mean enumerating every resource type in the cluster.

So mini-argo keeps a receipt. Pruning deletes `previous inventory − current
desired`, never touches anything whose ownership label names somebody else, and
is skipped entirely if any object failed to apply that pass.

---

## Layout

```
api/v1alpha1/          the GitSource type; the CRD is generated from it
cmd/main.go            wiring; the only file that knows where manifests come from
internal/
  manifests/           YAML bytes → objects            pure, no cluster
  source/              Snapshot + Source; DirFetcher, GitFetcher
  drift/               desired vs live comparison      pure, no cluster
  inventory/           set difference over ResourceRef pure, no cluster
  controller/          the reconcile loop              the only part needing a cluster
  httpapi/             read-only GET /api/gitsources
web/dashboard/         React + Vite status page
config/                generated CRD, RBAC, and deployment manifests
```

Everything that is a pure function lives outside the controller, which is what
lets most of the logic be tested with plain `go test` and no API server.

---

## Testing

```bash
make test   # unit tests, plus envtest for the controller
make lint
```

**envtest** runs the real `kube-apiserver` and `etcd` binaries locally, but with
no kubelet and no scheduler. Objects get created and no pod ever runs. A
Deployment sits at 0 ready replicas forever, and that is fine, because the only
question these tests ask is whether the controller wrote the right objects with
the right fields.

| Package | Coverage |
|---|---|
| `internal/inventory` | 100% |
| `internal/manifests` | 96% |
| `internal/httpapi` | 90% |
| `internal/drift` | 91% |
| `internal/controller` | 88% |
| `internal/source` | 84% |

The central test is the product specification:

> given a GitSource has synced a Deployment with 2 replicas, when somebody
> scales it to 5 behind the controller's back, then the next reconcile puts it
> back to 2, and `status.driftedObjects` names that Deployment.

---

## Things worth knowing

**You cannot delete a GitSource while the controller is down.** The finalizer
holds it in `Terminating` until cleanup runs. That is the feature working:
nothing leaks. Start the controller and it completes within seconds, even after
hours.

**`kubectl annotate` does not trigger a sync.** The watch is filtered to events
that change `metadata.generation`, which is what stops the controller waking
itself up every time it writes status. Annotations do not bump generation. Edit
`spec`, or wait for the interval.

**Rebuilding a `:dev` tag can silently keep running the old binary.** kind
caches images by tag. `make demo` tags every build uniquely and verifies the
digest the pod actually pulled, rather than trusting `kubectl rollout status`,
which can report success while the previous image is still running.

**Pruning is off by default and should stay that way.** Deleting a GitSource is
not a request to delete the workloads.

---

## Licence

Apache 2.0.
