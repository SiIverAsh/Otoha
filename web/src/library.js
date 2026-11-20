const LIBRARY_KEY_PREFIX = "otoha.library.v1.";
const PLAYLIST_NAME_LIMIT = 80;
const memoryByAccount = new Map();
const volatileAccounts = new Set();

let accountKey = `${LIBRARY_KEY_PREFIX}guest`;
let library = { favorites: [], playlists: [] };
let selectedPlaylistId = null;
let playlistForm = null;
let pickerTrack = null;
let pickerHTML = "";

function normalizeWork(work) {
  return {
    workno: work.workno,
    title: work.title,
    circle_name: work.circle_name,
    age_category: work.age_category,
    asmrone_id: work.asmrone_id,
    source_type: work.source_type,
    source_url: typeof work.source_url === "string" ? work.source_url : undefined,
    original_workno: typeof work.original_workno === "string" ? work.original_workno : undefined,
    vas: Array.isArray(work.vas) ? work.vas.filter((name) => typeof name === "string") : [],
    tags: Array.isArray(work.tags)
      ? work.tags.filter((tag) => typeof tag?.name === "string").map(({ name, source }) => ({ name, source }))
      : [],
    has_subtitle: work.has_subtitle,
    release_date: work.release_date,
    created_at: work.created_at,
    dl_count: work.dl_count,
    rating: work.rating,
    progress: work.progress,
    track_count: work.track_count,
  };
}

function normalizeTrack(track) {
  return {
    id: track.id,
    workno: track.workno,
    workTitle: track.workTitle,
    workVas: Array.isArray(track.workVas) ? track.workVas.filter((name) => typeof name === "string") : [],
    title: track.title,
    rel_path: typeof track.rel_path === "string" ? track.rel_path : "",
    duration: track.duration,
    start_seconds: Number.isFinite(track.start_seconds) ? track.start_seconds : undefined,
    end_seconds: Number.isFinite(track.end_seconds) ? track.end_seconds : undefined,
    chapters: Array.isArray(track.chapters)
      ? track.chapters.filter((chapter) => Number.isFinite(chapter?.start)).map(({ title, start }) => ({ title, start }))
      : [],
    has_subtitle: track.has_subtitle,
    streamIsHLS: Boolean(track.streamIsHLS) || /\.m3u8(?:$|\?)/i.test(track.stream_url || ""),
  };
}

function restoreLibrary(serialized) {
  if (!serialized) return { favorites: [], playlists: [] };
  let parsed;
  try {
    parsed = JSON.parse(serialized);
  } catch (error) {
    console.warn("无法读取本地音频库数据", error.name);
    return { favorites: [], playlists: [] };
  }
  const favorites = Array.isArray(parsed?.favorites) ? parsed.favorites : [];
  const playlists = Array.isArray(parsed?.playlists) ? parsed.playlists : [];
  return {
    favorites: favorites.filter((work) => typeof work?.workno === "string").map(normalizeWork),
    playlists: playlists.filter((list) => typeof list?.id === "string" && typeof list.name === "string").map((list) => ({
      id: list.id,
      name: list.name,
      tracks: Array.isArray(list.tracks)
        ? list.tracks.filter((track) => Number.isSafeInteger(track?.id) && typeof track.workno === "string").map(normalizeTrack)
        : [],
    })),
  };
}

function readLibrary() {
  if (!volatileAccounts.has(accountKey)) {
    try {
      const saved = restoreLibrary(localStorage.getItem(accountKey));
      memoryByAccount.set(accountKey, saved);
      return saved;
    } catch (error) {
      volatileAccounts.add(accountKey);
      console.warn("浏览器拒绝读取本地音频库", error.name);
    }
  }
  return memoryByAccount.get(accountKey) ?? { favorites: [], playlists: [] };
}

function changeLibrary(update) {
  library = readLibrary();
  update(library);
  memoryByAccount.set(accountKey, library);
  if (!volatileAccounts.has(accountKey)) {
    try {
      localStorage.setItem(accountKey, JSON.stringify(library));
    } catch (error) {
      volatileAccounts.add(accountKey);
      console.warn("浏览器拒绝保存本地音频库", error.name);
    }
  }
}

/** Loads the current account's browser-local collection. */
export function setLibraryUser(user) {
  const key = `${LIBRARY_KEY_PREFIX}${user?.id ?? "guest"}`;
  if (key !== accountKey) {
    selectedPlaylistId = null;
    playlistForm = null;
    closePlaylistPicker();
  }
  accountKey = key;
  library = readLibrary();
}

/** Reloads the collection after a storage event from another tab. */
export function refreshLibrary(key) {
  if ((key !== null && key !== accountKey) || !isLibraryPersistent()) return false;
  library = readLibrary();
  return true;
}

