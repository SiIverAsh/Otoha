import {
  closePlaylistPicker,
  getFavorites,
  getPlaylist,
  handleLibraryAction,
  handleLibraryForm,
  isFavorite,
  isLibraryPersistent,
  refreshLibrary,
  renderPlaylists,
  setLibraryUser,
  showPlaylistPicker,
  syncPlaylistForms,
  syncPlaylistPicker,
  toggleFavorite,
} from "./library.js";
import { closeDropdown, enhanceDropdowns } from "./dropdowns.js";

function readPreference(key, fallback) {
  try {
    return localStorage.getItem(`otoha.${key}`) ?? fallback;
  } catch {
    return fallback;
  }
}

function savePreference(key, value) {
  try {
    localStorage.setItem(`otoha.${key}`, value);
  } catch {
    // Keep the preference in this session when browser storage is unavailable.
  }
}

const state = {
  tab: "works",
  browseQuery: "",
  theme: readPreference("theme", "dark") === "light" ? "light" : "dark",
  sidebarCollapsed: readPreference("sidebar", "expanded") === "collapsed",
  sidebarHidden: readPreference("sidebar-hidden", "false") === "true",
  playerCollapsed: readPreference("player-collapsed", "false") === "true",
  task: { running: false, phase: "idle", message: "" },

  selectedWorkno: "",
  detail: null,
  collapsedFolders: {},

  player: {
    workno: "",
    workTitle: "",
    workVas: [],
    track: null,
    queue: [],
    queueIndex: 0,
    folderScope: "",
    queueOpen: false,
    playing: false,
    currentTime: 0,
    duration: 0,
    playbackRate: 1.0,
    incognito: false,
    showSubtitles: true,
    cues: [],
    activeSubtitle: "",
    error: "",
    sleepMenuOpen: false,
    sleepDeadline: 0,
  },
  heartbeatIntervalSec: 30,
};

const audio = new Audio();
let hlsPlayer = null;
let pendingSeek = null;
let playerRequestId = 0;
let trackStopPromise = null;
let sleepTimer = null;
let lastAudibleVolume = audio.volume;
audio.preload = "metadata";

let lastPlayTickMs = 0;
let unsentListenedSec = 0;
let heartbeatTimer = null;
let sseSource = null;
let taskNotification = null;
let drawerMotion = null;
let drawerClosing = false;
let drawerRequestId = 0;
const DRAWER_VISIBLE = { transform: "translateX(0)", opacity: 1 };
const DRAWER_HIDDEN = { transform: "translateX(100%)", opacity: 0 };
const DRAWER_ENTER = { duration: 280, easing: "cubic-bezier(0.22, 1, 0.36, 1)" };
const DRAWER_EXIT = { duration: 340, easing: "cubic-bezier(0.42, 0, 0.28, 1)" };
const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
const notificationDuration = 4000;
const notificationExitDuration = 180;

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

function icon(name) {
  const symbol = name === "x" ? "close" : name;
  return `<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><use href="#icon-${esc(symbol)}"></use></svg>`;
}

