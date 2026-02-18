# Local Development & Testing Guide

This document captures the full local development and testing cycle for the
WebApp operator. It is intended as a living reference — both for day-to-day
development and as a teaching resource for anyone onboarding to operator development.

> **Note:** Custom `make` targets that wrap these steps are planned but not yet
> implemented. Until then, use the commands below directly.

---

## Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| `kind` | Local Kubernetes cluster | https://kind.sigs.k8s.io/ |
| `kubectl` | Cluster interaction | https://kubernetes.io/docs/tasks/tools/ |
| `go` | Build and run the operator | https://go.dev/dl/ |
| `make` | Operator lifecycle targets | pre-installed on macOS |

Verify all tools are present:

```bash
go version        # expect 1.23+
kind version      # expect v0.20+
kubectl version --client
make --version
```

---

## The Local Dev Cycle

The full cycle from zero to a running operator is:

```
create cluster → install CRDs → run operator → apply CR → observe → teardown
```

Each step is documented below.

---

## Step 1 — Create the kind Cluster

```bash
kind create cluster --name platform-operator
```

This creates a single-node cluster (control-plane only), which is sufficient for
operator development. The `kubectl` context is automatically set to
`kind-platform-operator`.

Verify the cluster is up:

```bash
kubectl cluster-info --context kind-platform-operator
kubectl get nodes
```

Expected output:
```
NAME                              STATUS   ROLES           AGE   VERSION
platform-operator-control-plane   Ready    control-plane   ...   v1.35.0
```

> **Why single-node?** Multi-node clusters are needed for testing pod scheduling,
> anti-affinity, and node failure scenarios. For reconciler logic, CRD validation,
> and status condition testing, a single node is sufficient and faster to spin up.

---

## Step 2 — Install the CRDs

```bash
make install
```

This runs `controller-gen` to generate the CRD manifests from your type definitions
in `api/v1alpha1/webapp_types.go`, then applies them to the cluster via `kubectl apply`.

Verify the CRD is installed:

```bash
kubectl get crds | grep 54b3r.io
```

Expected output:
```
webapps.app.54b3r.io   ...
```

You can also inspect the full CRD schema:

```bash
kubectl get crd webapps.app.54b3r.io -o yaml
```

> **When to re-run:** Any time you modify `WebAppSpec` or `WebAppStatus` in
> `api/v1alpha1/webapp_types.go`, re-run `make install` to update the CRD in the cluster.

---

## Step 3 — Run the Operator Locally

```bash
make run
```

This does the following in sequence:
1. Runs `controller-gen` to regenerate RBAC markers and CRD manifests
2. Runs `go fmt ./...` and `go vet ./...`
3. Starts the operator process with `go run ./cmd/main.go`

The operator runs **out-of-cluster**, using your local `~/.kube/config` to connect
to the kind cluster. This is the standard local dev pattern — no image build or push required.

Expected startup logs:
```
INFO    setup   starting manager
INFO    starting server {"name": "health probe", "addr": "[::]:8081"}
INFO    Starting EventSource    {"controller": "webapp", ...}
INFO    Starting Controller     {"controller": "webapp", ...}
INFO    Starting workers        {"controller": "webapp", ..., "worker count": 1}
```

Once you see `Starting workers`, the operator is running and watching for `WebApp` resources.

> **Leader election:** By default, leader election is enabled. When running locally
> with a single process this is fine. To disable it: `go run ./cmd/main.go --leader-elect=false`

---

## Step 4 — Apply a Sample CR

In a **separate terminal**, apply the sample `WebApp` resource:

```bash
kubectl apply -f config/samples/app_v1alpha1_webapp.yaml
```

Verify the resource was created:

```bash
kubectl get webapps.app.54b3r.io
kubectl get webapps.app.54b3r.io webapp-sample -o yaml
```

> **What to observe at scaffold stage:** The CR is created and stored in etcd.
> The operator receives the reconcile event. Since the reconciler is a stub,
> no child resources (Deployment, Service) are created yet and no logs are emitted.
> This is expected and correct.

> **What to observe after implementation:** The operator logs will show reconcile
> activity, and a `Deployment` and `Service` will appear in the same namespace as the CR.

---

## Step 5 — Observe Operator Logs

Watch the terminal where `make run` is running. After applying a CR you should see
reconcile log lines. The verbosity depends on what log statements are in the reconciler.

Useful patterns to look for:

```
INFO    Reconciling WebApp    {"controller": "webapp", "name": "webapp-sample", "namespace": "default"}
INFO    Creating Deployment   {"name": "webapp-sample", "namespace": "default"}
INFO    Updating status       {"name": "webapp-sample", "namespace": "default"}
```

To watch cluster resources in real time in another terminal:

```bash
# Watch all WebApp resources
kubectl get webapps -w

# Watch Deployments created by the operator
kubectl get deployments -w

# Watch events (useful for debugging)
kubectl get events --sort-by='.lastTimestamp' -w
```

---

## Step 6 — Modify and Re-test

The inner dev loop is:

1. Edit `api/v1alpha1/webapp_types.go` or `internal/controller/webapp_controller.go`
2. Stop `make run` (`Ctrl+C`)
3. If you changed types: `make install` to update the CRD
4. `make run` to restart the operator
5. Re-apply the CR or let the operator re-reconcile existing resources

> **Tip:** The operator will re-reconcile all existing `WebApp` resources on startup
> due to the informer cache sync. You don't need to delete and re-create CRs to
> trigger reconciliation after a restart.

---

## Step 7 — Teardown

Delete the sample CR:

```bash
kubectl delete -f config/samples/app_v1alpha1_webapp.yaml
```

Stop the operator (`Ctrl+C` in the `make run` terminal).

Delete the kind cluster:

```bash
kind delete cluster --name platform-operator
```

Uninstall the CRDs (optional, since the cluster is gone):

```bash
make uninstall
```

---

## Troubleshooting

**`make install` fails with connection refused**
- The kind cluster is not running. Run `kind create cluster --name platform-operator` first.
- Check context: `kubectl config current-context` should show `kind-platform-operator`.

**Operator starts but no reconcile logs after applying CR**
- At scaffold stage this is expected — the reconciler body is empty.
- After implementation, check that log statements use `logf.FromContext(ctx)`.

**`no space left on device` during `go mod tidy`**
- The Go module cache download is hitting disk limits.
- Check: `df -k / | awk 'NR==2 {print $4/1024/1024 " GB available"}'`
- Free space via Docker Desktop reset or clearing `~/Library/Caches`.

**`GO111MODULE=off` error**
- Fix with: `go env -w GO111MODULE=on`
- This persists across sessions.

**Port conflict on `:8081` (health probe)**
- Another process is using the port. Find it: `lsof -i :8081`
- Or run with a different port: `go run ./cmd/main.go --health-probe-bind-address=:8082`

---

## Planned: Custom Make Targets

The following targets are planned to wrap the above steps for a faster dev loop:

| Target | Description |
|--------|-------------|
| `make dev-up` | Create kind cluster + install CRDs |
| `make dev-run` | Run operator locally (out-of-cluster) |
| `make dev-apply` | Apply sample CR |
| `make dev-logs` | Tail operator logs |
| `make dev-down` | Delete CR + kind cluster |

These will be added in a future iteration alongside the 3 Musketeers Makefile targets.