/** Indicates whether changes are being saved to browser storage. */
export function isLibraryPersistent() {
  return !volatileAccounts.has(accountKey);
}

/** Returns work summaries in order of most recently favorited. */
export function getFavorites() {
  return library.favorites.slice();
}

/** Checks whether a work is saved in the current user's favorites. */
export function isFavorite(workno) {
  return library.favorites.some((work) => work.workno === workno);
}

/** Adds a work to favorites or removes an already saved work. */
export function toggleFavorite(work) {
  const shouldAdd = !isFavorite(work.workno);
  changeLibrary((collection) => {
    collection.favorites = collection.favorites.filter((item) => item.workno !== work.workno);
    if (shouldAdd) collection.favorites.unshift(normalizeWork(work));
  });
  return shouldAdd;
}

function findPlaylist(id) {
  return library.playlists.find((playlist) => playlist.id === id);
}

/** Gets a saved playlist with tracks in their playback order. */
export function getPlaylist(id) {
  const playlist = findPlaylist(id);
  return playlist && { ...playlist, tracks: playlist.tracks.slice() };
}

/** Opens the playlist picker for an audio track from the work details. */
export function showPlaylistPicker(track) {
  pickerTrack = normalizeTrack(track);
}

/** Closes the picker and returns whether it had been open. */
export function closePlaylistPicker() {
  if (!pickerTrack) return false;
  pickerTrack = null;
  return true;
}

function trackName(track) {
  return track.title || track.rel_path?.split(/[\\/]/).pop() || `音轨 ${track.id}`;
}

function playlistNameForm(mode, label, name, esc) {
  const isPicker = mode === "create-and-add";
  return `
    <form class="playlist-picker-form" data-library-form="${mode}">
      <label for="playlist-name-${mode}">${label}</label>
      <input id="playlist-name-${mode}" name="playlist-name" maxlength="${PLAYLIST_NAME_LIMIT}" value="${esc(name)}"
        placeholder="输入播放列表名称" required />
      <button type="submit" class="primary">${isPicker ? "新建并添加" : "保存"}</button>
      ${isPicker ? "" : '<button type="button" data-action="playlist-cancel-form">取消</button>'}
    </form>
  `;
}

function playlistFormDisclosure(mode, name, esc) {
  return `<div id="playlist-form-${mode}" class="disclosure playlist-form-disclosure" data-playlist-form="${mode}" aria-hidden="true" inert>
    <div class="disclosure-content">${playlistNameForm(mode, "列表名称", name, esc)}</div>
  </div>`;
}

/** Reconciles the inline forms without replacing their controls or interrupting transitions. */
export function syncPlaylistForms(region) {
  for (const panel of region.querySelectorAll(".playlist-form-disclosure")) {
    const mode = panel.dataset.playlistForm;
    const trigger = region.querySelector(`[aria-controls="${panel.id}"]`);
    const isOpen = mode === playlistForm?.mode && (mode !== "rename" || trigger.dataset.playlistId === playlistForm.id);
    if (panel.getAttribute("aria-hidden") === String(!isOpen)) continue;

    const form = panel.querySelector("form");
    if (isOpen) form.reset();
    else if (panel.contains(region.ownerDocument.activeElement)) trigger.focus({ preventScroll: true });
    panel.setAttribute("aria-hidden", String(!isOpen));
    panel.inert = !isOpen;
    trigger.setAttribute("aria-expanded", String(isOpen));
    form.elements.namedItem("playlist-name").toggleAttribute("data-library-autofocus", isOpen);
  }
}

function playlistTrackRows(playlist, esc, icon) {
  if (!playlist.tracks.length) return '<p class="playlist-empty">列表为空。可从作品的音轨目录添加。</p>';
  const id = esc(playlist.id);
  return `
    <ol class="playlist-tracks">
      ${playlist.tracks.map((track, index) => `
        <li class="playlist-track">
          <button type="button" class="playlist-track-play" data-action="playlist-play" data-playlist-id="${id}" data-index="${index}" data-track-id="${track.id}">
            ${icon("play")}
            <span class="playlist-track-meta">
              <strong>${esc(trackName(track))}</strong>
              <small>${esc(track.workTitle || track.workno)}${track.workVas.length ? ` · ${esc(track.workVas.join(" / "))}` : ""}</small>
            </span>
          </button>
          <div class="playlist-track-actions">
            <button type="button" class="icon-button" data-action="playlist-move-track" data-playlist-id="${id}" data-track-id="${track.id}" data-direction="-1" aria-label="上移音轨" title="上移音轨" ${index === 0 ? "disabled" : ""}>${icon("chevron-up")}</button>
            <button type="button" class="icon-button" data-action="playlist-move-track" data-playlist-id="${id}" data-track-id="${track.id}" data-direction="1" aria-label="下移音轨" title="下移音轨" ${index === playlist.tracks.length - 1 ? "disabled" : ""}>${icon("chevron-down")}</button>
            <button type="button" class="icon-button" data-action="playlist-remove-track" data-playlist-id="${id}" data-track-id="${track.id}" aria-label="移出列表" title="移出列表">${icon("x")}</button>
          </div>
        </li>
      `).join("")}
    </ol>
  `;
}

