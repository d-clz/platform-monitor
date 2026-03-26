# Platform CI Monitor

A monitoring system for the Platform CI pipeline running on OpenShift (OCP).
Checks health across OCP cluster resources and a self-hosted GitLab instance, then presents results on a live dashboard with incident tracking and optional email alerts.

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
open http://localhost:8080
```

The mock data in `testdata/` covers every app state (ok, warning, critical, error, ocp-only, gitlab-only) and a set of incidents in various lifecycle stages so you can explore the full dashboard immediately.

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

apps:
  - name: pfm
    gitlabProjectID: 123
  - name: crm
    gitlabProjectID: 456
```

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
| `WEB_DIR` | `/web` | Directory containing `index.html` and `app.js` |
| `DATA_DIR` | `/data` | Directory containing data files (PVC mount) |
| `HOST` | `0.0.0.0` | Interface to bind to |
| `PORT` | `8080` | HTTP port to listen on |
| `LOG_FILE` | *(empty)* | Path to log file; logs go to stdout + file when set |

---

## Data files

All files are written to `DATA_DIR` (PVC in OCP, `./testdata` locally).

| File | Written by | Description |
|---|---|---|
| `results.json` | Monitor (CronJob) | Latest health snapshot — overwritten every run |
| `history.json` | Monitor (CronJob) | Compact alert log — appended when non-OK apps exist, capped at 200 entries |
| `incidents.json` | Monitor (CronJob) + Dashboard | Full incident lifecycle — opened/closed automatically, notes added via UI |
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
│   └── dashboard/main.go        # File server + notes API (POST/PUT/DELETE /notes)
├── internal/
│   ├── config/                  # YAML config loader
│   ├── checker/
│   │   ├── ocp.go               # OCP auto-discovery checker
│   │   └── gitlab.go            # GitLab REST API checker
│   ├── evaluator/               # Threshold logic, app merge
│   ├── reporter/
│   │   ├── reporter.go          # Writes results.json + history.json
│   │   └── incident.go          # Incident lifecycle + note CRUD
│   └── alerter/                 # Email sender (flag-gated)
├── web/
│   ├── index.html               # Dashboard SPA
│   └── app.js                   # Fetch + render results and incidents
├── deploy/                      # OpenShift manifests
├── testdata/                    # Mock data for local dashboard testing
│   ├── results.json
│   ├── incidents.json
│   └── history.json
├── .github/workflows/ci.yml     # GitHub Actions CI
├── Dockerfile
└── go.mod
```

---

## How it works

```
CronJob (every 15 min)
  │
  ├── OCPChecker    → auto-discovers SAs, tokens, rolebindings in platform-cicd
  ├── GitLabChecker → queries pipelines, failed jobs, runners per app
  ├── Evaluator     → merges results, applies thresholds → AppResult[]
  ├── Reporter      → reconciles incidents (open/close transitions)
  │                   writes results.json (overwrite)
  │                   writes incidents.json (lifecycle, notes preserved)
  │                   appends history.json (capped at 200 entries)
  └── Alerter       → sends email if EnableEmail=true and non-OK apps exist

Dashboard (always on)
  ├── GET  /data/*      → serves results.json, incidents.json from PVC
  ├── POST /notes       → appends a new note to an incident
  ├── PUT  /notes       → updates an existing note by index
  ├── DELETE /notes     → removes a note by index
  └── GET  /            → serves static dashboard assets
      └── browser polls every 60s, cache: no-store
```
