import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { setImmediate } from "node:timers/promises";
import { Script } from "node:vm";

// Exercise the player with browser, drawer and network test doubles.
const source = readFileSync(new URL("../src/app.js", import.meta.url), "utf8")
  .replaceAll("\r\n", "\n")
  .replace(/^import \{[\s\S]*?\} from "\.\/library\.js";\n/, "")
  .replace(/^import \{ closeDropdown, enhanceDropdowns \} from "\.\/dropdowns\.js";\n/m, "")
  .replace(/\nstartApp\(\);\s*$/, "");
const playerScript = new Script(`${source}
  const renders = { full: 0, player: 0 };
  render = () => { renders.full++; };
  renderPlayerOnly = () => { renders.player++; };
  openWorkDrawer = async (workno) => loadWorkDetail(workno);
  ({ audio, player: state.player, playSavedQueue, playQueueIndex, resetPlayer,
    seekTrack, resumeTrack, resumeFromHistory, setSleepTimer, renderPlayerHTML,
    togglePlayerCollapse, setQueueOpen, setSleepMenuOpen, closePlayerOverlay, renders });
`, { filename: "web/src/app.js" });

const firstTrack = {
  id: 1,
  title: "第一话",
  workno: "RJ100001",
  workTitle: "第一部作品",
  workVas: ["声优甲"],
  duration: 22,
};
const secondTrack = {
  id: 2,
  title: "第二话",
  workno: "RJ100002",
  workTitle: "第二部作品",
  workVas: ["声优乙"],
  duration: 45,
};

class FakeAudio extends EventTarget {
  volume = 1;
  paused = true;
  ended = false;
  readyState = 0;
  currentTime = 0;
  duration = 120;

  async play() {
    this.paused = false;
    this.dispatchEvent(new Event("play"));
  }

  pause() {
    if (this.paused) return;
    this.paused = true;
    this.dispatchEvent(new Event("pause"));
  }

  removeAttribute(name) {
    delete this[name];
  }

  load() {
    this.ended = false;
    this.readyState = 0;
    this.currentTime = 0;
  }

  loadMetadata() {
    this.readyState = 1;
    this.dispatchEvent(new Event("loadedmetadata"));
  }

  finish() {
    this.currentTime = this.duration;
    this.ended = true;
    this.dispatchEvent(new Event("timeupdate"));
    this.pause();
    this.dispatchEvent(new Event("ended"));
  }

  canPlayType() {
    return "";
  }
}

function fakeResponse(body) {
  return { ok: true, json: async () => body };
}

function createPlayer(workDetail, { elements = new Map(), preferences = new Map() } = {}) {
  const heartbeats = [];
  const subtitle = { textContent: "", hidden: true };
  const pendingSubtitles = new Map();
  const hls = { source: null, destroyed: false };
  let sleepSchedule;

  class FakeHls {
    static Events = { ERROR: "error" };

    static isSupported() {
      return true;
    }

    loadSource(url) {
      hls.source = url;
    }

    attachMedia() {}
    on() {}

    destroy() {
      hls.destroyed = true;
    }
  }

  const app = playerScript.runInNewContext({
    Audio: FakeAudio,
    FormData,
    performance: { now: () => 1000 },
    window: { Hls: FakeHls, matchMedia: () => new EventTarget(), addEventListener() {} },
    isFavorite: () => false,
    localStorage: {
      getItem: (key) => preferences.get(key) ?? null,
      setItem: (key, value) => preferences.set(key, String(value)),
    },
    document: {
      addEventListener() {},
      querySelector: (selector) => elements.get(selector) ?? null,
      querySelectorAll: (selector) => elements.get(selector) ?? [],
      getElementById: (id) => id === "player-sub" ? subtitle : elements.get(`#${id}`) ?? null,
    },
    loadWorkDetail(workno) {
      assert.equal(workno, workDetail?.workno);
      return workDetail;
    },
    async fetch(path, options) {
      const subtitlePath = "/api/subtitle/";
      if (path.startsWith(subtitlePath)) {
        const trackId = Number(path.slice(subtitlePath.length));
        return new Promise((resolve) => pendingSubtitles.set(trackId, resolve));
      }
      assert.equal(path, "/api/history/heartbeat");
      heartbeats.push(JSON.parse(options.body));
      return fakeResponse({});
    },
    setTimeout(callback, delay) {
      sleepSchedule = { callback, delay };
      return 1;
    },
    clearTimeout() {
      sleepSchedule = undefined;
    },
  });

  return {
    ...app,
    heartbeats,
    subtitle,
    hls,
    get sleepSchedule() {
      return sleepSchedule;
    },
    resolveSubtitle(trackId, text) {
      pendingSubtitles.get(trackId)(fakeResponse({
        found: true,
        cues: [{ start: 0, end: 5, text }],
      }));
    },
    expireSleepTimer() {
      const { callback } = sleepSchedule;
      sleepSchedule = undefined;
      callback();
    },
  };
}

