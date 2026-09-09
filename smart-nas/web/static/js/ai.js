"use strict";
/* ai.js AI 管家对话窗口与「AI 与模式」设置区块（v0.23）。
 *
 * 复用 app.js 的全局工具：$ / api / AUTH / esc / toast / IS_PRI / openGen / closeGen。
 * 对话走流式 SSE（POST /api/ai/chat/stream），失败自动回退非流式 /api/ai/chat；
 * 机器模式 / 服务端地址等需重启项保存后提示用户确认并自动重启（轮询 /healthz）。
 */

let aiConvID = "";          // 当前会话 ID
let aiConvs = [];           // 会话列表缓存
let aiCtrl = null;          // 流式请求 AbortController
let aiInited = false;       // 已初始化（避免重复拉取）

/* ==================== AI 对话 ==================== */

async function aiInit() {
  if (aiInited) { return; }
  aiInited = true;
  aiLoadConversations();
  // 状态徽标：当前模型与部署身份（失败静默，AI 未启用时对话接口也会给降级提示）
  api("/api/ai/status", { headers: AUTH() }).then(d => {
    const model = (d.preset && d.preset.model) || "-";
    const dep = d.deploy || {};
    const roleText = dep.role === "auxiliary" ? "辅机" : "服务端";
    const remote = dep.remote_active ? "（远端模型）" : "";
    const meta = $("ai-meta");
    if (meta) meta.textContent = "模型：" + model + " · " + roleText + remote;
  }).catch(() => {
    const meta = $("ai-meta");
    if (meta) meta.textContent = "AI 未启用（检查 [ai] 配置与 Ollama）";
  });
}

async function aiLoadConversations() {
  try {
    const list = await api("/api/ai/conversations", { headers: AUTH() }) || [];
    aiConvs = list;
    const el = $("ai-conv-list");
    if (!list.length) { el.innerHTML = '<div class="empty">暂无会话</div>'; return; }
    el.innerHTML = list.map(c => `
      <div class="ai-conv-item ${c.id === aiConvID ? "active" : ""}" data-cid="${escAttr(c.id)}" onclick="aiOpenConv('${jsStr(c.id)}')">
        <span class="ai-conv-title">${esc(c.title || "新会话")}</span>
        <button class="ai-conv-del" title="删除会话" onclick="event.stopPropagation();aiDeleteConv('${jsStr(c.id)}')">✕</button>
      </div>`).join("");
  } catch (e) { toast(e.message, "err"); }
}

async function aiNewConversation() {
  aiConvID = "";
  $("ai-title").textContent = "AI 管家";
  $("ai-messages").innerHTML = '<div class="empty">向 AI 提问，例如：「帮我总结 NAS 里的文档」「打开客厅的灯」</div>';
  aiRenderConvActive();
}

async function aiOpenConv(id) {
  if (aiCtrl) aiStop();
  try {
    const conv = await api("/api/ai/conversations/" + encodeURIComponent(id), { headers: AUTH() });
    aiConvID = conv.id;
    $("ai-title").textContent = conv.title || "新会话";
    const box = $("ai-messages");
    const msgs = (conv.messages || []).filter(m => m.role === "user" || m.role === "assistant");
    if (!msgs.length) {
      box.innerHTML = '<div class="empty">暂无消息</div>';
    } else {
      box.innerHTML = msgs.map(m => aiMsgHtml(m.role, m.content)).join("");
      box.scrollTop = box.scrollHeight;
    }
    aiRenderConvActive();
  } catch (e) { toast(e.message, "err"); }
}

async function aiDeleteConv(id) {
  if (!confirm("确定删除该会话？")) return;
  try {
    await api("/api/ai/conversations/" + encodeURIComponent(id), { method: "DELETE", headers: AUTH() });
    if (aiConvID === id) await aiNewConversation();
    aiLoadConversations();
  } catch (e) { toast(e.message, "err"); }
}

function aiRenderConvActive() {
  document.querySelectorAll("#ai-conv-list .ai-conv-item").forEach(el => {
    el.classList.toggle("active", el.getAttribute("data-cid") === aiConvID);
  });
}

