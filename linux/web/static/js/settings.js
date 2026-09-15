/**
 * settings.js - the model, the hardware, and the honest notices.
 *
 * Four things here exist because the service can be in a state the user needs
 * told about rather than left to infer from bad results:
 *
 *   demo mode          - sample data, never presented as real recognition
 *   insufficient memory- the box is below the floor the model needs
 *   OCR off            - packaged-goods recall is measurably worse without it
 *   CPU fallback       - a GPU runtime was detected but did not answer correctly
 */

import { api, emit, on, humanMessage, EventStream } from './api.js';

const el = (id) => document.getElementById(id);

const ui = {
  demoPanel: el('settings-demo-panel'),
  demo: el('settings-demo'),

  modelSummary: el('model-summary'),
  modelProgress: el('model-progress'),
  modelFill: el('model-progress-fill'),
  modelLabel: el('model-progress-label'),
  modelError: el('model-error'),
  modelMemory: el('model-memory'),
  start: el('model-start'),
  pause: el('model-pause'),
  resume: el('model-resume'),
  retry: el('model-retry'),

  runtimeKV: el('runtime-kv'),
  runtimeFallback: el('runtime-fallback'),

  form: el('settings-form'),
  household: el('settings-household'),
  diet: el('settings-diet'),
  allergies: el('settings-allergies'),
  error: el('settings-error'),

  cameraState: el('camera-state'),
};

let modelStream = null;

/* ------------------------------------------------------------ helpers --- */

