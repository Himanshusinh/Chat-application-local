"use strict";

// ------------------------------------------------------------------ setup
const TOKEN = (() => {
  const t = new URLSearchParams(location.search).get("t");
  if (t) { try { sessionStorage.setItem("t", t); } catch {} return t; }
  try { return sessionStorage.getItem("t") || ""; } catch { return ""; }
})();

const $ = (id) => document.getElementById(id);
const S = {
  me: null,
  settings: {},
  peers: new Map(),       // id -> peer
  chats: {},              // chat -> {last, unread}
  msgs: new Map(),        // chat -> [messages]
  active: "all",
  progress: new Map(),    // tid -> {done,total,speed}
  previews: new Map(),    // tid -> blob URL (sender-side image previews)
  uploads: new Map(),     // tid -> {cancelled, aborts:Set}
  typing: new Map(),      // chat -> {from, until}
  early: new Map(),       // msg id -> update that arrived before the message itself
};
const CHUNK = 8 * 1024 * 1024;   // bytes per request
const PARALLEL = 6;              // concurrent chunks per transfer
const AVATARS = ["🙂","😎","🤓","🧑‍💻","👩‍💼","👨‍💼","🦊","🐼","🐯","🦁","🐸","🐙","🦄","🚀","⚡","🔥","🌈","🍕","☕","🎧","🎯","🌻","🐶","🐱"];

async function api(path, body) {
  const opts = { method: body === undefined ? "GET" : "POST", headers: { "X-Token": TOKEN } };
  if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  const r = await fetch(path, opts);
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}

// ----------------------------------------------------------- native bridge
// Inside the desktop app (WebView2 on Windows, WKWebView on Mac) the page
// talks to the native window for notifications, the badge, etc.
const NATIVE = (window.chrome && window.chrome.webview) ? (s) => window.chrome.webview.postMessage(s)
  : (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.oc) ? (s) => window.webkit.messageHandlers.oc.postMessage(s)
  : null;
function nativeSend(msg) { if (!NATIVE) return false; try { NATIVE(JSON.stringify(msg)); return true; } catch { return false; } }
let appVisible = true;
function isAppVisible() { return appVisible && !document.hidden; }
window.ocNative = {
  setVisible(v) { appVisible = !!v; if (v) markRead(); },
  openChat(chat) { openChat(chat); },
  openSettings() { openSettings(); },
};

// ------------------------------------------------------------------ theme
// "light", "dark" or "system". Saved in the app settings (and cached locally
// so the next start opens in the right theme without a flash).
let themePref = "dark";
const darkMQ = matchMedia("(prefers-color-scheme: dark)");
function applyTheme(pref, save) {
  themePref = ["light", "dark", "system"].includes(pref) ? pref : "dark";
  try { localStorage.setItem("theme", themePref); } catch {}
  const dark = themePref === "dark" || (themePref === "system" && darkMQ.matches);
  document.documentElement.setAttribute("data-theme", dark ? "dark" : "light");
  document.querySelectorAll("#themeSeg button").forEach((b) => b.classList.toggle("on", b.dataset.theme === themePref));
  $("btnTheme").title = dark ? "Switch to light theme" : "Switch to dark theme";
  nativeSend({ type: "theme", mode: themePref, dark }); // native title bar follows
  if (save) api("/api/settings", { theme: themePref }).catch(() => {});
}
darkMQ.addEventListener("change", () => { if (themePref === "system") applyTheme("system"); });
$("btnTheme").addEventListener("click", () => applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark", true));
$("themeSeg").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-theme]");
  if (b) applyTheme(b.dataset.theme, true);
});

// ---------------------------------------------------------------- helpers
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function fmtSize(n) {
  if (n < 1024) return n + " B";
  const u = ["KB", "MB", "GB", "TB"]; let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + " " + u[i];
}
function fmtTime(ms) { return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); }
function fmtDay(ms) {
  const d = new Date(ms), now = new Date();
  const sameDay = (a, b) => a.toDateString() === b.toDateString();
  if (sameDay(d, now)) return "Today";
  const y = new Date(now); y.setDate(now.getDate() - 1);
  if (sameDay(d, y)) return "Yesterday";
  return d.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short", year: d.getFullYear() === now.getFullYear() ? undefined : "numeric" });
}
function fmtShort(ms) {
  if (!ms) return "";
  const d = new Date(ms);
  return d.toDateString() === new Date().toDateString() ? fmtTime(ms) : d.toLocaleDateString([], { day: "numeric", month: "short" });
}
function fmtEta(sec) {
  if (!isFinite(sec) || sec <= 0) return "";
  if (sec < 60) return Math.ceil(sec) + "s left";
  if (sec < 3600) return Math.ceil(sec / 60) + " min left";
  return (sec / 3600).toFixed(1) + " h left";
}
function linkify(text) {
  return esc(text).replace(/\bhttps?:\/\/[^\s<]+[^\s<.,;:!?)\]'"]/g, (u) => `<a href="${u}" target="_blank" rel="noopener">${u}</a>`);
}

