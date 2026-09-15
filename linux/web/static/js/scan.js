/**
 * scan.js - the progress screen, built for inference that takes minutes.
 *
 * Three things follow from "minutes, not seconds" and none of them are
 * cosmetic:
 *
 * 1. There is no percentage bar. A bar parked at 25% for two minutes is a lie,
 *    and the four calls of a scan are not equal in length anyway. What is shown
 *    instead is an elapsed clock, a per-call clock, and which call of the budget
 *    is running. All three are facts.
 * 2. An ETA appears only when a measurement exists for this hardware class.
 *    docs/PORTING_NOTES.md is explicit that no Pi, Jetson or physical Linux box
 *    was available to measure on, so on most installs there is no ETA and that
 *    is the correct state. A guessed one is worse than none.
 * 3. The scan belongs to the service, not to this connection. A phone locking,
 *    a tab closing or a Wi-Fi handover must not kill a four-minute scan, so this
 *    file reattaches on load rather than assuming it started what it is showing.
 */

import { api, emit, on, humanMessage, EventStream, CATEGORIES, categoryLabel } from './api.js';

/**
 * The three terminal states, verbatim from the shipped ScanningView.
 *
 * `empty` and `failed` MUST stay distinct. `empty` means the model answered
 * every time and genuinely saw no food, so advice about light and framing is
 * honest. `failed` means the calls themselves failed and we do not know what
 * was in the photo, so the same advice would be blaming the user's photo for
 * our failure. That merge is the exact bug this split exists to fix.
 */
const TERMINAL = {
  empty: {
    status: '',
    icon: '\u{1F441}',
    title: 'Nothing spotted',
    body: 'We could not spot anything. Try getting closer, adding more light, or filling more of the frame.',
    action: 'Try another photo',
  },
  failed: {
    status: 'The scan could not finish.',
    icon: '\u{26A0}',
    title: 'Scan could not finish',
    body: 'Something went wrong while identifying your food, so we could not read this photo. This usually clears up after closing other apps and trying again.',
    action: 'Try again',
  },
  cancelled: {
    status: 'Scan cancelled.',
    icon: '\u{2715}',
    title: 'Scan cancelled',
    body: 'No items were saved. You can start again whenever you like.',
    action: 'Back',
  },
};

const LAST_SCAN_KEY = 'dd.lastScanID';
const ACTIVE_PHASES = new Set(['idle', 'scanning']);

const el = (id) => document.getElementById(id);

const ui = {
  panel: el('scan-panel'),
  status: el('scan-status'),
  call: el('scan-call'),
  elapsed: el('scan-elapsed'),
  eta: el('scan-eta'),
  items: el('scan-items'),
  cancel: el('scan-cancel'),
  terminal: el('scan-terminal'),
  terminalIcon: el('terminal-icon'),
  terminalTitle: el('terminal-title'),
  terminalBody: el('terminal-body'),
  terminalDetail: el('terminal-detail'),
  terminalActions: el('terminal-actions'),
  review: el('scan-review'),
  reviewItems: el('review-items'),
  confirm: el('review-confirm'),
  discard: el('review-discard'),
};

const state = {
  scanID: null,
  phase: 'idle',
  progress: { photoIndex: 0, photoCount: 0, stage: '', itemsDone: 0, itemsTotal: 0 },
  items: [],
  inferenceFailures: 0,
  abortReason: '',
  abortDetail: '',
  callsUsed: 0,
  callBudget: 4,
  startedAt: 0,
  callStartedAt: 0,
  estimateSeconds: 0,
  estimateMeasured: false,
  streamStatus: 'live',
};

let stream = null;
let ticker = null;
let reviewDraft = [];

/* ----------------------------------------------------------- formatting - */

function clock(seconds) {
  const s = Math.max(0, Math.floor(seconds));
  const m = Math.floor(s / 60);
  return `${m}:${String(s % 60).padStart(2, '0')}`;
}