function aiMsgHtml(role, text) {
  return `<div class="ai-msg ${role === "user" ? "user" : "assistant"}"><div class="ai-msg-role">${role === "user" ? "我" : "AI"}</div><div class="ai-msg-body">${esc(text)}</div></div>`;
}

async function aiSend() {
  const input = $("ai-input");
  const content = (input.value || "").trim();
  if (!content || aiCtrl) return;
  input.value = "";

  const box = $("ai-messages");
  if (box.querySelector(".empty")) box.innerHTML = "";
  box.insertAdjacentHTML("beforeend", aiMsgHtml("user", content));
  box.scrollTop = box.scrollHeight;

  // 助手消息占位，流式追加
  box.insertAdjacentHTML("beforeend", aiMsgHtml("assistant", ""));
  const bodyEl = box.querySelector(".ai-msg.assistant:last-child .ai-msg-body");
  bodyEl.innerHTML = '<span class="ai-typing">思考中…</span>';
  box.scrollTop = box.scrollHeight;

  aiCtrl = new AbortController();
  $("btn-ai-send").style.display = "none";
  $("btn-ai-stop").style.display = "";

  try {
    const resp = await fetch(BASE + "/api/ai/chat/stream", {
      method: "POST",
      headers: AUTH(),
      body: JSON.stringify({ conversation_id: aiConvID, content }),
      signal: aiCtrl.signal
    });
    if (!resp.ok || !resp.body) {
      throw new Error("stream HTTP " + resp.status);
    }
    let acc = "";
    await aiReadSSE(resp.body, ev => {
      if (ev.conversation_id) aiConvID = ev.conversation_id;
      if (ev.error) { acc += (acc ? "\n" : "") + "[错误] " + ev.error; }
      if (ev.delta) acc += ev.delta;
      // done 帧携带全量最终文本（含工具轮次汇总），以此为准覆盖增量
      if (ev.content) acc = ev.content;
      bodyEl.textContent = acc || "…";
      box.scrollTop = box.scrollHeight;
    });
    if (!acc) { bodyEl.textContent = "（空回复）"; }
    aiLoadConversations(); // 新会话标题入库后刷新列表
  } catch (e) {
    if (e.name === "AbortError") {
      bodyEl.textContent += "\n（已停止）";
    } else {
      // 流式失败回退非流式一次性对话
      await aiFallbackSend(content, bodyEl);
    }
  } finally {
    aiCtrl = null;
    $("btn-ai-send").style.display = "";
    $("btn-ai-stop").style.display = "none";
  }
}

// aiReadSSE 解析 SSE 流：按空行分帧、剥 data: 前缀、逐事件回调
async function aiReadSSE(stream, onEvent) {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf("\n\n")) >= 0) {
      const frame = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      const line = frame.split("\n").find(l => l.startsWith("data: "));
      if (!line) continue;
      try { onEvent(JSON.parse(line.slice(6))); } catch (_) { /* 跳过坏帧 */ }
    }
  }
}

function aiStop() {
  if (aiCtrl) { aiCtrl.abort(); }
}

// aiFallbackSend 流式失败时回退非流式（渲染完整回复或错误提示）
async function aiFallbackSend(content, bodyEl) {
  bodyEl.innerHTML = '<span class="ai-typing">流式不可用，改用普通模式…</span>';
  try {
    const d = await api("/api/ai/chat", {
      method: "POST",
      headers: AUTH(),
      body: JSON.stringify({ conversation_id: aiConvID, content })
    });
    bodyEl.textContent = d.reply || "（空回复）";
    if (d.conversation_id) aiConvID = d.conversation_id;
    aiLoadConversations();
  } catch (e) {
    bodyEl.innerHTML = '<span class="ai-err">' + esc(e.message) + "</span>";
  }
}

/* ==================== 设置：AI 与模式（v0.23） ==================== */

// aiOpenSettings 跳转到「管理 → 设置」中的 AI 设置区块（仅主人/管理员）。
// AI 设置已并入主设置面板（index.html aview-settings）；打开后滚动到区块并刷新当前值。
function aiOpenSettings() {
  if (!IS_PRI()) { toast("仅主人/管理员可修改 AI 设置", "err"); return; }
  showTab("admin");
  showAdminView("settings");
  loadAISettings();
  const panel = document.getElementById("ai-settings-anchor");
  if (panel) panel.scrollIntoView({ behavior: "smooth", block: "start" });
}