/** Renders the playlist index and the selected playlist. */
export function renderPlaylists(esc, icon) {
  const selected = findPlaylist(selectedPlaylistId) ?? library.playlists[0];
  const id = esc(selected?.id);
  return `
    <section class="playlists" aria-label="播放列表">
      <div class="playlist-sidebar">
        <div class="playlist-heading">
          <h2>我的列表</h2>
          <button type="button" class="icon-button" id="playlist-create-trigger" data-action="playlist-create"
            aria-label="新建播放列表" title="新建播放列表" aria-controls="playlist-form-create" aria-expanded="false">${icon("plus")}</button>
        </div>
        ${playlistFormDisclosure("create", "", esc)}
        <div class="playlist-index">
          ${library.playlists.map((playlist) => `
            <button type="button" class="playlist-option" data-action="playlist-select" data-playlist-id="${esc(playlist.id)}" aria-pressed="${playlist.id === selected?.id}">
              ${icon("list-music")}
              <span>${esc(playlist.name)}</span>
              <small>${playlist.tracks.length} 首</small>
            </button>
          `).join("")}
        </div>
      </div>
      <div class="playlist-detail">
        ${selected ? `
          <div class="playlist-heading">
            <h2>${esc(selected.name)}</h2>
            <div class="playlist-track-actions">
              <button type="button" data-action="playlist-play" data-playlist-id="${id}" data-index="0" ${selected.tracks.length ? "" : "disabled"}>${icon("play")} 播放全部</button>
              <button type="button" class="icon-button" id="playlist-rename-trigger" data-action="playlist-rename" data-playlist-id="${id}"
                aria-label="重命名" title="重命名" aria-controls="playlist-form-rename" aria-expanded="false">${icon("pencil")}</button>
              <button type="button" class="icon-button" data-action="playlist-delete" data-playlist-id="${id}" aria-label="删除播放列表" title="删除播放列表">${icon("trash")}</button>
            </div>
          </div>
          ${playlistFormDisclosure("rename", selected.name, esc)}
          ${playlistTrackRows(selected, esc, icon)}
        ` : '<p class="playlist-empty">暂无播放列表</p>'}
      </div>
    </section>
  `;
}

function renderPlaylistPicker(esc, icon) {
  if (!pickerTrack) return "";
  return `
    <div class="playlist-heading">
      <h2 id="playlist-picker-title">添加到播放列表</h2>
      <button type="button" class="icon-button" data-action="playlist-close-picker" aria-label="关闭" title="关闭" autofocus>${icon("x")}</button>
    </div>
    <p id="playlist-picker-track">${esc(trackName(pickerTrack))}</p>
    <div class="playlist-index">
      ${library.playlists.map((playlist) => {
        const isAdded = playlist.tracks.some((track) => track.id === pickerTrack.id);
        return `
          <button type="button" class="playlist-option" data-action="playlist-save-track" data-playlist-id="${esc(playlist.id)}" ${isAdded ? "disabled" : ""}>
            ${icon(isAdded ? "check" : "list-music")}
            <span>${esc(playlist.name)}</span>
            <small>${isAdded ? "已添加" : `${playlist.tracks.length} 首`}</small>
          </button>
        `;
      }).join("")}
    </div>
    ${playlistNameForm("create-and-add", "新列表名称", "", esc)}
  `;
}

function removePlaylistPicker(dialog) {
  dialog.close();
  dialog.remove();
}

function dismissPlaylistPicker(dialog) {
  dialog.inert = true;
  delete dialog.dataset.visible;
  const fade = dialog.getAnimations().find((animation) => animation.transitionProperty === "opacity");
  if (!fade) {
    removePlaylistPicker(dialog);
    return;
  }
  const finish = () => {
    if (dialog.isConnected) removePlaylistPicker(dialog);
  };
  fade.finished.then(finish, finish);
}