/** Mirrors the status line ScanningView builds from the same progress values. */
function progressLine() {
  const p = state.progress;
  const photoPart = p.photoCount > 1 ? `Photo ${(p.photoIndex || 0) + 1} of ${p.photoCount}: ` : '';
  if (p.stage === 'cropping') return `${photoPart}splitting the photo into items...`;
  if (p.stage === 'identifying') {
    const total = Math.max(p.itemsTotal || 0, 1);
    const done = Math.min((p.itemsDone || 0) + 1, total);
    return `${photoPart}identifying item ${done} of ${total}...`;
  }
  return state.items.length ? 'Still looking...' : 'Looking for items...';
}

/* -------------------------------------------------------------- render -- */

function setPanels(which) {
  if (ui.panel) ui.panel.hidden = which !== 'running';
  if (ui.terminal) ui.terminal.hidden = which !== 'terminal';
  if (ui.review) ui.review.hidden = which !== 'review';
  emit('dd:scan-phase', { phase: state.phase, active: which === 'running' });
}

function renderClock() {
  if (!state.startedAt) return;
  const elapsed = (Date.now() - state.startedAt) / 1000;

  if (ui.elapsed) {
    const perCall = state.callStartedAt ? (Date.now() - state.callStartedAt) / 1000 : 0;
    ui.elapsed.textContent = state.callStartedAt
      ? `Elapsed ${clock(elapsed)} (this call ${clock(perCall)})`
      : `Elapsed ${clock(elapsed)}`;
  }

  if (ui.eta) {
    // No measurement for this hardware class means no estimate. Elapsed only.
    if (state.estimateMeasured && state.estimateSeconds > 0) {
      const left = state.estimateSeconds - elapsed;
      ui.eta.hidden = false;
      ui.eta.textContent = left > 0 ? `About ${clock(left)} left` : 'Longer than usual';
    } else {
      ui.eta.hidden = true;
      ui.eta.textContent = '';
    }
  }
}

function renderItems(list, into, editable) {
  if (!into) return;
  into.textContent = '';
  list.forEach((item, index) => {
    const li = document.createElement('li');
    li.className = 'card';

    const main = document.createElement('div');
    main.className = 'card__main';

    if (editable) {
      const nameField = document.createElement('label');
      nameField.className = 'field';
      const nameLabel = document.createElement('span');
      nameLabel.className = 'field__label';
      nameLabel.textContent = 'Name';
      const nameInput = document.createElement('input');
      nameInput.className = 'field__input';
      nameInput.type = 'text';
      nameInput.value = item.name || '';
      nameInput.addEventListener('input', () => {
        reviewDraft[index].name = nameInput.value;
      });
      nameField.append(nameLabel, nameInput);

      const catField = document.createElement('label');
      catField.className = 'field field--compact';
      const catLabel = document.createElement('span');
      catLabel.className = 'field__label';
      catLabel.textContent = 'Category';
      const select = document.createElement('select');
      select.className = 'field__input';
      for (const c of CATEGORIES) {
        const option = document.createElement('option');
        option.value = c.raw;
        option.textContent = c.label;
        if (String(item.category || item.categoryRaw || '').toLowerCase() === c.raw) option.selected = true;
        select.appendChild(option);
      }
      select.addEventListener('change', () => {
        reviewDraft[index].category = select.value;
      });
      catField.append(catLabel, select);

      main.append(nameField, catField);
    } else {
      const title = document.createElement('p');
      title.className = 'card__title';
      title.textContent = item.name || 'Unnamed';
      main.appendChild(title);

      const meta = document.createElement('p');
      meta.className = 'card__meta';
      const bits = [categoryLabel(item.category || item.categoryRaw)];
      if (item.brand) bits.push(item.brand);
      meta.textContent = bits.join(' | ');
      main.appendChild(meta);
    }

    li.appendChild(main);

    const actions = document.createElement('div');
    actions.className = 'card__actions';

    const confidence = Number(item.confidence);
    if (Number.isFinite(confidence)) {
      const badge = document.createElement('span');
      badge.className = 'badge badge--confidence';
      badge.dataset.low = String(confidence < 0.5);
      badge.textContent = `${Math.round(confidence * 100)}%`;
      actions.appendChild(badge);
    }

    if (editable) {
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'btn btn--quiet';
      remove.textContent = 'Remove';
      remove.addEventListener('click', () => {
        reviewDraft.splice(index, 1);
        renderItems(reviewDraft, ui.reviewItems, true);
      });
      actions.appendChild(remove);
    }

    li.appendChild(actions);
    into.appendChild(li);
  });
}