function notify(message, { tone = "info", cancelLabel = "" } = {}) {
  const card = document.createElement("article");
  card.className = `notification notification--${tone}`;
  card.innerHTML = `
    <div class="notification-copy">
      <p class="notification-message" role="${tone === "error" ? "alert" : "status"}"></p>
    </div>
    <button type="button" class="notification-close" aria-label="关闭通知">${icon("close")}</button>
    ${cancelLabel ? `<div class="notification-actions">
      <button type="button" class="danger" data-action="cancel-task">${esc(cancelLabel)}</button>
    </div>` : ""}
  `;
  const messageNode = card.querySelector(".notification-message");
  const closeButton = card.querySelector(".notification-close");
  let timer;
  let isHovered = false;
  let isFocused = false;
  let isDismissed = false;

  function dismiss() {
    if (isDismissed) return;
    isDismissed = true;
    clearTimeout(timer);
    if (reducedMotion.matches) {
      card.remove();
      return;
    }
    card.inert = true;
    card.classList.add("notification--leaving");
    setTimeout(() => card.remove(), notificationExitDuration);
  }

  function scheduleDismissal() {
    clearTimeout(timer);
    if (!isDismissed && !isHovered && !isFocused) {
      timer = setTimeout(dismiss, notificationDuration);
    }
  }

  card.addEventListener("pointerenter", (event) => {
    if (event.pointerType === "touch") return;
    isHovered = true;
    scheduleDismissal();
  });
  card.addEventListener("pointerleave", (event) => {
    if (event.pointerType === "touch") return;
    isHovered = false;
    scheduleDismissal();
  });
  card.addEventListener("focusin", () => {
    isFocused = true;
    scheduleDismissal();
  });
  card.addEventListener("focusout", (event) => {
    if (card.contains(event.relatedTarget)) return;
    isFocused = false;
    scheduleDismissal();
  });
  closeButton.addEventListener("click", dismiss);

  document.getElementById("notifications").prepend(card);
  messageNode.textContent = message;
  scheduleDismissal();
  return {
    dismiss,
    updateMessage(nextMessage) {
      if (!isDismissed && nextMessage !== messageNode.textContent) {
        messageNode.textContent = nextMessage;
      }
    },
    focus() {
      closeButton.focus({ preventScroll: true });
    },
  };
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

function accumulateListened() {
  if (!state.player.playing || !lastPlayTickMs) return;
  const now = performance.now();
  const delta = Math.max(0, (now - lastPlayTickMs) / 1000) * (audio.playbackRate || 1);
  lastPlayTickMs = now;
  if (delta < 120) {
    unsentListenedSec += delta;
  }
}

function getTrackDuration() {
  const track = state.player.track;
  const mediaEnd = track?.end_seconds ?? audio.duration;
  return Number.isFinite(mediaEnd) ? Math.max(0, mediaEnd - (track?.start_seconds ?? 0)) : 0;
}

function getTrackPosition() {
  const track = state.player.track;
  const position = Math.max(0, (pendingSeek ?? audio.currentTime) - (track?.start_seconds ?? 0));
  return track?.end_seconds == null ? position : Math.min(position, getTrackDuration());
}

function seekTrack(position) {
  const { track, duration } = state.player;
  const trackPosition = track?.start_seconds != null && duration > 0 ? Math.min(position, duration) : position;
  const mediaPosition = Math.max(0, trackPosition) + (track?.start_seconds ?? 0);
  if (audio.readyState === 0) {
    pendingSeek = mediaPosition;
  } else {
    pendingSeek = null;
    audio.currentTime = mediaPosition;
  }
}

function resumeTrack() {
  const { track, duration } = state.player;
  if (track?.start_seconds != null && duration > 0 && getTrackPosition() >= duration) {
    seekTrack(0);
  }
  return audio.play();
}

function buildHeartbeatPayload() {
  if (!state.player.workno || !state.player.track) return null;
  accumulateListened();
  const delta = Number(unsentListenedSec.toFixed(2));
  unsentListenedSec = 0;
  return {
    workno: state.player.workno,
    track_id: state.player.track.id,
    position: Number(getTrackPosition().toFixed(2)),
    listened_delta: delta,
    track_duration: Number(getTrackDuration().toFixed(2)),
    incognito: state.player.incognito,
    queue: state.player.folderScope === null ? null : {
      track_ids: state.player.queue.map((t) => t.id),
      current_index: state.player.queueIndex,
      folder_scope: state.player.folderScope,
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
      const progress = document.getElementById("state-progress");
      if (progress && progress.value === progress.querySelector("option[selected]")?.value) {
        progress.value = res.progress;
        progress.querySelector("option[selected]")?.removeAttribute("selected");
        progress.selectedOptions[0]?.setAttribute("selected", "");
      }
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
  if (audio.paused) return;
  state.player.playing = true;
  lastPlayTickMs = performance.now();
  renderPlayerOnly();
});

audio.addEventListener("pause", () => {
  if (!state.player.playing || !audio.paused) return;
  accumulateListened();
  lastPlayTickMs = 0;
  state.player.playing = false;
  sendHeartbeat(false);
  renderPlayerOnly();
});

audio.addEventListener("seeked", () => {
  if (state.player.playing) lastPlayTickMs = performance.now();
  state.player.currentTime = getTrackPosition();
  updateActiveSubtitle();
  sendHeartbeat(false);
  syncPlayerDOMFast();
});

audio.addEventListener("loadedmetadata", () => {
  if (pendingSeek !== null) {
    audio.currentTime = pendingSeek;
    pendingSeek = null;
  }
  state.player.currentTime = getTrackPosition();
  state.player.duration = getTrackDuration();
  sendHeartbeat(false);
  syncPlayerDOMFast();
});

audio.addEventListener("timeupdate", async () => {
  state.player.currentTime = getTrackPosition();
  const duration = getTrackDuration();
  if (duration > 0) state.player.duration = duration;
  updateActiveSubtitle();
  syncPlayerDOMFast();
  const end = state.player.track?.end_seconds;
  if (!audio.paused && pendingSeek === null && end != null && audio.currentTime >= end) {
    await advanceAfterTrack();
  }
});

audio.addEventListener("ended", async () => {
  if (audio.ended) await advanceAfterTrack();
});

async function advanceAfterTrack() {
  if (trackStopPromise) return;
  if (state.player.queueIndex + 1 < state.player.queue.length) {
    await playQueueIndex(state.player.queueIndex + 1, 0);
  } else {
    const stopped = stopCurrentTrack();
    renderPlayerOnly();
    await stopped;
  }
}

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
  const elapsedEl = document.getElementById("player-elapsed");
  const durationEl = document.getElementById("player-duration");
  const subEl = document.getElementById("player-sub");
  if (seekEl && !seekEl.matches(":active")) {
    seekEl.max = String(Math.max(1, Math.floor(state.player.duration || 1)));
    seekEl.value = String(Math.floor(state.player.currentTime || 0));
    seekEl.style.setProperty("--played", `${100 * Number(seekEl.value) / Number(seekEl.max)}%`);
  }
  if (elapsedEl) elapsedEl.textContent = state.player.currentTime ? fmtSec(state.player.currentTime) : "00:00";
  if (durationEl) durationEl.textContent = fmtSec(state.player.duration);
  if (subEl) {
    const text = state.player.error || (state.player.showSubtitles ? state.player.activeSubtitle : "");
    if (subEl.textContent !== text) subEl.textContent = text;
    subEl.hidden = !text;
  }
}

async function loadSubtitles(trackId, requestId) {
  try {
    const res = await api(`/api/subtitle/${trackId}`);
    if (requestId === playerRequestId && res?.found && Array.isArray(res.cues)) {
      state.player.cues = res.cues;
      updateActiveSubtitle();
      syncPlayerDOMFast();
    }
  } catch (_) {}
}

function stopCurrentTrack() {
  if (trackStopPromise) return trackStopPromise;
  const heartbeat = sendHeartbeat(false);
  state.player.playing = false;
  lastPlayTickMs = 0;
  audio.pause();
  trackStopPromise = heartbeat.finally(() => {
    trackStopPromise = null;
  });
  return trackStopPromise;
}

function clearPlaybackSource() {
  if (hlsPlayer) {
    hlsPlayer.destroy();
    hlsPlayer = null;
  }
  audio.removeAttribute("src");
  audio.load();
}

function isHLSStream(track) {
  return track.streamIsHLS === true || /\.m3u8(?:$|[?#])/i.test(track.stream_url || "");
}

async function resetPlayer() {
  ++playerRequestId;
  await stopCurrentTrack();
  clearPlaybackSource();
  pendingSeek = null;
  closePlayerOverlay(false);
  setSleepTimer(0);
  Object.assign(state.player, {
    workno: "",
    workTitle: "",
    workVas: [],
    track: null,
    queue: [],
    queueIndex: 0,
    folderScope: "",
    queueOpen: false,
    currentTime: 0,
    duration: 0,
    incognito: false,
    cues: [],
    activeSubtitle: "",
    error: "",
  });
}

async function playQueueIndex(idx, resumeSec = null, nextQueue = null) {
  const p = state.player;
  const tracks = nextQueue?.tracks ?? p.queue;
  const tr = tracks[idx];
  if (!tr) return;
  const requestId = ++playerRequestId;
  await stopCurrentTrack();
  if (requestId !== playerRequestId) return;

  clearPlaybackSource();
  const duration = tr.duration ?? 0;
  let startAt = Math.max(0, resumeSec ?? tr.play_position ?? 0);
  if (tr.start_seconds != null && duration > 0 && startAt >= duration) startAt = 0;
  Object.assign(p, {
    workno: tr.workno,
    workTitle: tr.workTitle,
    workVas: tr.workVas,
    queue: tracks,
    queueIndex: idx,
    track: tr,
    currentTime: startAt,
    duration,
    error: "",
    cues: [],
    activeSubtitle: "",
  });
  if (nextQueue) p.folderScope = nextQueue.folderScope;
  const mediaStart = startAt + (tr.start_seconds ?? 0);
  pendingSeek = mediaStart > 0 ? mediaStart : null;
  const streamURL = `/api/stream/${tr.id}`;
  if (isHLSStream(tr) && !audio.canPlayType("application/vnd.apple.mpegurl")) {
    if (!window.Hls?.isSupported()) {
      p.error = "当前浏览器不支持 HLS 播放";
      render();
      return;
    }
    hlsPlayer = new window.Hls();
    hlsPlayer.loadSource(streamURL);
    hlsPlayer.attachMedia(audio);
    hlsPlayer.on(window.Hls.Events.ERROR, (_event, data) => {
      if (data.fatal && requestId === playerRequestId) {
        audio.pause();
        p.error = "播放源加载失败，请检查代理设置或重新采集该作品";
        renderPlayerOnly();
      }
    });
  } else {
    audio.src = streamURL;
  }
  audio.playbackRate = p.playbackRate;
  loadSubtitles(tr.id, requestId);
  render();

  try {
    await audio.play();
  } catch (err) {
    if (requestId !== playerRequestId || err.name === "AbortError" || err.name === "NotAllowedError" || p.error) return;
    p.error = "音轨加载失败，请重试";
    renderPlayerOnly();
  }
}

async function playSavedQueue(tracks, index) {
  // The history API restores queues scoped to one work.
  await playQueueIndex(index, 0, { tracks, folderScope: null });
}

function startApp() {
  applyTheme();
  const layout = document.querySelector(".layout");
  if (!layout) return;
  setLibraryUser({ id: Number(layout.dataset.userId) });
  state.heartbeatIntervalSec = Number(layout.dataset.heartbeatInterval);
  resetHeartbeatTimer();
  syncPage();
  connectTaskSSE();

  // Older versions stored the view preference only in localStorage.
  if (!document.cookie.split("; ").some((cookie) => cookie.startsWith("otoha_view="))) {
    const view = readPreference("view", "grid");
    if (view === "table") {
      document.cookie = "otoha_view=table; Path=/; Max-Age=31536000; SameSite=Lax";
      if (state.tab === "works" && !new URL(location.href).searchParams.has("view")) {
        refreshPage().catch(reportRequestError);
      }
    }
  }
}

function connectTaskSSE() {
  if (sseSource) sseSource.close();
  sseSource = new EventSource("/api/tasks/events");
  let isInitial = true;
  sseSource.onmessage = (ev) => {
    try {
      const snap = JSON.parse(ev.data);
      const previous = state.task;
      state.task = snap;
      updateTaskNotification(snap, previous, isInitial);
      isInitial = false;
      syncTaskIndicator();
      if (state.tab === "settings" &&
          (JSON.stringify(snap.asmr_sync) !== JSON.stringify(previous.asmr_sync) ||
           JSON.stringify(snap.asmr18_preview) !== JSON.stringify(previous.asmr18_preview))) {
        window.htmx.ajax("GET", "/ui/settings/task", {
          source: "#asmrone-checkpoint",
          target: "#asmrone-checkpoint",
          swap: "innerHTML",
        }).catch((error) => { if (error) reportRequestError(error); });
      }
      if (previous.running && !snap.running && state.tab === "works") {
        refreshPage().catch(reportRequestError);
      }
    } catch (_) {}
  };
}

function showTaskNotification(snapshot) {
  taskNotification?.dismiss();
  const tone = snapshot.phase === "error" ? "error" : snapshot.phase === "done" ? "success" : "info";
  const cancelLabel = snapshot.running
    ? (snapshot.phase === "syncing_asmrone" ? "暂停并保存断点" : "中断任务")
    : "";
  taskNotification = notify(snapshot.message, { tone, cancelLabel });
}

function updateTaskNotification(snapshot, previous, isInitial) {
  const sameTask = snapshot.started_at === previous.started_at;
  if (snapshot.running) {
    if (previous.running && sameTask) {
      taskNotification?.updateMessage(snapshot.message);
    } else {
      showTaskNotification(snapshot);
    }
    return;
  }

  const isTerminal = ["done", "cancelled", "error"].includes(snapshot.phase);
  const isNewStatus = previous.running || !sameTask ||
    snapshot.phase !== previous.phase || snapshot.message !== previous.message;
  const shouldAnnounce = !isInitial || previous.running || snapshot.phase === "error";
  if (isTerminal && isNewStatus && shouldAnnounce) {
    showTaskNotification(snapshot);
  }
}

function reportRequestError(error) {
  notify(error?.message || "请求未完成", { tone: "error" });
}

function currentPageURL() {
  return location.pathname + location.search;
}

async function requestPage(url, { navigate = false, keepDrawer = false } = {}) {
  await window.htmx.ajax("GET", url, {
    source: "#page-content",
    target: "#page-content",
    swap: "innerHTML",
    headers: {
      "X-Otoha-Navigate": navigate ? "1" : "0",
      "X-Otoha-Keep-Drawer": keepDrawer ? "1" : "0",
    },
  }).catch((error) => {
    if (error) throw error;
    // HTMX rejects without a value when a newer navigation aborts the request.
  });
}

function refreshPage() {
  return requestPage(currentPageURL());
}

function refreshWorks() {
  if (state.tab === "works") return refreshPage();
}

let swappedDrawerRequest = 0;
let drawerResponseStatus;

async function openWorkDrawer(workno, { edit = false, preserveInput = false } = {}) {
  const requestId = ++drawerRequestId;
  drawerResponseStatus = undefined;
  const url = `/ui/works/${encodeURIComponent(workno)}${edit ? "?edit=1" : ""}`;
  await window.htmx.ajax("GET", url, {
    source: "#drawer-region",
    target: "#drawer-region",
    swap: "innerHTML",
    headers: {
      "X-Otoha-Drawer": String(requestId),
      "X-Otoha-Preserve-Input": preserveInput ? "1" : "0",
    },
  }).catch((error) => {
    if (requestId === drawerRequestId) throw error ?? new Error("无法连接到作品详情");
  });
  if (requestId !== drawerRequestId) return null;
  if (swappedDrawerRequest !== requestId) {
    const error = new Error("无法打开作品详情");
    error.status = drawerResponseStatus;
    throw error;
  }
  return state.detail;
}

function isCurrentWorkDrawer(workno, requestId) {
  return !drawerClosing && drawerRequestId === requestId && state.detail?.workno === workno;
}

function finishClosingDrawer() {
  drawerClosing = false;
  state.selectedWorkno = "";
  state.detail = null;
  document.querySelector(".drawer")?.remove();
  document.querySelector(".work-card.selected, .works-table tr.selected")?.classList.remove("selected");
}

function closeWorkDrawer() {
  ++drawerRequestId;
  if (!state.detail || drawerClosing) return;
  const drawer = document.querySelector(".drawer");
  if (!drawer || reducedMotion.matches) {
    cancelDrawerMotion();
    finishClosingDrawer();
    return;
  }
  const from = drawerMotion ? readDrawerPose(drawer) : DRAWER_VISIBLE;
  drawerClosing = true;
  startDrawerMotion(drawer, from);
}

function readDrawerPose(drawer) {
  const style = getComputedStyle(drawer);
  return { transform: style.transform, opacity: style.opacity };
}

function cancelDrawerMotion() {
  drawerMotion?.animation.cancel();
  drawerMotion = null;
}

function startDrawerMotion(drawer, from, elapsed = 0) {
  cancelDrawerMotion();
  const closing = drawerClosing;
  drawer.inert = closing;
  const to = closing ? DRAWER_HIDDEN : DRAWER_VISIBLE;
  const timing = closing ? DRAWER_EXIT : DRAWER_ENTER;
  const animation = drawer.animate([from, to], { ...timing, fill: "both" });
  animation.currentTime = elapsed;
  drawerMotion = { animation, from, closing };
  animation.finished.then(() => {
    if (drawerMotion?.animation !== animation) return;
    cancelDrawerMotion();
    if (closing) finishClosingDrawer();
  }, () => {}); // Replaced renders cancel the old animation after saving its time.
}

function captureDrawerState(drawer) {
  if (!drawer) return null;
  return {
    workno: drawer.dataset.workno,
    scrollTop: drawer.scrollTop,
    editorOpen: isDrawerEditorOpen(),
    motion: drawerMotion && {
      from: drawerMotion.from,
      elapsed: drawerMotion.animation.currentTime ?? 0,
      interruptedPose: drawerMotion.closing !== drawerClosing ? readDrawerPose(drawer) : null,
    },
  };
}

function syncDrawerMotion(previous) {
  const drawer = document.querySelector(".drawer");
  if (!drawer) return;
  if (previous?.workno === drawer.dataset.workno) {
    drawer.scrollTop = previous.scrollTop;
  }
  if (reducedMotion.matches) {
    if (drawerClosing) finishClosingDrawer();
    return;
  }
  if (previous?.motion) {
    const { from, elapsed, interruptedPose } = previous.motion;
    if (interruptedPose) startDrawerMotion(drawer, interruptedPose);
    else startDrawerMotion(drawer, from, elapsed);
  } else if (!previous) {
    startDrawerMotion(drawer, DRAWER_HIDDEN);
  }
}

const settingsDisclosureMotion = new WeakMap();

function toggleSettingsDisclosure(summary) {
  const section = summary.parentElement;
  const content = summary.nextElementSibling;
  const opening = !section.open || section.hasAttribute("data-closing");
  const previous = settingsDisclosureMotion.get(section);
  const from = section.getBoundingClientRect().height;
  settingsDisclosureMotion.delete(section);
  previous?.cancel();

  const finish = () => {
    section.open = opening;
    section.removeAttribute("data-disclosing");
    section.removeAttribute("data-closing");
    summary.removeAttribute("aria-expanded");
    content.inert = false;
  };
  section.open = opening;
  const to = section.getBoundingClientRect().height;
  if (reducedMotion.matches || from === to) {
    finish();
    return;
  }

  section.open = true;
  section.setAttribute("data-disclosing", "");
  section.toggleAttribute("data-closing", !opening);
  summary.setAttribute("aria-expanded", String(opening));
  content.inert = !opening;
  const animation = section.animate(
    [{ height: `${from}px` }, { height: `${to}px` }],
    { duration: 240, easing: DRAWER_ENTER.easing, fill: "both" },
  );
  settingsDisclosureMotion.set(section, animation);
  const settle = () => {
    if (settingsDisclosureMotion.get(section) !== animation) return;
    settingsDisclosureMotion.delete(section);
    finish();
    animation.cancel();
  };
  animation.finished.then(settle, settle);
}

reducedMotion.addEventListener("change", () => {
  if (!reducedMotion.matches) return;
  drawerMotion?.animation.finish();
  for (const section of document.querySelectorAll(".settings-page details[data-disclosing]")) {
    settingsDisclosureMotion.get(section)?.finish();
  }
});

document.addEventListener("keydown", (e) => {
  if (e.defaultPrevented || e.isComposing) return;
  if (e.key === "Escape") {
    if (closePlaylistPicker()) {
      e.preventDefault();
      render();
      return;
    }
    if (document.getElementById("playlist-picker")?.open) {
      e.preventDefault();
      return;
    }
    if (closeDropdown(true)) {
      e.preventDefault();
      return;
    }
    if (closePlayerOverlay()) {
      e.preventDefault();
      return;
    }
    if (state.detail) {
      e.preventDefault();
      closeWorkDrawer();
    }
  }
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
    const search = document.getElementById("search-input");
    if (search) {
      e.preventDefault();
      search.focus();
    }
  }
});

async function resumeFromHistory(entry) {
  const det = await openWorkDrawer(entry.workno);
  if (!det) return;

  const tracks = det.tracks || [];
  const trackById = new Map(tracks.map((track) => [track.id, track]));
  const segmentsBySource = new Map();
  for (const track of tracks) {
    if (track.source_track_id == null) continue;
    if (!segmentsBySource.has(track.source_track_id)) segmentsBySource.set(track.source_track_id, []);
    segmentsBySource.get(track.source_track_id).push(track);
  }
  const legacySegment = segmentsBySource.get(entry.track_id)?.find((track) =>
    entry.position >= track.start_seconds && (track.end_seconds == null || entry.position < track.end_seconds));
  const trackId = legacySegment?.id ?? entry.track_id;
  const resumePosition = entry.position - (legacySegment?.start_seconds ?? 0);
  let queue = [];
  let folderScope = "";

  if (entry.queue_ctx) {
    try {
      const parsed = JSON.parse(entry.queue_ctx);
      if (Array.isArray(parsed.track_ids) && parsed.track_ids.length > 0) {
        queue = parsed.track_ids.flatMap((id) => {
          const track = trackById.get(id);
          return track ? [track] : segmentsBySource.get(id) ?? [];
        });
        folderScope = parsed.folder_scope || "";
      }
    } catch (_) {}
  }
  let idx = queue.findIndex((track) => track.id === trackId);
  if (idx < 0) {
    queue = tracks;
    folderScope = "";
    idx = queue.findIndex((track) => track.id === trackId);
  }
  if (queue.length > 0) {
    await playQueueIndex(Math.max(0, idx), idx < 0 ? 0 : resumePosition, {
      tracks: withWorkContext(queue, det),
      folderScope,
    });
  } else {
    render();
  }
}

function withWorkContext(tracks, work) {
  return tracks.map((track) => ({
    ...track,
    workno: work.workno,
    workTitle: work.title,
    workVas: work.vas || [],
  }));
}

async function startPlayFromTree(trackId, parentFolderRel) {
  if (!state.detail || !state.detail.tracks) return;
  const all = state.detail.tracks;
  const scoped = all.filter((t) => {
    const slash = t.rel_path.replace(/\\/g, "/");
    const lastSlash = slash.lastIndexOf("/");
    const dir = lastSlash >= 0 ? slash.slice(0, lastSlash) : "";
    return dir === parentFolderRel;
  });
  const queue = scoped.length > 0 ? scoped : all;
  const idx = queue.findIndex((t) => t.id === trackId);
  if (idx < 0) return;
  await playQueueIndex(idx, queue[idx].play_position > 2 ? queue[idx].play_position : 0, {
    tracks: withWorkContext(queue, state.detail),
    folderScope: parentFolderRel,
  });
}

function renderPageHeading(title, count = null, actions = "") {
  return `
    <div class="page-heading">
      <div><h1>${title}</h1>${count === null ? "" : `<span class="page-count">${count.toLocaleString("zh-CN")} 部作品</span>`}</div>
      ${actions ? `<div class="page-actions">${actions}</div>` : ""}
    </div>`;
}

function renderEmptyState(glyph, title, hint, action = "") {
  return `<div class="empty-state">${icon(glyph)}<h2>${title}</h2><p>${hint}</p>${action}</div>`;
}

function renderFavoriteButton(workno, className = "icon-button") {
  const saved = isFavorite(workno);
  const label = saved ? "取消收藏" : "收藏作品";
  return `<button class="${className}${saved ? " active" : ""}" data-action="toggle-favorite"
    data-workno="${esc(workno)}" aria-label="${label}" aria-pressed="${saved}" title="${label}">${icon("heart")}</button>`;
}

const DLSITE_ORIGIN = "https://www.dlsite.com";
const DLSITE_WORKNO = /^[RBV]J(?:\d{6}|\d{8})$/;
const DLSITE_PRODUCT_PATH = /^\/[a-z][a-z-]*\/work\/(?:=|%3d)\/product_id\/([RBV]J(?:\d{6}|\d{8}))(?:\.html)?\/?$/i;

function dlsiteWorkURL(work) {
  const candidates = [work.workno, work.original_workno]
    .filter((value) => typeof value === "string")
    .map((value) => value.trim().toUpperCase());
  const workno = candidates.find((value) => DLSITE_WORKNO.test(value));
  if (typeof work.source_url === "string" && URL.canParse(work.source_url)) {
    const source = new URL(work.source_url);
    const product = source.pathname.match(DLSITE_PRODUCT_PATH)?.[1].toUpperCase();
    if (source.protocol === "https:" && !source.username && !source.password && !source.port &&
        (source.hostname === "www.dlsite.com" || source.hostname === "dlsite.com") &&
        product && (!workno || product === workno)) return source.href;
  }

  if (workno) {
    const general = work.age_category === 1;
    const sectionByPrefix = { RJ: general ? "home" : "maniax", BJ: "books", VJ: general ? "soft" : "pro" };
    return `${DLSITE_ORIGIN}/${sectionByPrefix[workno.slice(0, 2)]}/work/=/product_id/${workno}.html`;
  }
  const title = typeof work.title === "string" ? work.title.trim() : "";
  const keyword = title || candidates.find(Boolean);
  return keyword ? `${DLSITE_ORIGIN}/home/fsr/=/keyword/${encodeURIComponent(keyword)}` : DLSITE_ORIGIN;
}

function renderDLsiteLink(work, className, text = "") {
  const label = `在 DLsite 查看 ${work.title || work.workno || "作品"}`;
  return `<a class="dlsite-link ${className}" href="${esc(dlsiteWorkURL(work))}" target="_blank"
    rel="noopener noreferrer" aria-label="${esc(label)}" title="${esc(label)}">${text ? `<span>${esc(text)}</span>` : ""}${icon("external")}</a>`;
}

function renderWorkGrid(works) {
  return `<div class="card-grid">
    ${works.map((work) => {
      const workno = esc(work.workno);
      const creators = [...new Set([work.circle_name, ...(work.vas || [])].filter(Boolean))].join(" · ");
      const tags = (work.tags || []).slice(0, 2);
      return `<article class="work-card${state.selectedWorkno === work.workno ? " selected" : ""}">
        <button class="work-card-open" data-action="open-work" data-workno="${workno}" aria-label="查看 ${esc(work.title)}">
          <span class="work-cover">
            ${icon("headphones")}
            <img src="/api/cover/${encodeURIComponent(work.workno)}" loading="lazy" decoding="async" alt="" />
            <span class="cover-overlay"><span class="cover-play">${icon("chevron-right")}</span></span>
          </span>
          <span class="work-card-body">
            <span class="work-card-kicker"><span class="mono">${workno}</span>${ageBadgeHTML(work.age_category)}
              ${work.has_subtitle ? `<span class="badge badge-sfw">字幕</span>` : ""}
            </span>
            <span class="work-card-title" title="${esc(work.title)}">${esc(work.title)}</span>
            ${creators ? `<span class="work-card-meta" title="${esc(creators)}">${esc(creators)}</span>` : ""}
            ${tags.length ? `<span class="work-card-tags">${tags.map((tag) => `<span class="tag-pill">${esc(tag.name)}</span>`).join("")}</span>` : ""}
          </span>
        </button>
        ${renderFavoriteButton(work.workno, "icon-button card-favorite")}
        ${renderDLsiteLink(work, "card-dlsite")}
      </article>`;
    }).join("")}
  </div>`;
}

function renderFavoritesTab() {
  const favorites = getFavorites();
  return `${renderPageHeading("我的收藏", favorites.length)}
    ${favorites.length
      ? renderWorkGrid(favorites)
      : renderEmptyState("heart", "还没有收藏", "点击作品卡片或播放器上的心形图标即可收藏。",
          '<a class="primary button-link" href="/" hx-boost="true" hx-target="#page-content" hx-swap="innerHTML">浏览作品</a>')}`;
}

function renderPlayerButton(action, glyph, label, attrs = "", className = "") {
  return `<button type="button" class="icon-button ${className}" data-action="${action}" aria-label="${label}" title="${label}" ${attrs}>${icon(glyph)}</button>`;
}

function renderQueueDrawer() {
  const p = state.player;
  const chapters = p.track?.chapters || [];
  const visibility = `aria-hidden="${!p.queueOpen}"${p.queueOpen ? "" : " inert"}`;
  return `
    <button type="button" id="queue-scrim" class="queue-scrim" tabindex="-1" aria-label="关闭播放队列"
      data-action="toggle-queue" data-overlay="scrim" ${visibility}></button>
    <aside class="queue-drawer" id="player-queue" aria-labelledby="queue-title" ${visibility}>
      <header class="queue-header">
        <div><h2 id="queue-title">播放队列</h2><span>${p.queue.length} 首音轨</span></div>
        ${renderPlayerButton("toggle-queue", "close", "关闭队列", 'id="queue-close"')}
      </header>
      <div class="queue-items">
        ${p.queue.length ? p.queue.map((track, index) => `
          <button type="button" class="queue-track ${index === p.queueIndex ? "active" : ""}" data-action="player-queue-track" data-index="${index}" ${index === p.queueIndex ? 'aria-current="true"' : ""}>
            <span class="queue-track-index">${index === p.queueIndex && p.playing ? icon("play") : index + 1}</span>
            <span class="queue-track-meta">
              <strong>${esc(track.title || track.rel_path)}</strong>
              <span>${esc(track.workTitle)}</span>
            </span>
            <span class="queue-duration">${fmtSec(track.duration)}</span>
          </button>
        `).join("") : '<p class="queue-empty">队列为空</p>'}
      </div>
      <div class="queue-preferences">
        ${chapters.length ? `
          <select id="player-chapter" aria-label="跳转章节">
            <option value="">跳转章节</option>
            ${chapters.map(({ start, title }) => `<option value="${Number(start)}">${esc(title)} · ${start === 0 ? "00:00" : fmtSec(start)}</option>`).join("")}
          </select>
        ` : ""}
        <select id="player-rate" aria-label="播放速度">
          ${[0.75, 1, 1.25, 1.5, 2].map((rate) => `<option value="${rate}" ${p.playbackRate === rate ? "selected" : ""}>${rate}×</option>`).join("")}
        </select>
        <button type="button" class="${p.incognito ? "active" : ""}" data-action="toggle-incognito" aria-pressed="${p.incognito}" title="无痕收听时不记录历史或进度">无痕收听</button>
      </div>
    </aside>
  `;
}

function renderSleepChoices() {
  const deadline = state.player.sleepDeadline;
  const time = deadline ? new Date(deadline).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
  }) : "";
  return `
    <strong>${deadline ? `将在 ${time} 暂停` : "睡眠定时"}</strong>
    ${[5, 15, 30, 60].map((minutes) => `<button type="button" data-action="set-sleep-timer" data-minutes="${minutes}">${minutes} 分钟</button>`).join("")}
    ${deadline ? '<button type="button" data-action="set-sleep-timer" data-minutes="0">取消定时</button>' : ""}
  `;
}

