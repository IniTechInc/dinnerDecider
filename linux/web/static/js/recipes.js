/**
 * recipes.js - two buckets from one model call.
 *
 * Generating recipes is a model call like any other. On this hardware it queues
 * behind a running scan rather than being refused, because sessions nest and
 * one short text-only call is exactly what the inference gate exists to let
 * through. So the button says what it is going to cost in time.
 *
 * The failure string is the service's, not this file's. It is shown as sent.
 */

import { api, emit, on, humanMessage } from './api.js';

const el = (id) => document.getElementById(id);

const ui = {
  panel: el('recipes-panel'),
  generate: el('recipes-generate'),
  hint: el('recipes-hint'),
  error: el('recipes-error'),
  makeNowWrap: el('recipes-makenow-wrap'),
  makeNow: el('recipes-makenow'),
  almostWrap: el('recipes-almost-wrap'),
  almost: el('recipes-almost'),
};

let running = false;

function showError(message) {
  if (!ui.error) return;
  ui.error.hidden = !message;
  ui.error.textContent = message || '';
}

function ingredientList(recipe) {
  const list = document.createElement('ul');
  list.className = 'card__list';
  for (const line of recipe.ingredients || []) {
    const li = document.createElement('li');
    li.textContent = line.name || '';
    if (!line.hasIt) li.className = 'missing';
    list.appendChild(li);
  }
  return list;
}

function card(recipe, bucket) {
  const li = document.createElement('li');
  li.className = 'card';

  const main = document.createElement('div');
  main.className = 'card__main';

  const title = document.createElement('p');
  title.className = 'card__title card__title--recipe';
  title.textContent = recipe.name || 'Untitled';
  main.appendChild(title);

  const meta = document.createElement('p');
  meta.className = 'card__meta';
  const bits = [];
  if (Number(recipe.timeMinutes) > 0) bits.push(`${recipe.timeMinutes} min`);
  const missing = recipe.missingItems || [];
  if (missing.length) bits.push(`missing ${missing.join(', ')}`);
  meta.textContent = bits.join(' | ');
  main.appendChild(meta);

  main.appendChild(ingredientList(recipe));

  if ((recipe.steps || []).length) {
    const steps = document.createElement('details');
    const summary = document.createElement('summary');
    summary.className = 'staples__summary';
    summary.textContent = 'Steps';
    steps.appendChild(summary);
    const ol = document.createElement('ol');
    ol.className = 'card__list';
    for (const step of recipe.steps) {
      const item = document.createElement('li');
      item.textContent = step;
      ol.appendChild(item);
    }
    steps.appendChild(ol);
    main.appendChild(steps);
  }

  li.appendChild(main);

  const actions = document.createElement('div');
  actions.className = 'card__actions';

  if (bucket === 'makeNow') {
    const cook = document.createElement('button');
    cook.type = 'button';
    cook.className = 'btn btn--ghost';
    cook.textContent = 'Cooked it';
    cook.addEventListener('click', async () => {
      cook.disabled = true;
      try {
        await api.cook((recipe.ingredients || []).map((i) => i.name).filter(Boolean));
        emit('dd:inventory-changed', {});
        emit('dd:toast', { message: `Took the ingredients for ${recipe.name} out of your inventory.` });
      } catch (err) {
        showError(humanMessage(err));
      } finally {
        cook.disabled = false;
      }
    });
    actions.appendChild(cook);
  } else if (missing.length) {
    const shop = document.createElement('button');
    shop.type = 'button';
    shop.className = 'btn btn--ghost';
    shop.textContent = 'Add missing';
    shop.addEventListener('click', async () => {
      shop.disabled = true;
      try {
        await api.addShoppingItems(missing);
        emit('dd:shopping-changed', {});
        emit('dd:toast', { message: `Added ${missing.length} to the shopping list.` });
      } catch (err) {
        showError(humanMessage(err));
      } finally {
        shop.disabled = false;
      }
    });
    actions.appendChild(shop);
  }

  li.appendChild(actions);
  return li;
}

function renderBucket(list, into, wrap, bucket) {
  if (!into || !wrap) return;
  into.textContent = '';
  for (const recipe of list) into.appendChild(card(recipe, bucket));
  wrap.hidden = list.length === 0;
}

async function generate() {
  if (running) return;
  running = true;
  showError('');
  if (ui.hint) ui.hint.hidden = false;
  ui.generate.disabled = true;
  ui.generate.textContent = 'Thinking...';

  try {
    const bundle = await api.recipes();
    // The service sends its own failure sentence when the model answered but
    // the reply could not be read. Show it as written.
    if (bundle.error) {
      showError(bundle.error);
    } else {
      renderBucket(bundle.makeNow || [], ui.makeNow, ui.makeNowWrap, 'makeNow');
      renderBucket(bundle.almostThere || [], ui.almost, ui.almostWrap, 'almostThere');
      if (!(bundle.makeNow || []).length && !(bundle.almostThere || []).length) {
        showError('No ideas came back this time. Adding a few more items to your inventory usually helps.');
      }
    }
  } catch (err) {
    showError(humanMessage(err));
  } finally {
    running = false;
    ui.generate.disabled = false;
    ui.generate.textContent = 'Suggest dinners';
    if (ui.hint) ui.hint.hidden = true;
  }
}

function init() {
  if (!ui.panel || !ui.generate) return;
  ui.generate.addEventListener('click', generate);

  // A scan is running, so a recipe call would queue behind it for minutes. Say
  // so rather than letting the button look broken.
  on('dd:scan-phase', (e) => {
    const active = Boolean(e.detail && e.detail.active);
    ui.generate.disabled = active || running;
    ui.generate.textContent = active ? 'Waiting for the scan to finish' : 'Suggest dinners';
  });
}

init();
