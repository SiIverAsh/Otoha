import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { Script } from "node:vm";

const source = readFileSync(new URL("../src/app.js", import.meta.url), "utf8")
  .replaceAll("\r\n", "\n")
  .replace(/^import \{[^}]*\} from "\.\/(?:library|dropdowns)\.js";\n/gm, "")
  .replace(/\nstartApp\(\);\s*$/, "");
const searchScript = new Script(`${source}
  api = getSuggestions;
  openWorkDrawer = recordWork;
  reportRequestError = failOnError;
  bindSearchSuggestions;
`, { filename: "web/src/app.js" });

class TestElement extends EventTarget {
  children = [];
  attributes = new Map();
  className = "";
  dataset = {};
  id = "";
  textContent = "";

  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  removeAttribute(name) { this.attributes.delete(name); }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  contains(element) {
    return this === element || this.children.some((child) => child.contains(element));
  }
  scrollIntoView() {}
  click() { send(this, "click"); }

  querySelector(selector) {
    for (const child of this.children) {
      if (`#${child.id}` === selector || `.${child.className}` === selector) return child;
      const match = child.querySelector(selector);
      if (match) return match;
    }
    return null;
  }
}

function send(element, type, fields = {}) {
  const event = new Event(type, { cancelable: true });
  for (const [key, value] of Object.entries(fields)) Object.defineProperty(event, key, { value });
  element.dispatchEvent(event);
  return event;
}

function fixture() {
  const form = new TestElement();
  const input = new TestElement();
  input.id = "search-input";
  input.value = "";
  input.focus = () => {};
  input.setAttribute("aria-expanded", "false");
  const list = new TestElement();
  list.id = "search-suggestions";
  list.hidden = true;
  form.append(input, list);

  let timerId = 0;
  const timers = new Map();
  const requests = [];
  const openedWorks = [];
  const bindSearchSuggestions = searchScript.runInNewContext({
    AbortController,
    Audio: EventTarget,
    URLSearchParams,
    document: Object.assign(new EventTarget(), {
      activeElement: input,
      createElement: () => new TestElement(),
      getElementById: (id) => form.querySelector(`#${id}`),
    }),
    window: {
      addEventListener() {},
      matchMedia: () => Object.assign(new EventTarget(), { matches: true }),
    },
    FormData: class {
      constructor(element) {
        assert.equal(element, form);
        return new URLSearchParams({ q: input.value, source: "asmrone", age: "all", page: "3" });
      }
    },
    setTimeout(callback) {
      timers.set(++timerId, callback);
      return timerId;
    },
    clearTimeout(id) { timers.delete(id); },
    failOnError(error) { assert.fail(error); },
    getSuggestions(path, { signal }) {
      const url = new URL(path, "http://otoha.test");
      return new Promise((resolve) => {
        requests.push({ url, signal, respond: (items) => resolve({ items }) });
      });
    },
    async recordWork(workno) { openedWorks.push(workno); },
  });
  bindSearchSuggestions(form);

  return {
    form, input, list, requests, openedWorks,
    type(value, fields) {
      input.value = value;
      send(input, "input", fields);
    },
    flushDebounce() {
      const pending = [...timers.values()];
      timers.clear();
      for (const callback of pending) callback();
    },
  };
}

function settle() {
  return new Promise(setImmediate);
}

test("typing a partial work number opens suggestions without submitting the form", async () => {
  const search = fixture();
  search.type("12");
  search.type("123");
  assert.equal(search.requests.length, 0);
  assert.equal(search.list.hidden, true);

  search.flushDebounce();
  assert.equal(search.requests.length, 1);
  const [request] = search.requests;
  assert.equal(request.url.pathname, "/api/search");
  assert.deepEqual(Object.fromEntries(request.url.searchParams), {
    q: "123", source: "asmrone", age: "all", page: "1", page_size: "6",
  });
  const work = { workno: "RJ00012345", title: "Moon <img src=x>" };
  request.respond([work]);
  await settle();

  assert.equal(search.list.hidden, false);
  assert.equal(search.input.getAttribute("aria-expanded"), "true");
  const [option] = search.list.children;
  assert.equal(option.getAttribute("role"), "option");
  assert.equal(option.querySelector(".search-suggestion-title").textContent, work.title);
  assert.equal(option.querySelector(".search-suggestion-workno").textContent, work.workno);
  assert.equal(send(option, "mousedown").defaultPrevented, true);
  option.click();
  assert.deepEqual(search.openedWorks, [work.workno]);
  assert.equal(search.list.hidden, true);
});

test("a late response cannot replace newer suggestions", async () => {
  const search = fixture();
  search.type("123");
  search.flushDebounce();
  search.type("456");
  search.flushDebounce();

  assert.equal(search.requests.length, 2);
  const [earlier, newer] = search.requests;
  assert.equal(earlier.signal.aborted, true);
  newer.respond([{ workno: "RJ00045678", title: "Newer match" }]);
  await settle();
  earlier.respond([{ workno: "RJ00012345", title: "Stale match" }]);
  await settle();
  assert.equal(search.list.querySelector(".search-suggestion-title").textContent, "Newer match");

  search.type("");
  search.flushDebounce();
  assert.equal(search.requests.length, 2);
  assert.equal(search.list.hidden, true);
  assert.equal(search.input.getAttribute("aria-expanded"), "false");
});

test("IME waits for committed text and Enter opens the active work", async () => {
  const search = fixture();
  send(search.input, "compositionstart");
  search.type("耳", { isComposing: true });
  search.flushDebounce();
  assert.equal(search.requests.length, 0);

  search.input.value = "耳かき";
  send(search.input, "compositionend");
  search.flushDebounce();
  assert.equal(search.requests.length, 1);
  assert.equal(search.requests[0].url.searchParams.get("q"), "耳かき");
  search.requests[0].respond([
    { workno: "RJ00012345", title: "耳かき Moon" },
    { workno: "RJ00045678", title: "耳かき Star" },
  ]);
  await settle();

  assert.equal(send(search.input, "keydown", { key: "Enter" }).defaultPrevented, false);
  assert.equal(send(search.input, "keydown", { key: "ArrowDown" }).defaultPrevented, true);
  send(search.input, "keydown", { key: "ArrowDown" });
  const [, selected] = search.list.children;
  assert.equal(selected.getAttribute("aria-selected"), "true");
  assert.equal(search.input.getAttribute("aria-activedescendant"), selected.id);
  assert.equal(send(search.input, "keydown", { key: "Enter" }).defaultPrevented, true);
  assert.deepEqual(search.openedWorks, ["RJ00045678"]);
  assert.equal(search.input.getAttribute("aria-activedescendant"), null);

  search.type("456");
  search.flushDebounce();
  assert.equal(send(search.input, "keydown", { key: "Escape" }).defaultPrevented, true);
  assert.equal(search.requests[1].signal.aborted, true);
  search.requests[1].respond([{ workno: "RJ00045678", title: "Dismissed match" }]);
  await settle();
  assert.equal(search.list.hidden, true);
});
