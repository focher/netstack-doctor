const $ = (id) => document.getElementById(id);
const grid = $("grid");
let lastLayers = [];

const LAYER_META = [
  { layer: 1, name: "Physical", purpose: "Hardware links, interfaces, MTU" },
  { layer: 2, name: "Data Link", purpose: "MAC addressing, ARP, gateway L2" },
  { layer: 3, name: "Network", purpose: "IP routing, gateway, ICMP, path" },
  { layer: 4, name: "Transport", purpose: "TCP/UDP ports, handshakes, sockets" },
  { layer: 5, name: "Session", purpose: "Session setup, resumption, keep-alive" },
  { layer: 6, name: "Presentation", purpose: "TLS, certificates, encoding" },
  { layer: 7, name: "Application", purpose: "DNS, HTTP/HTTPS, app protocols" },
];

async function loadInfo() {
  try {
    const r = await fetch("/api/info");
    const info = await r.json();
    document.body.dataset.os = info.os;
    $("platform").textContent = `${info.osLabel} (${info.arch}) · ${info.hostname}`;
    $("hostline").textContent = `Diagnosing from ${info.hostname} on ${info.osLabel}`;
    $("target").value = info.defaults.target;
    $("dns").value = info.defaults.dns;
    if (info.ipv6Global) $("ipv6").checked = true;
  } catch (e) {
    $("platform").textContent = "ready";
  }
}

function statusWord(s) {
  return { green: "Healthy", yellow: "Warning", red: "Failed", gray: "Skipped" }[s] || s;
}

function skeleton() {
  grid.innerHTML = "";
  LAYER_META.forEach((m) => {
    const card = document.createElement("section");
    card.className = "card skeleton";
    card.innerHTML = `
      <div class="card-head">
        <div class="badge gray">${m.layer}</div>
        <div><h2>${m.name}</h2><div class="purpose">${m.purpose}</div></div>
        <div class="status-pill gray">testing…</div>
      </div>
      <ul class="tests">
        ${Array(3).fill('<li class="test"><span class="tdot" style="background:var(--gray)"></span><div class="tbody"><div class="shimmer" style="width:70%"></div><div class="shimmer" style="width:45%;margin-top:6px"></div></div></li>').join("")}
      </ul>`;
    grid.appendChild(card);
  });
}

function render(layers) {
  lastLayers = layers;
  grid.innerHTML = "";
  layers.sort((a, b) => a.layer - b.layer).forEach((L) => {
    const card = document.createElement("section");
    card.className = "card";
    const tests = L.tests.map((t, i) => `
      <li class="test" data-layer="${L.layer}" data-idx="${i}">
        <span class="tdot" style="background:var(--${t.status})"></span>
        <div class="tbody">
          <div class="tname">${escapeHtml(t.name)}</div>
          <div class="tsum">${escapeHtml(t.summary || "")}</div>
        </div>
        <span class="tdur">${t.durationMs}ms</span>
        <span class="chev">›</span>
      </li>`).join("");
    card.innerHTML = `
      <div class="card-head">
        <div class="badge ${L.status}">${L.layer}</div>
        <div><h2>${L.name}</h2><div class="purpose">${L.purpose}</div></div>
        <div class="status-pill ${L.status}">${statusWord(L.status)}</div>
      </div>
      <ul class="tests">${tests}</ul>`;
    grid.appendChild(card);
  });

  grid.querySelectorAll(".test").forEach((el) => {
    el.addEventListener("click", () => {
      const L = lastLayers.find((x) => x.layer == el.dataset.layer);
      openModal(L, L.tests[el.dataset.idx]);
    });
  });
}

function updateSummary(data) {
  let g = 0, y = 0, r = 0;
  data.layers.forEach((L) => L.tests.forEach((t) => {
    if (t.status === "green") g++; else if (t.status === "yellow") y++; else if (t.status === "red") r++;
  }));
  $("cnt-green").textContent = g;
  $("cnt-yellow").textContent = y;
  $("cnt-red").textContent = r;
  $("meta").textContent = `${data.config.target} · ran in ${(data.durationMs / 1000).toFixed(1)}s · ${new Date(data.ranAt).toLocaleTimeString()}`;
  $("summary").hidden = false;
}

function openModal(layer, test) {
  $("modal-title").textContent = test.name;
  $("modal-sub").innerHTML =
    `<span class="dot ${test.status}"></span> ${statusWord(test.status)} — ${escapeHtml(test.summary || "")} ` +
    `<span class="muted">· Layer ${layer.layer} ${layer.name} · ${test.durationMs}ms</span>`;
  $("modal-logs").textContent = (test.logs && test.logs.length ? test.logs.join("\n") : "No detailed log output captured.");
  $("modal").hidden = false;
}
$("modal-close").addEventListener("click", () => ($("modal").hidden = true));
$("modal").addEventListener("click", (e) => { if (e.target.id === "modal") $("modal").hidden = true; });
document.addEventListener("keydown", (e) => { if (e.key === "Escape") $("modal").hidden = true; });

