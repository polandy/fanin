'use strict';

const API = '/api/v1';
const TOKEN_KEY = 'fanin-owner-token';
const REFRESH_DELAY_MS = 200;
const VIEWS = ['inbox', 'board', 'hosts'];

let snap = null;
let refreshTimer = null;
let taskStream = null;

const main = document.getElementById('main');

// el builds an element; children are text or nodes, never HTML.
function el(tag, attrs, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === 'class') node.className = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else if (v === true) node.setAttribute(k, '');
    else if (v !== false && v != null) node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c != null && c !== false) node.append(c.nodeType ? c : String(c));
  }
  return node;
}

function notice(msg) {
  const n = document.getElementById('notice');
  n.textContent = msg || '';
  n.hidden = !msg;
}

function ownerToken() {
  let t = '';
  try { t = sessionStorage.getItem(TOKEN_KEY) || ''; } catch (e) { /* storage blocked */ }
  if (!t) {
    t = (window.prompt('Owner token (kept for this tab only):') || '').trim();
    if (t) {
      try { sessionStorage.setItem(TOKEN_KEY, t); } catch (e) { /* storage blocked */ }
    }
  }
  return t;
}

function forgetToken() {
  try { sessionStorage.removeItem(TOKEN_KEY); } catch (e) { /* storage blocked */ }
}

// post calls a command endpoint exactly like the CLI client; a failure is shown, never swallowed.
async function post(path, body) {
  const token = ownerToken();
  if (!token) { notice('No owner token: the command was not sent.'); return false; }
  const init = { method: 'POST', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' } };
  init.body = JSON.stringify(body || {});
  let resp;
  try {
    resp = await fetch(API + path, init);
  } catch (e) {
    notice('The daemon is unreachable: ' + e.message + '. Check that fanind runs, then retry.');
    return false;
  }
  if (resp.ok) { notice(''); await refresh(); return true; }
  if (resp.status === 401) forgetToken();
  let msg = resp.status + ' ' + resp.statusText;
  try { msg = (await resp.json()).error.message; } catch (e) { /* keep the status line */ }
  notice('Not done: ' + msg);
  return false;
}

async function refresh() {
  try {
    const resp = await fetch(API + '/snapshot');
    if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
    snap = await resp.json();
    notice('');
  } catch (e) {
    notice('Cannot read the snapshot: ' + e.message + '. The view shows the last data.');
    return;
  }
  render();
}

function scheduleRefresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(refresh, REFRESH_DELAY_MS);
}

function connect(after) {
  const live = document.getElementById('live');
  const es = new EventSource(API + '/events?after=' + after);
  es.onopen = () => { live.textContent = 'live'; live.classList.add('on'); };
  es.onerror = () => { live.textContent = 'reconnecting'; live.classList.remove('on'); };
  es.onmessage = scheduleRefresh;
  // The server names each SSE event after its type; listen to every one.
  for (const type of EVENT_TYPES) es.addEventListener(type, scheduleRefresh);
}

const EVENT_TYPES = [
  'task_created', 'task_state', 'run_started', 'run_state', 'progress', 'token_usage', 'result_reported',
  'gate_verdict', 'question_asked', 'question_answered', 'hold_set', 'hold_lifted', 'hook_started',
  'hook_finished', 'hook_failed', 'worker_nudged', 'pr_opened', 'merged', 'owner_go', 'project_paused',
  'project_resumed', 'pause_consumed', 'notified', 'notify_failed',
];