function gib(bytes) {
  const n = Number(bytes);
  if (!Number.isFinite(n) || n <= 0) return null;
  return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GiB`;
}

function setRows(rows) {
  if (!ui.runtimeKV) return;
  ui.runtimeKV.textContent = '';
  for (const [term, value] of rows) {
    if (value == null || value === '') continue;
    const dt = document.createElement('dt');
    dt.textContent = term;
    const dd = document.createElement('dd');
    dd.textContent = value;
    ui.runtimeKV.append(dt, dd);
  }
}

function notice(node, message) {
  if (!node) return;
  node.hidden = !message;
  node.textContent = message || '';
}

/* -------------------------------------------------------------- status -- */

function applyStatus(status) {
  if (!status) return;

  // The orange demo badge, mirroring the iOS one. Sample data is never passed
  // off as real recognition; the reason the service gives is shown with it.
  const demo = Boolean(status.demoMode);
  if (ui.demoPanel) ui.demoPanel.hidden = !demo;
  if (demo && ui.demo) {
    const reason = status.demoReason ? ` ${status.demoReason}` : '';
    ui.demo.textContent = `Demo mode (sample data). Nothing here came from looking at your photos.${reason}`;
  }

  const ocr = status.ocrMode || (status.ocr && status.ocr.mode);
  const ocrLine =
    ocr === 'none' || ocr === 'off' || ocr === false
      ? 'off (packaged goods are harder to read without it)'
      : String(ocr || 'unknown');

  setRows([
    ['Model', status.modelState || 'unknown'],
    ['Hardware target', status.hwTarget],
    ['Runtime variant', status.runtimeVariant],
    ['Text recognition', ocrLine],
    ['Memory total', gib(status.memory && status.memory.totalBytes)],
    ['Memory available', gib(status.memory && status.memory.availableBytes)],
    ['Service version', status.versions && status.versions.service],
    ['Runtime version', status.versions && status.versions.runtime],
  ]);

  // A GPU runtime was detected, probed, and did not give the right answer, so
  // the service fell back to CPU and made it sticky. On IoT this is the norm,
  // not an incident, but it explains a scan that suddenly takes minutes.
  if (status.runtimeFallback) {
    const detail = typeof status.runtimeFallback === 'string' ? ` ${status.runtimeFallback}` : '';
    notice(
      ui.runtimeFallback,
      `An accelerated runtime was detected but did not pass its known-answer check, so this service is running on the CPU.${detail} Scans will take longer.`
    );
  } else {
    notice(ui.runtimeFallback, '');
  }

  // Below the floor the model needs. The service still starts, the UI still
  // works, and the mock backs the API, which is the check working rather than
  // a failure.
  if (status.degraded || status.insufficientMemory) {
    const mem = status.memory || {};
    const have = gib(mem.availableBytes) || gib(mem.totalBytes);
    const need = gib(mem.requiredBytes);
    const numbers = have && need ? ` This box has ${have} available against roughly ${need} needed.` : '';
    notice(
      ui.modelMemory,
      `There is not enough memory here to hold the model.${numbers} Closing other services on this box, or moving to one with more memory, is the fix. Until then scans fall back to sample data.`
    );
  } else {
    notice(ui.modelMemory, '');
  }
}

/* --------------------------------------------------------------- model -- */

function applyModel(model) {
  if (!model || typeof model !== 'object') return;

  const state = String(model.state || model.phase || 'unknown');
  const done = Number(model.downloadedBytes) || 0;
  const total = Number(model.totalBytes) || 0;

  if (ui.modelSummary) {
    const size = total ? ` (${gib(total)})` : '';
    ui.modelSummary.textContent = `Model files: ${state}${size}.`;
  }

  // A download is the one place a percentage is a real measurement: the total
  // is known up front and every byte counts the same.
  const active = state === 'downloading' || state === 'paused';
  if (ui.modelProgress) ui.modelProgress.hidden = !active || !total;
  if (active && total) {
    const pct = Math.min(100, Math.round((done / total) * 100));
    if (ui.modelFill) ui.modelFill.style.width = `${pct}%`;
    if (ui.modelLabel) ui.modelLabel.textContent = `${gib(done)} of ${gib(total)} (${pct}%)`;
  }

  notice(ui.modelError, model.error || '');

  const show = (node, on_) => {
    if (node) node.hidden = !on_;
  };
  show(ui.start, state === 'idle' || state === 'absent' || state === 'notStarted');
  show(ui.pause, state === 'downloading');
  show(ui.resume, state === 'paused');
  show(ui.retry, state === 'failed' || Boolean(model.error));
}

async function refreshModel() {
  try {
    applyModel(await api.model());
  } catch (err) {
    if (err && err.status === 401) return;
    notice(ui.modelError, humanMessage(err));
  }
}

async function modelAction(action) {
  try {
    const out = await api.modelAction(action);
    if (out) applyModel(out);
    else await refreshModel();
    emit('dd:status-please', {});
  } catch (err) {
    notice(ui.modelError, humanMessage(err));
  }
}

function watchModel() {
  if (modelStream) return;
  modelStream = new EventStream(api.modelEventsPath(), {
    events: ['progress', 'state', 'done', 'error'],
    onEvent: (type, data) => {
      applyModel(data);
      if (type === 'done') emit('dd:status-please', {});
    },
    poll: () => api.model(),
    pollEveryMs: 4000,
  });
  modelStream.start();
}

/* ------------------------------------------------------------ settings -- */

async function loadSettings() {
  try {
    const values = await api.settings();
    if (ui.household) ui.household.value = String(values.householdSize != null ? values.householdSize : 2);
    if (ui.diet) ui.diet.value = values.dietPreference || '';
    if (ui.allergies) ui.allergies.value = values.allergies || '';
    notice(ui.error, '');
  } catch (err) {
    if (err && err.status === 401) return;
    notice(ui.error, humanMessage(err));
  }
}

async function saveSettings(event) {
  event.preventDefault();
  try {
    await api.saveSettings({
      householdSize: Number(ui.household ? ui.household.value : 2) || 2,
      dietPreference: ui.diet ? ui.diet.value.trim() : '',
      allergies: ui.allergies ? ui.allergies.value.trim() : '',
    });
    notice(ui.error, '');
    emit('dd:toast', { message: 'Saved.' });
  } catch (err) {
    notice(ui.error, humanMessage(err));
  }
}

/* ---------------------------------------------------------------- init -- */

function describeCamera() {
  if (!ui.cameraState) return;
  ui.cameraState.textContent = window.isSecureContext
    ? 'This page is a secure context, so the live preview inside the page is available as well as the photo button.'
    : 'This page is not a secure context, so the live preview inside the page is unavailable. The photo button still works, and is the path most people should use.';
}

function init() {
  if (!ui.modelSummary && !ui.form) return;

  describeCamera();

  if (ui.start) ui.start.addEventListener('click', () => modelAction('start'));
  if (ui.pause) ui.pause.addEventListener('click', () => modelAction('pause'));
  if (ui.resume) ui.resume.addEventListener('click', () => modelAction('resume'));
  if (ui.retry) ui.retry.addEventListener('click', () => modelAction('retry'));
  if (ui.form) ui.form.addEventListener('submit', saveSettings);

  on('dd:status', (e) => applyStatus(e.detail));
  on('dd:paired', () => {
    loadSettings();
    refreshModel();
  });
  on('dd:view', (e) => {
    if (e.detail && e.detail.view === 'settings') watchModel();
  });

  loadSettings();
  refreshModel();
}

init();
