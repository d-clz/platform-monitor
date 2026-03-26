'use strict';

const REFRESH_MS = 60_000;

// ---- Fetch helpers ----

async function fetchJSON(url) {
  const res = await fetch(url, { cache: 'no-store' });
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
  ibLabel.className = 'tok-label';
  ibLabel.textContent = 'IB:';
  wrap.appendChild(ibLabel);
  wrap.appendChild(tokenPill(ocp.imageBuilderToken));

  const depLabel = document.createElement('span');
  depLabel.className = 'tok-label';
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
  tr.className = `row-${app.level}`;
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

// ---- Incident rendering ----

function fmtDuration(minutes) {
  if (!minutes && minutes !== 0) return '';
  if (minutes < 60)  return `${minutes}m`;
  if (minutes < 1440) return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
  return `${Math.floor(minutes / 1440)}d ${Math.floor((minutes % 1440) / 60)}h`;
}

function fmtTS(iso) {
  if (!iso) return '—';
  return new Date(iso).toLocaleString();
}

function renderNoteItem(inc, note, idx) {
  const item = document.createElement('div');
  item.className = 'note-item';

  // Header: timestamp + action buttons
  const header = document.createElement('div');
  header.className = 'note-item-header';

  const ts = document.createElement('span');
  ts.className = 'note-ts';
  ts.textContent = fmtTS(note.timestamp);
  header.appendChild(ts);

  const actions = document.createElement('div');
  actions.className = 'note-actions';

  const editBtn = document.createElement('button');
  editBtn.type = 'button';
  editBtn.className = 'note-action-btn';
  editBtn.textContent = 'Edit';

  const delBtn = document.createElement('button');
  delBtn.type = 'button';
  delBtn.className = 'note-action-btn delete';
  delBtn.textContent = 'Delete';

  actions.appendChild(editBtn);
  actions.appendChild(delBtn);
  header.appendChild(actions);
  item.appendChild(header);

  // Content (view mode)
  const text = document.createElement('div');
  text.className = 'note-text';
  text.textContent = note.content;
  item.appendChild(text);

  // Inline edit form (hidden by default)
  const editForm = document.createElement('div');
  editForm.className = 'note-inline-edit';
  const editArea = document.createElement('textarea');
  editArea.value = note.content;
  const editActions = document.createElement('div');
  editActions.className = 'note-inline-actions';
  const saveBtn = document.createElement('button');
  saveBtn.type = 'button';
  saveBtn.className = 'save';
  saveBtn.textContent = 'Save';
  const cancelBtn = document.createElement('button');
  cancelBtn.type = 'button';
  cancelBtn.className = 'cancel';
  cancelBtn.textContent = 'Cancel';
  editActions.appendChild(saveBtn);
  editActions.appendChild(cancelBtn);
  editForm.appendChild(editArea);
  editForm.appendChild(editActions);
  item.appendChild(editForm);

  // Toggle into edit mode
  const openEdit = () => {
    text.style.display = 'none';
    actions.style.opacity = '0';
    actions.style.pointerEvents = 'none';
    editForm.style.display = 'flex';
    editArea.focus();
    editArea.setSelectionRange(editArea.value.length, editArea.value.length);
  };
  const closeEdit = () => {
    editForm.style.display = 'none';
    text.style.display = '';
    actions.style.opacity = '';
    actions.style.pointerEvents = '';
    editArea.value = note.content;
  };

  editBtn.addEventListener('click', openEdit);
  cancelBtn.addEventListener('click', closeEdit);

  saveBtn.addEventListener('click', async () => {
    const content = editArea.value.trim();
    if (!content) return;
    saveBtn.disabled = true;
    saveBtn.textContent = 'Saving…';
    try {
      const res = await fetch('/notes', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ incidentId: inc.id, noteIndex: idx, content }),
      });
      if (res.ok) {
        await refresh();
      } else {
        alert(`Failed to update note: ${res.status} ${res.statusText}`);
        closeEdit();
      }
    } catch (err) {
      alert(`Error: ${err.message}`);
    } finally {
      saveBtn.disabled = false;
      saveBtn.textContent = 'Save';
    }
  });

  delBtn.addEventListener('click', async () => {
    if (!confirm('Delete this note?')) return;
    delBtn.disabled = true;
    try {
      const res = await fetch('/notes', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ incidentId: inc.id, noteIndex: idx }),
      });
      if (res.ok) {
        await refresh();
      } else {
        alert(`Failed to delete note: ${res.status} ${res.statusText}`);
      }
    } catch (err) {
      alert(`Error: ${err.message}`);
    } finally {
      delBtn.disabled = false;
    }
  });

  return item;
}

