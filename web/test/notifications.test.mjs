import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { Script } from "node:vm";

const source = readFileSync(new URL("../src/app.js", import.meta.url), "utf8")
  .replaceAll("\r\n", "\n")
  .replace(/^import \{[\s\S]*?\} from "\.\/library\.js";\n/, "")
  .replace(/^import \{ closeDropdown, enhanceDropdowns \} from "\.\/dropdowns\.js";\n/m, "")
  .replace(/\nstartApp\(\);\s*$/, "");
const notificationScript = new Script(`${source}
  ({ notify, updateTaskNotification, syncTaskIndicator, state });
`, { filename: "web/src/app.js" });

class FakeElement extends EventTarget {
  children = [];
  innerHTML = "";
  textContent = "";
  parentElement = null;
  #markupNodes = new Map();

  prepend(child) {
    this.children.unshift(child);
    child.parentElement = this;
  }

  remove() {
    if (!this.parentElement) return;
    const siblings = this.parentElement.children;
    siblings.splice(siblings.indexOf(this), 1);
    this.parentElement = null;
  }

  querySelector(selector) {
    if (!this.innerHTML.includes(selector.slice(1))) return null;
    if (!this.#markupNodes.has(selector)) {
      const child = new FakeElement();
      child.parentElement = this;
      this.#markupNodes.set(selector, child);
    }
    return this.#markupNodes.get(selector);
  }

  contains(element) {
    for (let node = element; node; node = node.parentElement) {
      if (node === this) return true;
    }
    return false;
  }
}

function loadNotifications() {
  let elapsed = 0;
  let timerId = 0;
  const timers = new Map();
  const stack = new FakeElement();
  const indicator = { hidden: true };
  const document = {
    addEventListener() {},
    getElementById(id) { return id === "notifications" ? stack : null; },
    createElement() { return new FakeElement(); },
    querySelector(selector) { return selector === '[data-action="show-task-status"]' ? indicator : null; },
  };
  const implementation = notificationScript.runInNewContext({
    Audio: EventTarget,
    document,
    window: {
      addEventListener() {},
      matchMedia: () => Object.assign(new EventTarget(), { matches: true }),
    },
    setTimeout(callback, delay) {
      const id = ++timerId;
      timers.set(id, { at: elapsed + delay, callback });
      return id;
    },
    clearTimeout(id) { timers.delete(id); },
  });

  function advance(milliseconds) {
    const target = elapsed + milliseconds;
    while (true) {
      const next = [...timers].sort(([, first], [, second]) => first.at - second.at)[0];
      if (!next || next[1].at > target) break;
      const [id, timer] = next;
      elapsed = timer.at;
      timers.delete(id);
      timer.callback();
    }
    elapsed = target;
  }

  return { ...implementation, stack, indicator, advance };
}

function dispatch(element, type, properties) {
  element.dispatchEvent(Object.assign(new Event(type), properties));
}

function message(card) {
  return card.querySelector(".notification-message").textContent;
}

test("a toast expires after four seconds even when its message changes", () => {
  const { notify, stack, advance } = loadNotifications();
  const notice = notify("第一页");
  const [card] = stack.children;
  assert.equal(message(card), "第一页");

  advance(2000);
  notice.updateMessage("第二页");
  assert.equal(message(card), "第二页");
  assert.equal(stack.children[0], card);
  advance(1999);
  assert.equal(stack.children.length, 1);
  advance(1);
  assert.equal(stack.children.length, 0);
});

test("a toast remains while hovered or focused and expires after both leave", () => {
  const { notify, stack, advance } = loadNotifications();
  notify("任务正在进行", { cancelLabel: "中断任务" });
  const [card] = stack.children;
  const closeButton = card.querySelector(".notification-close");

  dispatch(card, "pointerenter", { pointerType: "mouse" });
  advance(6000);
  assert.equal(stack.children.length, 1);

  dispatch(card, "focusin");
  dispatch(card, "pointerleave", { pointerType: "mouse" });
  dispatch(card, "focusout", { relatedTarget: closeButton });
  advance(6000);
  assert.equal(stack.children.length, 1);

  dispatch(card, "focusout", { relatedTarget: null });
  advance(3999);
  assert.equal(stack.children.length, 1);
  advance(1);
  assert.equal(stack.children.length, 0);
});

const idleTask = { phase: "idle", running: false };
const runningTask = {
  phase: "syncing_asmrone",
  running: true,
  started_at: 1,
  message: "已同步第一页",
};

test("dismissed task progress stays hidden until the task completes", () => {
  const { updateTaskNotification, syncTaskIndicator, state, stack, indicator, advance } = loadNotifications();
  const progress = { ...runningTask, message: "已同步第二页" };
  const done = { ...progress, phase: "done", running: false, message: "同步完成" };

  updateTaskNotification(runningTask, idleTask, false);
  const [card] = stack.children;
  assert.equal(message(card), runningTask.message);
  assert.match(card.innerHTML, /data-action="cancel-task"/);
  advance(2000);
  updateTaskNotification(progress, runningTask, false);
  assert.equal(stack.children[0], card);
  assert.equal(message(card), progress.message);

  advance(2000);
  assert.equal(stack.children.length, 0);
  state.task = progress;
  syncTaskIndicator();
  assert.equal(indicator.hidden, false);
  updateTaskNotification({ ...progress, message: "已同步第三页" }, progress, false);
  assert.equal(stack.children.length, 0);

  updateTaskNotification(done, progress, false);
  state.task = done;
  syncTaskIndicator();
  assert.equal(indicator.hidden, true);
  assert.equal(stack.children.length, 1);
  assert.equal(message(stack.children[0]), done.message);
  assert.doesNotMatch(stack.children[0].innerHTML, /data-action="cancel-task"/);
  updateTaskNotification(done, done, false);
  assert.equal(stack.children.length, 1);
});

test("an initial completed task is quiet but a subsequent fast task completion is shown", () => {
  const { updateTaskNotification, stack } = loadNotifications();
  const done = { ...runningTask, phase: "done", running: false, message: "同步完成" };

  updateTaskNotification(done, idleTask, true);
  assert.equal(stack.children.length, 0);

  const next = { ...done, started_at: 2 };
  updateTaskNotification(next, done, false);
  assert.equal(stack.children.length, 1);
  assert.equal(message(stack.children[0]), next.message);
});
