/**
 * api.js - the only module that talks to the service.
 *
 * ASSUMED WIRE SHAPES
 * -------------------
 * The route table is fixed (implementation plan, Step 55). The response bodies
 * are not written down anywhere, so this file assumes the shapes below and
 * reads every field defensively: where a name could plausibly have been spelled
 * two ways, both are accepted, and a missing field degrades the UI rather than
 * throwing. Anything that turns out wrong is a one-line fix here, not a change
 * spread across six feature modules.
 *
 *   GET  /api/status
 *     { modelState, demoMode, demoReason, degraded, ocrMode, hwTarget,
 *       runtimeVariant, runtimeFallback, activeScanID, scanCallBudget,
 *       memory: { totalBytes, availableBytes, requiredBytes },
 *       versions: { service, runtime, model },
 *       scanEstimateSeconds, scanEstimateMeasured }
 *     scanEstimateMeasured is the gate on showing an ETA at all. Absent or
 *     false means elapsed-only, which is the correct default on hardware
 *     nobody has benchmarked.
 *
 *   POST /api/pair                 { token }                 -> 204 or { ok }
 *   POST /api/photos               multipart, field "photos" -> { photos: [{ id }] }
 *   DELETE /api/photos/{id}                                  -> 204
 *   POST /api/scan                 { photoIDs: [id] }        -> { scanID }
 *        409 -> { error: "scan_in_progress", activeScanID }
 *        429 -> { error: "too_many_scans" }
 *   GET  /api/scan/{id}
 *     { scanID, phase, progress: { photoIndex, photoCount, stage,
 *       itemsDone, itemsTotal }, items: [IdentifiedItem], inferenceFailures,
 *       abortReason, abortDetail, callsUsed, callBudget }
 *     phase is one of idle | scanning | finished | empty | failed | cancelled.
 *   GET    /api/scan/{id}/events   SSE, every frame carries an `id:`
 *   DELETE /api/scan/{id}                                    -> 204
 *   POST   /api/scan/{id}/confirm  { items: [...] }          -> { inserted, incremented }
 *   GET/POST /api/inventory, PATCH/DELETE /api/inventory/{id}
 *   POST   /api/cook               { ingredientNames: [] }
 *   POST   /api/recipes            -> { makeNow, almostThere, hasGeneratedRecipes }
 *   GET/POST /api/shopping, PATCH/DELETE /api/shopping/{id}
 *   GET    /api/shopping/export    text/plain
 *   GET/PUT /api/settings, GET/PUT /api/taste-profile
 *   GET    /api/model, POST /api/model/{start|pause|resume|retry}
 *   GET    /api/model/events       SSE
 *   GET    /api/kroger/status      -> { configured: false } when dormant
 *
 * SSE RESUME: a reconnect made by this file cannot set a Last-Event-ID header,
 * because EventSource does not allow one. It therefore carries the last id in a
 * `lastEventId` query parameter as well, and the server is expected to honour
 * either. The browser's own automatic reconnect still uses the header.
 */

const BASE = '/api';

/* ------------------------------------------------------------- events --- */

/**
 * A document-level bus. Modules never import each other; they announce.
 * Keeping the coupling here means no import cycles between feature files.
 */
export function emit(name, detail) {
  document.dispatchEvent(new CustomEvent(name, { detail }));
}

export function on(name, handler) {
  document.addEventListener(name, handler);
  return () => document.removeEventListener(name, handler);
}

/* -------------------------------------------------------------- error --- */

