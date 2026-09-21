/* Cron UI — plain JavaScript, no build step.
 *
 * Sections: i18n · api · state · render · task form · history · settings ·
 * import/export · init. All strings come from i18n.js via t(); all backend
 * calls go through api(), which attaches the ZimaOS session token.
 */
'use strict';

const API_BASE = '/cron';
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

/* ---------- i18n ---------- */

const LANGS = window.CRON_I18N || {};
const SHELL_LANG_MAP = { en: 'en', de: 'de', fr: 'fr', zh: 'zh' };
let lang = 'en';

// The ZimaOS shell keeps the UI language in localStorage.lang as "fr_FR",
// "de_DE", … (measured on v1.7.1); this module lives on the same origin and
// follows it unless the user picked a language here (cron_lang).
function resolveLanguage() {
  const own = safeGet('cron_lang');
  if (own && LANGS[own]) return own;
  const shell = (safeGet('lang') || navigator.language || 'en').slice(0, 2).toLowerCase();
  return LANGS[SHELL_LANG_MAP[shell]] ? SHELL_LANG_MAP[shell] : 'en';
}

function t(key, params) {
  let s = (LANGS[lang] && LANGS[lang][key]) || (LANGS.en && LANGS.en[key]) || key;
  if (params) for (const [k, v] of Object.entries(params)) s = s.replace(`{${k}}`, v);
  return s;
}

function applyI18n() {
  document.documentElement.lang = lang;
  document.title = t('app.title');
  $$('[data-i18n]').forEach((el) => { el.textContent = t(el.dataset.i18n); });
  $$('[data-i18n-ph]').forEach((el) => { el.placeholder = t(el.dataset.i18nPh); });
  $$('[data-i18n-title]').forEach((el) => { el.title = t(el.dataset.i18nTitle); });
  $('#langSelect').value = lang;
  fillStaticSelects();
}

function setLanguage(next) {
  lang = LANGS[next] ? next : 'en';
  safeSet('cron_lang', lang);
  applyI18n();
  render();
}

const dateFmt = () => new Intl.DateTimeFormat(lang === 'zh' ? 'zh-CN' : lang, { dateStyle: 'medium', timeStyle: 'short' });
const fmtTime = (ms) => (ms ? dateFmt().format(new Date(ms)) : '–');

function safeGet(k) { try { return localStorage.getItem(k); } catch { return null; } }
function safeSet(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } }

/* ---------- api ---------- */

class ApiError extends Error {
  constructor(status, code, message) { super(message); this.status = status; this.code = code; }
}

// The gateway forwards module calls without the session token, so it is
// attached here from the shell's localStorage. A 401 first goes through the
// shell's own refresh endpoint (measured on v1.7.1: POST /v1/users/refresh
// with {refresh_token} → data.{access_token,refresh_token,expires_at}, the
// same three keys the shell keeps in localStorage) and the call is retried
// once; only when that fails does the banner ask for a reload.
async function api(path, opts = {}, retried = false) {
  const headers = { Accept: 'application/json', ...(opts.headers || {}) };
  if (opts.body !== undefined && !(opts.body instanceof FormData)) headers['Content-Type'] = 'application/json';
  const token = safeGet('access_token');
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(API_BASE + path, { ...opts, headers, body: opts.body !== undefined && typeof opts.body !== 'string' ? JSON.stringify(opts.body) : opts.body });
  if (res.status === 401 && !retried && await refreshSession()) return api(path, opts, true);
  if (res.status === 204) return null;
  const isJson = (res.headers.get('content-type') || '').includes('application/json');
  const data = isJson ? await res.json().catch(() => ({})) : await res.text();
  if (!res.ok) throw new ApiError(res.status, (data && data.code) || 'http', (data && data.error) || `HTTP ${res.status}`);
  return data;
}

let refreshing = null;
function refreshSession() {
  if (refreshing) return refreshing; // parallel calls share one renewal
  refreshing = (async () => {
    const rt = safeGet('refresh_token');
    if (!rt) return false;
    try {
      const res = await fetch('/v1/users/refresh', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refresh_token: rt }) });
      const body = res.ok ? await res.json() : null;
      const d = body && body.data;
      if (!d || !d.access_token) return false;
      safeSet('access_token', d.access_token);
      if (d.refresh_token) safeSet('refresh_token', d.refresh_token);
      if (d.expires_at !== undefined) safeSet('expires_at', String(d.expires_at));
      return true;
    } catch {
      return false;
    } finally {
      setTimeout(() => { refreshing = null; }, 0);
    }
  })();
  return refreshing;
}

function describeError(err) {
  if (err instanceof ApiError && LANGS.en[`error.${err.code}`]) return t(`error.${err.code}`);
  return t('error.generic', { msg: err.message || String(err) });
}

/* ---------- state ---------- */

const state = {
  tasks: [],
  templates: [],
  categories: [],
  tags: [],
  backupJobs: [], // Sync & Backup module's jobs, read-only; null when it did not answer
  openLogs: new Map(), // id -> { entries, loading, search }
  pollTimer: null,
  editingId: null,
};

