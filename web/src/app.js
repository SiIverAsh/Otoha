const state = {
  booted: false,
  authEnabled: false,
  authenticated: true,
  user: null,
  mustResetPassword: false,

  tab: "works",
  viewMode: "table",
  query: "",
  ageMode: "general",
  sortBy: "release_date",
  sortDesc: true,
  circle: "",
  va: "",
  tag: "",
  progress: "",
  subtitleOnly: false,
  page: 1,
  pageSize: 40,

  works: [],
  totalWorks: 0,
  facets: { circles: [], vas: [], tags: [] },
  recent: [],
  historyItems: [],
  historyTotal: 0,
  storages: [],
  users: [],
  settings: null,
  asmrCheckpoint: null,

  searchDegraded: false,
  searchWarning: "",
  task: { running: false, phase: "idle", message: "" },

  selectedWorkno: "",
  detail: null,
  editingMeta: false,
  collapsedFolders: {},

  player: {
    workno: "",
    workTitle: "",
    track: null,
    queue: [],
    queueIndex: 0,
    folderScope: "",
    playing: false,
    currentTime: 0,
    duration: 0,
    playbackRate: 1.0,
    incognito: false,
    cues: [],
    activeSubtitle: "",
  },
  heartbeatIntervalSec: 30,
};

const audio = new Audio();
audio.preload = "metadata";

let lastPlayTickMs = 0;
let unsentListenedSec = 0;
let heartbeatTimer = null;
let sseSource = null;

async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {});
  if (opts.body && typeof opts.body === "object" && !(opts.body instanceof FormData)) {
    headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(opts.body);
  }
  const resp = await fetch(path, Object.assign({}, opts, { headers }));
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    const err = new Error(data.error || `HTTP ${resp.status}`);
    err.data = data;
    err.status = resp.status;
    throw err;
  }
  return data;
}

function esc(s) {
  return String(s ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function fmtSec(sec) {
  if (!sec || !isFinite(sec) || sec < 0) return "--:--";
  const total = Math.floor(sec);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const pad = (n) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
}

function fmtTimeAgo(unix) {
  if (!unix) return "-";
  const d = new Date(unix * 1000);
  return d.toLocaleString("zh-CN", { hour12: false });
}

function ageBadgeHTML(cat) {
  switch (Number(cat)) {
    case 1:
      return `<span class="badge badge-sfw">全年龄</span>`;
    case 2:
      return `<span class="badge badge-r15">R15</span>`;
    case 3:
      return `<span class="badge badge-r18">R18</span>`;
    default:
      return `<span class="badge badge-unk">未分级</span>`;
  }
}

const PROGRESS_LABELS = {
  "": "未标记",
  marked: "想听",
  listening: "在听",
  listened: "听过",
  replay: "重听",
  postponed: "搁置",
};

function accumulateListened() {
  if (!state.player.playing || !lastPlayTickMs) return;
  const now = performance.now();
  const delta = Math.max(0, (now - lastPlayTickMs) / 1000) * (audio.playbackRate || 1);
  lastPlayTickMs = now;
  if (delta < 120) {
    unsentListenedSec += delta;
  }
}

function buildHeartbeatPayload() {
  if (!state.player.workno || !state.player.track) return null;
  accumulateListened();
  const delta = Number(unsentListenedSec.toFixed(2));
  unsentListenedSec = 0;
  return {
    workno: state.player.workno,
    track_id: state.player.track.id,
    position: Number((audio.currentTime || 0).toFixed(2)),
    listened_delta: delta,
    track_duration: isFinite(audio.duration) && audio.duration > 0 ? Number(audio.duration.toFixed(2)) : 0,
    incognito: state.player.incognito,
    queue: {
      track_ids: state.player.queue.map((t) => t.id),
      current_index: state.player.queueIndex,
      folder_scope: state.player.folderScope || "",
    },
  };
}

async function sendHeartbeat(useBeacon = false) {
  const payload = buildHeartbeatPayload();
  if (!payload) return;
  if (useBeacon && navigator.sendBeacon) {
    navigator.sendBeacon("/api/history/heartbeat", JSON.stringify(payload));
    return;
  }
  try {
    const res = await api("/api/history/heartbeat", { method: "POST", body: payload });
    if (res && res.auto_advanced && state.detail && state.detail.workno === payload.workno) {
      state.detail.progress = res.progress;
      render();
    }
  } catch (_) {}
}

function resetHeartbeatTimer() {
  if (heartbeatTimer) clearInterval(heartbeatTimer);
  const ms = Math.max(5, state.heartbeatIntervalSec || 30) * 1000;
  heartbeatTimer = setInterval(() => {
    if (state.player.playing && state.player.track) {
      sendHeartbeat(false);
    }
  }, ms);
}

audio.addEventListener("play", () => {
  state.player.playing = true;
  lastPlayTickMs = performance.now();
  renderPlayerOnly();
});

audio.addEventListener("pause", () => {
  accumulateListened();
  lastPlayTickMs = 0;
  state.player.playing = false;
  sendHeartbeat(false);
  renderPlayerOnly();
});

audio.addEventListener("seeked", () => {
  if (state.player.playing) lastPlayTickMs = performance.now();
  state.player.currentTime = audio.currentTime || 0;
  updateActiveSubtitle();
  sendHeartbeat(false);
  renderPlayerOnly();
});

audio.addEventListener("loadedmetadata", () => {
  state.player.duration = isFinite(audio.duration) ? audio.duration : 0;
  sendHeartbeat(false);
  renderPlayerOnly();
});

audio.addEventListener("timeupdate", () => {
  state.player.currentTime = audio.currentTime || 0;
  if (isFinite(audio.duration) && audio.duration > 0) {
    state.player.duration = audio.duration;
  }
  updateActiveSubtitle();
  syncPlayerDOMFast();
});

audio.addEventListener("ended", async () => {
  await sendHeartbeat(false);
  if (state.player.queueIndex + 1 < state.player.queue.length) {
    playQueueIndex(state.player.queueIndex + 1, 0);
  } else {
    state.player.playing = false;
    renderPlayerOnly();
  }
});

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "hidden" && state.player.track) {
    sendHeartbeat(true);
  }
});

window.addEventListener("pagehide", () => {
  if (state.player.track) {
    sendHeartbeat(true);
  }
});

function updateActiveSubtitle() {
  const t = state.player.currentTime;
  const cues = state.player.cues || [];
  let text = "";
  for (let i = 0; i < cues.length; i++) {
    if (t >= cues[i].start && t < cues[i].end) {
      text = cues[i].text.replace(/\n/g, " / ");
      break;
    }
  }
  state.player.activeSubtitle = text;
}

function syncPlayerDOMFast() {
  const seekEl = document.getElementById("player-seek");
  const timeEl = document.getElementById("player-time");
  const subEl = document.getElementById("player-sub");
  if (seekEl && document.activeElement !== seekEl) {
    seekEl.max = String(Math.max(1, Math.floor(state.player.duration || 1)));
    seekEl.value = String(Math.floor(state.player.currentTime || 0));
  }
  if (timeEl) {
    timeEl.textContent = `${fmtSec(state.player.currentTime)} / ${fmtSec(state.player.duration)}`;
  }
  if (subEl) {
    subEl.textContent = state.player.activeSubtitle || "";
  }
}

