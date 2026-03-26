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
| Config file content | Only thresholds, alerting settings, GitLab base URL, and app→GitLab project ID mapping | OCP side is fully auto-discovered |
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

apps:
  - name: pfm
    gitlabProjectID: 123
  - name: crm
    gitlabProjectID: 456
```

## OCP checker discovery logic

1. List all SAs in `platform-cicd` → extract app names by stripping `-image-builder` / `-deployer` suffix
2. List all Secrets in `platform-cicd` → match by `kubernetes.io/service-account.name` annotation, extract `creationTimestamp` for token age
3. List all RoleBindings cluster-wide → filter by subject kind=ServiceAccount, subject namespace=`platform-cicd`, strip suffix to get app name, group by namespace

## App merge logic (evaluator)

Three possible states per app:
- **OCP + GitLab**: Full checks on both sides
- **OCP-only**: App discovered in cluster but no GitLab project ID in config — OCP checks run, GitLab skipped
- **GitLab-only**: App in config but no SAs found in cluster — flagged as warning

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

## Build plan and progress

| Step | Package | Status |
|---|---|---|
| 1 | `go.mod` + project skeleton | ✅ Done |
| 2 | `internal/config` | ✅ Done — 12 tests, 88.1% coverage |
| 3 | `internal/checker/ocp` | ✅ Done — 8 tests, awaiting user confirmation |
| 4 | `internal/checker/gitlab` | ⬜ Next |
| 5 | `internal/evaluator` | ⬜ |
| 6 | `internal/reporter` | ⬜ |
| 7 | `internal/alerter` | ⬜ |
| 8 | `cmd/monitor/main.go` | ⬜ |
| 9 | `web/` (dashboard HTML/JS) | ⬜ |
| 10 | `deploy/` (k8s manifests) | ⬜ |
| 11 | `Dockerfile` | ⬜ |

## Step 4 — GitLab checker design (ready to build)

Queries self-hosted GitLab REST API for each app in the config:
- `GET /api/v4/projects/:id/pipelines?ref=<default_branch>&per_page=1` → last pipeline status + timestamp
- `GET /api/v4/projects/:id/jobs?per_page=100` → filter by `status=failed` within `pipelineFailureWindow`
- `GET /api/v4/runners?type=project_type` → runner availability, `contacted_at` freshness

Output: per-app `GitLabAppStatus` with last pipeline info, failed job count by stage, runner status.

Same pattern as OCP checker: `HTTPClient` interface, mock HTTP server in tests.

## Deployment topology

All resources in `platform-cicd` namespace:
- **ConfigMap `ci-monitor-config`** — app registry + thresholds + `enableEmail: false`
- **Secret `ci-monitor-secrets`** — GitLab PAT + SMTP credentials
- **PVC `ci-monitor-data`** (1Gi) — `results.json` (overwritten) + `history.json` (append)
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
```

## Dependencies

- `gopkg.in/yaml.v3 v3.0.1` — YAML config parsing (only external dep)
- Everything else is Go stdlib (`net/http`, `encoding/json`, `time`, `os`, etc.)