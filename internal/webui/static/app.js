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