async function loadTasks() {
  const params = new URLSearchParams();
  if ($('#filterCategory').value) params.set('category', $('#filterCategory').value);
  if ($('#filterTag').value) params.set('tag', $('#filterTag').value);
  const q = params.toString();
  try {
    const [tasks, categories, tags] = await Promise.all([
      api(`/tasks${q ? `?${q}` : ''}`), api('/categories'), api('/tags'),
    ]);
    state.tasks = tasks;
    state.categories = categories;
    state.tags = tags;
    hideBanner();
    render();
    schedulePoll();
    loadBackupJobs();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      showBanner(describeError(err), 'bad');
    } else {
      showBanner(t('error.offline'), 'warn');
      setTimeout(loadTasks, 5000);
    }
  }
}

// Poll while something is executing so the pulse and the result update
// without the user clicking; stop as soon as everything is idle.
function schedulePoll() {
  clearTimeout(state.pollTimer);
  if (state.tasks.some((task) => task.executing)) state.pollTimer = setTimeout(loadTasks, 2000);
}

async function loadTemplates() {
  try { state.templates = await api('/templates'); } catch { state.templates = []; }
  fillTemplateSelect();
}

/* ---------- render ---------- */

function render() {
  renderStats();
  renderFilters();
  renderTable();
}

/* ---------- Sync & Backup jobs, read-only ----------
   The Sync & Backup module keeps its own scheduler; this list only shows
   what it has planned so one page holds everything that runs on a timer.
   Measured on ZimaOS 1.7.1 from this page: GET /v2/zbackup/api/jobs answers
   200 with the shell's session token, 401 without, and a module that is not
   installed gives 404 at the gateway — then the card stays hidden. */

const ZBACKUP_API = '/v2/zbackup/api';

async function loadBackupJobs() {
  const card = $('#zbackupCard');
  try {
    const res = await fetchWithSession(`${ZBACKUP_API}/jobs`);
    if (res.status === 404) { card.hidden = true; return; }
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const jobs = await res.json();
    state.backupJobs = Array.isArray(jobs) ? jobs : [];
    renderBackupJobs();
  } catch (err) {
    // the module is there but did not answer: say so instead of hiding it
    console.warn('Sync & Backup jobs not loaded', err);
    state.backupJobs = null;
    renderBackupJobs(describeError(err));
  }
}

// fetchWithSession is fetch with the shell's token and one refresh on 401,
// for an endpoint outside this module's API base.
async function fetchWithSession(url, retried = false) {
  const token = safeGet('access_token');
  const res = await fetch(url, { headers: { Accept: 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) } });
  if (res.status === 401 && !retried && await refreshSession()) return fetchWithSession(url, true);
  return res;
}

function backupScheduleLabel(job) {
  const s = job.schedule || {};
  if (s.type === 'manual' || !s.type) return t('zb.manual');
  return scheduleLabel({ type: s.type, interval_min: s.interval_min, cron_expr: s.cron_expr });
}

function backupResultPill(job) {
  if (job.running) return `<span class="pill accent"><span class="dot pulse"></span>${t('status.executing')}</span>`;
  const r = job.last_result;
  if (!r) return `<span class="muted">${t('zb.notRun')}</span>`;
  const cls = r.success ? (r.code === 'empty' ? 'warn' : 'ok') : 'bad';
  const label = LANGS.en[`zb.result.${r.code}`] ? t(`zb.result.${r.code}`) : r.code;
  return `<span class="pill ${cls}" title="${esc((r.message || '').slice(0, 300))}">${esc(label)}</span>`;
}

function renderBackupJobs(errorText) {
  const card = $('#zbackupCard');
  const body = $('#zbackupBody');
  const jobs = state.backupJobs;
  if (errorText) {
    card.hidden = false;
    body.innerHTML = `<tr><td colspan="5" class="empty">${esc(errorText)}</td></tr>`;
    return;
  }
  if (!jobs || !jobs.length) { card.hidden = true; return; }
  card.hidden = false;
  body.innerHTML = '';
  for (const job of jobs) {
    const tr = document.createElement('tr');
    tr.className = job.enabled ? '' : 'disabled';
    const from = (job.sources || []).map((p) => p.split('/').filter(Boolean).pop() || p);
    const to = job.target && (job.target.path || job.target.remote || job.target.host || job.target.type) || '';
    const next = job.enabled && job.schedule && job.schedule.type !== 'manual' && job.next_run_at ? fmtTime(job.next_run_at) : '–';
    tr.innerHTML = `
      <td class="name"><strong>${esc(job.name)}</strong><code class="cmd" title="${esc((job.sources || []).join('\n'))}">${esc(from.join(', '))} → ${esc(to)}</code></td>
      <td class="nowrap"><span class="tag">${t(job.kind === 'sync' ? 'zb.kind.sync' : 'zb.kind.backup')}</span>${job.enabled ? '' : ` <span class="muted">${t('zb.disabled')}</span>`}</td>
      <td class="nowrap">${backupScheduleLabel(job)}</td>
      <td class="nowrap">${next}</td>
      <td>${backupResultPill(job)}</td>`;
    body.appendChild(tr);
  }
}

