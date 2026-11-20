import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { setImmediate } from "node:timers/promises";
import { Script } from "node:vm";

const source = readFileSync(new URL("../src/app.js", import.meta.url), "utf8")
  .replaceAll("\r\n", "\n")
  .replace(/^import \{[\s\S]*?\} from "\.\/library\.js";\n/, "")
  .replace(/^import \{ closeDropdown, enhanceDropdowns \} from "\.\/dropdowns\.js";\n/m, "")
  .replace(/\nstartApp\(\);\s*$/, "");
const drawerScript = new Script(`${source}
  ({ state, openWorkDrawer, closeWorkDrawer });
`, { filename: "web/src/app.js" });

const workno = "RJ000001";

function createAnimation(frames, options) {
  const finished = Promise.withResolvers();
  return {
    frames,
    options,
    finished: finished.promise,
    currentTime: null,
    playbackRate: 1,
    canceled: false,
    finish() {
      this.currentTime = options.duration;
      finished.resolve();
    },
    cancel() {
      this.canceled = true;
      finished.reject(new Error("Animation canceled"));
    },
  };
}

function loadDrawer({ reducedMotion = false } = {}) {
  const motionPreference = new EventTarget();
  motionPreference.matches = reducedMotion;
  const animations = [];
  const selected = new Set(["selected"]);
  const handlers = new Map();
  const drawerRegion = {
    id: "drawer-region",
    classList: { contains: () => false },
    querySelector: (selector) => selector === ".drawer" ? drawer : null,
    get firstElementChild() {
      return drawer;
    },
  };
  const queuedResponses = [];
  let drawer = null;
  let swapCount = 0;

  function addEventListener(type, listener) {
    if (!handlers.has(type)) handlers.set(type, []);
    handlers.get(type).push(listener);
  }

  function fire(type, detail) {
    const event = {
      detail,
      target: drawerRegion,
      defaultPrevented: false,
      preventDefault() { this.defaultPrevented = true; },
    };
    for (const listener of handlers.get(type) ?? []) listener(event);
    return !event.defaultPrevented;
  }

  function replaceDrawer(currentWorkno) {
    swapCount++;
    drawer = {
      dataset: { workno: currentWorkno, detail: JSON.stringify({ workno: currentWorkno }) },
      inert: false,
      scrollTop: 0,
      pose: { transform: "none", opacity: "1" },
      contains: () => false,
      querySelector: () => null,
      querySelectorAll: () => [],
      animate(frames, options) {
        const animation = createAnimation(frames, options);
        animations.push(animation);
        return animation;
      },
      remove() {
        assert.equal(drawer, this);
        drawer = null;
      },
    };
  }

  const app = drawerScript.runInNewContext({
    Audio: EventTarget,
    document: {
      addEventListener,
      body: { addEventListener },
      getElementById: (id) => id === drawerRegion.id ? drawerRegion : null,
      querySelectorAll: () => [],
      querySelector(selector) {
        if (selector === ".drawer") return drawer;
        if (selector === ".work-card.selected, .works-table tr.selected") {
          return { classList: { remove: (name) => selected.delete(name) } };
        }
        return null;
      },
    },
    window: {
      matchMedia: () => motionPreference,
      addEventListener() {},
      htmx: {
        async ajax(method, path, options) {
          assert.equal(method, "GET");
          assert.equal(options.target, "#drawer-region");
          const targetURL = new URL(path, "http://localhost");
          const requestedWorkno = decodeURIComponent(targetURL.pathname.split("/").at(-1));
          assert.equal(targetURL.pathname, `/ui/works/${encodeURIComponent(requestedWorkno)}`);
          const detail = {
            target: drawerRegion,
            elt: drawerRegion,
            requestConfig: { path, headers: options.headers },
            xhr: { status: 200 },
            shouldSwap: true,
          };
          await queuedResponses.shift();
          if (!fire("htmx:beforeSwap", detail) || !detail.shouldSwap) return;
          replaceDrawer(requestedWorkno);
          fire("htmx:afterSwap", detail);
        },
      },
    },
    getComputedStyle: (element) => element.pose,
    enhanceDropdowns() {},
    closeDropdown() {},
  });

  return {
    ...app,
    animations,
    get drawer() { return drawer; },
    get swapCount() { return swapCount; },
    get isWorkSelected() { return selected.has("selected"); },
    deferNextResponse() {
      const { promise, resolve, reject } = Promise.withResolvers();
      queuedResponses.push(promise);
      return { resolve, abort: () => reject() };
    },
  };
}