test("switching between works saves the previous track's history", async () => {
  const app = createPlayer();
  await app.playSavedQueue([firstTrack, secondTrack], 0);
  app.audio.currentTime = 7;

  await app.playQueueIndex(1, 0);

  assert.equal(app.heartbeats.length, 1);
  assert.equal(app.heartbeats[0].workno, firstTrack.workno);
  assert.equal(app.heartbeats[0].track_id, firstTrack.id);
  assert.equal(app.heartbeats[0].position, 7);
  assert.equal(app.heartbeats[0].queue, null);
  assert.equal(app.player.workno, secondTrack.workno);
  assert.equal(app.player.workTitle, secondTrack.workTitle);
  assert.equal(app.audio.src, `/api/stream/${secondTrack.id}`);
});

test("a late subtitle response cannot replace the current track's subtitles", async () => {
  const app = createPlayer();
  await app.playSavedQueue([firstTrack, secondTrack], 0);
  await app.playQueueIndex(1, 0);
  app.resolveSubtitle(secondTrack.id, "当前音轨字幕");
  await setImmediate();
  app.resolveSubtitle(firstTrack.id, "旧音轨字幕");
  await setImmediate();

  assert.equal(app.subtitle.textContent, "当前音轨字幕");
  assert.equal(app.subtitle.hidden, false);
});

test("the saved HLS flag is sufficient for playback and logout releases the stream", async () => {
  const app = createPlayer();
  await app.playSavedQueue([{ ...secondTrack, streamIsHLS: true }], 0);
  assert.equal(app.hls.source, `/api/stream/${secondTrack.id}`);
  app.player.queueOpen = true;
  app.setSleepTimer(15);

  await app.resetPlayer();

  assert.equal(app.heartbeats.at(-1).track_id, secondTrack.id);
  assert.equal(app.hls.destroyed, true);
  assert.equal(app.audio.paused, true);
  assert.equal(app.player.track, null);
  assert.equal(app.player.queue.length, 0);
  assert.equal(app.player.queueOpen, false);
  assert.equal(app.sleepSchedule, undefined);
});

test("the sleep timer pauses playback and clears its deadline", async () => {
  const app = createPlayer();
  await app.playSavedQueue([firstTrack], 0);
  app.setSleepTimer(5);
  assert.equal(app.sleepSchedule.delay, 5 * 60_000);

  app.expireSleepTimer();

  assert.equal(app.audio.paused, true);
  assert.equal(app.player.playing, false);
  assert.equal(app.player.sleepDeadline, 0);
});

function playerTag(markup, id) {
  const tag = markup.match(new RegExp(`<[^>]+\\bid="${id}"[^>]*>`))?.[0];
  assert.ok(tag, `Expected #${id}`);
  return tag;
}

function assertPlayerSurface(markup, id, isHidden) {
  const tag = playerTag(markup, id);
  assert.ok(tag.includes(`aria-hidden="${isHidden}"`));
  assert.equal(/\sinert(?:[\s=>])/.test(tag), isHidden);
}

function fakePlayerSurface(isHidden) {
  const attributes = new Map([["aria-hidden", String(isHidden)]]);
  return {
    inert: isHidden,
    setAttribute: (name, value) => attributes.set(name, value),
    getAttribute: (name) => attributes.get(name),
  };
}