function renderRunning() {
  if (ui.status) {
    const suffix = state.streamStatus === 'retrying' ? ' (reconnecting, the scan keeps running)' : '';
    ui.status.textContent = progressLine() + suffix;
  }
  if (ui.call) {
    const used = Math.min(Math.max(state.callsUsed, 1), state.callBudget);
    ui.call.textContent = `Call ${used} of ${state.callBudget}`;
  }
  renderItems(state.items, ui.items, false);
  renderClock();
  setPanels('running');
}

function actionButton(label, kind, onClick) {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = `btn ${kind}`;
  button.textContent = label;
  button.addEventListener('click', onClick);
  return button;
}

function renderTerminal(phase) {
  const copy = TERMINAL[phase];
  if (!copy || !ui.terminal) return;

  ui.terminal.dataset.phase = phase;
  if (ui.terminalIcon) ui.terminalIcon.textContent = copy.icon;
  if (ui.terminalTitle) ui.terminalTitle.textContent = copy.title;
  if (ui.terminalBody) ui.terminalBody.textContent = copy.body;

  if (ui.status) ui.status.textContent = copy.status;

  // On a device sitting at the memory floor the "close other apps" advice in
  // the failed copy is literally correct, and the service can say which floor
  // it hit. Append that rather than leaving the user to guess.
  if (ui.terminalDetail) {
    if (phase === 'failed' && state.abortReason === 'insufficient_memory' && state.abortDetail) {
      ui.terminalDetail.hidden = false;
      ui.terminalDetail.textContent = state.abortDetail;
    } else {
      ui.terminalDetail.hidden = true;
      ui.terminalDetail.textContent = '';
    }
  }

  if (ui.terminalActions) {
    ui.terminalActions.textContent = '';
    if (phase === 'cancelled' && state.items.length) {
      const keep = `Keep the ${state.items.length} found`;
      ui.terminalActions.appendChild(actionButton(keep, 'btn--primary', () => enterReview()));
      ui.terminalActions.appendChild(actionButton(copy.action, 'btn--quiet', reset));
    } else {
      ui.terminalActions.appendChild(actionButton(copy.action, 'btn--primary', reset));
    }
  }

  setPanels('terminal');
}

function enterReview() {
  reviewDraft = state.items.map((item) => ({
    name: item.name || '',
    brand: item.brand === undefined ? null : item.brand,
    category: String(item.category || item.categoryRaw || 'other').toLowerCase(),
    confidence: Number.isFinite(Number(item.confidence)) ? Number(item.confidence) : 0,
  }));
  renderItems(reviewDraft, ui.reviewItems, true);
  if (ui.confirm) {
    ui.confirm.disabled = reviewDraft.length === 0;
    ui.confirm.textContent = reviewDraft.length === 1 ? 'Add 1 item' : `Add ${reviewDraft.length} items`;
  }
  setPanels('review');
}

function render() {
  if (ACTIVE_PHASES.has(state.phase)) {
    renderRunning();
    return;
  }
  if (state.phase === 'finished') {
    enterReview();
    return;
  }
  if (TERMINAL[state.phase]) {
    renderTerminal(state.phase);
    return;
  }
  setPanels('none');
}

