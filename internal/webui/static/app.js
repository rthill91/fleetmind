// FleetMind web UI: settings persistence, live fleet roster, and an LLM
// chatbot that drives the FleetMind MCP tools. Everything is client-side —
// the bearer token and LLM API key never leave the browser except in their
// natural target requests.

const PROVIDER_DEFAULTS = {
  anthropic: {
    baseUrl: "https://api.anthropic.com",
    model: "claude-sonnet-4-6",
  },
  openai: {
    baseUrl: "https://api.openai.com/v1",
    model: "gpt-4.1",
  },
  "openai-compatible": {
    baseUrl: "",
    model: "",
  },
};

const SYSTEM_PROMPT =
  "You are FleetMind, an SRE assistant with read-only access to a Linux host " +
  "via MCP tools. Use the tools to investigate the user's question before " +
  "answering. Prefer concrete evidence (process IDs, /proc fields, journal " +
  "lines) over speculation. Tools are strictly read-only — never claim to " +
  "have changed anything.";

const MAX_TOOL_ROUND_TRIPS = 10;

// ---- settings persistence ----

const settings = {
  load() {
    return {
      token: localStorage.getItem("fleetmind.token") || "",
      provider:
        localStorage.getItem("fleetmind.llm.provider") || "anthropic",
      baseUrl: localStorage.getItem("fleetmind.llm.baseUrl") || "",
      apiKey: localStorage.getItem("fleetmind.llm.apiKey") || "",
      model: localStorage.getItem("fleetmind.llm.model") || "",
    };
  },
  save(partial) {
    for (const [k, v] of Object.entries(partial)) {
      const key =
        k === "token" ? "fleetmind.token" : `fleetmind.llm.${k}`;
      if (v === null || v === undefined || v === "") {
        localStorage.removeItem(key);
      } else {
        localStorage.setItem(key, v);
      }
    }
  },
};

// ---- DOM helpers ----

const $ = (sel) => document.querySelector(sel);

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k === "dataset") Object.assign(node.dataset, v);
    else if (k.startsWith("on") && typeof v === "function") {
      node.addEventListener(k.slice(2), v);
    } else if (v !== null && v !== undefined) {
      node.setAttribute(k, v);
    }
  }
  for (const child of children) {
    if (child === null || child === undefined) continue;
    node.appendChild(
      typeof child === "string" ? document.createTextNode(child) : child,
    );
  }
  return node;
}

function setStatus(elem, state, text) {
  elem.dataset.state = state;
  elem.textContent = text;
}

// ---- lightweight markdown → HTML ----

