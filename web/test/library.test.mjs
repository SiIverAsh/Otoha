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
  setLibraryUser,
  showPlaylistPicker,
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
