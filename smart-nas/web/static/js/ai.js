"use strict";
/* ai.js AI 管家对话窗口与「AI 与模式」设置区块（v0.23）。
 *
 * 复用 app.js 的全局工具：$ / api / AUTH / esc / toast / IS_PRI / openGen / closeGen。
 * 对话走流式 SSE（POST /api/ai/chat/stream），失败自动回退非流式 /api/ai/chat；
 * AI 设置并入设置页「保存设置」统一提交（v0.26）；需重启项由页面提示条 +
 * 「立即重启」按钮处理（后端以 need_restart 标志下发，重启后自动重置）。
 */

let aiConvID = "";          // 当前会话 ID
let aiConvs = [];           // 会话列表缓存
let aiCtrl = null;          // 流式请求 AbortController
let aiInited = false;       // 已初始化（避免重复拉取）
let aiOffReason = "";       // AI 禁用原因（非空 = AI 已禁用，v0.26）

/* ==================== AI 对话 ==================== */

// aiTabEnter 点击「AI」菜单时调用：已禁用则每次给出自动消失的差异化提示；
// 正常时首次进入执行初始化
function aiTabEnter() {
  if (aiOffReason) { toast(aiOffReason, "err"); return; }
  aiInit();
}

async function aiInit() {
  if (aiInited) { return; }
  aiInited = true;
  try {
    const d = await api("/api/ai/status", { headers: AUTH() });
    // v0.26 后端显式下发禁用态（enabled=false + 差异化原因）
    if (d && d.enabled === false) {
      aiOffReason = d.reason || "AI 功能不可用";
      markAIDisabled();
      toast(aiOffReason, "err");
      return;
    }
    const model = (d && d.preset && d.preset.model) || "-";
    const dep = (d && d.deploy) || {};
    const roleText = dep.role === "auxiliary" ? "辅机" : "服务端";
    const remote = dep.remote_active ? "（远端模型）" : "";
    const meta = $("ai-meta");
    if (meta) meta.textContent = "模型：" + model + " · " + roleText + remote;
    aiLoadConversations();
  } catch (e) {
    // status 接口异常：视为 AI 不可用（对话接口也会给降级提示）
    aiOffReason = "AI 状态查询失败：" + e.message;
    markAIDisabled();
    toast(aiOffReason, "err");
  }
}

// markAIDisabled AI 禁用后的界面降级：状态徽标提示 + 禁用输入区
function markAIDisabled() {
  const meta = $("ai-meta");
  if (meta) meta.textContent = "AI 已禁用";
  const box = $("ai-messages");
  if (box) box.innerHTML = '<div class="empty">' + esc(aiOffReason) + "</div>";
  const input = $("ai-input");
  const send = $("btn-ai-send");
  if (input) input.disabled = true;
  if (send) send.disabled = true;
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
  return `<div class="ai-msg ${role === "user" ? "user" : "assistant"}"><div class="ai-msg-role">${role === "user" ? "我" : "AI"}</div><div class="ai-msg-body">${esc(lstrip(text))}</div></div>`;
}

// lstrip 去除开头空白：部分模型首帧以换行起始，会在气泡顶部留下空白
//（后端 trimLeadingBlank 已清理入库文本，此处覆盖流式实时显示与历史消息）
function lstrip(s) { return (s || "").replace(/^\s+/, ""); }

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
      bodyEl.textContent = lstrip(acc) || "…";
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
    bodyEl.textContent = lstrip(d.reply) || "（空回复）";
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
    // v0.26 AI 总开关（拨动开关；AI 禁用时接口仍可用，保证可重新启用）
    const sw = $("set-ai-enabled");
    if (sw) sw.checked = !!(st && st.enabled);
    // v0.26 待重启提示：存在需重启服务才生效的已保存变更时显示提示条
    setRestartTip(!!(st && st.need_restart));
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
  const sw = $("set-ai-enabled");
  const payload = {
    enabled: sw ? !!sw.checked : undefined,
    model: $("set-ai-model").value || undefined,
    temperature: parseFloat($("set-ai-temp").value),
    conversation_max_tokens: parseInt($("set-ai-maxtok").value, 10) || undefined,
    deploy_mode: $("set-ai-mode").value,
    server_addr: $("set-ai-server").value.trim()
  };
  // 统一由设置页「保存设置」按钮调用：仅提交并返回响应，
  // 需重启项由调用方依据响应中的 need_restart 展示提示条（v0.26）
  return await api("/api/ai/settings", { method: "PUT", headers: AUTH(), body: JSON.stringify(payload) });
}