const segmenter = typeof Intl !== "undefined" && Intl.Segmenter ? new Intl.Segmenter(undefined, { granularity: "grapheme" }) : null;
// Count emojis if the text is only emojis (for big emoji rendering), else 0.
function emojiOnly(text) {
  const t = text.trim();
  if (!t || t.length > 80 || !/^[\p{Extended_Pictographic}\p{Emoji_Component}‍️\s]+$/u.test(t) || !/\p{Extended_Pictographic}|\p{Regional_Indicator}/u.test(t)) return 0;
  if (/^[\d#*\s]+$/.test(t)) return 0;
  const n = segmenter ? [...segmenter.segment(t.replace(/\s+/g, ""))].length : 1;
  return n <= 8 ? n : 0;
}

const GROUP_SVG = `<svg viewBox="0 0 24 24"><circle cx="9" cy="8" r="3.2"/><path d="M3 19c.6-3.3 3-5 6-5s5.4 1.7 6 5"/><circle cx="17" cy="9" r="2.4"/><path d="M16.5 14c2.4.2 4 1.7 4.5 4.5"/></svg>`;
function avatarHTML(id, emoji, cls = "", online, elId = "") {
  const idAttr = elId ? ` id="${elId}"` : "";
  if (id === "all") return `<div class="avatar group ${cls}"${idAttr}>${GROUP_SVG}</div>`;
  const dot = online === undefined ? "" : `<span class="dot ${online ? "on" : ""}"></span>`;
  return `<div class="avatar ${cls}"${idAttr}>${esc(emoji || "🙂")}${dot}</div>`;
}
// Current name/picture of a person (live: follows renames instantly).
function personName(id, fallback) { const p = S.peers.get(id); return (p && p.name) || fallback || "Someone"; }
function personAvatar(id, fallback) { const p = S.peers.get(id); return (p && p.avatar) || fallback || "🙂"; }

let toastTimer;
function toast(msg, ms = 4000) {
  const t = $("toast"); t.textContent = msg; t.hidden = false;
  clearTimeout(toastTimer); toastTimer = setTimeout(() => (t.hidden = true), ms);
}

const ICONS = {
  folder: `<svg viewBox="0 0 24 24"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/></svg>`,
  image: `<svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="1.8"/><path d="M21 16l-5-5-9 9"/></svg>`,
  video: `<svg viewBox="0 0 24 24"><rect x="3" y="5" width="13" height="14" rx="2"/><path d="M16 10l5-3v10l-5-3"/></svg>`,
  audio: `<svg viewBox="0 0 24 24"><path d="M9 18V5l11-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="17" cy="16" r="3"/></svg>`,
  archive: `<svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4"/></svg>`,
  file: `<svg viewBox="0 0 24 24"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8Z"/><path d="M14 3v5h5M9 13h6M9 17h6"/></svg>`,
};
function fileIcon(name, folder) {
  if (folder) return ICONS.folder;
  const ext = (name.split(".").pop() || "").toLowerCase();
  if (/^(png|jpe?g|gif|webp|bmp|heic|svg|avif)$/.test(ext)) return ICONS.image;
  if (/^(mp4|mov|mkv|avi|webm|m4v)$/.test(ext)) return ICONS.video;
  if (/^(mp3|wav|m4a|aac|flac|ogg)$/.test(ext)) return ICONS.audio;
  if (/^(zip|rar|7z|tar|gz|dmg|iso)$/.test(ext)) return ICONS.archive;
  return ICONS.file;
}
const isImage = (n) => /\.(png|jpe?g|gif|webp|bmp|svg|avif)$/i.test(n);
const isVideo = (n) => /\.(mp4|webm|mov|m4v)$/i.test(n);

// ------------------------------------------------------------- chat list
function chatName(chat) {
  if (chat === "all") return "Everyone";
  const p = S.peers.get(chat);
  return p ? p.name : "Unknown";
}

function renderChatList() {
  const q = $("search").value.trim().toLowerCase();
  const list = $("chatlist");
  const all = S.chats.all || {};
  const online = [...S.peers.values()].filter((p) => p.online).length;
  let html = "";
  if (!q || "everyone".includes(q)) {
    html += chatItemHTML("all", "Everyone", "", `${online} online`, all, true);
  }
  const people = [...S.peers.values()]
    .filter((p) => !q || p.name.toLowerCase().includes(q))
    .sort((a, b) => (b.online - a.online) || (((S.chats[b.id] || {}).last || {}).time || 0) - (((S.chats[a.id] || {}).last || {}).time || 0) || a.name.localeCompare(b.name));
  if (people.length) html += `<div class="section-label">People</div>`;
  for (const p of people) {
    const c = S.chats[p.id] || {};
    const sub = c.last ? lastPreview(c.last) : p.online ? (p.via === "lan" ? "Online · same network" : "Online · remote") : "Offline";
    html += chatItemHTML(p.id, p.name, p.avatar, sub, c, p.online);
  }
  if (!S.peers.size) {
    html += `<div class="empty-peers">Looking for colleagues on this network…<br>Make sure OfficeChat is open on their computer and they use the same <b>team key</b>. For other networks, use <b>Settings › Connect</b>.</div>`;
  }
  list.innerHTML = html;
  const totalUnread = Object.values(S.chats).reduce((a, c) => a + (c.unread || 0), 0);
  document.title = totalUnread ? `(${totalUnread}) OfficeChat` : "OfficeChat";
  if (totalUnread !== lastBadge) { lastBadge = totalUnread; nativeSend({ type: "badge", count: totalUnread }); }
}

let lastBadge = -1;
function lastPreview(m) {
  const who = m.from === S.me.id ? "You: " : m.to === "all" ? personName(m.from, m.fromName).split(" ")[0] + ": " : "";
  return who + (m.kind === "sticker" ? "Sticker" : m.text || "");
}

function chatItemHTML(id, name, avatar, sub, c, online) {
  const unread = c.unread && id !== S.active ? `<span class="badge">${c.unread}</span>` : "";
  return `<div class="chat-item ${id === S.active ? "active" : ""} ${online ? "" : "offline"}" data-chat="${esc(id)}">
    ${avatarHTML(id, avatar, "", id === "all" ? undefined : online)}
    <div class="ci-text">
      <div class="ci-row"><span class="ci-name">${esc(name)}</span><span class="ci-time">${fmtShort(c.last && c.last.time)}</span></div>
      <div class="ci-row"><span class="ci-last">${esc(sub)}</span>${unread}</div>
    </div></div>`;
}

$("chatlist").addEventListener("click", (e) => {
  const item = e.target.closest(".chat-item");
  if (item) openChat(item.dataset.chat);
});
$("search").addEventListener("input", renderChatList);

function renderMe() {
  $("meAvatar").outerHTML = avatarHTML(S.me.id, S.me.avatar, "", undefined, "meAvatar");
  $("meName").textContent = S.me.name;
  const online = [...S.peers.values()].filter((p) => p.online).length;
  $("meSub").textContent = online ? `Online · ${online} colleague${online === 1 ? "" : "s"} connected` : "Online · waiting for colleagues";
}

function renderNetInfo(state) {
  const addrs = (state.ips || []).map((ip) => `<b>${esc(ip)}</b>`).join(", ");
  $("netinfo").innerHTML = `This computer: ${addrs || "no network"} · port ${state.port}` +
    (state.discovery ? "" : `<br><span class="warn">Auto-discovery is blocked (UDP 45454). Add people by address in Settings.</span>`);
  $("myAddrs").innerHTML = `Others can connect to this computer at: ${(state.ips || []).map((ip) => `<code>${esc(ip)}:${state.port}</code>`).join(" ")}`;
}

// ----------------------------------------------------------------- header
function renderHeader() {
  const c = S.active;
  const p = S.peers.get(c);
  $("headAvatar").outerHTML = avatarHTML(c, p ? p.avatar : "🙂", "", undefined, "headAvatar");
  $("headTitle").textContent = chatName(c);
  const ty = S.typing.get(c);
  const sub = $("headSub");
  if (ty && ty.until > Date.now()) {
    const who = c === "all" ? (S.peers.get(ty.from) || {}).name || "Someone" : "";
    sub.textContent = who ? `${who.split(" ")[0]} is typing…` : "typing…";
    sub.className = "head-sub typing";
    return;
  }
  sub.className = "head-sub";
  if (c === "all") {
    const on = [...S.peers.values()].filter((x) => x.online).map((x) => x.name.split(" ")[0]);
    sub.textContent = on.length ? `You, ${on.slice(0, 6).join(", ")}${on.length > 6 ? ` and ${on.length - 6} more` : ""}` : "Only you are online right now";
  } else if (p) {
    sub.textContent = p.online ? `Online · ${p.os === "darwin" ? "Mac" : p.os === "windows" ? "Windows" : p.os} · ${p.via === "lan" ? "same network" : "remote"}` : "Offline · messages will be delivered when they're back";
  }
}

// --------------------------------------------------------------- messages
async function openChat(chat) {
  S.active = chat;
  $("app").classList.add("in-chat");
  renderHeader();
  renderChatList();
  if (!S.msgs.has(chat)) {
    $("messages").innerHTML = "";
    try { S.msgs.set(chat, await api(`/api/messages?chat=${encodeURIComponent(chat)}`)); } catch (e) { toast(e.message); return; }
    if (S.active !== chat) return;
  }
  renderMessages();
  markRead();
  $("input").focus();
}

function markRead() {
  const c = S.chats[S.active];
  if (!isAppVisible()) return;
  if (c && c.unread) { c.unread = 0; renderChatList(); }
  api("/api/read", { chat: S.active }).catch(() => {});
}

function renderMessages() {
  const box = $("messages");
  const list = S.msgs.get(S.active) || [];
  if (!list.length) {
    const p = S.peers.get(S.active);
    box.innerHTML = `<div class="empty-chat"><div class="big">${S.active === "all" ? "👋" : esc((p && p.avatar) || "💬")}</div>
      <p>${S.active === "all" ? "Say hello to everyone in the office." : `Start a conversation with ${esc(chatName(S.active))}.`}</p>
      <p style="font-size:12.5px">Drag files or whole folders here to send them.</p></div>`;
    return;
  }
  let html = "", prev = null;
  for (const m of list) { html += msgHTML(m, prev); prev = m; }
  box.innerHTML = html;
  box.scrollTop = box.scrollHeight;
}

function msgHTML(m, prev) {
  let out = "";
  if (!prev || new Date(prev.time).toDateString() !== new Date(m.time).toDateString()) {
    out += `<div class="day">${fmtDay(m.time)}</div>`;
  }
  const mine = m.from === S.me.id;
  const cont = prev && prev.from === m.from && m.time - prev.time < 5 * 60e3 && new Date(prev.time).toDateString() === new Date(m.time).toDateString();
  const showSender = !mine && m.to === "all" && !cont;
  const avatar = mine || m.to !== "all" ? "" : avatarHTML(m.from, personAvatar(m.from, m.avatar), "small");
  let body;
  if (m.kind === "files") {
    body = fileCardHTML(m);
  } else if (m.kind === "sticker") {
    body = stickerHTML(m.sticker, !mine);
  } else {
    const n = emojiOnly(m.text || "");
    body = n ? `<div class="bubble jumbo jumbo-${n === 1 ? 1 : n <= 3 ? 2 : 3}">${esc(m.text.trim())}</div>`
             : `<div class="bubble">${linkify(m.text || "")}</div>`;
  }
  let tick = "";
  if (mine && m.to !== "all") {
    tick = m.status === "read" ? `<span class="tick-read" title="Read">✓✓</span>`
         : m.status === "sent" ? `<span title="Delivered">✓✓</span>`
         : m.status === "pending" ? `<span class="tick-pending" title="Waiting – delivers when they're online">🕓</span>` : "";
  }
  return out + `<div class="msg ${mine ? "mine" : ""} ${cont ? "cont" : ""}" data-id="${esc(m.id)}">
    ${avatar}
    <div class="msg-body">
      ${showSender ? `<div class="sender">${esc(personName(m.from, m.fromName))}</div>` : ""}
      ${body}
      <div class="meta">${fmtTime(m.time)} ${tick}</div>
    </div></div>`;
}

function fileCardHTML(m) {
  const t = m.transfer;
  const mine = m.from === S.me.id;
  const pr = S.progress.get(t.id);
  const status = t.status || "";
  const meta = t.folder ? `Folder · ${t.count} file${t.count === 1 ? "" : "s"} · ${fmtSize(t.total)}`
             : t.count > 1 ? `${t.count} files · ${fmtSize(t.total)}` : fmtSize(t.total);
  let preview = "";
  if (t.count === 1 && (isImage(t.name) || isVideo(t.name))) {
    const src = mine ? S.previews.get(t.id) : status === "done" ? `/api/file?tid=${encodeURIComponent(t.id)}&t=${TOKEN}` : "";
    if (src) preview = isVideo(t.name) ? `<video class="fc-preview" src="${src}" controls preload="metadata"></video>`
                                       : `<img class="fc-preview" src="${src}" alt="" loading="lazy">`;
  }
  let pct = 0, line = "", cls = "";
  if (status === "sending" || status === "receiving") {
    const done = pr ? pr.done : 0, total = pr ? pr.total : t.total;
    pct = total ? (done / total) * 100 : 0;
    const speed = pr ? pr.speed : 0;
    line = `${status === "sending" ? "Sending" : "Receiving"} · ${fmtSize(done)} of ${fmtSize(total)}` +
           (speed > 0 ? ` · ${fmtSize(speed)}/s · ${fmtEta((total - done) / speed)}` : "");
  } else if (status === "done") {
    pct = 100; cls = "ok"; line = mine ? "✓ Sent" : "✓ Received";
  } else if (status === "failed") {
    cls = "err"; line = "Failed" + (t.error ? ": " + t.error : "");
  } else if (status === "cancelled") {
    cls = "err"; line = "Cancelled";
  }
  let actions = "";
  if (!mine && status === "done") {
    actions = `<div class="fc-actions"><button class="btn small" data-act="open" data-tid="${esc(t.id)}">Open</button>
               <button class="btn small" data-act="reveal" data-tid="${esc(t.id)}">Show in folder</button></div>`;
  } else if (mine && status === "sending" && S.uploads.has(t.id)) {
    actions = `<div class="fc-actions"><button class="btn small" data-act="cancel" data-tid="${esc(t.id)}">Cancel</button></div>`;
  }
  const strip = (rel) => (t.folder && rel.startsWith(t.name + "/") ? rel.slice(t.name.length + 1) : rel);
  const files = t.count > 1 && t.files ? `<div class="fc-files">${t.files.slice(0, 50).map((f) => esc(strip(f.rel))).join("<br>")}${t.count > 50 ? "<br>…" : ""}</div>` : "";
  const showBar = status === "sending" || status === "receiving";
  return `<div class="file-card" data-tid="${esc(t.id)}">
    ${preview}
    <div class="fc-main"><div class="fc-icon">${fileIcon(t.name, t.folder)}</div>
      <div class="fc-text"><div class="fc-name" title="${esc(t.name)}">${esc(t.name)}</div><div class="fc-sub">${meta}</div></div></div>
    ${files}
    ${showBar ? `<div class="fc-bar"><div style="width:${pct.toFixed(1)}%"></div></div>` : ""}
    <div class="fc-status ${cls}">${esc(line)}</div>
    ${actions}
  </div>`;
}

// Keep pinned to the newest message while images/videos load in.
let stickBottom = true;
$("messages").addEventListener("scroll", () => {
  const b = $("messages");
  stickBottom = b.scrollHeight - b.scrollTop - b.clientHeight < 80;
});
$("messages").addEventListener("load", () => {
  if (stickBottom) $("messages").scrollTop = $("messages").scrollHeight;
}, true);

$("messages").addEventListener("click", async (e) => {
  const img = e.target.closest("img.fc-preview");
  if (img) {
    const lb = document.createElement("div");
    lb.className = "lightbox"; lb.innerHTML = `<img src="${img.src}">`;
    lb.onclick = () => lb.remove();
    document.body.append(lb);
    return;
  }
  const b = e.target.closest("button[data-act]");
  if (!b) return;
  const tid = b.dataset.tid;
  try {
    if (b.dataset.act === "open") await api(`/api/open?tid=${encodeURIComponent(tid)}`, {});
    if (b.dataset.act === "reveal") await api(`/api/open?reveal=1&tid=${encodeURIComponent(tid)}`, {});
    if (b.dataset.act === "cancel") cancelUpload(tid);
  } catch (err) { toast(err.message); }
});

function rerenderMessage(m) {
  const list = S.msgs.get(m.chat);
  if (!list) { S.early.set(m.id, m); return; }
  const i = list.findIndex((x) => x.id === m.id);
  if (i < 0) { S.early.set(m.id, m); return; }
  list[i] = m;
  if (m.chat !== S.active) return;
  const el = $("messages").querySelector(`.msg[data-id="${CSS.escape(m.id)}"]`);
  if (!el) return;
  const tmp = document.createElement("div");
  tmp.innerHTML = msgHTML(m, list[i - 1]);
  el.replaceWith(tmp.lastElementChild);
}

function addMessage(m) {
  if (S.early.has(m.id)) { m = S.early.get(m.id); S.early.delete(m.id); }
  const list = S.msgs.get(m.chat);
  const chat = (S.chats[m.chat] ||= { unread: 0 });
  const summary = { ...m, transfer: undefined, text: m.kind === "files" ? "📎 " + m.transfer.name : m.text };
  chat.last = summary;
  if (list) {
    if (list.some((x) => x.id === m.id)) return false;
    list.push(m);
    if (m.chat === S.active) {
      const box = $("messages");
      const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 160 || m.from === S.me.id;
      if (list.length === 1) renderMessages();
      else box.insertAdjacentHTML("beforeend", msgHTML(m, list[list.length - 2]));
      if (nearBottom) box.scrollTop = box.scrollHeight;
    }
  }
  return true;
}

// --------------------------------------------------------------- composer
const input = $("input");
function autosize() { input.style.height = "auto"; input.style.height = Math.min(input.scrollHeight, 160) + "px"; }
input.addEventListener("input", () => { autosize(); notifyTyping(); });
input.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); sendText(); }
  if (e.key === "Escape") closePanels();
});
$("btnSend").addEventListener("click", () => sendText());

