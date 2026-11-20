import assert from "node:assert/strict";
import test, { beforeEach } from "node:test";
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
} from "../src/library.js";

const storage = new Map();
const work = { workno: "RJ123456", title: "雨声", vas: ["声优"], tags: [{ name: "雨" }] };
const firstTrack = {
  id: 7,
  workno: work.workno,
  title: "雨",
  rel_path: "雨.mp3",
  workTitle: work.title,
  workVas: work.vas,
  stream_url: "https://audio.example.test/rain.m3u8?signature=test",
};
const secondTrack = { id: 9, workno: "RJ567890", title: "海", rel_path: "海.mp3" };

function action(action, fields = {}) {
  return handleLibraryAction({ dataset: { action, ...fields } });
}

function submit(libraryForm, value) {
  let errorMessage = "";
  return handleLibraryForm({
    dataset: { libraryForm },
    elements: {
      namedItem: () => ({
        value,
        setCustomValidity(message) {
          errorMessage = message;
        },
        reportValidity() {
          return !errorMessage;
        },
      }),
    },
  });
}

function savedLibrary(account) {
  return JSON.parse(storage.get(`otoha.library.v1.${account}`));
}

function pickerFixture() {
  const document = { createElement: () => new PickerDialog() };
  const region = {
    ownerDocument: document,
    firstElementChild: null,
    append(dialog) { this.firstElementChild = dialog; },
  };
  class PickerDialog extends EventTarget {
    dataset = {};
    open = false;
    inert = false;
    fade = null;

    get isConnected() { return region.firstElementChild === this; }
    get innerHTML() { return this.html; }
    set innerHTML(html) {
      this.html = html;
      this.input = {
        value: "",
        selectionStart: 0,
        selectionEnd: 0,
        focus() { document.activeElement = this; },
        setSelectionRange(start, end) {
          this.selectionStart = start;
          this.selectionEnd = end;
        },
      };
    }
    querySelector(selector) {
      assert.equal(selector, "input");
      return this.input;
    }
    get ownerDocument() { return document; }
    setAttribute() {}
    getBoundingClientRect() { return { left: 20, top: 20, right: 100, bottom: 100 }; }
    showModal() { this.open = true; }
    close() { this.open = false; }
    remove() { region.firstElementChild = null; }
    getAnimations() {
      return this.fade ? [{ transitionProperty: "opacity", finished: this.fade.promise }] : [];
    }
  }

  function render() {
    syncPlaylistPicker(region, String, () => "", render);
  }
  return { region, render };
}

beforeEach(() => {
  storage.clear();
  globalThis.localStorage = {
    getItem: (key) => storage.get(key) ?? null,
    setItem(key, value) {
      storage.set(key, value);
    },
  };
  setLibraryUser({ id: 101 });
});

test("isolates favorites and playlists by account and restores them on return", () => {
  assert.equal(toggleFavorite(work), true);
  action("playlist-create");
  assert.equal(submit("create", "  夜听  "), true);
  const [playlist] = savedLibrary(101).playlists;

  setLibraryUser({ id: 102 });
  assert.deepEqual(getFavorites(), []);
  assert.equal(getPlaylist(playlist.id), undefined);

  setLibraryUser({ id: 101 });
  assert.equal(getFavorites()[0].workno, work.workno);
  assert.equal(getPlaylist(playlist.id).name, "夜听");
  assert.equal(toggleFavorite(work), false);
  assert.equal(isFavorite(work.workno), false);
});

test("creates, adds, renames, moves, and removes tracks without persisting remote URLs", () => {
  showPlaylistPicker(firstTrack);
  assert.equal(submit("create-and-add", "  "), false);
  assert.equal(submit("create-and-add", "通勤"), true);
  assert.equal(closePlaylistPicker(), false);
  const [{ id }] = savedLibrary(101).playlists;
  const savedTrack = getPlaylist(id).tracks[0];
  assert.equal(savedTrack.streamIsHLS, true);
  assert.equal(savedTrack.stream_url, undefined);

  showPlaylistPicker(firstTrack);
  action("playlist-save-track", { playlistId: id });
  assert.equal(getPlaylist(id).tracks.length, 1);
  showPlaylistPicker(secondTrack);
  action("playlist-save-track", { playlistId: id });
  action("playlist-move-track", { playlistId: id, trackId: "9", direction: "-1" });
  assert.deepEqual(getPlaylist(id).tracks.map((track) => track.id), [9, 7]);

  action("playlist-rename", { playlistId: id });
  submit("rename", "雨与海");
  assert.equal(getPlaylist(id).name, "雨与海");
  action("playlist-remove-track", { playlistId: id, trackId: "9" });
  assert.deepEqual(getPlaylist(id).tracks.map((track) => track.id), [7]);
});