async function run() {
  const btn = $("run");
  btn.disabled = true; btn.textContent = "Running…";
  $("empty") && ($("empty").style.display = "none");
  skeleton();
  try {
    const cfg = {
      ipv4: $("ipv4").checked,
      ipv6: $("ipv6").checked,
      target: $("target").value.trim(),
      dns: $("dns").value.trim(),
    };
    const r = await fetch("/api/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    });
    const data = await r.json();
    lastRunData = data;
    render(data.layers);
    updateSummary(data);
    updateAnalyzeButton();
  } catch (e) {
    grid.innerHTML = `<div class="empty"><p>Diagnostics failed: ${escapeHtml(String(e))}</p></div>`;
  } finally {
    btn.disabled = false; btn.textContent = "Run diagnostics";
  }
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// ---------- Local LLM (Ollama) ----------

let lastRunData = null;

function setLLMStatus(msg, cls) {
  const el = $("llmstatus");
  el.textContent = msg;
  el.className = "llmstatus" + (cls ? " " + cls : "");
}

async function detectModels() {
  const btn = $("detect");
  btn.disabled = true;
  setLLMStatus("Contacting " + $("llmhost").value.trim() + " …");
  try {
    const r = await fetch("/api/llm/models", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ host: $("llmhost").value.trim() }),
    });
    const data = await r.json();
    const sel = $("model");
    if (!data.ok) {
      sel.innerHTML = "<option>— no models detected —</option>";
      sel.disabled = true;
      setLLMStatus(data.error || "Could not reach Ollama", "err");
      updateAnalyzeButton();
      return;
    }
    if (!data.models.length) {
      sel.innerHTML = "<option>— no models installed —</option>";
      sel.disabled = true;
      setLLMStatus("Connected, but no models found. Try `ollama pull llama3`.", "err");
    } else {
      sel.innerHTML = data.models
        .map((m) => {
          const meta = [m.params, m.sizeGB ? m.sizeGB.toFixed(1) + " GB" : ""].filter(Boolean).join(", ");
          return `<option value="${escapeHtml(m.name)}">${escapeHtml(m.name)}${meta ? " (" + meta + ")" : ""}</option>`;
        })
        .join("");
      sel.disabled = false;
      setLLMStatus(`Found ${data.models.length} model(s) at ${data.endpoint}`, "ok");
    }
  } catch (e) {
    setLLMStatus("Request failed: " + e, "err");
  } finally {
    btn.disabled = false;
    updateAnalyzeButton();
  }
}

function updateAnalyzeButton() {
  const ready = lastRunData && !$("model").disabled;
  $("analyze").disabled = !ready;
}

async function analyze() {
  if (!lastRunData) return;
  const btn = $("analyze");
  btn.disabled = true;
  const panel = $("aipanel");
  panel.hidden = false;
  $("aipanel-meta").textContent = "";
  $("aipanel-stats").hidden = true;
  $("aipanel-stats").innerHTML = "";
  $("aipanel-body").innerHTML =
    `<div class="thinking"><div class="spinner"></div> ${escapeHtml($("model").value)} is analyzing ${lastRunData.layers.length} layers of results…</div>`;
  panel.scrollIntoView({ behavior: "smooth", block: "start" });
  try {
    const r = await fetch("/api/llm/analyze", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        host: $("llmhost").value.trim(),
        model: $("model").value,
        config: lastRunData.config,
        layers: lastRunData.layers,
      }),
    });
    const data = await r.json();
    if (!data.ok) {
      $("aipanel-body").innerHTML = `<p class="llmstatus err">Analysis failed: ${escapeHtml(data.error || "unknown error")}</p>`;
      return;
    }
    $("aipanel-meta").textContent = `${data.model}${data.durationMs ? " · " + (data.durationMs / 1000).toFixed(1) + "s" : ""}`;
    renderAnalysisStats(data);
    $("aipanel-body").innerHTML = renderMarkdown(data.analysis);
  } catch (e) {
    $("aipanel-body").innerHTML = `<p class="llmstatus err">Request failed: ${escapeHtml(String(e))}</p>`;
  } finally {
    btn.disabled = false;
  }
}