function renderIncidentCard(inc) {
  const card = document.createElement('div');
  card.className = `inc-card ${inc.status} ${inc.peakLevel}`;

  // ---- Header ----
  const header = document.createElement('div');
  header.className = 'inc-header';

  const appSpan = document.createElement('span');
  appSpan.className = 'inc-app';
  appSpan.textContent = inc.appName;
  header.appendChild(appSpan);

  header.appendChild(badge(inc.peakLevel));

  const metaSpan = document.createElement('span');
  metaSpan.className = 'inc-meta';
  if (inc.status === 'resolved') {
    metaSpan.textContent = `${fmtTS(inc.openedAt)} → ${fmtTS(inc.closedAt)}`;
  } else {
    metaSpan.textContent = `opened ${fmtTS(inc.openedAt)}`;
  }
  header.appendChild(metaSpan);

  if (inc.status === 'resolved' && inc.durationMinutes != null) {
    const dur = document.createElement('span');
    dur.className = 'inc-duration';
    dur.textContent = fmtDuration(inc.durationMinutes);
    header.appendChild(dur);
  }

  const statusEl = document.createElement('span');
  statusEl.className = `inc-status ${inc.status}`;
  const dot = document.createElement('span');
  dot.className = 'inc-dot';
  statusEl.appendChild(dot);
  statusEl.appendChild(document.createTextNode(
    inc.status === 'open' ? 'Open' : 'Resolved'
  ));
  header.appendChild(statusEl);

  card.appendChild(header);

  // ---- Body ----
  const body = document.createElement('div');
  body.className = 'inc-body';

  // Issues
  if (inc.issues && inc.issues.length > 0) {
    const issueWrap = document.createElement('div');
    const issueTitle = document.createElement('div');
    issueTitle.className = 'inc-issues-title';
    issueTitle.textContent = 'Issues';
    issueWrap.appendChild(issueTitle);
    const ul = document.createElement('ul');
    ul.className = 'inc-issues-list';
    inc.issues.forEach(iss => {
      const li = document.createElement('li');
      li.textContent = iss;
      ul.appendChild(li);
    });
    issueWrap.appendChild(ul);
    body.appendChild(issueWrap);
  }

  // Notes
  const notesWrap = document.createElement('div');
  const notesTitle = document.createElement('div');
  notesTitle.className = 'inc-notes-title';
  notesTitle.textContent = 'Notes';
  notesWrap.appendChild(notesTitle);

  if (inc.notes && inc.notes.length > 0) {
    const notesList = document.createElement('div');
    notesList.className = 'inc-notes-list';
    inc.notes.forEach((n, idx) => notesList.appendChild(renderNoteItem(inc, n, idx)));
    notesWrap.appendChild(notesList);
  }

  // Note form — visibility depends on incident state:
  //   has notes              → collapsed, toggled by "Edit" button
  //   resolved + no notes    → collapsed, toggled by "Add note" button
  //   open + no notes        → hidden (stay focused on fixing)
  const hasNotes   = inc.notes && inc.notes.length > 0;
  const isResolved = inc.status === 'resolved';

  const showToggle = hasNotes || isResolved;

  if (showToggle) {
    const toggleLabel = 'Add note';

    const toggleBtn = document.createElement('button');
    toggleBtn.type = 'button';
    toggleBtn.className = 'note-toggle-btn';
    toggleBtn.textContent = toggleLabel;

    const form = document.createElement('form');
    form.className = 'note-form';
    form.style.display = 'none';
    const textarea = document.createElement('textarea');
    textarea.placeholder = 'Root cause, fix, lesson learned…';
    const saveBtn = document.createElement('button');
    saveBtn.type = 'submit';
    saveBtn.textContent = 'Save';
    form.appendChild(textarea);
    form.appendChild(saveBtn);

    toggleBtn.addEventListener('click', () => {
      const visible = form.style.display !== 'none';
      form.style.display = visible ? 'none' : 'flex';
      toggleBtn.textContent = visible ? toggleLabel : 'Cancel';
      if (!visible) textarea.focus();
    });

    form.addEventListener('submit', async e => {
      e.preventDefault();
      const content = textarea.value.trim();
      if (!content) return;
      saveBtn.disabled = true;
      saveBtn.textContent = 'Saving…';
      try {
        const res = await fetch('/notes', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ incidentId: inc.id, content }),
        });
        if (res.ok) {
          textarea.value = '';
          form.style.display = 'none';
          await refresh();
        } else {
          alert(`Failed to save note: ${res.status} ${res.statusText}`);
        }
      } catch (err) {
        alert(`Error: ${err.message}`);
      } finally {
        saveBtn.disabled = false;
        saveBtn.textContent = 'Save';
      }
    });

    notesWrap.appendChild(toggleBtn);
    notesWrap.appendChild(form);
  }

  body.appendChild(notesWrap);
  card.appendChild(body);
  return card;
}

function renderIncidents(incidents) {
  const list = document.getElementById('incident-list');
  list.innerHTML = '';

  if (!incidents || incidents.length === 0) {
    const el = document.createElement('div');
    el.className = 'empty';
    el.textContent = 'No incidents recorded yet.';
    list.appendChild(el);
    return;
  }

  // Open incidents first (newest open → oldest open), then resolved (newest first).
  const open     = incidents.filter(i => i.status === 'open')
    .sort((a, b) => new Date(b.openedAt) - new Date(a.openedAt));
  const resolved = incidents.filter(i => i.status === 'resolved')
    .sort((a, b) => new Date(b.openedAt) - new Date(a.openedAt));

  [...open, ...resolved].forEach(inc => list.appendChild(renderIncidentCard(inc)));
}

// ---- Refresh loop ----

async function refresh() {
  try {
    const [results, incidents] = await Promise.allSettled([
      fetchJSON('/data/results.json'),
      fetchJSON('/data/incidents.json'),
    ]);

    clearError();

    if (results.status === 'fulfilled') {
      renderResults(results.value);
    } else {
      showError(`Could not load results.json: ${results.reason.message}`);
    }

    if (incidents.status === 'fulfilled') {
      renderIncidents(incidents.value);
    } else if (incidents.reason?.message?.includes('404')) {
      renderIncidents([]);
    }

  } catch (err) {
    showError(`Unexpected error: ${err.message}`);
  }
}

refresh();
setInterval(refresh, REFRESH_MS);
