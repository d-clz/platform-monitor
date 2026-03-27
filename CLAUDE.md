# Platform CI Monitor

## Working principles

- **Plan first.** Lay out what you're going to build before writing code. Let the user review before proceeding.
- **Do NOT guess.** If something conflicts or is ambiguous, stop and ask.
- **Tests for everything.** Every package gets Go tests. The user runs `go test` locally and reports results.
- **Step by step.** Complete one step, get confirmation, then move to the next.

## Project overview

A monitoring system for a Platform CI pipeline running on OpenShift (OCP). It checks health across two independent surfaces — OCP cluster resources and a self-hosted GitLab instance — and presents results on a dashboard with optional email alerts.

**Runtime:** CronJob in `platform-cicd` namespace, runs every 15 minutes.
**Language:** Go, stdlib only (except `gopkg.in/yaml.v3` for config parsing). No `client-go` — raw HTTP to kube API.
**Dashboard:** Self-contained static web app (nginx or Go file server) reading results JSON from a shared PVC.

## Architecture decisions (locked in)

| Decision | Choice | Rationale |
|---|---|---|
| OCP resource discovery | Auto-discover SAs, tokens, rolebindings on every run | No need to manually list apps or target namespaces |
| Config file content | Only thresholds, alerting settings, GitLab base URL, and root group ID | Both OCP and GitLab are fully auto-discovered |
| Config format | YAML via `gopkg.in/yaml.v3` | User preference |
| Kube API access | Raw `net/http` + `encoding/json` | Avoids massive `client-go` dependency tree; we only do read-only list operations |
| GitLab API access | Raw `net/http` + `encoding/json` | Same approach, self-hosted GitLab REST API |
| OCP ↔ GitLab correlation | Independent, no correlation for v1 | No native join key between the two; simplest approach |
| Email alerting | Wired in but gated behind `ENABLE_EMAIL_ALERTS` flag (default `false`) | Not enabled yet |
| Dashboard | Simple static web app (self-contained), not Grafana | Self-contained, no external dependencies |
| Testability | All external I/O behind interfaces (`HTTPClient`), injectable `Now` function | Mock HTTP servers in tests |

## Cluster context

- Single OCP cluster
- SA home namespace: `platform-cicd` (fixed)
- Namespace pattern: `<application>-<environment>` (e.g. `pfm-sit`, `pfm-uat`)
- Two SAs per app: `<app>-image-builder`, `<app>-deployer`
- Two token secrets per app: `<app>-image-builder-token`, `<app>-deployer-token`
- Token secrets have annotation `kubernetes.io/service-account.name` pointing to the SA
- Two ClusterRoles: `ci-image-builder`, `ci-deployer`
- RoleBindings in each target namespace reference SAs from `platform-cicd`
- GitLab CI variables `REGISTRY_PASS` and `OCP_<APP>_KUBECONFIG` are sourced from these tokens

## Config file shape

```yaml
thresholds:
  tokenAgeWarningDays: 60
  tokenAgeCriticalDays: 90
  runnerStalenessMinutes: 10
  pipelineFailureWindow: "24h"

alerting:
  enableEmail: false
  smtpHost: ""
  smtpPort: 587
  senderAddress: ""
  recipientAddresses: []

gitlabBaseURL: "https://gitlab.example.com"
gitlabGroupID: 5   # numeric ID of the root GitLab group; sub-groups become apps, projects become repos
```

**No `apps` list.** Apps and repos are fully auto-discovered at runtime:
- Root group → sub-groups (one per app) → projects (repos per app)
- Cache persisted to `gitlab-projects-cache.json` on the PVC; append-only (repos are never removed)

## OCP checker discovery logic

1. List all SAs in `platform-cicd` → extract app names by stripping `-image-builder` / `-deployer` suffix
2. List all Secrets in `platform-cicd` → match by `kubernetes.io/service-account.name` annotation, extract `creationTimestamp` for token age
3. List all RoleBindings cluster-wide → filter by subject kind=ServiceAccount, subject namespace=`platform-cicd`, strip suffix to get app name, group by namespace

## GitLab discovery model

Two-level discovery on every run:
1. `GET /api/v4/groups/:rootID/subgroups` (paginated) → one sub-group per app
2. `GET /api/v4/groups/:subgroupID/projects` (paginated) → one or more repos per app

Results merged with the on-disk cache (`gitlab-projects-cache.json`) using append-only logic — repos already in cache are never removed. `changed=true` triggers a cache write.