async function loadSubtitles(trackId) {
  state.player.cues = [];
  state.player.activeSubtitle = "";
  try {
    const res = await api(`/api/subtitle/${trackId}`);
    if (res && res.found && Array.isArray(res.cues)) {
      state.player.cues = res.cues;
      updateActiveSubtitle();
    }
  } catch (_) {}
  renderPlayerOnly();
}

async function playQueueIndex(idx, resumeSec = null) {
  if (idx < 0 || idx >= state.player.queue.length) return;
  if (state.player.track) {
    await sendHeartbeat(false);
  }
  const tr = state.player.queue[idx];
  state.player.queueIndex = idx;
  state.player.track = tr;
  state.player.duration = tr.duration || 0;
  audio.src = `/api/stream/${tr.id}`;
  audio.playbackRate = state.player.playbackRate || 1.0;

  const startAt = resumeSec !== null ? resumeSec : tr.play_position || 0;
  audio.currentTime = startAt > 0 ? startAt : 0;
  state.player.currentTime = audio.currentTime;
  loadSubtitles(tr.id);
  try {
    await audio.play();
  } catch (_) {}
  render();
}

async function bootstrap() {
  const me = await api("/api/auth/me");
  state.authEnabled = !!me.auth_enabled;
  state.authenticated = !!me.authenticated;
  state.user = me.user;
  state.ageMode = me.default_hide_nsfw ? "general" : "all";
  state.heartbeatIntervalSec = me.heartbeat_interval_sec || 30;
  state.searchDegraded = !!me.search_degraded;
  state.searchWarning = me.search_warning || "";
  resetHeartbeatTimer();

  if (state.authenticated) {
    connectTaskSSE();
    await Promise.all([loadWorks(), loadFacets(), loadRecent()]);
  }
  state.booted = true;
  render();
}

function connectTaskSSE() {
  if (sseSource) sseSource.close();
  sseSource = new EventSource("/api/tasks/events");
  sseSource.onmessage = (ev) => {
    try {
      const snap = JSON.parse(ev.data);
      const wasRunning = state.task.running;
      state.task = snap;
      if (snap.asmr_sync) {
        state.asmrCheckpoint = snap.asmr_sync;
      }
      if (wasRunning && !snap.running) {
        loadWorks();
        loadFacets();
      } else {
        render();
      }
    } catch (_) {}
  };
}

async function loadWorks() {
  const params = new URLSearchParams({
    page: String(state.page),
    page_size: String(state.pageSize),
    age: state.ageMode,
    sort_by: state.sortBy,
    sort_desc: state.sortDesc ? "1" : "0",
  });
  if (state.circle) params.set("circle", state.circle);
  if (state.va) params.set("va", state.va);
  if (state.tag) params.set("tag", state.tag);
  if (state.progress) params.set("progress", state.progress);
  if (state.subtitleOnly) params.set("subtitle_only", "1");

  let data;
  if (state.query.trim()) {
    params.set("q", state.query.trim());
    data = await api(`/api/search?${params.toString()}`);
    state.searchDegraded = !!data.degraded;
    state.searchWarning = data.warning || "";
  } else {
    data = await api(`/api/works?${params.toString()}`);
  }
  state.works = data.items || [];
  state.totalWorks = data.total || 0;
  render();
}

async function loadFacets() {
  const data = await api(`/api/works/facets?age=${encodeURIComponent(state.ageMode)}`);
  state.facets = {
    circles: data.circles || [],
    vas: data.vas || [],
    tags: data.tags || [],
  };
  render();
}

async function loadRecent() {
  const data = await api(`/api/history/recent?age=${encodeURIComponent(state.ageMode)}&limit=8`);
  state.recent = data.items || [];
  render();
}

async function loadHistoryTab() {
  const data = await api(`/api/history?age=${encodeURIComponent(state.ageMode)}&page=1&page_size=80`);
  state.historyItems = data.items || [];
  state.historyTotal = data.total || 0;
  render();
}

async function loadSettingsTab() {
  const [settings, storages, asmrSt] = await Promise.all([
    api("/api/settings"),
    api("/api/storages"),
    api("/api/asmrone/status").catch(() => ({ checkpoint: null })),
  ]);
  state.settings = settings;
  state.storages = storages.items || [];
  state.asmrCheckpoint = asmrSt.checkpoint || state.asmrCheckpoint;
  if (state.user && state.user.is_admin) {
    const u = await api("/api/users").catch(() => ({ items: [] }));
    state.users = u.items || [];
  }
  render();
}

async function openWorkDrawer(workno) {
  state.selectedWorkno = workno;
  state.editingMeta = false;
  state.detail = await api(`/api/works/${encodeURIComponent(workno)}`);
  render();
}

async function resumeFromHistory(entry) {
  const det = await api(`/api/works/${encodeURIComponent(entry.workno)}`);
  state.selectedWorkno = det.workno;
  state.detail = det;

  const trackById = new Map((det.tracks || []).map((t) => [t.id, t]));
  let queue = [];
  let idx = 0;
  let folderScope = "";

  if (entry.queue_ctx) {
    try {
      const parsed = JSON.parse(entry.queue_ctx);
      if (Array.isArray(parsed.track_ids) && parsed.track_ids.length > 0) {
        queue = parsed.track_ids.map((id) => trackById.get(id)).filter(Boolean);
        idx = Math.min(Math.max(0, parsed.current_index || 0), Math.max(0, queue.length - 1));
        folderScope = parsed.folder_scope || "";
      }
    } catch (_) {}
  }
  if (queue.length === 0 && det.tracks && det.tracks.length > 0) {
    queue = det.tracks;
    const foundIdx = queue.findIndex((t) => t.id === entry.track_id);
    idx = foundIdx >= 0 ? foundIdx : 0;
  }
  if (queue.length > 0) {
    state.player.workno = det.workno;
    state.player.workTitle = det.title;
    state.player.queue = queue;
    state.player.folderScope = folderScope;
    await playQueueIndex(idx, entry.position || 0);
  } else {
    render();
  }
}

function startPlayFromTree(trackId, parentFolderRel) {
  if (!state.detail || !state.detail.tracks) return;
  const all = state.detail.tracks;
  const scoped = all.filter((t) => {
    const slash = t.rel_path.replace(/\\/g, "/");
    const lastSlash = slash.lastIndexOf("/");
    const dir = lastSlash >= 0 ? slash.slice(0, lastSlash) : "";
    return dir === parentFolderRel;
  });
  const queue = scoped.length > 0 ? scoped : all;
  const idx = Math.max(0, queue.findIndex((t) => t.id === trackId));
  state.player.workno = state.detail.workno;
  state.player.workTitle = state.detail.title;
  state.player.queue = queue;
  state.player.folderScope = parentFolderRel;
  playQueueIndex(idx, queue[idx].play_position > 2 ? queue[idx].play_position : 0);
}

