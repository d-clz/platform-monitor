'use strict';

const REFRESH_MS   = 300_000; // 5 min
const HOT_WINDOW   = 60;      // days kept in metrics.json

// ---- State ----
let allEntries    = [];
let allJobEntries = [];
let selectedDays  = 30;
let selectedApp   = '';
let selectedJob   = '';

// ---- Fetch ----

async function fetchJSON(url) {
  const res = await fetch(url, { cache: 'no-store' });
  if (!res.ok) throw new Error(`${url} returned ${res.status}`);
  return res.json();
}

async function loadEntries(rangeDays) {
  const entries = [];

  const hot = await fetchJSON('/data/metrics.json').catch(() => []);
  entries.push(...hot);

  if (rangeDays > HOT_WINDOW) {
    const index = await fetchJSON('/data/metrics-index.json').catch(() => []);
    const cutoff = new Date(Date.now() - rangeDays * 86400000);

    const needed = index.filter(fname => {
      const m = fname.match(/metrics-(\d{4})-(\d{2})\.json/);
      if (!m) return false;
      // include if the month ends on or after cutoff
      const monthEnd = new Date(+m[1], +m[2], 0);
      return monthEnd >= cutoff;
    });

    const results = await Promise.allSettled(
      needed.map(f => fetchJSON(`/data/${f}`))
    );
    for (const r of results) {
      if (r.status === 'fulfilled') entries.push(...r.value);
    }
  }

  return entries;
}

// ---- Aggregation ----

function getApps(entries) {
  return [...new Set(entries.map(e => e.app))].sort();
}

// Returns { [day: 'YYYY-MM-DD']: { total, failed, durations[], runners[] } }
function aggregateByDay(entries, appName, rangeDays) {
  const cutoff = Date.now() - rangeDays * 86400000;
  const filtered = entries.filter(e =>
    e.app === appName && new Date(e.ts).getTime() >= cutoff
  );

  const byDay = {};
  for (const e of filtered) {
    const day = e.ts.slice(0, 10);
    if (!byDay[day]) byDay[day] = { total: 0, failed: 0, durations: [], runners: [] };
    const d = byDay[day];
    d.total++;
    if (e.pipelineStatus === 'failed' || e.pipelineStatus === 'canceled') d.failed++;
    if (e.pipelineDuration > 0) d.durations.push(e.pipelineDuration);
    d.runners.push(e.runnersOnline);
  }
  return byDay;
}

function computeSummary(dayData) {
  let totalRuns = 0, totalFailed = 0;
  const allDurations = [], allRunners = [];
  for (const d of Object.values(dayData)) {
    totalRuns  += d.total;
    totalFailed += d.failed;
    allDurations.push(...d.durations);
    allRunners.push(...d.runners);
  }
  const errorRate   = totalRuns > 0 ? totalFailed / totalRuns * 100 : 0;
  const avgDuration = allDurations.length > 0 ? mean(allDurations) : 0;
  const avgRunners  = allRunners.length  > 0 ? mean(allRunners)  : 0;
  return { totalRuns, errorRate, avgDuration, avgRunners };
}

function mean(arr) { return arr.reduce((a, b) => a + b, 0) / arr.length; }

// ---- Formatting ----

function fmtDuration(secs) {
  if (!secs) return '—';
  const m = Math.floor(secs / 60), s = Math.floor(secs % 60);
  return m > 0 ? `${m}m ${s.toString().padStart(2, '0')}s` : `${s}s`;
}

function fmtPct(pct) { return pct.toFixed(1) + '%'; }

function errClass(pct) {
  if (pct > 30) return 'err-high';
  if (pct > 10) return 'err-mid';
  return 'err-low';
}

function errBarColor(pct) {
  if (pct > 30) return '#f87171';
  if (pct > 10) return '#fbbf24';
  return '#4ade80';
}

// ---- Canvas charts ----

function initCanvas(canvas) {
  const dpr = window.devicePixelRatio || 1;
  const w   = canvas.clientWidth  || canvas.parentElement.clientWidth;
  const h   = canvas.clientHeight || 180;
  canvas.width  = w * dpr;
  canvas.height = h * dpr;
  const ctx = canvas.getContext('2d');
  ctx.scale(dpr, dpr);
  return { ctx, w, h };
}

const PAD = { top: 14, right: 14, bottom: 34, left: 48 };

function drawGrid(ctx, w, h, yMax, fmtY) {
  const pw = w - PAD.left - PAD.right;
  const ph = h - PAD.top  - PAD.bottom;

  ctx.font = '10px ui-monospace, monospace';
  ctx.textBaseline = 'middle';
  ctx.textAlign = 'right';

  for (let i = 0; i <= 4; i++) {
    const y   = PAD.top + ph - (i / 4) * ph;
    const val = (i / 4) * yMax;

    ctx.strokeStyle = '#1e293b';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(PAD.left, y);
    ctx.lineTo(PAD.left + pw, y);
    ctx.stroke();

    ctx.fillStyle = '#475569';
    ctx.fillText(fmtY(val), PAD.left - 6, y);
  }
  return { pw, ph };
}

