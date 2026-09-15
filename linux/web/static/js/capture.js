/**
 * capture.js - getting photos off the phone and into the service.
 *
 * THE FALLBACK IS THE PRIMARY PATH, and that is not a consolation prize.
 *
 * `getUserMedia` is gated to secure contexts. A Raspberry Pi serving plain HTTP
 * on a LAN address is not one, and never will be, so the live preview is the
 * path that cannot be relied on. A plain file input with capture="environment"
 * opens the phone's own camera app on both iOS and Android, is not gated by
 * secure context at all, and works over plain HTTP on a LAN IP. It is the
 * default here and most people should never see anything else.
 *
 * Both paths converge on one transcode: decode, bake the EXIF orientation into
 * the pixels, downscale to 2016px on the long edge, and re-encode as JPEG at
 * 0.9. That is what makes an iPhone HEIC work without a single server-side
 * dependency (Safari decodes HEIC natively, the canvas emits JPEG), and it cuts
 * roughly ten times the bytes off a slow LAN upload.
 */

import { api, emit, on, humanMessage } from './api.js';

/* Mirrors internal/api/limits.go. Checked here so the user gets an instant
   local error instead of waiting out an upload to be told 413. */
const MAX_FILES = 8;
const MAX_FILE_BYTES = 12 * 1024 * 1024;
const MAX_TOTAL_BYTES = 48 * 1024 * 1024;
const MAX_PIXELS = 40 * 1000 * 1000;
const MAX_DIMENSION = 20000;

/* A guard on the source file, before we ask a phone to decode it. The service
   never sees this many bytes; the transcode below is what it receives. */
const MAX_SOURCE_BYTES = 80 * 1024 * 1024;

const CAPTURE_LONG_EDGE = 2016;
const JPEG_QUALITY = 0.9;

/* ======================================================================
   PATH 1 - the native camera app. Always available, no TLS, plain HTTP.
   Nothing in this section may depend on a secure context.
   ====================================================================== */

/**
 * Builds the file input that opens the camera.
 *
 * `capture="environment"` asks for the rear camera. On iOS Safari and Android
 * Chrome this launches the system camera UI directly. On a desktop browser the
 * attribute is ignored and the user gets a file picker, which is the right
 * behaviour there.
 *
 * @param {boolean} wantsCamera true for the camera button, false for the
 *   library button. When `capture` is present most platforms return exactly one
 *   photo, so the multi-select batch lives on the library input.
 */
export function createCameraInput(wantsCamera) {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = 'image/*';
  input.multiple = true;
  if (wantsCamera) {
    input.setAttribute('capture', 'environment');
  }
  input.hidden = true;
  return input;
}

/* ======================================================================
   The shared transcode. Both paths run exactly this.
   ====================================================================== */

async function decodeOriented(source) {
  // createImageBitmap with imageOrientation "from-image" bakes the EXIF
  // rotation into the bitmap. Without it a photo taken in portrait arrives
  // sideways, the tile grid samples the wrong regions of the image, and the
  // model confidently describes whatever happens to be there. That is a silent
  // wrong-answer bug, not a cosmetic one.
  if (typeof createImageBitmap === 'function') {
    try {
      return await createImageBitmap(source, { imageOrientation: 'from-image' });
    } catch {
      /* older Safari rejects the options bag; fall through */
    }
  }
  return decodeViaElement(source);
}

function decodeViaElement(blob) {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(blob);
    const img = new Image();
    img.decoding = 'sync';
    img.onload = () => {
      URL.revokeObjectURL(url);
      // Browsers apply image-orientation: from-image to <img> by default, so
      // naturalWidth and naturalHeight are already the oriented dimensions and
      // drawImage paints the oriented pixels.
      resolve(img);
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error('This file is not an image this browser can open.'));
    };
    img.src = url;
  });
}

function dimensionsOf(decoded) {
  const w = decoded.width || decoded.naturalWidth || 0;
  const h = decoded.height || decoded.naturalHeight || 0;
  return { w, h };
}

function canvasToBlob(canvas, type, quality) {
  return new Promise((resolve, reject) => {
    if (!canvas.toBlob) {
      reject(new Error('This browser cannot re-encode the photo.'));
      return;
    }
    canvas.toBlob((blob) => {
      if (blob) resolve(blob);
      else reject(new Error('This browser could not re-encode the photo.'));
    }, type, quality);
  });
}

/**
 * Decode, orient, downscale to the long edge, re-encode as JPEG.
 * Returns { blob, width, height } or throws with a message worth showing.
 */