function setSleepMenuOpen(open, restoreFocus = true) {
  if (state.player.sleepMenuOpen === open) return;
  state.player.sleepMenuOpen = open;
  const menu = document.getElementById("player-timer-menu");
  const trigger = document.getElementById("player-timer-trigger");
  trigger?.setAttribute("aria-expanded", String(open));
  if (!open && restoreFocus) trigger?.focus({ preventScroll: true });
  if (!menu) return;
  if (open) menu.innerHTML = renderSleepChoices();
  menu.inert = !open;
  menu.setAttribute("aria-hidden", String(!open));
}

function setSleepTimer(minutes) {
  const p = state.player;
  clearTimeout(sleepTimer);
  sleepTimer = null;
  p.sleepDeadline = 0;
  if (minutes === 0) return;
  const delay = minutes * 60_000;
  p.sleepDeadline = Date.now() + delay;
  sleepTimer = setTimeout(() => {
    sleepTimer = null;
    p.sleepDeadline = 0;
    audio.pause();
    renderPlayerOnly();
  }, delay);
}

function setQueueOpen(open, restoreFocus = true) {
  if (state.player.queueOpen === open) return;
  const drawer = document.getElementById("player-queue");
  const scrim = document.getElementById("queue-scrim");
  const trigger = document.querySelector('#player-bar [data-action="toggle-queue"]');
  if (!open && drawer?.querySelector('.dropdown-trigger[aria-expanded="true"]')) {
    closeDropdown(false, { immediate: true });
  }

  state.player.queueOpen = open;
  trigger?.setAttribute("aria-expanded", String(open));
  if (!open && restoreFocus) trigger?.focus({ preventScroll: true });
  for (const surface of [drawer, scrim]) {
    if (!surface) continue;
    surface.inert = !open;
    surface.setAttribute("aria-hidden", String(!open));
  }
  if (open) document.getElementById("queue-close")?.focus({ preventScroll: true });
}