function updatePlaylistPicker(dialog, html) {
  const input = dialog.querySelector("input");
  const draft = input.value;
  const selection = [input.selectionStart, input.selectionEnd];
  const active = dialog.ownerDocument.activeElement;
  dialog.innerHTML = html;
  const nextInput = dialog.querySelector("input");
  nextInput.value = draft;
  if (active === input) {
    nextInput.focus({ preventScroll: true });
    nextInput.setSelectionRange(...selection);
  } else {
    dialog.querySelector("[autofocus]").focus({ preventScroll: true });
  }
}

/** Reconciles the modal with the selected track and waits for its exit transition. */
export function syncPlaylistPicker(region, esc, icon, render) {
  const current = region.firstElementChild;
  const html = renderPlaylistPicker(esc, icon);
  if (!html) {
    pickerHTML = "";
    if (current && !current.inert) dismissPlaylistPicker(current);
    return;
  }
  if (current && !current.inert) {
    if (html !== pickerHTML) updatePlaylistPicker(current, html);
    pickerHTML = html;
    return;
  }
  if (current) removePlaylistPicker(current);

  const dialog = region.ownerDocument.createElement("dialog");
  dialog.className = "playlist-picker";
  dialog.id = "playlist-picker";
  dialog.setAttribute("aria-labelledby", "playlist-picker-title");
  dialog.setAttribute("aria-describedby", "playlist-picker-track");
  dialog.innerHTML = html;
  pickerHTML = html;

  const dismiss = () => {
    if (closePlaylistPicker()) render();
  };
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    dismiss();
  });
  dialog.addEventListener("click", (event) => {
    if (event.target !== dialog) return;
    const bounds = dialog.getBoundingClientRect();
    if (event.clientX < bounds.left || event.clientX > bounds.right ||
        event.clientY < bounds.top || event.clientY > bounds.bottom) {
      dismiss();
    }
  });
  region.append(dialog);
  dialog.showModal();
  // Apply the initial opacity and transform before starting the transition.
  dialog.getBoundingClientRect();
  dialog.dataset.visible = "";
}

/** Handles collection actions that do not start audio playback. */
export function handleLibraryAction(target) {
  const { action, playlistId, trackId, direction } = target.dataset;
  switch (action) {
    case "playlist-select":
      selectedPlaylistId = playlistId;
      playlistForm = null;
      break;
    case "playlist-create":
      playlistForm = { mode: "create" };
      break;
    case "playlist-rename":
      playlistForm = { mode: "rename", id: playlistId };
      break;
    case "playlist-cancel-form":
      playlistForm = null;
      break;
    case "playlist-delete": {
      const playlist = getPlaylist(playlistId);
      if (playlist && window.confirm(`删除播放列表“${playlist.name}”？`)) {
        changeLibrary((collection) => {
          collection.playlists = collection.playlists.filter((item) => item.id !== playlistId);
        });
        selectedPlaylistId = null;
        playlistForm = null;
      }
      break;
    }
    case "playlist-remove-track":
    case "playlist-move-track":
      changeLibrary(() => {
        const playlist = findPlaylist(playlistId);
        if (!playlist) return;
        const index = playlist.tracks.findIndex((track) => track.id === Number(trackId));
        if (index < 0) return;
        if (action === "playlist-remove-track") {
          playlist.tracks.splice(index, 1);
          return;
        }
        const next = index + Number(direction);
        if (next < 0 || next >= playlist.tracks.length) return;
        [playlist.tracks[index], playlist.tracks[next]] = [playlist.tracks[next], playlist.tracks[index]];
      });
      break;
    case "playlist-close-picker":
      closePlaylistPicker();
      break;
    case "playlist-save-track":
      if (!pickerTrack) break;
      changeLibrary(() => {
        const playlist = findPlaylist(playlistId);
        if (!playlist) return;
        if (!playlist.tracks.some((track) => track.id === pickerTrack.id)) playlist.tracks.push(pickerTrack);
        closePlaylistPicker();
      });
      break;
    default:
      return false;
  }
  return true;
}

/** Saves a create or rename form. Returns false if the form needs correction. */
export function handleLibraryForm(form) {
  const input = form.elements.namedItem("playlist-name");
  const name = input.value.trim();
  input.setCustomValidity(name ? "" : "请输入播放列表名称");
  if (!input.reportValidity()) {
    input.setCustomValidity("");
    return false;
  }

  const mode = form.dataset.libraryForm;
  changeLibrary((collection) => {
    if (mode === "rename") {
      const playlist = findPlaylist(playlistForm?.id);
      if (playlist) playlist.name = name;
      return;
    }
    const id = crypto.getRandomValues(new Uint32Array(2)).join("-");
    const tracks = mode === "create-and-add" && pickerTrack ? [pickerTrack] : [];
    collection.playlists.push({ id, name, tracks });
    selectedPlaylistId = id;
  });
  playlistForm = null;
  closePlaylistPicker();
  return true;
}