let lastTyping = 0;
function notifyTyping() {
  if (Date.now() - lastTyping < 3000 || !input.value.trim()) return;
  lastTyping = Date.now();
  api("/api/typing", { to: S.active }).catch(() => {});
}

async function sendText(text) {
  const t = text ?? input.value;
  if (!t.trim()) return;
  if (text === undefined) { input.value = ""; autosize(); }
  lastTyping = 0;
  try {
    const m = await api("/api/send", { to: S.active, text: t });
    addMessage(m);
    renderChatList();
  } catch (e) {
    toast(e.message);
    if (text === undefined) { input.value = t; autosize(); }
  }
}

// --------------------------------------------------------------- emojis
const RECENT_KEY = "recentEmoji";
let emojiCats = [];
function loadRecent() { try { return JSON.parse(localStorage.getItem(RECENT_KEY)) || []; } catch { return []; } }
function pushRecent(e) {
  const r = [e, ...loadRecent().filter((x) => x !== e)].slice(0, 32);
  try { localStorage.setItem(RECENT_KEY, JSON.stringify(r)); } catch {}
}
function buildEmoji() {
  emojiCats = window.EMOJI.map(([icon, name, data]) => ({
    icon, name, items: data.split("|").map((s) => { const i = s.indexOf(" "); return { e: s.slice(0, i), k: s.slice(i + 1) }; }),
  }));
  $("emojiTabs").innerHTML = [`<button data-cat="recent" title="Recent">🕘</button>`]
    .concat(emojiCats.map((c, i) => `<button data-cat="${i}" title="${esc(c.name)}">${c.icon}</button>`)).join("");
  renderEmojiGrid();
}
function renderEmojiGrid() {
  const q = $("emojiSearch").value.trim().toLowerCase();
  let html = "";
  if (q) {
    const hits = emojiCats.flatMap((c) => c.items).filter((x) => x.k.includes(q));
    html = hits.length ? hits.map((x) => `<button title="${esc(x.k)}">${x.e}</button>`).join("") : `<div class="ep-label">No emoji found</div>`;
  } else {
    const rec = loadRecent();
    if (rec.length) html += `<div class="ep-label" id="cat-recent">Recent</div>` + rec.map((e) => `<button>${e}</button>`).join("");
    emojiCats.forEach((c, i) => {
      html += `<div class="ep-label" id="cat-${i}">${esc(c.name)}</div>` + c.items.map((x) => `<button title="${esc(x.k)}">${x.e}</button>`).join("");
    });
  }
  $("emojiGrid").innerHTML = html;
}
$("btnEmoji").addEventListener("click", (e) => {
  e.stopPropagation();
  const p = $("emojiPanel");
  const open = p.hidden;
  closePanels();
  p.hidden = !open;
  $("btnEmoji").classList.toggle("on", !p.hidden);
  if (!p.hidden) { renderEmojiGrid(); $("emojiSearch").focus(); }
});
$("emojiSearch").addEventListener("input", renderEmojiGrid);
$("emojiPanel").addEventListener("keydown", (e) => {
  if (e.key === "Escape") { $("emojiPanel").hidden = true; $("btnEmoji").classList.remove("on"); input.focus(); }
});
$("emojiTabs").addEventListener("click", (e) => {
  const b = e.target.closest("button"); if (!b) return;
  $("emojiSearch").value = ""; renderEmojiGrid();
  const target = $("cat-" + b.dataset.cat);
  if (target) $("emojiGrid").scrollTop = target.offsetTop - $("emojiGrid").offsetTop;
});
$("emojiGrid").addEventListener("click", (e) => {
  const b = e.target.closest("button"); if (!b) return;
  const em = b.textContent;
  pushRecent(em);
  if ($("emojiBig").checked) { sendText(em); return; }
  const s = input.selectionStart ?? input.value.length, en = input.selectionEnd ?? input.value.length;
  input.value = input.value.slice(0, s) + em + input.value.slice(en);
  input.selectionStart = input.selectionEnd = s + em.length;
  input.focus(); autosize();
});
document.addEventListener("click", (e) => {
  if (!e.target.closest(".panel") && !e.target.closest("#btnEmoji") && !e.target.closest("#btnSticker")) closePanels();
});