function closePlayerOverlay(restoreFocus = true) {
  if (state.player.queueOpen) {
    setQueueOpen(false, restoreFocus);
    return true;
  }
  if (!state.player.sleepMenuOpen) return false;
  setSleepMenuOpen(false, restoreFocus);
  return true;
}

function togglePlayerCollapse() {
  const collapsed = !state.playerCollapsed;
  state.playerCollapsed = collapsed;
  savePreference("player-collapsed", String(collapsed));
  if (collapsed) closePlayerOverlay(false);

  const bar = document.getElementById("player-bar");
  const dock = document.getElementById("player-dock");
  bar.closest(".layout").classList.toggle("player-collapsed", collapsed);
  const visible = collapsed ? dock : bar;
  const hidden = collapsed ? bar : dock;
  visible.inert = false;
  visible.setAttribute("aria-hidden", "false");
  const toggle = document.getElementById(collapsed ? "player-expand" : "player-collapse");
  toggle.focus({ preventScroll: true });
  hidden.inert = true;
  hidden.setAttribute("aria-hidden", "true");
}

function renderPlayerHTML() {
  const p = state.player;
  const collapsed = state.playerCollapsed;
  const title = p.track?.title || p.track?.rel_path || "尚未播放";
  const dockTitle = p.track ? p.workTitle || p.workno || title : title;
  const cover = p.workno
    ? `<img src="/api/cover/${encodeURIComponent(p.workno)}" alt="" />`
    : icon("headphones");
  const credit = p.workVas.join(" / ") || p.workTitle;
  const subtitle = p.error || (p.showSubtitles ? p.activeSubtitle : "");
  const favorite = Boolean(p.workno && isFavorite(p.workno));
  const muted = audio.muted || audio.volume === 0;
  const maxSeek = Math.max(1, Math.floor(p.duration));
  const seekValue = Math.min(maxSeek, Math.floor(p.currentTime));
  return `
    <div class="player-shell">
      <footer class="player-bar" id="player-bar" aria-label="播放器" aria-hidden="${collapsed}" ${collapsed ? "inert" : ""}>
        <div class="player-info">
          <div class="player-cover">
            ${cover}
          </div>
          <div class="player-track-meta">
            <div class="player-track-title" title="${esc(title)}">${esc(title)}</div>
            ${credit ? `<div class="player-work-sub" title="${esc(credit)}">${esc(credit)}</div>` : ""}
          </div>
          ${renderPlayerButton("toggle-favorite", "heart", favorite ? "移出收藏" : "收藏作品",
            `data-workno="${esc(p.workno)}" aria-pressed="${favorite}" ${p.workno ? "" : "disabled"}`)}
        </div>

        <div class="player-center">
          <div class="player-controls">
            ${renderPlayerButton("player-prev", "previous", "上一首", p.queueIndex <= 0 ? "disabled" : "")}
            ${renderPlayerButton("player-toggle", p.playing ? "pause" : "play", p.playing ? "暂停" : "播放", p.track ? "" : "disabled", "player-play")}
            ${renderPlayerButton("player-next", "next", "下一首", p.queueIndex + 1 >= p.queue.length ? "disabled" : "")}
          </div>
          <div class="player-seek-row">
            <span id="player-elapsed" class="player-elapsed mono">${p.currentTime ? fmtSec(p.currentTime) : "00:00"}</span>
            <input id="player-seek" type="range" min="0" max="${maxSeek}" value="${seekValue}" aria-label="播放进度" style="--played:${100 * seekValue / maxSeek}%" ${p.track ? "" : "disabled"} />
            <span id="player-duration" class="player-duration mono">${fmtSec(p.duration)}</span>
          </div>
        </div>

        <div class="player-tools">
          ${renderPlayerButton("toggle-player-subtitles", "captions", "字幕", `aria-pressed="${p.showSubtitles}"`)}
          ${renderPlayerButton("toggle-sleep-menu", "timer", "睡眠定时", `id="player-timer-trigger" aria-expanded="${p.sleepMenuOpen}" aria-controls="player-timer-menu"`, p.sleepDeadline ? "active" : "")}
          ${renderPlayerButton("toggle-queue", "queue", "播放队列", `aria-expanded="${p.queueOpen}" aria-controls="player-queue"`)}
          <div class="player-volume">
            ${renderPlayerButton("toggle-mute", muted ? "volume-off" : "volume", muted ? "取消静音" : "静音", `aria-pressed="${muted}"`)}
            <input id="player-volume" type="range" min="0" max="100" step="1" value="${Math.round(audio.volume * 100)}" aria-label="音量" style="--played:${muted ? 0 : audio.volume * 100}%" />
          </div>
          ${renderPlayerButton("toggle-player-collapse", "chevron-down", "收起播放栏",
            'id="player-collapse" aria-controls="player-bar" aria-expanded="true"', "player-collapse")}
        </div>
        <div class="player-timer-menu" id="player-timer-menu" aria-label="睡眠定时"
          aria-hidden="${!p.sleepMenuOpen}" ${p.sleepMenuOpen ? "" : "inert"}>
          ${renderSleepChoices()}
        </div>
      </footer>
      <aside id="player-dock" class="player-dock" aria-label="迷你播放器"
        aria-hidden="${!collapsed}" ${collapsed ? "" : "inert"}>
        <div class="player-cover">${cover}</div>
        <div class="player-dock-meta">
          <div class="player-dock-title" title="${esc(dockTitle)}">${esc(dockTitle)}</div>
          ${p.track && title !== dockTitle ? `<div class="player-dock-track" title="${esc(title)}">${esc(title)}</div>` : ""}
        </div>
        ${renderPlayerButton("player-toggle", p.playing ? "pause" : "play", p.playing ? "暂停" : "播放",
          `id="player-dock-toggle" ${p.track ? "" : "disabled"}`, "player-dock-toggle")}
        ${renderPlayerButton("toggle-player-collapse", "chevron-up", "展开播放栏",
          'id="player-expand" aria-controls="player-bar" aria-expanded="false"', "player-dock-expand")}
      </aside>
      <div id="player-sub" class="subtitle-line" role="status" ${subtitle ? "" : "hidden"}>${esc(subtitle)}</div>
      ${renderQueueDrawer()}
    </div>
  `;
}