test("the sleep menu can close and reopen without replacing its animated surface", () => {
  const menu = fakePlayerSurface(true);
  const trigger = fakePlayerSurface(false);
  let focusCount = 0;
  trigger.focus = () => { focusCount++; };
  const elements = new Map([
    ["#player-timer-menu", menu],
    ["#player-timer-trigger", trigger],
  ]);
  const app = createPlayer(undefined, { elements });
  assertPlayerSurface(app.renderPlayerHTML(), "player-timer-menu", true);

  app.setSleepMenuOpen(true);
  assertPlayerSurface(app.renderPlayerHTML(), "player-timer-menu", false);
  assert.equal(menu.inert, false);
  assert.equal(menu.getAttribute("aria-hidden"), "false");
  assert.equal(trigger.getAttribute("aria-expanded"), "true");
  assert.doesNotMatch(menu.innerHTML, /取消定时/);
  const choices = menu.innerHTML;

  assert.equal(app.closePlayerOverlay(), true);
  assert.equal(menu.inert, true);
  assert.equal(menu.getAttribute("aria-hidden"), "true");
  assert.equal(trigger.getAttribute("aria-expanded"), "false");
  assert.equal(focusCount, 1);
  assertPlayerSurface(app.renderPlayerHTML(), "player-timer-menu", true);
  app.setSleepTimer(15);
  assert.equal(menu.innerHTML, choices);

  app.setSleepMenuOpen(true);
  assert.equal(menu.inert, false);
  assert.match(menu.innerHTML, /取消定时/);
  assert.equal(app.renders.player, 0);
});

function queueFixture(isOpen = false) {
  const drawer = { ...fakePlayerSurface(!isOpen), querySelector: () => null };
  const scrim = fakePlayerSurface(!isOpen);
  const trigger = fakePlayerSurface(false);
  trigger.setAttribute("aria-expanded", String(isOpen));
  const focused = [];
  trigger.focus = () => focused.push("trigger");
  const closeButton = {
    focus() {
      assert.equal(drawer.inert, false);
      focused.push("close");
    },
  };
  const elements = new Map([
    ["#player-queue", drawer],
    ["#queue-scrim", scrim],
    ["#queue-close", closeButton],
    ['#player-bar [data-action="toggle-queue"]', trigger],
  ]);
  return { drawer, scrim, trigger, focused, elements };
}

function assertQueueOpen(app, queue, open) {
  assert.equal(app.player.queueOpen, open);
  assert.equal(queue.trigger.getAttribute("aria-expanded"), String(open));
  assert.equal(queue.drawer.inert, !open);
  assert.equal(queue.drawer.getAttribute("aria-hidden"), String(!open));
  assert.equal(queue.scrim.inert, !open);
  assert.equal(queue.scrim.getAttribute("aria-hidden"), String(!open));
  const markup = app.renderPlayerHTML();
  assertPlayerSurface(markup, "player-queue", !open);
  assertPlayerSurface(markup, "queue-scrim", !open);
}

