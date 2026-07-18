#!/usr/bin/env python3
"""DinnerDecider human-in-the-loop labeling tool.

Keyboard-first web tool for verifying model pre-labels on grocery/receipt crops.
Python 3.13 stdlib only. Serves a single self-contained page on port 50188.

Run:  python3 serve.py [--include-synth]
Then open http://localhost:50188
"""

import argparse
import json
import os
import sys
import threading
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs, unquote

PORT = 50188
HERE = os.path.dirname(os.path.abspath(__file__))
DATASET = os.path.abspath(os.path.join(HERE, "..", "dataset"))
REAL_JSONL = os.path.join(DATASET, "real", "crops.jsonl")
SYNTH_JSONL = os.path.join(DATASET, "synth", "crops.jsonl")
VERIFIED_JSONL = os.path.join(DATASET, "labels_verified.jsonl")

CATEGORIES = ["produce", "dairy", "meat", "pantry", "snack",
              "beverage", "condiment", "frozen", "other"]

NEGATIVE_INTERVAL = 8  # interleave a negative every N real items

_write_lock = threading.Lock()

CONTENT_TYPES = {
    ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
    ".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
}


def now_iso():
    return datetime.now(timezone.utc).isoformat()


def read_jsonl(path):
    rows = []
    if not os.path.exists(path):
        return rows
    with open(path, "r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError:
                continue  # tolerate a half-written appended line mid-scan
    return rows


def load_crops(include_synth):
    """Rescan JSONL sources fresh each call so newly appended crops appear."""
    crops = []
    for row in read_jsonl(REAL_JSONL):
        crops.append(row)
    if include_synth:
        for row in read_jsonl(SYNTH_JSONL):
            crops.append(row)
    return crops


def load_journal():
    """Journal is the source of truth for what's been verified."""
    return read_jsonl(VERIFIED_JSONL)


def build_queue(include_synth):
    """Ordered list of pending crops.

    Real unlabeled crops sorted by prelabel confidence ascending (most
    uncertain first). Negatives interleaved every ~8 items. Synth (pre-trusted)
    only appears if include_synth and appended at the end.
    """
    crops = load_crops(include_synth)
    journal = load_journal()
    done_ids = {e["id"] for e in journal}

    def conf(c):
        pl = c.get("prelabel") or {}
        v = pl.get("confidence")
        return v if isinstance(v, (int, float)) else 1.0

    real = [c for c in crops if c.get("source") == "real" and c["id"] not in done_ids]
    real.sort(key=conf)
    negatives = [c for c in crops if c.get("source") == "negative" and c["id"] not in done_ids]
    synth = [c for c in crops if c.get("source") == "synth" and c["id"] not in done_ids]

    queue = []
    neg_i = 0
    for i, c in enumerate(real):
        queue.append(c)
        if negatives and (i + 1) % NEGATIVE_INTERVAL == 0 and neg_i < len(negatives):
            queue.append(negatives[neg_i])
            neg_i += 1
    # any leftover negatives at the end
    while neg_i < len(negatives):
        queue.append(negatives[neg_i])
        neg_i += 1
    if include_synth:
        queue.extend(synth)
    return queue, crops, journal


def compute_stats(include_synth):
    crops = load_crops(include_synth)
    journal = load_journal()
    by_source = {}
    for c in crops:
        s = c.get("source", "unknown")
        by_source[s] = by_source.get(s, 0) + 1
    actions = {"accepted": 0, "corrected": 0, "rejected": 0}
    for e in journal:
        a = e.get("action")
        if a in actions:
            actions[a] += 1
    verified_ids = {e["id"] for e in journal}
    total = len(crops)
    done = len([c for c in crops if c["id"] in verified_ids])
    graded = actions["accepted"] + actions["corrected"]
    corr_rate = (actions["corrected"] / graded) if graded else 0.0
    return {
        "by_source": by_source,
        "actions": actions,
        "total": total,
        "done": done,
        "remaining": total - done,
        "correction_rate": round(corr_rate, 4),
    }


def append_verified(entry):
    with _write_lock:
        with open(VERIFIED_JSONL, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(entry, ensure_ascii=False) + "\n")
            fh.flush()
            os.fsync(fh.fileno())


def undo_last():
    """Remove the most recent journal entry so its crop re-enters the queue."""
    with _write_lock:
        rows = load_journal()
        if not rows:
            return None
        removed = rows[-1]
        rows = rows[:-1]
        tmp = VERIFIED_JSONL + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            for r in rows:
                fh.write(json.dumps(r, ensure_ascii=False) + "\n")
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, VERIFIED_JSONL)
        return removed


