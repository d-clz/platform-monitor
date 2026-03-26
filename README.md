# Platform CI Monitor

A monitoring system for the Platform CI pipeline running on OpenShift (OCP).
Checks health across OCP cluster resources and a self-hosted GitLab instance, then presents results on a live dashboard with incident tracking, long-term reporting, and optional email alerts.

---

## Quick start — view the dashboard locally

**Prerequisites:** Go 1.22+

```bash
# 1. Clone / enter the project
cd platform-monitor

# 2. Install dependencies
go mod download

# 3. Serve the dashboard against mock data
WEB_DIR=./web DATA_DIR=./testdata go run ./cmd/dashboard

# 4. Open in browser
open http://localhost:8080        # dashboard
open http://localhost:8080/report # long-term report
```

The mock data in `testdata/` covers every app state (ok, warning, critical, error, ocp-only, gitlab-only), incidents in various lifecycle stages, and 3 weeks of per-app and per-job metrics so you can explore all views immediately.

---

## Run tests

```bash
go test ./...                        # all packages
go test ./internal/checker/  -v      # checker only
go test ./internal/evaluator/ -v     # evaluator only
go test ./internal/reporter/  -v     # reporter only
go test ./internal/alerter/   -v     # alerter only
go vet ./...                         # lint
```

---

## Configuration

Edit `deploy/configmap.yaml` (or create a local `config.yaml`) before deploying.

```yaml
thresholds:
  tokenAgeWarningDays: 60      # warn when SA token is older than this
  tokenAgeCriticalDays: 90     # critical when older than this
  runnerStalenessMinutes: 10   # runner is stale if not seen within this window
  pipelineFailureWindow: "24h" # look-back window for failed job counts

alerting:
  enableEmail: false           # set true to enable SMTP alerts
  smtpHost: ""
  smtpPort: 587
  senderAddress: ""
  recipientAddresses: []

gitlabBaseURL: "https://gitlab.example.com"
gitlabGroupID: 42              # numeric ID of the GitLab group (or sub-group) to monitor
```

Projects are auto-discovered from the group on every cron run and cached in `gitlab-projects-cache.json`. New projects are detected automatically; removed projects are intentionally retained in the cache to preserve long-term report continuity.

---

## Environment variables

### Monitor (CronJob)

| Variable | Default | Description |
|---|---|---|
| `CONFIG_PATH` | `/etc/ci-monitor/config.yaml` | Path to config YAML |
| `KUBE_API_URL` | `https://kubernetes.default.svc` | Kube API base URL |
| `KUBE_TOKEN_PATH` | `/var/run/secrets/kubernetes.io/serviceaccount/token` | SA bearer token path |
| `GITLAB_TOKEN` | *(required)* | GitLab Personal Access Token (read_api scope) |
| `SMTP_USERNAME` | *(optional)* | SMTP auth username |
| `SMTP_PASSWORD` | *(optional)* | SMTP auth password |
| `DATA_DIR` | `/data` | Directory for output files |

### Dashboard

| Variable | Default | Description |
|---|---|---|
| `WEB_DIR` | `/web` | Directory containing `index.html`, `app.js`, `report.html`, `report.js` |
| `DATA_DIR` | `/data` | Directory containing data files (PVC mount) |
| `HOST` | `0.0.0.0` | Interface to bind to |
| `PORT` | `8080` | HTTP port to listen on |
| `LOG_FILE` | *(empty)* | Path to log file; logs go to stdout + file when set |

---

## Data files

All files are written to `DATA_DIR` (PVC in OCP, `./testdata` locally).