function renderPlayerOnly() {
  const existing = document.querySelector(".player-shell");
  if (!existing) return;
  if (existing.querySelector('.dropdown-trigger[aria-expanded="true"]')) {
    closeDropdown(true, { immediate: true });
  }
  const focusedButton = captureButtonFocus(existing);
  const focusedField = existing.contains(document.activeElement) ? document.activeElement.id : "";
  const queueScrollTop = existing.querySelector(".queue-items")?.scrollTop;
  const template = document.createElement("template");
  template.innerHTML = renderPlayerHTML();
  const player = template.content.firstElementChild;
  // Keep animated surfaces mounted while refreshing their controls and content.
  for (const selector of [".player-info", ".player-center", ".player-tools", "#player-sub"]) {
    existing.querySelector(selector).replaceWith(player.querySelector(selector));
  }
  for (const selector of ["#player-dock", "#player-queue"]) {
    existing.querySelector(selector).replaceChildren(...player.querySelector(selector).childNodes);
  }
  if (state.player.sleepMenuOpen) {
    existing.querySelector("#player-timer-menu").replaceChildren(...player.querySelector("#player-timer-menu").childNodes);
  }
  bindPlayerInputs();
  enhanceDropdowns(existing, icon("chevron-down"));
  const queue = existing.querySelector(".queue-items");
  if (queue && queueScrollTop !== undefined) queue.scrollTop = queueScrollTop;
  restoreButtonFocus(existing, focusedButton);
  if (focusedField) document.getElementById(focusedField)?.focus({ preventScroll: true });
}

function captureButtonFocus(scope) {
  const button = document.activeElement;
  if (!scope?.contains(button) || !button.matches("button:focus-visible")) return null;
  return { data: { ...button.dataset }, form: button.form?.id || "" };
}

function restoreButtonFocus(scope, focusedButton) {
  if (!scope || !focusedButton) return;
  const button = [...scope.querySelectorAll("button:enabled")].find((candidate) =>
    !candidate.closest("[inert]") && (candidate.form?.id || "") === focusedButton.form &&
    Object.keys(candidate.dataset).length === Object.keys(focusedButton.data).length &&
    Object.entries(focusedButton.data).every(([key, value]) => candidate.dataset[key] === value)
  );
  button?.focus({ preventScroll: true });
}

function sidebarCollapseLabel() {
  return state.sidebarCollapsed ? "展开导航文字" : "折叠为图标栏";
}

function sidebarVisibilityLabel() {
  return state.sidebarHidden ? "展开侧边栏" : "收起侧边栏";
}

function updateSidebarToggle(button, expanded, label) {
  button.setAttribute("aria-expanded", String(expanded));
  button.setAttribute("aria-label", label);
  button.title = label;
}

