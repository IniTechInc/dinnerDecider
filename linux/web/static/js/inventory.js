/**
 * inventory.js - what is actually on the shelves.
 *
 * The merge key is the service's business, not this file's: quantity changes go
 * back as a PATCH and the list is re-read. The browser never re-derives a plan
 * the service already applied.
 */

import { api, emit, on, humanMessage, CATEGORIES, categoryLabel } from './api.js';

const el = (id) => document.getElementById(id);

const ui = {
  panel: el('inventory-panel'),
  form: el('inventory-add-form'),
  name: el('inventory-add-name'),
  category: el('inventory-add-category'),
  sort: el('inventory-sort'),
  count: el('inventory-count'),
  error: el('inventory-error'),
  items: el('inventory-items'),
  empty: el('inventory-empty'),
  staplesWrap: el('inventory-staples-wrap'),
  staplesCount: el('inventory-staples-count'),
  staples: el('inventory-staples'),
};

let items = [];
let loaded = false;

function showError(message) {
  if (!ui.error) return;
  ui.error.hidden = !message;
  ui.error.textContent = message || '';
}

function sorted(list) {
  const mode = ui.sort ? ui.sort.value : 'name';
  const copy = list.slice();
  if (mode === 'category') {
    copy.sort((a, b) => {
      const c = String(a.categoryRaw || '').localeCompare(String(b.categoryRaw || ''));
      return c !== 0 ? c : String(a.name || '').localeCompare(String(b.name || ''));
    });
  } else if (mode === 'dateAdded') {
    copy.sort((a, b) => Date.parse(b.dateAdded || 0) - Date.parse(a.dateAdded || 0));
  } else {
    copy.sort((a, b) => String(a.name || '').localeCompare(String(b.name || '')));
  }
  return copy;
}

function card(item) {
  const li = document.createElement('li');
  li.className = 'card';

  const main = document.createElement('div');
  main.className = 'card__main';

  const title = document.createElement('p');
  title.className = 'card__title';
  title.textContent = item.name || 'Unnamed';
  main.appendChild(title);

  const meta = document.createElement('p');
  meta.className = 'card__meta';
  const bits = [categoryLabel(item.categoryRaw || item.category)];
  if (item.brand) bits.push(item.brand);
  meta.textContent = bits.join(' | ');
  main.appendChild(meta);

  li.appendChild(main);

  const actions = document.createElement('div');
  actions.className = 'card__actions';

  const stepper = document.createElement('div');
  stepper.className = 'stepper';

  const minus = document.createElement('button');
  minus.type = 'button';
  minus.textContent = '-';
  minus.setAttribute('aria-label', `One fewer ${item.name}`);
  minus.addEventListener('click', () => changeQuantity(item, -1));

  const value = document.createElement('span');
  value.className = 'stepper__value';
  value.textContent = String(item.quantity != null ? item.quantity : 1);

  const plus = document.createElement('button');
  plus.type = 'button';
  plus.textContent = '+';
  plus.setAttribute('aria-label', `One more ${item.name}`);
  plus.addEventListener('click', () => changeQuantity(item, 1));

  stepper.append(minus, value, plus);
  actions.appendChild(stepper);

  const staple = document.createElement('button');
  staple.type = 'button';
  staple.className = 'btn btn--quiet';
  staple.textContent = item.isStaple ? 'Unstaple' : 'Staple';
  staple.setAttribute('aria-label', item.isStaple ? `Stop treating ${item.name} as a staple` : `Treat ${item.name} as a staple`);
  staple.addEventListener('click', () => patchItem(item, { isStaple: !item.isStaple }));
  actions.appendChild(staple);

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

  const main = sorted(items.filter((i) => !i.isStaple));
  const staples = sorted(items.filter((i) => i.isStaple));

  ui.items.textContent = '';
  for (const item of main) ui.items.appendChild(card(item));

  if (ui.staples) {
    ui.staples.textContent = '';
    for (const item of staples) ui.staples.appendChild(card(item));
  }
  if (ui.staplesWrap) ui.staplesWrap.hidden = staples.length === 0;
  if (ui.staplesCount) ui.staplesCount.textContent = staples.length ? `(${staples.length})` : '';

  if (ui.empty) ui.empty.hidden = !loaded || items.length > 0;
  if (ui.count) {
    const total = items.reduce((sum, i) => sum + (Number(i.quantity) || 0), 0);
    ui.count.textContent = items.length ? `${items.length} rows, ${total} on hand` : '';
  }
}

async function refresh() {
  try {
    items = await api.inventory();
    loaded = true;
    showError('');
  } catch (err) {
    if (err && err.status === 401) return; // the shell opens the pairing dialog
    showError(humanMessage(err));
  }
  render();
}

async function changeQuantity(item, delta) {
  const next = Math.max(0, (Number(item.quantity) || 0) + delta);
  if (next === 0) {
    await removeItem(item);
    return;
  }
  await patchItem(item, { quantity: next });
}

async function patchItem(item, changes) {
  try {
    await api.updateInventoryItem(item.id, changes);
    await refresh();
    emit('dd:inventory-changed', {});
  } catch (err) {
    showError(humanMessage(err));
  }
}

async function removeItem(item) {
  try {
    await api.deleteInventoryItem(item.id);
    await refresh();
    emit('dd:inventory-changed', {});
  } catch (err) {
    showError(humanMessage(err));
  }
}

async function add(event) {
  event.preventDefault();
  const name = ui.name ? ui.name.value.trim() : '';
  if (!name) return;
  try {
    await api.addInventoryItem({
      name,
      brand: null,
      categoryRaw: ui.category ? ui.category.value : 'other',
      quantity: 1,
      isStaple: false,
    });
    if (ui.name) ui.name.value = '';
    await refresh();
    emit('dd:inventory-changed', {});
  } catch (err) {
    showError(humanMessage(err));
  }
}

function init() {
  if (!ui.panel) return;

  if (ui.category) {
    for (const c of CATEGORIES) {
      const option = document.createElement('option');
      option.value = c.raw;
      option.textContent = c.label;
      if (c.raw === 'other') option.selected = true;
      ui.category.appendChild(option);
    }
  }

  if (ui.form) ui.form.addEventListener('submit', add);
  if (ui.sort) ui.sort.addEventListener('change', render);

  on('dd:inventory-changed', () => refresh());
  on('dd:paired', () => refresh());
  on('dd:view', (e) => {
    if (e.detail && e.detail.view === 'inventory' && !loaded) refresh();
  });

  refresh();
}

init();