| File | Written by | Description |
|---|---|---|
| `results.json` | Monitor | Latest health snapshot — overwritten every run |
| `history.json` | Monitor | Compact alert log — appended when non-OK apps exist, capped at 200 entries |
| `incidents.json` | Monitor + Dashboard | Full incident lifecycle — opened/closed automatically, notes added via UI |
| `metrics.json` | Monitor | Hot metrics: per-app time-series for last 60 days, appended every run |
| `metrics-YYYY-MM.json` | Monitor | Cold metrics: monthly archives, rotated out of the hot file automatically |
| `metrics-index.json` | Monitor | List of cold metric archive files |
| `job-metrics.json` | Monitor | Weekly per-job aggregates: runs, failures, total duration — capped at 52 weeks |
| `job-metrics-state.json` | Monitor | Last-seen pipeline ID per app (prevents double-counting across cron polls) |
| `gitlab-projects-cache.json` | Monitor | Cached group project discovery: path → ID map, append-only |
| `dashboard.log` | Dashboard | Server log (when `LOG_FILE` is set) |

---

## Incident tracking

The dashboard tracks problems as **incidents** — one per app per degradation period.

### Lifecycle

```
CronJob run: app ok → non-ok     incident opens   (id: appname-YYYYMMDD-HHMM)
CronJob run: app still non-ok    incident updates  (peak level escalates if worse)
CronJob run: app non-ok → ok     incident closes   (duration calculated)
CronJob run: app non-ok again    new incident opens
```

### Incident card states

| State | Left border | Note form |
|---|---|---|
| Open — no notes | amber / red / purple | Hidden — stay focused on fixing |
| Open — has notes | amber / red / purple | **Add note** button (collapsed) |
| Resolved — no notes | green | **Add note** button (collapsed) |
| Resolved — has notes | green | **Add note** button (collapsed) |

### Adding and managing notes

Notes are added directly from the dashboard — no curl or API tools needed.

- **Add note** — click the button on the card, type, click Save
- **Edit note** — hover a note → click **Edit** → update inline → Save
- **Delete note** — hover a note → click **Delete** → confirm

Use notes to record root cause, fix steps, and lessons learned:

```
Root cause: deployer SA token expired after 94 days — never rotated.
Fix: rotated token, updated OCP_SVC_KUBECONFIG in GitLab CI variables.
Lesson: add a 50-day calendar reminder for all CI token rotations.
```

---

## Pipeline job details

On the dashboard, click any app name (▶) to expand an inline job breakdown showing every job in the latest pipeline — name, stage, status badge, and duration.

---

## Long-term report (`/report`)

The report page provides trend analysis over selectable time ranges (7d / 30d / 90d / 1y).

### App-level charts

Select an app to see daily trends for:
- **Pipeline error rate** — percentage of cron samples where the last pipeline was failed or canceled
- **Avg pipeline duration** — mean duration in seconds per day
- **Runners online** — mean non-stale runner count per day

### Job-level charts

Select an app, then select a job to see weekly trends for:
- **Job error rate** — percentage of pipeline runs where the job failed or was canceled
- **Job avg duration** — mean job duration in seconds per week
- **Pipeline runs** — distinct pipeline runs counted per week (each pipeline counted once, deduped by ID)

### Data architecture

| Layer | File | Retention |
|---|---|---|
| Hot (app metrics) | `metrics.json` | Rolling 60 days |
| Cold (app metrics) | `metrics-YYYY-MM.json` | Monthly archives, 1 year+ |
| Job metrics | `job-metrics.json` | Rolling 52 weeks (no cold rotation needed) |

The report page loads only the files needed for the selected range — cold archives are fetched lazily when the range exceeds the 60-day hot window.

---

## Build Docker image

```bash
docker build -t platform-monitor:latest .
```

Both binaries are baked into the same image:
- `/monitor` — CronJob entrypoint
- `/dashboard` — file server + notes API

---

## Deploy to OpenShift