function syncSidebarPresentation() {
  const sidebar = document.getElementById("library-sidebar");
  if (!sidebar) return;
  const visibilityToggle = document.querySelector(".sidebar-visibility-toggle");
  if (state.sidebarHidden && sidebar.contains(document.activeElement)) {
    visibilityToggle.focus({ preventScroll: true });
  }
  sidebar.closest(".layout").classList.toggle("sidebar-collapsed", state.sidebarCollapsed);
  sidebar.setAttribute("aria-hidden", String(state.sidebarHidden));
  sidebar.toggleAttribute("inert", state.sidebarHidden);
  updateSidebarToggle(sidebar.querySelector(".sidebar-toggle"), !state.sidebarCollapsed, sidebarCollapseLabel());
  updateSidebarToggle(visibilityToggle, !state.sidebarHidden, sidebarVisibilityLabel());
}

function captureDrawerInputs(drawer) {
  if (!drawer || drawer.dataset.workno !== state.detail?.workno || drawerClosing) return [];
  const inputs = [];
  const editor = document.getElementById("drawer-metadata-editor");
  for (const field of drawer.querySelectorAll("input[id], select[id], textarea[id]")) {
    if (editor?.inert && editor.contains(field)) continue;
    const initial = field.tagName === "SELECT"
      ? (field.querySelector("option[selected]") ?? field.options[0]).value
      : field.defaultValue;
    const focused = document.activeElement === field;
    if (field.value === initial && !focused) continue;
    inputs.push({
      id: field.id,
      value: field.value,
      initial,
      focused,
      selectionStart: field.selectionStart,
      selectionEnd: field.selectionEnd,
    });
  }
  return inputs;
}

function restoreDrawerInputs(inputs) {
  for (const input of inputs) {
    const field = document.getElementById(input.id);
    if (!field) continue;
    // Keep the draft if the redraw did not supply a new saved value.
    if (field.value === input.initial) field.value = input.value;
    if (input.focused) {
      field.focus({ preventScroll: true });
      if (Number.isInteger(input.selectionStart)) {
        field.setSelectionRange(input.selectionStart, input.selectionEnd);
      }
    }
  }
}

function applyTheme() {
  document.documentElement.dataset.theme = state.theme;
  const toggle = document.querySelector(".theme-toggle");
  if (!toggle) return;
  const isDark = state.theme === "dark";
  const label = isDark ? "切换到浅色主题" : "切换到深色主题";
  toggle.setAttribute("aria-label", label);
  toggle.title = label;
  toggle.querySelector("use").setAttribute("href", isDark ? "#icon-sun" : "#icon-moon");
}

function syncTaskIndicator() {
  const status = document.querySelector('[data-action="show-task-status"]');
  if (status) status.hidden = !state.task.running && state.task.phase !== "error";
}

function syncPage() {
  const page = document.querySelector("[data-page-tab]");
  if (page) syncNavigation(page);
  for (const time of document.querySelectorAll("[data-history-time]")) {
    time.textContent = fmtTimeAgo(Number(time.dataset.historyTime));
  }
  render();
  enhanceDropdowns(document.getElementById("app"), icon("chevron-down"));
}

function syncNavigation(page) {
  state.tab = page.dataset.pageTab;
  state.browseQuery = page.dataset.browseQuery;
  document.querySelector(".main-pane").dataset.page = state.tab;
  if (state.tab === "works" && new URLSearchParams(location.search).has("view")) {
    const view = page.dataset.view;
    savePreference("view", view);
    document.cookie = `otoha_view=${view}; Path=/; Max-Age=31536000; SameSite=Lax`;
  }
  for (const link of document.querySelectorAll("[data-nav-path]")) {
    link.href = link.dataset.navPath === "/"
      ? page.dataset.homeUrl
      : link.dataset.navPath + page.dataset.browseQuery;
    if (link.dataset.navTab === state.tab) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }

}

let libraryHTML;
let libraryRegion;
let libraryExit;

function renderBrowserLibrary() {
  const region = document.getElementById("browser-library");
  if (!region) {
    libraryRegion = null;
    libraryHTML = null;
    libraryExit = null;
    return;
  }
  const warning = isLibraryPersistent() ? "" : '<p class="banner-warn" role="status">浏览器存储不可用。收藏和播放列表仅保留在本次会话中。</p>';
  const html = warning + (state.tab === "favorites"
    ? renderFavoritesTab()
    : renderPageHeading("播放列表") + renderPlaylists(esc, icon));
  if (region === libraryRegion) {
    syncPlaylistForms(region);
    if (html === libraryHTML) return;
    // A save can change the list as it closes its form. Keep the exit in place.
    const exit = Array.from(region.querySelectorAll('.playlist-form-disclosure[aria-hidden="true"]'))
      .flatMap((panel) => panel.getAnimations())
      .find((motion) => motion.transitionProperty === "grid-template-rows" && motion.playState !== "finished");
    if (exit) {
      if (exit !== libraryExit) {
        libraryExit = exit;
        exit.finished.then(renderBrowserLibrary, renderBrowserLibrary);
      }
      return;
    }
  }
  const focus = region.contains(document.activeElement) ? document.activeElement.id : "";
  region.innerHTML = html;
  window.htmx.process(region);
  libraryRegion = region;
  libraryHTML = html;
  libraryExit = null;
  syncPlaylistForms(region);
  if (focus) document.getElementById(focus)?.focus({ preventScroll: true });
}

function syncFavorites() {
  for (const button of document.querySelectorAll('[data-action="toggle-favorite"]')) {
    const saved = isFavorite(button.dataset.workno);
    const label = saved ? "取消收藏" : "收藏作品";
    button.classList.toggle("active", saved);
    button.setAttribute("aria-pressed", String(saved));
    button.setAttribute("aria-label", label);
    button.title = label;
  }
}

function syncWorkSelection() {
  for (const card of document.querySelectorAll(".work-card, .works-table tbody tr")) {
    const workno = card.dataset.workCard ?? card.querySelector('[data-action="open-work"]')?.dataset.workno;
    card.classList.toggle("selected", Boolean(workno && workno === state.selectedWorkno));
  }
  for (const track of document.querySelectorAll(".drawer button[data-action='play-track']")) {
    const playing = Number(track.dataset.trackId) === state.player.track?.id;
    track.closest(".tree-track").classList.toggle("playing", playing);
    const marker = track.querySelector("[data-playing-marker]");
    if (marker) marker.hidden = !playing;
  }
}

function syncFolders() {
  for (const label of document.querySelectorAll(".drawer [data-folder-key]")) {
    const collapsed = Boolean(state.collapsedFolders[label.dataset.folderKey]);
    label.setAttribute("aria-expanded", String(!collapsed));
    const content = label.nextElementSibling;
    content.inert = collapsed;
    content.setAttribute("aria-hidden", String(collapsed));
  }
}

function isDrawerEditorOpen() {
  return document.getElementById("drawer-metadata-editor")?.inert === false;
}

function render() {
  const player = document.getElementById("player-region");
  if (!player) return;
  if (player.firstElementChild) renderPlayerOnly();
  else {
    player.closest(".layout").classList.toggle("player-collapsed", state.playerCollapsed);
    player.innerHTML = renderPlayerHTML();
    bindPlayerInputs();
    enhanceDropdowns(player, icon("chevron-down"));
  }
  applyTheme();
  syncSidebarPresentation();
  syncTaskIndicator();
  renderBrowserLibrary();
  syncPlaylistPicker(document.getElementById("playlist-picker-region"), esc, icon, render);
  syncFavorites();
  syncWorkSelection();
  syncFolders();
}

let drawerTransition;
let pageTransition;
let asmr18Draft;

function rememberASMR18Draft() {
  const urls = document.getElementById("asmr18-urls");
  if (!urls) return;
  const category = document.getElementById("asmr18-category");
  const workers = document.getElementById("asmr18-workers");
  const interval = document.getElementById("asmr18-interval");
  const probe = document.getElementById("asmr18-probe");
  const refresh = document.getElementById("asmr18-refresh");
  asmr18Draft = {
    urls: urls.value,
    category: category.value,
    workers: workers.value,
    interval: interval.value,
    probe: probe.checked,
    refresh: refresh.checked,
  };
}

function restoreASMR18Draft() {
  const urls = document.getElementById("asmr18-urls");
  if (!urls || !asmr18Draft) return;
  urls.value = asmr18Draft.urls;
  document.getElementById("asmr18-category").value = asmr18Draft.category;
  document.getElementById("asmr18-workers").value = asmr18Draft.workers;
  document.getElementById("asmr18-interval").value = asmr18Draft.interval;
  document.getElementById("asmr18-probe").checked = asmr18Draft.probe;
  document.getElementById("asmr18-refresh").checked = asmr18Draft.refresh;
}

function asmr18Options() {
  const workers = document.getElementById("asmr18-workers");
  const interval = document.getElementById("asmr18-interval");
  for (const field of [workers, interval]) {
    if (!field.checkValidity()) {
      field.reportValidity();
      throw new Error("请检查 ASMR18 采集参数");
    }
  }
  return {
    workers: workers.valueAsNumber || Number(workers.defaultValue),
    interval_sec: Number.isNaN(interval.valueAsNumber) ? Number(interval.defaultValue) : interval.valueAsNumber,
    probe_duration: document.getElementById("asmr18-probe").checked,
  };
}

function refreshSettings() {
  if (state.tab === "settings") return refreshPage();
}

