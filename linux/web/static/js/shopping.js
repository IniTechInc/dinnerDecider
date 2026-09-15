/**
 * shopping.js - the list, and nothing clever.
 *
 * Dedupe and first-seen casing are the service's job. This file sends names and
 * re-reads the list; it never merges locally, because doing so in two places is
 * how the two copies drift.
 */

import { api, on, emit, humanMessage } from './api.js';

const el = (id) => document.getElementById(id);

const ui = {
  panel: el('shopping-panel'),
  form: el('shopping-add-form'),
  name: el('shopping-add-name'),
  error: el('shopping-error'),
  items: el('shopping-items'),
  empty: el('shopping-empty'),
  clearChecked: el('shopping-clear-checked'),
};

let items = [];
let loaded = false;

function showError(message) {
  if (!ui.error) return;
  ui.error.hidden = !message;
  ui.error.textContent = message || '';
}

function card(item) {
  const li = document.createElement('li');
  li.className = item.isChecked ? 'card card--checked' : 'card';

  const check = document.createElement('input');
  check.type = 'checkbox';
  check.className = 'card__check';
  check.checked = Boolean(item.isChecked);
  check.setAttribute('aria-label', `Check off ${item.name}`);
  check.addEventListener('change', () => toggle(item, check.checked));
  li.appendChild(check);

  const main = document.createElement('div');
  main.className = 'card__main';
  const title = document.createElement('p');
  title.className = 'card__title';
  title.textContent = item.name || '';
  main.appendChild(title);
  li.appendChild(main);

  const actions = document.createElement('div');
  actions.className = 'card__actions';
  const remove = document.createElement('button');
  remove.type = 'button';
  remove.className = 'btn btn--quiet';
  remove.textContent = 'Delete';
  remove.setAttribute('aria-label', `Delete ${item.name}`);
  remove.addEventListener('click', () => removeItem(item));
  actions.appendChild(remove);
  li.appendChild(actions);

  return li;
}

function render() {
  if (!ui.items) return;
  ui.items.textContent = '';
  for (const item of items) ui.items.appendChild(card(item));
  if (ui.empty) ui.empty.hidden = !loaded || items.length > 0;
  if (ui.clearChecked) ui.clearChecked.hidden = !items.some((i) => i.isChecked);
}

async function refresh() {
  try {
    items = await api.shopping();
    loaded = true;
    showError('');
  } catch (err) {
    if (err && err.status === 401) return;
    showError(humanMessage(err));
  }
  render();
}

async function toggle(item, checked) {
  try {
    await api.updateShoppingItem(item.id, { isChecked: checked });
    await refresh();
  } catch (err) {
    showError(humanMessage(err));
  }
}

async function removeItem(item) {
  try {
    await api.deleteShoppingItem(item.id);
    await refresh();
  } catch (err) {
    showError(humanMessage(err));
  }
}

async function add(event) {
  event.preventDefault();
  const name = ui.name ? ui.name.value.trim() : '';
  if (!name) return;
  try {
    await api.addShoppingItems([name]);
    if (ui.name) ui.name.value = '';
    await refresh();
    emit('dd:shopping-changed', {});
  } catch (err) {
    showError(humanMessage(err));
  }
}

async function clearChecked() {
  const checked = items.filter((i) => i.isChecked);
  try {
    for (const item of checked) await api.deleteShoppingItem(item.id);
    await refresh();
  } catch (err) {
    showError(humanMessage(err));
  }
}

function init() {
  if (!ui.panel) return;
  if (ui.form) ui.form.addEventListener('submit', add);
  if (ui.clearChecked) ui.clearChecked.addEventListener('click', clearChecked);

  on('dd:shopping-changed', () => refresh());
  on('dd:paired', () => refresh());
  on('dd:view', (e) => {
    if (e.detail && e.detail.view === 'recipes' && !loaded) refresh();
  });

  refresh();
}

init();
