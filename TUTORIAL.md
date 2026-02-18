# Building the WebApp Operator — A Narrative Tutorial

This tutorial walks through building the WebApp operator from scratch, explaining every decision along the way. It is structured around two tiers:

- **MVP** — the bare minimum for a functional, trustworthy operator suitable for internal testing
- **Production-Ready** — what elevates the MVP to something you can confidently run in a real cluster at scale

For each component you will find:
- What it is and why it exists
- Whether it belongs in the MVP or can be added later
- The tradeoff of skipping it
- What "adding it later" actually costs in terms of refactoring

---

## Table of Contents

1. [Mental Model — What an Operator Actually Is](#1-mental-model)
2. [The Two Tiers — MVP vs Production-Ready](#2-the-two-tiers)
3. [Project Initialization](#3-project-initialization)
4. [Defining Your API — The CRD Types](#4-defining-your-api)
5. [The Reconcile Loop — Core Pattern](#5-the-reconcile-loop)
6. [Managing Child Resources — Deployment and Service](#6-managing-child-resources)
7. [Finalizers — Never Skip This](#7-finalizers)
8. [Status and Conditions — Observability of State](#8-status-and-conditions)
9. [RBAC — Least Privilege From Day One](#9-rbac)
10. [Leader Election — On by Default, Disable for Local Dev](#10-leader-election)
11. [Metrics — Add Later, Low Lift](#11-metrics)
12. [Configuration Management — Env Vars, ConfigMaps, and GitOps](#12-configuration-management)
13. [What the MVP Looks Like End to End](#13-mvp-summary)
14. [The Path From MVP to Production-Ready](#14-path-to-production)
15. [Operator-to-Market Roadmap — From Idea to Platform Initiative](#15-roadmap)

---

## 1. Mental Model

An operator is a **control loop** that runs inside your cluster and continuously asks one question:

> "Does the current state of the world match what the user declared they want?"

If the answer is no, it acts to close the gap. If the answer is yes, it does nothing (or schedules a future check).

This is called **reconciliation**, and it is the entire job of an operator.

```
User applies a WebApp CR
        │
        ▼
Operator watches for changes (via informers)
        │
        ▼
Reconcile() is called with the CR's namespace/name
        │
        ├── Fetch the CR from the API server
        ├── Compare desired state (spec) vs actual state (cluster)
        ├── Create / update / delete child resources to close the gap
        ├── Update status to reflect what actually happened
        └── Return — either done, or requeue after a duration
```

Three things to internalize before writing a single line:

1. **Reconcile is called many times** — on create, update, delete, and on a timer. Design every function assuming it has been called 100 times already.
2. **You do not own the cluster** — other controllers, humans, and automation are also mutating resources. Your operator must handle drift gracefully.
3. **The API server is the source of truth** — never cache state in memory between reconcile calls. Always re-fetch.

---

## 2. The Two Tiers

### MVP — Bare Minimum for Internal Testing

An operator at MVP level must:

| Component | Why It's Required at MVP |
|-----------|--------------------------|
| CRD with Spec + Status | Without this there is no custom resource to manage |
| Reconcile loop | The operator's entire purpose |
| Child resource management (Deployment + Service) | The actual work the operator does |
| Owner references on child resources | Without this, deleting the CR leaks the Deployment and Service |
| Finalizer | Without this, deletion can leave external state orphaned — **never skip** |
| Basic status reporting | Without this, operators are black boxes — unusable in practice |
| Least-privilege RBAC | Without this, your operator is a security risk from day one |

### Production-Ready — What Gets Added

| Component | Why It Matters at Scale | Refactor Cost if Added Later |
|-----------|------------------------|------------------------------|
| Structured status conditions (`metav1.Condition`) | Enables GitOps tooling, automation, and alerting to read state programmatically | Low — additive change to the Status struct |
| Leader election | Required when running >1 replica for HA | Low — single flag in `main.go` |
| Prometheus metrics | Reconcile latency, error rates, queue depth | Low — `controller-runtime` exposes these automatically |
| Custom metrics | Domain-specific observability | Medium — requires registering collectors |
| `envtest` integration tests | Catch regressions without a real cluster | Medium — test suite setup has some boilerplate |
| Webhook validation | Reject invalid CRs before they hit the reconciler | High — requires cert infrastructure |
| OLM bundle / OperatorHub packaging | Required for distribution | High — separate workflow entirely |

**Key insight**: Everything in the "Low" refactor cost column should be in the MVP. The cost of adding it later is negligible, but the cost of operating without it is real.

---

## 3. Project Initialization

```bash
operator-sdk init \
  --domain 54b3r.io \
  --repo github.com/54b3r/platform-operator-blueprint

operator-sdk create api \
  --group app \
  --version v1alpha1 \
  --kind WebApp \
  --resource \
  --controller
```

### What `--domain` Does

The domain becomes the API group prefix: `app.54b3r.io`. This is how Kubernetes namespaces your CRD away from built-in resources. Choose something you own — a real domain or your org's namespace — because changing it later requires migrating all existing CRs.

### What `--repo` Does

Sets the Go module path. This must match your actual repository path because Go imports are absolute. If you rename the repo later, you will need to update every import in the project.

### What Gets Generated

After these two commands you have a compilable, runnable (but empty) operator. The key files:

- **`main.go`** — sets up the manager, registers the controller, starts the informer cache and reconcile workers
- **`api/v1alpha1/webapp_types.go`** — your CRD struct, currently empty stubs
- **`controllers/webapp_controller.go`** — your reconciler, currently a no-op
- **`config/`** — Kustomize manifests for CRDs, RBAC, and the manager Deployment

---

## 4. Defining Your API

`api/v1alpha1/webapp_types.go` is where you define what a `WebApp` looks like to the user. This is a contract — once CRs exist in a cluster, changing field names or types is a breaking change.

### Spec — Desired State

```go
type WebAppSpec struct {
    Image    string `json:"image"`
    Replicas int32  `json:"replicas,omitempty"`
    Port     int32  `json:"port,omitempty"`
}
```

**Design rules for Spec:**
- Fields should represent **intent**, not implementation details
- Use `omitempty` for optional fields with sensible defaults
- Never put runtime state in Spec — that belongs in Status
- Think carefully before adding a field — removing one later is a breaking change

### Status — Observed State

```go
type WebAppStatus struct {
    Conditions        []metav1.Condition `json:"conditions,omitempty"`
    AvailableReplicas int32              `json:"availableReplicas,omitempty"`
}
```

**Why `metav1.Condition` instead of a plain string?**

A plain string like `Status: "Ready"` is human-readable but machine-unfriendly. `metav1.Condition` is the standard Kubernetes condition type used by Deployments, Nodes, and every core resource. It gives you:
- `Type` — what aspect of the resource this condition describes (e.g. `Available`)
- `Status` — `True`, `False`, or `Unknown`
- `Reason` — a machine-readable CamelCase reason code
- `Message` — a human-readable explanation
- `ObservedGeneration` — which version of the spec this condition reflects (critical for detecting stale status)

GitOps tools (Flux, ArgoCD), alerting systems, and automation can all read conditions programmatically. A plain string cannot be reliably parsed.

**Tradeoff of skipping conditions at MVP**: You can start with `AvailableReplicas` only and add conditions later. The refactor cost is low — it's an additive change to the struct. But if you're already writing the status update logic, adding conditions costs almost nothing now.

### Markers

Above the types, add kubebuilder markers that control CRD generation:

```go
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas"
//+kubebuilder:printcolumn:name="Available",type="integer",JSONPath=".status.availableReplicas"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
```

The `printcolumn` markers control what `kubectl get webapps` shows. This is a small thing that makes a big difference in day-to-day usability.

---

## 5. The Reconcile Loop

The reconcile function is the heart of the operator. Everything else supports it.

```go
func (r *WebAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    // Always fetch fresh — never trust cached state from a previous call
    app := &appv1alpha1.WebApp{}
    if err := r.Get(ctx, req.NamespacedName, app); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Handle deletion before anything else
    if !app.DeletionTimestamp.IsZero() {
        return r.handleDeletion(ctx, app)
    }

    // Ensure finalizer is registered
    if !controllerutil.ContainsFinalizer(app, webappFinalizer) {
        controllerutil.AddFinalizer(app, webappFinalizer)
        return ctrl.Result{}, r.Update(ctx, app)
    }

    // Reconcile child resources
    if err := r.reconcileDeployment(ctx, app); err != nil {
        return ctrl.Result{}, err
    }
    if err := r.reconcileService(ctx, app); err != nil {
        return ctrl.Result{}, err
    }

    // Update status last — after all mutations are done
    if err := r.updateStatus(ctx, app); err != nil {
        return ctrl.Result{}, err
    }

    log.Info("Reconciled WebApp", "name", req.Name)
    return ctrl.Result{RequeueAfter: time.Minute}, nil
}
```

### Return Values

| Return | Meaning |
|--------|---------|
| `ctrl.Result{}, nil` | Done, no requeue unless a watched resource changes |
| `ctrl.Result{Requeue: true}, nil` | Requeue immediately |
| `ctrl.Result{RequeueAfter: d}, nil` | Requeue after duration `d` |
| `ctrl.Result{}, err` | Requeue with exponential backoff |

**Why `RequeueAfter: time.Minute`?**

Even if nothing changes, you want the operator to periodically re-check for drift — a human may have manually edited the Deployment, or a pod may have crashed. One minute is a reasonable default for most operators. Adjust based on how quickly you need to detect and correct drift.

### Error Handling

Return errors for transient failures (API server unavailable, resource conflict). Do not return errors for expected conditions like "resource not found" — use `client.IgnoreNotFound()` for those.

When you return an error, `controller-runtime` will requeue with exponential backoff. This is the correct behavior for transient failures — it prevents thundering herd on a degraded API server.

---

## 6. Managing Child Resources

The operator's job is to ensure a `Deployment` and `Service` exist and match the `WebApp` spec. The pattern for each is identical:

1. Try to fetch the existing resource
2. If it doesn't exist, create it
3. If it exists but is out of sync with the spec, update it
4. Set an owner reference so Kubernetes garbage collects it when the CR is deleted

### Owner References — Never Skip This

```go
if err := controllerutil.SetControllerReference(app, deployment, r.Scheme); err != nil {
    return err
}
```

This single line sets the `ownerReferences` field on the child resource. When the `WebApp` CR is deleted, Kubernetes automatically deletes the `Deployment` and `Service` — no custom cleanup code needed for in-cluster resources.

**What happens if you skip it**: Deleting a `WebApp` CR leaves the `Deployment` and `Service` running indefinitely. They become orphaned resources that consume cluster capacity and are invisible to the operator. In a busy cluster this accumulates into significant resource waste and confusion.

**Tradeoff**: There is no tradeoff. Always set owner references on child resources. The only exception is when the child resource lives in a different namespace or is cluster-scoped — in those cases you need a finalizer instead (which we cover next).

### Create-or-Update Pattern

```go
func (r *WebAppReconciler) reconcileDeployment(ctx context.Context, app *appv1alpha1.WebApp) error {
    desired := r.buildDeployment(app)

    existing := &appsv1.Deployment{}
    err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)

    if apierrors.IsNotFound(err) {
        return r.Create(ctx, desired)
    }
    if err != nil {
        return err
    }

    // Update only the fields we own — do not overwrite fields set by other controllers
    existing.Spec.Replicas = desired.Spec.Replicas
    existing.Spec.Template.Spec.Containers[0].Image = desired.Spec.Template.Spec.Containers[0].Image
    return r.Update(ctx, existing)
}
```

**Why selective field updates?** Other controllers (HPA, admission webhooks, service meshes) may also be writing to the same resource. Blindly replacing the entire spec will cause a fight between controllers. Only update the fields your operator owns.

---

## 7. Finalizers — Never Skip This

A finalizer is a string key placed on a resource's `metadata.finalizers` list. Kubernetes will not delete the resource from etcd until all finalizers have been removed. This gives your operator a guaranteed window to run cleanup logic before the object disappears.

```go
const webappFinalizer = "app.54b3r.io/finalizer"

func (r *WebAppReconciler) handleDeletion(ctx context.Context, app *appv1alpha1.WebApp) (ctrl.Result, error) {
    if controllerutil.ContainsFinalizer(app, webappFinalizer) {
        // Run your cleanup logic here
        // For this operator: child resources are handled by owner references,
        // but if you were managing external resources (DNS, cloud LBs, databases)
        // you would clean them up here before removing the finalizer

        controllerutil.RemoveFinalizer(app, webappFinalizer)
        if err := r.Update(ctx, app); err != nil {
            return ctrl.Result{}, err
        }
    }
    return ctrl.Result{}, nil
}
```

### Why This Is in the MVP — Not Optional

**Scenario without a finalizer:**

1. User deletes the `WebApp` CR
2. Kubernetes immediately removes it from etcd
3. Your operator was mid-reconcile managing an external load balancer
4. The CR is gone — the operator has no way to know it needs to clean up the load balancer
5. The load balancer runs forever, accruing cost, with no owner

For this specific operator (Deployment + Service only), owner references handle in-cluster cleanup. But the finalizer pattern costs almost nothing to implement and is the correct foundation for any operator that might later manage external resources. Retrofitting finalizers into an operator that already has live CRs in production is painful — you have to handle the migration of existing objects that don't have the finalizer yet.

**Add it now. Always.**

### What Happens During `kubectl delete`

```
kubectl delete webapp webapp-sample
        │
        ▼
Kubernetes sets DeletionTimestamp on the CR (does NOT delete yet)
        │
        ▼
Operator's Reconcile() is triggered
        │
        ▼
Operator sees DeletionTimestamp.IsZero() == false
        │
        ▼
Operator runs cleanup logic
        │
        ▼
Operator removes finalizer via r.Update()
        │
        ▼
Kubernetes sees finalizers list is empty → deletes the CR from etcd
```

---

## 8. Status and Conditions

Status is how your operator communicates back to the user and to automation. It is the difference between an operator that is a black box and one that is observable.

### The Two Levels

**Level 1 — Scalar fields (MVP minimum):**
```go
app.Status.AvailableReplicas = deployment.Status.AvailableReplicas
```
Simple, readable with `kubectl get webapp`, but not machine-parseable in a structured way.

**Level 2 — Structured conditions (production-ready, low lift):**
```go
meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
    Type:               "Available",
    Status:             metav1.ConditionTrue,
    Reason:             "DeploymentAvailable",
    Message:            fmt.Sprintf("%d/%d replicas available", deployment.Status.AvailableReplicas, *deployment.Spec.Replicas),
    ObservedGeneration: app.Generation,
})
```

### Why `ObservedGeneration` Matters

`Generation` is incremented every time the CR's `spec` changes. `ObservedGeneration` on a condition tells you which version of the spec that condition reflects. Without it, you cannot tell whether a `Ready: True` condition was set before or after the last spec change — making status unreliable during rolling updates.

### Always Use `r.Status().Update()` — Not `r.Update()`

The status subresource is a separate API endpoint. Using `r.Update()` to write status will be silently ignored if the CRD has `//+kubebuilder:subresource:status` enabled (which it should). Always use `r.Status().Update()` for status writes.

**Tradeoff of skipping conditions**: You can ship with scalar fields only and add conditions later. The refactor is additive — you are adding fields to the struct and adding `meta.SetStatusCondition` calls. No existing fields need to change. Cost is low. But given that it costs almost nothing to add now, there is little reason to defer it.

---

## 9. RBAC — Least Privilege From Day One

RBAC markers in the controller file generate the `ClusterRole` that the operator's service account is bound to. These are the only permissions the operator will have.

```go
//+kubebuilder:rbac:groups=app.54b3r.io,resources=webapps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=app.54b3r.io,resources=webapps/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=app.54b3r.io,resources=webapps/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
```

### Why This Is MVP — Not Optional

In a multi-tenant cluster, an over-permissioned operator is a lateral movement vector. If the operator pod is compromised, an attacker inherits its permissions. Wildcards (`verbs=*`, `resources=*`) are never acceptable in production.

**Common mistakes:**
- Forgetting the `finalizers` subresource — the operator will fail to add/remove finalizers silently
- Forgetting the `status` subresource — `r.Status().Update()` will return a 403
- Using `verbs=*` because it's easier — don't

**Tradeoff of getting this wrong**: You cannot easily reduce permissions on a running operator without redeploying. Expanding permissions is easy; contracting them risks breaking things if you miss something. Start tight and expand as needed.

---

## 10. Leader Election — On by Default, Disable for Local Dev

When you run more than one replica of your operator (which you should in production for availability), without leader election all replicas will attempt to reconcile the same resources simultaneously. This causes:
- Conflicting writes and resource version conflicts
- Flapping state as replicas fight over the desired state
- Unpredictable behavior that is very hard to debug

Leader election uses a Kubernetes `Lease` object to elect one active leader. All other replicas stand by and take over only if the leader fails.

### Why We Enable It in the MVP by Default

The cost is one flag in `main.go` and one RBAC marker. That's it. There is no architectural change, no new dependency, and no meaningful complexity added. The cost of enabling it is near zero. The cost of forgetting to enable it before you scale to multiple replicas is a production incident.

We build with it on from day one and disable it only for local development:

```go
mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
    LeaderElection:          true,
    LeaderElectionID:        "webapp.54b3r.io",
    LeaderElectionNamespace: "platform-operator-blueprint-system",
})
```

Required RBAC marker:
```go
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
```

### Disabling for Local Dev

When running `make run` locally, leader election attempts to acquire a `Lease` in the cluster. This works fine with `kind`, but if you want to skip it during rapid iteration you can pass a flag:

```bash
make run ARGS="--leader-elect=false"
```

Or set it via environment variable in your local shell. The binary always defaults to `--leader-elect=true` — the safe default for any deployed environment.

**Rule of thumb**: The binary is always built with leader election on. The only place it is ever disabled is in a developer's local shell, never in a deployed manifest.

---

## 11. Metrics — Add Later, Low Lift

`controller-runtime` automatically exposes the following Prometheus metrics at `:8080/metrics` with zero configuration:

| Metric | What It Tells You |
|--------|------------------|
| `controller_runtime_reconcile_total` | Total reconcile calls, labeled by result (success/error) |
| `controller_runtime_reconcile_errors_total` | Error count — alert on this |
| `controller_runtime_reconcile_time_seconds` | Reconcile latency histogram |
| `workqueue_depth` | How backed up the reconcile queue is |

These are available the moment your operator starts. You do not need to write any code.

### Custom Metrics

For domain-specific metrics (e.g. number of managed WebApps, number of failed deployments), you register a Prometheus collector:

```go
var managedWebApps = prometheus.NewGauge(prometheus.GaugeOpts{
    Name: "webapp_operator_managed_total",
    Help: "Total number of WebApp resources being managed",
})
```

**When to add custom metrics**: After you have the operator running and can observe what questions you are actually asking about it in production. Adding them speculatively before you know what you need is premature.

**Refactor cost**: Low. Custom metrics are additive — register a collector, increment/decrement it in the reconcile loop. No existing code needs to change.

---

## 12. Configuration Management — Env Vars, ConfigMaps, and GitOps

Hard-coded values in operator code are a portability and security problem. Configuration that varies between environments (dev, staging, prod) should be injected at deploy time, not baked into the binary. This section covers the full stack: env vars in code, ConfigMaps as the source, and Kustomize overlays as the GitOps-friendly delivery mechanism.

### What Should Be Configurable

| Value | Why |
|-------|-----|
| Default image tag | Differs between environments |
| Default replica count | Differs between environments |
| Reconcile requeue interval | May need tuning per environment |
| Feature flags | Enable/disable behavior without redeploying |
| External service endpoints | Never hardcode URLs |
| Leader election toggle | Off for local dev, on everywhere else |

### Layer 1 — Env Vars in Code

The operator binary reads configuration from environment variables with safe defaults:

```go
func getEnvOrDefault(key, defaultVal string) string {
    if val := os.Getenv(key); val != "" {
        return val
    }
    return defaultVal
}

// Usage in reconciler
defaultImage := getEnvOrDefault("DEFAULT_IMAGE", "nginx:latest")
requeueInterval, _ := time.ParseDuration(getEnvOrDefault("REQUEUE_INTERVAL", "60s"))
```

This keeps the binary environment-agnostic. The same image runs in dev, staging, and prod — only the injected values differ.

### Layer 2 — ConfigMap as the Config Source

Rather than hardcoding values directly in the Deployment manifest, store them in a ConfigMap. This makes config changes a VCS commit, not a binary rebuild or manifest edit:

```yaml
# config/manager/operator-config.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: webapp-operator-config
  namespace: platform-operator-blueprint-system
data:
  DEFAULT_IMAGE: "nginx:latest"
  REQUEUE_INTERVAL: "60s"
```

Reference the ConfigMap in the manager Deployment:

```yaml
# config/manager/manager.yaml
envFrom:
  - configMapRef:
      name: webapp-operator-config
```

Now changing a config value is a one-line diff in `operator-config.yaml` — reviewable, auditable, and rollback-able via git.

### Layer 3 — Kustomize Overlays for Environment Promotion

The `config/` directory is already structured for Kustomize. Use overlays to manage environment-specific values without duplicating manifests:

```
config/
├── base/                        # Shared across all environments
│   ├── kustomization.yaml
│   ├── manager/
│   │   ├── manager.yaml
│   │   └── operator-config.yaml
│   └── rbac/
└── overlays/
    ├── dev/
    │   ├── kustomization.yaml
    │   └── operator-config-patch.yaml   # DEFAULT_IMAGE: nginx:latest
    ├── staging/
    │   ├── kustomization.yaml
    │   └── operator-config-patch.yaml   # DEFAULT_IMAGE: nginx:1.25
    └── prod/
        ├── kustomization.yaml
        └── operator-config-patch.yaml   # DEFAULT_IMAGE: nginx:1.25-hardened
```

Deploy to a specific environment:
```bash
kubectl apply -k config/overlays/staging
```

### GitOps Integration (Flux / ArgoCD)

This structure is directly consumable by GitOps tooling. Point Flux or ArgoCD at `config/overlays/prod` and every merged PR that touches that directory triggers a reconciled deployment — no manual `kubectl apply` in production, ever.

```yaml
# Example Flux Kustomization
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: webapp-operator
spec:
  path: ./config/overlays/prod
  sourceRef:
    kind: GitRepository
    name: platform-operator-blueprint
  interval: 5m
  prune: true
```

### What This Buys You

| Capability | How |
|------------|-----|
| Config changes without rebuilding | ConfigMap + envFrom |
| Environment promotion | Kustomize overlays |
| Audit trail for config changes | Git history on ConfigMap files |
| GitOps-driven deployment | Flux/ArgoCD pointing at overlay path |
| Secret injection | Replace ConfigMap with SecretRef for sensitive values |

**On Secrets**: Never put sensitive values (credentials, tokens, API keys) in a ConfigMap. Use a `Secret` with `secretRef` in the same `envFrom` block, or an external secrets operator (External Secrets Operator, Vault Agent) for production.

---

## 13. MVP Summary

A WebApp operator at MVP level has exactly these components — nothing more, nothing less:

```
✅ CRD with Spec (image, replicas, port) and Status (availableReplicas, conditions)
✅ Reconcile loop — idempotent, fetches fresh state every call
✅ Deployment management — create-or-update, selective field updates
✅ Service management — create-or-update
✅ Owner references on all child resources
✅ Finalizer — registered on create, removed on cleanup
✅ Basic status update — availableReplicas + at least one condition
✅ Least-privilege RBAC markers (including finalizers and leases subresources)
✅ Leader election enabled by default in main.go — disable only via flag for local dev
✅ Env vars for all configurable values, sourced from a ConfigMap
✅ Kustomize overlay structure for environment promotion
```

What is explicitly **not** in the MVP:
- Custom Prometheus metrics (built-in controller-runtime metrics are sufficient)
- Webhook validation
- `envtest` integration tests (unit tests are sufficient at MVP)
- GitOps tooling configuration (Flux/ArgoCD) — the structure supports it, wiring it up is environment-specific
- OLM bundle

---

## 14. The Path From MVP to Production-Ready

Once the MVP is running in your internal cluster, this is the ordered path to production-readiness:

| Step | What | Why Now | Refactor Cost |
|------|------|---------|---------------|
| 1 | Wire GitOps tooling to overlay path | Before any team other than you deploys this | None — structure already exists |
| 2 | Add `envtest` integration tests | Before promoting to staging | Medium — test suite boilerplate |
| 3 | Add custom Prometheus metrics + alerts | Once you know what to observe | Low — additive |
| 4 | Add webhook validation | Once API is stable, before external consumers | High — cert infrastructure required |
| 5 | Multi-replica deployment (leader election already on) | Before any HA requirement | None — already enabled |
| 6 | OLM bundle + OperatorHub | If distributing externally | High — separate workflow |

Steps 1 and 5 cost nothing because the MVP already accounts for them. Steps 2–3 are the natural next investments after internal validation. Steps 4 and 6 are deferred intentionally — they require infrastructure and process maturity that should not block an MVP.

---

## 15. Operator-to-Market Roadmap — From Idea to Platform Initiative

This section covers the non-technical journey: how you take an operator idea from concept to a platform capability that a team or organization actually adopts and relies on.

### Phase 1 — Discovery and Problem Definition

Before writing any code, answer these questions:

- **What manual operational task does this operator eliminate?** If you cannot answer this in one sentence, the scope is not clear enough.
- **Who are the consumers?** Internal platform teams, application developers, or external users? The answer shapes the API design.
- **What is the blast radius of a bug?** An operator that manages a single Deployment is low risk. One that manages databases or network infrastructure is high risk — the bar for testing and validation is higher.
- **Is an operator the right tool?** Operators are appropriate when the operational logic is complex, stateful, or domain-specific. If a Helm chart or simple Deployment would suffice, use that instead.

### Phase 2 — API Design (Before Any Code)

The CRD is a public API. Treat it like one.

- Draft the `Spec` and `Status` structs as a YAML document first — no Go code yet
- Review with consumers: does this API make sense to the people who will use it?
- Define the conditions your operator will set and what each one means
- Identify what is in scope for v1alpha1 and what is explicitly deferred to v1beta1
- Document breaking vs non-breaking changes — adding optional fields is safe, renaming is not

### Phase 3 — MVP Build and Internal Validation

This is what this repository covers. The goal is a working operator that:
- Runs in a `kind` cluster
- Passes a basic smoke test (apply CR, verify child resources, delete CR, verify cleanup)
- Has the MVP components from section 13 in place
- Is reviewed by at least one other engineer before being called done

### Phase 4 — Internal Staging Rollout

- Deploy to a non-production cluster with real workloads
- Write `envtest` integration tests covering the happy path and key failure modes
- Set up Prometheus alerts on `controller_runtime_reconcile_errors_total`
- Document runbooks: what does an on-call engineer do if the operator is down? If it is stuck reconciling?
- Define an SLO: what is the acceptable reconcile latency? What is the acceptable error rate?

### Phase 5 — Production Promotion

- Multi-replica deployment (leader election already on from MVP)
- GitOps pipeline wired to the prod overlay
- Incident response runbook reviewed and published
- Rollback procedure documented and tested — can you safely disable the operator without breaking existing workloads?
- Change management: how do consumers get notified of API changes?

### Phase 6 — Platform Capability (Ongoing)

An operator that reaches this phase is a platform primitive — something other teams build on top of.

- **Versioning**: Graduate from `v1alpha1` to `v1beta1` to `v1` as the API stabilizes. Each graduation requires a conversion webhook if fields change.
- **Multi-tenancy**: Does the operator need namespace-scoped vs cluster-scoped behavior? Does it need to enforce tenant isolation?
- **Deprecation policy**: How long do you support old API versions? Kubernetes itself maintains n-2 version support.
- **Operator lifecycle**: Who owns this operator long-term? What is the on-call rotation? What is the process for accepting external contributions?

### Summary Timeline

```
Week 1-2   │ Discovery + API design (no code)
Week 2-4   │ MVP build (this repository)
Week 4-6   │ Internal staging rollout + integration tests
Week 6-8   │ Production promotion + GitOps wiring
Ongoing    │ Platform capability — versioning, multi-tenancy, lifecycle
```

The timeline compresses or expands based on blast radius. A low-risk operator managing stateless workloads can move from MVP to production in two weeks. One managing stateful infrastructure should take longer — the validation phase is not optional.

---

*This tutorial accompanies the code in this repository. Each section maps directly to a file or function in the codebase.*