function renderStats() {
  const total = state.tasks.length;
  const running = state.tasks.filter((task) => task.status === 'running').length;
  const last = Math.max(0, ...state.tasks.map((task) => task.last_run_at || 0));
  $('#statTotal').textContent = total;
  $('#statRunning').textContent = running;
  $('#statPaused').textContent = total - running;
  $('#statLastRun').textContent = last ? fmtTime(last) : t('stats.never');
}

function renderFilters() {
  fillSelect($('#filterCategory'), state.categories, t('tasks.filterAllCategories'));
  fillSelect($('#filterTag'), state.tags, t('tasks.filterAllTags'));
  const dl = $('#categoryList');
  dl.innerHTML = '';
  state.categories.forEach((c) => { const o = document.createElement('option'); o.value = c; dl.appendChild(o); });
}

function fillSelect(sel, values, allLabel) {
  const cur = sel.value;
  sel.innerHTML = '';
  sel.appendChild(new Option(allLabel, ''));
  values.forEach((v) => sel.appendChild(new Option(v, v)));
  sel.value = values.includes(cur) ? cur : '';
}

function scheduleLabel(task) {
  const f = formOf(task);
  const time = () => `${pad2(f.hour)}:${pad2(f.minute)}`;
  switch (f.kind) {
    case 'daily': return t('sched.words.daily', { time: time() });
    case 'weekly': return t('sched.words.weekly', { day: t(`day.${f.weekday}`), time: time() });
    case 'monthly': return t('sched.words.monthly', { day: f.day, time: time() });
    case 'hourly': return f.minute ? t('sched.words.hourlyAt', { minute: pad2(f.minute) }) : t('sched.words.hourly');
    case 'minutes': {
      const m = f.every;
      if (m % 1440 === 0) return m === 1440 ? t('schedule.everyDay') : t('schedule.everyDays', { n: m / 1440 });
      if (m % 60 === 0) return m === 60 ? t('schedule.everyHour') : t('schedule.everyHours', { n: m / 60 });
      return t('schedule.everyMin', { n: m });
    }
    default: return `<code>${esc(f.expr)}</code>`;
  }
}

function statusPill(task) {
  if (task.executing) return `<span class="pill accent"><span class="dot pulse"></span>${t('status.executing')}</span>`;
  if (task.status === 'running') return `<span class="pill ok"><span class="dot"></span>${t('status.running')}</span>`;
  return `<span class="pill warn"><span class="dot"></span>${t('status.paused')}</span>`;
}

function resultPill(result, task) {
  if (!result) return '<span class="muted">–</span>';
  const label = LANGS.en[`result.${result.code}`] ? t(`result.${result.code}`) : (result.success ? t('result.completed') : t('result.exit_error'));
  const cls = result.success ? 'ok' : (result.code && result.code.startsWith('skipped') ? 'warn' : 'bad');
  const retry = task && task.current_retry ? ` <span class="muted">${t('result.retry', { n: task.current_retry, max: task.retry_count })}</span>` : '';
  return `<span class="pill ${cls}" title="${esc((result.message || '').slice(0, 300))}">${label}</span>${retry}`;
}

function renderTable() {
  const body = $('#taskBody');
  body.innerHTML = '';
  if (!state.tasks.length) {
    body.innerHTML = `<tr><td colspan="6" class="empty">${t('tasks.empty')}</td></tr>`;
    return;
  }
  for (const task of state.tasks) {
    const tr = document.createElement('tr');
    tr.className = 'task-row';
    tr.dataset.id = task.id;
    const badges = [
      task.category ? `<span class="tag cat">${esc(task.category)}</span>` : '',
      ...(task.tags || []).map((tag) => `<span class="tag">${esc(tag)}</span>`),
      task.depends_on && task.depends_on.length ? `<span class="tag" title="${t('tasks.dependsOn', { n: task.depends_on.length })}">↳ ${task.depends_on.length}</span>` : '',
    ].join('');
    const open = state.openLogs.has(task.id);
    tr.innerHTML = `
      <td class="name"><strong>${esc(task.name)}</strong><div>${badges}</div><code class="cmd" title="${esc(task.command)}">${esc(task.command)}</code></td>
      <td class="nowrap">${scheduleLabel(task)}</td>
      <td>${statusPill(task)}</td>
      <td class="nowrap">${task.status === 'running' ? fmtTime(task.next_run_at) : '–'}</td>
      <td>${resultPill(task.last_result, task)}</td>
      <td class="actions">
        <button class="sm" data-act="run">${t('action.run')}</button>
        <button class="sm" data-act="edit">${t('action.edit')}</button>
        <button class="sm" data-act="toggle">${task.status === 'running' ? t('action.pause') : t('action.resume')}</button>
        <button class="sm" data-act="logs">${open ? t('action.hideLogs') : t('action.logs')}</button>
        <button class="sm ghost" data-act="delete">${t('action.delete')}</button>
      </td>`;
    body.appendChild(tr);
    if (open) body.appendChild(renderLogsRow(task));
  }
}

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/* ---------- task actions ---------- */