// ---------------------------------------------------------------- stickers
const BUILTIN = new Map((window.STICKERS || []).map(([id, e, cap]) => ["b:" + id, { e, cap }]));
let myStickers = [];
const stickerURL = (st) => `/api/sticker?id=${encodeURIComponent(st.id)}&ext=${encodeURIComponent(st.ext)}&t=${TOKEN}`;

function stickerHTML(st, received) {
  if (!st) return "";
  const b = BUILTIN.get(st.id);
  if (b) return `<div class="sticker" title="${esc(b.cap)}"><div class="st-emoji">${b.e}</div><div class="st-cap">${esc(b.cap)}</div></div>`;
  const savable = received && !myStickers.some((x) => x.id === st.id);
  return `<img class="sticker-img ${savable ? "savable" : ""}" src="${stickerURL(st)}" alt="Sticker"
    data-sid="${esc(st.id)}" data-ext="${esc(st.ext)}" ${savable ? 'title="Click to add to your stickers"' : ""}>`;
}

function renderStickerPanel() {
  let html = `<div class="ep-label">My stickers</div>
    <button class="sp-item sp-add" data-add="1">＋ Add<br>sticker</button>`;
  html += myStickers.map((st) => `<button class="sp-item" data-sid="${esc(st.id)}" data-ext="${esc(st.ext)}" title="Right-click to remove"><img src="${stickerURL(st)}" alt=""></button>`).join("");
  if (!myStickers.length) html += `<div class="sp-hint">Add your own images or GIFs, or click a sticker someone sent you to save it.</div>`;
  html += `<div class="ep-label">OfficeChat stickers</div>`;
  for (const [id, b] of BUILTIN) html += `<button class="sp-item" data-sid="${esc(id)}" title="${esc(b.cap)}"><span class="st-emoji">${b.e}</span><span class="st-cap">${esc(b.cap)}</span></button>`;
  $("stickerGrid").innerHTML = html;
}
async function loadStickers() { try { myStickers = await api("/api/stickers"); } catch {} }