/* --------------------------------------------------------- frame merge -- */

/**
 * Applies whatever fields a frame happens to carry.
 *
 * The SSE event names are an assumption; the field names are the same ones the
 * snapshot endpoint uses. Merging field by field means a differently named
 * event still updates the screen correctly, and an unrecognised frame is a
 * no-op rather than a crash.
 */
function applyFrame(data) {
  if (!data || typeof data !== 'object') return;

  if (Array.isArray(data)) {
    state.items = data;
    return;
  }

  if (typeof data.phase === 'string') state.phase = data.phase;
  if (data.progress && typeof data.progress === 'object') state.progress = { ...state.progress, ...data.progress };
  else if (typeof data.stage === 'string') state.progress = { ...state.progress, ...data };

  if (Array.isArray(data.items)) state.items = data.items;
  else if (data.item && typeof data.item === 'object') state.items = state.items.concat([data.item]);
  else if (data.name && data.category !== undefined) state.items = state.items.concat([data]);

  if (Number.isFinite(Number(data.inferenceFailures))) state.inferenceFailures = Number(data.inferenceFailures);
  if (typeof data.abortReason === 'string') state.abortReason = data.abortReason;
  if (typeof data.abortDetail === 'string') state.abortDetail = data.abortDetail;
  else if (typeof data.detail === 'string' && data.abortReason) state.abortDetail = data.detail;

  if (Number.isFinite(Number(data.callBudget)) && Number(data.callBudget) > 0) state.callBudget = Number(data.callBudget);

  if (Number.isFinite(Number(data.callsUsed))) {
    const used = Number(data.callsUsed);
    if (used !== state.callsUsed) {
      state.callsUsed = used;
      state.callStartedAt = Date.now(); // a new call started; the per-call clock restarts
    }
  }
}

/* --------------------------------------------------------------- flow --- */

function startTicker() {
  stopTicker();
  ticker = setInterval(renderClock, 1000);
}

function stopTicker() {
  clearInterval(ticker);
  ticker = null;
}

function detach() {
  if (stream) {
    stream.stop();
    stream = null;
  }
  stopTicker();
}

function reset() {
  detach();
  state.scanID = null;
  state.phase = 'idle';
  state.items = [];
  state.inferenceFailures = 0;
  state.abortReason = '';
  state.abortDetail = '';
  state.callsUsed = 0;
  state.startedAt = 0;
  state.callStartedAt = 0;
  try {
    window.localStorage.removeItem(LAST_SCAN_KEY);
  } catch {
    /* private browsing */
  }
  setPanels('none');
}

async function attach(scanID, opts = {}) {
  if (!scanID) return;
  detach();

  state.scanID = scanID;
  state.items = [];
  state.callsUsed = 0;
  state.startedAt = Date.now();
  state.callStartedAt = 0;
  state.phase = 'scanning';
  state.streamStatus = 'live';

  try {
    window.localStorage.setItem(LAST_SCAN_KEY, scanID);
  } catch {
    /* private browsing */
  }

  // The snapshot first, so a reattach shows everything found so far rather than
  // an empty list until the next event happens to arrive.
  try {
    const snapshot = await api.scan(scanID);
    if (snapshot.startedAt) {
      const began = Date.parse(snapshot.startedAt);
      if (Number.isFinite(began)) state.startedAt = began;
    }
    applyFrame(snapshot);
  } catch (err) {
    emit('dd:toast', { message: humanMessage(err), kind: 'error' });
  }

  render();
  startTicker();

  if (!ACTIVE_PHASES.has(state.phase)) {
    stopTicker();
    return;
  }

  stream = new EventStream(api.scanEventsPath(scanID), {
    events: ['snapshot', 'progress', 'item', 'items', 'phase', 'done'],
    onEvent: (type, data) => {
      applyFrame(data);
      render();
      if (!ACTIVE_PHASES.has(state.phase)) {
        detach();
        emit('dd:scan-complete', { phase: state.phase, items: state.items.length });
      }
    },
    onStatus: (s) => {
      state.streamStatus = s;
      if (ACTIVE_PHASES.has(state.phase)) renderRunning();
    },
    poll: () => api.scan(scanID),
    pollEveryMs: 5000,
  });
  stream.start();

  if (opts.attached) {
    emit('dd:toast', { message: 'A scan was already running on this service, so this page joined it.' });
  }
}