async function onTableClick(ev) {
  const btn = ev.target.closest('button[data-act]');
  if (!btn) return;
  const id = btn.closest('tr').dataset.id;
  const task = state.tasks.find((x) => x.id === id);
  if (!task) return;
  try {
    switch (btn.dataset.act) {
      case 'run': await api(`/tasks/${id}/run`, { method: 'POST' }); await loadTasks(); break;
      case 'toggle': await api(`/tasks/${id}/toggle`, { method: 'POST' }); await loadTasks(); break;
      case 'edit': openTaskForm(task); break;
      case 'logs': toggleLogs(task); break;
      case 'delete':
        confirmDialog(t('confirm.deleteTitle'), t('confirm.deleteText', { name: task.name }), t('confirm.delete'), async () => {
          await api(`/tasks/${id}`, { method: 'DELETE' });
          state.openLogs.delete(id);
          await loadTasks();
        });
        break;
      default:
    }
  } catch (err) { showBanner(describeError(err), 'bad'); }
}

async function runAll() {
  const ids = state.tasks.filter((task) => task.status === 'running').map((task) => task.id);
  if (!ids.length) return;
  try { await api('/tasks/bulk/run', { method: 'POST', body: { ids } }); await loadTasks(); } catch (err) { showBanner(describeError(err), 'bad'); }
}

/* ---------- history ---------- */

function toggleLogs(task) {
  if (state.openLogs.has(task.id)) { state.openLogs.delete(task.id); render(); return; }
  const entry = { entries: [], loading: true, search: '' };
  state.openLogs.set(task.id, entry);
  render();
  api(`/tasks/${task.id}/logs`).then((logs) => { entry.entries = logs; entry.loading = false; render(); })
    .catch((err) => { entry.loading = false; showBanner(describeError(err), 'bad'); render(); });
}

function renderLogsRow(task) {
  const view = state.openLogs.get(task.id);
  const tr = document.createElement('tr');
  tr.className = 'logs-row';
  tr.dataset.id = task.id;
  const td = document.createElement('td');
  td.colSpan = 6;
  const term = view.search.toLowerCase();
  const shown = view.entries.filter((l) => !term || (l.message || '').toLowerCase().includes(term)).slice(0, 200);
  const spark = view.entries.length > 1
    ? `<div class="spark">${view.entries.slice(0, 40).reverse().map((l) => `<i class="${l.success ? '' : 'bad'}" title="${fmtTime(l.time)}"></i>`).join('')}</div>` : '';
  let list;
  if (view.loading) list = `<div class="muted">${t('logs.loading')}</div>`;
  else if (!shown.length) list = `<div class="muted">${t('logs.empty')}</div>`;
  else {
    list = `<div class="logs-list">${shown.map((l) => `
      <div class="log-item">
        <span class="time">${fmtTime(l.time)}</span>
        <pre>${esc(l.message) || `<span class="muted">${t('logs.noOutput')}</span>`}</pre>
        <span class="dur">${t('logs.duration', { ms: l.duration_ms || 0 })}</span>
        ${resultPill({ success: l.success, code: l.code, message: '' })}
      </div>`).join('')}</div>`;
  }
  td.innerHTML = `
    <div class="logs-head">
      <span class="title">${t('logs.title')} · ${esc(task.name)}</span>
      <input type="text" class="compact log-search" placeholder="${t('logs.search')}" value="${esc(view.search)}">
      <button class="sm" data-log="csv">${t('logs.exportCsv')}</button>
      <button class="sm" data-log="json">${t('logs.exportJson')}</button>
      <button class="sm ghost" data-log="clear">${t('logs.clear')}</button>
    </div>${spark}${list}`;
  tr.appendChild(td);
  $('.log-search', td).addEventListener('input', (ev) => {
    view.search = ev.target.value;
    const pos = ev.target.selectionStart;
    render();
    const again = $(`tr.logs-row[data-id="${task.id}"] .log-search`);
    if (again) { again.focus(); again.setSelectionRange(pos, pos); }
  });
  $$('button[data-log]', td).forEach((b) => b.addEventListener('click', () => onLogAction(task, b.dataset.log)));
  return tr;
}

async function onLogAction(task, action) {
  if (action === 'csv' || action === 'json') {
    // window.open cannot carry the bearer header; fetch and hand over a blob.
    const res = await fetch(`${API_BASE}/tasks/${task.id}/logs${action === 'csv' ? '?format=csv' : ''}`, { headers: { Authorization: `Bearer ${safeGet('access_token') || ''}` } });
    const blob = await res.blob();
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `${task.name.replace(/[^\w.-]+/g, '_')}_history.${action}`;
    a.click();
    URL.revokeObjectURL(a.href);
    return;
  }
  confirmDialog(t('logs.clear'), t('confirm.clearLogsText', { name: task.name }), t('confirm.clear'), async () => {
    await api(`/tasks/${task.id}/logs/clear`, { method: 'POST' });
    const view = state.openLogs.get(task.id);
    if (view) view.entries = [];
    render();
  });
}