async function sendSticker(st) {
  closePanels();
  try {
    const m = await api("/api/send", { to: S.active, sticker: st });
    addMessage(m);
    renderChatList();
  } catch (e) { toast(e.message); }
}

function closePanels() {
  $("emojiPanel").hidden = true; $("stickerPanel").hidden = true;
  $("btnEmoji").classList.remove("on"); $("btnSticker").classList.remove("on");
}

$("btnSticker").addEventListener("click", async (e) => {
  e.stopPropagation();
  const open = $("stickerPanel").hidden;
  closePanels();
  if (open) { await loadStickers(); renderStickerPanel(); $("stickerPanel").hidden = false; $("btnSticker").classList.add("on"); }
});
$("stickerGrid").addEventListener("click", (e) => {
  const b = e.target.closest(".sp-item"); if (!b) return;
  if (b.dataset.add) { $("stickerPick").click(); return; }
  sendSticker({ id: b.dataset.sid, ext: b.dataset.ext || "" });
});
$("stickerGrid").addEventListener("contextmenu", async (e) => {
  const b = e.target.closest(".sp-item[data-ext]"); if (!b || !b.dataset.ext) return;
  e.preventDefault();
  if (!confirm("Remove this sticker from your stickers?")) return;
  myStickers = await api("/api/stickers/remove", { id: b.dataset.sid });
  renderStickerPanel();
});
$("stickerPick").addEventListener("change", async (e) => {
  const files = [...e.target.files];
  e.target.value = "";
  for (const f of files) {
    try {
      const { data, ext } = await prepareSticker(f);
      myStickers = await api("/api/stickers/add", { data, ext });
    } catch (err) { toast(err.message); }
  }
  renderStickerPanel();
});
// Stickers are kept small: GIFs stay as they are (animation), other images
// are scaled to at most 320px.
async function prepareSticker(file) {
  const toB64 = (buf) => { let s = ""; const b = new Uint8Array(buf); for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode.apply(null, b.subarray(i, i + 0x8000)); return btoa(s); };
  if (file.type === "image/gif") {
    if (file.size > 2 * 1024 * 1024) throw new Error("GIF stickers can be at most 2 MB");
    return { data: toB64(await file.arrayBuffer()), ext: "gif" };
  }
  const url = URL.createObjectURL(file);
  try {
    const img = await new Promise((res, rej) => { const i = new Image(); i.onload = () => res(i); i.onerror = () => rej(new Error("That file isn't an image")); i.src = url; });
    const scale = Math.min(1, 320 / Math.max(img.naturalWidth, img.naturalHeight));
    const c = document.createElement("canvas");
    c.width = Math.max(1, Math.round(img.naturalWidth * scale)); c.height = Math.max(1, Math.round(img.naturalHeight * scale));
    c.getContext("2d").drawImage(img, 0, 0, c.width, c.height);
    const blob = await new Promise((res) => c.toBlob(res, "image/png"));
    return { data: toB64(await blob.arrayBuffer()), ext: "png" };
  } finally { URL.revokeObjectURL(url); }
}
// Click a sticker someone sent you to keep it.
$("messages").addEventListener("click", async (e) => {
  const img = e.target.closest("img.sticker-img.savable"); if (!img) return;
  if (!confirm("Add this sticker to your stickers?")) return;
  try {
    myStickers = await api("/api/stickers/save", { id: img.dataset.sid, ext: img.dataset.ext });
    img.classList.remove("savable"); img.removeAttribute("title");
    toast("Added to your stickers");
  } catch (err) { toast(err.message); }
});