function renderTreeNodes(nodes, parentRel = "") {
  if (!nodes || nodes.length === 0) return "";
  return nodes
    .map((n) => {
      if (n.type === "folder") {
        const key = `${state.detail.workno}:${n.rel_path}`;
        const collapsed = !!state.collapsedFolders[key];
        return `
          <div class="tree-folder">
            <div class="tree-folder-label" data-action="toggle-folder" data-folder-key="${esc(key)}">
              ${collapsed ? "▸" : "▾"} 📁 ${esc(n.name)}
            </div>
            ${collapsed ? "" : renderTreeNodes(n.children || [], n.rel_path)}
          </div>
        `;
      }
      const tr = n.track;
      const isPlaying = state.player.track && state.player.track.id === tr.id;
      const dur = tr.duration || 0;
      const pos = tr.play_position || 0;
      const pct = dur > 0 ? Math.min(100, Math.round((pos / dur) * 100)) : 0;
      return `
        <div class="tree-track ${isPlaying ? "playing" : ""}" data-action="play-track" data-track-id="${tr.id}" data-folder-rel="${esc(parentRel)}" title="${esc(tr.rel_path)}">
          <div class="tree-track-row">
            <span class="tree-track-name">${isPlaying ? "▶ " : ""}${esc(n.name)}</span>
            <span class="tree-track-meta">
              ${tr.has_subtitle ? `<span class="badge badge-sfw">字幕</span>` : ""}
              ${pos > 0 ? `${fmtSec(pos)} / ` : ""}${fmtSec(dur)}
            </span>
          </div>
          ${
            pos > 0
              ? `<div class="track-progress-bg"><div class="track-progress-fill" style="width:${pct}%"></div></div>`
              : ""
          }
        </div>
      `;
    })
    .join("");
}

function parseSafeJSON(raw, fallback) {
  if (!raw) return fallback;
  try {
    return JSON.parse(raw);
  } catch (_) {
    return fallback;
  }
}