class Handler(BaseHTTPRequestHandler):
    include_synth = False

    def log_message(self, *args):
        pass  # quiet

    def _send_json(self, obj, code=200):
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_bytes(self, body, ctype, code=200):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        if path == "/" or path == "/index.html":
            self._send_bytes(PAGE.encode("utf-8"), "text/html; charset=utf-8")
            return
        if path == "/api/queue":
            queue, crops, journal = build_queue(self.include_synth)
            self._send_json({
                "items": queue,
                "stats": compute_stats(self.include_synth),
                "categories": CATEGORIES,
            })
            return
        if path == "/api/stats":
            self._send_json(compute_stats(self.include_synth))
            return
        if path.startswith("/img/"):
            self._serve_image(unquote(path[len("/img/"):]))
            return
        self._send_json({"error": "not found"}, 404)

    def _serve_image(self, rel):
        # rel is relative to DATASET; guard against traversal
        target = os.path.abspath(os.path.join(DATASET, rel))
        if not target.startswith(DATASET + os.sep):
            self._send_json({"error": "forbidden"}, 403)
            return
        if not os.path.exists(target):
            self._send_json({"error": "missing"}, 404)
            return
        ext = os.path.splitext(target)[1].lower()
        ctype = CONTENT_TYPES.get(ext, "application/octet-stream")
        with open(target, "rb") as fh:
            self._send_bytes(fh.read(), ctype)

    def do_POST(self):
        parsed = urlparse(self.path)
        path = parsed.path
        length = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(length) if length else b""
        try:
            data = json.loads(raw.decode("utf-8")) if raw else {}
        except json.JSONDecodeError:
            self._send_json({"error": "bad json"}, 400)
            return

        if path == "/api/label":
            cid = data.get("id")
            label = data.get("label")
            action = data.get("action")
            if not cid or action not in ("accepted", "corrected", "rejected"):
                self._send_json({"error": "invalid payload"}, 400)
                return
            entry = {
                "id": cid,
                "label": label,
                "verified_at": now_iso(),
                "action": action,
            }
            append_verified(entry)
            self._send_json({"ok": True, "entry": entry})
            return

        if path == "/api/undo":
            removed = undo_last()
            self._send_json({"ok": removed is not None, "removed": removed})
            return

        self._send_json({"error": "not found"}, 404)


# ------------------------------------------------------------------ HTML PAGE

