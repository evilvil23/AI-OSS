"use strict";
/* smarthome.js 智能家居设备管理（v0.23）。
 *
 * 数据源：HomeAssistant（Deps.HAClient → /api/ai/ha/*）。
 * 复用 app.js 的全局工具：$ / api / AUTH / esc / escAttr / jsStr / toast。
 * 灯 / 开关 / 风扇 / 窗帘提供开关控制，空调支持温度设定，传感器只读；
 * 米家 / MQTT（internal/iot）接入后在此扩展设备来源。
 */

const SH_DOMAIN_LABEL = {
  light: "灯", switch: "开关", climate: "空调", cover: "窗帘",
  fan: "风扇", sensor: "传感器", media_player: "媒体播放", lock: "门锁"
};
// 可控域：state 为 on/off 时展示开关按钮
const SH_TOGGLE_DOMAINS = ["light", "switch", "fan", "cover", "media_player", "lock"];
let shDomain = "";   // 当前域过滤（空 = 全部）
let shInited = false;

function shInit() {
  if (!shInited) {
    shInited = true;
    shLoadStatus();
  }
  shLoadDevices();
}

async function shLoadStatus() {
  const el = $("sh-status");
  try {
    const st = await api("/api/ai/ha/status", { headers: AUTH() });
    el.innerHTML = st.connected
      ? `<span class="tag">HomeAssistant 已连接</span> <span style="color:var(--muted);font-size:12px">${esc(st.base_url || "")}</span>`
      : `<span class="tag" style="color:var(--warn,#e6a23c)">HomeAssistant 未连接</span> <span style="color:var(--muted);font-size:12px">检查 [ai.homeassistant] 配置与网络</span>`;
    $("sh-tip").style.display = "";
  } catch (e) {
    el.innerHTML = `<span class="tag" style="color:var(--warn,#e6a23c)">HomeAssistant 未配置</span> <span style="color:var(--muted);font-size:12px">${esc(e.message)}</span>`;
    $("sh-devices").innerHTML = `<div class="empty">在 config.toml [ai.homeassistant] 中配置 base_url 与 token 后即可管理设备</div>`;
  }
}

async function shLoadDevices() {
  const box = $("sh-devices");
  if (!box) return;
  try {
    const d = await api("/api/ai/ha/devices" + (shDomain ? "?domain=" + encodeURIComponent(shDomain) : ""), { headers: AUTH() });
    const devices = d.devices || [];
    if (!devices.length) {
      box.innerHTML = `<div class="empty">${shDomain ? "该分类下暂无设备" : "未发现设备（检查 HomeAssistant 实体与令牌权限）"}</div>`;
      return;
    }
    box.innerHTML = devices.map(shDeviceCard).join("");
  } catch (e) {
    box.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
  }
}

let shAllDomains = null; // 全量域列表缓存（渲染过滤按钮用）

async function shLoadDomainTabs() {
  try {
    const d = await api("/api/ai/ha/devices", { headers: AUTH() });
    const counts = {};
    (d.devices || []).forEach(x => { counts[x.domain] = (counts[x.domain] || 0) + 1; });
    shAllDomains = Object.keys(counts).sort();
    const tabs = [{ key: "", label: "全部" }].concat(
      shAllDomains.map(k => ({ key: k, label: (SH_DOMAIN_LABEL[k] || k) + " (" + counts[k] + ")" }))
    );
    $("sh-domains").innerHTML = tabs.map(t =>
      `<button data-domain="${escAttr(t.key)}" class="${t.key === shDomain ? "active" : ""}" onclick="shPickDomain('${jsStr(t.key)}')">${esc(t.label)}</button>`
    ).join("");
  } catch (_) { /* 状态徽标已展示错误，此处静默 */ }
}

function shPickDomain(key) {
  shDomain = key;
  shLoadDomainTabs().then(shLoadDevices);
}

function shDeviceCard(d) {
  const on = String(d.state || "").toLowerCase() === "on";
  const controllable = SH_TOGGLE_DOMAINS.includes(d.domain);
  const stateLabel = esc(d.state || "-");
  const stateCls = on ? "on" : d.domain === "sensor" ? "muted" : "off";
  let actions = "";
  if (controllable && ["on", "off"].includes(String(d.state || "").toLowerCase())) {
    actions = `<div class="device-actions">
      <button class="btn ${on ? "" : "primary"}" onclick="shToggle('${jsStr(d.entity_id)}', ${on ? "'turn_off'" : "'turn_on'"})">${on ? "关闭" : "开启"}</button>
    </div>`;
  }
  if (d.domain === "climate") {
    const unit = (d.attributes && d.attributes.unit_of_measurement) || "°C";
    const cur = d.attributes && d.attributes.temperature != null ? d.attributes.temperature : "";
    actions += `<div class="device-actions">
      <input type="number" id="sh-temp-${escAttr(d.entity_id)}" value="${escAttr(cur)}" step="0.5" style="width:90px" placeholder="目标${esc(unit)}">
      <button class="btn primary" onclick="shSetClimate('${jsStr(d.entity_id)}')">设定温度</button>
    </div>`;
  }
  const attrs = shAttrSummary(d);
  return `<div class="device-card" data-entity="${escAttr(d.entity_id)}">
    <div class="device-head">
      <span class="device-name" title="${escAttr(d.entity_id)}">${esc(d.name)}</span>
      <span class="tag">${esc(SH_DOMAIN_LABEL[d.domain] || d.domain)}</span>
    </div>
    <div class="device-state ${stateCls}">状态：${stateLabel}</div>
    ${attrs ? `<div class="device-attrs">${attrs}</div>` : ""}
    ${actions}
  </div>`;
}

// shAttrSummary 挑选关键属性摘要（温度 / 湿度 / 亮度 / 单位等）
function shAttrSummary(d) {
  const a = d.attributes || {};
  const parts = [];
  if (a.temperature != null) parts.push("温度 " + a.temperature + (a.unit_of_measurement || ""));
  if (a.current_temperature != null) parts.push("室温 " + a.current_temperature + (a.unit_of_measurement || ""));
  if (a.humidity != null) parts.push("湿度 " + a.humidity + "%");
  if (a.brightness != null) parts.push("亮度 " + Math.round(a.brightness / 2.55) + "%");
  return parts.map(esc).join(" · ");
}

// shToggle 开 / 关设备（service 传入 turn_on / turn_off）
async function shToggle(entityID, service) {
  const domain = entityID.split(".")[0];
  try {
    await api("/api/ai/ha/service", {
      method: "POST",
      headers: AUTH(),
      body: JSON.stringify({ domain, service, entity_id: entityID })
    });
    toast("已" + (service === "turn_on" ? "开启" : "关闭"), "ok");
    setTimeout(shLoadDevices, 600); // 等待 HA 状态回写后刷新
  } catch (e) { toast(e.message, "err"); }
}

// shSetClimate 设定空调温度（climate/set_temperature）
async function shSetClimate(entityID) {
  const v = $("sh-temp-" + CSS.escape(entityID)).value;
  if (v === "" || isNaN(parseFloat(v))) { toast("请输入有效温度", "err"); return; }
  try {
    await api("/api/ai/ha/service", {
      method: "POST",
      headers: AUTH(),
      body: JSON.stringify({
        domain: "climate",
        service: "set_temperature",
        entity_id: entityID,
        data: { temperature: parseFloat(v) }
      })
    });
    toast("温度已设定", "ok");
    setTimeout(shLoadDevices, 600);
  } catch (e) { toast(e.message, "err"); }
}