```bash
# 1. Fill in your GitLab PAT
#    echo -n 'glpat-xxxx' | base64
#    then edit deploy/secret.yaml

# 2. Update deploy/configmap.yaml with your apps and GitLab base URL

# 3. Build and push the image to the OCP internal registry
docker build -t image-registry.openshift-image-registry.svc:5000/platform-cicd/platform-monitor:latest .
docker push     image-registry.openshift-image-registry.svc:5000/platform-cicd/platform-monitor:latest

# 4. Apply manifests in order
oc apply -f deploy/rbac.yaml
oc apply -f deploy/pvc.yaml
oc apply -f deploy/configmap.yaml
oc apply -f deploy/secret.yaml
oc apply -f deploy/cronjob.yaml
oc apply -f deploy/dashboard-deployment.yaml
oc apply -f deploy/service.yaml
oc apply -f deploy/route.yaml

# 5. Get the dashboard URL
oc get route ci-dashboard -n platform-cicd
```

The CronJob runs every 15 minutes. Trigger a manual run to verify:

```bash
oc create job ci-monitor-manual --from=cronjob/ci-monitor -n platform-cicd
oc logs -f job/ci-monitor-manual -n platform-cicd
```

---

## Project structure

```
platform-monitor/
├── cmd/
│   ├── monitor/main.go          # CronJob entrypoint
│   └── dashboard/main.go        # File server + notes API (POST/PUT/DELETE /notes) + /report route
├── internal/
│   ├── config/                  # YAML config loader
│   ├── checker/
│   │   ├── ocp.go               # OCP auto-discovery checker
│   │   └── gitlab.go            # GitLab REST API checker (pipelines, jobs, runners)
│   ├── evaluator/               # Threshold logic, app merge
│   ├── reporter/
│   │   ├── reporter.go          # Writes results.json + history.json, orchestrates all writers
│   │   ├── incident.go          # Incident lifecycle + note CRUD
│   │   ├── metrics.go           # App-level time-series metrics, hot/cold rotation
│   │   └── jobmetrics.go        # Per-job weekly aggregates, pipeline dedup
│   └── alerter/                 # Email sender (flag-gated)
├── web/
│   ├── index.html               # Dashboard SPA
│   ├── app.js                   # Fetch + render results, incidents, expandable job rows
│   ├── report.html              # Long-term report SPA
│   └── report.js                # Metrics/job loading, canvas charts, hot+cold fetch
├── deploy/                      # OpenShift manifests
├── testdata/                    # Mock data for local testing
│   ├── results.json
│   ├── incidents.json
│   ├── history.json
│   ├── metrics.json             # 7 days of app-level time-series
│   ├── metrics-index.json       # Empty — no cold archives in testdata
│   ├── job-metrics.json         # 3 weeks of per-job weekly aggregates
│   └── job-metrics-state.json   # Last-seen pipeline IDs
├── .github/workflows/ci.yml     # CI: test on push/PR, build+release on tags only
├── Dockerfile
└── go.mod
```

---

## How it works

```
CronJob (every 15 min)
  │
  ├── OCPChecker    → auto-discovers SAs, tokens, rolebindings in platform-cicd
  ├── GitLabChecker → queries pipelines, pipeline jobs, failed jobs, runners per app
  ├── Evaluator     → merges results, applies thresholds → AppResult[]
  ├── Reporter      → reconciles incidents (open/close transitions)
  │                   writes results.json (overwrite)
  │                   writes incidents.json (lifecycle, notes preserved)
  │                   appends history.json (capped at 200 entries)
  │                   appends metrics.json (hot, 60-day window; rotates to metrics-YYYY-MM.json)
  │                   upserts job-metrics.json (weekly aggregates, 52-week cap)
  └── Alerter       → sends email if EnableEmail=true and non-OK apps exist

Dashboard — /  (always on)
  ├── GET  /data/*      → serves all data files from PVC
  ├── POST /notes       → appends a new note to an incident
  ├── PUT  /notes       → updates an existing note by index
  ├── DELETE /notes     → removes a note by index
  └── GET  /            → serves static dashboard (index.html, app.js)
      └── browser polls every 60s, cache: no-store

Report — /report  (same server)
  └── GET  /report      → serves report.html + report.js
      └── browser polls every 5min; loads metrics.json + cold archives as needed
          + job-metrics.json for per-job weekly charts
```