PAGE = r"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>DinnerDecider Labeler</title>
<style>
:root{
  --cream:#FFF6E8; --cream2:#FAF8F5; --card:#FFFFFF; --line:#EFEAE2;
  --ink:#1F1D1B; --muted:#6E625B; --terra:#E4573D; --terra2:#D64B31;
  --gold:#F2B23C; --sage:#7FA27A; --sageBg:#E8F0E6;
}
@media (prefers-color-scheme: dark){
  :root{
    --cream:#1F1D1B; --cream2:#2A2724; --card:#2B2320; --line:#3a332e;
    --ink:#F5EFE6; --muted:#CFC7BD; --terra:#E4573D; --terra2:#F2B23C;
    --gold:#F2C46E; --sage:#A9C4A5; --sageBg:#2f3a2d;
  }
}
*{box-sizing:border-box}
html,body{margin:0;height:100%}
body{
  background:var(--cream); color:var(--ink);
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
  -webkit-font-smoothing:antialiased;
}
.wrap{max-width:760px;margin:0 auto;padding:18px 20px 60px;}
header{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:14px;}
.brand{font-weight:800;font-size:18px;letter-spacing:-.2px;}
.brand .dot{color:var(--terra);}
.saved{font-size:13px;color:var(--sage);opacity:0;transition:opacity .18s;font-weight:600;}
.saved.show{opacity:1;}
.progress{height:8px;border-radius:6px;background:var(--line);overflow:hidden;margin:4px 0 6px;}
.progress > i{display:block;height:100%;background:var(--terra);width:0%;transition:width .25s;}
.metaline{display:flex;justify-content:space-between;font-size:12.5px;color:var(--muted);margin-bottom:14px;}
.card{
  background:var(--card);border:1px solid var(--line);border-radius:18px;
  padding:18px;box-shadow:0 1px 0 rgba(0,0,0,.03),0 10px 30px rgba(31,29,27,.05);
}
.imgbox{
  width:100%;background:var(--cream2);border:1px solid var(--line);border-radius:14px;
  display:flex;align-items:center;justify-content:center;min-height:200px;max-height:340px;
  overflow:hidden;margin-bottom:14px;
}
.imgbox img{max-width:100%;max-height:340px;object-fit:contain;display:block;}
.photoname{font-size:12px;color:var(--muted);margin:-6px 0 12px;}
.receipt{
  font-family:"Courier New",ui-monospace,Menlo,monospace;
  background:repeating-linear-gradient(var(--cream2),var(--cream2) 27px,var(--line) 27px,var(--line) 28px);
  border:1px dashed var(--line);border-radius:10px;padding:12px 14px;margin-bottom:16px;
  font-size:14px;line-height:28px;white-space:pre-wrap;color:var(--ink);max-height:180px;overflow:auto;
}
.receipt .lbl{color:var(--muted);font-size:11px;letter-spacing:1px;text-transform:uppercase;}
.srcpill{display:inline-block;font-size:11px;font-weight:700;padding:2px 9px;border-radius:20px;
  background:var(--sageBg);color:var(--sage);margin-left:8px;vertical-align:middle;text-transform:uppercase;letter-spacing:.5px;}
.srcpill.negative{background:rgba(228,87,61,.12);color:var(--terra);}
.fields{display:grid;grid-template-columns:1fr 1fr;gap:12px;}
.field{display:flex;flex-direction:column;gap:5px;}
.field.full{grid-column:1 / -1;}
label.k{font-size:11.5px;font-weight:700;color:var(--muted);text-transform:uppercase;letter-spacing:.6px;}
input,select{
  font:inherit;font-size:16px;padding:10px 12px;border:1.5px solid var(--line);
  border-radius:10px;background:var(--cream2);color:var(--ink);width:100%;
}
input:focus,select:focus{outline:none;border-color:var(--terra);}
.confrow{display:flex;align-items:center;gap:10px;}
.confrow input[type=range]{accent-color:var(--terra);}
.confval{font-variant-numeric:tabular-nums;font-weight:700;min-width:38px;text-align:right;}
.catgrid{display:grid;grid-template-columns:repeat(3,1fr);gap:7px;margin-top:6px;grid-column:1/-1;}
.catbtn{font:inherit;font-size:13px;font-weight:600;padding:9px 6px;border-radius:9px;border:1.5px solid var(--line);
  background:var(--cream2);color:var(--ink);cursor:pointer;display:flex;align-items:center;gap:6px;justify-content:center;}