// ------------------------------------------------------------ file sending
$("btnAttach").addEventListener("click", () => $("filePick").click());
$("btnFolder").addEventListener("click", () => $("folderPick").click());
$("filePick").addEventListener("change", (e) => {
  const items = [...e.target.files].map((f) => ({ file: f, rel: f.name }));
  e.target.value = "";
  sendFiles(items);
});
$("folderPick").addEventListener("change", (e) => {
  const files = [...e.target.files];
  e.target.value = "";
  if (!files.length) return;
  const items = files.map((f) => ({ file: f, rel: f.webkitRelativePath || f.name }));
  const top = (items[0].rel.split("/")[0]) || "Folder";
  sendFiles(items, { name: top, folder: true });
});

input.addEventListener("paste", (e) => {
  const files = [...(e.clipboardData && e.clipboardData.files || [])];
  if (!files.length) return;
  e.preventDefault();
  const stamp = new Date().toISOString().replace(/[:T]/g, "-").slice(0, 19);
  sendFiles(files.map((f, i) => {
    const name = f.name && f.name !== "image.png" ? f.name : `Pasted ${stamp}${i ? "-" + i : ""}.${(f.type.split("/")[1] || "png").replace("jpeg", "jpg")}`;
    return { file: f, rel: name };
  }));
});

