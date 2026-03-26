'use strict';

const REFRESH_MS = 60_000;

// ---- Fetch helpers ----

async function fetchJSON(url) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${url} returned ${res.status}`);
  return res.json();
}

function showError(msg) {
  const el = document.getElementById('error-banner');
  el.textContent = msg;
  el.style.display = 'block';
}

function clearError() {
  const el = document.getElementById('error-banner');
  el.style.display = 'none';
}

// ---- Rendering helpers ----

function badge(level) {
  const el = document.createElement('span');
  el.className = `badge ${level}`;
  el.textContent = level;
  return el;
}

function pill(text, cls) {
  const el = document.createElement('span');
  el.className = `pill ${cls}`;
  el.textContent = text;
  return el;
}

function tokenPill(tok) {
  if (!tok || !tok.present) return pill('missing', 'missing');
  if (tok.level === 'critical') return pill(`${tok.ageDays}d`, 'missing');
  if (tok.level === 'warning')  return pill(`${tok.ageDays}d`, 'warn');
  return pill(`${tok.ageDays}d`, 'present');
}

function td(content) {
  const el = document.createElement('td');
  if (content instanceof Node) {
    el.appendChild(content);
  } else {
    el.textContent = content ?? '—';
  }
  return el;
}

function frag(...nodes) {
  const f = document.createDocumentFragment();
  nodes.forEach(n => f.appendChild(n));
  return f;
}

// ---- App row ----

function renderSAs(ocp) {
  if (!ocp) return document.createTextNode('—');
  const wrap = document.createElement('div');
  wrap.className = 'sa-status';
  wrap.appendChild(pill('IB', ocp.imageBuilderSAPresent ? 'present' : 'missing'));
  wrap.appendChild(pill('DEP', ocp.deployerSAPresent   ? 'present' : 'missing'));
  return wrap;
}

function renderTokens(ocp) {
  if (!ocp) return document.createTextNode('—');
  const wrap = document.createElement('div');
  wrap.className = 'sa-status';
  const ibLabel = document.createElement('span');
  ibLabel.style.cssText = 'font-size:11px;color:#64748b;margin-right:2px';
  ibLabel.textContent = 'IB:';
  wrap.appendChild(ibLabel);
  wrap.appendChild(tokenPill(ocp.imageBuilderToken));
  const depLabel = document.createElement('span');
  depLabel.style.cssText = 'font-size:11px;color:#64748b;margin-left:6px;margin-right:2px';
  depLabel.textContent = 'DEP:';
  wrap.appendChild(depLabel);
  wrap.appendChild(tokenPill(ocp.deployerToken));
  return wrap;
}

function renderNamespaces(ocp) {
  if (!ocp || !ocp.bindingNamespaces || ocp.bindingNamespaces.length === 0) {
    return document.createTextNode('—');
  }
  const wrap = document.createElement('div');
  wrap.className = 'ns-list';
  ocp.bindingNamespaces.forEach(ns => {
    const tag = document.createElement('span');
    tag.className = 'ns-tag';
    tag.textContent = ns;
    wrap.appendChild(tag);
  });
  return wrap;
}

function renderPipeline(gl) {
  if (!gl || !gl.lastPipeline) return document.createTextNode('—');
  const p = gl.lastPipeline;
  const wrap = document.createElement('div');
  const b = badge(pipelineLevelFor(p.status));
  wrap.appendChild(b);
  if (p.webURL) {
    const a = document.createElement('a');
    a.className = 'pipeline-link';
    a.href = p.webURL;
    a.target = '_blank';
    a.rel = 'noopener';
    a.textContent = ` #${p.id} (${p.ref})`;
    wrap.appendChild(a);
  } else {
    wrap.appendChild(document.createTextNode(` #${p.id} (${p.ref})`));
  }
  return wrap;
}

function pipelineLevelFor(status) {
  if (status === 'success' || status === 'passed') return 'ok';
  if (status === 'failed' || status === 'canceled') return 'warning';
  return 'warning';
}

function renderFailedJobs(gl) {
  if (!gl) return document.createTextNode('—');
  const byStage = gl.failedJobsByStage || {};
  const entries = Object.entries(byStage);
  if (entries.length === 0) return document.createTextNode('0');
  const wrap = document.createElement('div');
  wrap.className = 'ns-list';
  entries.forEach(([stage, count]) => {
    const tag = document.createElement('span');
    tag.className = 'ns-tag';
    tag.style.background = '#3b1c1c';
    tag.style.color = '#fca5a5';
    tag.textContent = `${stage}: ${count}`;
    wrap.appendChild(tag);
  });
  return wrap;
}

function renderRunners(gl) {
  if (!gl) return document.createTextNode('—');
  if (gl.runnerCount === 0) return pill('none', 'missing');
  const cls = gl.staleRunnerCount === gl.runnerCount ? 'warn' : 'present';
  const wrap = document.createElement('div');
  wrap.className = 'sa-status';
  wrap.appendChild(pill(`${gl.runnerCount - gl.staleRunnerCount}/${gl.runnerCount} active`, cls));
  return wrap;
}