test("collapsing and reopening preserves playback and the scheduled sleep timer", async () => {
  const preferences = new Map();
  const focusOrder = [];
  const layoutClasses = new Set();
  const layout = {
    classList: {
      toggle(name, enabled) {
        if (enabled) layoutClasses.add(name);
        else layoutClasses.delete(name);
      },
    },
  };
  const bar = { ...fakePlayerSurface(false), closest: () => layout };
  const dock = fakePlayerSurface(true);
  const expandButton = {
    focus() {
      assert.equal(dock.inert, false);
      focusOrder.push("expand");
    },
  };
  const collapseButton = {
    focus() {
      assert.equal(bar.inert, false);
      focusOrder.push("collapse");
    },
  };
  const queue = queueFixture(true);
  const elements = new Map([
    ...queue.elements,
    ["#player-bar", bar],
    ["#player-dock", dock],
    ["#player-expand", expandButton],
    ["#player-collapse", collapseButton],
  ]);
  const app = createPlayer(undefined, { elements, preferences });
  await app.playSavedQueue([firstTrack, secondTrack], 0);
  app.audio.currentTime = 7;
  app.audio.dispatchEvent(new Event("timeupdate"));
  app.setSleepTimer(15);
  const { sleepDeadline } = app.player;
  const timer = app.sleepSchedule;
  app.player.queueOpen = true;

  app.togglePlayerCollapse();

  const collapsed = app.renderPlayerHTML();
  assertPlayerSurface(collapsed, "player-bar", true);
  assertPlayerSurface(collapsed, "player-dock", false);
  assert.equal(bar.inert, true);
  assert.equal(bar.getAttribute("aria-hidden"), "true");
  assert.equal(dock.getAttribute("aria-hidden"), "false");
  assert.equal(layoutClasses.has("player-collapsed"), true);
  assertQueueOpen(app, queue, false);
  assert.deepEqual(queue.focused, []);
  assert.equal(preferences.get("otoha.player-collapsed"), "true");
  const restored = createPlayer(undefined, { preferences });
  assertPlayerSurface(restored.renderPlayerHTML(), "player-bar", true);
  assertPlayerSurface(restored.renderPlayerHTML(), "player-dock", false);

  app.togglePlayerCollapse();

  const expanded = app.renderPlayerHTML();
  assertPlayerSurface(expanded, "player-bar", false);
  assertPlayerSurface(expanded, "player-dock", true);
  assert.equal(dock.inert, true);
  assert.equal(dock.getAttribute("aria-hidden"), "true");
  assert.equal(bar.getAttribute("aria-hidden"), "false");
  assert.equal(layoutClasses.has("player-collapsed"), false);
  assert.deepEqual(focusOrder, ["expand", "collapse"]);
  assert.equal(preferences.get("otoha.player-collapsed"), "false");
  assert.equal(app.audio.paused, false);
  assert.equal(app.audio.currentTime, 7);
  assert.equal(app.player.track.id, firstTrack.id);
  assert.equal(app.player.queue.length, 2);
  assert.equal(app.player.sleepDeadline, sleepDeadline);
  assert.equal(app.sleepSchedule, timer);
});

test("reversing the queue drawer preserves its surfaces, focus order, and playback", async () => {
  const queue = queueFixture();
  const app = createPlayer(undefined, { elements: queue.elements });
  await app.playSavedQueue([firstTrack, secondTrack], 0);
  app.audio.currentTime = 7;
  app.audio.dispatchEvent(new Event("timeupdate"));
  const renders = { ...app.renders };
  assertQueueOpen(app, queue, false);

  app.setQueueOpen(true);
  assertQueueOpen(app, queue, true);
  app.setQueueOpen(false);
  assertQueueOpen(app, queue, false);
  app.setQueueOpen(true);
  assertQueueOpen(app, queue, true);

  assert.deepEqual(queue.focused, ["close", "trigger", "close"]);
  assert.deepEqual({ ...app.renders }, renders);
  assert.equal(app.audio.paused, false);
  assert.equal(app.audio.currentTime, 7);
  assert.equal(app.player.track.id, firstTrack.id);
  assert.equal(app.player.queue.length, 2);
});