test("inline playlist forms keep stable markup and make only the expanded form focusable", (context) => {
  context.after(() => action("playlist-cancel-form"));
  action("playlist-create");
  submit("create", "通勤");
  const [{ id }] = savedLibrary(101).playlists;
  const document = { activeElement: null };
  const panels = ["create", "rename"].map((mode) => {
    const input = {
      value: "",
      toggleAttribute(name, enabled) {
        assert.equal(name, "data-library-autofocus");
        this.autofocus = enabled;
      },
    };
    const form = {
      elements: { namedItem: () => input },
      reset() { input.value = mode === "create" ? "" : "通勤"; },
    };
    const trigger = {
      dataset: { playlistId: id },
      setAttribute(name, value) { this[name] = value; },
      focus() { document.activeElement = this; },
    };
    return {
      id: `playlist-form-${mode}`,
      dataset: { playlistForm: mode },
      inert: true,
      "aria-hidden": "true",
      contains: (element) => element === input,
      querySelector: () => form,
      getAttribute(name) { return this[name]; },
      setAttribute(name, value) { this[name] = value; },
      input,
      trigger,
    };
  });
  const region = {
    ownerDocument: document,
    querySelectorAll: () => panels,
    querySelector: (selector) => panels.find((panel) => selector.includes(panel.id)).trigger,
  };
  const [create, rename] = panels;
  const markup = renderPlaylists(String, () => "");
  assert.match(markup, /id="playlist-form-create"[^>]*aria-hidden="true" inert/);
  assert.match(markup, /id="playlist-form-rename"[^>]*aria-hidden="true" inert/);
  assert.doesNotMatch(markup, /data-library-autofocus/);

  action("playlist-create");
  assert.equal(renderPlaylists(String, () => ""), markup);
  syncPlaylistForms(region);
  assert.equal(create.inert, false);
  assert.equal(create.input.autofocus, true);
  assert.equal(create.trigger["aria-expanded"], "true");
  create.input.value = "未保存";
  document.activeElement = create.input;
  syncPlaylistForms(region);
  assert.equal(create.input.value, "未保存");

  action("playlist-cancel-form");
  syncPlaylistForms(region);
  assert.equal(create.inert, true);
  assert.equal(create.input.autofocus, false);
  assert.equal(create.trigger["aria-expanded"], "false");
  assert.equal(document.activeElement, create.trigger);

  action("playlist-rename", { playlistId: id });
  assert.equal(renderPlaylists(String, () => ""), markup);
  syncPlaylistForms(region);
  assert.equal(create.inert, true);
  assert.equal(rename.inert, false);
  assert.equal(rename.input.autofocus, true);
  assert.equal(rename.input.value, "通勤");
});

test("the open playlist picker keeps its dialog and waits for the exit fade on Escape", async (context) => {
  context.after(() => closePlaylistPicker());
  const { region, render } = pickerFixture();
  showPlaylistPicker(firstTrack);
  render();
  const dialog = region.firstElementChild;
  assert.equal(dialog.open, true);
  assert.equal(dialog.dataset.visible, "");
  render();
  assert.equal(region.firstElementChild, dialog);

  dialog.fade = Promise.withResolvers();
  const escape = new Event("cancel", { cancelable: true });
  assert.equal(dialog.dispatchEvent(escape), false);
  render();
  assert.equal(dialog.dataset.visible, undefined);
  assert.equal(dialog.inert, true);
  assert.equal(dialog.open, true);
  assert.equal(region.firstElementChild, dialog);

  dialog.fade.resolve();
  await dialog.fade.promise;
  assert.equal(region.firstElementChild, null);
  assert.equal(dialog.open, false);
});