/* ---------- task form ---------- */

const form = {
  name: () => $('#nameInput'), command: () => $('#commandInput'), kind: () => $('#scheduleKind'),
  interval: () => $('#intervalInput'), cron: () => $('#cronInput'), category: () => $('#categoryInput'),
  schedTime: () => $('#schedTime'), schedWeekday: () => $('#schedWeekday'), schedDay: () => $('#schedDay'), schedMinute: () => $('#schedMinute'),
  priority: () => $('#priorityInput'), tags: () => $('#tagsInput'), timeout: () => $('#timeoutInput'),
  maxLogs: () => $('#maxLogsInput'), retryCount: () => $('#retryCountInput'), retryDelay: () => $('#retryDelayInput'),
  depends: () => $('#dependsSelect'), allowParallel: () => $('#allowParallelCheck'),
  webhookUrl: () => $('#webhookUrlInput'), webhookFormat: () => $('#webhookFormatSelect'),
  webhookOnSuccess: () => $('#webhookOnSuccess'), webhookOnFailure: () => $('#webhookOnFailure'),
  emailTo: () => $('#emailToInput'), smtpHost: () => $('#smtpHostInput'), smtpUser: () => $('#smtpUserInput'),
  smtpPass: () => $('#smtpPassInput'), smtpPort: () => $('#smtpPortInput'),
  emailOnSuccess: () => $('#emailOnSuccess'), emailOnFailure: () => $('#emailOnFailure'),
};

function fillStaticSelects() {
  const wf = form.webhookFormat();
  const cur = wf.value;
  wf.innerHTML = '';
  ['generic', 'n8n', 'discord', 'slack', 'home_assistant', 'uptime_kuma'].forEach((f) => wf.appendChild(new Option(t(`webhookFormat.${f}`), f)));
  wf.value = cur || 'generic';
  fillTemplateSelect();
}

function fillTemplateSelect() {
  const sel = $('#templateSelect');
  sel.innerHTML = '';
  sel.appendChild(new Option(t('form.blank'), ''));
  state.templates.forEach((tpl) => {
    const name = LANGS.en[`tpl.${tpl.id}.name`] ? t(`tpl.${tpl.id}.name`) : tpl.name;
    const desc = LANGS.en[`tpl.${tpl.id}.desc`] ? t(`tpl.${tpl.id}.desc`) : tpl.description;
    sel.appendChild(new Option(`${name} — ${desc}`, tpl.id));
  });
}

function resetForm() {
  $('#formNotice').hidden = true;
  Object.values(form).forEach((get) => {
    const el = get();
    if (el.type === 'checkbox') el.checked = false;
    else if (el.multiple) Array.from(el.options).forEach((o) => { o.selected = false; });
    else el.value = '';
  });
  fillSchedule({ type: 'cron', cron_expr: '0 3 * * *' });
  validateSchedule();
  form.webhookFormat().value = 'generic';
  form.webhookOnFailure().checked = true;
  form.emailOnFailure().checked = true;
  $('#envRows').innerHTML = '';
  $('#templateSelect').value = '';
  $('#cronFeedback').textContent = '';
  updateScheduleFields();
}

function openTaskForm(task) {
  resetForm();
  state.editingId = task ? task.id : null;
  $('#taskModalTitle').textContent = task ? t('form.editTitle') : t('form.createTitle');
  $('#taskSaveBtn').textContent = task ? t('form.save') : t('form.create');
  $('#templateField').hidden = !!task;
  const dep = form.depends();
  dep.innerHTML = '';
  state.tasks.filter((x) => !task || x.id !== task.id).forEach((x) => dep.appendChild(new Option(x.name, x.id)));
  if (task) fillForm(task);
  $('#taskModal').hidden = false;
  form.name().focus();
}

function fillForm(task) {
  form.name().value = task.name;
  form.command().value = task.command;
  fillSchedule(task);
  validateSchedule();
  form.category().value = task.category || '';
  form.priority().value = task.priority || '';
  form.tags().value = (task.tags || []).join(', ');
  form.timeout().value = task.timeout_sec || '';
  form.maxLogs().value = task.max_log_entries || '';
  form.retryCount().value = task.retry_count || '';
  form.retryDelay().value = task.retry_delay_sec || '';
  form.allowParallel().checked = !!task.allow_parallel;
  Array.from(form.depends().options).forEach((o) => { o.selected = (task.depends_on || []).includes(o.value); });
  Object.entries(task.env || {}).forEach(([k, v]) => addEnvRow(k, v));
  for (const n of task.notifications || []) {
    if (n.type === 'webhook') {
      form.webhookUrl().value = n.target; form.webhookFormat().value = n.webhook_format || 'generic';
      form.webhookOnSuccess().checked = !!n.on_success; form.webhookOnFailure().checked = !!n.on_failure;
    } else if (n.type === 'email') {
      form.emailTo().value = n.target; form.smtpHost().value = n.smtp_host || ''; form.smtpPort().value = n.smtp_port || '';
      form.smtpUser().value = n.smtp_user || ''; form.smtpPass().value = n.smtp_pass || '';
      form.emailOnSuccess().checked = !!n.on_success; form.emailOnFailure().checked = !!n.on_failure;
    }
  }
  updateScheduleFields();
}