// Render the model + request + generation detail grid above the analysis.
function renderAnalysisStats(data) {
  const el = $("aipanel-stats");
  const m = data.modelInfo || {};
  const rq = data.request || {};
  const mx = data.metrics || {};
  const cell = (k, v) => v == null || v === "" ? "" :
    `<div class="statcell"><div class="k">${escapeHtml(k)}</div><div class="v">${v}</div></div>`;
  const num = (n) => n == null ? null : Number(n).toLocaleString();
  const ms = (n) => n == null ? null : n >= 1000 ? (n / 1000).toFixed(1) + "s" : Math.round(n) + "ms";

  let html = "";

  // --- Model ---
  html += `<div class="statgroup">Model</div>`;
  html += cell("Name", escapeHtml(data.model));
  html += cell("Parameters", m.parameterSize && escapeHtml(m.parameterSize));
  html += cell("Quantization", m.quantization && escapeHtml(m.quantization));
  html += cell("Architecture", m.architecture && escapeHtml(m.architecture));
  html += cell("Family", m.family && escapeHtml(m.family));
  html += cell("Format", m.format && escapeHtml(m.format.toUpperCase()));
  html += cell("Max context", m.maxContext && num(m.maxContext) + " <small>tok</small>");
  html += cell("Embedding dim", m.embeddingLength && num(m.embeddingLength));
  if (m.capabilities && m.capabilities.length)
    html += cell("Capabilities", escapeHtml(m.capabilities.join(", ")));
  html += cell("Endpoint", data.endpoint && escapeHtml(data.endpoint.replace(/^https?:\/\//, "")));

  // --- Log / payload sent ---
  html += `<div class="statgroup">Diagnostics log sent</div>`;
  html += cell("Layers", rq.layers != null && num(rq.layers));
  html += cell("Tests", rq.tests != null && num(rq.tests));
  html += cell("Log lines", rq.logLines != null && num(rq.logLines));
  html += cell("Payload size", rq.totalChars != null && num(rq.totalChars) + " <small>chars</small>");
  html += cell("Approx tokens", rq.approxTokens != null && "~" + num(rq.approxTokens));
  html += cell("Context window", data.numCtx && num(data.numCtx) + " <small>tok requested</small>");

  // --- Generation metrics ---
  html += `<div class="statgroup">Generation</div>`;
  html += cell("Prompt tokens", mx.promptTokens != null && num(mx.promptTokens) + " <small>ingested</small>");
  html += cell("Response tokens", mx.responseTokens != null && num(mx.responseTokens));
  html += cell("Speed", mx.genTokensPerSec != null && mx.genTokensPerSec + " <small>tok/s</small>");
  html += cell("Model load", ms(mx.loadMs));
  html += cell("Prompt eval", ms(mx.promptEvalMs));
  html += cell("Generation", ms(mx.evalMs));
  html += cell("Total", ms(mx.totalMs));
  if (mx.doneReason) html += cell("Finish reason", escapeHtml(mx.doneReason));

  // --- Context utilization bar (proves the whole log fit) ---
  if (mx.promptTokens && m.maxContext) {
    const pct = Math.min(100, (mx.promptTokens / m.maxContext) * 100);
    const usedPct = mx.contextUsedPct != null ? mx.contextUsedPct : pct.toFixed(1);
    html += `<div class="ctxbar">
      <div class="lbl">Input used <strong>${num(mx.promptTokens)}</strong> of ${num(m.maxContext)} max context tokens (${usedPct}%) — full diagnostics log ingested, not truncated</div>
      <div class="track"><div class="fill" style="width:${pct}%"></div></div>
    </div>`;
  }

  el.innerHTML = html;
  el.hidden = false;
}

// Minimal, safe Markdown renderer (headings, bold, code, lists, paragraphs).
function renderMarkdown(md) {
  const esc = escapeHtml(md);
  const lines = esc.split("\n");
  let html = "", inList = false;
  const inline = (s) =>
    s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
     .replace(/`([^`]+)`/g, "<code>$1</code>");
  for (let raw of lines) {
    const line = raw.trimEnd();
    let m;
    if ((m = line.match(/^#{1,6}\s+(.*)/))) {
      if (inList) { html += "</ul>"; inList = false; }
      html += `<h3>${inline(m[1])}</h3>`;
    } else if ((m = line.match(/^\s*[-*]\s+(.*)/))) {
      if (!inList) { html += "<ul>"; inList = true; }
      html += `<li>${inline(m[1])}</li>`;
    } else if (line.trim() === "") {
      if (inList) { html += "</ul>"; inList = false; }
    } else {
      if (inList) { html += "</ul>"; inList = false; }
      html += `<p>${inline(line)}</p>`;
    }
  }
  if (inList) html += "</ul>";
  return html;
}

$("detect").addEventListener("click", detectModels);
$("analyze").addEventListener("click", analyze);
$("model").addEventListener("change", updateAnalyzeButton);
$("aipanel-close").addEventListener("click", () => ($("aipanel").hidden = true));

$("run").addEventListener("click", run);
loadInfo();