.catbtn .num{font-size:11px;color:var(--muted);font-weight:800;}
.catbtn.active{background:var(--terra);border-color:var(--terra);color:#fff;}
.catbtn.active .num{color:rgba(255,255,255,.8);}
.actions{display:flex;gap:10px;margin-top:16px;flex-wrap:wrap;}
button.primary{background:var(--terra);color:#fff;border:none;font-weight:700;font-size:15px;
  padding:12px 20px;border-radius:11px;cursor:pointer;flex:1;min-width:120px;}
button.primary:hover{background:var(--terra2);}
button.ghost{background:transparent;color:var(--muted);border:1.5px solid var(--line);font-weight:600;
  padding:12px 16px;border-radius:11px;cursor:pointer;}
button.ghost:hover{border-color:var(--terra);color:var(--terra);}
.cheat{margin-top:20px;background:var(--card);border:1px solid var(--line);border-radius:14px;padding:12px 14px;}
.cheat h3{margin:0 0 8px;font-size:12px;text-transform:uppercase;letter-spacing:1px;color:var(--muted);}
.keys{display:flex;flex-wrap:wrap;gap:7px;}
.keys span{font-size:12.5px;color:var(--ink);background:var(--cream2);border:1px solid var(--line);
  padding:4px 8px;border-radius:7px;}
.keys kbd{font-family:ui-monospace,Menlo,monospace;font-weight:700;color:var(--terra);
  background:var(--card);border:1px solid var(--line);border-radius:5px;padding:0 5px;margin-right:5px;}
.statspanel{margin-top:16px;font-size:12.5px;color:var(--muted);display:flex;flex-wrap:wrap;gap:14px;}
.statspanel b{color:var(--ink);}
.empty{text-align:center;padding:50px 20px;color:var(--muted);}
.empty h2{color:var(--ink);}
</style>
</head>
<body>
<div class="wrap">
  <header>
    <div class="brand">Dinner<span class="dot">.</span>Decider Labeler</div>
    <div class="saved" id="saved">saved &#10003;</div>
  </header>
  <div class="progress"><i id="bar"></i></div>
  <div class="metaline">
    <span id="prog">loading...</span>
    <span id="rate"></span>
  </div>

  <div id="main"></div>

  <div class="cheat">
    <h3>Keyboard</h3>
    <div class="keys">
      <span><kbd>Enter</kbd>accept</span>
      <span><kbd>type</kbd>edit name</span>
      <span><kbd>Tab</kbd>next field</span>
      <span><kbd>1-9</kbd>category</span>
      <span><kbd>N</kbd>not food</span>
      <span><kbd>S</kbd>skip</span>
      <span><kbd>U</kbd>undo last</span>
    </div>
  </div>

  <div class="statspanel" id="stats"></div>
</div>

<script>
const CATS = ["produce","dairy","meat","pantry","snack","beverage","condiment","frozen","other"];
let queue = [], idx = 0, cur = null, stats = null;
let sessionCount = 0, sessionStart = Date.now();

function fmtPct(n){ return Math.round(n*100)+"%"; }

async function loadQueue(keepId){
  const r = await fetch("/api/queue");
  const d = await r.json();
  queue = d.items || [];
  stats = d.stats;
  if(keepId){
    const i = queue.findIndex(x=>x.id===keepId);
    idx = i>=0 ? i : 0;
  } else {
    idx = 0;
  }
  render();
}

function render(){
  const main = document.getElementById("main");
  renderStats();
  updateProgress();
  if(idx >= queue.length){
    cur = null;
    main.innerHTML = '<div class="empty"><h2>All caught up &#127869;</h2>'+
      '<p>No more crops to verify right now. New crops appear automatically as they are extracted.</p>'+
      '<button class="ghost" onclick="loadQueue()">Rescan for new crops</button></div>';
    return;
  }
  cur = queue[idx];
  const pl = cur.prelabel || {};
  const src = cur.source || "real";
  const conf = (typeof pl.confidence==="number")?pl.confidence:0.5;
  const pill = src!=="real" ? '<span class="srcpill '+(src==='negative'?'negative':'')+'">'+src+'</span>' : '';
  const imgPath = cur.image ? ('/img/'+encodeURI(cur.image)) : '';
  const photo = cur.image ? cur.image.split('/').pop() : 'unknown';
  main.innerHTML = `
    <div class="card">
      <div class="imgbox">${imgPath?`<img src="${imgPath}" alt="crop">`:'<span style="color:var(--muted)">no image</span>'}${''}</div>
      <div class="photoname">photo: ${photo} ${pill}</div>
      <div class="receipt"><span class="lbl">OCR</span>\n${escapeHtml(cur.ocr||'(no OCR text)')}</div>
      <div class="fields">
        <div class="field full">
          <label class="k" for="f_name">Name</label>
          <input id="f_name" value="${escapeAttr(pl.name||'')}" autocomplete="off">
        </div>
        <div class="field full">
          <label class="k" for="f_brand">Brand</label>
          <input id="f_brand" value="${escapeAttr(pl.brand||'')}" autocomplete="off">
        </div>
        <div class="catgrid" id="catgrid"></div>
        <div class="field full">
          <label class="k">Confidence <span class="confval" id="confval">${fmtPct(conf)}</span></label>
          <div class="confrow">
            <input type="range" id="f_conf" min="0" max="1" step="0.05" value="${conf}">
          </div>
        </div>
      </div>
      <div class="actions">
        <button class="primary" id="btnAccept">Accept &#9166;</button>
        <button class="ghost" id="btnSkip">Skip (S)</button>
        <button class="ghost" id="btnNot">Not food (N)</button>
        <button class="ghost" id="btnUndo">Undo (U)</button>
      </div>
    </div>`;
  buildCats(pl.category);
  document.getElementById("f_conf").addEventListener("input",e=>{
    document.getElementById("confval").textContent = fmtPct(parseFloat(e.target.value));
  });
  document.getElementById("btnAccept").onclick = ()=>submit();
  document.getElementById("btnSkip").onclick = ()=>skip();
  document.getElementById("btnNot").onclick = ()=>notFood();
  document.getElementById("btnUndo").onclick = ()=>undo();
  // focus name at END so typing edits (per spec: "typing immediately edits name")
  const nm = document.getElementById("f_name");
  nm.focus(); nm.setSelectionRange(nm.value.length, nm.value.length);
}

let selectedCat = null;
function buildCats(active){
  selectedCat = active || null;
  const g = document.getElementById("catgrid");
  g.innerHTML = "";
  CATS.forEach((c,i)=>{
    const b = document.createElement("button");
    b.type="button"; b.className="catbtn"+(c===selectedCat?" active":"");
    b.innerHTML = `<span class="num">${i+1}</span>${c}`;
    b.onclick = ()=>{ setCat(c); };
    g.appendChild(b);
  });
}
function setCat(c){
  selectedCat = c;
  [...document.querySelectorAll(".catbtn")].forEach(b=>{
    b.classList.toggle("active", b.textContent.replace(/^\d+/,'')===c);
  });
}

function currentLabel(){
  return {
    name: (document.getElementById("f_name").value||"").trim(),
    brand: (document.getElementById("f_brand").value||"").trim(),
    category: selectedCat || "other",
    confidence: parseFloat(document.getElementById("f_conf").value),
  };
}

function isCorrected(label){
  const pl = cur.prelabel || {};
  return (label.name!==(pl.name||"").trim()) ||
         (label.brand!==(pl.brand||"").trim()) ||
         (label.category!==(pl.category||"other")) ||
         (Math.abs(label.confidence-((typeof pl.confidence==="number")?pl.confidence:0.5))>0.001);
}

async function post(url,body){
  const r = await fetch(url,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body||{})});
  return r.json();
}

