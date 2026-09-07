'use strict';
const $ = (id) => document.getElementById(id);
const sessionKey = location.hash.slice(1);
let stopped = false;
let state, selectedJob, formDirty = false, shownKey = '', hideKeyTimer, noticeTimer;
const routes = new Set(['overview', 'builds', 'connection', 'settings']);
async function api(route, method = 'GET', body) {
  const response = await fetch('/admin/' + route, {method, headers: {'Authorization': 'Bearer ' + sessionKey, ...(body ? {'Content-Type': 'application/json'} : {})}, body: body ? JSON.stringify(body) : undefined});
  const value = await response.json();
  if (!response.ok) throw new Error(value.error || 'The request could not be completed.');
  return value;
}
function notify(message, error = false) {
  const node = $('notice'); node.textContent = message; node.className = 'notice' + (error ? ' error' : ''); node.hidden = false;
  clearTimeout(noticeTimer); if (!error) noticeTimer = setTimeout(() => {node.hidden = true;}, 5000);
}
function navigate(route) {
  for (const name of routes) $(name).hidden = name !== route;
  for (const link of document.querySelectorAll('[data-tab]')) {if (link.dataset.tab === route) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');}
  document.title = 'Kilnbridge — ' + (route === 'overview' ? 'Overview' : route[0].toUpperCase() + route.slice(1));
}
document.addEventListener('click', event => {const link = event.target.closest('a[href^="#"]'); if (link && routes.has(link.hash.slice(1))) {event.preventDefault(); navigate(link.hash.slice(1));}});
async function action(button, work) {button.disabled = true; button.setAttribute('aria-busy', 'true'); try {await work();} catch (e) {notify(e.message, true);} finally {button.disabled = stopped; button.removeAttribute('aria-busy');}}
function emptyBuilds(container) {
  const box = document.createElement('div'); box.className = 'empty-state';
  const symbol = document.createElement('div'); symbol.className = 'empty-symbol'; symbol.textContent = '↗'; symbol.setAttribute('aria-hidden', 'true');
  const title = document.createElement('h3'); title.textContent = 'Ready for your first build';
  const description = document.createElement('p'); description.textContent = 'Send a source snapshot from your other machine to get started.';
  box.append(symbol, title, description); container.replaceChildren(box);
}
function renderBuilds(container, jobs) {
  if (!jobs.length) {emptyBuilds(container); return;}
  const nodes = jobs.map(job => {
    const row = document.createElement('button'); row.className = 'build-row'; row.type = 'button';
    const copy = document.createElement('div'); const name = document.createElement('strong'); name.textContent = job.project + ' · ' + job.version;
    const detail = document.createElement('p'); detail.textContent = job.stage + ' · ' + new Date(job.createdAt).toLocaleString([], {month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit'});
    const badge = document.createElement('span'); badge.className = 'small-status'; badge.dataset.status = job.status; badge.textContent = job.status.replaceAll('_', ' ');
    copy.append(name, detail); row.append(copy, badge); row.addEventListener('click', () => showJob(job.id)); return row;
  }); container.replaceChildren(...nodes);
}
function render() {
  const {config, jobs, platform, tools, updates} = state;
  $('version').textContent = 'Version ' + state.version;
  $('sidebar-name').textContent = config.name;
  const active = jobs.filter(j => j.status === 'running');
  const status = config.paused ? 'New builds paused' : active.length ? 'Build in progress' : 'Ready to build';
  $('sidebar-status').textContent = platform === 'windows' ? status : 'Development preview';
  $('agent-status').textContent = platform === 'windows' ? status : 'Development preview';
  $('agent-status').dataset.tone = config.paused || platform !== 'windows' ? 'warning' : 'success';
  $('machine-name').textContent = config.name;
  $('ready-title').textContent = config.paused ? 'Waiting for you to resume' : active.length ? active[0].stage : 'Your agent is listening';
  $('ready-description').textContent = platform === 'windows' ? 'Builds run in the background, even after you close this window.' : 'Settings and connection preview. Actual Windows builds run on your Windows PC.';
  $('tools-status').textContent = tools.status;
  $('prepare-tools').disabled = tools.status === 'Preparing' || platform !== 'windows';
  if (platform !== 'windows') $('tools-hint').textContent = 'Tool installation is available in the Windows application.';
  $('tools-log').textContent = tools.log || 'No setup activity yet.';
  $('update-status').textContent = updates.status;
  $('check-updates').disabled = updates.status === 'Checking';
  $('update-detail').textContent = updates.error || (updates.version ? 'Version ' + updates.version + ' will install after builds finish and settings closes.' : config.autoUpdate ? 'Your work always comes first.' : 'Automatic updates are turned off.');
  const selectedAddress = $('address').value;
  const addresses = JSON.stringify(state.addresses);
  if ($('address').dataset.values !== addresses) {
    $('address').replaceChildren(...state.addresses.map(address => {const option = document.createElement('option'); option.value = address; option.textContent = address; return option;}));
    if (state.addresses.includes(selectedAddress)) $('address').value = selectedAddress;
    $('address').dataset.values = addresses;
  }
  $('fingerprint').value = state.fingerprint;
  if (!formDirty) {
    $('settings-name').value = config.name; $('autostart').checked = config.autoStart; $('auto-update').checked = config.autoUpdate;
    $('paused').checked = config.paused; $('retention').value = String(config.retentionDays); $('repository').value = config.repository;
  }
  $('autostart').disabled = platform !== 'windows';
  const jobsJSON = JSON.stringify(jobs);
  if ($('all-builds').dataset.jobs !== jobsJSON) {renderBuilds($('all-builds'), jobs); renderBuilds($('recent-builds'), jobs.slice(0, 3)); $('all-builds').dataset.jobs = jobsJSON;}
}
async function refresh() {if (stopped) return; try {state = await api('state'); render(); if (selectedJob && $('job-dialog').open) await refreshJob();} catch (e) {notify(e.message, true); $('agent-status').textContent = 'Connection unavailable';}}
async function getKey() {const value = await api('key'); shownKey = value.key; return shownKey;}
function hideKey() {$('agent-key').value = ''; $('agent-key').type = 'password'; $('show-key').textContent = 'Show key'; shownKey = '';}
$('show-key').addEventListener('click', () => action($('show-key'), async () => {if ($('agent-key').type === 'text') {hideKey(); return;} $('agent-key').value = await getKey(); $('agent-key').type = 'text'; $('show-key').textContent = 'Hide key'; clearTimeout(hideKeyTimer); hideKeyTimer = setTimeout(hideKey, 30000);}));
$('copy-key').addEventListener('click', () => action($('copy-key'), async () => {await navigator.clipboard.writeText(await getKey()); notify('Access key copied.');}));
$('copy-env').addEventListener('click', () => action($('copy-env'), async () => {const key = await getKey(); await navigator.clipboard.writeText(`KILNBRIDGE_URL="${$('address').value}"\nKILNBRIDGE_KEY="${key}"\nKILNBRIDGE_PIN_SHA256="${state.fingerprint}"\n`); notify('Connection settings copied. Paste them into your local .env file.');}));
$('rotate-key').addEventListener('click', () => $('rotate-dialog').showModal());
$('keep-key').addEventListener('click', () => $('rotate-dialog').close());
$('confirm-rotate').addEventListener('click', () => action($('confirm-rotate'), async () => {await api('key/rotate', 'POST'); hideKey(); $('rotate-dialog').close(); notify('New key generated. Update your scripts before their next request.');}));
$('prepare-tools').addEventListener('click', () => action($('prepare-tools'), async () => {await api('tools/prepare', 'POST'); $('tools-details').open = true; await refresh();}));
$('check-updates').addEventListener('click', () => action($('check-updates'), async () => {await api('updates/check', 'POST'); await refresh();}));
$('settings-form').addEventListener('input', () => {formDirty = true; $('save-status').textContent = 'Unsaved changes';});
$('settings-form').addEventListener('submit', event => {event.preventDefault(); action(event.submitter, async () => {
  const config = {...state.config, name: $('settings-name').value.trim(), autoStart: $('autostart').checked, autoUpdate: $('auto-update').checked, paused: $('paused').checked, retentionDays: Number($('retention').value), repository: $('repository').value.trim()};
  await api('config', 'PUT', config); formDirty = false; $('save-status').textContent = 'Preferences saved'; await refresh();
});});
async function showJob(id) {selectedJob = id; try {await refreshJob(); if (!$('job-dialog').open) $('job-dialog').showModal();} catch (e) {notify(e.message, true);}}
async function refreshJob() {const {job, log} = await api('jobs/' + selectedJob); $('job-title').textContent = job.project + ' · ' + job.version; $('job-stage').textContent = job.error || job.destination || job.stage; const node = $('job-log'); const atEnd = node.scrollTop + node.clientHeight >= node.scrollHeight - 30; node.textContent = log || 'Waiting for build activity…'; if (atEnd) node.scrollTop = node.scrollHeight; $('cancel-job').hidden = !['running', 'queued'].includes(job.status); $('retry-job').hidden = job.status !== 'upload_failed';}
$('close-job').addEventListener('click', () => $('job-dialog').close());
$('job-dialog').addEventListener('close', () => {selectedJob = undefined;});
$('cancel-job').addEventListener('click', () => action($('cancel-job'), async () => {await api('jobs/' + selectedJob + '/cancel', 'POST'); await refresh();}));
$('retry-job').addEventListener('click', () => action($('retry-job'), async () => {await api('jobs/' + selectedJob + '/retry-upload', 'POST'); await refresh();}));
if (!/^[a-f0-9]{64}$/.test(sessionKey)) notify('Open this page from the Kilnbridge application to access your settings.', true);
else {refresh(); setInterval(() => {if (!document.hidden) refresh();}, 3000);}
document.addEventListener('visibilitychange', () => {if (document.hidden) hideKey(); else if (/^[a-f0-9]{64}$/.test(sessionKey)) refresh();});

$('stop-agent').addEventListener('click', () => action($('stop-agent'), async () => {
 await api('stop', 'POST'); stopped = true; hideKey();
 $('agent-status').textContent = 'Agent stopped'; $('sidebar-status').textContent = 'Stopped';
 notify('The background agent has stopped. You can close this window. Open Kilnbridge again to resume.');
 document.querySelectorAll('button, input, select').forEach(element => {element.disabled = true;});
}));
