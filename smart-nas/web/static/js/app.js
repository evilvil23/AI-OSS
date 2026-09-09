"use strict";
const BASE = "";
let TOKEN = localStorage.getItem("nas_token") || "";
let USERNAME = localStorage.getItem("nas_username") || "";
let ROLE = localStorage.getItem("nas_role") || "";
let PARENT = 0;
let CRUMB = [];
let FILES = [];               // 当前目录文件缓存
let SEL = {};                 // 勾选的 id → file
let SORT = { key: "name", asc: true }; // 排序字段与方向

const AUTH = () => ({ "Authorization": "Bearer " + TOKEN, "Content-Type": "application/json" });
const $ = id => document.getElementById(id);
const IS_MASTER = () => ROLE === "master";
const IS_PRI = () => ROLE === "master" || ROLE === "admin";
const ROLE_LABEL = r => r === "master" ? "主人" : r === "admin" ? "管理员" : r === "user" ? "普通用户" : r || "未知";

// 系统状态刷新频率（秒，由设置接口覆盖；普通用户用默认值）
let SYS_REFRESH = { cpu: 5000, disk: 60000 };
let sysCpuTimer = null, sysDiskTimer = null;

/* ---------- 基础工具 ---------- */
function fmtBytes(n) {
  if (n == null || n < 0) return "-";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return n.toFixed(n >= 100 || i === 0 ? 0 : 1) + " " + units[i];
}
// Windows 绝对路径校验与归一化（v0.13）：手动输入路径统一为反斜杠并校验合法性
function normalizeWinPath(p) {
  return String(p == null ? "" : p).trim().replace(/[\\/]+/g, "\\");
}
function isValidWinPath(p) {
  if (!p || !/^[A-Za-z]:[\\/]/.test(p)) return false;
  return !/[<>:"|?*]/.test(p.slice(2));
}

// 文件类型分类：常用格式归类显示，无法归类直接显示尾缀（如 XML）
function fileTypeName(name) {
  const m = /\.([a-z0-9]+)$/i.exec(name || "");
  if (!m) return "-";
  const ext = m[1].toLowerCase();
  const groups = {
    "图片": ["png", "jpg", "jpeg", "gif", "webp", "bmp", "svg", "ico", "tif", "tiff", "heic", "raw", "psd"],
    "视频": ["mp4", "mkv", "avi", "mov", "wmv", "flv", "webm", "m4v", "ts", "rmvb", "mpg", "mpeg", "3gp"],
    "音频": ["mp3", "flac", "wav", "aac", "ogg", "wma", "m4a", "ape", "opus", "cue"],
    "文档": ["doc", "docx", "pdf", "txt", "md", "xls", "xlsx", "ppt", "pptx", "csv", "rtf", "odt", "epub"],
    "压缩包": ["zip", "rar", "7z", "tar", "gz", "bz2", "xz", "iso"]
  };
  for (const label of Object.keys(groups)) {
    if (groups[label].includes(ext)) return label;
  }
  return ext.toUpperCase();
}
function fmtTime(t) {
  if (!t) return "-";
  const d = new Date(t);
  return isNaN(d) ? t : d.toLocaleString();
}
function toast(msg, type) {
  const t = document.createElement("div");
  t.className = "toast " + (type || "");
  t.textContent = msg;
  $("toast-box").appendChild(t);
  setTimeout(() => t.remove(), 3500);
}
async function api(path, opts) {
  console.log("[api]", (opts && opts.method) || "GET", path);
  const resp = await fetch(BASE + path, opts);
  console.log("[api] ->", resp.status, resp.statusText, path);
  // token 失效：强制回到登录页，避免页面空白误导
  if (resp.status === 401 && !/\/auth\/login/.test(path)) {
    console.warn("[api] 401 未认证，强制登出", path);
    forceLogout("登录已过期，请重新登录");
  }
  const j = await resp.json().catch(() => null);
  console.log("[api] body", j);
  if (!resp.ok || !j || j.code !== 0) {
    throw new Error((j && j.message) || ("HTTP " + resp.status));
  }
  return j.data;
}
function esc(s) { return (s || "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])); }
function escAttr(s) { return (s || "").replace(/'/g, "&#39;").replace(/"/g, "&quot;"); }
// jsStr 将字符串安全嵌入 onclick 等 JS 单引号字符串字面量：
// 反斜杠（如 Windows 路径 C:\）必须转义，否则会破坏 JS 语法导致点击失效。
function jsStr(s) {
  return String(s == null ? "" : s)
    .replace(/\\/g, "\\\\")
    .replace(/'/g, "\\'")
    .replace(/"/g, "&quot;")
    .replace(/\r/g, "\\r")
    .replace(/\n/g, "\\n");
}
function b64utf8(s) {
  const bytes = new TextEncoder().encode(s);
  let bin = "";
  bytes.forEach(b => bin += String.fromCharCode(b));
  return btoa(bin);
}

/* ---------- 通用弹窗 ---------- */
function openGen(title, bodyHtml, opts) {
  $("gen-title").textContent = title;
  $("gen-body").innerHTML = bodyHtml;
  // opts.wide：宽版弹窗（备份任务等表单较复杂的场景）
  const box = document.querySelector("#gen-modal .modal");
  if (box) box.classList.toggle("modal-wide", !!(opts && opts.wide));
  $("gen-modal").classList.add("open");
}
function closeGen() { $("gen-modal").classList.remove("open"); }

/* ---------- 登录 / 退出 ---------- */
async function doLogin() {
  const btn = $("btn-login");
  btn.disabled = true;
  try {
    const u = $("username").value.trim(), p = $("password").value;
    if (!u || !p) { toast("请输入用户名和密码", "err"); return; }
    const data = await api("/api/auth/login", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: u, password: p })
    });
    TOKEN = data.token;
    USERNAME = (data.user && data.user.username) || u;
    ROLE = (data.user && data.user.role) || "user";
    localStorage.setItem("nas_token", TOKEN);
    localStorage.setItem("nas_username", USERNAME);
    localStorage.setItem("nas_role", ROLE);
    enterMain();
  } catch (e) {
    toast("登录失败：" + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}
function doLogout() {
  TOKEN = ""; USERNAME = ""; ROLE = "";
  localStorage.removeItem("nas_token");
  localStorage.removeItem("nas_username");
  localStorage.removeItem("nas_role");
  stopSysTimers();
  if (ws) ws.close();
  $("main-view").style.display = "none";
  $("login-view").style.display = "flex";
}
// 认证失效时的强制登出（防止重复执行）
function forceLogout(msg) {
  if (window._forceLoggedOut) return;
  window._forceLoggedOut = true;
  doLogout();
  if (msg) toast(msg, "err");
  console.warn("[auth] forceLogout:", msg);
}
async function enterMain() {
  // 以服务端最新用户信息为准，避免旧的本地角色（如 admin）导致主人权限异常
  try {
    const me = await api("/api/auth/me", { headers: AUTH() });
    USERNAME = me.username || USERNAME;
    ROLE = me.role || ROLE;
    window._myid = me.id;
    localStorage.setItem("nas_username", USERNAME);
    localStorage.setItem("nas_role", ROLE);
    console.log("[auth] /me ok:", USERNAME, ROLE, "id=", me.id);
  } catch (e) {
    console.warn("[auth] /me 失败，回到登录页:", e.message);
    forceLogout("登录已过期，请重新登录");
    return;
  }
  $("login-view").style.display = "none";
  $("main-view").style.display = "block";
  $("user-name").textContent = USERNAME + "（" + ROLE_LABEL(ROLE) + "）";
  $("nav-admin").style.display = IS_PRI() ? "" : "none";
  if (IS_PRI()) await loadSettings();
  // 主人可创建管理员角色；管理员只能创建普通用户
  $("nu-role-admin").style.display = IS_MASTER() ? "" : "none";
  connectWS();
  showTab("system");
}

/* ---------- Tab 切换 ---------- */
const TABS = ["system", "files", "backup", "trash", "ai", "smarthome", "admin"];
function showTab(name) {
  TABS.forEach(t => {
    $("view-" + t).style.display = t === name ? "" : "none";
    const btn = document.querySelector(`#main-nav button[data-tab="${t}"]`);
    if (btn) btn.classList.toggle("active", t === name);
  });
  if (name === "system") startSysTimers();
  if (name === "files") refresh();
  if (name === "backup") loadBackupTasks();
  if (name === "trash") loadTrash();
  if (name === "ai") aiInit();
  if (name === "smarthome") shInit();
  if (name === "admin") { showAdminView("users"); adminLoadUsers(); loadSettings(); loadPlugins(); loadAISettings(); }
}
const ADMIN_VIEWS = ["users", "settings", "plugins"];
function showAdminView(name) {
  ADMIN_VIEWS.forEach(v => {
    $("aview-" + v).style.display = v === name ? "" : "none";
    const btn = document.querySelector(`#admin-nav button[data-aview="${v}"]`);
    if (btn) btn.classList.toggle("active", v === name);
  });
}

/* ==================== 主页：系统状态 ==================== */
async function loadSysCore() {
  try {
    const d = await api("/api/system/status", { headers: AUTH() });
    const st = d.system || {};
    $("sys-host").textContent = st.hostname || "-";
    $("sys-os").textContent = (st.os || "-") + " " + (st.arch || "") + " · " + (st.cpu_cores || 0) + " 核";
    $("sys-cpu").textContent = (st.cpu_usage || 0).toFixed(1) + "%";
    $("sys-mem").textContent = (st.memory_used ? fmtBytes(st.memory_used) : "-") + " / " + (st.memory_total ? fmtBytes(st.memory_total) : "-");
    $("sys-up").textContent = (st.uptime || "-") + " · " + (st.lan_ip || "-");
    $("sys-ws").textContent = d.ws_connections || 0;
  } catch (e) { /* 忽略 */ }
}
async function loadSysDisks() {
  try {
    const d = await api("/api/system/status", { headers: AUTH() });
    const disks = (d.system && d.system.disks) || [];
    const el = $("sys-disks");
    if (!disks.length) { el.innerHTML = '<div class="empty">未获取到磁盘信息</div>'; return; }
    const rows = disks.map(x => `
      <tr>
        <td>${esc(x.mount_point)}</td>
        <td>${fmtBytes(x.total)}</td>
        <td>${fmtBytes(x.used)}</td>
        <td>${(x.usage || 0).toFixed(1)}%</td>
      </tr>`).join("");
    el.innerHTML = `<table><thead><tr><th>盘符</th><th>总量</th><th>已用</th><th>使用率</th></tr></thead><tbody>${rows}</tbody></table>`;
  } catch (e) { /* 忽略 */ }
}
function startSysTimers() {
  stopSysTimers();
  loadSysCore(); loadSysDisks();
  sysCpuTimer = setInterval(loadSysCore, SYS_REFRESH.cpu);
  sysDiskTimer = setInterval(loadSysDisks, SYS_REFRESH.disk);
}
function stopSysTimers() {
  if (sysCpuTimer) { clearInterval(sysCpuTimer); sysCpuTimer = null; }
  if (sysDiskTimer) { clearInterval(sysDiskTimer); sysDiskTimer = null; }
}
// 登录设备图标（显示器=电脑 / 手机）
function deviceIcon(d) {
  if (d === "mobile") {
    return '<span title="手机" style="display:inline-flex;vertical-align:middle;color:var(--accent)"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="2" width="14" height="20" rx="2"/><line x1="12" y1="18" x2="12.01" y2="18"/></svg></span>';
  }
  return '<span title="电脑" style="display:inline-flex;vertical-align:middle;color:var(--accent)"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg></span>';
}
function deviceLabel(d) { return d === "mobile" ? "手机" : "电脑"; }
function showOnline() {
  if (!IS_PRI()) { toast("仅主人/管理员可查看在线用户明细", "err"); return; }
  api("/api/system/status", { headers: AUTH() }).then(d => {
    const users = d.online_users || [];
    if (!users.length) { openGen("在线用户", '<div class="empty">当前无在线用户</div>'); return; }
    const rows = users.map(u => {
      const devs = (u.devices && u.devices.length ? u.devices : ["pc"]);
      const icons = devs.map(deviceIcon).join('<span style="margin-left:6px"></span>');
      return `
      <tr>
        <td>${esc(u.username)}</td>
        <td><span class="tag ${u.role === 'master' ? 'master' : u.role === 'admin' ? 'admin' : ''}">${ROLE_LABEL(u.role)}</span></td>
        <td title="${devs.map(deviceLabel).join("、")}">${icons} <span style="color:var(--muted);font-size:12px;margin-left:4px">${devs.map(deviceLabel).join("、")}</span></td>
      </tr>`;
    }).join("");
    openGen("在线用户（共 " + (d.ws_connections || 0) + " 个连接）",
      `<table><thead><tr><th>用户</th><th>角色</th><th>登录设备</th></tr></thead><tbody>${rows}</tbody></table>` +
      '<div class="m-actions"><button class="primary" onclick="closeGen()">关闭</button></div>');
  }).catch(e => toast(e.message, "err"));
}

/* ==================== 个人信息（v0.14） ==================== */
async function showProfile() {
  let me = null;
  try { me = await api("/api/auth/me", { headers: AUTH() }); } catch (e) { toast(e.message, "err"); return; }
  if (!me) return;
  const perms = me.permissions || [];
  const permHtml = perms.length
    ? perms.map(p => `<span class="tag" style="margin:2px 4px 2px 0">${esc(p.path)} <span style="color:${p.write ? "var(--ok)" : "var(--muted)"}">${p.write ? "读写" : (p.read ? "只读" : "无")}</span></span>`).join("")
    : '<span class="tag on">全量访问（未配置限制）</span>';
  const isMaster = me.role === "master";
  const body = `
    <div class="form-row"><label>用户名</label><div style="flex:1">${esc(me.username)}</div></div>
    <div class="form-row"><label>角色</label><div style="flex:1"><span class="tag ${isMaster ? 'master' : me.role === 'admin' ? 'admin' : ''}">${ROLE_LABEL(me.role)}</span></div></div>
    <div class="form-row"><label>创建时间</label><div style="flex:1">${fmtTime(me.created_at)}</div></div>
    <div class="form-row" style="align-items:flex-start"><label>目录权限</label><div style="flex:1">${permHtml}</div></div>
    ${isMaster ? '<div class="file-hint">主人密码仅可通过 config.toml 的 [auth] 段修改</div>' : `
    <div style="border-top:1px solid var(--line);margin:12px 0;padding-top:12px">
      <div class="form-row"><label>旧密码</label><input type="password" id="pf-old" placeholder="当前密码"></div>
      <div class="form-row"><label>新密码</label><input type="password" id="pf-new" placeholder="至少 6 位"></div>
      <div class="m-actions" style="margin-top:8px">
        <button onclick="closeGen()">关闭</button>
        <button class="primary" onclick="doChangePwd()">修改密码</button>
      </div>
    </div>`}`;
  openGen("个人信息", body);
}
async function doChangePwd() {
  const oldPwd = $("pf-old") ? $("pf-old").value : "";
  const newPwd = $("pf-new") ? $("pf-new").value : "";
  if (!oldPwd || !newPwd) { toast("请输入旧密码与新密码", "err"); return; }
  if (newPwd.length < 6) { toast("新密码至少 6 位", "err"); return; }
  try {
    await api("/api/auth/change-password", {
      method: "POST", headers: AUTH(),
      body: JSON.stringify({ old_password: oldPwd, new_password: newPwd })
    });
    toast("密码已修改", "ok"); closeGen();
  } catch (e) { toast(e.message, "err"); }
}

/* ==================== 设置 ==================== */
// 设置仅主人可修改：管理员/普通用户为只读查看
function setSettingsDisabled(disabled) {
  ["set-cpu", "set-disk", "set-trash", "set-logpath", "set-logsize", "set-logage", "set-bkdir", "set-bklevel", "set-bkexcludes"].forEach(id => { const el = $(id); if (el) el.disabled = disabled; });
  ["btn-pick-trash", "btn-pick-log", "btn-pick-bkdir", "btn-save-settings", "btn-clear-cache", "btn-reset-settings"].forEach(id => { const el = $(id); if (el) el.disabled = disabled; });
  const tip = $("settings-lock-tip"); if (tip) tip.style.display = disabled ? "" : "none";
}
async function loadSettings() {
  const locked = !IS_MASTER(); // 主人可改，其余只读
  setSettingsDisabled(locked);
  try {
    const s = await api("/api/admin/settings", { headers: AUTH() });
    SYS_REFRESH.cpu = (s.cpu_refresh_seconds || 5) * 1000;
    SYS_REFRESH.disk = (s.disk_refresh_seconds || 60) * 1000;
    $("set-cpu").value = s.cpu_refresh_seconds || 5;
    $("set-disk").value = s.disk_refresh_seconds || 60;
    $("set-trash").value = s.trash_path || "";
    $("set-logpath").value = s.log_path || "";
    $("set-logsize").value = s.log_max_size || 100;
    $("set-logage").value = s.log_max_age || 30;
    $("set-bkdir").value = s.backup_output_dir || "";
    $("set-bklevel").value = String(s.backup_compress_level || 9);
    $("set-bkexcludes").value = (s.backup_exclude_rules || []).join("\n");
  } catch (e) { /* 忽略 */ } finally { setSettingsDisabled(locked); }
}
async function saveSettings() {
  if (!IS_MASTER()) { toast("只有主人可以修改系统设置", "err"); return; }
  const cpu = parseInt($("set-cpu").value) || 5;
  const disk = parseInt($("set-disk").value) || 60;
  let trash = $("set-trash").value.trim();
  let logpath = $("set-logpath").value.trim();
  const logsize = parseInt($("set-logsize").value) || 100;
  const logage = parseInt($("set-logage").value) || 30;
  // 路径归一化（统一反斜杠）+ 合法性校验（盘符开头的绝对路径）
  if (trash) {
    trash = normalizeWinPath(trash);
    if (!isValidWinPath(trash)) { toast("回收站路径不合法：应为盘符开头的绝对路径，如 Y:\\nas\\trash", "err"); return; }
  }
  if (logpath) {
    logpath = normalizeWinPath(logpath);
    if (!isValidWinPath(logpath)) { toast("日志路径不合法：应为盘符开头的绝对路径", "err"); return; }
  }
  let bkdir = $("set-bkdir").value.trim();
  if (bkdir) {
    bkdir = normalizeWinPath(bkdir);
    if (!isValidWinPath(bkdir)) { toast("备份存放目录不合法：应为盘符开头的绝对路径", "err"); return; }
  }
  const bklevel = parseInt($("set-bklevel").value, 10) || 9;
  // 全局排除规则：每行一条（可直接复制用作 exclude-list.txt）
  const bkexcludes = String($("set-bkexcludes").value || "").split("\n")
    .map(l => l.trim()).filter(l => l !== "");
  try {
    await api("/api/admin/settings", {
      method: "PUT", headers: AUTH(),
      body: JSON.stringify({
        cpu_refresh_seconds: cpu, disk_refresh_seconds: disk, trash_path: trash,
        log_path: logpath, log_max_size: logsize, log_max_age: logage,
        backup_output_dir: bkdir, backup_compress_level: bklevel,
        backup_exclude_rules: bkexcludes
      })
    });
    SYS_REFRESH.cpu = cpu * 1000;
    SYS_REFRESH.disk = disk * 1000;
    if (startSysTimers) startSysTimers();
    toast("设置已保存并生效", "ok");
  } catch (e) { toast(e.message, "err"); }
}

// 清除缓存：清空视频转封装产物缓存目录（仅主人）
async function clearSystemCache() {
  if (!IS_MASTER()) { toast("只有主人可以清除缓存", "err"); return; }
  if (!confirm("确定清除视频转封装缓存？\n正在播放的视频不受影响；下次播放 mkv 等格式将重新转封装。")) return;
  try {
    const r = await api("/api/admin/cache/clear", { method: "POST", headers: AUTH() });
    if (r && r.cleared) toast("缓存已清除", "ok");
    else toast((r && r.message) || "无需清除", "err");
  } catch (e) { toast(e.message, "err"); }
}

// 重置设置：全部恢复默认值（仅主人）
async function resetSystemSettings() {
  if (!IS_MASTER()) { toast("只有主人可以重置系统设置", "err"); return; }
  if (!confirm("确定将所有系统设置重置为默认值？\n备份全局排除规则也会恢复为默认预设（含注释分组）。")) return;
  try {
    await api("/api/admin/settings/reset", { method: "POST", headers: AUTH() });
    await loadSettings();
    toast("设置已重置为默认值", "ok");
  } catch (e) { toast(e.message, "err"); }
}

/* ---------- 目录选择（前端内置浏览器，v0.13 重构） ----------
 * 不再调用服务端系统文件选择器——那会导致手机访问时对话框弹出在服务器上。
 * 回收站 / 日志 / 权限目录三处均使用此内置浏览器选择服务器上的真实目录。 */
// 通用：打开内置目录浏览器选择一个目录，返回所选路径字符串；取消返回 null
function pickNasFolder(title) {
  return new Promise(resolve => {
    folderPickerOpen({
      title: title || "选择目录",
      onPick: async (dirId, path) => {
        if (path) resolve(path);
        else { toast("请选择具体文件夹", "err"); resolve(null); }
      }
    });
  });
}
// 回收站位置选择
async function pickTrashDir() {
  if (!IS_MASTER()) { toast("只有主人可以修改系统设置", "err"); return; }
  const p = await pickNasFolder("选择回收站位置");
  if (p) {
    $("set-trash").value = p;
    toast("已选择回收站位置：" + p, "ok");
  }
}
// 备份默认存放目录选择（设置页）
async function pickBackupDir() {
  if (!IS_MASTER()) { toast("只有主人可以修改系统设置", "err"); return; }
  const p = await pickNasFolder("选择备份默认存放目录");
  if (p) {
    $("set-bkdir").value = p;
    toast("已选择备份默认存放目录：" + p, "ok");
  }
}
// 日志位置选择：选择目录后自动拼接默认日志文件名 smart-nas.log
async function pickLogDir() {
  if (!IS_MASTER()) { toast("只有主人可以修改系统设置", "err"); return; }
  const p = await pickNasFolder("选择日志位置");
  if (p) {
    const file = p.replace(/[\\/]+$/, "") + "\\smart-nas.log";
    $("set-logpath").value = file;
    toast("已选择日志位置：" + file, "ok");
  }
}

/* ==================== 客户端：文件 ==================== */
async function refresh() {
  const kw = $("keyword").value.trim();
  const q = kw ? ("?keyword=" + encodeURIComponent(kw)) : ("?parent_id=" + PARENT);
  // 磁盘根节点（parent_id=0）只展示磁盘，不支持上传/新建/批量操作
  const atRoot = PARENT === 0 && !kw;
  console.log("[files] refresh 开始", { PARENT, kw, q, atRoot, hasToken: !!TOKEN });
  $("btn-new-folder").style.display = atRoot ? "none" : "";
  $("btn-upload").style.display = atRoot ? "none" : "";
  const diskTip = $("disk-tip"); if (diskTip) diskTip.style.display = atRoot ? "" : "none";
  clearSel();
  try {
    const files = await api("/api/files" + q, { headers: AUTH() });
    FILES = files;
    renderCrumb();
    renderFiles();
    loadStats();
  } catch (e) {
    console.error("[files] refresh 异常:", e);
    toast("加载失败：" + e.message, "err");
  }
}

function renderCrumb() {
  const el = $("crumb");
  el.innerHTML = "";
  const rootA = document.createElement("a");
  rootA.textContent = "磁盘";
  rootA.title = "真实文件系统根（磁盘列表）";
  rootA.onclick = () => {
    CRUMB = []; PARENT = 0;
    $("keyword").value = ""; // 清空残留关键词，否则会走搜索而非磁盘列表
    refresh();
  };
  el.appendChild(rootA);
  CRUMB.forEach((c, i) => {
    const sep = document.createElement("span"); sep.className = "sep"; sep.textContent = " / ";
    const a = document.createElement("a");
    a.textContent = c.name;
    if (c.path) a.title = c.path;
    a.onclick = () => {
      CRUMB = CRUMB.slice(0, i + 1);
      PARENT = c.id;
      $("keyword").value = "";
      refresh();
    };
    el.appendChild(sep); el.appendChild(a);
  });
}

/* ---------- 排序 ---------- */
function setSort(key) {
  if (SORT.key === key) SORT.asc = !SORT.asc; else { SORT.key = key; SORT.asc = true; }
  renderFiles();
}
function sortArrow(key) {
  if (SORT.key !== key) return "";
  return SORT.asc ? " ↑" : " ↓";
}
function sortedFiles() {
  const arr = FILES.slice();
  const dir = SORT.asc ? 1 : -1;
  const key = SORT.key;
  arr.sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1; // 目录始终在前
    let va = a[key], vb = b[key];
    if (key === "size") {
      va = a.is_dir ? 0 : (a.size || 0);
      vb = b.is_dir ? 0 : (b.size || 0);
    } else if (key === "updated_at") {
      va = a.updated_at || ""; vb = b.updated_at || "";
    } else {
      va = String(va || ""); vb = String(vb || "");
    }
    if (va === vb) return 0;
    const cmp = va > vb ? 1 : -1;
    return cmp * dir;
  });
  return arr;
}

/* ---------- 勾选 / 批量操作 ---------- */
const isAtRootView = () => PARENT === 0 && !$("keyword").value.trim();
function renderSel() {
  const n = Object.keys(SEL).length;
  $("sel-info").style.display = n ? "" : "none";
  $("sel-info").textContent = "已选 " + n + " 项";
  $("btn-copy").style.display = n && !isAtRootView() ? "" : "none";
  $("btn-move").style.display = n && !isAtRootView() ? "" : "none";
  $("btn-delete").style.display = n && !isAtRootView() ? "" : "none";
  $("btn-clear-sel").style.display = n ? "" : "none";
  $("batch-tip").style.display = (n === 0 && !isAtRootView() && FILES.length) ? "" : "none";
  const allSel = FILES.length > 0 && FILES.every(f => SEL[f.id]);
  const headChk = document.querySelector("#file-table th.chk input");
  if (headChk) headChk.checked = allSel;
}
function toggleSel(id, checked) {
  const f = FILES.find(x => x.id === id);
  if (!f) return;
  if (checked) SEL[id] = f; else delete SEL[id];
  renderSel();
}
function toggleAll(checked) {
  FILES.forEach(f => {
    if (f.is_dir && f.parent_id === 0) return; // 磁盘根节点不可勾选
    if (checked) SEL[f.id] = f; else delete SEL[f.id];
  });
  renderFiles();
  renderSel();
}
function clearSel() { SEL = {}; renderSel(); }

async function batchAction(action) {
  const ids = Object.keys(SEL).map(Number);
  if (!ids.length) { toast("请先勾选文件", "err"); return; }
  folderPickerOpen({
    title: action === "copy" ? "选择复制目标目录" : "选择移动目标目录",
    onPick: async (dirId) => {
      if (dirId === 0) { toast("请选择具体文件夹作为目标", "err"); return; }
      if (action === "copy") {
        await api("/api/files/copy", { method: "POST", headers: AUTH(), body: JSON.stringify({ ids, target_dir_id: dirId }) });
        toast("复制完成", "ok");
      } else {
        await api("/api/files/move", { method: "POST", headers: AUTH(), body: JSON.stringify({ ids, target_dir_id: dirId }) });
        toast("移动完成", "ok");
      }
      clearSel();
      refresh();
    }
  });
}
function batchDelete() {
  const ids = Object.keys(SEL).map(Number);
  const names = Object.values(SEL).map(f => f.name).slice(0, 5).join("、");
  if (!ids.length) { toast("请先勾选文件", "err"); return; }
  openGen("批量删除", `
    <div class="hint" style="margin:0">确定将选中的 ${ids.length} 项（${esc(names)}${ids.length > 5 ? "…" : ""}）移入回收站吗？可在回收站中恢复。</div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="danger" onclick="doBatchDelete([${ids.join(",")}])">确认删除</button></div>`);
}
async function doBatchDelete(ids) {
  try {
    await api("/api/files", { method: "DELETE", headers: AUTH(), body: JSON.stringify({ ids }) });
    toast("已移入回收站", "ok"); closeGen(); clearSel(); refresh();
  } catch (e) { toast(e.message, "err"); }
}

/* ---------- 目标目录选择器（复制/移动/回收站/备份共用，独立弹窗） ----------
 * 独立于通用弹窗 gen-modal：在备份任务弹窗之上叠加选择时不会破坏底下的表单。 */
let FPK = { onPick: null, dir: 0, crumb: [], path: "" };
function folderPickerOpen(opts) {
  FPK = { onPick: opts.onPick, dir: 0, crumb: [], path: "" };
  $("fpk-title").textContent = opts.title || "选择目录";
  $("fpk-body").innerHTML = '<div class="empty">加载中…</div>';
  $("fpk-modal").classList.add("open");
  renderFolderPicker();
}
function closeFpk() { $("fpk-modal").classList.remove("open"); }
async function renderFolderPicker() {
  try {
    const files = await api("/api/files?parent_id=" + FPK.dir, { headers: AUTH() });
    const dirs = (files || []).filter(f => f.is_dir);
    const cur = FPK.crumb[FPK.crumb.length - 1];
    const pathText = FPK.path || (cur && cur.path) || "（磁盘）";
    const crumb = FPK.crumb.map((c, i) =>
      `<a onclick="fpkNav('${c.id}', ${JSON.stringify(FPK.crumb.slice(0, i + 1)).replace(/"/g, '&quot;')})">${esc(c.name)}</a>`
    ).join(" <span style='color:var(--muted)'>/</span> ");
    const list = dirs.map(d => `
      <div class="fpk-item" title="选择目录" onclick="fpkEnter(${d.id}, '${jsStr(d.name)}', '${jsStr(d.storage_path || "")}')">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>
        <span style="flex:1">${esc(d.name)}</span><span style="color:var(--muted);font-size:12px">${esc(d.storage_path || "")}</span>
      </div>`).join("") || '<div class="empty">当前没有可进入的文件夹</div>';
    $("fpk-body").innerHTML = `
      <div class="fpk-nav">
        <a onclick="fpkRoot()">磁盘</a> ${crumb ? " <span style='color:var(--muted)'>/</span> " + crumb : ""}
        ${FPK.dir !== 0 ? `<button class="btn" onclick="fpkUp()" style="margin-left:8px">上一级</button>` : ""}
      </div>
      <div class="crumb-cur">当前路径：${esc(pathText)}</div>
      <div class="fpk-list">${list}</div>
      <div class="m-actions">
        <button onclick="closeFpk()">取消</button>
        <button class="primary" onclick="fpkChoose()">选择此目录</button>
      </div>`;
  } catch (e) { $("fpk-body").innerHTML = '<div class="empty">加载失败：' + esc(e.message) + '</div>'; }
}
async function fpkEnter(id, name, path) {
  FPK.crumb.push({ id, name, path });
  FPK.dir = id;
  FPK.path = path;
  renderFolderPicker();
}
async function fpkUp() {
  FPK.crumb.pop();
  const last = FPK.crumb[FPK.crumb.length - 1];
  FPK.dir = last ? last.id : 0;
  FPK.path = last ? last.path : "";
  renderFolderPicker();
}
async function fpkRoot() {
  FPK.crumb = []; FPK.dir = 0; FPK.path = "";
  renderFolderPicker();
}
async function fpkNav(id, crumb) {
  FPK.crumb = crumb;
  FPK.dir = id;
  const last = crumb[crumb.length - 1];
  FPK.path = last ? last.path : "";
  renderFolderPicker();
}
async function fpkChoose() {
  const fn = FPK.onPick;
  closeFpk();
  await fn(FPK.dir, FPK.path);
}

/* ---------- 文件预览 ---------- */
function previewFile(id) {
  const f = (FILES || []).find(x => x.id === id);
  if (!f || f.is_dir) return;
  const name = f.name || "";
  const imgExt = /\.(png|jpe?g|gif|webp|bmp|svg|ico)$/i;
  const txtExt = /\.(txt|md|log|go|py|js|ts|json|toml|yaml|yml|xml|html|css|conf|ini|sh|bat|c|cpp|h|java|sql)$/i;
  const url = BASE + "/api/files/" + f.id + "/download";

  // 视频 / 音频文件：直接交给抽屉播放器在线播放（v0.18 修复：此前仅提示"不支持预览"）
  if (isVideoExt(name) || isAudioExt(name)) {
    if ($("gen-modal").classList.contains("open")) closeGen();
    playVideo(id, name);
    return;
  }
  // 图片 / 文本：真正的内联预览（头部已有 ✕，底部不再放重复的"关闭"按钮）
  if (imgExt.test(name) || txtExt.test(name)) {
    $("gen-title").textContent = "预览 - " + name;
    $("gen-body").innerHTML = '<div class="empty">加载中…</div>';
    $("gen-modal").classList.add("open");
    const p = imgExt.test(name) ? fetch(url, { headers: { "Authorization": "Bearer " + TOKEN } }).then(r => r.blob()).then(blob =>
        `<img class="preview-img" src="${URL.createObjectURL(blob)}" alt="${escAttr(name)}">`
      ) : fetch(url, { headers: { "Authorization": "Bearer " + TOKEN } }).then(r => r.text()).then(t =>
        `<pre class="preview-text">${esc(t.slice(0, 200000))}</pre>`
      );
    p.then(html => {
      $("gen-body").innerHTML = html +
        `<div class="m-actions"><button class="primary" onclick="downloadFile(${f.id}, '${jsStr(f.name)}')">下载</button></div>`;
    }).catch(e => { $("gen-body").innerHTML = '<div class="empty">加载失败：' + esc(e.message) + '</div>'; });
    return;
  }
  // 无法预览的类型："详细信息"（头部已有 ✕ 关闭，不再放重复的"关闭"按钮）
  $("gen-title").textContent = "详细信息";
  $("gen-body").innerHTML = `
    <div class="form-row"><label>文件名</label><span>${esc(f.name)}</span></div>
    <div class="form-row"><label>大小</label><span>${fmtBytes(f.size)}</span></div>
    <div class="form-row"><label>类型</label><span>${esc(f.mime_type || "-")}</span></div>
    <div class="form-row"><label>MD5</label><span>${esc(f.md5 || "-")}</span></div>
    <div class="m-actions"><button class="primary" onclick="downloadFile(${f.id}, '${jsStr(f.name)}')">下载</button></div>`;
  $("gen-modal").classList.add("open");
}

function renderFiles() {
  const el = $("file-table");
  if (!FILES.length) {
    const msg = isAtRootView() ? "未发现可用磁盘（可在 config.toml 的 storage.disks 配置，或连接真实磁盘）" : "此目录为空";
    el.innerHTML = '<div class="empty">' + msg + '</div>';
    return;
  }
  const rows = sortedFiles().map(f => {
    const pathTip = f.is_dir && f.storage_path ? ` title="${escAttr(f.storage_path)}"` : "";
    const checked = SEL[f.id] ? "checked" : "";
    const isDiskNode = f.is_dir && f.parent_id === 0; // 磁盘根节点：仅可进入，不可勾选/操作
    const nameCell = f.is_dir
      ? `<span class="fname"${pathTip} onclick="openDir(${f.id}, '${jsStr(f.name)}', '${jsStr(f.storage_path || "")}')">📁 <span>${esc(f.name)}</span></span>`
      : `<span class="fname"${pathTip} onclick="previewFile(${f.id})">📄 <span>${esc(f.name)}</span></span>`;
    return `
    <tr data-id="${f.id}">
      <td class="chk">${isDiskNode ? "" : `<input type="checkbox" ${checked} onclick="toggleSel(${f.id}, this.checked)">`}</td>
      <td class="col-name">${nameCell}</td>
      <td class="col-type">${f.is_dir ? (f.storage_path ? "磁盘/文件夹" : "文件夹") : fileTypeName(f.name)}</td>
      <td>${f.is_dir ? "-" : fmtBytes(f.size)}</td>
      <td class="col-md5">${f.is_dir ? "-" : (f.md5 ? f.md5.slice(0, 8) + "…" : "-")}</td>
      <td class="col-time">${fmtTime(f.updated_at)}</td>
      <td class="actions">
        <button onclick="showDetail(${f.id})">详情</button>
        ${!f.is_dir && !isDiskNode && (isVideoExt(f.name) || isAudioExt(f.name)) ? `<button class="btn-play" onclick="playVideo(${f.id}, '${jsStr(f.name)}')">播放</button>` : ''}
        ${f.is_dir || isDiskNode ? '' : `<button onclick="downloadFile(${f.id}, '${jsStr(f.name)}')">下载</button>`}
        ${f.is_dir || isDiskNode ? '' : `<button onclick="shareFile(${f.id}, '${jsStr(f.name)}')">分享</button>`}
        ${f.is_dir || isDiskNode ? '' : `<button onclick="openVersions(${f.id}, '${jsStr(f.name)}')">版本</button>`}
        ${isDiskNode ? `<span style="color:var(--muted)">磁盘根目录</span>` : `<button onclick="renameFile(${f.id}, '${jsStr(f.name)}')">重命名</button>`}
        ${isDiskNode ? '' : `<button class="del" onclick="askDeleteFile(${f.id}, '${jsStr(f.name)}')">删除</button>`}
      </td>
    </tr>`;
  }).join("");
  el.innerHTML = `<table><thead><tr>
      <th class="chk"><input type="checkbox" onclick="toggleAll(this.checked)"></th>
      <th class="sortable" onclick="setSort('name')">名称${sortArrow("name")}</th>
      <th class="col-type">类型</th>
      <th class="sortable" onclick="setSort('size')">大小${sortArrow("size")}</th>
      <th class="col-md5">MD5</th>
      <th class="col-time sortable" onclick="setSort('updated_at')">修改时间${sortArrow("updated_at")}</th>
      <th>操作</th>
    </tr></thead><tbody>${rows}</tbody></table>`;
  renderSel();
  initVideoInfo();
}

// 文件/文件夹详细信息弹窗（v0.14；v0.19 起从详情接口获取真实路径）
async function showDetail(id) {
  let f = (FILES || []).find(x => x.id === id);
  if (!f) { toast("未找到该条目", "err"); return; }
  // 列表接口对文件脱敏（storage_path 为空），详情接口已做读权限校验并返回真实路径
  try {
    const d = await api("/api/files/" + id, { headers: AUTH() });
    if (d) f = d;
  } catch (e) { /* 详情接口失败时使用列表缓存 */ }
  const rows = [
    ["名称", esc(f.name)],
    ["类型", f.is_dir ? (f.parent_id === 0 ? "磁盘根目录" : "文件夹") : fileTypeName(f.name)],
    ["大小", f.is_dir ? "-" : fmtBytes(f.size)],
    ["路径", f.storage_path ? `<span style="word-break:break-all">${esc(f.storage_path)}</span>` : "-"],
    ["MD5", f.is_dir ? "-" : (f.md5 || "-")],
    ["修改时间", fmtTime(f.updated_at)],
  ];
  const body = rows.map(r => `<div class="form-row"><label>${r[0]}</label><div style="flex:1;min-width:0;font-size:13px">${r[1]}</div></div>`).join("");
  // 底部操作：视频提供「播放」；文件提供「下载」；不再放重复的「关闭」（头部已有 ✕）
  let actions = "";
  if (!f.is_dir) {
    if (isVideoExt(f.name) || isAudioExt(f.name)) actions += `<button class="primary" onclick="closeGen();playVideo(${f.id}, '${jsStr(f.name)}')">播放</button>`;
    actions += `<button onclick="downloadFile(${f.id}, '${jsStr(f.name)}')">下载</button>`;
  }
  openGen("详细信息", body + (actions ? `<div class="m-actions">${actions}</div>` : ""));
}

function openDir(id, name, path) {
  CRUMB.push({ id, name: name || ("目录 " + id), path: path || "" });
  PARENT = id;
  $("keyword").value = ""; // 进入目录时清空搜索关键词
  refresh();
}

async function loadStats() {
  try {
    const s = await api("/api/storage/stats", { headers: AUTH() });
    $("st-files").textContent = s.total_files;
    $("st-used").textContent = fmtBytes(s.used_bytes);
    $("st-version").textContent = fmtBytes(s.version_bytes);
    $("st-trash").textContent = s.trash_files + " 项 / " + fmtBytes(s.trash_bytes);
  } catch (e) { /* 忽略 */ }
}

function newFolder() {
  openGen("新建文件夹", `
    <div class="form-row"><label>文件夹名称</label><input type="text" id="gf-name" placeholder="请输入名称"></div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="primary" onclick="doNewFolder()">创建</button></div>`);
  setTimeout(() => { const el = $("gf-name"); if (el) el.focus(); }, 50);
}
async function doNewFolder() {
  const name = $("gf-name").value.trim();
  if (!name) { toast("请输入文件夹名称", "err"); return; }
  try {
    await api("/api/files/mkdir", { method: "POST", headers: AUTH(), body: JSON.stringify({ name, parent_id: PARENT }) });
    toast("已创建", "ok"); closeGen(); refresh();
  } catch (e) { toast(e.message, "err"); }
}

function askDeleteFile(id, name) {
  openGen("删除文件", `
    <div class="hint" style="margin:0">确定将 “${esc(name)}” 移入回收站吗？可在回收站中恢复。</div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="danger" onclick="doDeleteFile(${id})">确认删除</button></div>`);
}
async function doDeleteFile(id) {
  try {
    await api("/api/files", { method: "DELETE", headers: AUTH(), body: JSON.stringify({ ids: [id] }) });
    toast("已移入回收站", "ok"); closeGen(); refresh();
  } catch (e) { toast(e.message, "err"); }
}

function renameFile(id, name) {
  openGen("重命名", `
    <div class="form-row"><label>新名称</label><input type="text" id="gf-rename" value="${escAttr(name)}"></div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="primary" onclick="doRename(${id})">保存</button></div>`);
  setTimeout(() => { const el = $("gf-rename"); if (el) { el.focus(); el.select(); } }, 50);
}
async function doRename(id) {
  const name = $("gf-rename").value.trim();
  if (!name) { toast("名称不能为空", "err"); return; }
  try {
    await api("/api/files/" + id + "/rename", { method: "PUT", headers: AUTH(), body: JSON.stringify({ name }) });
    toast("已重命名", "ok"); closeGen(); refresh();
  } catch (e) { toast(e.message, "err"); }
}

function shareFile(id, name) {
  openGen("分享文件 - " + (name || ""), `
    <div class="form-row"><label>有效期（小时）</label><input type="number" id="sf-hours" value="0" min="0"></div>
    <div class="form-row"><label>访问密码（可选）</label><input type="text" id="sf-pwd" placeholder="留空为无密码"></div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="primary" onclick="doShare(${id})">生成分享</button></div>`);
}
async function doShare(id) {
  const hours = parseInt($("sf-hours").value) || 0;
  const pwd = $("sf-pwd").value || "";
  try {
    const d = await api("/api/files/" + id + "/share", {
      method: "POST", headers: AUTH(), body: JSON.stringify({ expire_hours: hours, password: pwd })
    });
    const url = location.origin + d.url;
    if (navigator.clipboard) { try { await navigator.clipboard.writeText(url); } catch (e) {} }
    closeGen();
    openGen("分享链接", `
      <div class="hint">已生成分享链接，可粘贴到浏览器打开（设置密码时需附带 ?password=xxx）</div>
      <input type="text" style="width:100%;padding:8px 10px;border-radius:8px;border:1px solid var(--line);background:var(--bg);color:var(--text)" readonly onclick="this.select()" value="${escAttr(url)}">
      <div class="m-actions"><button class="primary" onclick="closeGen()">关闭</button></div>`);
    toast("分享链接已生成", "ok");
  } catch (e) { toast(e.message, "err"); }
}

async function downloadFile(id, name) {
  try {
    const resp = await fetch(BASE + "/api/files/" + id + "/download", { headers: { "Authorization": "Bearer " + TOKEN } });
    if (!resp.ok) throw new Error("HTTP " + resp.status);
    const blob = await resp.blob();
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.click();
    URL.revokeObjectURL(a.href);
  } catch (e) { toast("下载失败：" + e.message, "err"); }
}

/* ---------- 历史版本 ---------- */
async function openVersions(fileId, name) {
  $("vm-title").textContent = "历史版本 - " + name;
  $("vm-body").innerHTML = '<div class="loading">加载中…</div>';
  $("version-modal").classList.add("open");
  try {
    const versions = await api("/api/files/" + fileId + "/versions", { headers: AUTH() });
    if (!versions.length) { $("vm-body").innerHTML = '<div class="empty">暂无历史版本</div>'; return; }
    const rows = versions.map(v => `
      <tr>
        <td>v${v.version}</td>
        <td>${fmtBytes(v.size)}</td>
        <td>${esc(v.md5 || "-")}</td>
        <td>${fmtTime(v.created_at)}</td>
      </tr>`).join("");
    $("vm-body").innerHTML = `<table><thead><tr><th>版本</th><th>大小</th><th>MD5</th><th>时间</th></tr></thead><tbody>${rows}</tbody></table>`;
  } catch (e) {
    $("vm-body").innerHTML = '<div class="empty">加载失败：' + esc(e.message) + '</div>';
  }
}
function closeModal() { $("version-modal").classList.remove("open"); }

/* ---------- 上传（tus 单块） ---------- */
function onFilesSelected(input) {
  const files = Array.from(input.files || []);
  input.value = "";
  files.forEach(f => uploadOne(f));
}

async function uploadOne(file) {
  toast("开始上传：" + file.name);
  try {
    // 1) 初始化（秒传检测）
    const init = await api("/api/files/upload/init", {
      method: "POST", headers: AUTH(),
      body: JSON.stringify({ filename: file.name, size: file.size, md5: "", parent_id: PARENT })
    });
    if (init.dedup) { toast("秒传成功：" + file.name, "ok"); refresh(); return; }

    const url = init.upload_url;
    const meta = `filename ${b64utf8(file.name)},parent_id ${b64utf8(String(PARENT))}`;
    const tus = { "Tus-Resumable": "1.0.0" };

    // 2) 创建会话
    let resp = await fetch(BASE + url, {
      method: "POST",
      headers: Object.assign(tus, { "Upload-Length": file.size, "Upload-Metadata": meta, "Authorization": "Bearer " + TOKEN })
    });
    if (!resp.ok) throw new Error("创建上传会话失败 HTTP " + resp.status);
    if (resp.headers.get("X-Upload-Dedup") === "true") {
      toast("秒传成功：" + file.name, "ok"); refresh(); return;
    }

    // 3) 上传数据（单块）
    resp = await fetch(BASE + url, {
      method: "PATCH",
      headers: Object.assign(tus, { "Upload-Offset": "0", "Content-Type": "application/offset+octet-stream", "Authorization": "Bearer " + TOKEN }),
      body: file
    });
    if (!resp.ok) throw new Error("上传失败 HTTP " + resp.status);
    toast("上传完成：" + file.name, "ok");
    refresh();
  } catch (e) {
    toast("上传失败 " + file.name + "：" + e.message, "err");
  }
}

/* ==================== 客户端：回收站 ==================== */
let TRASH = [];   // 回收站列表缓存
let TSEL = {};    // 勾选的 id → file
async function loadTrash() {
  try {
    TRASH = await api("/api/files/trash", { headers: AUTH() });
    TSEL = {}; // 刷新后清空陈旧选择
    renderTrash();
  } catch (e) { toast("加载失败：" + e.message, "err"); }
}
function renderTrash() {
  const files = TRASH;
  const el = $("trash-table");
  if (!files.length) { el.innerHTML = '<div class="empty">回收站为空</div>'; renderTrashSel(); return; }
  const rows = files.map(f => `
    <tr>
      <td class="chk"><input type="checkbox" ${TSEL[f.id] ? "checked" : ""} onclick="toggleTrashSel(${f.id}, this.checked)"></td>
      <td class="col-name" style="word-break:break-all">${f.is_dir ? "📁" : "📄"} ${esc(f.name)}</td>
      <td class="col-type">${f.is_dir ? "文件夹" : esc(fileTypeName(f.name))}</td>
      <td>${f.is_dir ? "-" : fmtBytes(f.size)}</td>
      <td class="col-time">${fmtTime(f.deleted_at)}</td>
      <td class="actions">
        <button onclick="doRestoreTrash([${f.id}])">还原</button>
        <button class="del" onclick="askPurgeTrash([${f.id}], '${jsStr(f.name)}')">删除</button>
      </td>
    </tr>`).join("");
  el.innerHTML = `<table><thead><tr>
      <th class="chk"><input type="checkbox" onclick="toggleAllTrashSel(this.checked)"></th>
      <th>名称</th><th class="col-type">类型</th><th>大小</th><th class="col-time">删除时间</th><th>操作</th>
    </tr></thead><tbody>${rows}</tbody></table>`;
  renderTrashSel();
}
function toggleTrashSel(id, checked) {
  const f = TRASH.find(x => x.id === id);
  if (!f) return;
  if (checked) TSEL[id] = f; else delete TSEL[id];
  renderTrashSel();
}
function toggleAllTrashSel(checked) {
  TRASH.forEach(f => { if (checked) TSEL[f.id] = f; else delete TSEL[f.id]; });
  renderTrash();
}
function clearTrashSel() { TSEL = {}; renderTrash(); }
function renderTrashSel() {
  const n = Object.keys(TSEL).length;
  $("trash-sel-info").style.display = n ? "" : "none";
  $("trash-sel-info").textContent = "已选 " + n + " 项";
  ["btn-trash-restore", "btn-trash-purge", "btn-trash-clearsel"].forEach(id => { $(id).style.display = n ? "" : "none"; });
  const tip = $("trash-tip"); if (tip) tip.style.display = n ? "none" : "";
}
// 还原（单个/批量共用）
async function doRestoreTrash(ids) {
  try {
    await api("/api/files/restore", { method: "POST", headers: AUTH(), body: JSON.stringify({ ids }) });
    toast("已还原 " + ids.length + " 项", "ok"); TSEL = {}; loadTrash();
  } catch (e) { toast(e.message, "err"); }
}
function doBatchRestoreTrash() {
  const ids = Object.keys(TSEL).map(Number);
  if (!ids.length) { toast("请先勾选要还原的文件", "err"); return; }
  doRestoreTrash(ids);
}
// 物理删除（单个/批量共用，二次确认）
function askPurgeTrash(ids, name) {
  const label = ids.length === 1 ? "“" + name + "”" : ids.length + " 项";
  openGen("删除回收站文件", `
    <div class="hint" style="margin:0">确定永久删除 ${label} 吗？该操作不可恢复！</div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="danger" onclick="doPurgeTrash([${ids.join(",")}])">确认删除</button></div>`);
}
function askBatchPurgeTrash() {
  const ids = Object.keys(TSEL).map(Number);
  if (!ids.length) { toast("请先勾选要删除的文件", "err"); return; }
  askPurgeTrash(ids, "");
}
async function doPurgeTrash(ids) {
  try {
    await api("/api/files/trash/purge", { method: "POST", headers: AUTH(), body: JSON.stringify({ ids }) });
    toast("已永久删除 " + ids.length + " 项", "ok"); closeGen(); TSEL = {}; loadTrash();
  } catch (e) { toast(e.message, "err"); }
}
// 一键还原全部
function askRestoreAllTrash() {
  if (!TRASH.length) { toast("回收站为空", "err"); return; }
  openGen("一键还原", `
    <div class="hint" style="margin:0">确定将回收站全部 ${TRASH.length} 项还原到原位置吗？</div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="primary" onclick="doRestoreAllTrash()">全部还原</button></div>`);
}
async function doRestoreAllTrash() {
  try {
    const d = await api("/api/files/trash/restore-all", { method: "POST", headers: AUTH() });
    toast("已还原 " + (d.restored || 0) + " 项", "ok"); closeGen(); loadTrash();
  } catch (e) { toast(e.message, "err"); }
}
// 一键清空
function askClearTrash() {
  if (!TRASH.length) { toast("回收站为空", "err"); return; }
  openGen("清空回收站", `
    <div class="hint" style="margin:0">确定清空回收站全部 ${TRASH.length} 项吗？该操作不可恢复！</div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="danger" onclick="doClearTrash()">确认清空</button></div>`);
}
async function doClearTrash() {
  try {
    const d = await api("/api/files/trash/clear", { method: "POST", headers: AUTH() });
    toast("已清空 " + (d.cleared || 0) + " 项", "ok"); closeGen(); TSEL = {}; loadTrash();
  } catch (e) { toast(e.message, "err"); }
}

/* ==================== 管理端：用户 ==================== */
async function adminCreateUser() {
  const username = $("nu-name").value.trim();
  const password = $("nu-pass").value;
  if (!username || !password) { toast("用户名和密码必填", "err"); return; }
  const role = $("nu-role").value;
  if (role === "admin" && !IS_MASTER()) { toast("只有主人才可创建管理员账号", "err"); return; }
  try {
    await api("/api/admin/users", {
      method: "POST", headers: AUTH(),
      body: JSON.stringify({ username, password, role, permissions: [] })
    });
    $("nu-name").value = ""; $("nu-pass").value = "";
    toast("用户已创建，请为其配置目录权限", "ok"); adminLoadUsers();
  } catch (e) { toast(e.message, "err"); }
}

async function adminLoadUsers() {
  try {
    const users = await api("/api/admin/users", { headers: AUTH() });
    adminUsersCache = users;
    renderUsers(users);
  } catch (e) { toast("加载失败：" + e.message, "err"); }
}
function renderUsers(users) {
  const el = $("users-table");
  if (!users.length) { el.innerHTML = '<div class="empty">暂无用户</div>'; return; }
  const rows = users.map(u => {
    // 主人与管理各自的可见操作：主人可管理除自己外的所有用户；管理员仅能管理普通用户
    const isSelf = u.id === MyID();
    const canEdit = (IS_MASTER() && u.role !== "master") || (ROLE === "admin" && u.role === "user");
    const perms = (u.permissions || []).map(p =>
      `<span class="tag">${esc(p.path)} <span style="color:${p.write ? "var(--ok)" : "var(--muted)"}">${p.write ? "读写" : (p.read ? "只读" : "无")}</span></span>`
    ).join(" ") || '<span style="color:var(--muted)">全量</span>';
    const roleTag = `<span class="tag ${u.role === 'master' ? 'master' : u.role === 'admin' ? 'admin' : ''}">${ROLE_LABEL(u.role)}</span>`;
    return `
    <tr>
      <td>${esc(u.username)}${isSelf ? ' <span style="color:var(--muted);font-size:12px">(我)</span>' : ''}</td>
      <td>${roleTag}</td>
      <td style="max-width:340px">${perms}</td>
      <td>${fmtTime(u.created_at)}</td>
      <td class="actions">
        ${canEdit ? `<button onclick="adminSetRole(${u.id}, '${jsStr(u.role)}', '${jsStr(u.username)}')">角色</button>` : ''}
        ${canEdit ? `<button onclick="openPermModal(${u.id}, '${jsStr(u.username)}')">权限</button>` : ''}
        ${canEdit ? `<button onclick="adminResetPwd(${u.id}, '${jsStr(u.username)}')">重置密码</button>` : ''}
        ${canEdit ? `<button class="del" onclick="askDeleteUser(${u.id}, '${jsStr(u.username)}')">删除</button>` : ''}
      </td>
    </tr>`;
  }).join("");
  el.innerHTML = `<table><thead><tr><th>用户名</th><th>角色</th><th>目录权限</th><th>创建时间</th><th>操作</th></tr></thead><tbody>${rows}</tbody></table>`;
}
function MyID() {
  // 从登录接口获取当前用户 ID（首次登录时缓存）
  if (window._myid !== undefined) return window._myid;
  api("/api/auth/me", { headers: AUTH() }).then(u => { window._myid = u.id; renderUsers(adminUsersCache || []); }).catch(() => {});
  return -1;
}
let adminUsersCache = null;

function adminSetRole(id, cur, name) {
  const opts = ["user"];
  if (IS_MASTER()) opts.push("admin");
  const options = opts.map(r => `<option value="${r}" ${r === cur ? "selected" : ""}>${ROLE_LABEL(r)}</option>`).join("");
  openGen("修改角色 - " + name, `
    <div class="form-row"><label>角色</label><select id="gf-role" style="flex:1;padding:8px 10px;border-radius:8px;border:1px solid var(--line);background:var(--bg);color:var(--text)">${options}</select></div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="primary" onclick="doSetRole(${id})">保存</button></div>`);
}
async function doSetRole(id) {
  const role = $("gf-role").value;
  try {
    await api("/api/admin/users/" + id, { method: "PUT", headers: AUTH(), body: JSON.stringify({ role }) });
    toast("已更新", "ok"); closeGen(); adminLoadUsers();
  } catch (e) { toast(e.message, "err"); }
}
function adminResetPwd(id, name) {
  openGen("重置密码 - " + name, `
    <div class="form-row"><label>新密码</label><input type="password" id="gf-pwd" placeholder="请输入新密码"></div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="primary" onclick="doResetPwd(${id})">保存</button></div>`);
  setTimeout(() => { const el = $("gf-pwd"); if (el) el.focus(); }, 50);
}
async function doResetPwd(id) {
  const pwd = $("gf-pwd").value;
  if (!pwd) { toast("密码不能为空", "err"); return; }
  try {
    await api("/api/admin/users/" + id, { method: "PUT", headers: AUTH(), body: JSON.stringify({ password: pwd }) });
    toast("密码已重置", "ok"); closeGen();
  } catch (e) { toast(e.message, "err"); }
}
function askDeleteUser(id, name) {
  openGen("删除用户", `
    <div class="hint" style="margin:0">确定删除用户 “${esc(name)}” 吗？该操作不可恢复。</div>
    <div class="m-actions"><button onclick="closeGen()">取消</button><button class="danger" onclick="doDeleteUser(${id})">确认删除</button></div>`);
}
async function doDeleteUser(id) {
  try {
    await api("/api/admin/users/" + id, { method: "DELETE", headers: AUTH() });
    toast("已删除", "ok"); closeGen(); adminLoadUsers();
  } catch (e) { toast(e.message, "err"); }
}

/* ==================== 管理端：目录权限 ==================== */
let PERM_USER_ID = 0;
let PERM_ITEMS = [];

function openPermModal(id, name) {
  PERM_USER_ID = id;
  $("perm-title").textContent = "目录权限 - " + name;
  PERM_ITEMS = [];
  // 通过 API 查询最新权限
  api("/api/admin/users", { headers: AUTH() }).then(users => {
    const u = (users || []).find(x => x.id === id);
    PERM_ITEMS = ((u && u.permissions) || []).map(p => ({ path: p.path, read: !!p.read, write: !!p.write }));
    renderPermRows();
  }).catch(e => toast(e.message, "err"));
  $("perm-modal").classList.add("open");
}

function renderPermRows() {
  const el = $("perm-rows");
  if (!PERM_ITEMS.length) {
    el.innerHTML = '<div class="empty">未配置权限：该用户可访问全部目录</div>' +
      '<div style="text-align:center;margin-top:8px"><button class="btn" onclick="addPermRow()">+ 添加目录</button></div>';
    return;
  }
  el.innerHTML = `<table><thead><tr><th style="width:45%">目录路径</th><th>读</th><th>写</th><th></th></tr></thead><tbody>` +
    PERM_ITEMS.map((p, i) => `
      <tr>
        <td>
          <div style="display:flex;gap:6px;align-items:center">
            <input type="text" value="${escAttr(p.path)}" data-idx="${i}" data-key="path" class="perm-input" placeholder="如 D:/ 或 D:/photo" style="flex:1;min-width:120px;padding:6px 8px;border-radius:6px;border:1px solid var(--line);background:var(--bg);color:var(--text)">
            <button class="btn icon-btn" onclick="pickPermPath(${i})" title="选择目录" aria-label="选择目录"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg></button>
          </div>
        </td>
        <td><input type="checkbox" ${p.read ? "checked" : ""} data-idx="${i}" data-key="read"></td>
        <td><input type="checkbox" ${p.write ? "checked" : ""} data-idx="${i}" data-key="write"></td>
        <td class="actions"><button class="del" onclick="removePermRow(${i})">移除</button></td>
      </tr>`).join("") + `</tbody></table>`;
  el.querySelectorAll(".perm-input").forEach(inp => {
    inp.addEventListener("change", () => {
      const idx = +inp.dataset.idx;
      PERM_ITEMS[idx].path = inp.value;
    });
  });
  el.querySelectorAll('input[type=checkbox]').forEach(cb => {
    cb.addEventListener("change", () => {
      const idx = +cb.dataset.idx;
      PERM_ITEMS[idx][cb.dataset.key] = cb.checked;
    });
  });
}

function addPermRow() {
  PERM_ITEMS.push({ path: "", read: true, write: false });
  renderPermRows();
}
// 权限目录选择：内置目录浏览器
async function pickPermPath(i) {
  const p = await pickNasFolder("选择目录");
  if (p) {
    PERM_ITEMS[i].path = p;
    renderPermRows();
  }
}
function removePermRow(i) {
  PERM_ITEMS.splice(i, 1);
  renderPermRows();
}
function closePermModal() {
  $("perm-modal").classList.remove("open");
  PERM_USER_ID = 0;
}
async function savePerms() {
  const perms = [];
  for (const item of PERM_ITEMS) {
    const raw = String(item.path || "").trim();
    if (!raw) continue;
    const norm = normalizeWinPath(raw);
    if (!isValidWinPath(norm)) {
      toast("目录路径不合法（应形如 Y:\\catalog）：" + raw, "err");
      return;
    }
    perms.push({ path: norm, read: !!item.read, write: !!item.write });
  }
  try {
    await api("/api/admin/users/" + PERM_USER_ID + "/permissions", {
      method: "PUT", headers: AUTH(), body: JSON.stringify({ permissions: perms })
    });
    toast("权限已保存", "ok");
    closePermModal();
    adminLoadUsers();
  } catch (e) { toast(e.message, "err"); }
}

/* ==================== 管理端：插件 ==================== */
async function loadPlugins() {
  try {
    const plugins = await api("/api/plugins", { headers: AUTH() });
    renderPlugins(plugins);
  } catch (e) { toast("加载失败：" + e.message, "err"); }
}
function renderPlugins(plugins) {
  const el = $("plugins-table");
  if (!plugins.length) { el.innerHTML = '<div class="empty">未安装插件（插件系统启用后，将插件目录放入 plugins/ 并重启生效）</div>'; return; }
  const rows = plugins.map(p => `
    <tr>
      <td>${esc(p.name || p.id)}</td>
      <td>${esc(p.id)}</td>
      <td><span class="tag ${p.enabled ? 'on' : 'off'}">${p.enabled ? "已启用" : "已停用"}</span></td>
      <td>${esc(p.version || "-")}</td>
      <td class="actions">
        ${p.enabled
          ? `<button onclick="adminPluginAct('${jsStr(p.id)}','disable')">停用</button>`
          : `<button onclick="adminPluginAct('${jsStr(p.id)}','enable')">启用</button>`}
        <button onclick="adminPluginAct('${jsStr(p.id)}','reload')">重载</button>
      </td>
    </tr>`).join("");
  el.innerHTML = `<table><thead><tr><th>名称</th><th>ID</th><th>状态</th><th>版本</th><th>操作</th></tr></thead><tbody>${rows}</tbody></table>`;
}
async function adminPluginAct(id, action) {
  try {
    await api("/api/plugins/" + id + "/" + action, { method: "POST", headers: AUTH() });
    toast(action === "enable" ? "已启用" : action === "disable" ? "已停用" : "已重载", "ok");
    loadPlugins();
  } catch (e) { toast(e.message, "err"); }
}

/* ==================== 备份还原（v0.20） ==================== */
const TRIGGER_LABEL = { manual: "手动", timer: "定时", realtime: "USB 实时", interval: "间隔" };
const STATUS_LABEL = { success: "成功", partial: "部分成功", failed: "失败", conflict: "待确认冲突" };
let BACKUP_TASKS = [];
let BACKUP_HISTORY_TASK = 0;

async function loadBackupTasks() {
  try {
    BACKUP_TASKS = (await api("/api/backup/tasks", { headers: AUTH() })) || [];
    renderBackupTasks();
    $("btn-new-task").style.display = IS_PRI() ? "" : "none";
  } catch (e) {
    const el = $("backup-tasks-table");
    if (el) el.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
  }
}
function renderBackupTasks() {
  const el = $("backup-tasks-table");
  if (!BACKUP_TASKS.length) { el.innerHTML = '<div class="empty">还没有备份任务。点击「创建备份任务」开始（仅主人/管理员）</div>'; return; }
  const rows = BACKUP_TASKS.map(t => `
    <tr>
      <td>${esc(t.task_name)}</td>
      <td title="${esc((t.source_paths || []).join("、"))}">${esc((t.source_paths || []).join("、"))}</td>
      <td title="${esc(t.output_dir)}">${esc(t.output_dir)}</td>
      <td><span class="tag ${t.enabled ? 'on' : 'off'}">${t.enabled ? "启用" : "停用"}</span></td>
      <td>${TRIGGER_LABEL[t.trigger_mode] || esc(t.trigger_mode)}${t.cron_expr ? "（" + esc(t.cron_expr) + "）" : ""}${t.usb_device_id ? "（" + esc(t.usb_device_id) + "）" : ""}</td>
      <td>${t.enable_compress ? "zip L" + (t.compress_level || 6) : "目录"}</td>
      <td class="actions">
        <button onclick="showBackupHistory(${t.task_id},'${jsStr(t.task_name)}')">历史</button>
        ${IS_PRI() ? `
          <button onclick="runBackupTask(${t.task_id})">立即备份</button>
          <button onclick="showBackupTaskModal(${t.task_id})">编辑</button>
          <button class="danger" onclick="deleteBackupTask(${t.task_id},'${jsStr(t.task_name)}')">删除</button>` : ""}
      </td>
    </tr>`).join("");
  el.innerHTML = `<table><thead><tr><th>任务名</th><th>备份源</th><th>存放目录</th><th>状态</th><th>触发</th><th>压缩</th><th>操作</th></tr></thead><tbody>${rows}</tbody></table>`;
}
async function runBackupTask(id) {
  openBackupProgress(); // 触发备份后自动展开进度面板
  try {
    const h = await api(`/api/backup/tasks/${id}/run`, { method: "POST", headers: AUTH() });
    toast(`备份完成：${STATUS_LABEL[h.status] || h.status}${h.remark ? "（" + h.remark + "）" : ""}`, h.status === "success" ? "ok" : "err");
    if (BACKUP_HISTORY_TASK === id) showBackupHistory(id, $("backup-history-title").textContent.replace("备份历史：", ""));
  } catch (e) { toast(e.message, "err"); }
}

/* ---------- 任务进度（v0.21）：百分比进度条 + 实时执行日志 ---------- */
const PROG_PHASE_LABEL = { prescan: "扫描中", copying: "备份中", zipping: "压缩中", hashing: "校验中", finished: "已结束" };
const PROG_STATUS_LABEL = { running: "运行中", success: "成功", partial: "部分成功", failed: "失败" };
let BK_PROG_TIMER = null;
let BK_PROG_OPEN = {};   // taskID -> 详情是否展开
let BK_PROG_LAST = {};   // taskID -> 上次轮询到的状态（检测运行结束，自动刷新列表/历史）

function openBackupProgress() {
  const panel = $("backup-progress-panel");
  if (!panel) return;
  if (panel.style.display === "none") toggleBackupProgress();
}
function toggleBackupProgress() {
  const panel = $("backup-progress-panel");
  if (!panel) return;
  const show = panel.style.display === "none";
  panel.style.display = show ? "" : "none";
  const btn = $("btn-bk-progress");
  if (btn) btn.classList.toggle("primary", show);
  if (show) {
    pollBackupProgress();
    if (!BK_PROG_TIMER) BK_PROG_TIMER = setInterval(pollBackupProgress, 1500);
  } else if (BK_PROG_TIMER) {
    clearInterval(BK_PROG_TIMER);
    BK_PROG_TIMER = null;
  }
}
async function pollBackupProgress() {
  const panel = $("backup-progress-panel");
  if (!panel || panel.style.display === "none") return;
  let list = [];
  try { list = (await api("/api/backup/progress", { headers: AUTH() })) || []; } catch (e) { return; }
  // 检测运行状态变化：任务从 running → 结束时，刷新任务列表与已打开的历史
  let finished = false;
  list.forEach(p => {
    const prev = BK_PROG_LAST[p.task_id];
    if (prev === "running" && p.status !== "running") finished = true;
    BK_PROG_LAST[p.task_id] = p.status;
  });
  if (finished) {
    loadBackupTasks();
    if (BACKUP_HISTORY_TASK) showBackupHistory(BACKUP_HISTORY_TASK, $("backup-history-title").textContent.replace("备份历史：", ""));
  }
  renderBackupProgress(list);
}
function toggleProgDetail(taskID) {
  BK_PROG_OPEN[taskID] = !BK_PROG_OPEN[taskID];
  pollBackupProgress();
}
function renderBackupProgress(list) {
  const panel = $("backup-progress-panel");
  if (!panel) return;
  if (!list.length) {
    panel.innerHTML = '<div class="empty" style="margin-bottom:10px">暂无执行记录。点击任务行的「立即备份」或等待定时触发后，这里会实时显示进度与日志</div>';
    return;
  }
  panel.innerHTML = list.map(p => {
    const running = p.status === "running";
    const statusTag = running ? `<span class="tag run">● ${PROG_STATUS_LABEL[p.status]}</span>`
      : `<span class="tag ${p.status === "success" ? "on" : p.status === "failed" ? "off" : ""}">${PROG_STATUS_LABEL[p.status] || esc(p.status)}</span>`;
    const pctText = p.percent >= 0 ? Math.min(100, p.percent).toFixed(1) + "%" : "…";
    const barFill = p.percent >= 0
      ? `<i style="width:${Math.min(100, p.percent)}%"></i>`
      : `<i></i>`;
    const bar = running && p.percent < 0 ? `<div class="bk-prog-bar indet">${barFill}</div>` : `<div class="bk-prog-bar">${barFill}</div>`;
    const phase = PROG_PHASE_LABEL[p.phase] || esc(p.phase);
    const sizeText = p.total_bytes > 0 ? `${fmtBytes(p.copied_bytes)} / ${fmtBytes(p.total_bytes)}` : fmtBytes(p.copied_bytes);
    const fileText = p.files_total > 0 ? `${p.files_done} / ${p.files_total} 个文件` : `${p.files_done} 个文件`;
    const cur = running && p.current_file ? `<div class="bk-prog-meta">当前：${esc(p.current_file)}</div>` : "";
    const elapsed = fmtDuration(p.started_at, p.ended_at);
    const skipped = p.skipped > 0 ? `，跳过 ${p.skipped}` : "";
    const remark = !running && p.remark ? `<div class="bk-prog-meta" title="${esc(p.remark)}">${esc(p.remark.length > 120 ? p.remark.slice(0, 120) + "…" : p.remark)}</div>` : "";
    const open = !!BK_PROG_OPEN[p.task_id];
    const detail = open ? `<pre class="bk-prog-log">${esc((p.log || []).join("\n")) || "（暂无日志）"}</pre>` : "";
    return `
    <div class="bk-prog-card">
      <div class="bk-prog-head">
        <span class="name">${esc(p.task_name)}</span>
        ${statusTag}
        <span class="muted">${phase}</span>
        ${bar}
        <span class="pct">${running ? pctText : (p.status === "failed" ? "—" : pctText)}</span>
        <button class="btn" onclick="toggleProgDetail(${p.task_id})">${open ? "收起详情" : "展开详情"}</button>
      </div>
      <div class="bk-prog-meta">${sizeText}（${fileText}${skipped}）· 耗时 ${elapsed}</div>
      ${cur}${remark}${detail}
    </div>`;
  }).join("");
  // 展开的日志自动滚动到底部
  panel.querySelectorAll(".bk-prog-log").forEach(el => { el.scrollTop = el.scrollHeight; });
}
function fmtDuration(start, end) {
  const ms = Math.max(0, new Date(end || Date.now()).getTime() - new Date(start).getTime());
  const s = Math.floor(ms / 1000);
  if (s < 60) return s + " 秒";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} 分 ${s % 60} 秒`;
  return `${Math.floor(m / 60)} 时 ${m % 60} 分`;
}
async function deleteBackupTask(id, name) {
  if (!confirm(`确定删除任务「${name}」？历史备份记录与产物将保留。`)) return;
  try {
    await api(`/api/backup/tasks/${id}`, { method: "DELETE", headers: AUTH() });
    toast("任务已删除", "ok");
    loadBackupTasks();
  } catch (e) { toast(e.message, "err"); }
}
async function showBackupHistory(taskID, name) {
  try {
    const list = (await api(`/api/backup/tasks/${taskID}/history`, { headers: AUTH() })) || [];
    BACKUP_HISTORY_TASK = taskID;
    $("backup-history-panel").style.display = "";
    $("backup-history-title").textContent = `备份历史：${name}`;
    renderBackupHistory(list);
  } catch (e) { toast(e.message, "err"); }
}
function closeBackupHistory() {
  $("backup-history-panel").style.display = "none";
  BACKUP_HISTORY_TASK = 0;
}
function renderBackupHistory(list) {
  const el = $("backup-history-table");
  if (!list.length) { el.innerHTML = '<div class="empty">还没有备份记录</div>'; return; }
  const rows = list.map(h => `
    <tr>
      <td>${h.backup_type === "incremental" ? "增量" : "完整"}${h.parent_backup_id ? `<span class="muted"> ← #${h.parent_backup_id}</span>` : ""}</td>
      <td>${fmtTime(h.start_time)}</td>
      <td>${fmtTime(h.end_time)}</td>
      <td>${fmtBytes(h.total_size)}</td>
      <td><span class="tag ${h.status === "success" ? "on" : h.status === "failed" ? "off" : ""}" title="${esc(h.remark || "")}">${STATUS_LABEL[h.status] || esc(h.status)}</span></td>
      <td><span class="tag ${h.is_frozen ? "on" : ""}">${h.is_frozen ? "已冻结" : "正常"}</span></td>
      <td class="actions">
        <button onclick="viewBackupContents(${h.backup_id})">内容</button>
        ${IS_PRI() ? `
          <button onclick="restoreBackup(${h.backup_id})">还原</button>
          <button onclick="toggleFreeze(${h.backup_id},${!h.is_frozen})">${h.is_frozen ? "解冻" : "冻结"}</button>
          <button class="danger" onclick="deleteBackup(${h.backup_id})">删除</button>` : ""}
      </td>
    </tr>`).join("");
  el.innerHTML = `<table><thead><tr><th>类型</th><th>开始</th><th>结束</th><th>大小</th><th>状态</th><th>冻结</th><th>操作</th></tr></thead><tbody>${rows}</tbody></table>`;
}
async function viewBackupContents(backupID) {
  try {
    const entries = (await api(`/api/backup/backups/${backupID}/contents`, { headers: AUTH() })) || [];
    const items = entries.slice(0, 500).map(e => `<tr><td>${e.is_dir ? "📁" : "📄"} ${esc(e.rel_path)}</td><td>${e.is_dir ? "-" : fmtBytes(e.size)}</td></tr>`).join("");
    const more = entries.length > 500 ? `<div class="file-hint">仅显示前 500 项（共 ${entries.length} 项）</div>` : "";
    openGen("备份内容", `<div class="m-body">${more}<table><thead><tr><th>文件</th><th>大小</th></tr></thead><tbody>${items}</tbody></table></div>`);
  } catch (e) { toast(e.message, "err"); }
}
async function toggleFreeze(backupID, frozen) {
  try {
    await api(`/api/backup/backups/${backupID}/freeze`, { method: "POST", headers: AUTH(), body: JSON.stringify({ frozen }) });
    toast(frozen ? "已冻结（不参与配额，自动清理不删除）" : "已解除冻结", "ok");
    showBackupHistory(BACKUP_HISTORY_TASK, $("backup-history-title").textContent.replace("备份历史：", ""));
  } catch (e) { toast(e.message, "err"); }
}
async function deleteBackup(backupID) {
  if (!confirm(`确定删除备份 #${backupID}？产物将被物理删除，不可恢复${"（冻结的备份也可手动删除）"}。`)) return;
  try {
    await api(`/api/backup/backups/${backupID}`, { method: "DELETE", headers: AUTH() });
    toast("备份已删除", "ok");
    showBackupHistory(BACKUP_HISTORY_TASK, $("backup-history-title").textContent.replace("备份历史：", ""));
  } catch (e) { toast(e.message, "err"); }
}
// 还原弹窗：目录选择器 + 冲突策略（替代原生 prompt）
function restoreBackup(backupID) {
  const folderSvg = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>';
  openGen(`还原备份 #${backupID}`, `
    <div class="form-row"><label>还原到指定目录（留空 = 还原到原位置）</label>
      <input type="text" id="rst-target" placeholder="如 B:\\Restore">
      <button class="btn icon-btn" onclick="pickRestoreDir()" style="flex:none" title="选择目录" aria-label="选择目录">${folderSvg}</button>
    </div>
    <div class="form-row"><label>同名文件处理</label>
      <select id="rst-conflict">
        <option value="ask" selected>逐个询问</option>
        <option value="overwrite">覆盖</option>
        <option value="skip">跳过</option>
        <option value="rename">重命名旧文件</option>
      </select>
    </div>
    <div class="form-row"><label></label>
      <button class="btn" onclick="doRestore(${backupID})">确定</button>
      <button class="btn" onclick="closeGen()">取消</button>
    </div>`);
}
// 还原目标目录选择（内置目录浏览器）
async function pickRestoreDir() {
  const p = await pickNasFolder("选择还原目标目录");
  if (p) $("rst-target").value = p;
}
async function doRestore(backupID) {
  const target = String($("rst-target").value || "").trim();
  const conflict = $("rst-conflict").value;
  closeGen();
  try {
    const res = await api(`/api/backup/backups/${backupID}/restore`, {
      method: "POST", headers: AUTH(),
      body: JSON.stringify({ target_dir: normalizeWinPath(target), conflict })
    });
    if (res.status === "conflict") {
      if (confirm(`存在 ${res.conflicts.length} 个同名文件冲突：\n${res.conflicts.slice(0, 10).join("\n")}${res.conflicts.length > 10 ? "\n…" : ""}\n\n是否覆盖这些文件继续还原？`)) {
        const r2 = await api(`/api/backup/backups/${backupID}/restore`, {
          method: "POST", headers: AUTH(),
          body: JSON.stringify({ target_dir: normalizeWinPath(target), conflict: "overwrite" })
        });
        toast(`还原完成：恢复 ${r2.restored} 个文件${r2.skipped.length ? "，跳过 " + r2.skipped.length : ""}`, "ok");
      } else {
        toast("已取消还原", "ok");
      }
      return;
    }
    toast(`还原完成：恢复 ${res.restored} 个文件${res.skipped && res.skipped.length ? "，跳过 " + res.skipped.length : ""}`, res.status === "success" ? "ok" : "err");
  } catch (e) { toast(e.message, "err"); }
}

/* ---------- 创建 / 编辑任务弹窗 ---------- */
let BK_USB_DEVICES = [];
let BK_SOURCES = [];    // 备份源列表（多选，目录选择器添加）
let BK_EXCLUDES = [];   // 排除规则列表（多选）
const BK_SIZE_UNITS = ["MB", "GB", "TB"];

async function showBackupTaskModal(taskID) {
  const t = taskID ? BACKUP_TASKS.find(x => x.task_id === taskID) : null;
  const triggerOptions = ["manual", "timer", "interval", "realtime"].map(m =>
    `<option value="${m}" ${t && t.trigger_mode === m ? "selected" : ""}>${TRIGGER_LABEL[m]}</option>`).join("");
  openGen(t ? "编辑备份任务" : "创建备份任务", `
    <div class="form-row"><label>任务名称</label><input type="text" id="bk-name" value="${t ? esc(t.task_name) : ""}"></div>
    <div class="form-row"><label>备份类型</label>
      <select id="bk-btype">
        <option value="auto" ${!t || t.backup_type !== "full" ? "selected" : ""}>增量（首次完整，后续仅备份变化）</option>
        <option value="full" ${t && t.backup_type === "full" ? "selected" : ""}>完全（每次全量备份）</option>
      </select>
    </div>
    <div class="form-row" style="align-items:flex-start"><label style="padding-top:6px">备份源</label>
      <div style="flex:1;min-width:0">
        <div id="bk-src-list" class="bk-list"></div>
        <div class="bk-add-row">
          <button class="btn" onclick="bkPickSource()"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>添加目录</button>
          <input type="text" id="bk-src-input" placeholder="或输入完整路径，如 S:\\photo" onkeydown="if(event.key==='Enter'){bkAddSource();return false;}">
          <button class="btn" onclick="bkAddSource()">添加</button>
        </div>
      </div>
    </div>
    <div class="form-row" style="align-items:flex-start"><label style="padding-top:6px">排除规则</label>
      <div style="flex:1;min-width:0">
        <div id="bk-exc-list" class="bk-list"></div>
        <div class="bk-add-row">
          <input type="text" id="bk-exc-input" placeholder="文件名 / 目录名 / 通配符，如 *.tmp">
          <button class="btn" onclick="bkAddExclude()">添加规则</button>
          <button class="btn" onclick="bkClearExcludes()" title="移除全部排除规则（含预填的全局规则）；保存后该任务将不使用任何排除规则">清空</button>
        </div>
      </div>
    </div>
    <div class="form-row"><label>存放目录</label><input type="text" id="bk-outdir" value="${t ? esc(t.output_dir) : ""}" placeholder="新建任务默认使用全局设置">
      <button class="btn icon-btn" onclick="bkPickOutput()" style="flex:none" title="选择存放目录" aria-label="选择存放目录"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg></button>
    </div>
    <div class="form-row"><label>压缩</label>
      <label class="chk"><input type="checkbox" id="bk-zip" ${!t || t.enable_compress ? "checked" : ""}> 压缩为 zip（无损）</label>
      <select id="bk-level" class="bk-w-sm" title="压缩级别"></select>
    </div>
    <div class="form-row"><label>触发方式</label><select id="bk-trigger" onchange="bkTriggerChanged()">${triggerOptions}</select></div>
    <div class="form-row" id="bk-cron-row" style="display:none"><label>cron 表达式</label><input type="text" id="bk-cron" value="${t ? esc(t.cron_expr) : ""}" placeholder="分 时 日 月 周，如 0 4 * * *"></div>
    <div class="form-row" id="bk-simple-row" style="display:none"><label>简单周期</label>
      <select id="bk-unit"><option value="day">每日</option><option value="week">每周</option><option value="month">每月</option></select>
      <select id="bk-weekday" style="display:none"><option value="1">周一</option><option value="2">周二</option><option value="3">周三</option><option value="4">周四</option><option value="5">周五</option><option value="6">周六</option><option value="7">周日</option></select>
      <input type="number" id="bk-every" min="1" value="1" class="bk-w-num" title="间隔数 / 几号">
      <input type="number" id="bk-hour" min="0" max="23" value="4" class="bk-w-num" title="时">
      <input type="number" id="bk-minute" min="0" max="59" value="0" class="bk-w-num" title="分">
    </div>
    <div class="form-row" id="bk-interval-row" style="display:none"><label>间隔（小时）</label><input type="number" id="bk-interval" min="1" value="${t && t.trigger_mode === "interval" ? esc(t.cron_expr) : 6}"></div>
    <div class="form-row" id="bk-usb-row" style="display:none"><label>绑定 USB 设备</label>
      <select id="bk-usb"><option value="">（加载中…）</option></select>
      <span class="set-desc">仅绑定的设备插入才触发备份；陌生 U 盘不会触发</span>
    </div>
    <div class="form-row"><label>数量配额</label><input type="number" id="bk-maxcount" min="0" value="${t ? t.max_backup_count : 0}" class="bk-w-unit" placeholder="0"><span class="set-desc">最多保留几份备份（0 = 不限制；冻结备份不计数）</span></div>
    <div class="form-row"><label>大小配额</label>
      <input type="number" id="bk-maxsize-num" min="0" step="any" value="${t && t.max_backup_size ? esc(parseSizeNum(t.max_backup_size)) : ""}" class="bk-w-unit" placeholder="0">
      <select id="bk-maxsize-unit" class="bk-w-unit">
        ${BK_SIZE_UNITS.map(u => `<option value="${u}">${u}</option>`).join("")}
      </select>
      <span class="set-desc">备份总大小上限（0/留空 = 不限制；冻结备份不占配额）</span>
    </div>
    <div class="form-row"><label>邮件通知</label>
      <label class="chk"><input type="checkbox" id="bk-email" onchange="bkTriggerChanged()" ${t && t.enable_email ? "checked" : ""}> 任务结束后发送结果邮件</label>
    </div>
    <div class="form-row" id="bk-smtp-row" style="display:none"><label>SMTP 配置</label><input type="text" id="bk-smtp" value="${t ? esc(t.smtp_config) : ""}" placeholder='{"host":"smtp.qq.com","port":587,"username":"u","password":"p","from":"u@qq.com","to":["me@qq.com"]}'></div>
    <div class="m-actions">
      <button class="primary save-btn" onclick="saveBackupTask(${taskID || 0})">保存任务</button>
    </div>
  `, { wide: true });
  // 编辑时恢复多源/多规则（须在下方异步预填之前执行，避免覆盖新建任务的全局规则预填）
  BK_SOURCES = t ? (t.source_paths || []).slice() : [];
  BK_EXCLUDES = t ? (t.exclude_patterns || []).slice() : [];
  renderBkSources();
  renderBkExcludes();
  // 压缩级别下拉（默认取全局设置）
  try {
    const dfl = await api("/api/backup/defaults", { headers: AUTH() });
    const lv = t ? (t.compress_level || 6) : (dfl.compress_level || 6);
    $("bk-level").innerHTML = [9,8,7,6,5,4,3,2,1].map(n =>
      `<option value="${n}" ${n === lv ? "selected" : ""}>级别 ${n}${n === 9 ? "（最高）" : ""}</option>`).join("");
    if (!t && !$("bk-outdir").value && dfl.output_dir) $("bk-outdir").value = dfl.output_dir;
    // 新建任务：排除规则默认预填全局规则（设置页可配，任务内可增删/清空）
    if (!t && !BK_EXCLUDES.length && Array.isArray(dfl.exclude_rules) && dfl.exclude_rules.length) {
      BK_EXCLUDES = dfl.exclude_rules.slice();
      renderBkExcludes();
    }
  } catch (e) {
    $("bk-level").innerHTML = [9,8,7,6,5,4,3,2,1].map(n => `<option value="${n}">级别 ${n}</option>`).join("");
  }
  // 大小配额单位：编辑时按已存值推断
  if (t && t.max_backup_size) {
    const m = String(t.max_backup_size).toUpperCase().match(/^([0-9.]+)\s*(MB|GB|TB|KB|B)?$/);
    if (m && m[2]) $("bk-maxsize-unit").value = m[2];
  }
  bkTriggerChanged();
  loadUSBDevices(t ? t.usb_device_id : "");
}
// parseSizeNum 从 "10GB" 提取数值部分
function parseSizeNum(s) {
  const m = String(s).match(/^([0-9.]+)/);
  return m ? m[1] : "";
}
function renderBkSources() {
  const el = $("bk-src-list");
  if (!el) return;
  if (!BK_SOURCES.length) { el.innerHTML = '<div class="bk-empty">尚未添加备份源</div>'; return; }
  el.innerHTML = BK_SOURCES.map((p, i) => `
    <span class="bk-chip" title="${esc(p)}">${esc(p)}<a onclick="bkRemoveSource(${i})" title="移除">✕</a></span>`).join("");
}
function renderBkExcludes() {
  const el = $("bk-exc-list");
  if (!el) return;
  if (!BK_EXCLUDES.length) { el.innerHTML = '<div class="bk-empty">无排除规则（可选）</div>'; return; }
  el.innerHTML = BK_EXCLUDES.map((p, i) => `
    <span class="bk-chip" title="${esc(p)}">${esc(p)}<a onclick="bkRemoveExclude(${i})" title="移除">✕</a></span>`).join("");
}
async function bkPickSource() {
  const p = await pickNasFolder("选择备份源目录");
  if (p) addBkSourcePath(normalizeWinPath(p));
}
function bkAddSource() {
  const raw = String($("bk-src-input").value || "").trim();
  if (!raw) return;
  const norm = normalizeWinPath(raw);
  if (!isValidWinPath(norm)) { toast("路径不合法（应为盘符开头的绝对路径）：" + raw, "err"); return; }
  if (addBkSourcePath(norm)) $("bk-src-input").value = "";
}
// addBkSourcePath 添加备份源并处理嵌套包含：
//   与现有源相同 / 被现有源包含 → 拒绝添加；
//   包含现有源（如添加 S:\Code 而已有 S:\Code\C#）→ 自动移除被包含的子目录源
function addBkSourcePath(norm) {
  const key = s => s.toLowerCase().replace(/[\\/]+$/, "");
  const n = key(norm);
  for (const p of BK_SOURCES) {
    const q = key(p);
    if (n === q) { toast("该备份源已添加", "err"); return false; }
    if (n.startsWith(q + "\\") || n.startsWith(q + "/")) {
      toast(`已包含在现有备份源 ${p} 中，无需重复添加`, "err");
      return false;
    }
  }
  const removed = [];
  BK_SOURCES = BK_SOURCES.filter(p => {
    const q = key(p);
    if (q.startsWith(n + "\\") || q.startsWith(n + "/")) { removed.push(p); return false; }
    return true;
  });
  BK_SOURCES.push(norm);
  renderBkSources();
  if (removed.length) toast(`已移除被包含的备份源：${removed.join("、")}`, "ok");
  return true;
}
function bkRemoveSource(i) { BK_SOURCES.splice(i, 1); renderBkSources(); }
function bkAddExclude() {
  const raw = String($("bk-exc-input").value || "").trim();
  if (!raw) return;
  if (BK_EXCLUDES.includes(raw)) { toast("该规则已添加", "err"); return; }
  BK_EXCLUDES.push(raw);
  $("bk-exc-input").value = "";
  renderBkExcludes();
}
function bkRemoveExclude(i) { BK_EXCLUDES.splice(i, 1); renderBkExcludes(); }
// bkClearExcludes 清空全部排除规则（含新建任务预填的全局规则）；保存后该任务不使用任何排除规则
function bkClearExcludes() {
  if (!BK_EXCLUDES.length) return;
  BK_EXCLUDES = [];
  renderBkExcludes();
  toast("排除规则已清空：该任务将不再排除任何文件", "ok");
}
async function bkPickOutput() {
  const p = await pickNasFolder("选择备份包存放目录");
  if (p) $("bk-outdir").value = p;
}
function bkTriggerChanged() {
  const m = $("bk-trigger").value;
  $("bk-cron-row").style.display = (m === "timer" && $("bk-cron").value) ? "" : "none";
  $("bk-simple-row").style.display = m === "timer" ? "" : "none";
  $("bk-interval-row").style.display = m === "interval" ? "" : "none";
  $("bk-usb-row").style.display = m === "realtime" ? "" : "none";
  $("bk-smtp-row").style.display = $("bk-email").checked ? "" : "none";
}
async function loadUSBDevices(selected) {
  try {
    BK_USB_DEVICES = (await api("/api/backup/usb-devices", { headers: AUTH() })) || [];
  } catch (e) { BK_USB_DEVICES = []; }
  const sel = $("bk-usb");
  if (!sel) return;
  sel.innerHTML = '<option value="">（不绑定）</option>' + BK_USB_DEVICES.map(d =>
    `<option value="${esc(d.id)}" ${d.id === selected ? "selected" : ""}>${esc(d.label || d.mount)}（${esc(d.mount)}）</option>`).join("");
}
async function saveBackupTask(taskID) {
  if (!BK_SOURCES.length) { toast("请至少添加一个备份源", "err"); return; }
  const outdir = normalizeWinPath(String($("bk-outdir").value || "").trim());
  if (!isValidWinPath(outdir)) { toast("存放目录不合法（应为盘符开头的绝对路径）", "err"); return; }
  const sizeNum = String($("bk-maxsize-num").value || "").trim();
  const sizeUnit = $("bk-maxsize-unit").value;
  const maxsize = sizeNum && parseFloat(sizeNum) > 0 ? sizeNum + sizeUnit : "";
  const body = {
    task_name: String($("bk-name").value || "").trim(),
    source_paths: BK_SOURCES.slice(),
    exclude_patterns: BK_EXCLUDES.slice(),
    output_dir: outdir,
    enable_compress: $("bk-zip").checked,
    compress_level: parseInt($("bk-level").value, 10) || 6,
    backup_type: $("bk-btype").value,
    trigger_mode: $("bk-trigger").value,
    max_backup_count: parseInt($("bk-maxcount").value, 10) || 0,
    max_backup_size: maxsize,
    enable_email_notify: $("bk-email").checked,
    smtp_config: String($("bk-smtp").value || "").trim(),
  };
  const m = $("bk-trigger").value;
  if (m === "timer") {
    const cron = String($("bk-cron").value || "").trim();
    if (cron) { body.cron_expr = cron; }
    else {
      body.simple_period = {
        unit: $("bk-unit").value, every: parseInt($("bk-every").value, 10) || 1,
        weekday: parseInt($("bk-weekday").value, 10) || 1,
        hour: parseInt($("bk-hour").value, 10) || 0,
        minute: parseInt($("bk-minute").value, 10) || 0,
      };
    }
  } else if (m === "interval") {
    body.cron_expr = String($("bk-interval").value || "6");
  } else if (m === "realtime") {
    body.usb_device_id = String($("bk-usb").value || "");
    if (!body.usb_device_id) { toast("实时触发需绑定 USB 设备", "err"); return; }
  }
  try {
    if (taskID) {
      await api(`/api/backup/tasks/${taskID}`, { method: "PUT", headers: AUTH(), body: JSON.stringify(body) });
      toast("任务已更新", "ok");
    } else {
      await api("/api/backup/tasks", { method: "POST", headers: AUTH(), body: JSON.stringify(body) });
      toast("任务已创建", "ok");
    }
    closeGen();
    loadBackupTasks();
  } catch (e) { toast(e.message, "err"); }
}

/* ==================== WebSocket ==================== */
let ws = null;
function connectWS() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  ws = new WebSocket(`${proto}://${location.host}/ws?token=${encodeURIComponent(TOKEN)}`);
  ws.onopen = () => { $("ws-dot").className = "dot on"; $("ws-text").textContent = "实时通道已连接"; };
  ws.onclose = () => { $("ws-dot").className = "dot off"; $("ws-text").textContent = "实时通道已断开"; };
  ws.onerror = () => { $("ws-dot").className = "dot off"; $("ws-text").textContent = "连接异常"; };
  ws.onmessage = ev => {
    let msg; try { msg = JSON.parse(ev.data); } catch (e) { return; }
    if (msg.type === "ping") { ws.send(JSON.stringify({ type: "pong" })); return; }
    if (msg.type === "file_progress" && msg.data && msg.data.done) toast("文件传输完成", "ok");
    if (msg.type === "notification") toast((msg.data && msg.data.content) || "通知", "ok");
  };
}

/* ==================== 视频在线播放（v0.17，Video.js） ==================== */
const VIDEO_EXT = /\.(mp4|mkv|avi|mov|webm|m4v|flv|ts|mpg|mpeg|3gp|wmv|rmvb)$/i;
const AUDIO_EXT = /\.(mp3|flac|wav|aac|ogg|m4a|opus|wma)$/i;
const VIDEO_INFO = {};   // fileID -> VideoInfo（列表展示缓存，避免重复探测）
let PV = null;           // 播放会话 {fileID, name, info, ticket, url}
let PV_PLAYER = null;    // Video.js 播放器实例

function isVideoExt(name) { return VIDEO_EXT.test(name || ""); }
function isAudioExt(name) { return AUDIO_EXT.test(name || ""); }

// 媒体 MIME 类型（音频按扩展名映射，视频统一 mp4）
function mediaMimeType(info) {
  if (info && info.is_audio) {
    const m = { mp3: "audio/mpeg", flac: "audio/flac", wav: "audio/wav", aac: "audio/aac", ogg: "audio/ogg", m4a: "audio/mp4", opus: "audio/opus", wma: "audio/x-ms-wma" };
    return m[info.container] || "audio/mpeg";
  }
  return "video/mp4";
}

// 不可播放原因的中文提示（与后端 reasonText 保持一致）
function videoRefuseText(info) {
  switch (info && info.reason) {
    case "unsupported": return "该格式暂不支持在线播放，请下载";
    case "probe_failed": return "无法识别视频分辨率，暂不支持在线播放，请下载";
    case "too_large": return "该视频分辨率超过2K，建议下载后本地播放";
    case "ffmpeg_missing": return "服务器未安装 ffmpeg，无法在线播放该格式，请下载";
    default: return "该视频暂不支持在线播放，请下载";
  }
}

// 列表渲染后异步获取视频分辨率，按结果更新行内按钮：
// 可播放 → 保留「播放」；>2K → 隐藏播放、类型列加「高清」标签；其余 → 仅下载
// 并发探测限制为 4，避免大目录瞬时发起大量 ffprobe 进程
let videoInfoQueue = [];
let videoInfoRunning = 0;
function initVideoInfo() {
  FILES.forEach(f => {
    if (f.is_dir) return;
    // 音频无需 ffprobe 探测，直接标记可播放
    if (isAudioExt(f.name)) {
      VIDEO_INFO[f.id] = { playable: true, too_large: false, is_audio: true, container: (f.name.match(/\.([a-z0-9]+)$/i) || [])[1] || "" };
      applyVideoInfo(f.id);
      return;
    }
    if (!isVideoExt(f.name)) return;
    if (VIDEO_INFO[f.id] !== undefined) { applyVideoInfo(f.id); return; }
    videoInfoQueue.push(f.id);
  });
  pumpVideoInfo();
}
function pumpVideoInfo() {
  while (videoInfoRunning < 4 && videoInfoQueue.length) {
    const id = videoInfoQueue.shift();
    videoInfoRunning++;
    api("/api/video/info?file=" + id, { headers: AUTH() })
      .then(info => { VIDEO_INFO[id] = info; applyVideoInfo(id); })
      .catch(() => { VIDEO_INFO[id] = { playable: false, too_large: false, reason: "probe_failed" }; applyVideoInfo(id); })
      .finally(() => { videoInfoRunning--; pumpVideoInfo(); });
  }
}
function applyVideoInfo(fileID) {
  const row = document.querySelector(`#file-table tr[data-id="${fileID}"]`);
  const info = VIDEO_INFO[fileID];
  if (!row || !info) return;
  const playBtn = row.querySelector(".btn-play");
  const typeCell = row.querySelector(".col-type");
  if (info.too_large) {
    if (typeCell && !typeCell.querySelector(".tag.hd")) {
      typeCell.innerHTML += ' <span class="tag hd" title="分辨率超过2K，建议下载后本地播放">高清</span>';
    }
    row.dataset.tooLarge = "1";
  }
  // >2K 或不可播放：隐藏「播放」按钮（>2K 仅保留「高清」标签 + 下载，符合需求）
  if ((info.too_large || !info.playable) && playBtn) playBtn.remove();
}

// 点击「播放」：先取分辨率，>2K 时由播放器拦截弹出悬浮提示窗
async function playVideo(fileID, name) {
  if (!PV_PLAYER) { toast("播放器组件未加载，请刷新页面后重试", "err"); return; }
  let info = VIDEO_INFO[fileID];
  if (!info) {
    try { info = await api("/api/video/info?file=" + fileID, { headers: AUTH() }); VIDEO_INFO[fileID] = info; }
    catch (e) { toast("无法播放：" + e.message, "err"); return; }
  }
  if (!info.playable) {
    toast(videoRefuseText(info), "err");
    return;
  }
  openPlayer(fileID, name, info);
  if (info.too_large) {
    // 播放器内部拦截：弹出悬浮提示窗（继续播放 / 下载）
    $("pv-hd-warn").style.display = "flex";
    showLoading("等待确认…");
  } else {
    startPlayback(fileID, name, info);
  }
}

// 打开播放器抽屉（不立即播放）
function openPlayer(fileID, name, info) {
  PV = { fileID, name, info, ticket: null, url: "" };
  $("pv-title").textContent = name;
  $("pv-badge").style.display = info.too_large ? "" : "none";
  $("pv-error").style.display = "none";
  $("pv-hd-warn").style.display = "none";
  $("player-drawer").classList.add("open");
}

// 申请播放凭证并开始播放（Video.js）
async function startPlayback(fileID, name, info) {
  $("pv-hd-warn").style.display = "none";
  if (!PV) return;
  showLoading("申请播放凭证…");
  try {
    const data = await api("/api/play/ticket", { method: "POST", headers: AUTH(), body: JSON.stringify({ file_id: fileID }) });
    if (!PV) return; // 播放器已被关闭
    PV.ticket = data.token;
    PV.url = data.url;
    PV_PLAYER.src({ src: data.url, type: mediaMimeType(info) });
    showLoading("加载中…");
    PV_PLAYER.play().catch(() => { /* 自动播放被拦截时等待用户点击播放 */ });
  } catch (e) {
    if (!PV) return;
    showPlayerError(e.message || "视频加载失败");
  }
}

function closePlayer() {
  if (PV && PV.ticket) {
    api("/api/play/ticket/" + PV.ticket, { method: "DELETE", headers: AUTH() }).catch(() => {});
  }
  if (PV_PLAYER) {
    PV_PLAYER.pause();
    PV_PLAYER.reset(); // 清空 src 与播放状态
  }
  $("pv-hd-warn").style.display = "none";
  $("pv-error").style.display = "none";
  $("pv-loading").style.display = "flex";
  $("pv-loading-text").textContent = "加载中…";
  $("player-drawer").classList.remove("open");
  PV = null;
}

function retryPlayer() {
  $("pv-error").style.display = "none";
  if (!PV || !PV.url) { closePlayer(); return; }
  showLoading("重新连接…");
  const u = PV.url + (PV.url.indexOf("?") >= 0 ? "&" : "?") + "r=" + Date.now();
  PV_PLAYER.src({ src: u, type: mediaMimeType(PV.info) });
  PV_PLAYER.play().catch(() => {});
}

function showLoading(text) {
  $("pv-loading-text").textContent = text || "加载中…";
  $("pv-loading").style.display = "flex";
}
function hideLoading() { $("pv-loading").style.display = "none"; }
function showPlayerError(msg) {
  hideLoading();
  $("pv-error-msg").textContent = msg;
  $("pv-error").style.display = "flex";
}

// 初始化 Video.js 播放器（页面加载时执行一次）
function initVideoPlayer() {
  if (typeof videojs === "undefined") {
    console.warn("[play] video.js 未加载，播放功能不可用");
    return;
  }
  PV_PLAYER = videojs("pv-video", {
    controls: true,
    autoplay: false,
    preload: "auto",
    fluid: false,
    bigPlayButtonCentered: true,
    playbackRates: [0.5, 0.75, 1, 1.25, 1.5, 2], // 倍速菜单（v0.19）
    controlBar: { pictureInPictureToggle: false, playbackRateMenuButton: true }
  });
  PV_PLAYER.on("loadedmetadata", hideLoading);
  PV_PLAYER.on("playing", hideLoading);
  PV_PLAYER.on("waiting", () => showLoading("缓冲中…"));
  PV_PLAYER.on("stalled", () => showLoading("缓冲中…"));
  PV_PLAYER.on("error", () => {
    if (PV && PV.url) showPlayerError("网络中断或视频无法加载，请重试");
  });
  // 超高清悬浮提示窗按钮
  $("pv-warn-play").onclick = () => { if (PV) startPlayback(PV.fileID, PV.name, PV.info); };
  $("pv-warn-dl").onclick = () => { if (PV) { downloadFile(PV.fileID, PV.name); closePlayer(); } };
  // 键盘控制（v0.19）：抽屉打开时全局生效，无需点击播放器获得焦点
  document.addEventListener("keydown", onPlayerKeydown);
  // 手机滑动快进/快退（v0.19）
  initSwipeSeek();
}

// 播放器键盘控制：←/→ 快退/快进 5 秒，↑/↓ 音量，空格 播放/暂停，F 全屏
function onPlayerKeydown(e) {
  if (!$("player-drawer").classList.contains("open") || !PV_PLAYER) return;
  const tag = (e.target && e.target.tagName) || "";
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
  switch (e.key) {
    case "ArrowLeft": e.preventDefault(); seekBy(-5); break;
    case "ArrowRight": e.preventDefault(); seekBy(5); break;
    case "ArrowUp": e.preventDefault(); changeVolume(0.05); break;
    case "ArrowDown": e.preventDefault(); changeVolume(-0.05); break;
    case " ": e.preventDefault(); togglePlay(); break;
    case "f": case "F": e.preventDefault(); if (PV_PLAYER.requestFullscreen) PV_PLAYER.requestFullscreen(); break;
  }
}
function seekBy(sec) {
  if (!PV_PLAYER) return;
  const d = PV_PLAYER.duration() || 0;
  const t = Math.max(0, Math.min((PV_PLAYER.currentTime() || 0) + sec, d || Infinity));
  PV_PLAYER.currentTime(t);
}
function changeVolume(delta) {
  if (!PV_PLAYER) return;
  const v = Math.max(0, Math.min(1, (PV_PLAYER.volume() || 0) + delta));
  PV_PLAYER.volume(v);
  PV_PLAYER.muted(v === 0);
}
function togglePlay() {
  if (!PV_PLAYER) return;
  if (PV_PLAYER.paused()) PV_PLAYER.play().catch(() => {}); else PV_PLAYER.pause();
}

// 手机滑动快进/快退：在视频上水平滑动，按滑动距离占视频宽度比例跳转
function initSwipeSeek() {
  if (!PV_PLAYER) return;
  const videoEl = PV_PLAYER.el().querySelector("video");
  if (!videoEl) return;
  let swipe = null;
  videoEl.addEventListener("touchstart", e => {
    if (e.touches.length !== 1) return;
    const t = e.touches[0];
    swipe = { x: t.clientX, y: t.clientY, startTime: PV_PLAYER.currentTime() || 0, dur: PV_PLAYER.duration() || 0 };
  }, { passive: true });
  videoEl.addEventListener("touchmove", e => {
    if (!swipe || e.touches.length !== 1) return;
    const t = e.touches[0];
    const dx = t.clientX - swipe.x;
    const dy = t.clientY - swipe.y;
    // 仅水平滑动为主且位移足够时触发快进/快退
    if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 24) {
      e.preventDefault();
      const rect = videoEl.getBoundingClientRect();
      const ratio = rect.width ? dx / rect.width : 0;
      const target = swipe.startTime + ratio * (swipe.dur || 60);
      PV_PLAYER.currentTime(Math.max(0, Math.min(target, swipe.dur || target)));
    }
  }, { passive: false });
  videoEl.addEventListener("touchend", () => { swipe = null; });
}

/* ---------- 初始化 ---------- */
initVideoPlayer();
if (TOKEN && USERNAME) enterMain();
$("password").addEventListener("keydown", e => { if (e.key === "Enter") doLogin(); });