test("updating the playlist choices preserves the open picker and its draft", (context) => {
  context.after(() => closePlaylistPicker());
  const { region, render } = pickerFixture();
  showPlaylistPicker(firstTrack);
  render();
  const dialog = region.firstElementChild;
  dialog.input.value = "夜间收藏";
  dialog.input.focus();
  dialog.input.setSelectionRange(0, 2);

  const key = "otoha.library.v1.101";
  storage.set(key, JSON.stringify({ playlists: [{ id: "new", name: "通勤", tracks: [] }] }));
  assert.equal(refreshLibrary(key), true);
  render();

  assert.equal(region.firstElementChild, dialog);
  assert.match(dialog.innerHTML, /通勤/);
  assert.equal(dialog.input.value, "夜间收藏");
  assert.equal(dialog.ownerDocument.activeElement, dialog.input);
  assert.deepEqual([dialog.input.selectionStart, dialog.input.selectionEnd], [0, 2]);
});

test("a canceled exit cannot remove a reopened picker, and no transition closes immediately", async (context) => {
  context.after(() => closePlaylistPicker());
  const { region, render } = pickerFixture();
  showPlaylistPicker(firstTrack);
  render();
  const closing = region.firstElementChild;
  closing.fade = Promise.withResolvers();
  action("playlist-close-picker");
  render();

  showPlaylistPicker(secondTrack);
  render();
  const reopened = region.firstElementChild;
  assert.notEqual(reopened, closing);
  assert.equal(closing.open, false);
  assert.match(reopened.innerHTML, /海/);
  closing.fade.reject(new Error("Transition canceled"));
  await assert.rejects(closing.fade.promise, /Transition canceled/);
  assert.equal(region.firstElementChild, reopened);
  assert.equal(reopened.open, true);

  action("playlist-close-picker");
  render();
  assert.equal(region.firstElementChild, null);
  assert.equal(reopened.open, false);
});

test("retains segment boundaries when a playlist is saved and restored", () => {
  showPlaylistPicker({ ...firstTrack, start_seconds: 0, end_seconds: 42.5 });
  assert.equal(submit("create-and-add", "分轨"), true);
  const [{ id }] = savedLibrary(101).playlists;
  showPlaylistPicker({ ...secondTrack, start_seconds: 42.5 });
  action("playlist-save-track", { playlistId: id });
  setLibraryUser({ id: 101 });

  const [track, last] = getPlaylist(id).tracks;
  assert.equal(track.start_seconds, 0);
  assert.equal(track.end_seconds, 42.5);
  assert.equal(track.streamIsHLS, true);
  assert.equal(last.start_seconds, 42.5);
  assert.equal(last.end_seconds, undefined);
});

test("retains another tab's saved updates when editing the collection", () => {
  const differentWork = { ...work, workno: "RJ654321" };
  storage.set("otoha.library.v1.101", JSON.stringify({ favorites: [differentWork], playlists: [] }));

  toggleFavorite(work);
  assert.deepEqual(getFavorites().map((item) => item.workno), [work.workno, differentWork.workno]);

  storage.clear();
  assert.equal(refreshLibrary("otoha.library.v1.102"), false);
  assert.equal(refreshLibrary(null), true);
  assert.deepEqual(getFavorites(), []);
});

test("keeps unsaved changes in memory when storage is blocked", () => {
  globalThis.localStorage.setItem = () => {
    throw new DOMException("Storage quota reached", "QuotaExceededError");
  };
  setLibraryUser({ id: 103 });

  toggleFavorite(work);
  assert.equal(isLibraryPersistent(), false);
  assert.equal(isFavorite(work.workno), true);
  action("playlist-create");
  submit("create", "本次会话");
  assert.equal(isFavorite(work.workno), true);

  setLibraryUser({ id: 102 });
  assert.equal(isFavorite(work.workno), false);
  setLibraryUser({ id: 103 });
  assert.equal(isFavorite(work.workno), true);
});