function renderMarkdown(text) {
  // Escape HTML so LLM output can never inject raw tags.
  let html = text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");

  // Protect fenced code blocks (``` … ```)
  const codeBlocks = [];
  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
    codeBlocks.push(`<pre><code>${code}</code></pre>`);
    return `\x00CB${codeBlocks.length - 1}\x00`;
  });

  // Protect inline code (`…`)
  const inlineCodes = [];
  html = html.replace(/`([^`\n]+)`/g, (_, code) => {
    inlineCodes.push(`<code>${code}</code>`);
    return `\x00IC${inlineCodes.length - 1}\x00`;
  });

  // Bold then italic (order matters)
  html = html.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/(?<!\*)\*([^*\n]+)\*(?!\*)/g, "<em>$1</em>");

  // Headers
  html = html.replace(/^#### (.+)$/gm, "<h5>$1</h5>");
  html = html.replace(/^### (.+)$/gm, "<h4>$1</h4>");
  html = html.replace(/^## (.+)$/gm, "<h3>$1</h3>");
  html = html.replace(/^# (.+)$/gm, "<h2>$1</h2>");

  // Horizontal rules
  html = html.replace(/^---+$/gm, "<hr>");

  // Links — only allow http/https to prevent javascript: URIs
  html = html.replace(
    /\[([^\]]+)\]\((https?:\/\/[^)]+)\)/g,
    '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>',
  );

  // Tables: consecutive lines containing pipes
  html = html.replace(/(?:^\|.+\|\s*(?:\n|$))+/gm, (match) => {
    const rows = match.trim().split("\n");
    if (rows.length < 2) return match;
    // Check if second row is a separator (e.g. |---|---|)
    const sepTest = rows[1].replace(/\s/g, "");
    const isSep = /^\|?[\-:|]+(?:\|[\-:|]+)+\|?$/.test(sepTest);
    if (!isSep) return match;

    const parseRow = (row) =>
      row.replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());

    const headerCells = parseRow(rows[0]);
    const thead = "<thead><tr>" + headerCells.map((c) => `<th>${c}</th>`).join("") + "</tr></thead>";

    const bodyRows = rows.slice(2);
    const tbody = bodyRows.length
      ? "<tbody>" + bodyRows.map((r) => {
          const cells = parseRow(r);
          return "<tr>" + cells.map((c) => `<td>${c}</td>`).join("") + "</tr>";
        }).join("") + "</tbody>"
      : "";

    return `<table>${thead}${tbody}</table>`;
  });

  // Unordered lists: consecutive lines starting with - or *
  html = html.replace(/(?:^[*\-] .+(?:\n|$))+/gm, (match) => {
    const items = match
      .trim()
      .split("\n")
      .map((l) => `<li>${l.replace(/^[*\-] /, "")}</li>`)
      .join("");
    return `<ul>${items}</ul>`;
  });

  // Ordered lists: consecutive lines starting with 1. 2. etc.
  html = html.replace(/(?:^\d+\. .+(?:\n|$))+/gm, (match) => {
    const items = match
      .trim()
      .split("\n")
      .map((l) => `<li>${l.replace(/^\d+\. /, "")}</li>`)
      .join("");
    return `<ol>${items}</ol>`;
  });

  // Convert remaining newlines to <br>
  html = html.replace(/\n/g, "<br>");

  // Clean stray <br> adjacent to block elements
  html = html.replace(/<br>(<\/?(?:h[2-5]|pre|ul|ol|li|hr|table|thead|tbody|tr|th|td))/g, "$1");
  html = html.replace(/((?:<\/(?:h[2-5]|pre|ul|ol|li|table|thead|tbody|tr|th|td)>|<hr>))<br>/g, "$1");

  // Restore protected code
  html = html.replace(/\x00CB(\d+)\x00/g, (_, i) => codeBlocks[i]);
  html = html.replace(/\x00IC(\d+)\x00/g, (_, i) => inlineCodes[i]);

  return html;
}

// ---- settings panel wiring ----

function initSettings() {
  const s = settings.load();

  $("#token").value = s.token;
  $("#llm-provider").value = s.provider;
  $("#llm-base-url").value =
    s.baseUrl || PROVIDER_DEFAULTS[s.provider]?.baseUrl || "";
  $("#llm-model").value =
    s.model || PROVIDER_DEFAULTS[s.provider]?.model || "";
  $("#llm-api-key").value = s.apiKey;

  $("#llm-provider").addEventListener("change", (e) => {
    const provider = e.target.value;
    const defaults = PROVIDER_DEFAULTS[provider] || { baseUrl: "", model: "" };
    // Only overwrite the fields if the user hasn't customised them.
    if (!$("#llm-base-url").value) $("#llm-base-url").value = defaults.baseUrl;
    if (!$("#llm-model").value) $("#llm-model").value = defaults.model;
  });

  $("#token-form").addEventListener("submit", (e) => {
    e.preventDefault();
    settings.save({ token: $("#token").value.trim() });
    setStatus($("#token-status"), "ok", "saved");
    // Connecting the SSE/fleet stream now that we have a token.
    fleet.connect();
  });

  $("#token-test").addEventListener("click", async () => {
    settings.save({ token: $("#token").value.trim() });
    setStatus($("#token-status"), "working", "testing");
    try {
      const ok = await fleet.testToken();
      setStatus(
        $("#token-status"),
        ok ? "ok" : "bad",
        ok ? "ok" : "unauthorized",
      );
      if (ok) fleet.connect();
    } catch (err) {
      setStatus($("#token-status"), "bad", err.message || "error");
    }
  });

  $("#llm-form").addEventListener("submit", (e) => {
    e.preventDefault();
    settings.save({
      provider: $("#llm-provider").value,
      baseUrl: $("#llm-base-url").value.trim(),
      model: $("#llm-model").value.trim(),
      apiKey: $("#llm-api-key").value,
    });
    setStatus($("#token-status"), "ok", "llm saved");
  });
}

function currentLLM() {
  const s = settings.load();
  const baseUrl = s.baseUrl || PROVIDER_DEFAULTS[s.provider]?.baseUrl || "";
  const model = s.model || PROVIDER_DEFAULTS[s.provider]?.model || "";
  return { provider: s.provider, baseUrl, model, apiKey: s.apiKey };
}

function currentToken() {
  return ($("#token").value || settings.load().token).trim();
}

// ---- fleet roster + live SSE ----

const fleet = (() => {
  const known = new Map(); // node_id → Peer
  let stream = null;
  let streamAbort = null;
  let heartbeatTimer = null;

  function render() {
    const list = $("#nodes-list");
    const empty = $("#nodes-empty");
    list.innerHTML = "";
    if (known.size === 0) {
      $("#nodes-count").textContent = "—";
      return;
    }
    empty.classList.add("hidden");
    $("#nodes-count").textContent = `${known.size} node${known.size === 1 ? "" : "s"}`;
    const peers = [...known.values()].sort((a, b) =>
      a.node_id.localeCompare(b.node_id),
    );
    for (const p of peers) {
      list.appendChild(renderCard(p));
    }
  }

  function renderCard(p) {
    const age = ageSeconds(p.last_heartbeat);
    let state = "ok";
    if (age > 30) state = "bad";
    else if (age > 15) state = "warn";
    return el(
      "li",
      { class: "node-card" },
      el(
        "div",
        { class: "row" },
        el("span", { class: "id" }, p.node_id.slice(0, 12)),
        el(
          "span",
          { class: "heartbeat", dataset: { state } },
          `${Math.round(age)}s`,
        ),
      ),
      el("div", { class: "url" }, p.advertise_url),
      el(
        "div",
        { class: "meta" },
        `v${p.version || "?"} · ${(p.tools || []).length} tools`,
      ),
    );
  }

  function ageSeconds(iso) {
    if (!iso) return 999;
    const t = Date.parse(iso);
    if (Number.isNaN(t)) return 999;
    return (Date.now() - t) / 1000;
  }

  async function testToken() {
    const token = currentToken();
    if (!token) throw new Error("no token");
    const res = await fetch("/healthz", {
      headers: { Authorization: `Bearer ${token}` },
    });
    return res.ok;
  }

  async function loadInitial() {
    const token = currentToken();
    if (!token) return;
    try {
      const res = await fetch("/fleet/peers", {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (res.status === 404) {
        // Fleet mode disabled — show the empty-state copy.
        known.clear();
        $("#nodes-empty").classList.remove("hidden");
        render();
        return;
      }
      if (!res.ok) {
        throw new Error(`/fleet/peers → ${res.status}`);
      }
      const body = await res.json();
      known.clear();
      for (const p of body.peers || []) {
        known.set(p.node_id, p);
      }
      render();
    } catch (err) {
      console.warn("loadInitial:", err);
    }
  }

  async function connect() {
    disconnect();
    const token = currentToken();
    if (!token) return;
    await loadInitial();

    // EventSource cannot set Authorization, so we hand-parse SSE over fetch.
    streamAbort = new AbortController();
    try {
      const res = await fetch("/fleet/events", {
        headers: {
          Authorization: `Bearer ${token}`,
          Accept: "text/event-stream",
        },
        signal: streamAbort.signal,
      });
      if (res.status === 404) {
        // Fleet mode disabled — surface the same hint.
        $("#nodes-empty").classList.remove("hidden");
        return;
      }
      if (!res.ok) {
        console.warn("/fleet/events", res.status);
        return;
      }
      stream = res.body.getReader();
      parseSSE(stream);
    } catch (err) {
      if (err.name !== "AbortError") console.warn("fleet stream:", err);
    }

    // Periodically re-fetch peer data so status stays accurate even if the
    // SSE stream drops silently or heartbeats lag.
    heartbeatTimer = setInterval(() => {
      loadInitial();
    }, 5000);
  }

  function disconnect() {
    if (streamAbort) {
      streamAbort.abort();
      streamAbort = null;
    }
    stream = null;
    if (heartbeatTimer) {
      clearInterval(heartbeatTimer);
      heartbeatTimer = null;
    }
  }

  async function parseSSE(reader) {
    const decoder = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      buf += decoder.decode(value, { stream: true });
      // SSE messages are separated by a blank line.
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        const frame = buf.slice(0, i);
        buf = buf.slice(i + 2);
        handleFrame(frame);
      }
    }
  }

  function handleFrame(frame) {
    let event = "message";
    const dataLines = [];
    for (const line of frame.split("\n")) {
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
    }
    if (dataLines.length === 0) return;
    let peer;
    try {
      peer = JSON.parse(dataLines.join("\n"));
    } catch {
      return;
    }
    if (event === "peer_removed") known.delete(peer.node_id);
    else known.set(peer.node_id, peer);
    render();
  }

  return { connect, disconnect, testToken };
})();

// ---- MCP Streamable HTTP client ----

const mcp = (() => {
  let sessionId = null;
  let initialized = false;
  let nextId = 1;
  let toolsCache = null;

  async function rpc(method, params, isNotification = false) {
    const token = currentToken();
    if (!token) throw new Error("no fleetmind token configured");
    const body = isNotification
      ? { jsonrpc: "2.0", method, params }
      : { jsonrpc: "2.0", id: nextId++, method, params };
    const headers = {
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
      Authorization: `Bearer ${token}`,
    };
    if (sessionId) headers["Mcp-Session-Id"] = sessionId;

    const res = await fetch("/mcp", {
      method: "POST",
      headers,
      body: JSON.stringify(body),
    });
    const newSession = res.headers.get("Mcp-Session-Id");
    if (newSession) sessionId = newSession;
    if (isNotification) {
      // Notifications get 202 Accepted with no body.
      if (!res.ok && res.status !== 202) {
        throw new Error(`mcp ${method} → ${res.status}`);
      }
      return null;
    }
    if (!res.ok) {
      const txt = await res.text().catch(() => "");
      throw new Error(`mcp ${method} → ${res.status}: ${txt}`);
    }
    const payload = await readMCPResponse(res);
    if (payload.error) {
      throw new Error(`mcp ${method}: ${payload.error.message}`);
    }
    return payload.result;
  }

  async function readMCPResponse(res) {
    const ctype = res.headers.get("Content-Type") || "";
    if (ctype.includes("application/json")) {
      return res.json();
    }
    // SSE single-response: the server frames the JSON-RPC reply as one event.
    const text = await res.text();
    for (const block of text.split("\n\n")) {
      for (const line of block.split("\n")) {
        if (line.startsWith("data:")) {
          try {
            return JSON.parse(line.slice(5).trim());
          } catch {
            /* keep scanning */
          }
        }
      }
    }
    throw new Error("mcp: empty response");
  }

  async function ensureInitialized() {
    if (initialized) return;
    await rpc("initialize", {
      protocolVersion: "2025-06-18",
      capabilities: {},
      clientInfo: { name: "fleetmind-webui", version: "1.0" },
    });
    await rpc("notifications/initialized", undefined, true);
    initialized = true;
  }

  async function listTools() {
    if (toolsCache) return toolsCache;
    await ensureInitialized();
    const result = await rpc("tools/list", {});
    toolsCache = result.tools || [];
    return toolsCache;
  }

  async function callTool(name, args) {
    await ensureInitialized();
    return rpc("tools/call", { name, arguments: args || {} });
  }

  function reset() {
    sessionId = null;
    initialized = false;
    toolsCache = null;
  }

  return { listTools, callTool, reset };
})();

// ---- chat ----

const chat = (() => {
  // Provider-neutral message log. Each entry is { role, content } where
  // content is either a string (assistant text / user text) or a structured
  // list of blocks for tool round-trips. We translate to provider-native
  // shapes only when sending to the LLM.
  let messages = [];
  let busy = false;

  function appendBubble(role, text) {
    const node = el("div", { class: `bubble ${role}` });
    if (role === "assistant") {
      node.innerHTML = renderMarkdown(text);
    } else {
      node.textContent = text;
    }
    $("#chat-transcript").appendChild(node);
    scrollToEnd();
    return node;
  }

  function appendToolCard(name, args) {
    const card = el(
      "details",
      { class: "tool-call" },
      el(
        "summary",
        {},
        el("span", { class: "name" }, name),
        el("span", { class: "status" }, "running…"),
      ),
      el(
        "div",
        { class: "body" },
        el("div", { class: "label" }, "arguments"),
        el("pre", {}, JSON.stringify(args || {}, null, 2)),
      ),
    );
    $("#chat-transcript").appendChild(card);
    scrollToEnd();
    return {
      card,
      setResult(text, ok) {
        card.querySelector(".status").textContent = ok ? "done" : "error";
        const body = card.querySelector(".body");
        body.appendChild(el("div", { class: "label" }, "result"));
        body.appendChild(el("pre", {}, text));
      },
    };
  }

  function scrollToEnd() {
    const t = $("#chat-transcript");
    t.scrollTop = t.scrollHeight;
  }

  function reset() {
    messages = [];
    $("#chat-transcript").innerHTML = "";
    mcp.reset();
  }

  function toolResultText(result) {
    if (!result) return "";
    // FleetMind tools return a one-line summary in content[] and the typed
    // payload in structuredContent. When both are present the summary just
    // duplicates fields already in the structured payload (e.g. "3 items"
    // vs {"count": 3, ...}) — prefer the structured form and skip the text
    // to keep the LLM's tool_result tight.
    const sc = result.structuredContent;
    if (sc && typeof sc === "object" && Object.keys(sc).length > 0) {
      return JSON.stringify(sc, null, 2);
    }
    if (Array.isArray(result.content)) {
      return result.content
        .map((c) => (c.type === "text" ? c.text : JSON.stringify(c)))
        .join("\n");
    }
    return JSON.stringify(result, null, 2);
  }

  async function send(userText) {
    if (busy) return;
    if (!userText.trim()) return;
    const llm = currentLLM();
    if (!llm.apiKey || !llm.baseUrl || !llm.model) {
      appendBubble("error", "Configure an LLM provider, base URL, API key and model first.");
      return;
    }
    busy = true;
    $("#chat-send").disabled = true;

    appendBubble("user", userText);
    messages.push({ role: "user", content: userText });

    try {
      const tools = await mcp.listTools();
      if (llm.provider === "anthropic") {
        await driveAnthropic(llm, tools);
      } else {
        await driveOpenAI(llm, tools);
      }
    } catch (err) {
      appendBubble("error", err.message || String(err));
    } finally {
      busy = false;
      $("#chat-send").disabled = false;
    }
  }

  // ---- Anthropic driver ----

  async function driveAnthropic(llm, mcpTools) {
    const anthropicTools = mcpTools.map((t) => ({
      name: t.name,
      description: t.description || "",
      input_schema: t.inputSchema || { type: "object", properties: {} },
    }));

    // Anthropic messages: user/assistant only. Tool calls are content blocks
    // inside assistant messages; tool results go in user messages.
    const apiMessages = [];
    for (const m of messages) {
      apiMessages.push(toAnthropic(m));
    }

    for (let round = 0; round < MAX_TOOL_ROUND_TRIPS; round++) {
      const res = await fetch(`${llm.baseUrl}/v1/messages`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "x-api-key": llm.apiKey,
          "anthropic-version": "2023-06-01",
          "anthropic-dangerous-direct-browser-access": "true",
        },
        body: JSON.stringify({
          model: llm.model,
          max_tokens: 4096,
          system: SYSTEM_PROMPT,
          tools: anthropicTools,
          messages: apiMessages,
        }),
      });
      if (!res.ok) {
        const t = await res.text().catch(() => "");
        throw new Error(`Anthropic ${res.status}: ${t.slice(0, 400)}`);
      }
      const reply = await res.json();
      const blocks = reply.content || [];
      apiMessages.push({ role: "assistant", content: blocks });
      messages.push({ role: "assistant", content: blocks, _shape: "anthropic" });

      const toolUses = blocks.filter((b) => b.type === "tool_use");
      const textBlocks = blocks.filter((b) => b.type === "text");
      for (const tb of textBlocks) {
        if (tb.text && tb.text.trim()) appendBubble("assistant", tb.text);
      }
      if (toolUses.length === 0) return;

      const toolResultBlocks = [];
      for (const tu of toolUses) {
        const card = appendToolCard(tu.name, tu.input);
        try {
          const result = await mcp.callTool(tu.name, tu.input);
          const text = toolResultText(result);
          card.setResult(text, !result.isError);
          toolResultBlocks.push({
            type: "tool_result",
            tool_use_id: tu.id,
            content: text || "(no content)",
            is_error: !!result.isError,
          });
        } catch (err) {
          card.setResult(err.message, false);
          toolResultBlocks.push({
            type: "tool_result",
            tool_use_id: tu.id,
            content: err.message,
            is_error: true,
          });
        }
      }
      apiMessages.push({ role: "user", content: toolResultBlocks });
      messages.push({
        role: "user",
        content: toolResultBlocks,
        _shape: "anthropic",
      });
    }
    appendBubble("error", `Stopped after ${MAX_TOOL_ROUND_TRIPS} tool round-trips.`);
  }

  function toAnthropic(m) {
    if (m._shape === "anthropic") return { role: m.role, content: m.content };
    return { role: m.role, content: m.content };
  }

  // ---- OpenAI driver ----

  async function driveOpenAI(llm, mcpTools) {
    const openaiTools = mcpTools.map((t) => ({
      type: "function",
      function: {
        name: t.name,
        description: t.description || "",
        parameters: t.inputSchema || { type: "object", properties: {} },
      },
    }));

    const apiMessages = [{ role: "system", content: SYSTEM_PROMPT }];
    for (const m of messages) {
      if (m._shape === "openai") {
        for (const sub of m._sub) apiMessages.push(sub);
      } else {
        apiMessages.push({ role: m.role, content: m.content });
      }
    }

    for (let round = 0; round < MAX_TOOL_ROUND_TRIPS; round++) {
      const res = await fetch(`${llm.baseUrl}/chat/completions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${llm.apiKey}`,
        },
        body: JSON.stringify({
          model: llm.model,
          messages: apiMessages,
          tools: openaiTools,
        }),
      });
      if (!res.ok) {
        const t = await res.text().catch(() => "");
        throw new Error(`OpenAI ${res.status}: ${t.slice(0, 400)}`);
      }
      const reply = await res.json();
      const msg = reply.choices?.[0]?.message;
      if (!msg) throw new Error("OpenAI: empty response");

      apiMessages.push(msg);
      const sub = [msg];

      if (msg.content && msg.content.trim()) appendBubble("assistant", msg.content);

      const toolCalls = msg.tool_calls || [];
      if (toolCalls.length === 0) {
        messages.push({ role: "assistant", content: msg.content || "", _shape: "openai", _sub: sub });
        return;
      }

      for (const tc of toolCalls) {
        let args = {};
        try {
          args = JSON.parse(tc.function.arguments || "{}");
        } catch (err) {
          args = { _parse_error: String(err), _raw: tc.function.arguments };
        }
        const card = appendToolCard(tc.function.name, args);
        let text;
        let isError = false;
        try {
          const result = await mcp.callTool(tc.function.name, args);
          text = toolResultText(result);
          isError = !!result.isError;
          card.setResult(text, !isError);
        } catch (err) {
          text = err.message;
          isError = true;
          card.setResult(text, false);
        }
        const toolMsg = {
          role: "tool",
          tool_call_id: tc.id,
          content: text || (isError ? "error" : "(no content)"),
        };
        apiMessages.push(toolMsg);
        sub.push(toolMsg);
      }
      messages.push({ role: "assistant", content: msg.content || "", _shape: "openai", _sub: sub });
    }
    appendBubble("error", `Stopped after ${MAX_TOOL_ROUND_TRIPS} tool round-trips.`);
  }

  return { send, reset };
})();

// ---- chat UI wiring ----

function initChat() {
  const form = $("#chat-form");
  const input = $("#chat-input");

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const text = input.value;
    input.value = "";
    chat.send(text);
  });

  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      form.requestSubmit();
    }
  });

  $("#chat-clear").addEventListener("click", () => chat.reset());
}

// ---- boot ----

document.addEventListener("DOMContentLoaded", () => {
  initSettings();
  initChat();
  if (currentToken()) fleet.connect();
});