async function beginScan(photoIDs) {
  try {
    const out = await api.startScan(photoIDs);
    await attach(out.scanID, { attached: out.attached });
  } catch (err) {
    emit('dd:toast', { message: humanMessage(err), kind: 'error' });
  }
}

async function cancel() {
  if (!state.scanID) return;
  ui.cancel.disabled = true;
  try {
    await api.cancelScan(state.scanID);
    // The phase still arrives over the stream; this is only the fast path so
    // the button does not sit there looking ignored for a whole call.
    state.phase = 'cancelled';
    detach();
    render();
  } catch (err) {
    emit('dd:toast', { message: humanMessage(err), kind: 'error' });
  } finally {
    ui.cancel.disabled = false;
  }
}

async function confirm() {
  if (!state.scanID || !reviewDraft.length) return;
  ui.confirm.disabled = true;
  try {
    const out = (await api.confirmScan(state.scanID, reviewDraft)) || {};
    const inserted = Number(out.inserted) || 0;
    const incremented = Number(out.incremented) || 0;
    const parts = [];
    if (inserted) parts.push(`${inserted} new`);
    if (incremented) parts.push(`${incremented} topped up`);
    emit('dd:toast', { message: parts.length ? `Saved: ${parts.join(', ')}.` : 'Saved.' });
    emit('dd:inventory-changed', {});
    emit('dd:scan-finished', {});
    reset();
  } catch (err) {
    emit('dd:toast', { message: humanMessage(err), kind: 'error' });
  } finally {
    ui.confirm.disabled = false;
  }
}

function discard() {
  emit('dd:scan-finished', {});
  reset();
}

/* --------------------------------------------------------------- init --- */

async function reattachOnLoad(status) {
  if (state.scanID) return;

  const active = status && (status.activeScanID || status.activeScanId);
  if (active) {
    await attach(active, { attached: true });
    return;
  }

  let remembered = null;
  try {
    remembered = window.localStorage.getItem(LAST_SCAN_KEY);
  } catch {
    remembered = null;
  }
  if (!remembered) return;

  try {
    const snapshot = await api.scan(remembered);
    applyFrame(snapshot);
    state.scanID = remembered;
    if (ACTIVE_PHASES.has(state.phase)) {
      await attach(remembered, {});
    } else {
      render();
    }
  } catch {
    // The scan is gone from the registry, which is normal after a restart or a
    // sweep. Forget it quietly.
    reset();
  }
}

function init() {
  if (!ui.panel) return;

  if (ui.cancel) ui.cancel.addEventListener('click', cancel);
  if (ui.confirm) ui.confirm.addEventListener('click', confirm);
  if (ui.discard) ui.discard.addEventListener('click', discard);

  on('dd:photos-ready', (e) => beginScan((e.detail && e.detail.photoIDs) || []));

  on('dd:status', (e) => {
    const status = e.detail;
    if (!status) return;
    if (Number(status.scanCallBudget) > 0) state.callBudget = Number(status.scanCallBudget);
    // The gate on showing an ETA at all: a number AND a claim that it was
    // measured on hardware like this one.
    state.estimateMeasured = status.scanEstimateMeasured === true;
    state.estimateSeconds = Number(status.scanEstimateSeconds) || 0;
    reattachOnLoad(status);
  });

  setPanels('none');
}

init();
