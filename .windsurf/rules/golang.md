---
description: Go coding standards and best practices for the platform-operator-blueprint project
---

# Go Development Rules

Adapted from [github/awesome-copilot go.instructions.md](https://github.com/github/awesome-copilot/blob/main/instructions/go.instructions.md),
extended with Kubernetes operator and controller-runtime specific conventions.

---

## General

- Write simple, clear, and idiomatic Go code. Favor clarity over cleverness.
- Follow the principle of least surprise.
- Keep the happy path left-aligned — return early to reduce nesting.
- Prefer `if condition { return }` over if-else chains.
- Make the zero value useful.
- Write self-documenting code with clear, descriptive names.
- Document ALL exported types, functions, methods, constants, and packages — no exceptions.
- Use Go modules for dependency management (`go.mod` / `go.sum`).
- Prefer standard library solutions over custom implementations when the functionality exists.
- Never use emoji in code, comments, or documentation.

---

## Naming Conventions

### Packages
- Lowercase, single-word names. No underscores, hyphens, or mixedCaps.
- Name packages by what they provide, not what they contain.
- Avoid generic names: `util`, `common`, `base`, `helpers`.
- Package names are singular, not plural.
- **CRITICAL**: Each `.go` file has exactly ONE `package` declaration at the top.
  Never duplicate it. When editing an existing file, preserve the existing declaration.

### Variables and Functions
- Use `mixedCaps` (unexported) or `MixedCaps` (exported) — never underscores.
- Keep names short but descriptive. Single-letter variables only for tight loop scopes.
- Exported names start with a capital letter; unexported with lowercase.
- Avoid stuttering: prefer `http.Server` over `http.HTTPServer`.

### Interfaces
- Use the `-er` suffix where possible (`Reconciler`, `Reader`, `Watcher`).
- Single-method interfaces are named after the method.
- Keep interfaces small and focused (1–3 methods is ideal).
- Define interfaces close to where they are used, not where they are implemented.

### Constants
- Exported: `MixedCaps`. Unexported: `mixedCaps`.
- Group related constants in `const` blocks.
- Use typed constants for better type safety.

---

## Code Style and Formatting

- Always run `gofmt` before committing. CI must enforce this.
- Use `goimports` to manage import grouping automatically.
- Import groups (in order, separated by blank lines):
  1. Standard library
  2. Third-party packages
  3. Internal / project packages
- Add blank lines to separate logical groups within a function.
- Keep functions focused — if a function needs a comment to explain what each section does, it should probably be split.

---

## Comments and Documentation

- **Every exported symbol must have a doc comment.** No exceptions.
- Doc comments start with the name of the symbol: `// WebAppReconciler reconciles a WebApp resource.`
- Package comments start with `// Package <name>`.
- Use `//` line comments for all inline and block documentation.
- Document **why**, not **what** — unless the what is genuinely complex.
- Comments are complete English sentences. No trailing punctuation on error messages.
- Inline comments explain non-obvious logic, business rules, or operator-specific behavior.
- Every struct field that is exported must have a comment explaining its purpose.

### Operator-Specific Comment Rules
- Every `Reconcile` function must have a comment block above it explaining:
  - What resource it reconciles
  - What child resources it manages
  - What the finalizer does (if present)
- Every RBAC marker (`//+kubebuilder:rbac:...`) must have an inline comment on the line above
  explaining why that permission is needed.
- Every `ctrl.Result` return must have a comment explaining the requeue rationale.

---

## Error Handling

- Check errors immediately after every function call.
- Never ignore errors with `_` unless you explicitly document why.
- Wrap errors with context: `fmt.Errorf("reconciling deployment: %w", err)`.
- Use `errors.New` for static errors, `fmt.Errorf` for dynamic ones.
- Export sentinel errors for cases callers need to check: `var ErrNotFound = errors.New(...)`.
- Use `errors.Is` and `errors.As` for error inspection — never string matching.
- Error messages: lowercase, no trailing punctuation.
- Place error return as the last return value.
- Name error variables `err` (or `err<Thing>` when multiple errors are in scope).
- **Never log and return an error** — choose one. In operators, return the error and let
  `controller-runtime` handle requeue logging.

---

## Structs and Types

- Define types to add meaning and type safety — avoid stringly-typed APIs.
- Use struct tags for all JSON, YAML, and Kubernetes API fields.
- Every struct must have a doc comment explaining its purpose and role.
- Every exported struct field must have a doc comment.
- Use pointer receivers when the method modifies the receiver or the struct is large.
- Use value receivers for small, immutable structs.
- Be consistent within a type's method set — don't mix pointer and value receivers.
- Prefer `any` over `interface{}` (Go 1.18+). Avoid unconstrained types where possible.

### Kubernetes CRD Type Rules
- `Spec` fields represent **desired state** (intent). Never put runtime state in Spec.
- `Status` fields represent **observed state**. Always use `metav1.Condition` for conditions.
- All CRD types must have the following kubebuilder markers:
  - `//+kubebuilder:object:root=true` on the root type
  - `//+kubebuilder:subresource:status` to enable status subresource
  - `//+kubebuilder:printcolumn` markers for any field useful in `kubectl get` output
- Use `omitempty` on optional fields. Document default values in the field comment.

---

## Architecture and Project Structure

- Controller logic lives in `internal/controller/`.
- API types live in `api/<version>/`.
- Shared helpers that are internal-only go in `internal/`.
- `main.go` is the entrypoint only — no business logic.
- Avoid circular dependencies.
- Use `go mod tidy` regularly to clean up unused dependencies.
- Keep dependencies minimal — every new dependency is a maintenance and security surface.

---

## Concurrency

- Never start a goroutine without knowing how it will stop.
- Use `context.Context` for cancellation — pass it as the first argument to every function
  that does I/O or long-running work.
- Prefer channels for communication, mutexes for state protection.
- Always use `defer` for cleanup (closing files, releasing locks, removing finalizers).
- Never modify maps concurrently without synchronization.

---

## Testing

- Test files use `_test.go` suffix, placed next to the code they test.
- Use table-driven tests for multiple scenarios.
- Name tests: `Test_FunctionName_Scenario` (e.g., `Test_Reconcile_WhenDeploymentMissing`).
- Use `t.Run` subtests for organization.
- Test both success and error paths.
- Mark helper functions with `t.Helper()`.
- Use `t.Cleanup()` for resource teardown.
- For operator tests, use `envtest` (controller-runtime's test environment) — not mocks of
  the Kubernetes API server.
- Test the reconcile loop end-to-end with `envtest`: create a CR, assert child resources
  are created, assert status conditions are set.

---

## Common Pitfalls — Never Do These

- Ignoring errors (even in tests).
- Goroutine leaks — always ensure goroutines can exit.
- Not using `defer` for cleanup.
- Modifying maps concurrently.
- Using global variables unnecessarily — use dependency injection via struct fields.
- Forgetting to close resources (files, HTTP response bodies, DB connections).
- Not understanding nil interface vs nil pointer (a nil `*MyType` stored in an interface is not nil).
- Duplicate `package` declarations — compile error, always check existing files first.
- In operators: calling `r.Update()` to update status — always use `r.Status().Update()`.
- In operators: mutating a resource and updating status in the same call — do them separately.
- In operators: not using `client.IgnoreNotFound(err)` when fetching the primary resource.
- In operators: adding a finalizer without implementing the cleanup logic for it.