test("closing an entering drawer animates the existing element from its current pose", async () => {
  const app = loadDrawer();
  await app.openWorkDrawer(workno);
  const [entrance] = app.animations;
  const drawer = app.drawer;
  drawer.pose = { transform: "matrix(1, 0, 0, 1, 86, 0)", opacity: "0.8" };

  app.closeWorkDrawer();

  const exit = app.animations.at(-1);
  assert.equal(app.swapCount, 1);
  assert.equal(app.drawer, drawer);
  assert.equal(drawer.inert, true);
  assert.equal(entrance.canceled, true);
  assert.equal(exit.frames[0].transform, drawer.pose.transform);
  assert.equal(Number(exit.frames[0].opacity), Number(drawer.pose.opacity));
  assert.equal(exit.frames[1].transform, "translateX(100%)");
  assert.equal(Number(exit.frames[1].opacity), 0);
  assert.equal(exit.playbackRate, 1);
  assert.notEqual(exit.options.easing, entrance.options.easing);
  assert.notEqual(exit.options.duration, entrance.options.duration);
  assert.equal(app.state.selectedWorkno, workno);

  exit.finish();
  await setImmediate();
  assert.equal(app.drawer, null);
  assert.equal(app.state.detail, null);
  assert.equal(app.state.selectedWorkno, "");
  assert.equal(app.isWorkSelected, false);
  assert.equal(app.swapCount, 1);
});

test("reduced motion closes the existing drawer immediately without an animation", async () => {
  const app = loadDrawer({ reducedMotion: true });
  await app.openWorkDrawer(workno);
  assert.ok(app.drawer);

  app.closeWorkDrawer();

  assert.equal(app.animations.length, 0);
  assert.equal(app.drawer, null);
  assert.equal(app.state.detail, null);
  assert.equal(app.isWorkSelected, false);
  assert.equal(app.swapCount, 1);
});

test("a delayed fragment leaves the close animation and drawer scroll intact", async () => {
  const app = loadDrawer();
  await app.openWorkDrawer(workno);
  app.animations.at(-1).finish();
  await setImmediate();
  app.drawer.scrollTop = 112;
  const response = app.deferNextResponse();
  const delayed = app.openWorkDrawer(workno, { edit: true });
  app.closeWorkDrawer();
  const exit = app.animations.at(-1);
  exit.currentTime = 89;

  response.resolve();
  assert.equal(await delayed, null);

  assert.equal(app.animations.at(-1), exit);
  assert.equal(exit.canceled, false);
  assert.equal(exit.currentTime, 89);
  assert.equal(exit.frames[1].transform, "translateX(100%)");
  assert.equal(app.drawer.scrollTop, 112);
  assert.equal(app.drawer.inert, true);
  assert.equal(app.swapCount, 1);
});

test("opening another work absorbs the aborted request and keeps only the latest drawer", async () => {
  const app = loadDrawer();
  const firstResponse = app.deferNextResponse();
  const first = app.openWorkDrawer(workno);
  const latestWorkno = "RJ000002";
  const latest = app.openWorkDrawer(latestWorkno);

  firstResponse.abort();

  assert.equal(await first, null);
  assert.equal((await latest).workno, latestWorkno);
  assert.equal(app.drawer.dataset.workno, latestWorkno);
  assert.equal(app.swapCount, 1);
});

test("reopening a closing drawer enters from its current pose and survives the old exit", async () => {
  const app = loadDrawer();
  await app.openWorkDrawer(workno);
  app.animations.at(-1).finish();
  await setImmediate();
  app.closeWorkDrawer();
  const exit = app.animations.at(-1);
  const interruptedPose = { transform: "matrix(1, 0, 0, 1, 104, 0)", opacity: "0.74" };
  app.drawer.pose = interruptedPose;
  exit.currentTime = 110;

  await app.openWorkDrawer(workno);

  const reopened = app.animations.at(-1);
  assert.equal(exit.canceled, true);
  assert.equal(reopened.currentTime, 0);
  assert.equal(reopened.frames[0].transform, interruptedPose.transform);
  assert.equal(Number(reopened.frames[0].opacity), Number(interruptedPose.opacity));
  assert.equal(reopened.frames[1].transform, "translateX(0)");
  assert.equal(app.drawer.inert, false);
  reopened.finish();
  await setImmediate();
  assert.ok(app.drawer);
  assert.equal(app.state.selectedWorkno, workno);
});