async function loadAISettings() {
  if (!IS_PRI()) return;
  try {
    const [st, models] = await Promise.all([
      api("/api/ai/settings", { headers: AUTH() }),
      api("/api/ai/models", { headers: AUTH() }).catch(() => null)
    ]);
    // 模式下拉：deploy_mode=auto → 自动；role=auxiliary → 辅机；其余 → 服务端
    $("set-ai-mode").value = st.deploy_mode === "auto" ? "auto"
      : (st.role === "auxiliary" ? "auxiliary" : "server");
    $("set-ai-server").value = st.server_addr || "";
    $("set-ai-temp").value = st.temperature != null ? st.temperature : 0.7;
    $("set-ai-maxtok").value = st.conversation_max_tokens || 40;
    const sel = $("set-ai-model");
    sel.innerHTML = "";
    const list = (models && models.models) || [];
    if (!list.length) {
      // AI 未启用或无模型：降级为可编辑输入（当前值作占位）
      sel.innerHTML = `<option value="${escAttr(st.model || "")}">${esc(st.model || "（无可用模型）" )}</option>`;
    } else {
      list.forEach(m => {
        const name = m.name || m.model || "";
        sel.insertAdjacentHTML("beforeend",
          `<option value="${escAttr(name)}" ${name === st.model ? "selected" : ""}>${esc(name)}</option>`);
      });
      if (st.model && ![...sel.options].some(o => o.value === st.model)) {
        sel.insertAdjacentHTML("afterbegin", `<option value="${escAttr(st.model)}" selected>${esc(st.model + "（当前）")}</option>`);
      }
    }
  } catch (e) {
    // AI 未启用：面板保留默认值并提示（HA 设备管理等不受影响）
    toast("AI 设置加载失败：" + e.message, "err");
  }
}

async function saveAISettings() {
  const payload = {
    model: $("set-ai-model").value || undefined,
    temperature: parseFloat($("set-ai-temp").value),
    conversation_max_tokens: parseInt($("set-ai-maxtok").value, 10) || undefined,
    deploy_mode: $("set-ai-mode").value,
    server_addr: $("set-ai-server").value.trim()
  };
  let st;
  try {
    st = await api("/api/ai/settings", { method: "PUT", headers: AUTH(), body: JSON.stringify(payload) });
  } catch (e) { toast(e.message, "err"); return; }

  const need = (st && st.need_restart) || [];
  if (!need.length) {
    toast("AI 设置已保存并生效", "ok");
    return;
  }
  // 需重启项被修改：确认后自动重启，重启完成提示刷新
  if (!confirm("以下设置需重启服务后才能生效：\n" + need.join("、") +
    "\n\n是否立即重启服务？（重启完成后请刷新页面）")) {
    toast("已保存，部分设置将在下次重启后生效", "ok");
    return;
  }
  try {
    await api("/api/admin/restart", { method: "POST", headers: AUTH() });
  } catch (e) { toast("重启请求失败：" + e.message, "err"); return; }
  toast("服务正在重启，完成后将自动刷新页面…", "ok");
  aiPollRestartAndReload();
}

// aiPollRestartAndReload 轮询 /healthz：先容忍旧进程关闭（连接断开），再等新进程就绪后刷新
function aiPollRestartAndReload() {
  const started = Date.now();
  let seenDown = false;
  const timer = setInterval(async () => {
    const elapsed = Date.now() - started;
    if (elapsed > 210000) { // 3.5 分钟兜底：提示手动刷新
      clearInterval(timer);
      toast("重启超时，请稍后手动刷新页面", "err");
      return;
    }
    try {
      const r = await fetch(BASE + "/healthz", { cache: "no-store" });
      if (r.ok && seenDown) {
        clearInterval(timer);
        toast("服务已重启完成", "ok");
        setTimeout(() => location.reload(), 800);
      }
    } catch (_) {
      seenDown = true; // 旧进程已停止监听
    }
  }, 1500);
}