// Drag & drop of files and whole folders, anywhere in the window. Dropping
// on a person in the list sends it to them; elsewhere, to the open chat.
let dragDepth = 0;
function dropChatFor(e) {
  const item = e.target && e.target.closest && e.target.closest(".chat-item");
  return item ? item.dataset.chat : S.active;
}
function showDrop(chat) {
  $("dropzone").hidden = false;
  $("dropText").textContent = `Drop to send to ${chatName(chat)}`;
  document.querySelectorAll(".chat-item.drop-hover").forEach((x) => x.classList.remove("drop-hover"));
  if (chat !== S.active) {
    const el = document.querySelector(`.chat-item[data-chat="${CSS.escape(chat)}"]`);
    if (el) el.classList.add("drop-hover");
  }
}
function hideDrop() {
  dragDepth = 0;
  $("dropzone").hidden = true;
  document.querySelectorAll(".chat-item.drop-hover").forEach((x) => x.classList.remove("drop-hover"));
}
document.addEventListener("dragenter", (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  dragDepth++;
  showDrop(dropChatFor(e));
});
document.addEventListener("dragover", (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault(); // without this the app would open the file itself
  e.dataTransfer.dropEffect = "copy";
  showDrop(dropChatFor(e));
});
document.addEventListener("dragleave", (e) => {
  if (!hasFiles(e)) return;
  dragDepth--;
  const out = e.clientX <= 0 || e.clientY <= 0 || e.clientX >= innerWidth || e.clientY >= innerHeight;
  if (dragDepth <= 0 || out) hideDrop();
});
document.addEventListener("dragend", hideDrop);
window.addEventListener("blur", () => { if (!$("dropzone").hidden) hideDrop(); });
document.addEventListener("drop", async (e) => {
  e.preventDefault();
  if (!hasFiles(e)) { hideDrop(); return; }
  const to = dropChatFor(e);
  hideDrop();
  // Entries must be taken synchronously, before any await.
  const entries = [...e.dataTransfer.items].filter((i) => i.kind === "file").map((i) => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
  const plain = [...e.dataTransfer.files];
  let items = [];
  if (entries.length) {
    const progress = { n: 0 };
    const t = setInterval(() => toast(`Reading files… ${progress.n.toLocaleString()}`, 1200), 700);
    try { for (const en of entries) await walkEntry(en, items, progress); } finally { clearInterval(t); }
  } else {
    items = plain.map((f) => ({ file: f, rel: f.name }));
  }
  if (!items.length) { toast("Nothing to send (empty folder?)"); return; }
  if (to !== S.active) await openChat(to);
  const dirs = entries.filter((x) => x.isDirectory);
  if (entries.length === 1 && dirs.length === 1) sendFiles(items, { to, name: dirs[0].name, folder: true });
  else sendFiles(items, { to, name: entries.length <= 1 ? items[0].file.name : `${entries.length} items`, folder: dirs.length > 0 });
});
function hasFiles(e) { return !!e.dataTransfer && [...e.dataTransfer.types].includes("Files"); }
async function walkEntry(entry, out, progress) {
  if (entry.isFile) {
    const f = await new Promise((res, rej) => entry.file(res, rej)).catch(() => null);
    if (f) { out.push({ file: f, rel: entry.fullPath.replace(/^\/+/, "") }); if (progress) progress.n++; }
  } else if (entry.isDirectory) {
    const reader = entry.createReader();
    for (;;) {
      const batch = await new Promise((res, rej) => reader.readEntries(res, rej)).catch(() => []);
      if (!batch.length) break;
      for (const child of batch) await walkEntry(child, out, progress);
    }
  }
}

async function sendFiles(items, opts = {}) {
  items = items.filter((i) => !/(^|\/)(\.DS_Store|Thumbs\.db|desktop\.ini)$/i.test(i.rel));
  if (!items.length) return;
  const to = opts.to || S.active;
  const name = opts.name || (items.length === 1 ? items[0].file.name : `${items.length} files`);
  let res;
  try {
    res = await api("/api/send-start", { to, name, folder: !!opts.folder, files: items.map((i) => ({ rel: i.rel, size: i.file.size })) });
  } catch (e) { toast(e.message, 6000); return; }
  const tid = res.tid;
  if (items.length === 1 && (isImage(items[0].rel) || isVideo(items[0].rel))) S.previews.set(tid, URL.createObjectURL(items[0].file));
  const ctl = { cancelled: false, aborts: new Set() };
  S.uploads.set(tid, ctl);
  if (!addMessage(res.msg)) rerenderMessage(res.msg);
  renderChatList();

  // Jobs: every chunk of every file, for every recipient. Small files first
  // finish quickly; large files are spread over parallel connections.
  const jobs = [];
  for (const it of items) {
    const size = it.file.size;
    let off = 0;
    do {
      for (const peer of res.peers) jobs.push({ it, off, len: Math.min(CHUNK, size - off), peer });
      off += CHUNK;
    } while (off < size);
  }
  const failed = new Set();
  let next = 0, lastErr = "";
  async function worker() {
    while (next < jobs.length && !ctl.cancelled) {
      const j = jobs[next++];
      if (failed.has(j.peer)) continue;
      let ok = false;
      for (let attempt = 0; attempt < 5 && !ctl.cancelled; attempt++) {
        try { await uploadChunk(tid, j, ctl); ok = true; break; }
        catch (e) { lastErr = e.message; if (ctl.cancelled || /finished|unknown transfer/.test(e.message)) break; await sleep(600 * (attempt + 1)); }
      }
      if (!ok && !ctl.cancelled) failed.add(j.peer);
    }
  }
  await Promise.all(Array.from({ length: Math.min(PARALLEL, jobs.length) }, worker));
  S.uploads.delete(tid);
  const status = ctl.cancelled ? "cancelled" : failed.size >= res.peers.length ? "failed" : "done";
  if (failed.size && status === "done") toast(`Some people did not receive "${name}": ${lastErr}`, 7000);
  try { await api("/api/send-finish", { tid, status, error: status === "failed" ? lastErr : "", failedPeers: [...failed] }); } catch {}
}

async function uploadChunk(tid, j, ctl) {
  const ac = new AbortController();
  ctl.aborts.add(ac);
  try {
    const q = new URLSearchParams({ tid, peer: j.peer, rel: j.it.rel, size: j.it.file.size, off: j.off });
    const r = await fetch(`/api/upload?${q}`, { method: "POST", headers: { "X-Token": TOKEN }, body: j.it.file.slice(j.off, j.off + j.len), signal: ac.signal });
    if (!r.ok) { const b = await r.json().catch(() => ({})); throw new Error(b.error || r.statusText); }
  } finally { ctl.aborts.delete(ac); }
}

function cancelUpload(tid) {
  const ctl = S.uploads.get(tid);
  if (!ctl) return;
  ctl.cancelled = true;
  for (const a of ctl.aborts) a.abort();
}

window.addEventListener("beforeunload", (e) => {
  if (S.uploads.size) { e.preventDefault(); e.returnValue = "Files are still being sent."; }
});

// ------------------------------------------------------------ notifications
let audioCtx;
function ding() {
  try {
    audioCtx ||= new AudioContext();
    const t = audioCtx.currentTime;
    [880, 1320].forEach((f, i) => {
      const o = audioCtx.createOscillator(), g = audioCtx.createGain();
      o.frequency.value = f; o.type = "sine";
      g.gain.setValueAtTime(0.0001, t + i * 0.12);
      g.gain.exponentialRampToValueAtTime(0.15, t + i * 0.12 + 0.02);
      g.gain.exponentialRampToValueAtTime(0.0001, t + i * 0.12 + 0.25);
      o.connect(g).connect(audioCtx.destination);
      o.start(t + i * 0.12); o.stop(t + i * 0.12 + 0.3);
    });
  } catch {}
}
function notify(m) {
  ding();
  const name = personName(m.from, m.fromName);
  const title = m.to === "all" ? `${name} · Everyone` : name;
  const body = m.kind === "files" ? `📎 ${m.transfer.name} (${fmtSize(m.transfer.total)})` : m.kind === "sticker" ? "Sent a sticker" : m.text;
  if (nativeSend({ type: "notify", title, body, chat: m.chat })) return;
  if (!("Notification" in window) || Notification.permission !== "granted") return;
  const n = new Notification(title, { body, tag: m.chat, silent: true });
  n.onclick = () => { window.focus(); openChat(m.chat); n.close(); };
}
document.addEventListener("click", () => {
  if (!NATIVE && "Notification" in window && Notification.permission === "default") Notification.requestPermission();
}, { once: true });
document.addEventListener("visibilitychange", () => { if (!document.hidden) markRead(); });
window.addEventListener("focus", () => markRead());

// ------------------------------------------------------------------ events
function onEvent(ev) {
  switch (ev.type) {
    case "message": {
      const m = ev.msg;
      if (!addMessage(m)) break;
      if (m.from !== S.me.id) {
        const viewing = m.chat === S.active && isAppVisible() && document.hasFocus();
        if (viewing) markRead(); else { S.chats[m.chat].unread = (S.chats[m.chat].unread || 0) + 1; notify(m); }
        S.typing.delete(m.chat);
        if (m.chat === S.active) renderHeader();
      }
      renderChatList();
      break;
    }
    case "update":
      S.progress.delete(ev.msg.transfer && ev.msg.transfer.id);
      rerenderMessage(ev.msg);
      break;
    case "progress": {
      S.progress.set(ev.tid, ev);
      if (ev.chat !== S.active) break;
      const card = $("messages").querySelector(`.file-card[data-tid="${CSS.escape(ev.tid)}"]`);
      const list = S.msgs.get(ev.chat) || [];
      const m = list.find((x) => x.transfer && x.transfer.id === ev.tid);
      if (!card || !m) break;
      const bar = card.querySelector(".fc-bar > div");
      if (!bar) { rerenderMessage(m); break; }
      bar.style.width = (ev.total ? (ev.done / ev.total) * 100 : 0).toFixed(1) + "%";
      const st = card.querySelector(".fc-status");
      st.textContent = `${m.transfer.status === "sending" ? "Sending" : "Receiving"} · ${fmtSize(ev.done)} of ${fmtSize(ev.total)}` +
        (ev.speed > 0 ? ` · ${fmtSize(ev.speed)}/s · ${fmtEta((ev.total - ev.done) / ev.speed)}` : "");
      break;
    }
    case "peers": {
      const before = [...S.peers.values()].map((p) => p.id + p.name + p.avatar).join("|");
      S.peers = new Map(ev.peers.map((p) => [p.id, p]));
      const after = [...S.peers.values()].map((p) => p.id + p.name + p.avatar).join("|");
      renderMe(); renderChatList(); renderHeader();
      if (before !== after) renderMessages(); // someone renamed: update names in the open chat
      break;
    }
    case "typing":
      S.typing.set(ev.chat, { from: ev.from, until: Date.now() + 4500 });
      if (ev.chat === S.active) { renderHeader(); setTimeout(renderHeader, 4600); }
      break;
    case "appupdate":
      renderUpdate(ev.update);
      break;
    case "reload":
      if (S.msgs.has(ev.chat)) {
        api(`/api/messages?chat=${encodeURIComponent(ev.chat)}`).then((l) => { S.msgs.set(ev.chat, l); if (ev.chat === S.active) renderMessages(); });
      }
      break;
  }
}

async function loadState() {
  const st = await api("/api/state");
  S.me = st.me; S.settings = st.settings;
  applyTheme(st.settings.theme);
  renderUpdate(st.update);
  S.peers = new Map(st.peers.map((p) => [p.id, p]));
  S.chats = st.chats || {};
  renderMe(); renderNetInfo(st); renderChatList(); renderHeader();
  return st;
}

function connectEvents() {
  const es = new EventSource(`/api/events?t=${TOKEN}`);
  let first = true;
  es.onopen = async () => {
    if (first) { first = false; return; }
    // Reconnected (e.g. app restarted): resync everything.
    S.msgs.clear();
    try { await loadState(); openChat(S.active); } catch {}
  };
  es.onmessage = (e) => { try { onEvent(JSON.parse(e.data)); } catch (err) { console.error(err); } };
}

// --------------------------------------------------------------- settings
let pickedAvatar = "";
$("btnSettings").addEventListener("click", openSettings);
async function openSettings() {
  const st = await loadState();
  const s = st.settings;
  $("setName").value = s.name;
  $("setTeam").value = s.teamKey;
  $("setDir").value = s.downloadDir;
  $("setReopen").checked = !!s.reopenOnMessage;
  $("setAutoStart").checked = !!st.autoStart;
  $("autoStartRow").hidden = !(st.os === "darwin" || st.os === "windows");
  pickedAvatar = s.avatar;
  renderAvatarPick();
  renderManual(s.manualPeers || []);
  $("fwBox").hidden = !st.canFixFirewall;
  $("settings").showModal();
}
function renderAvatarPick() {
  const list = AVATARS.includes(pickedAvatar) ? AVATARS : [pickedAvatar, ...AVATARS];
  $("avatarPick").innerHTML = list.map((a) => `<button type="button" class="${a === pickedAvatar ? "on" : ""}">${a}</button>`).join("");
}
$("avatarPick").addEventListener("click", (e) => {
  const b = e.target.closest("button"); if (!b) return;
  pickedAvatar = b.textContent; renderAvatarPick();
});
function renderManual(list) {
  $("manualList").innerHTML = list.map((a) => {
    const p = [...S.peers.values()].find((x) => x.addr === a);
    return `<div class="manual-item"><code>${esc(a)}</code><span class="hint">${p ? esc(p.name) + (p.online ? " · online" : " · offline") : "not reachable yet"}</span>
      <button type="button" class="btn small" data-rm="${esc(a)}">Remove</button></div>`;
  }).join("");
}
$("manualList").addEventListener("click", async (e) => {
  const b = e.target.closest("button[data-rm]"); if (!b) return;
  const st = await api("/api/remove-peer", { addr: b.dataset.rm });
  renderManual(st.settings.manualPeers || []);
});
$("btnAddPeer").addEventListener("click", async () => {
  const addr = $("addAddr").value.trim();
  if (!addr) return;
  const b = $("btnAddPeer"); b.disabled = true; b.textContent = "Connecting…";
  try {
    const info = await api("/api/add-peer", { addr });
    toast(`Connected to ${info.name}`);
    $("addAddr").value = "";
    const st = await loadState();
    renderManual(st.settings.manualPeers || []);
  } catch (e) { toast(e.message, 9000); }
  finally { b.disabled = false; b.textContent = "Connect"; }
});
$("btnFirewall").addEventListener("click", async () => {
  const b = $("btnFirewall"); b.disabled = true;
  try { await api("/api/fix-firewall", {}); toast("Firewall rules added. Other computers can now reach this one."); }
  catch (e) { toast(e.message, 8000); }
  finally { b.disabled = false; }
});
$("btnOpenDir").addEventListener("click", () => api("/api/open-downloads", {}).catch((e) => toast(e.message)));
$("btnSaveSettings").addEventListener("click", async () => {
  try {
    const st = await api("/api/settings", {
      name: $("setName").value, avatar: pickedAvatar, teamKey: $("setTeam").value,
      downloadDir: $("setDir").value, reopenOnMessage: $("setReopen").checked, autoStart: $("setAutoStart").checked,
    });
    S.me = st.me; renderMe();
    $("settings").close();
    toast("Saved");
  } catch (e) { toast(e.message); }
});
$("btnQuit").addEventListener("click", async () => {
  if (!confirm("Quit OfficeChat? You won't receive messages or files until you open it again.")) return;
  await api("/api/quit", {}).catch(() => {});
  document.body.innerHTML = `<div style="margin:auto;padding:80px;text-align:center;color:#888">OfficeChat has quit. You can close this window.</div>`;
  setTimeout(() => window.close(), 300);
});
$("btnClear").addEventListener("click", async () => {
  if (!confirm(`Clear the chat history with ${chatName(S.active)} on this computer?`)) return;
  await api("/api/clear", { chat: S.active });
  S.msgs.set(S.active, []);
  delete S.chats[S.active];
  renderMessages(); renderChatList();
});
$("btnBack").addEventListener("click", () => $("app").classList.remove("in-chat"));

// ---------------------------------------------------------------- updates
let updInfo = null;
function renderUpdate(u) {
  if (!u) return;
  updInfo = u;
  const bar = $("updateBar"), btn = $("btnUpdateNow");
  const show = (title, sub, button) => {
    bar.hidden = false; $("ubTitle").textContent = title; $("ubSub").textContent = sub;
    btn.hidden = !button;
  };
  if (u.state === "downloading") show(`Downloading update ${u.version}`, u.total ? `${Math.floor((u.done / u.total) * 100)}%` : "…", false);
  else if (u.state === "ready") show(`Update ${u.version} is ready`, "Restart to install it", true);
  else if (u.state === "installing") show(`Installing ${u.version}…`, "OfficeChat will restart", false);
  else bar.hidden = true;
  const st = $("updStatus");
  const cur = `Version ${u.current}`;
  st.textContent = {
    disabled: `${cur} (development build — updates off)`,
    checking: `${cur} · checking…`,
    uptodate: `${cur} · you're up to date`,
    downloading: `${cur} · downloading ${u.version}…`,
    ready: `${cur} · ${u.version} ready, restart to install`,
    installing: `${cur} · installing ${u.version}…`,
    error: `${cur} · ${u.error || "update check failed"}`,
  }[u.state] || cur;
}
$("btnUpdateNow").addEventListener("click", async () => {
  if (S.uploads.size && !confirm("Files are still being sent. Restart anyway?")) return;
  $("btnUpdateNow").disabled = true;
  await api("/api/update/install", {}).catch((e) => toast(e.message));
});
$("btnCheckUpdate").addEventListener("click", async () => {
  try { renderUpdate(await api("/api/update/check", {})); } catch (e) { toast(e.message); }
});

// ------------------------------------------------------------------- boot
(async function boot() {
  buildEmoji();
  loadStickers();
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closePanels(); });
  try {
    await loadState();
  } catch (e) {
    document.body.innerHTML = `<div style="padding:60px;text-align:center">Can't reach OfficeChat. Please open it from its icon again.<br><small>${esc(e.message)}</small></div>`;
    return;
  }
  connectEvents();
  openChat("all");
  setInterval(() => { renderChatList(); }, 60e3);
})();