export async function transcode(source, label) {
  if (source.size && source.size > MAX_SOURCE_BYTES) {
    throw new Error(`${label} is ${formatBytes(source.size)}, which is too large to open on this device.`);
  }

  const decoded = await decodeOriented(source);
  const { w, h } = dimensionsOf(decoded);

  if (!w || !h) {
    throw new Error(`${label} could not be read as an image.`);
  }
  if (w > MAX_DIMENSION || h > MAX_DIMENSION) {
    throw new Error(`${label} is ${w} by ${h}, and the service refuses any side over ${MAX_DIMENSION} pixels.`);
  }
  if (w * h > MAX_PIXELS) {
    throw new Error(`${label} is ${Math.round((w * h) / 1e6)} megapixels, and the service accepts at most ${MAX_PIXELS / 1e6}.`);
  }

  const scale = Math.min(1, CAPTURE_LONG_EDGE / Math.max(w, h));
  const targetW = Math.max(1, Math.round(w * scale));
  const targetH = Math.max(1, Math.round(h * scale));

  // Successive halving before the final draw. A single drawImage across a large
  // reduction aliases badly, and the model is looking at small objects.
  let surface = decoded;
  let curW = w;
  let curH = h;
  while (curW / 2 > targetW && curH / 2 > targetH) {
    const half = document.createElement('canvas');
    half.width = Math.max(1, Math.floor(curW / 2));
    half.height = Math.max(1, Math.floor(curH / 2));
    half.getContext('2d').drawImage(surface, 0, 0, half.width, half.height);
    if (surface !== decoded && surface.width) releaseCanvas(surface);
    surface = half;
    curW = half.width;
    curH = half.height;
  }

  const canvas = document.createElement('canvas');
  canvas.width = targetW;
  canvas.height = targetH;
  const ctx = canvas.getContext('2d');
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = 'high';
  ctx.drawImage(surface, 0, 0, targetW, targetH);

  if (typeof decoded.close === 'function') decoded.close();

  const blob = await canvasToBlob(canvas, 'image/jpeg', JPEG_QUALITY);
  releaseCanvas(canvas);

  if (blob.size > MAX_FILE_BYTES) {
    throw new Error(`${label} is still ${formatBytes(blob.size)} after shrinking, and the per-photo limit is ${formatBytes(MAX_FILE_BYTES)}.`);
  }

  return { blob, width: targetW, height: targetH };
}

function releaseCanvas(canvas) {
  canvas.width = 0;
  canvas.height = 0;
}