function updateScheduleFields() {
  const kind = form.kind().value;
  $$('[data-sched]').forEach((el) => { el.hidden = !el.dataset.sched.split(' ').includes(kind); });
}

/* ----- schedule as words (mirrors schedule.Form in lintux-modkit) ----- */

const pad2 = (n) => String(n).padStart(2, '0');

// scheduleFromForm renders the picked words as the API's {type,
// interval_min, cron_expr}; "every N minutes" is the interval type.
function scheduleFromForm() {
  const kind = form.kind().value;
  const [hh, mm] = (form.schedTime().value || '03:00').split(':').map(Number);
  const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, Number(v) || lo));
  switch (kind) {
    case 'daily': return { type: 'cron', cron_expr: `${mm} ${hh} * * *` };
    case 'weekly': return { type: 'cron', cron_expr: `${mm} ${hh} * * ${Number(form.schedWeekday().value)}` };
    case 'monthly': return { type: 'cron', cron_expr: `${mm} ${hh} ${clamp(form.schedDay().value, 1, 28)} * *` };
    case 'hourly': return { type: 'cron', cron_expr: `${clamp(form.schedMinute().value, 0, 59)} * * * *` };
    case 'minutes': return { type: 'interval', interval_min: parseInt(form.interval().value, 10) || 0 };
    default: return { type: 'cron', cron_expr: form.cron().value.trim() };
  }
}

// formOf reads a task's schedule back into words. Strict: a bare number in
// each field, nothing else — what the form cannot say stays an expression.
function formOf(task) {
  if (task.type === 'interval') return { kind: 'minutes', every: task.interval_min || 0 };
  const f = (task.cron_expr || '').trim().split(/\s+/);
  const num = (x, lo, hi) => (/^\d+$/.test(x) && Number(x) >= lo && Number(x) <= hi ? Number(x) : null);
  if (f.length === 5) {
    const [mi, ho, dom, dow] = [num(f[0], 0, 59), num(f[1], 0, 23), num(f[2], 1, 28), num(f[4], 0, 7)];
    const star = (x) => x === '*';
    if (mi !== null && ho !== null && star(f[2]) && star(f[3]) && star(f[4])) return { kind: 'daily', hour: ho, minute: mi };
    if (mi !== null && ho !== null && star(f[2]) && star(f[3]) && dow !== null) return { kind: 'weekly', hour: ho, minute: mi, weekday: dow % 7 };
    if (mi !== null && ho !== null && dom !== null && star(f[3]) && star(f[4])) return { kind: 'monthly', hour: ho, minute: mi, day: dom };
    if (mi !== null && star(f[1]) && star(f[2]) && star(f[3]) && star(f[4])) return { kind: 'hourly', minute: mi };
  }
  return { kind: 'cron', expr: task.cron_expr || '' };
}

function fillSchedule(task) {
  const f = formOf(task);
  form.kind().value = f.kind;
  form.schedTime().value = `${pad2(f.hour ?? 3)}:${pad2(f.minute ?? 0)}`;
  form.schedWeekday().value = String(f.weekday ?? 0);
  form.schedDay().value = f.day ?? 1;
  form.schedMinute().value = f.kind === 'hourly' ? f.minute : 0;
  form.interval().value = f.kind === 'minutes' ? (f.every || '') : '';
  form.cron().value = f.kind === 'cron' ? f.expr : (task.cron_expr || '0 3 * * *');
  updateScheduleFields();
}

function addEnvRow(key = '', value = '') {
  const row = document.createElement('div');
  row.className = 'env-row';
  row.innerHTML = `<input type="text" class="env-key" placeholder="${t('form.envKey')}"><input type="text" class="env-val" placeholder="${t('form.envValue')}"><button type="button" class="sm ghost">&times;</button>`;
  $('.env-key', row).value = key;
  $('.env-val', row).value = value;
  $('button', row).addEventListener('click', () => row.remove());
  $('#envRows').appendChild(row);
}