function showSaved(){
  const s=document.getElementById("saved");
  s.classList.add("show");
  setTimeout(()=>s.classList.remove("show"),700);
}

async function submit(){
  if(!cur) return;
  const label = currentLabel();
  const action = isCorrected(label) ? "corrected" : "accepted";
  await post("/api/label",{id:cur.id,label,action});
  afterAction();
}
async function skip(){
  if(!cur) return;
  // skip = advance without recording (leaves it in queue for later rescan)
  idx++;
  if(idx>=queue.length){ await loadQueue(); } else { render(); }
}
async function notFood(){
  if(!cur) return;
  const label = {name:"unknown",brand:"",category:"other",confidence:0.1};
  await post("/api/label",{id:cur.id,label,action:"rejected"});
  afterAction();
}
async function undo(){
  const d = await post("/api/undo",{});
  if(d.ok && d.removed){
    await loadQueue(d.removed.id);
  }
}
function afterAction(){
  showSaved();
  sessionCount++;
  idx++;
  refreshStats();
  if(idx>=queue.length){ loadQueue(); } else { render(); }
}

async function refreshStats(){
  const r = await fetch("/api/stats"); stats = await r.json(); renderStats(); updateProgress();
}
function renderStats(){
  if(!stats) return;
  const s=stats; const bs=s.by_source||{};
  const parts = Object.keys(bs).map(k=>`${k}: <b>${bs[k]}</b>`).join(" &middot; ");
  document.getElementById("stats").innerHTML =
    `<span>sources &rarr; ${parts||'none'}</span>`+
    `<span>accepted <b>${s.actions.accepted}</b> / corrected <b>${s.actions.corrected}</b> / rejected <b>${s.actions.rejected}</b></span>`+
    `<span>correction rate <b>${fmtPct(s.correction_rate)}</b></span>`;
}
function updateProgress(){
  if(!stats) return;
  const pct = stats.total ? (stats.done/stats.total*100) : 0;
  document.getElementById("bar").style.width = pct+"%";
  document.getElementById("prog").textContent = `${stats.done} / ${stats.total} verified`;
  const mins = (Date.now()-sessionStart)/60000;
  const rate = mins>0.05 ? (sessionCount/mins) : 0;
  document.getElementById("rate").textContent = sessionCount? `${rate.toFixed(1)}/min this session` : "";
}