function drawXLabels(ctx, data, pw, h, step) {
  ctx.font = '10px ui-monospace, monospace';
  ctx.fillStyle = '#475569';
  ctx.textAlign = 'center';
  ctx.textBaseline = 'top';
  const n = data.length;

  data.forEach((d, i) => {
    if (i % step !== 0) return;
    const x = PAD.left + (n <= 1 ? pw / 2 : (i / (n - 1)) * pw);
    ctx.fillText(d.x.slice(5), x, h - PAD.bottom + 6); // "MM-DD"
  });
}

function noData(ctx, w, h) {
  ctx.fillStyle = '#334155';
  ctx.font = '11px ui-monospace, monospace';
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.fillText('No data', w / 2, h / 2);
}

// Bar chart — used for error rate
function drawBarChart(canvas, data) {
  const { ctx, w, h } = initCanvas(canvas);

  ctx.fillStyle = '#0f172a';
  ctx.fillRect(0, 0, w, h);

  if (!data.length) { noData(ctx, w, h); return; }

  const yMax = 100;
  const { pw, ph } = drawGrid(ctx, w, h, yMax, v => Math.round(v) + '%');

  const n   = data.length;
  const gap = Math.max(1, pw / n * 0.15);
  const bw  = pw / n - gap;

  data.forEach((d, i) => {
    const x  = PAD.left + (i / n) * pw + gap / 2;
    const bh = (d.y / yMax) * ph;
    const y  = PAD.top + ph - bh;
    ctx.fillStyle = errBarColor(d.y);
    ctx.fillRect(x, y, Math.max(1, bw), Math.max(1, bh));
  });

  drawXLabels(ctx, data, pw, h, Math.ceil(n / 10));
}

// Line chart — used for duration and runners
function drawLineChart(canvas, data, color) {
  const { ctx, w, h } = initCanvas(canvas);

  ctx.fillStyle = '#0f172a';
  ctx.fillRect(0, 0, w, h);

  const valid = data.filter(d => d.y > 0);
  if (!valid.length) { noData(ctx, w, h); return; }

  const yMax = Math.max(...data.map(d => d.y)) * 1.15;
  const isDuration = color === '#3b82f6';
  const fmtY = isDuration
    ? v => v >= 60 ? Math.round(v / 60) + 'm' : Math.round(v) + 's'
    : v => Math.round(v);

  const { pw, ph } = drawGrid(ctx, w, h, yMax, fmtY);
  const n = data.length;

  // Area fill under the line
  ctx.beginPath();
  let started = false;
  let lastX = 0, lastY = 0;
  data.forEach((d, i) => {
    if (!d.y) return;
    const x = PAD.left + (n <= 1 ? pw / 2 : (i / (n - 1)) * pw);
    const y = PAD.top + ph - (d.y / yMax) * ph;
    if (!started) { ctx.moveTo(x, PAD.top + ph); ctx.lineTo(x, y); started = true; }
    else ctx.lineTo(x, y);
    lastX = x; lastY = y;
  });
  if (started) {
    ctx.lineTo(lastX, PAD.top + ph);
    ctx.closePath();
    ctx.fillStyle = color + '18'; // ~10% opacity
    ctx.fill();
  }

  // Line
  ctx.strokeStyle = color;
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.beginPath();
  started = false;
  data.forEach((d, i) => {
    if (!d.y) return;
    const x = PAD.left + (n <= 1 ? pw / 2 : (i / (n - 1)) * pw);
    const y = PAD.top + ph - (d.y / yMax) * ph;
    started ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
    started = true;
  });
  ctx.stroke();

  // Dots
  ctx.fillStyle = color;
  data.forEach((d, i) => {
    if (!d.y) return;
    const x = PAD.left + (n <= 1 ? pw / 2 : (i / (n - 1)) * pw);
    const y = PAD.top + ph - (d.y / yMax) * ph;
    ctx.beginPath();
    ctx.arc(x, y, 3, 0, Math.PI * 2);
    ctx.fill();
  });

  drawXLabels(ctx, data, pw, h, Math.ceil(n / 10));
}

// ---- Rendering ----

function renderSummaryTable(apps) {
  const tbody = document.getElementById('summary-tbody');
  if (!apps.length) {
    tbody.innerHTML = '<tr><td colspan="5" class="empty">No metrics data yet — data appears after the first cron run.</td></tr>';
    return;
  }

  tbody.innerHTML = '';
  for (const app of apps) {
    const dayData = aggregateByDay(allEntries, app, selectedDays);
    const s = computeSummary(dayData);

    const tr = document.createElement('tr');
    if (app === selectedApp) tr.className = 'selected';

    tr.innerHTML = `
      <td>${app}</td>
      <td>${s.totalRuns}</td>
      <td><span class="err-rate ${errClass(s.errorRate)}">${fmtPct(s.errorRate)}</span></td>
      <td>${fmtDuration(s.avgDuration)}</td>
      <td>${s.avgRunners > 0 ? s.avgRunners.toFixed(1) : '—'}</td>
    `;
    tr.addEventListener('click', () => {
      selectedApp = app;
      renderSummaryTable(apps);
      renderCharts();
      document.getElementById('app-select').value = app;
    });
    tbody.appendChild(tr);
  }
}