function renderDrawer() {
  const d = state.detail;
  if (!d) return "";
  const userTags = (d.tags || []).filter((t) => t.source === "user").map((t) => t.name);
  const dlsiteTags = (d.tags || []).filter((t) => t.source !== "user");
  const langEditions = parseSafeJSON(d.language_editions_json, []);
  const ranks = parseSafeJSON(d.rank_json, []);
  const remoteAttachments = parseSafeJSON(d.remote_files_json, []);
  const bestRank = Array.isArray(ranks) && ranks.length > 0 ? Math.min(...ranks.map((r) => r.rank || 9999)) : 0;

  return `
    <aside class="drawer">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px;">
        <span class="mono">
          ${esc(d.workno)} ${ageBadgeHTML(d.age_category)}
          ${d.has_subtitle ? `<span class="badge badge-sfw">含字幕</span>` : ""}
        </span>
        <div style="display:flex;gap:4px;">
          <button data-action="sync-work-tracks" title="从 asmr.one 同步远端音轨树与字幕">同步音轨</button>
          <button data-action="toggle-edit-meta">${state.editingMeta ? "取消编辑" : "编辑"}</button>
          <button data-action="scrape-work" title="刷新该作品元数据">刷新</button>
          <button data-action="close-drawer">✕</button>
        </div>
      </div>

      <div class="drawer-header">
        <img class="drawer-cover" src="/api/cover/${esc(d.workno)}" alt="${esc(d.workno)}" />
        <div style="min-width:0;flex:1;">
          <div class="drawer-title" title="${esc(d.title)}">${esc(d.title)}</div>
          <div style="color:var(--muted);margin-bottom:3px;">
            ${d.circle_name ? `社团：<a href="#" data-action="filter-circle" data-val="${esc(d.circle_name)}" style="color:#93c5fd;">${esc(d.circle_name)}</a>` : ""}
            ${d.release_date ? ` · ${esc(d.release_date)}` : ""}
          </div>
          <div style="color:var(--muted);">
            ${(d.vas || []).length ? `CV：${d.vas.map((v) => `<a href="#" data-action="filter-va" data-val="${esc(v)}" style="color:#cbd5e1;margin-right:6px;">${esc(v)}</a>`).join("")}` : "CV：-"}
          </div>
          <div style="color:var(--muted);font-size:11px;margin-top:2px;" title="剧本: ${(d.scenarios || []).join(", ") || "-"} | 插画: ${(d.illusts || []).join(", ") || "-"} | 音乐: ${(d.musics || []).join(", ") || "-"}">
            评分 ${d.rating ? d.rating.toFixed(2) : "-"} (${d.rate_count || 0}) · 销量 ${d.dl_count || 0} · ¥${d.price || 0}
            ${d.duration ? ` · 时长 ${fmtSec(d.duration)}` : ""}
            ${bestRank > 0 && bestRank < 9999 ? ` · 最高榜 #${bestRank}` : ""}
            ${d.series_name ? ` · 系列：${esc(d.series_name)}` : ""}
          </div>
          ${
            Array.isArray(langEditions) && langEditions.length > 0
              ? `<div style="margin-top:4px;">
                  ${langEditions
                    .map(
                      (ed) =>
                        `<span class="tag-pill ${ed.workno === d.workno ? "user-tag" : ""}" data-action="jump-edition" data-workno="${esc(ed.workno)}" style="cursor:pointer;" title="切换/检索语言版本 ${esc(ed.workno)}">${esc(ed.label || ed.lang)}:${esc(ed.workno)}</span>`
                    )
                    .join("")}
                </div>`
              : ""
          }
        </div>
      </div>

      ${
        state.editingMeta
          ? `
        <div class="drawer-section">
          <div class="drawer-section-title">手工编辑元数据（离线兜底）</div>
          <div class="form-row"><label>标题</label><input id="meta-title" value="${esc(d.title)}" style="flex:1" /></div>
          <div class="form-row"><label>社团</label><input id="meta-circle" value="${esc(d.circle_name)}" style="flex:1" /></div>
          <div class="form-row"><label>发售日</label><input id="meta-date" value="${esc(d.release_date)}" placeholder="YYYY-MM-DD" style="flex:1" /></div>
          <div class="form-row">
            <label>分级</label>
            <select id="meta-age" style="flex:1">
              <option value="1" ${d.age_category === 1 ? "selected" : ""}>全年龄 (GEN)</option>
              <option value="2" ${d.age_category === 2 ? "selected" : ""}>R15</option>
              <option value="3" ${d.age_category === 3 ? "selected" : ""}>R18</option>
              <option value="0" ${!d.age_category ? "selected" : ""}>未分级</option>
            </select>
          </div>
          <div class="form-row"><label>声优 (逗号分隔)</label><input id="meta-vas" value="${esc((d.vas || []).join(", "))}" style="flex:1" /></div>
          <div class="form-row"><label>DLsite标签</label><input id="meta-tags" value="${esc(dlsiteTags.map((t) => t.name).join(", "))}" style="flex:1" /></div>
          <button class="primary" data-action="save-meta" style="width:100%;">保存元数据</button>
        </div>
      `
          : ""
      }

      <div class="drawer-section">
        <div class="drawer-section-title">个人状态与自定义标签</div>
        <div style="display:flex;gap:6px;margin-bottom:6px;">
          <select id="state-progress" data-action="change-state">
            ${Object.entries(PROGRESS_LABELS)
              .map(([k, label]) => `<option value="${k}" ${(d.progress || "") === k ? "selected" : ""}>${label}</option>`)
              .join("")}
          </select>
          <select id="state-rating" data-action="change-state">
            <option value="0">未评分</option>
            ${[5, 4, 3, 2, 1]
              .map((r) => `<option value="${r}" ${d.user_rating === r ? "selected" : ""}>${"★".repeat(r)}</option>`)
              .join("")}
          </select>
          <input id="state-review" value="${esc(d.state?.review || "")}" placeholder="短评备注..." style="flex:1;" />
          <button data-action="save-state">保存</button>
        </div>
        <div style="margin-bottom:6px;">
          ${(d.tags || [])
            .map(
              (t) =>
                `<span class="tag-pill ${t.source === "user" ? "user-tag" : ""}" data-action="filter-tag" data-val="${esc(t.name)}" style="cursor:pointer;">${esc(t.name)}</span>`
            )
            .join("")}
        </div>
        <div style="display:flex;gap:6px;">
          <input id="user-tags-input" value="${esc(userTags.join(", "))}" placeholder="自定义标签（逗号分隔）" style="flex:1;" />
          <button data-action="save-user-tags">更新标签</button>
        </div>
      </div>

      <div class="drawer-section">
        <div class="drawer-section-title">
          <span>音轨目录树 (${d.tracks ? d.tracks.length : 0})</span>
          <span style="font-weight:400;">按所在版本目录独立建队</span>
        </div>
        ${d.tree && d.tree.length > 0 ? renderTreeNodes(d.tree, "") : `<div style="color:var(--muted);">暂无本地音轨文件</div>`}
      </div>

      ${
        Array.isArray(remoteAttachments) && remoteAttachments.length > 0
          ? `<div class="drawer-section">
              <div class="drawer-section-title">特典与文本附件 (${remoteAttachments.length})</div>
              ${remoteAttachments
                .slice(0, 20)
                .map(
                  (att) =>
                    `<div style="display:flex;justify-content:space-between;font-size:11px;padding:2px 0;">
                      <span style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${esc(att.rel_path)}">[${esc(att.type)}] ${esc(att.rel_path)}</span>
                      ${att.download_url ? `<a href="${esc(att.download_url)}" target="_blank" rel="noreferrer" style="color:#93c5fd;flex-shrink:0;margin-left:6px;">查看</a>` : ""}
                    </div>`
                )
                .join("")}
            </div>`
          : ""
      }

      ${
        d.paths && d.paths.length > 0
          ? `<div class="drawer-section">
              <div class="drawer-section-title">存储路径 (${d.paths.length})</div>
              ${d.paths.map((p) => `<div class="mono" style="color:var(--muted);font-size:11px;word-break:break-all;">[${esc(p.storage_name)}] ${esc(p.rel_path)}</div>`).join("")}
            </div>`
          : ""
      }
    </aside>
  `;
}

function renderWorksTab() {
  const totalPages = Math.max(1, Math.ceil(state.totalWorks / state.pageSize));
  return `
    ${
      state.recent.length > 0
        ? `<div class="recent-strip">
            ${state.recent
              .map(
                (r) => `
              <div class="recent-chip" data-action="resume-recent" data-workno="${esc(r.workno)}" title="继续播放：${esc(r.work_title)} (${fmtSec(r.position)})">
                <img src="/api/cover/${esc(r.workno)}" alt="" />
                <div class="recent-meta">
                  <div class="recent-title">▶ ${esc(r.work_title)}</div>
                  <div class="recent-sub">${esc(r.track_name || r.workno)} · 续播 ${fmtSec(r.position)}</div>
                </div>
              </div>
            `
              )
              .join("")}
          </div>`
        : ""
    }

    <div class="filter-strip">
      <select id="filter-age">
        <option value="general" ${state.ageMode === "general" ? "selected" : ""}>仅全年龄 (SFW)</option>
        <option value="sfw_unrated" ${state.ageMode === "sfw_unrated" ? "selected" : ""}>全年龄 + 未分级</option>
        <option value="r15" ${state.ageMode === "r15" ? "selected" : ""}>R15 及以下</option>
        <option value="r18" ${state.ageMode === "r18" ? "selected" : ""}>仅 R18</option>
        <option value="all" ${state.ageMode === "all" ? "selected" : ""}>全部分级</option>
      </select>

      <select id="filter-progress">
        <option value="">全部进度</option>
        ${Object.entries(PROGRESS_LABELS)
          .filter(([k]) => k !== "")
          .map(([k, v]) => `<option value="${k}" ${state.progress === k ? "selected" : ""}>${v}</option>`)
          .join("")}
      </select>

      <select id="filter-circle">
        <option value="">全部社团 (${state.facets.circles.length})</option>
        ${state.facets.circles
          .map((c) => `<option value="${esc(c.name)}" ${state.circle === c.name ? "selected" : ""}>${esc(c.name)} (${c.count})</option>`)
          .join("")}
      </select>

      <select id="filter-va">
        <option value="">全部声优 (${state.facets.vas.length})</option>
        ${state.facets.vas
          .map((v) => `<option value="${esc(v.name)}" ${state.va === v.name ? "selected" : ""}>${esc(v.name)} (${v.count})</option>`)
          .join("")}
      </select>

      <select id="filter-tag">
        <option value="">全部标签 (${state.facets.tags.length})</option>
        ${state.facets.tags
          .map((t) => `<option value="${esc(t.name)}" ${state.tag === t.name ? "selected" : ""}>${esc(t.name)} (${t.count})</option>`)
          .join("")}
      </select>

      <select id="filter-sort">
        <option value="release_date" ${state.sortBy === "release_date" ? "selected" : ""}>按发售日</option>
        <option value="created_at" ${state.sortBy === "created_at" ? "selected" : ""}>按入库时间</option>
        <option value="rating" ${state.sortBy === "rating" ? "selected" : ""}>按评分</option>
        <option value="dl_count" ${state.sortBy === "dl_count" ? "selected" : ""}>按销量</option>
        <option value="workno" ${state.sortBy === "workno" ? "selected" : ""}>按作品号</option>
      </select>

      <button class="${state.subtitleOnly ? "active" : ""}" data-action="toggle-sub-only">带字幕</button>

      ${
        state.circle || state.va || state.tag || state.progress || state.query || state.subtitleOnly
          ? `<button data-action="reset-filters">重置筛选</button>`
          : ""
      }

      <div style="margin-left:auto;display:flex;align-items:center;gap:6px;">
        <span style="color:var(--muted);">共 ${state.totalWorks} 部</span>
        <button class="${state.viewMode === "table" ? "active" : ""}" data-action="set-view" data-mode="table">紧凑表格</button>
        <button class="${state.viewMode === "grid" ? "active" : ""}" data-action="set-view" data-mode="grid">封面网格</button>
      </div>
    </div>

    ${
      state.viewMode === "table"
        ? `
      <table class="compact-table works-table">
        <thead>
          <tr>
            <th style="width:98px;">作品号</th>
            <th style="width:62px;">分级</th>
            <th>标题</th>
            <th style="width:130px;">社团</th>
            <th style="width:150px;">声优</th>
            <th style="width:190px;">标签</th>
            <th style="width:66px;">进度</th>
            <th style="width:56px;">音轨</th>
            <th style="width:86px;">发售日</th>
          </tr>
        </thead>
        <tbody>
          ${
            state.works.length === 0
              ? `<tr><td colspan="9" style="text-align:center;color:var(--muted);padding:24px;">暂无匹配作品（请前往「设置与存储」添加目录扫描，或切换左上角分级筛选）</td></tr>`
              : state.works
                  .map(
                    (w) => `
              <tr class="${state.selectedWorkno === w.workno ? "selected" : ""}" data-action="open-work" data-workno="${esc(w.workno)}"
                  title="${esc(w.workno)} · ${esc(w.title)} | 评分:${w.rating || "-"} 销量:${w.dl_count || 0}">
                <td class="mono">${esc(w.workno)}</td>
                <td>${ageBadgeHTML(w.age_category)}</td>
                <td class="work-title-cell">${w.has_subtitle ? `<span class="badge badge-sfw">字</span>` : ""}${esc(w.title)}</td>
                <td>${esc(w.circle_name || "-")}</td>
                <td title="${esc((w.vas || []).join(", "))}">${esc((w.vas || []).join(" / ") || "-")}</td>
                <td title="${esc((w.tags || []).map((t) => t.name).join(", "))}">
                  ${(w.tags || [])
                    .slice(0, 3)
                    .map((t) => `<span class="tag-pill ${t.source === "user" ? "user-tag" : ""}">${esc(t.name)}</span>`)
                    .join("")}
                </td>
                <td>${esc(PROGRESS_LABELS[w.progress || ""] || "-")}</td>
                <td>${w.track_count || 0}</td>
                <td class="mono">${esc(w.release_date || "-")}</td>
              </tr>
            `
                  )
                  .join("")
          }
        </tbody>
      </table>
    `
        : `
      <div class="card-grid">
        ${state.works
          .map(
            (w) => `
          <div class="work-card" data-action="open-work" data-workno="${esc(w.workno)}" title="${esc(w.title)}">
            <img src="/api/cover/${esc(w.workno)}" loading="lazy" alt="${esc(w.workno)}" />
            <div class="work-card-body">
              <div style="margin-bottom:3px;">${ageBadgeHTML(w.age_category)}<span class="mono" style="color:var(--muted);">${esc(w.workno)}</span></div>
              <div class="work-card-title">${esc(w.title)}</div>
              <div style="color:var(--muted);font-size:11px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">
                ${esc(w.circle_name || "-")} · ${(w.vas || []).join("/") || "-"}
              </div>
            </div>
          </div>
        `
          )
          .join("")}
      </div>
    `
    }

    <div class="pagination">
      <button data-action="prev-page" ${state.page <= 1 ? "disabled" : ""}>上一页</button>
      <span style="color:var(--muted);">第 ${state.page} / ${totalPages} 页</span>
      <button data-action="next-page" ${state.page >= totalPages ? "disabled" : ""}>下一页</button>
    </div>
  `;
}

function renderHistoryTab() {
  return `
    <div class="filter-strip">
      <span style="color:var(--muted);">收听会话历史（30 分钟内自动聚合 · 当前分级过滤：${esc(state.ageMode)}）</span>
      <div style="margin-left:auto;display:flex;gap:6px;">
        <button data-action="clear-history-days" data-days="7">清除近 7 天</button>
        <button class="danger" data-action="clear-history-all">清空全部历史并压缩数据库 (VACUUM)</button>
      </div>
    </div>

    <table class="compact-table">
      <thead>
        <tr>
          <th style="width:98px;">作品号</th>
          <th style="width:62px;">分级</th>
          <th>作品标题</th>
          <th style="width:200px;">续播音轨</th>
          <th style="width:88px;">续播位置</th>
          <th style="width:88px;">实听时长</th>
          <th style="width:155px;">最后收听</th>
          <th style="width:110px;">操作</th>
        </tr>
      </thead>
      <tbody>
        ${
          state.historyItems.length === 0
            ? `<tr><td colspan="8" style="text-align:center;color:var(--muted);padding:24px;">暂无收听记录</td></tr>`
            : state.historyItems
                .map(
                  (h) => `
            <tr>
              <td class="mono">${esc(h.workno)}</td>
              <td>${ageBadgeHTML(h.age_category)}</td>
              <td class="work-title-cell">${esc(h.work_title)}</td>
              <td title="${esc(h.track_name)}">${esc(h.track_name || "-")}</td>
              <td class="mono">${fmtSec(h.position)}</td>
              <td class="mono">${fmtSec(h.listened)}</td>
              <td class="mono">${esc(fmtTimeAgo(h.last_at))}</td>
              <td>
                <button data-action="resume-history" data-id="${h.id}">续播</button>
                <button class="danger" data-action="delete-history" data-id="${h.id}">删除</button>
              </td>
            </tr>
          `
                )
                .join("")
        }
      </tbody>
    </table>
  `;
}

function renderSettingsTab() {
  const s = state.settings || {};
  return `
    <div class="settings-grid">
      <div class="panel-box">
        <h3>媒体库存储与扫描</h3>
        <div style="margin-bottom:8px;">
          ${
            state.storages.length === 0
              ? `<div style="color:var(--muted);margin-bottom:6px;">尚未配置本地音声目录</div>`
              : state.storages
                  .map(
                    (st) => `
              <div style="display:flex;align-items:center;justify-content:space-between;padding:4px 0;border-bottom:1px solid var(--border);">
                <div>
                  <strong>${esc(st.name)}</strong>
                  <span class="mono" style="color:var(--muted);margin-left:6px;">${esc(st.root)}</span>
                </div>
                <div style="display:flex;gap:4px;">
                  <button data-action="scan-storage" data-id="${st.id}">扫描</button>
                  <button class="danger" data-action="delete-storage" data-id="${st.id}">移除</button>
                </div>
              </div>
            `
                  )
                  .join("")
          }
        </div>
        <div style="display:flex;gap:6px;margin-bottom:10px;">
          <input id="new-st-name" placeholder="别名 (如 Voices)" style="width:110px;" />
          <input id="new-st-root" placeholder="本地绝对路径 (如 D:\\VOICES)" style="flex:1;" />
          <button class="primary" data-action="add-storage">添加存储</button>
        </div>
        <div style="display:flex;gap:6px;flex-wrap:wrap;">
          <button class="primary" data-action="scan-all" data-scrape="0">仅扫描本地文件（离线）</button>
          <button data-action="scan-all" data-scrape="1">扫描并抓取 DLsite 元数据</button>
          <button data-action="reindex-bleve">重建全文索引</button>
          ${state.task.running ? `<button class="danger" data-action="cancel-task">中断当前任务</button>` : ""}
        </div>

        <div style="border-top:1px solid var(--border);margin-top:10px;padding-top:10px;">
          <h3 style="margin-bottom:6px;">asmr.one 全站目录与音轨树断点同步</h3>
          ${
            state.asmrCheckpoint
              ? `<div style="font-size:11px;color:var(--muted);margin-bottom:8px;line-height:1.6;">
                  <div><strong>进度：</strong>第 ${state.asmrCheckpoint.current_page || 0} / ${state.asmrCheckpoint.total_pages || "?"} 页（总库 ${state.asmrCheckpoint.total_count || "?"} 部 · 新增 ${state.asmrCheckpoint.inserted || 0} · 重复跳过 ${state.asmrCheckpoint.duplicates || 0} · 失败 ${state.asmrCheckpoint.failures || 0}）</div>
                  <div><strong>字段完整度：</strong>社团 ${state.asmrCheckpoint.completeness?.circle_rate_pct ?? 0}% · 声优 ${state.asmrCheckpoint.completeness?.va_rate_pct ?? 0}% · 标签 ${state.asmrCheckpoint.completeness?.tags_rate_pct ?? 0}% · 封面 ${state.asmrCheckpoint.completeness?.cover_rate_pct ?? 0}%</div>
                  <div><strong>速度 / ETA：</strong>${state.asmrCheckpoint.works_per_sec || 0} 部/s · 下次续跑页码 #${state.asmrCheckpoint.next_page || 1}${state.asmrCheckpoint.eta_seconds ? ` · 预计剩余 ${fmtSec(state.asmrCheckpoint.eta_seconds)}` : ""}</div>
                </div>`
              : `<div style="font-size:11px;color:var(--muted);margin-bottom:8px;">支持按页增量写入 SQLite + Bleve 并保存精确页码断点；点开任意作品自动拉取远端音轨目录树与同步字幕。</div>`
          }
          <div style="display:flex;gap:6px;flex-wrap:wrap;">
            <button class="primary" data-action="sync-asmrone-full">启动 / 断点续跑全站同步</button>
            <button data-action="sync-asmrone-latest">快速同步最新 200 部</button>
            ${state.task.running ? `<button class="danger" data-action="cancel-task">暂停并保存断点</button>` : ""}
          </div>
        </div>
      </div>

      <div class="panel-box">
        <h3>隐私、分级与代理设置</h3>
        <div class="form-row">
          <label>默认隐藏非全年龄作品 (SFW)</label>
          <input type="checkbox" id="cfg-hide-nsfw" ${s.default_hide_nsfw ? "checked" : ""} />
        </div>
        <div class="form-row">
          <label>开启收听历史记录（关闭后不写任何记录）</label>
          <input type="checkbox" id="cfg-history-enabled" ${s.history_enabled ? "checked" : ""} />
        </div>
        <div class="form-row">
          <label>历史保留上限（天数 / 条数）</label>
          <div style="display:flex;gap:4px;">
            <input type="number" id="cfg-hist-days" value="${s.history_max_days ?? 90}" style="width:72px;" />
            <input type="number" id="cfg-hist-entries" value="${s.history_max_entries ?? 500}" style="width:72px;" />
          </div>
        </div>
        <div class="form-row">
          <label>播放心跳间隔（秒）</label>
          <input type="number" id="cfg-hb-sec" value="${s.heartbeat_interval_sec ?? 30}" style="width:90px;" />
        </div>
        <div class="form-row">
          <label>元数据语言 (locale)</label>
          <select id="cfg-locale">
            ${["zh_cn", "ja_jp", "zh_tw", "en_us", "ko_kr"]
              .map((l) => `<option value="${l}" ${s.scraper_locale === l ? "selected" : ""}>${l}</option>`)
              .join("")}
          </select>
        </div>
        <div class="form-row">
          <label>抓取代理（支持账密 HTTP/SOCKS5）</label>
          <input id="cfg-proxy" value="${esc(s.scraper_proxy || "")}" placeholder="socks5://user:pass@127.0.0.1:1080" style="width:220px;" />
        </div>
        <div class="form-row">
          <label>抓取固定间隔（秒）</label>
          <input type="number" step="0.5" id="cfg-interval" value="${s.scraper_interval_sec ?? 3}" style="width:90px;" />
        </div>
        <div class="form-row">
          <label>启用 asmr.one 四级元数据兜底</label>
          <input type="checkbox" id="cfg-asmrone" ${s.asmrone_enabled !== false ? "checked" : ""} />
        </div>
        <div class="form-row">
          <label>最大扫描深度</label>
          <input type="number" id="cfg-depth" value="${s.scan_max_depth ?? 6}" style="width:90px;" />
        </div>
        <div class="form-row">
          <label>启用只读 WebDAV (/dav/)</label>
          <input type="checkbox" id="cfg-webdav" ${s.webdav_enabled ? "checked" : ""} />
        </div>
        <div class="form-row">
          <label>开启多用户登录认证</label>
          <input type="checkbox" id="cfg-auth" ${s.auth_enabled ? "checked" : ""} />
        </div>
        <button class="primary" data-action="save-settings" style="width:100%;">保存设置</button>
      </div>

      <div class="panel-box">
        <h3>用户账号与元数据备份</h3>
        <div style="display:flex;gap:6px;margin-bottom:8px;">
          <input id="new-user-name" placeholder="用户名" style="width:100px;" />
          <input id="new-user-pwd" type="password" placeholder="密码 (≥4位)" style="flex:1;" />
          <label style="display:flex;align-items:center;gap:3px;"><input type="checkbox" id="new-user-admin" checked />管理员</label>
          <button data-action="create-user">保存账号</button>
        </div>
        <div style="margin-bottom:10px;color:var(--muted);">
          当前用户：${(state.users || []).map((u) => `${esc(u.username)}${u.must_reset_password ? "(待设密)" : ""}`).join(", ") || "default"}
        </div>
        <div style="display:flex;gap:6px;">
          <a href="/api/export/metadata" target="_blank"><button type="button">导出元数据 JSON</button></a>
          <label style="cursor:pointer;border:1px solid var(--border);padding:4px 10px;border-radius:4px;background:var(--panel-alt);">
            导入元数据 JSON
            <input type="file" id="import-meta-file" accept=".json" style="display:none;" />
          </label>
        </div>
      </div>
    </div>
  `;
}

function renderPlayerHTML() {
  const p = state.player;
  return `
    <div class="player-bar" id="player-bar">
      <div class="player-info">
        <div class="player-track-title" title="${esc(p.track ? p.track.rel_path : "")}">
          ${p.track ? esc(p.track.rel_path) : "未播放音轨"}
        </div>
        <div class="player-work-sub">
          ${p.workno ? `${esc(p.workno)} · ${esc(p.workTitle)}` : "选择作品并点击音轨即可播放"}
        </div>
      </div>

      <div class="player-controls">
        <button data-action="player-prev" ${!p.track || p.queueIndex <= 0 ? "disabled" : ""}>⏮</button>
        <button class="primary" data-action="player-toggle" ${!p.track ? "disabled" : ""}>${p.playing ? "⏸" : "▶"}</button>
        <button data-action="player-next" ${!p.track || p.queueIndex + 1 >= p.queue.length ? "disabled" : ""}>⏭</button>
      </div>

      <div class="player-Timeline">
        <div class="player-seek-row">
          <input id="player-seek" type="range" min="0" max="${Math.max(1, Math.floor(p.duration || 1))}" value="${Math.floor(p.currentTime || 0)}" ${!p.track ? "disabled" : ""} />
          <span id="player-time" class="mono">${fmtSec(p.currentTime)} / ${fmtSec(p.duration)}</span>
        </div>
        <div id="player-sub" class="subtitle-line">${esc(p.activeSubtitle || "")}</div>
      </div>

      <div style="display:flex;align-items:center;gap:6px;">
        <select id="player-rate" title="倍速">
          ${[0.75, 1.0, 1.25, 1.5, 2.0]
            .map((r) => `<option value="${r}" ${p.playbackRate === r ? "selected" : ""}>${r}x</option>`)
            .join("")}
        </select>
        <button class="${p.incognito ? "active" : ""}" data-action="toggle-incognito" title="无痕收听：本次会话不上报播放进度与历史">
          ${p.incognito ? "🕵️ 无痕开启" : "无痕"}
        </button>
      </div>
    </div>
  `;
}

function renderPlayerOnly() {
  const existing = document.getElementById("player-bar");
  if (!existing) {
    render();
    return;
  }
  const tmp = document.createElement("div");
  tmp.innerHTML = renderPlayerHTML();
  existing.replaceWith(tmp.firstElementChild);
  bindPlayerInputs();
}

function renderLogin() {
  return `
    <div class="login-wrap">
      <form class="login-card" id="login-form">
        <h2 style="font-size:15px;margin-bottom:12px;">Otoha · 登录</h2>
        <div class="form-row"><label>用户名</label><input id="login-user" required style="width:180px;" /></div>
        <div class="form-row"><label>密码</label><input id="login-pwd" type="password" required style="width:180px;" /></div>
        ${
          state.mustResetPassword
            ? `<div style="color:#fde047;margin-bottom:8px;">检测到迁移账号，请直接输入新密码完成首次设密。</div>`
            : ""
        }
        <button class="primary" type="submit" style="width:100%;">进入媒体库</button>
      </form>
    </div>
  `;
}

function render() {
  const root = document.getElementById("app");
  if (!state.booted) {
    root.innerHTML = `<div style="padding:24px;color:var(--muted);">正在加载 Otoha...</div>`;
    return;
  }
  if (state.authEnabled && !state.authenticated) {
    root.innerHTML = renderLogin();
    document.getElementById("login-form")?.addEventListener("submit", async (e) => {
      e.preventDefault();
      const username = document.getElementById("login-user").value;
      const password = document.getElementById("login-pwd").value;
      try {
        await api("/api/auth/login", {
          method: "POST",
          body: { username, password, new_password: state.mustResetPassword ? password : "" },
        });
        state.mustResetPassword = false;
        await bootstrap();
      } catch (err) {
        if (err.data && err.data.must_reset_password) {
          state.mustResetPassword = true;
          render();
        } else {
          alert(err.message);
        }
      }
    });
    return;
  }

  root.innerHTML = `
    <div class="layout">
      <header class="topbar">
        <div class="brand">Otoha<span>音葉</span></div>
        <nav class="nav-tabs">
          <button class="${state.tab === "works" ? "active" : ""}" data-action="switch-tab" data-tab="works">音声库</button>
          <button class="${state.tab === "history" ? "active" : ""}" data-action="switch-tab" data-tab="history">收听历史</button>
          <button class="${state.tab === "settings" ? "active" : ""}" data-action="switch-tab" data-tab="settings">设置与存储</button>
        </nav>

        <form class="search-box" id="search-form">
          <input id="search-input" value="${esc(state.query)}" placeholder="检索标题 / 社团 / 声优 / 标签（支持 空格交集、-排除、&quot;短语&quot;）" />
          <button type="submit">检索</button>
        </form>

        <div class="topbar-right">
          ${state.authEnabled && state.user ? `<span style="color:var(--muted);">${esc(state.user.username)}</span><button data-action="logout">退出</button>` : ""}
        </div>
      </header>

      ${
        state.searchDegraded
          ? `<div class="banner-warn">
              <span>全文索引处于降级模式（已自动切换为 SQL LIKE 检索）：${esc(state.searchWarning)}</span>
              <button data-action="reindex-bleve">立即重建索引</button>
            </div>`
          : ""
      }

      ${
        state.task && (state.task.running || state.task.phase === "error")
          ? `<div class="task-bar">
              <span>[${esc(state.task.phase)}] ${esc(state.task.message)}</span>
              ${state.task.running ? `<button class="danger" data-action="cancel-task">中断</button>` : ""}
            </div>`
          : ""
      }

      <div class="workspace">
        <main class="main-pane">
          ${state.tab === "works" ? renderWorksTab() : state.tab === "history" ? renderHistoryTab() : renderSettingsTab()}
        </main>
        ${state.tab === "works" && state.detail ? renderDrawer() : ""}
      </div>

      ${renderPlayerHTML()}
    </div>
  `;

  bindEvents();
  bindPlayerInputs();
}

function bindPlayerInputs() {
  document.getElementById("player-seek")?.addEventListener("change", (e) => {
    audio.currentTime = Number(e.target.value);
  });
  document.getElementById("player-rate")?.addEventListener("change", (e) => {
    const r = Number(e.target.value) || 1.0;
    state.player.playbackRate = r;
    audio.playbackRate = r;
  });
}

function bindEvents() {
  document.getElementById("search-form")?.addEventListener("submit", (e) => {
    e.preventDefault();
    state.query = document.getElementById("search-input").value;
    state.page = 1;
    state.tab = "works";
    loadWorks();
  });

  document.getElementById("filter-age")?.addEventListener("change", (e) => {
    state.ageMode = e.target.value;
    state.page = 1;
    loadWorks();
    loadFacets();
    loadRecent();
  });
  document.getElementById("filter-progress")?.addEventListener("change", (e) => {
    state.progress = e.target.value;
    state.page = 1;
    loadWorks();
  });
  document.getElementById("filter-circle")?.addEventListener("change", (e) => {
    state.circle = e.target.value;
    state.page = 1;
    loadWorks();
  });
  document.getElementById("filter-va")?.addEventListener("change", (e) => {
    state.va = e.target.value;
    state.page = 1;
    loadWorks();
  });
  document.getElementById("filter-tag")?.addEventListener("change", (e) => {
    state.tag = e.target.value;
    state.page = 1;
    loadWorks();
  });
  document.getElementById("filter-sort")?.addEventListener("change", (e) => {
    state.sortBy = e.target.value;
    state.page = 1;
    loadWorks();
  });

  document.getElementById("import-meta-file")?.addEventListener("change", async (e) => {
    const f = e.target.files?.[0];
    if (!f) return;
    const txt = await f.text();
    const res = await api("/api/import/metadata", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: txt,
    });
    alert(`已导入 ${res.imported} 部作品元数据`);
    loadWorks();
  });
}

document.addEventListener("click", async (e) => {
  const target = e.target.closest("[data-action]");
  if (!target) return;
  const act = target.dataset.action;

  try {
    switch (act) {
      case "switch-tab": {
        state.tab = target.dataset.tab;
        if (state.tab === "works") {
          await Promise.all([loadWorks(), loadRecent()]);
        } else if (state.tab === "history") {
          await loadHistoryTab();
        } else if (state.tab === "settings") {
          await loadSettingsTab();
        }
        break;
      }
      case "set-view":
        state.viewMode = target.dataset.mode;
        render();
        break;
      case "toggle-sub-only":
        state.subtitleOnly = !state.subtitleOnly;
        state.page = 1;
        await loadWorks();
        break;
      case "reset-filters":
        state.query = "";
        state.circle = "";
        state.va = "";
        state.tag = "";
        state.progress = "";
        state.subtitleOnly = false;
        state.page = 1;
        await loadWorks();
        break;
      case "prev-page":
        if (state.page > 1) {
          state.page--;
          await loadWorks();
        }
        break;
      case "next-page":
        state.page++;
        await loadWorks();
        break;
      case "open-work":
        await openWorkDrawer(target.dataset.workno);
        break;
      case "close-drawer":
        state.selectedWorkno = "";
        state.detail = null;
        render();
        break;
      case "filter-circle":
        e.preventDefault();
        state.circle = target.dataset.val;
        state.page = 1;
        await loadWorks();
        break;
      case "filter-va":
        e.preventDefault();
        state.va = target.dataset.val;
        state.page = 1;
        await loadWorks();
        break;
      case "filter-tag":
        e.preventDefault();
        state.tag = target.dataset.val;
        state.page = 1;
        await loadWorks();
        break;
      case "toggle-folder": {
        const k = target.dataset.folderKey;
        state.collapsedFolders[k] = !state.collapsedFolders[k];
        render();
        break;
      }
      case "play-track":
        startPlayFromTree(Number(target.dataset.trackId), target.dataset.folderRel || "");
        break;
      case "player-toggle":
        if (!state.player.track) return;
        if (audio.paused) await audio.play();
        else audio.pause();
        break;
      case "player-prev":
        playQueueIndex(state.player.queueIndex - 1, 0);
        break;
      case "player-next":
        playQueueIndex(state.player.queueIndex + 1, 0);
        break;
      case "toggle-incognito":
        state.player.incognito = !state.player.incognito;
        renderPlayerOnly();
        break;
      case "save-state":
      case "change-state": {
        if (!state.detail) return;
        const progress = document.getElementById("state-progress").value;
        const rating = Number(document.getElementById("state-rating").value) || 0;
        const review = document.getElementById("state-review").value;
        await api(`/api/works/${encodeURIComponent(state.detail.workno)}/state`, {
          method: "POST",
          body: { progress, rating, review },
        });
        await openWorkDrawer(state.detail.workno);
        await loadWorks();
        break;
      }
      case "save-user-tags": {
        if (!state.detail) return;
        const raw = document.getElementById("user-tags-input").value;
        const tags = raw
          .split(/[,，]/)
          .map((s) => s.trim())
          .filter(Boolean);
        await api(`/api/works/${encodeURIComponent(state.detail.workno)}/tags`, {
          method: "POST",
          body: { tags },
        });
        await openWorkDrawer(state.detail.workno);
        await loadFacets();
        break;
      }
      case "toggle-edit-meta":
        state.editingMeta = !state.editingMeta;
        render();
        break;
      case "save-meta": {
        if (!state.detail) return;
        const splitCSV = (id) =>
          document
            .getElementById(id)
            .value.split(/[,，]/)
            .map((s) => s.trim())
            .filter(Boolean);
        await api(`/api/works/${encodeURIComponent(state.detail.workno)}/metadata`, {
          method: "PUT",
          body: {
            title: document.getElementById("meta-title").value,
            circle_name: document.getElementById("meta-circle").value,
            release_date: document.getElementById("meta-date").value,
            age_category: Number(document.getElementById("meta-age").value),
            vas: splitCSV("meta-vas"),
            tags: splitCSV("meta-tags"),
          },
        });
        state.editingMeta = false;
        await openWorkDrawer(state.detail.workno);
        await loadWorks();
        break;
      }
      case "scrape-work":
        if (!state.detail) return;
        await api(`/api/works/${encodeURIComponent(state.detail.workno)}/scrape`, { method: "POST" });
        await openWorkDrawer(state.detail.workno);
        await loadWorks();
        break;
      case "sync-work-tracks":
        if (!state.detail) return;
        await api(`/api/works/${encodeURIComponent(state.detail.workno)}/sync-tracks`, { method: "POST" });
        await openWorkDrawer(state.detail.workno);
        await loadWorks();
        break;
      case "jump-edition": {
        const wno = target.dataset.workno;
        if (!wno) break;
        try {
          await openWorkDrawer(wno);
        } catch (_) {
          await api(`/api/works/${encodeURIComponent(wno)}/scrape`, { method: "POST" });
          await openWorkDrawer(wno);
          await loadWorks();
        }
        break;
      }
      case "sync-asmrone-full":
        await api("/api/asmrone/sync", {
          method: "POST",
          body: { page_size: 100, order: "id", sort: "asc", max_pages: 0 },
        });
        break;
      case "sync-asmrone-latest":
        await api("/api/asmrone/sync", {
          method: "POST",
          body: { page_size: 100, order: "create_date", sort: "desc", max_pages: 2, reset_checkpoint: true },
        });
        break;
      case "resume-recent": {
        const entry = state.recent.find((r) => r.workno === target.dataset.workno);
        if (entry) await resumeFromHistory(entry);
        break;
      }
      case "resume-history": {
        const entry = state.historyItems.find((h) => h.id === Number(target.dataset.id));
        if (entry) {
          state.tab = "works";
          await resumeFromHistory(entry);
        }
        break;
      }
      case "delete-history":
        await api(`/api/history/${target.dataset.id}`, { method: "DELETE" });
        await loadHistoryTab();
        break;
      case "clear-history-days":
        await api(`/api/history?days=${target.dataset.days}`, { method: "DELETE" });
        await loadHistoryTab();
        break;
      case "clear-history-all":
        if (!confirm("确认清空全部收听历史并执行 SQLite VACUUM 彻底擦除磁盘残留？")) return;
        await api("/api/history", { method: "DELETE" });
        state.recent = [];
        await loadHistoryTab();
        break;
      case "add-storage": {
        const name = document.getElementById("new-st-name").value.trim();
        const root = document.getElementById("new-st-root").value.trim();
        await api("/api/storages", { method: "POST", body: { kind: "local", name, root } });
        await loadSettingsTab();
        break;
      }
      case "delete-storage":
        await api(`/api/storages/${target.dataset.id}`, { method: "DELETE" });
        await loadSettingsTab();
        break;
      case "scan-storage":
        await api("/api/scan", { method: "POST", body: { storage_id: Number(target.dataset.id), scrape_metadata: false } });
        break;
      case "scan-all":
        await api("/api/scan", { method: "POST", body: { storage_id: 0, scrape_metadata: target.dataset.scrape === "1" } });
        break;
      case "cancel-task":
        await api("/api/tasks/cancel", { method: "POST" });
        break;
      case "reindex-bleve": {
        const res = await api("/api/search/reindex", { method: "POST" });
        state.searchDegraded = false;
        state.searchWarning = "";
        alert(`索引重建完成：共索引 ${res.indexed} 部作品`);
        render();
        break;
      }
      case "save-settings": {
        const body = {
          default_hide_nsfw: document.getElementById("cfg-hide-nsfw").checked,
          history_enabled: document.getElementById("cfg-history-enabled").checked,
          history_max_days: Number(document.getElementById("cfg-hist-days").value),
          history_max_entries: Number(document.getElementById("cfg-hist-entries").value),
          heartbeat_interval_sec: Number(document.getElementById("cfg-hb-sec").value),
          scraper_locale: document.getElementById("cfg-locale").value,
          scraper_proxy: document.getElementById("cfg-proxy").value.trim(),
          scraper_interval_sec: Number(document.getElementById("cfg-interval").value),
          asmrone_enabled: document.getElementById("cfg-asmrone").checked,
          scan_max_depth: Number(document.getElementById("cfg-depth").value),
          webdav_enabled: document.getElementById("cfg-webdav").checked,
          auth_enabled: document.getElementById("cfg-auth").checked,
        };
        const saved = await api("/api/settings", { method: "PUT", body });
        state.settings = saved;
        state.heartbeatIntervalSec = saved.heartbeat_interval_sec;
        resetHeartbeatTimer();
        alert("设置已保存");
        await bootstrap();
        break;
      }
      case "create-user": {
        const username = document.getElementById("new-user-name").value.trim();
        const password = document.getElementById("new-user-pwd").value;
        const is_admin = document.getElementById("new-user-admin").checked;
        await api("/api/users", { method: "POST", body: { username, password, is_admin } });
        await loadSettingsTab();
        break;
      }
      case "logout":
        await api("/api/auth/logout", { method: "POST" });
        await bootstrap();
        break;
    }
  } catch (err) {
    alert(err.message);
  }
});

bootstrap();