function route() {
  const h = location.hash.replace(/^#/, '');
  if (h.startsWith('task/')) return { view: 'task', id: decodeURIComponent(h.slice(5)) };
  return { view: VIEWS.includes(h) ? h : 'inbox' };
}

function taskLink(id, title) {
  return el('a', { href: '#task/' + encodeURIComponent(id) }, title || id);
}

function when(iso) {
  return iso ? new Date(iso).toLocaleString() : '';
}

function render() {
  if (!snap) return;
  const r = route();
  for (const a of document.querySelectorAll('nav a')) a.classList.toggle('active', a.dataset.view === r.view);
  const badge = document.getElementById('inbox-count');
  badge.textContent = snap.questions.length;
  badge.hidden = snap.questions.length === 0;
  if (taskStream && r.view !== 'task') { taskStream.close(); taskStream = null; }
  const views = { inbox: renderInbox, board: renderBoard, hosts: renderHosts, task: renderTask };
  main.replaceChildren(...views[r.view](r));
}

function renderInbox() {
  if (snap.questions.length === 0) return [el('p', { class: 'muted' }, 'Nothing waits for you.')];
  return snap.questions.map((q) => {
    const opts = q.options.map((o, i) => el('button', {
      class: i + 1 === q.recommended ? 'recommended' : '',
      onclick: (ev) => { ev.target.closest('.card').querySelectorAll('button').forEach((b) => { b.disabled = true; });
        post('/tasks/' + encodeURIComponent(q.task_id) + '/answer', { answer: String(i + 1) }).then((ok) => { if (!ok) render(); }); },
    }, (i + 1) + ') ' + o.label + (i + 1 === q.recommended ? ' (recommended)' : ''),
    o.description ? el('small', {}, o.description) : null));
    const free = q.allow_text ? el('form', {
      class: 'free',
      onsubmit: (ev) => {
        ev.preventDefault();
        const text = ev.target.elements.text.value.trim();
        if (text) post('/tasks/' + encodeURIComponent(q.task_id) + '/answer', { answer: text });
      },
    }, el('input', { name: 'text', placeholder: 'Free-text answer', 'aria-label': 'Free-text answer' }),
    el('button', { type: 'submit' }, 'Send')) : null;
    return el('section', { class: 'card' },
      el('div', { class: 'row' }, el('strong', {}, q.project), taskLink(q.task_id), el('span', { class: 'muted' }, when(q.asked_at))),
      el('pre', {}, q.question), el('div', { class: 'options' }, opts), free);
  });
}

function taskList(tasks, extra) {
  if (tasks.length === 0) return el('p', { class: 'muted' }, 'none');
  return el('ul', { class: 'plain' }, tasks.map((t) => el('li', { class: 'row' },
    taskLink(t.id, t.title || t.id), el('span', { class: 'muted' }, t.state), extra ? extra(t) : null)));
}

function readyList(ready) {
  if (ready.length === 0) return el('p', { class: 'muted' }, 'none');
  return el('ul', { class: 'plain' }, ready.map((r) => el('li', { class: 'row' },
    el('strong', {}, '#' + r.pr), taskLink(r.id, r.title || r.id), el('span', { class: 'muted' }, r.verdict))));
}

function renderBoard() {
  return snap.board.map((b) => {
    const holds = b.holds.length === 0 ? el('p', { class: 'muted' }, 'none') : el('ul', { class: 'plain' },
      b.holds.map((h) => el('li', { class: 'row warn-text' },
        el('strong', {}, h.kind + ' hold'), h.reason, h.fix_task ? el('span', {}, 'fix: ', taskLink(h.fix_task)) : null,
        el('button', { onclick: () => post('/holds/' + encodeURIComponent(h.id) + '/lift') }, 'Lift'))));
    return el('section', { class: 'card' }, el('h2', {}, b.project),
      el('h3', {}, 'Done (' + b.done.count + ')'), taskList(b.done.newest),
      el('h3', {}, 'Next'), taskList(b.next, (t) => (t.blocked ? el('span', { class: 'muted' }, 'blocked') : null)),
      el('h3', {}, 'Waiting on you'), taskList(b.waiting),
      el('h3', {}, 'Ready for go (' + b.ready.length + ')'), readyList(b.ready),
      el('h3', {}, 'CI and merge holds'), holds,
      el('h3', {}, 'Decisions'), taskList(b.decisions));
  });
}

function workerRow(w) {
  return el('li', { class: 'row' }, el('strong', {}, w.agent), w.project, el('span', { class: 'muted' }, w.status),
    w.waiting_owner ? el('span', { class: 'warn-text' }, 'waiting on you') : null,
    w.task_id ? taskLink(w.task_id) : null);
}

function renderHosts() {
  if (snap.hosts.length === 0) return [el('p', { class: 'muted' }, 'No host data: the daemon runs without a fleet view.')];
  return snap.hosts.map((h) => {
    if (h.warning) {
      return el('section', { class: 'card warn' }, el('h2', {}, h.name), el('p', { class: 'warn-text' }, 'Warning: ' + h.warning));
    }
    const group = (title, ws) => [el('h3', {}, title),
      ws.length ? el('ul', { class: 'plain' }, ws.map(workerRow)) : el('p', { class: 'muted' }, 'none')];
    return el('section', { class: 'card' }, el('h2', {}, h.name),
      group('Managed workers', h.workers.filter((w) => w.kind === 'managed')),
      group('Owner sessions', h.workers.filter((w) => w.kind === 'owner')));
  });
}

function renderTask(r) {
  const t = snap.tasks.find((x) => x.id === r.id);
  if (!t) return [el('p', { class: 'muted' }, 'Task ' + r.id + ' is not open. It is done, or the id is wrong.')];
  const runs = snap.runs.filter((x) => x.task_id === t.id);
  const history = el('ul', { class: 'plain', id: 'task-events' });
  watchTask(t.id, history);
  const q = snap.questions.find((x) => x.task_id === t.id);
  return [el('section', { class: 'card' }, el('h2', {}, t.title || t.id),
    el('div', { class: 'row' }, el('strong', {}, t.project), el('span', { class: 'muted' }, t.id + ' · ' + t.shape + ' · ' + t.state)),
    el('pre', {}, t.prompt)),
  q ? el('p', {}, 'An open question waits in the ', el('a', { href: '#inbox' }, 'inbox'), '.') : null,
  el('h3', {}, 'Runs'),
  runs.length ? el('ul', { class: 'plain' }, runs.map((x) => el('li', { class: 'row' },
    'run ' + x.number, el('span', { class: 'muted' }, x.state), x.branch, x.result && x.result.pr ? 'PR #' + x.result.pr : null)))
    : el('p', { class: 'muted' }, 'none'),
  el('h3', {}, 'Events'), history].filter(Boolean);
}

// watchTask lists the task's events from the whole log and follows new ones.
function watchTask(id, list) {
  if (taskStream) taskStream.close();
  taskStream = new EventSource(API + '/events?after=0');
  const add = (ev) => {
    const e = JSON.parse(ev.data);
    if (e.task_id !== id) return;
    list.append(el('li', { class: 'row' }, el('span', { class: 'muted' }, when(e.at)), e.type, e.detail));
  };
  for (const type of EVENT_TYPES) taskStream.addEventListener(type, add);
}

window.addEventListener('hashchange', render);
refresh().then(() => { if (snap) connect(snap.last_seq); });