test("the mini player shows the current work and an independent playback control", async () => {
  const preferences = new Map([["otoha.player-collapsed", "true"]]);
  const app = createPlayer(undefined, { preferences });

  const miniPlayer = () => {
    const dock = app.renderPlayerHTML().match(/<aside\b[^>]*\bid="player-dock"[^>]*>[\s\S]*?<\/aside>/)?.[0];
    assert.ok(dock, "Expected mini player markup");
    assertPlayerSurface(dock, "player-dock", false);
    return dock;
  };
  const idle = miniPlayer();
  assert.match(playerTag(idle, "player-dock-toggle"), /\sdisabled(?:\s|>)/);
  assert.doesNotMatch(idle, /\/api\/cover\//);
  assert.match(playerTag(idle, "player-expand"), /data-action="toggle-player-collapse"/);

  await app.playSavedQueue([firstTrack, secondTrack], 0);

  const first = miniPlayer();
  assert.match(first, /class="player-dock-title"[^>]*>第一部作品</);
  assert.match(first, /class="player-dock-track"[^>]*>第一话</);
  assert.ok(first.includes(`src="/api/cover/${firstTrack.workno}"`));
  const playToggle = playerTag(first, "player-dock-toggle");
  assert.match(playToggle, /data-action="player-toggle"/);
  assert.match(playToggle, /aria-label="暂停"/);
  assert.doesNotMatch(playToggle, /\sdisabled(?:\s|>)/);

  await app.playQueueIndex(1, 0);

  const second = miniPlayer();
  assert.match(second, /class="player-dock-title"[^>]*>第二部作品</);
  assert.match(second, /class="player-dock-track"[^>]*>第二话</);
  assert.ok(second.includes(`src="/api/cover/${secondTrack.workno}"`));
  assert.ok(!second.includes(firstTrack.workno));
  app.audio.pause();
  assert.match(playerTag(miniPlayer(), "player-dock-toggle"), /aria-label="播放"/);
});

test("a segment resumes and seeks relative to its own timeline", async () => {
  const app = createPlayer();
  const segment = { ...firstTrack, start_seconds: 30, end_seconds: 52, play_position: 4 };
  await app.playQueueIndex(0, null, { tracks: [segment], folderScope: "ASMR18" });
  app.audio.loadMetadata();

  assert.equal(app.audio.currentTime, 34);
  assert.equal(app.player.currentTime, 4);
  assert.equal(app.player.duration, 22);
  assert.equal(app.heartbeats.at(-1).position, 4);
  assert.equal(app.heartbeats.at(-1).track_duration, 22);

  app.seekTrack(10);
  app.audio.dispatchEvent(new Event("seeked"));
  assert.equal(app.audio.currentTime, 40);
  assert.equal(app.player.currentTime, 10);
  assert.equal(app.heartbeats.at(-1).position, 10);
});

test("segments advance at their boundary and the last one can be replayed", async () => {
  const app = createPlayer();
  const segments = [
    { ...firstTrack, start_seconds: 0, end_seconds: 22 },
    { ...secondTrack, start_seconds: 22, end_seconds: 67 },
  ];
  await app.playSavedQueue(segments, 0);
  app.audio.loadMetadata();
  app.audio.currentTime = 22.1;
  app.audio.dispatchEvent(new Event("timeupdate"));
  await setImmediate();

  assert.equal(app.player.track.id, secondTrack.id);
  assert.equal(app.heartbeats.at(-1).position, 22);
  app.audio.loadMetadata();
  assert.equal(app.audio.currentTime, 22);
  assert.equal(app.player.currentTime, 0);

  app.audio.currentTime = 67.1;
  app.audio.dispatchEvent(new Event("timeupdate"));
  assert.equal(app.audio.paused, true);
  assert.equal(app.player.currentTime, 45);
  assert.equal(app.heartbeats.at(-1).position, 45);

  await app.resumeTrack();
  assert.equal(app.audio.currentTime, 22);
  assert.equal(app.audio.paused, false);
});

test("legacy history resumes in the segment containing the old position", async () => {
  const oldTrackId = 101;
  const workDetail = {
    workno: firstTrack.workno,
    title: firstTrack.workTitle,
    tracks: [
      { ...firstTrack, source_track_id: oldTrackId, start_seconds: 0, end_seconds: 22 },
      { ...secondTrack, source_track_id: oldTrackId, start_seconds: 22, duration: null },
    ],
  };
  const app = createPlayer(workDetail);
  await app.resumeFromHistory({
    workno: workDetail.workno,
    track_id: oldTrackId,
    position: 35,
    queue_ctx: JSON.stringify({ track_ids: [oldTrackId], folder_scope: "ASMR18" }),
  });
  app.audio.loadMetadata();

  assert.equal(app.player.track.id, secondTrack.id);
  assert.equal(app.player.queue.length, 2);
  assert.equal(app.player.folderScope, "ASMR18");
  assert.equal(app.audio.currentTime, 35);
  assert.equal(app.player.currentTime, 13);
  assert.equal(app.player.duration, 98);
  assert.equal(app.heartbeats.at(-1).track_duration, 98);
});

test("a segment without an end timestamp advances when the media ends", async () => {
  const app = createPlayer();
  await app.playSavedQueue([{ ...firstTrack, start_seconds: 90, duration: null }, secondTrack], 0);
  app.audio.loadMetadata();
  app.audio.finish();
  await setImmediate();

  assert.equal(app.player.track.id, secondTrack.id);
  assert.equal(app.heartbeats.at(-1).position, 30);
  assert.equal(app.heartbeats.at(-1).track_duration, 30);
});
