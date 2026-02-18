# platform-operator-blueprint

A production-grade sample Kubernetes operator built with the [Operator SDK](https://sdk.operatorframework.io/), written in Go.

This operator manages a `WebApp` custom resource, automatically provisioning and reconciling a `Deployment` and `Service` for a given container workload. It is intentionally built to production standards — including finalizers, leader election, structured status conditions, Prometheus metrics, and least-privilege RBAC — to serve as a reference for building real-world operators at scale.

---

## Prerequisites

Before getting started, ensure the following tools are installed and configured:

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.23+ | https://go.dev/dl/ |
| operator-sdk CLI | v1.42+ | https://sdk.operatorframework.io/docs/installation/ |
| kubectl | latest | https://kubernetes.io/docs/tasks/tools/ |
| docker / podman | latest | https://docs.docker.com/get-docker/ |
| kind or minikube | latest | https://kind.sigs.k8s.io/ or https://minikube.sigs.k8s.io/ |

### Installing operator-sdk on macOS

```bash
# Option 1 — Homebrew
brew install operator-sdk

# Option 2 — Direct binary (recommended for Apple Silicon / darwin/arm64)
export ARCH=$(case $(uname -m) in x86_64) echo -n amd64 ;; aarch64) echo -n arm64 ;; *) echo -n $(uname -m) ;; esac)
export OS=$(uname | awk '{print tolower($0)}')
export OPERATOR_SDK_DL_URL=https://github.com/operator-framework/operator-sdk/releases/download/v1.42.0
curl -LO ${OPERATOR_SDK_DL_URL}/operator-sdk_${OS}_${ARCH}
chmod +x operator-sdk_${OS}_${ARCH}
sudo mv operator-sdk_${OS}_${ARCH} /usr/local/bin/operator-sdk
```

Verify installs:
```bash
go version
operator-sdk version
kubectl version --client
docker version
kind version
```

---

## Step 1 — Start a Local Cluster

Using `kind` (recommended for local dev):
```bash
kind create cluster --name operator-test
kubectl cluster-info --context kind-operator-test
```

---

## Step 2 — Initialize the Operator Project

From inside `~/go/src/github.com/54b3r/platform-operator-blueprint`:

```bash
operator-sdk init \
  --domain 54b3r.io \
  --repo github.com/54b3r/platform-operator-blueprint \
  --plugins=go/v4
```

This generates:
- `main.go` — entrypoint, sets up the manager
- `go.mod` / `go.sum` — module dependencies
- `config/` — Kustomize base manifests (RBAC, manager deployment)
- `Makefile` — all common build/deploy targets
- `Dockerfile` — multi-stage build for the operator image

---

## Step 3 — Scaffold an API and Controller

```bash
operator-sdk create api \
  --group app \
  --version v1alpha1 \
  --kind WebApp \
  --resource \
  --controller \
  --plugins=go/v4
```

This generates:
- `api/v1alpha1/webapp_types.go` — your CRD struct (`Spec` + `Status`)
- `internal/controller/webapp_controller.go` — your `Reconcile()` function
- `config/crd/` — CRD manifest (auto-generated from types)
- `config/rbac/` — RBAC role for the controller

---

## Step 4 — Define Your API Types

Edit `api/v1alpha1/webapp_types.go` to define what your custom resource looks like:

```go
type WebAppSpec struct {
    // Container image to run
    Image    string `json:"image"`
    // Number of replicas for the managed Deployment
    Replicas int32  `json:"replicas,omitempty"`
    // Port the container listens on
    Port     int32  `json:"port,omitempty"`
}

type WebAppStatus struct {
    // Structured conditions following Kubernetes API conventions
    Conditions        []metav1.Condition `json:"conditions,omitempty"`
    // Number of available replicas reported from the managed Deployment
    AvailableReplicas int32              `json:"availableReplicas,omitempty"`
}
```

Key design decisions:
- **`Conditions`** follow the `metav1.Condition` pattern used across Kubernetes itself — allows GitOps tooling and automation to programmatically read operator state
- **`AvailableReplicas`** surfaces real runtime state back to the CR so `kubectl get webapp` gives meaningful output
- **`Port`** is included so the operator can configure the Service without hardcoding

After editing types, regenerate deepcopy methods and CRD manifests:

```bash
make generate
make manifests
```

---

## Step 5 — Implement the Reconcile Loop

Edit `internal/controller/webapp_controller.go`. The full production reconcile pattern:

```go
func (r *WebAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    // 1. Fetch the custom resource
    app := &appv1alpha1.WebApp{}
    if err := r.Get(ctx, req.NamespacedName, app); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 2. Handle deletion via finalizer
    if !app.DeletionTimestamp.IsZero() {
        return r.handleDeletion(ctx, app)
    }

    // 3. Register finalizer if not present
    if !controllerutil.ContainsFinalizer(app, webappFinalizer) {
        controllerutil.AddFinalizer(app, webappFinalizer)
        return ctrl.Result{}, r.Update(ctx, app)
    }

    // 4. Reconcile child Deployment
    if err := r.reconcileDeployment(ctx, app); err != nil {
        return ctrl.Result{}, err
    }

    // 5. Reconcile child Service
    if err := r.reconcileService(ctx, app); err != nil {
        return ctrl.Result{}, err
    }

    // 6. Update status conditions
    if err := r.updateStatus(ctx, app); err != nil {
        return ctrl.Result{}, err
    }

    log.Info("Reconciled WebApp", "name", req.Name)
    return ctrl.Result{RequeueAfter: time.Minute}, nil
}
```

Key principles:
- **Idempotent** — safe to call repeatedly with no side effects
- **Finalizer checked before any mutation** — ensures cleanup runs before the object is removed from etcd
- Use `ctrl.Result{RequeueAfter: time.Minute}` to schedule periodic drift detection
- Use `controllerutil.SetControllerReference()` to set owner refs on child resources
- Always update status via `r.Status().Update()`, not `r.Update()`

### Production Components in the Reconcile Loop

#### Finalizers
Prevent the CR from being deleted until the operator has completed cleanup. Critical when your operator manages external state (cloud resources, DNS, databases) that Kubernetes cannot garbage collect on its own.

#### Leader Election
Enabled in `main.go` via `--leader-elect` flag. Ensures only one controller replica is active at a time when running multiple pods for HA. Uses a Kubernetes `Lease` object under the hood.

```go
mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
    LeaderElection:          true,
    LeaderElectionID:        "54b3r.io",
    LeaderElectionNamespace: "platform-operator-blueprint-system",
})
```

#### Structured Status Conditions
Follow the `metav1.Condition` pattern — the same convention used by Deployments, Nodes, and every core Kubernetes resource:

```go
meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
    Type:               "Available",
    Status:             metav1.ConditionTrue,
    Reason:             "DeploymentAvailable",
    Message:            "WebApp deployment is available",
    ObservedGeneration: app.Generation,
})
```

#### Prometheus Metrics
`controller-runtime` exposes reconcile metrics automatically at `:8080/metrics`. Custom metrics can be registered for domain-specific observability (e.g. number of managed WebApps, reconcile error rate).

---

## Step 6 — Add RBAC Markers (Least Privilege)

Above the `Reconcile` function, declare the minimum permissions required — no wildcards:

```go
//+kubebuilder:rbac:groups=app.54b3r.io,resources=webapps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=app.54b3r.io,resources=webapps/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=app.54b3r.io,resources=webapps/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
```

Why least privilege matters:
- In multi-tenant clusters, over-permissioned operators are a lateral movement risk
- Explicit verb lists make security reviews straightforward
- The `finalizers` subresource requires its own explicit permission — a common oversight

Regenerate RBAC manifests after any marker changes:
```bash
make manifests
```

---

## Step 7 — Run Locally (No Container Needed)

Install the CRD into your cluster, then run the controller locally:

```bash
make install    # applies CRD to the cluster
make run        # runs controller on your machine, connected to the cluster
```

In another terminal, apply a sample custom resource:
```bash
kubectl apply -f config/samples/app_v1alpha1_webapp.yaml
kubectl get webapps
kubectl describe webapp webapp-sample
```

The scaffolded sample will be at `config/samples/app_v1alpha1_webapp.yaml`. Edit it to match the `WebApp` spec:

```yaml
apiVersion: app.54b3r.io/v1alpha1
kind: WebApp
metadata:
  name: webapp-sample
  namespace: default
spec:
  image: nginx:latest
  replicas: 2
  port: 80
```

After applying, check status conditions:
```bash
kubectl get webapp webapp-sample -o jsonpath='{.status.conditions}' | jq .
```

---

## Step 8 — Build and Deploy as a Container

**Option A — Using `kind` (no registry needed):**
```bash
# Build the image locally
make docker-build IMG=platform-operator-blueprint:v0.0.1

# Load it directly into the kind cluster
kind load docker-image platform-operator-blueprint:v0.0.1 --name operator-test

# Deploy to the cluster
make deploy IMG=platform-operator-blueprint:v0.0.1
```

**Option B — Using a remote registry (Docker Hub, ECR, GCR, etc.):**
```bash
# Build and push the operator image
make docker-build docker-push IMG=docker.io/<your-org>/platform-operator-blueprint:v0.0.1

# Deploy to the cluster
make deploy IMG=docker.io/<your-org>/platform-operator-blueprint:v0.0.1
```

Check the operator pod:
```bash
kubectl get pods -n platform-operator-blueprint-system
kubectl logs -n platform-operator-blueprint-system deploy/platform-operator-blueprint-controller-manager
```

---

## Step 9 — Undeploy and Cleanup

```bash
make undeploy           # removes operator from cluster
make uninstall          # removes CRD from cluster
kind delete cluster --name operator-test
```

---

## Project Structure (After Scaffolding)

```
platform-operator-blueprint/
├── api/
│   └── v1alpha1/
│       ├── webapp_types.go          # CRD type definitions (Spec, Status, Conditions)
│       ├── groupversion_info.go
│       └── zz_generated.deepcopy.go # auto-generated, do not edit
├── internal/
│   └── controller/
│       ├── webapp_controller.go     # Reconcile logic, finalizer, status updates
│       └── suite_test.go
├── config/
│   ├── crd/                         # Generated CRD manifests
│   ├── rbac/                        # Generated least-privilege RBAC manifests
│   ├── manager/                     # Operator deployment manifest (leader election enabled)
│   └── samples/
│       └── app_v1alpha1_webapp.yaml # Example WebApp CR
├── main.go                          # Manager setup, leader election, metrics
├── Dockerfile
├── Makefile
├── go.mod
└── go.sum
```

---

## Useful Make Targets

| Target | Description |
|--------|-------------|
| `make generate` | Regenerate deepcopy methods |
| `make manifests` | Regenerate CRD + RBAC YAML from markers |
| `make install` | Apply CRDs to cluster |
| `make run` | Run controller locally |
| `make test` | Run unit + integration tests via envtest |
| `make docker-build` | Build operator container image |
| `make deploy` | Deploy operator to cluster |
| `make undeploy` | Remove operator from cluster |

---

## References

- [Operator SDK Docs](https://sdk.operatorframework.io/docs/)
- [Kubebuilder Book](https://book.kubebuilder.io/) *(same underlying framework)*
- [controller-runtime](https://pkg.go.dev/sigs.k8s.io/controller-runtime)
- [OperatorHub.io](https://operatorhub.io/)
