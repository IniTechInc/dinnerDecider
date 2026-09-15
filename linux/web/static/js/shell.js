/**
 * shell.js - the frame the four views live in.
 *
 * Owns view switching, the pairing dialog and the toaster. Nothing here knows
 * anything about scanning, inventory or recipes; the feature modules announce
 * on the document bus and this file reacts.
 *
 * Navigation is real hash routing rather than a click handler, so the browser
 * back button works, a view can be linked to, and a reload lands where the
 * phone was left.
 */

import { api, emit, on, humanMessage } from './api.js';

const VIEWS = ['scan', 'inventory', 'recipes', 'settings'];
const DEFAULT_VIEW = 'scan';

const el = (id) => document.getElementById(id);

/* ------------------------------------------------------------- routing -- */

function currentView() {
  const raw = (window.location.hash || '').replace(/^#/, '');
  return VIEWS.includes(raw) ? raw : DEFAULT_VIEW;
}

function applyView(name) {
  for (const view of VIEWS) {
    const section = el(`view-${view}`);
    if (section) section.hidden = view !== name;
  }
  for (const tab of document.querySelectorAll('.tab')) {
    const isCurrent = tab.dataset.tab === name;
    if (isCurrent) tab.setAttribute('aria-current', 'page');
    else tab.removeAttribute('aria-current');
  }
  document.title = name === DEFAULT_VIEW ? 'DinnerDecider' : `DinnerDecider - ${name[0].toUpperCase()}${name.slice(1)}`;
  emit('dd:view', { view: name });
}

/* -------------------------------------------------------------- toasts -- */

let toastSeq = 0;

export function toast(message, kind) {
  const host = el('shell-toaster');
  if (!host || !message) return;
  toastSeq += 1;
  const node = document.createElement('div');
  node.className = 'toast';
  if (kind) node.dataset.kind = kind;
  node.textContent = message;
  host.appendChild(node);
  const life = kind === 'error' ? 7000 : 4000;
  setTimeout(() => node.remove(), life);
}

/* ------------------------------------------------------------- pairing -- */

let pairingOpen = false;

function openPairing() {
  const dialog = el('pair-dialog');
  if (!dialog || pairingOpen) return;
  pairingOpen = true;
  const err = el('pair-error');
  if (err) err.hidden = true;
  if (typeof dialog.showModal === 'function') dialog.showModal();
  else dialog.setAttribute('open', '');
  const field = el('pair-token');
  if (field) field.focus();
}

function closePairing() {
  const dialog = el('pair-dialog');
  pairingOpen = false;
  if (!dialog) return;
  if (typeof dialog.close === 'function') dialog.close();
  else dialog.removeAttribute('open');
}

async function submitPairing() {
  const field = el('pair-token');
  const err = el('pair-error');
  const token = field ? field.value.trim() : '';
  if (!token) return;

  try {
    const ok = await api.pair(token);
    if (!ok) {
      if (err) {
        err.hidden = false;
        err.textContent = 'That token was not accepted. Check the value the service printed when it started.';
      }
      return;
    }
    if (field) field.value = '';
    closePairing();
    toast('Paired. This browser will stay signed in.');
    emit('dd:paired', {});
  } catch (e) {
    if (err) {
      err.hidden = false;
      err.textContent = humanMessage(e);
    }
  }
}

/* ------------------------------------------------------ status plumbing -- */

async function refreshStatus() {
  try {
    const status = await api.status();
    const badge = el('shell-demo-badge');
    if (badge) badge.hidden = !status.demoMode;
    emit('dd:status', status);
    return status;
  } catch (err) {
    if (!err || err.status !== 401) {
      emit('dd:status', null);
    }
    return null;
  }
}

/* ---------------------------------------------------------------- init -- */

function init() {
  applyView(currentView());
  window.addEventListener('hashchange', () => applyView(currentView()));

  const pairButton = el('shell-pair-button');
  if (pairButton) pairButton.addEventListener('click', openPairing);

  const submit = el('pair-submit');
  if (submit) submit.addEventListener('click', submitPairing);

  const cancel = el('pair-cancel');
  if (cancel) cancel.addEventListener('click', closePairing);

  const form = el('pair-form');
  if (form) {
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      submitPairing();
    });
  }

  const dialog = el('pair-dialog');
  if (dialog) dialog.addEventListener('close', () => { pairingOpen = false; });

  // A 401 from anywhere in the app means this browser has not been paired.
  on('dd:unauthorized', () => openPairing());

  on('dd:toast', (e) => {
    const detail = e.detail || {};
    toast(detail.message, detail.kind);
  });

  // One status fetch feeds every module that cares, and it is refreshed when a
  // scan ends because the model state and the demo banner can both move.
  on('dd:paired', () => refreshStatus());
  on('dd:scan-finished', () => refreshStatus());
  on('dd:status-please', () => refreshStatus());

  refreshStatus();
}

init();