function readForm() {
  const num = (el) => { const v = parseInt(el.value, 10); return Number.isFinite(v) ? v : 0; };
  const req = {
    name: form.name().value.trim(),
    command: form.command().value.trim(),
    ...scheduleFromForm(),
    timeout_sec: num(form.timeout()),
    retry_count: num(form.retryCount()),
    retry_delay_sec: num(form.retryDelay()),
    max_log_entries: num(form.maxLogs()),
    category: form.category().value.trim(),
    priority: num(form.priority()),
    tags: form.tags().value.split(',').map((s) => s.trim()).filter(Boolean),
    depends_on: Array.from(form.depends().selectedOptions).map((o) => o.value),
    allow_parallel: form.allowParallel().checked,
    notifications: [],
  };
  const env = {};
  $$('#envRows .env-row').forEach((row) => { const k = $('.env-key', row).value.trim(); if (k) env[k] = $('.env-val', row).value; });
  if (Object.keys(env).length) req.env = env;
  if (form.webhookUrl().value.trim()) {
    req.notifications.push({ enabled: true, type: 'webhook', target: form.webhookUrl().value.trim(), webhook_format: form.webhookFormat().value,
      on_success: form.webhookOnSuccess().checked, on_failure: form.webhookOnFailure().checked });
  }
  if (form.emailTo().value.trim() && form.smtpHost().value.trim()) {
    req.notifications.push({ enabled: true, type: 'email', target: form.emailTo().value.trim(), smtp_host: form.smtpHost().value.trim(),
      smtp_port: num(form.smtpPort()) || 587, smtp_user: form.smtpUser().value.trim(), smtp_pass: form.smtpPass().value,
      on_success: form.emailOnSuccess().checked, on_failure: form.emailOnFailure().checked });
  }
  return req;
}

async function saveTask() {
  const notice = $('#formNotice');
  notice.hidden = true;
  const req = readForm();
  try {
    if (state.editingId) await api(`/tasks/${state.editingId}`, { method: 'PUT', body: req });
    else await api('/tasks', { method: 'POST', body: req });
    $('#taskModal').hidden = true;
    await loadTasks();
  } catch (err) {
    notice.textContent = describeError(err);
    notice.hidden = false;
  }
}

function applyTemplate() {
  const tpl = state.templates.find((x) => x.id === $('#templateSelect').value);
  if (!tpl) return;
  form.name().value = LANGS.en[`tpl.${tpl.id}.name`] ? t(`tpl.${tpl.id}.name`) : tpl.name;
  form.command().value = tpl.command;
  form.category().value = tpl.category || '';
  form.timeout().value = tpl.timeout_sec || '';
  fillSchedule(tpl);
  validateSchedule();
}

let cronTimer = null;
// validateSchedule shows the next runs for every list shape (they are all
// cron expressions underneath) and the validator's verdict for a typed one.
function validateSchedule() {
  const sch = scheduleFromForm();
  const fb = $('#cronFeedback');
  clearTimeout(cronTimer);
  if (sch.type !== 'cron' || !sch.cron_expr) { fb.textContent = ''; fb.className = 'cron-feedback'; return; }
  const typed = form.kind().value === 'cron';
  cronTimer = setTimeout(async () => {
    try {
      const d = await api('/cron/validate', { method: 'POST', body: { expr: sch.cron_expr } });
      if (d.valid) {
        fb.className = 'cron-feedback ok';
        fb.innerHTML = `${typed ? `✓ ${t('form.cronValid')}` : ''}<div class="next">${t('form.nextRuns')} ${d.next_runs.slice(0, 3).map(fmtTime).join(' · ')}</div>`;
      } else {
        fb.className = 'cron-feedback bad';
        fb.innerHTML = `✗ ${t('form.cronInvalid')}${d.errors.map((e) => `<div>${esc(e.field)}: ${esc(e.message)}</div>`).join('')}`;
      }
    } catch { fb.textContent = ''; }
  }, 250);
}

/* ---------- dialogs ---------- */

function confirmDialog(title, text, okLabel, onOk) {
  $('#confirmTitle').textContent = title;
  $('#confirmText').textContent = text;
  const ok = $('#confirmOkBtn');
  ok.textContent = okLabel;
  ok.onclick = async () => {
    try { await onOk(); } catch (err) { showBanner(describeError(err), 'bad'); }
    $('#confirmModal').hidden = true;
  };
  $('#confirmModal').hidden = false;
}

function showBanner(text, kind) {
  const b = $('#banner');
  b.className = `notice banner ${kind}`;
  b.textContent = text;
  b.hidden = false;
}
function hideBanner() { $('#banner').hidden = true; }

/* ---------- settings ---------- */

async function openSettings() {
  $('#settingsNotice').hidden = true;
  try {
    const s = await api('/settings');
    $('#tgTokenInput').value = s.telegram_bot_token || '';
    $('#tgChatInput').value = s.telegram_chat_id || '';
    $('#tgOnSuccess').checked = !!s.telegram_on_success;
    $('#tgOnFailure').checked = s.telegram_on_failure !== false;
    $('#settingsModal').hidden = false;
  } catch (err) { showBanner(describeError(err), 'bad'); }
}

function settingsBody() {
  return {
    telegram_bot_token: $('#tgTokenInput').value.trim(),
    telegram_chat_id: $('#tgChatInput').value.trim(),
    telegram_on_success: $('#tgOnSuccess').checked,
    telegram_on_failure: $('#tgOnFailure').checked,
  };
}

async function saveSettings() {
  const n = $('#settingsNotice');
  try {
    await api('/settings', { method: 'PUT', body: settingsBody() });
    n.className = 'notice ok'; n.textContent = t('settings.saved'); n.hidden = false;
    setTimeout(() => { $('#settingsModal').hidden = true; }, 600);
  } catch (err) { n.className = 'notice bad'; n.textContent = describeError(err); n.hidden = false; }
}