function beforeDrawerSwap(detail) {
  const requestId = Number(detail.requestConfig.headers["X-Otoha-Drawer"]);
  if (requestId !== drawerRequestId) {
    detail.shouldSwap = false;
    return;
  }
  drawerResponseStatus = detail.xhr.status;
  if (!detail.shouldSwap) return;
  const drawer = detail.target.querySelector(".drawer");
  drawerClosing = false;
  drawerTransition = {
    requestId,
    pose: captureDrawerState(drawer),
    inputs: detail.requestConfig.headers["X-Otoha-Preserve-Input"] === "1" ? captureDrawerInputs(drawer) : [],
    focusedButton: drawer ? captureButtonFocus(drawer) : null,
  };
  cancelDrawerMotion();
  closeDropdown(true, { immediate: true });
}

function afterDrawerSwap(target) {
  const drawer = target.querySelector(".drawer");
  if (!drawer || !drawerTransition) return;
  const { requestId, pose, inputs, focusedButton } = drawerTransition;
  drawerTransition = null;
  swappedDrawerRequest = requestId;
  state.detail = JSON.parse(drawer.dataset.detail);
  state.selectedWorkno = state.detail.workno;
  syncDrawerMotion(pose);
  const editor = document.getElementById("drawer-metadata-editor");
  const hadOpenEditor = pose?.workno === drawer.dataset.workno && pose.editorOpen;
  if (editor && !editor.inert && !hadOpenEditor && !reducedMotion.matches) {
    editor.setAttribute("aria-hidden", "true");
    editor.getBoundingClientRect();
    editor.setAttribute("aria-hidden", "false");
  }
  if (pose?.workno === drawer.dataset.workno) {
    restoreDrawerInputs(inputs);
    restoreButtonFocus(drawer, focusedButton);
  }
  syncFavorites();
  syncWorkSelection();
  syncFolders();
  enhanceDropdowns(drawer, icon("chevron-down"));
}

document.addEventListener("htmx:beforeSwap", ({ detail }) => {
  if (detail.target.classList.contains("login-wrap") && detail.xhr.status === 422) {
    detail.shouldSwap = true;
    detail.isError = false;
    return;
  }
  if (detail.target.id === "drawer-region") {
    beforeDrawerSwap(detail);
    return;
  }
  if (detail.target.id !== "page-content" || !detail.shouldSwap) return;
  const { elt, headers, verb } = detail.requestConfig;
  const navigate = headers["X-Otoha-Navigate"] === "1" ||
    (elt !== detail.target && verb === "get");
  const search = document.getElementById("search-input");
  const resetsSearch = elt?.closest("[data-nav-path], #search-form, [data-clear-work-filters]");
  const preserveSearch = !navigate || (!resetsSearch && search?.value !== search?.defaultValue);
  pageTransition = {
    navigate,
    keepDrawer: headers["X-Otoha-Keep-Drawer"] === "1",
    home: elt?.closest('[data-nav-path="/"]'),
    searchDraft: preserveSearch ? search?.value : undefined,
    openSections: [...detail.target.querySelectorAll(".settings-page details[id][open]:not([data-closing])")]
      .map(({ id }) => id),
    previousTab: state.tab,
  };
  rememberASMR18Draft();
  closeDropdown(true, { immediate: true });
});

document.addEventListener("htmx:afterSwap", ({ detail }) => {
  const { target } = detail;
  if (target.id === "drawer-region") {
    afterDrawerSwap(target);
    return;
  }
  if (target.id !== "page-content") return;

  const transition = pageTransition;
  pageTransition = null;
  const newTab = target.querySelector("[data-page-tab]")?.dataset.pageTab ?? state.tab;
  if (transition?.navigate) {
    document.querySelector(".main-pane").scrollTop = 0;
    if (!transition.keepDrawer && (transition.home || transition.previousTab !== newTab)) closeWorkDrawer();
    if (transition.previousTab !== newTab) closePlaylistPicker();
  }
  restoreASMR18Draft();
  for (const id of transition?.openSections ?? []) {
    const section = document.getElementById(id);
    if (section) section.open = true;
  }
  const search = document.getElementById("search-input");
  if (search && transition?.searchDraft !== undefined && search.defaultValue !== transition.searchDraft) {
    search.value = transition.searchDraft;
  }
  syncPage();
});

document.addEventListener("htmx:historyRestore", () => {
  closeWorkDrawer();
  closePlaylistPicker();
  restoreASMR18Draft();
  syncPage();
});

document.addEventListener("htmx:responseError", ({ detail }) => {
  if (detail.target.id === "drawer-region") return;
  reportRequestError(new Error(`请求失败（HTTP ${detail.xhr.status}）`));
});

function bindPlayerInputs() {
  document.getElementById("player-chapter")?.addEventListener("change", (e) => {
    if (e.target.value !== "") {
      seekTrack(Number(e.target.value));
      e.target.value = "";
    }
  });
  document.getElementById("player-seek")?.addEventListener("input", (e) => {
    const seek = e.target;
    seek.style.setProperty("--played", `${100 * Number(seek.value) / Number(seek.max)}%`);
    document.getElementById("player-elapsed").textContent = Number(seek.value) ? fmtSec(Number(seek.value)) : "00:00";
  });
  document.getElementById("player-seek")?.addEventListener("change", (e) => {
    seekTrack(Number(e.target.value));
  });
  document.getElementById("player-rate")?.addEventListener("change", (e) => {
    accumulateListened();
    state.player.playbackRate = Number(e.target.value);
    audio.playbackRate = state.player.playbackRate;
  });
  document.getElementById("player-volume")?.addEventListener("input", (e) => {
    audio.volume = Number(e.target.value) / 100;
    audio.muted = false;
    if (audio.volume > 0) lastAudibleVolume = audio.volume;
    syncVolumeControl();
  });
}

function syncVolumeControl() {
  const volumeButton = document.querySelector('[data-action="toggle-mute"]');
  const volumeSlider = document.getElementById("player-volume");
  const muted = audio.muted || audio.volume === 0;
  const label = muted ? "取消静音" : "静音";
  if (volumeButton.getAttribute("aria-label") !== label) {
    volumeButton.setAttribute("aria-label", label);
    volumeButton.setAttribute("aria-pressed", String(muted));
    volumeButton.title = label;
    volumeButton.innerHTML = icon(muted ? "volume-off" : "volume");
  }
  volumeSlider.value = String(Math.round(audio.volume * 100));
  volumeSlider.style.setProperty("--played", `${muted ? 0 : audio.volume * 100}%`);
}

document.addEventListener("change", async (event) => {
  if (event.target.id.startsWith("asmr18-")) rememberASMR18Draft();
  if (event.target.id !== "import-meta-file") return;
  const file = event.target.files?.[0];
  if (!file) return;
  try {
    const result = await api("/api/import/metadata", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: await file.text(),
    });
    notify(`已导入 ${result.imported} 部作品元数据`, { tone: "success" });
    await refreshPage();
  } catch (error) {
    reportRequestError(error);
  } finally {
    event.target.value = "";
  }
});

function focusLibraryForm() {
  document.querySelector("[data-library-autofocus]")?.focus({ preventScroll: true });
}

document.addEventListener("submit", (event) => {
  const form = event.target;
  if (!form.matches("[data-library-form]")) return;
  event.preventDefault();
  if (handleLibraryForm(form)) {
    render();
    focusLibraryForm();
  }
});

window.addEventListener("storage", (event) => {
  if (refreshLibrary(event.key)) render();
});

function isWorkDrawerBackground(target) {
  return Boolean(target.closest(".workspace, .sidebar")) &&
    !target.closest(".drawer, .dropdown, .search-box, button, a, input, select, textarea, label, summary, [contenteditable]");
}