function escapeHtml(s){return (s||"").replace(/[&<>]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));}
function escapeAttr(s){return (s||"").replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));}

document.addEventListener("keydown",(e)=>{
  if(!cur){ if(e.key.toLowerCase()==="u"){e.preventDefault();undo();} return; }
  const tag = document.activeElement && document.activeElement.tagName;
  const inField = tag==="INPUT"||tag==="SELECT";
  if(e.key==="Enter"){ e.preventDefault(); submit(); return; }
  // number quick-pick categories 1-9 (only when not typing into a text input)
  if(/^[1-9]$/.test(e.key)){
    const activeName = document.activeElement && document.activeElement.id;
    if(activeName!=="f_name" && activeName!=="f_brand"){
      e.preventDefault(); setCat(CATS[parseInt(e.key,10)-1]); return;
    }
  }
  if(!inField){
    const k=e.key.toLowerCase();
    if(k==="n"){e.preventDefault();notFood();return;}
    if(k==="s"){e.preventDefault();skip();return;}
    if(k==="u"){e.preventDefault();undo();return;}
  } else {
    // in name/brand field: still allow S/N/U via... no, typing edits. Only U with meta.
  }
});

loadQueue();
setInterval(refreshStats, 20000); // periodic rescan of stats
</script>
</body>
</html>
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--include-synth", action="store_true",
                    help="also surface pre-trusted synth crops for review")
    args = ap.parse_args()
    Handler.include_synth = args.include_synth

    os.makedirs(os.path.dirname(VERIFIED_JSONL), exist_ok=True)

    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print(f"DinnerDecider labeler on http://localhost:{PORT}  (include_synth={args.include_synth})")
    print(f"dataset: {DATASET}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        server.shutdown()


if __name__ == "__main__":
    main()