function renderIssues(issues) {
  if (!issues || issues.length === 0) return document.createTextNode('—');
  const ul = document.createElement('ul');
  ul.className = 'issues-list';
  issues.forEach(i => {
    const li = document.createElement('li');
    li.textContent = i;
    ul.appendChild(li);
  });
  return ul;
}

function renderAppRow(app) {
  const tr = document.createElement('tr');
  tr.appendChild(td(app.name));
  const lvlTd = document.createElement('td');
  lvlTd.appendChild(badge(app.level));
  tr.appendChild(lvlTd);
  tr.appendChild(td(app.source.replace('_', ' ')));

  const saTd = document.createElement('td');
  saTd.appendChild(renderSAs(app.ocp));
  tr.appendChild(saTd);

  const tokTd = document.createElement('td');
  tokTd.appendChild(renderTokens(app.ocp));
  tr.appendChild(tokTd);

  const nsTd = document.createElement('td');
  nsTd.appendChild(renderNamespaces(app.ocp));
  tr.appendChild(nsTd);

  const pipeTd = document.createElement('td');
  pipeTd.appendChild(renderPipeline(app.gitlab));
  tr.appendChild(pipeTd);

  const jobsTd = document.createElement('td');
  jobsTd.appendChild(renderFailedJobs(app.gitlab));
  tr.appendChild(jobsTd);

  const runTd = document.createElement('td');
  runTd.appendChild(renderRunners(app.gitlab));
  tr.appendChild(runTd);

  const issueTd = document.createElement('td');
  issueTd.appendChild(renderIssues(app.issues));
  tr.appendChild(issueTd);

  return tr;
}

// ---- Results rendering ----

function renderResults(data) {
  document.getElementById('cnt-total').textContent = data.totalApps ?? 0;
  document.getElementById('cnt-ok').textContent    = data.okCount ?? 0;
  document.getElementById('cnt-warn').textContent  = data.warningCount ?? 0;
  document.getElementById('cnt-crit').textContent  = data.criticalCount ?? 0;
  document.getElementById('cnt-err').textContent   = data.errorCount ?? 0;

  const ts = data.timestamp ? new Date(data.timestamp).toLocaleString() : '—';
  document.getElementById('last-updated').textContent = `Last run: ${ts}`;

  const tbody = document.getElementById('app-tbody');
  tbody.innerHTML = '';
  if (!data.apps || data.apps.length === 0) {
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = 10;
    td.className = 'empty';
    td.textContent = 'No applications found.';
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  data.apps.forEach(app => tbody.appendChild(renderAppRow(app)));
}

// ---- History rendering ----

function renderHistory(entries) {
  const list = document.getElementById('history-list');
  list.innerHTML = '';

  if (!entries || entries.length === 0) {
    const li = document.createElement('li');
    li.className = 'empty';
    li.textContent = 'No alert history yet.';
    list.appendChild(li);
    return;
  }

  // Show newest first.
  [...entries].reverse().forEach(entry => {
    const li = document.createElement('li');
    li.className = 'hist-entry';

    const header = document.createElement('div');
    header.className = 'hist-header';

    const ts = document.createElement('span');
    ts.className = 'hist-ts';
    ts.textContent = new Date(entry.timestamp).toLocaleString();
    header.appendChild(ts);

    if (entry.criticalCount > 0) header.appendChild(badge('critical'));
    if (entry.warningCount  > 0) header.appendChild(badge('warning'));
    if (entry.errorCount    > 0) header.appendChild(badge('error'));

    li.appendChild(header);

    if (entry.alerts && entry.alerts.length > 0) {
      const ul = document.createElement('ul');
      ul.className = 'hist-alerts';
      entry.alerts.forEach(a => {
        const item = document.createElement('li');
        item.textContent = `[${a.level.toUpperCase()}] ${a.appName}: ${(a.issues || []).join('; ')}`;
        ul.appendChild(item);
      });
      li.appendChild(ul);
    }

    list.appendChild(li);
  });
}

// ---- Refresh loop ----

async function refresh() {
  try {
    const [results, history] = await Promise.allSettled([
      fetchJSON('/data/results.json'),
      fetchJSON('/data/history.json'),
    ]);

    clearError();

    if (results.status === 'fulfilled') {
      renderResults(results.value);
    } else {
      showError(`Could not load results.json: ${results.reason.message}`);
    }

    if (history.status === 'fulfilled') {
      renderHistory(history.value);
    } else if (history.reason?.message?.includes('404')) {
      renderHistory([]);
    }
    // Non-404 history errors are silently ignored (results are more important).

  } catch (err) {
    showError(`Unexpected error: ${err.message}`);
  }
}

refresh();
setInterval(refresh, REFRESH_MS);