Per-repo check: last pipeline + jobs via `GET /api/v4/projects/:id/pipelines` and `/jobs`. App-level health = worst status across all repos.

## App merge logic (evaluator)

Three possible states per app:
- **OCP + GitLab**: Full checks on both sides
- **OCP-only**: App in cluster but not found in GitLab discovery — OCP checks run, GitLab skipped
- **GitLab-only**: App in GitLab discovery but no SAs found in cluster — flagged as warning

## Dashboard output

Per-app row showing: SA status (2/2 present), token health (age + exists), rolebinding namespaces, last pipeline status, failure count (24h), runner status. Top bar with aggregate counts. Alert history panel at bottom.

## Project structure

```
platform-ci-monitor/
├── cmd/
│   ├── monitor/main.go          # CronJob entrypoint
│   └── dashboard/main.go        # Static file server
├── internal/
│   ├── config/config.go         # YAML config loader
│   ├── checker/
│   │   ├── ocp.go               # OCP auto-discovery checker
│   │   └── gitlab.go            # GitLab REST API checker
│   ├── evaluator/evaluator.go   # Threshold logic, app merge
│   ├── reporter/reporter.go     # Write results.json + history.json
│   └── alerter/alerter.go       # Email sender (flag-gated)
├── web/
│   ├── index.html               # Dashboard SPA
│   └── app.js                   # Fetch + render results
├── deploy/
│   ├── configmap.yaml
│   ├── secret.yaml
│   ├── pvc.yaml
│   ├── cronjob.yaml
│   ├── dashboard-deployment.yaml
│   ├── service.yaml
│   ├── route.yaml
│   └── rbac.yaml
├── go.mod
├── Dockerfile
└── CLAUDE.md
```

## Known gaps / improvement backlog

- **OCP errors are run-fatal**: `ocpChecker.Check()` returns a single error that aborts OCP data for all apps. There is no per-app OCP error field — a single unreachable namespace kills the whole OCP surface. Future improvement: make `OCPAppStatus` carry an `Error string` field so partial failures are surfaced per-app at `LevelError` instead of silently dropping all OCP data.

## Build plan and progress

| Step | Package | Status |
|---|---|---|
| 1 | `go.mod` + project skeleton | ✅ Done |
| 2 | `internal/config` | ✅ Done |
| 3 | `internal/checker/ocp` | ✅ Done |
| 4 | `internal/checker/gitlab` | ✅ Done — multi-repo discovery + cache |
| 5 | `internal/evaluator` | ✅ Done |
| 6 | `internal/reporter` | ✅ Done — metrics, job-metrics, rotation |
| 7 | `internal/alerter` | ✅ Done |
| 8 | `cmd/monitor/main.go` | ✅ Done |
| 9 | `web/` (dashboard HTML/JS) | ✅ Done — multi-repo expand UI |
| 10 | `deploy/` (k8s manifests) | ✅ Done |
| 11 | `Dockerfile` | ✅ Done |

## Deployment topology

All resources in `platform-cicd` namespace:
- **ConfigMap `ci-monitor-config`** — thresholds + `gitlabGroupID` + `enableEmail: false`
- **Secret `ci-monitor-secrets`** — GitLab PAT + SMTP credentials
- **PVC `ci-monitor-data`** (1Gi) — `results.json`, `metrics.json`, `job-metrics.json`, `incidents.json`, `gitlab-projects-cache.json`
- **CronJob `ci-monitor`** — `*/15 * * * *`, mounts ConfigMap + Secret + PVC
- **ServiceAccount `ci-monitor-sa`** — read-only ClusterRole for SAs, Secrets (metadata), RoleBindings
- **Deployment `ci-dashboard`** — nginx serving static HTML, PVC mounted read-only
- **Service + Route** — exposes dashboard

## Commands

```bash
go mod tidy          # resolve dependencies
go test ./...        # run all tests
go test ./internal/config/ -v        # test config package
go test ./internal/checker/ -v       # test checker package
go vet ./...         # lint

# Run dashboard locally against testdata
WEB_DIR=./web DATA_DIR=./testdata go run ./cmd/dashboard
# then open http://localhost:8080
```

## Dependencies

- `gopkg.in/yaml.v3 v3.0.1` — YAML config parsing (only external dep)
- Everything else is Go stdlib (`net/http`, `encoding/json`, `time`, `os`, etc.)