document.addEventListener("click", async (e) => {
  if (e.target.closest("a.dlsite-link")) return;
  const summary = e.target.closest(".settings-page details > summary");
  if (summary) {
    e.preventDefault();
    toggleSettingsDisclosure(summary);
    return;
  }
  const target = e.target.closest("[data-action]");
  if (!target) {
    if (state.detail && isWorkDrawerBackground(e.target)) closeWorkDrawer();
    return;
  }
  const act = target.dataset.action;

  try {
    if (handleLibraryAction(target)) {
      render();
      focusLibraryForm();
      return;
    }
    switch (act) {
      case "toggle-sidebar":
        state.sidebarCollapsed = !state.sidebarCollapsed;
        savePreference("sidebar", state.sidebarCollapsed ? "collapsed" : "expanded");
        syncSidebarPresentation();
        break;
      case "toggle-sidebar-visibility":
        state.sidebarHidden = !state.sidebarHidden;
        savePreference("sidebar-hidden", String(state.sidebarHidden));
        syncSidebarPresentation();
        break;
      case "toggle-theme":
        state.theme = state.theme === "dark" ? "light" : "dark";
        savePreference("theme", state.theme);
        render();
        break;
      case "open-work":
        await openWorkDrawer(target.dataset.workno);
        break;
      case "close-drawer":
        closeWorkDrawer();
        break;
      case "filter-circle":
      case "filter-va":
      case "filter-tag": {
        e.preventDefault();
        const facet = act.slice("filter-".length);
        const keepDrawer = Boolean(target.closest(".drawer"));
        const query = state.tab === "works"
          ? new URLSearchParams(location.search)
          : new URLSearchParams(state.browseQuery);
        if (query.get(facet) === target.dataset.val) query.delete(facet);
        else query.set(facet, target.dataset.val);
        query.delete("page");
        await requestPage(`/?${query}`, { navigate: true, keepDrawer });
        break;
      }
      case "toggle-favorite": {
        const workno = target.dataset.workno;
        const work = target.dataset.work
          ? JSON.parse(target.dataset.work)
          : getFavorites().find((favorite) => favorite.workno === workno);
        if (!work) return;
        toggleFavorite(work);
        render();
        break;
      }
      case "playlist-add-track": {
        const work = state.detail;
        const track = work?.tracks?.find(({ id }) => id === Number(target.dataset.trackId));
        if (track) {
          showPlaylistPicker({ ...track, workno: work.workno, workTitle: work.title, workVas: work.vas });
          render();
        }
        break;
      }
      case "playlist-play": {
        const playlist = getPlaylist(target.dataset.playlistId);
        if (playlist?.tracks.length) {
          const index = target.dataset.trackId
            ? playlist.tracks.findIndex(({ id }) => id === Number(target.dataset.trackId))
            : Number(target.dataset.index);
          if (index >= 0) await playSavedQueue(playlist.tracks, index);
        }
        break;
      }
      case "toggle-folder": {
        const k = target.dataset.folderKey;
        state.collapsedFolders[k] = !state.collapsedFolders[k];
        render();
        break;
      }
      case "play-track":
        await startPlayFromTree(Number(target.dataset.trackId), target.dataset.folderRel || "");
        break;
      case "player-toggle":
        if (!state.player.track) return;
        if (state.player.error) await playQueueIndex(state.player.queueIndex, state.player.currentTime);
        else if (audio.paused) await resumeTrack();
        else audio.pause();
        break;
      case "toggle-player-collapse":
        togglePlayerCollapse();
        break;
      case "player-prev":
        await playQueueIndex(state.player.queueIndex - 1, 0);
        break;
      case "player-next":
        await playQueueIndex(state.player.queueIndex + 1, 0);
        break;
      case "player-queue-track": {
        const index = Number(target.dataset.index);
        if (index !== state.player.queueIndex) await playQueueIndex(index, 0);
        else if (audio.paused) {
          if (state.player.error) await playQueueIndex(index, state.player.currentTime);
          else await resumeTrack();
        }
        break;
      }
      case "toggle-queue":
        if (state.player.sleepMenuOpen) closePlayerOverlay(false);
        setQueueOpen(!state.player.queueOpen);
        break;
      case "toggle-player-subtitles":
        state.player.showSubtitles = !state.player.showSubtitles;
        renderPlayerOnly();
        break;
      case "toggle-sleep-menu":
        setQueueOpen(false, false);
        setSleepMenuOpen(!state.player.sleepMenuOpen);
        break;
      case "set-sleep-timer":
        setSleepMenuOpen(false);
        setSleepTimer(Number(target.dataset.minutes));
        renderPlayerOnly();
        break;
      case "toggle-mute": {
        const muted = audio.muted || audio.volume === 0;
        if (muted && audio.volume === 0) audio.volume = lastAudibleVolume;
        audio.muted = !muted;
        syncVolumeControl();
        break;
      }
      case "toggle-incognito": {
        const heartbeat = sendHeartbeat(false);
        state.player.incognito = !state.player.incognito;
        renderPlayerOnly();
        await heartbeat;
        break;
      }
      case "save-state": {
        if (!state.detail) return;
        const { workno } = state.detail;
        const requestId = drawerRequestId;
        const progress = document.getElementById("state-progress").value;
        const rating = Number(document.getElementById("state-rating").value) || 0;
        const review = document.getElementById("state-review").value;
        await api(`/api/works/${encodeURIComponent(workno)}/state`, {
          method: "POST",
          body: { progress, rating, review },
        });
        if (isCurrentWorkDrawer(workno, requestId)) {
          await openWorkDrawer(workno, { edit: isDrawerEditorOpen(), preserveInput: true });
        }
        await refreshWorks();
        break;
      }
      case "save-user-tags": {
        if (!state.detail) return;
        const { workno } = state.detail;
        const requestId = drawerRequestId;
        const raw = document.getElementById("user-tags-input").value;
        const tags = raw
          .split(/[,，]/)
          .map((s) => s.trim())
          .filter(Boolean);
        await api(`/api/works/${encodeURIComponent(workno)}/tags`, {
          method: "POST",
          body: { tags },
        });
        if (isCurrentWorkDrawer(workno, requestId)) {
          await openWorkDrawer(workno, { edit: isDrawerEditorOpen(), preserveInput: true });
        }
        await refreshWorks();
        break;
      }
      case "toggle-edit-meta":
        if (state.detail) {
          const editor = document.getElementById("drawer-metadata-editor");
          if (editor.inert) {
            await openWorkDrawer(state.detail.workno, { edit: true, preserveInput: true });
          } else {
            editor.inert = true;
            editor.setAttribute("aria-hidden", "true");
            target.setAttribute("aria-expanded", "false");
            target.textContent = "编辑";
          }
        }
        break;
      case "save-meta": {
        if (!state.detail) return;
        const { workno } = state.detail;
        const requestId = drawerRequestId;
        const splitCSV = (id) =>
          document
            .getElementById(id)
            .value.split(/[,，]/)
            .map((s) => s.trim())
            .filter(Boolean);
        await api(`/api/works/${encodeURIComponent(workno)}/metadata`, {
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
        if (isCurrentWorkDrawer(workno, requestId)) await openWorkDrawer(workno);
        await refreshWorks();
        break;
      }
      case "scrape-work":
      case "sync-work-tracks": {
        if (!state.detail) return;
        const { workno } = state.detail;
        const requestId = drawerRequestId;
        const operation = act === "scrape-work" ? "scrape" : "sync-tracks";
        await api(`/api/works/${encodeURIComponent(workno)}/${operation}`, { method: "POST" });
        if (isCurrentWorkDrawer(workno, requestId)) await openWorkDrawer(workno);
        await refreshWorks();
        break;
      }
      case "jump-edition": {
        const workno = target.dataset.workno;
        const requestId = drawerRequestId + 1;
        try {
          await openWorkDrawer(workno);
        } catch (error) {
          if (error.status !== 404) throw error;
          await api(`/api/works/${encodeURIComponent(workno)}/scrape`, { method: "POST" });
          if (drawerRequestId === requestId) await openWorkDrawer(workno);
          await refreshWorks();
        }
        break;
      }
      case "asmr18-preview":
      case "asmr18-import": {
        const urls = document.getElementById("asmr18-urls").value.split(/\s+/).filter(Boolean);
        if (!urls.length) throw new Error("请先填写 ASMR18 作品页面地址");
        await api("/api/asmr18/sync", {
          method: "POST",
          body: { ...asmr18Options(), urls, max_works: 0, dry_run: act === "asmr18-preview", refresh: true },
        });
        break;
      }
      case "asmr18-latest":
        await api("/api/asmr18/sync", {
          method: "POST",
          body: {
            ...asmr18Options(),
            category: document.getElementById("asmr18-category").value,
            max_pages: 1,
            max_works: 24,
            refresh: document.getElementById("asmr18-refresh").checked,
          },
        });
        break;
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
      case "resume-recent":
      case "resume-history": {
        const entry = JSON.parse(target.dataset.entry);
        if (act === "resume-history") {
          await requestPage("/" + state.browseQuery, { navigate: true, keepDrawer: true });
        }
        await resumeFromHistory(entry);
        break;
      }
      case "add-storage": {
        const name = document.getElementById("new-st-name").value.trim();
        const root = document.getElementById("new-st-root").value.trim();
        await api("/api/storages", { method: "POST", body: { kind: "local", name, root } });
        await refreshSettings();
        break;
      }
      case "delete-storage":
        await api(`/api/storages/${target.dataset.id}`, { method: "DELETE" });
        await refreshSettings();
        break;
      case "scan-storage":
        await api("/api/scan", { method: "POST", body: { storage_id: Number(target.dataset.id), scrape_metadata: false } });
        break;
      case "scan-all":
        await api("/api/scan", { method: "POST", body: { storage_id: 0, scrape_metadata: target.dataset.scrape === "1" } });
        break;
      case "show-task-status":
        showTaskNotification(state.task);
        if (target.matches(":focus-visible")) taskNotification.focus();
        break;
      case "cancel-task":
        await api("/api/tasks/cancel", { method: "POST" });
        break;
      case "reindex-bleve": {
        const res = await api("/api/search/reindex", { method: "POST" });
        notify(`索引重建完成，共索引 ${res.indexed} 部作品`, { tone: "success" });
        await refreshWorks();
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
        if (saved.auth_enabled !== Boolean(document.querySelector('[data-action="logout"]'))) {
          location.reload();
          break;
        }
        state.heartbeatIntervalSec = saved.heartbeat_interval_sec;
        resetHeartbeatTimer();
        notify("设置已保存", { tone: "success" });
        await refreshSettings();
        break;
      }
      case "create-user": {
        const username = document.getElementById("new-user-name").value.trim();
        const password = document.getElementById("new-user-pwd").value;
        const is_admin = document.getElementById("new-user-admin").checked;
        await api("/api/users", { method: "POST", body: { username, password, is_admin } });
        await refreshSettings();
        break;
      }
      case "logout":
        await resetPlayer();
        await api("/api/auth/logout", { method: "POST" });
        sseSource?.close();
        location.assign("/");
        break;
    }
  } catch (err) {
    notify(err.message, { tone: "error" });
  }
});

startApp();