async function testTelegram() {
  const n = $('#settingsNotice');
  n.className = 'notice'; n.textContent = t('settings.testSending'); n.hidden = false;
  try {
    const body = settingsBody();
    const d = await api('/settings/test-telegram', { method: 'POST', body: { bot_token: body.telegram_bot_token, chat_id: body.telegram_chat_id } });
    n.className = d.success ? 'notice ok' : 'notice bad';
    n.textContent = d.success ? t('settings.testOk') : `${t('settings.testFail')}: ${d.error || ''}`;
  } catch (err) { n.className = 'notice bad'; n.textContent = describeError(err); }
}

/* ---------- import / export ---------- */

async function exportTasks() {
  try {
    const data = await api('/export');
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `cron_export_${new Date().toISOString().slice(0, 10)}.json`;
    a.click();
    URL.revokeObjectURL(a.href);
  } catch (err) { showBanner(describeError(err), 'bad'); }
}

async function importTasks(file) {
  const body = $('#importBody');
  try {
    const text = await file.text();
    JSON.parse(text); // fail early with a readable message
    const r = await api('/import', { method: 'POST', body: text });
    let html = `<p>${t('import.result', { n: r.imported })}</p>`;
    if (r.skipped && r.skipped.length) {
      html += `<p class="muted" style="margin-top:8px">${t('import.skipped', { n: r.skipped.length })}</p><ul style="margin:6px 0 0 18px">`;
      html += r.skipped.map((s) => `<li><strong>${esc(s.name || '?')}</strong> — ${LANGS.en[`error.${s.code}`] ? t(`error.${s.code}`) : esc(s.reason)}</li>`).join('');
      html += '</ul>';
    }
    body.innerHTML = html;
    $('#importModal').hidden = false;
    await loadTasks();
  } catch (err) {
    body.innerHTML = `<p class="notice bad">${err instanceof SyntaxError ? t('error.bad_json') : describeError(err)}</p>`;
    $('#importModal').hidden = false;
  }
}

/* ---------- theme ---------- */

function initTheme() {
  const saved = safeGet('cron_theme');
  if (saved === 'dark') document.documentElement.dataset.theme = 'dark';
  updateThemeIcon();
}
function toggleTheme() {
  const dark = document.documentElement.dataset.theme === 'dark';
  if (dark) delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = 'dark';
  safeSet('cron_theme', dark ? 'light' : 'dark');
  updateThemeIcon();
}
function updateThemeIcon() { $('#themeToggle').innerHTML = document.documentElement.dataset.theme === 'dark' ? '&#9728;' : '&#9790;'; }

/* ---------- init ---------- */

function init() {
  lang = resolveLanguage();
  initTheme();
  applyI18n();

  $('#langSelect').addEventListener('change', (ev) => setLanguage(ev.target.value));
  $('#themeToggle').addEventListener('click', toggleTheme);
  $('#settingsBtn').addEventListener('click', openSettings);
  $('#settingsCancelBtn').addEventListener('click', () => { $('#settingsModal').hidden = true; });
  $('#settingsSaveBtn').addEventListener('click', saveSettings);
  $('#tgTestBtn').addEventListener('click', testTelegram);

  $('#newTaskBtn').addEventListener('click', () => openTaskForm(null));
  $('#taskCancelBtn').addEventListener('click', () => { $('#taskModal').hidden = true; });
  $('#taskSaveBtn').addEventListener('click', saveTask);
  $('#templateSelect').addEventListener('change', applyTemplate);
  $('#scheduleKind').addEventListener('change', () => { updateScheduleFields(); validateSchedule(); });
  ['#schedTime', '#schedWeekday', '#schedDay', '#schedMinute', '#cronInput'].forEach((sel) => { $(sel).addEventListener('input', validateSchedule); $(sel).addEventListener('change', validateSchedule); });
  $('#addEnvBtn').addEventListener('click', () => addEnvRow());
  $('#runAllBtn').addEventListener('click', runAll);
  $('#taskBody').addEventListener('click', onTableClick);
  $('#filterCategory').addEventListener('change', loadTasks);
  $('#filterTag').addEventListener('change', loadTasks);

  $('#confirmCancelBtn').addEventListener('click', () => { $('#confirmModal').hidden = true; });
  $('#exportBtn').addEventListener('click', exportTasks);
  $('#importBtn').addEventListener('click', () => $('#importFile').click());
  $('#importFile').addEventListener('change', (ev) => { if (ev.target.files[0]) importTasks(ev.target.files[0]); ev.target.value = ''; });
  $('#importCloseBtn').addEventListener('click', () => { $('#importModal').hidden = true; });

  $$('.modal-overlay').forEach((ov) => ov.addEventListener('click', (ev) => { if (ev.target === ov) ov.hidden = true; }));
  document.addEventListener('keydown', (ev) => { if (ev.key === 'Escape') $$('.modal-overlay').forEach((ov) => { ov.hidden = true; }); });

  loadTemplates();
  loadTasks();
}

document.addEventListener('DOMContentLoaded', init);