function formatBytes(n) {
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(n / 1024))} KB`;
}

/* ======================================================================
   PATH 2 - live preview. Progressive enhancement only.
   Everything below is gated on window.isSecureContext, which a LAN address
   over plain HTTP will never satisfy. Nothing above depends on it.
   ====================================================================== */

function livePreviewAvailable() {
  return Boolean(
    window.isSecureContext &&
      navigator.mediaDevices &&
      typeof navigator.mediaDevices.getUserMedia === 'function'
  );
}

/* ======================================================================
   Wiring.
   ====================================================================== */

const el = (id) => document.getElementById(id);

const ui = {
  panel: el('capture-panel'),
  take: el('capture-take'),
  choose: el('capture-choose'),
  live: el('capture-live'),
  hint: el('capture-hint'),
  error: el('capture-error'),
  thumbs: el('capture-thumbs'),
  submit: el('capture-submit'),
  scan: el('capture-scan'),
  clear: el('capture-clear'),
  livePanel: el('capture-live-panel'),
  video: el('capture-video'),
  shoot: el('capture-shoot'),
  liveClose: el('capture-live-close'),
};

const selection = [];
let stream = null;
let seq = 0;

function showError(message) {
  if (!ui.error) return;
  if (!message) {
    ui.error.hidden = true;
    ui.error.textContent = '';
    return;
  }
  ui.error.hidden = false;
  ui.error.textContent = message;
}

function totalBytes() {
  return selection.reduce((sum, p) => sum + p.blob.size, 0);
}

function renderThumbs() {
  if (!ui.thumbs) return;
  ui.thumbs.textContent = '';
  for (const photo of selection) {
    const li = document.createElement('li');
    li.className = 'thumb';

    const img = document.createElement('img');
    img.src = photo.url;
    img.alt = photo.name;
    li.appendChild(img);

    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'thumb__remove';
    remove.textContent = 'x';
    remove.setAttribute('aria-label', `Remove ${photo.name}`);
    remove.addEventListener('click', () => {
      const i = selection.findIndex((p) => p.id === photo.id);
      if (i >= 0) {
        URL.revokeObjectURL(selection[i].url);
        selection.splice(i, 1);
        renderThumbs();
      }
    });
    li.appendChild(remove);

    ui.thumbs.appendChild(li);
  }

  if (ui.submit) ui.submit.hidden = selection.length === 0;
  if (ui.scan) {
    ui.scan.textContent = selection.length === 1 ? 'Scan this photo' : `Scan ${selection.length} photos`;
  }
}

function clearSelection() {
  for (const photo of selection) URL.revokeObjectURL(photo.url);
  selection.length = 0;
  renderThumbs();
  showError('');
}

async function accept(files) {
  showError('');
  const problems = [];

  for (const file of Array.from(files)) {
    if (selection.length >= MAX_FILES) {
      problems.push(`Only ${MAX_FILES} photos can go in one scan.`);
      break;
    }
    const label = file.name || 'That photo';
    try {
      const out = await transcode(file, label);
      if (totalBytes() + out.blob.size > MAX_TOTAL_BYTES) {
        problems.push(`Adding ${label} would push this batch past the ${formatBytes(MAX_TOTAL_BYTES)} upload limit.`);
        continue;
      }
      seq += 1;
      selection.push({
        id: `p${seq}`,
        name: label,
        blob: out.blob,
        url: URL.createObjectURL(out.blob),
      });
    } catch (err) {
      problems.push(err.message || `${label} could not be used.`);
    }
  }

  renderThumbs();
  if (problems.length) showError(problems.join(' '));
}

function bindInput(wantsCamera, button) {
  if (!button) return;
  const input = createCameraInput(wantsCamera);
  document.body.appendChild(input);
  input.addEventListener('change', async () => {
    const files = input.files;
    if (files && files.length) await accept(files);
    input.value = '';
  });
  button.addEventListener('click', () => input.click());
}

async function openLive() {
  showError('');
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      video: { facingMode: { ideal: 'environment' }, width: { ideal: 2016 }, height: { ideal: 1512 } },
      audio: false,
    });
  } catch (err) {
    showError('This browser would not open the camera. Use the photo button instead, which does not need one.');
    return;
  }
  if (ui.video) {
    ui.video.srcObject = stream;
    await ui.video.play().catch(() => {});
  }
  if (ui.livePanel) ui.livePanel.hidden = false;
}

function closeLive() {
  if (stream) {
    for (const track of stream.getTracks()) track.stop();
    stream = null;
  }
  if (ui.video) ui.video.srcObject = null;
  if (ui.livePanel) ui.livePanel.hidden = true;
}

async function shoot() {
  if (!ui.video || !ui.video.videoWidth) return;
  const frame = document.createElement('canvas');
  frame.width = ui.video.videoWidth;
  frame.height = ui.video.videoHeight;
  frame.getContext('2d').drawImage(ui.video, 0, 0);
  try {
    // A live frame carries no EXIF, so the shared transcode simply downscales
    // and re-encodes it. Same code path, same output shape.
    const blob = await canvasToBlob(frame, 'image/jpeg', JPEG_QUALITY);
    releaseCanvas(frame);
    await accept([new File([blob], `camera-${Date.now()}.jpg`, { type: 'image/jpeg' })]);
  } catch (err) {
    showError(err.message || 'That frame could not be saved.');
  }
}

async function submit() {
  if (!selection.length) return;
  if (totalBytes() > MAX_TOTAL_BYTES) {
    showError(`This batch is ${formatBytes(totalBytes())}, over the ${formatBytes(MAX_TOTAL_BYTES)} upload limit. Remove a photo.`);
    return;
  }

  ui.scan.disabled = true;
  ui.scan.textContent = 'Uploading...';
  try {
    const files = selection.map((p, i) => new File([p.blob], `photo-${i + 1}.jpg`, { type: 'image/jpeg' }));
    const uploaded = await api.uploadPhotos(files);
    const ids = uploaded.map((p) => p.id).filter(Boolean);
    if (!ids.length) throw new Error('The service accepted the photos but returned no ids.');
    emit('dd:photos-ready', { photoIDs: ids, count: ids.length });
  } catch (err) {
    showError(humanMessage(err));
  } finally {
    ui.scan.disabled = false;
    renderThumbs();
  }
}

function init() {
  if (!ui.panel) return;

  bindInput(true, ui.take);
  bindInput(false, ui.choose);

  if (ui.clear) ui.clear.addEventListener('click', clearSelection);
  if (ui.scan) ui.scan.addEventListener('click', submit);

  if (livePreviewAvailable()) {
    if (ui.live) {
      ui.live.hidden = false;
      ui.live.addEventListener('click', openLive);
    }
    if (ui.shoot) ui.shoot.addEventListener('click', shoot);
    if (ui.liveClose) ui.liveClose.addEventListener('click', closeLive);
  } else if (ui.hint) {
    ui.hint.textContent =
      'The camera button opens your phone camera app, which works over plain HTTP. A live preview inside the page would need a secure page, which a LAN address is not.';
  }

  // The capture controls get out of the way while a scan is running, and the
  // selection is only thrown away once its items are safely in the inventory.
  on('dd:scan-phase', (e) => {
    const active = Boolean(e.detail && e.detail.active);
    ui.panel.hidden = active;
    if (active) closeLive();
  });
  on('dd:scan-finished', () => {
    clearSelection();
  });

  renderThumbs();
}

init();