function renderCharts() {
  const section = document.getElementById('charts-section');
  if (!selectedApp) { section.style.display = 'none'; return; }
  section.style.display = '';
  document.getElementById('charts-title').textContent = `Charts — ${selectedApp}`;

  const dayData = aggregateByDay(allEntries, selectedApp, selectedDays);
  const days    = Object.keys(dayData).sort();

  const errorData    = days.map(d => ({ x: d, y: dayData[d].total > 0 ? dayData[d].failed / dayData[d].total * 100 : 0 }));
  const durationData = days.map(d => ({ x: d, y: dayData[d].durations.length > 0 ? mean(dayData[d].durations) : 0 }));
  const runnerData   = days.map(d => ({ x: d, y: dayData[d].runners.length  > 0 ? mean(dayData[d].runners)  : 0 }));

  drawBarChart(document.getElementById('chart-error'),    errorData);
  drawLineChart(document.getElementById('chart-duration'), durationData, '#3b82f6');
  drawLineChart(document.getElementById('chart-runners'),  runnerData,   '#2dd4bf');
}

// ---- Job rendering ----

function getAppJobs(jobEntries, appName) {
  return [...new Set(jobEntries.filter(e => e.app === appName).map(e => e.job))].sort();
}

function renderJobSection() {
  const section = document.getElementById('job-section');
  if (!selectedApp) { section.style.display = 'none'; return; }

  const jobs = getAppJobs(allJobEntries, selectedApp);
  if (!jobs.length) { section.style.display = 'none'; return; }

  section.style.display = '';
  document.getElementById('job-app-name').textContent = selectedApp;

  const sel = document.getElementById('job-select');
  const prev = sel.value;
  sel.innerHTML = jobs.map(j => `<option value="${j}">${j}</option>`).join('');
  if (jobs.includes(prev)) { sel.value = prev; selectedJob = prev; }
  else { selectedJob = jobs[0]; sel.value = jobs[0]; }

  renderJobCharts();
}

function renderJobCharts() {
  if (!selectedApp || !selectedJob) return;

  const entries = allJobEntries
    .filter(e => e.app === selectedApp && e.job === selectedJob)
    .sort((a, b) => a.week.localeCompare(b.week));

  // Use short week label "W13" on x-axis
  const label = e => e.week.replace(/^\d{4}-/, '');

  const errorData    = entries.map(e => ({ x: label(e), y: e.runs > 0 ? e.failures / e.runs * 100 : 0 }));
  const durationData = entries.map(e => ({ x: label(e), y: e.runs > 0 ? e.totalDuration / e.runs : 0 }));
  const runsData     = entries.map(e => ({ x: label(e), y: e.runs }));

  const maxRuns = Math.max(...runsData.map(d => d.y), 1);

  drawBarChart(document.getElementById('chart-job-error'),    errorData);
  drawLineChart(document.getElementById('chart-job-duration'), durationData, '#a78bfa');
  drawLineChart(document.getElementById('chart-job-runs'),     runsData,     '#2dd4bf');
}

function render() {
  const apps = getApps(allEntries).filter(app => {
    const dayData = aggregateByDay(allEntries, app, selectedDays);
    return Object.keys(dayData).length > 0;
  });

  // Populate app select
  const sel = document.getElementById('app-select');
  const prev = sel.value;
  sel.innerHTML = apps.map(a => `<option value="${a}">${a}</option>`).join('');
  if (apps.includes(prev)) sel.value = prev;
  else if (apps.length) sel.value = apps[0];

  if (!selectedApp || !apps.includes(selectedApp)) {
    selectedApp = apps[0] || '';
  }
  sel.value = selectedApp;

  renderSummaryTable(apps);
  renderCharts();
  renderJobSection();
}

async function refresh() {
  try {
    [allEntries, allJobEntries] = await Promise.all([
      loadEntries(selectedDays),
      fetchJSON('/data/job-metrics.json').catch(() => []),
    ]);
    document.getElementById('error-banner').style.display = 'none';
  } catch (e) {
    const banner = document.getElementById('error-banner');
    banner.textContent = 'Failed to load metrics: ' + e.message;
    banner.style.display = 'block';
  }
  render();
  document.getElementById('range-info').textContent =
    `Updated ${new Date().toLocaleTimeString()}`;
}

// ---- Controls ----

document.querySelectorAll('.range-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    selectedDays = +btn.dataset.days;
    refresh();
  });
});

document.getElementById('app-select').addEventListener('change', e => {
  selectedApp = e.target.value;
  renderSummaryTable(getApps(allEntries).filter(app => {
    return Object.keys(aggregateByDay(allEntries, app, selectedDays)).length > 0;
  }));
  renderCharts();
});

document.getElementById('job-select').addEventListener('change', e => {
  selectedJob = e.target.value;
  renderJobCharts();
});

// Redraw all charts on resize so canvas pixels stay sharp
window.addEventListener('resize', () => { renderCharts(); renderJobCharts(); });

// ---- Init ----
refresh();
setInterval(refresh, REFRESH_MS);