export class ApiError extends Error {
  constructor(status, payload, fallback) {
    const code = (payload && (payload.error || payload.code)) || '';
    super((payload && payload.message) || fallback || code || `HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.payload = payload || null;
    this.retryAfter = 0;
  }
}

/**
 * Turns any failure into something a person can act on. The 429 and 503 cases
 * are the ones the service actually produces under load, and "Error: 429" tells
 * a user on a phone in a kitchen nothing at all.
 */
export function humanMessage(err) {
  if (!err) return 'Something went wrong.';
  if (err.name === 'AbortError') return 'Cancelled.';
  if (err instanceof TypeError) {
    return 'Cannot reach the DinnerDecider service. Check that it is running and that this device is on the same network.';
  }
  if (!(err instanceof ApiError)) return err.message || 'Something went wrong.';

  const wait = err.retryAfter > 0 ? ` Try again in about ${err.retryAfter} seconds.` : '';
  switch (err.status) {
    case 401:
      return 'This browser is not paired with the service yet.';
    case 403:
      return 'This browser is not allowed to do that.';
    case 404:
      return 'That is no longer there.';
    case 413:
      return err.message || 'That photo is too large for the service to accept.';
    case 429:
      return `The service is already busy and will not start another job yet.${wait || ' Wait for the running scan to finish, or cancel it.'}`;
    case 503:
      return `The service is at capacity right now.${wait || ' It will free up shortly.'}`;
    default:
      return err.message || `The service returned an error (${err.status}).`;
  }
}

/* ------------------------------------------------------------ request --- */

async function parseBody(res) {
  const type = res.headers.get('Content-Type') || '';
  if (type.includes('json')) {
    try {
      return await res.json();
    } catch {
      return null;
    }
  }
  const text = await res.text();
  return text ? { message: text.slice(0, 400) } : null;
}

async function request(method, path, options = {}) {
  const { body, signal, headers = {}, expect = 'json' } = options;
  const init = { method, signal, credentials: 'same-origin', headers: { Accept: 'application/json', ...headers } };

  if (body instanceof FormData) {
    init.body = body; // let the browser set the multipart boundary
  } else if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }

  const res = await fetch(BASE + path, init);

  if (res.status === 204 || res.status === 205) return null;

  const payload = await parseBody(res);

  if (!res.ok) {
    const err = new ApiError(res.status, payload);
    const retry = Number(res.headers.get('Retry-After'));
    if (Number.isFinite(retry) && retry > 0) err.retryAfter = Math.round(retry);
    if (res.status === 401) emit('dd:unauthorized', { path });
    throw err;
  }

  return expect === 'text' ? payload : payload;
}

const get = (path, opts) => request('GET', path, opts);
const post = (path, body, opts) => request('POST', path, { ...opts, body });
const patch = (path, body, opts) => request('PATCH', path, { ...opts, body });
const put = (path, body, opts) => request('PUT', path, { ...opts, body });
const del = (path, opts) => request('DELETE', path, opts);

/* --------------------------------------------------------- endpoints ---- */

const pick = (obj, ...names) => {
  for (const n of names) {
    if (obj && obj[n] !== undefined && obj[n] !== null) return obj[n];
  }
  return undefined;
};

export const api = {
  /* status and pairing */

  async status(opts) {
    return (await get('/status', opts)) || {};
  },

  /**
   * Exchanges a printed pairing token for a session cookie. Resolves true on
   * success, false on a rejected token, and throws on anything else, so the
   * dialog can tell "wrong token" apart from "service is down".
   */
  async pair(token) {
    try {
      await post('/pair', { token: String(token || '').trim() });
      return true;
    } catch (err) {
      if (err instanceof ApiError && (err.status === 401 || err.status === 403)) return false;
      throw err;
    }
  },

  /* photos and scanning */

  async uploadPhotos(blobs, opts = {}) {
    const form = new FormData();
    blobs.forEach((blob, i) => {
      const name = blob.name || `photo-${i + 1}.jpg`;
      form.append('photos', blob, name);
    });
    const out = (await post('/photos', form, opts)) || {};
    const photos = pick(out, 'photos', 'items') || [];
    return photos.map((p) => (typeof p === 'string' ? { id: p } : { ...p, id: pick(p, 'id', 'photoID', 'photoId') }));
  },

  deletePhoto(id, opts) {
    return del(`/photos/${encodeURIComponent(id)}`, opts);
  },

  /**
   * Starts a scan, or attaches to the one already running.
   *
   * A 409 is not an error here. The service returns the running scan's id
   * precisely so a second phone, or the same phone in a second tab, joins the
   * scan in progress instead of being told off. Resolves
   * { scanID, attached } where attached distinguishes the two.
   */
  async startScan(photoIDs, opts) {
    try {
      const out = (await post('/scan', { photoIDs }, opts)) || {};
      return { scanID: pick(out, 'scanID', 'scanId', 'id'), attached: false };
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        const active = pick(err.payload || {}, 'activeScanID', 'activeScanId', 'scanID', 'scanId');
        if (active) return { scanID: active, attached: true };
      }
      throw err;
    }
  },

  async scan(id, opts) {
    return (await get(`/scan/${encodeURIComponent(id)}`, opts)) || {};
  },

  cancelScan(id, opts) {
    return del(`/scan/${encodeURIComponent(id)}`, opts);
  },

  confirmScan(id, items, opts) {
    return post(`/scan/${encodeURIComponent(id)}/confirm`, { items }, opts);
  },

  scanEventsPath(id) {
    return `${BASE}/scan/${encodeURIComponent(id)}/events`;
  },

  /* inventory */

  async inventory(opts) {
    const out = await get('/inventory', opts);
    return normaliseList(out);
  },

  addInventoryItem(item, opts) {
    return post('/inventory', item, opts);
  },

  updateInventoryItem(id, changes, opts) {
    return patch(`/inventory/${encodeURIComponent(id)}`, changes, opts);
  },

  deleteInventoryItem(id, opts) {
    return del(`/inventory/${encodeURIComponent(id)}`, opts);
  },

  cook(ingredientNames, opts) {
    return post('/cook', { ingredientNames }, opts);
  },

  /* recipes */

  async recipes(opts) {
    return (await post('/recipes', undefined, opts)) || {};
  },

  /* shopping */

  async shopping(opts) {
    const out = await get('/shopping', opts);
    return normaliseList(out);
  },

  addShoppingItems(names, opts) {
    return post('/shopping', { names }, opts);
  },

  updateShoppingItem(id, changes, opts) {
    return patch(`/shopping/${encodeURIComponent(id)}`, changes, opts);
  },

  deleteShoppingItem(id, opts) {
    return del(`/shopping/${encodeURIComponent(id)}`, opts);
  },

  /* settings */

  async settings(opts) {
    return (await get('/settings', opts)) || {};
  },

  saveSettings(values, opts) {
    return put('/settings', values, opts);
  },

  async tasteProfile(opts) {
    return (await get('/taste-profile', opts)) || {};
  },

  saveTasteProfile(profile, opts) {
    return put('/taste-profile', profile, opts);
  },

  /* model */

  async model(opts) {
    return (await get('/model', opts)) || {};
  },

  modelAction(action, opts) {
    return post(`/model/${action}`, undefined, opts);
  },

  modelEventsPath() {
    return `${BASE}/model/events`;
  },

  /* kroger */

  async krogerStatus(opts) {
    try {
      return (await get('/kroger/status', opts)) || {};
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return { configured: false };
      throw err;
    }
  },
};

function normaliseList(out) {
  if (Array.isArray(out)) return out;
  if (out && Array.isArray(out.items)) return out.items;
  return [];
}

/* ---------------------------------------------------------------- SSE --- */

const BACKOFF_MS = [1000, 2000, 4000, 8000, 15000];
const POLL_AFTER_MS = 30000;

/**
 * An SSE subscription that survives a phone locking, a tab closing, a Wi-Fi
 * handover, and a home router that buffers.
 *
 * Reconnects with backoff, resumes from the last event id it saw, and if the
 * stream will not come back within POLL_AFTER_MS it falls back to polling the
 * snapshot endpoint. The scan itself is owned by the service, not by this
 * connection, so losing the stream is a display problem and never a data one.
 */
export class EventStream {
  /**
   * @param {string} path       absolute path on this origin
   * @param {object} config
   *   events      - named SSE event types to listen for, besides "message"
   *   onEvent     - (type, data, rawEvent) for every frame
   *   onOpen      - called on each successful (re)connection
   *   onStatus    - ("live" | "retrying" | "polling") for the UI
   *   poll        - async () => snapshot, used when the stream will not hold
   *   pollEveryMs - polling cadence, default 4000
   */
  constructor(path, config = {}) {
    this.path = path;
    this.config = config;
    this.source = null;
    this.lastEventId = '';
    this.attempt = 0;
    this.stopped = true;
    this.timer = null;
    this.pollTimer = null;
    this.downSince = 0;
  }

  start() {
    if (!this.stopped) return;
    this.stopped = false;
    this.attempt = 0;
    this.downSince = 0;
    this.#open();
  }

  stop() {
    this.stopped = true;
    this.#closeSource();
    clearTimeout(this.timer);
    clearInterval(this.pollTimer);
    this.timer = null;
    this.pollTimer = null;
  }

  #closeSource() {
    if (this.source) {
      this.source.close();
      this.source = null;
    }
  }

  #url() {
    if (!this.lastEventId) return this.path;
    const sep = this.path.includes('?') ? '&' : '?';
    return `${this.path}${sep}lastEventId=${encodeURIComponent(this.lastEventId)}`;
  }

  #open() {
    if (this.stopped) return;
    this.#closeSource();

    let source;
    try {
      source = new EventSource(this.#url(), { withCredentials: true });
    } catch {
      this.#scheduleRetry();
      return;
    }
    this.source = source;

    const handle = (type) => (event) => {
      if (event.lastEventId) this.lastEventId = event.lastEventId;
      let data = event.data;
      if (typeof data === 'string' && data.length && (data[0] === '{' || data[0] === '[')) {
        try {
          data = JSON.parse(data);
        } catch {
          /* leave it as text; a malformed frame must not kill the stream */
        }
      }
      if (this.config.onEvent) this.config.onEvent(type, data, event);
    };

    source.addEventListener('message', handle('message'));
    for (const type of this.config.events || []) {
      source.addEventListener(type, handle(type));
    }

    source.addEventListener('open', () => {
      this.attempt = 0;
      this.downSince = 0;
      this.#stopPolling();
      if (this.config.onStatus) this.config.onStatus('live');
      if (this.config.onOpen) this.config.onOpen();
    });

    source.addEventListener('error', () => {
      if (this.stopped) return;
      // EventSource retries on its own, but it would do so without our
      // lastEventId and without any backoff, which is the wrong behaviour
      // against a 503 subscriber cap. Take the reconnect over ourselves.
      this.#closeSource();
      if (!this.downSince) this.downSince = Date.now();
      if (this.config.onStatus) this.config.onStatus('retrying');
      if (Date.now() - this.downSince >= POLL_AFTER_MS) this.#startPolling();
      this.#scheduleRetry();
    });
  }

  #scheduleRetry() {
    clearTimeout(this.timer);
    const base = BACKOFF_MS[Math.min(this.attempt, BACKOFF_MS.length - 1)];
    this.attempt += 1;
    const jitter = Math.floor(Math.random() * 400);
    this.timer = setTimeout(() => this.#open(), base + jitter);
  }

  #startPolling() {
    if (this.pollTimer || !this.config.poll) return;
    if (this.config.onStatus) this.config.onStatus('polling');
    const every = this.config.pollEveryMs || 4000;
    const tick = async () => {
      try {
        const snapshot = await this.config.poll();
        if (snapshot && this.config.onEvent) this.config.onEvent('snapshot', snapshot, null);
      } catch {
        /* the next tick tries again */
      }
    };
    tick();
    this.pollTimer = setInterval(tick, every);
  }

  #stopPolling() {
    clearInterval(this.pollTimer);
    this.pollTimer = null;
  }
}
