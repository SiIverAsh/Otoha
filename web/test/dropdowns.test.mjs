import assert from "node:assert/strict";
import test from "node:test";
import { closeDropdown, enhanceDropdowns } from "../src/dropdowns.js";

class TestElement extends EventTarget {
  children = [];
  parentElement = null;
  className = "";
  id = "";
  hidden = false;
  dataset = {};
  attributes = new Map();
  style = {};
  offsetWidth = 250;
  offsetHeight = 120;
  offsetTop = 0;
  clientHeight = 120;
  scrollTop = 0;
  animations = [];

  constructor(tag, document) {
    super();
    this.tagName = tag;
    this.ownerDocument = document;
  }

  get isConnected() { return this.ownerDocument.body.contains(this); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  setAttribute(name, value) { this.attributes.set(name, value); }
  removeAttribute(name) { this.attributes.delete(name); }
  focus() { this.ownerDocument.activeElement = this; }
  insertAdjacentHTML() {}
  getBoundingClientRect() { return { left: 24, top: 50, bottom: 90 }; }
  getAnimations() { return this.animations; }

  before(element) {
    const parent = this.parentElement;
    parent.children.splice(parent.children.indexOf(this), 0, element);
    element.parentElement = parent;
  }

  append(...elements) {
    for (const element of elements) {
      element.remove();
      element.parentElement = this;
      this.children.push(element);
    }
  }

  remove() {
    if (!this.parentElement) return;
    const siblings = this.parentElement.children;
    siblings.splice(siblings.indexOf(this), 1);
    this.parentElement = null;
  }

  matches(selector) {
    if (selector === ":disabled") return Boolean(this.disabled);
    if (selector === "select[id]:not([multiple]):not([data-dropdown-source])") {
      return this.tagName === "select" && this.id && !this.multiple &&
        !Object.hasOwn(this.dataset, "dropdownSource");
    }
    if (selector === '.dropdown-option:not([aria-disabled="true"])') {
      return this.matches(".dropdown-option") && this.getAttribute("aria-disabled") !== "true";
    }
    if (selector.startsWith("#")) return this.id === selector.slice(1);
    return this.className.split(" ").includes(selector.slice(1));
  }

  contains(element) {
    return this === element || this.children.some((child) => child.contains(element));
  }

  closest(selector) {
    return this.matches(selector) ? this : this.parentElement?.closest(selector) ?? null;
  }

  querySelectorAll(selector) {
    return this.children.flatMap((child) => [
      ...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector),
    ]);
  }

  querySelector(selector) { return this.querySelectorAll(selector)[0] ?? null; }
}

function fixture() {
  const document = new EventTarget();
  document.createElement = (tag) => new TestElement(tag, document);
  document.body = document.createElement("body");
  document.querySelector = (selector) => document.body.querySelector(selector);
  document.documentElement = { clientWidth: 1000 };
  document.defaultView = Object.assign(new EventTarget(), { AbortController, innerHeight: 700 });

  function addSelect(id, label, options) {
    const select = document.createElement("select");
    select.id = id;
    select.setAttribute("aria-label", label);
    select.selectedIndex = 0;
    select.options = options.map((option, index) => ({
      ...option, index, dataset: option.count === undefined ? {} : { count: String(option.count) },
      matches: (selector) => selector === ":disabled" && Boolean(option.disabled),
    }));
    Object.defineProperties(select, {
      selectedOptions: { get() { return [this.options[this.selectedIndex]]; } },
      value: {
        get() { return this.selectedOptions[0]?.value ?? ""; },
        set(value) { this.selectedIndex = this.options.findIndex((option) => option.value === value); },
      },
    });
    document.body.append(select);
    return select;
  }

  return { document, addSelect };
}

function send(element, type, fields = {}) {
  const event = new Event(type, { cancelable: true });
  for (const [key, value] of Object.entries(fields)) Object.defineProperty(event, key, { value });
  element.dispatchEvent(event);
  return event;
}

function typeSearch(input, text, fields) {
  input.value = text;
  send(input, "input", fields);
}

function visibleOptionLabels(listbox) {
  return listbox.querySelectorAll(".dropdown-option")
    .filter((option) => !option.hidden)
    .map((option) => option.querySelector(".dropdown-option-text").textContent);
}

test("circle search filters by name fragments and selects through the native control", (context) => {
  context.after(() => closeDropdown(false, { immediate: true }));
  const { document, addSelect } = fixture();
  const circle = addSelect("filter-circle", "社团", [
    { label: "全部社团", value: "" },
    { label: "海岸声音工房", value: "coast", count: 12 },
    { label: "森林声音工房", value: "forest" },
    { label: "海岸声音社", value: "unavailable", disabled: true },
  ]);
  circle.dataset.dropdownSearch = "";
  const changes = [];
  circle.addEventListener("change", (event) => changes.push({ value: circle.value, bubbles: event.bubbles }));
  enhanceDropdowns(document.body, "");

  const trigger = document.body.querySelector("#filter-circle-trigger");
  send(trigger, "click");
  const menu = document.body.querySelector(".dropdown-menu-searchable");
  assert.ok(menu, "expected the circle menu to offer search");
  const input = menu.querySelector(".dropdown-search");
  const listbox = menu.querySelector(".dropdown-results");
  assert.equal(menu.getAttribute("role"), "dialog");
  assert.equal(input.getAttribute("role"), "combobox");
  assert.equal(input.getAttribute("aria-controls"), listbox.id);
  assert.equal(listbox.getAttribute("role"), "listbox");
  assert.equal(document.activeElement, input);

  typeSearch(input, "银河");
  assert.deepEqual(visibleOptionLabels(listbox), []);
  assert.equal(menu.querySelector(".dropdown-no-results").hidden, false);
  assert.equal(input.getAttribute("aria-activedescendant"), null);

  typeSearch(input, "");
  assert.equal(visibleOptionLabels(listbox)[0], "全部社团");
  assert.equal(menu.querySelector(".dropdown-no-results").hidden, true);

  typeSearch(input, "声音　岸");
  assert.deepEqual(visibleOptionLabels(listbox), ["海岸声音工房", "海岸声音社"]);
  assert.deepEqual(changes, []);
  send(input, "keydown", { key: "ArrowDown" });
  const enabledMatch = listbox.querySelectorAll(".dropdown-option")
    .find((option) => !option.hidden && option.getAttribute("aria-disabled") !== "true");
  assert.equal(input.getAttribute("aria-activedescendant"), enabledMatch.id);
  assert.equal(send(input, "keydown", { key: "Enter" }).defaultPrevented, true);
  assert.deepEqual(changes, [{ value: "coast", bubbles: true }]);
  assert.equal(trigger.querySelector(".dropdown-caption").textContent, "海岸声音工房 (12)");
  assert.equal(menu.isConnected, false);
  assert.equal(document.activeElement, trigger);
});

test("voice actor search folds text, tolerates a typo and waits for IME composition", (context) => {
  context.after(() => closeDropdown(false, { immediate: true }));
  const { document, addSelect } = fixture();
  const viewport = Object.assign(new EventTarget(), {
    offsetLeft: 0, offsetTop: 0, width: 1000, height: 700,
  });
  document.defaultView.visualViewport = viewport;
  const voiceActor = addSelect("filter-va", "声优", [
    { label: "全部声优", value: "" },
    { label: "Mizuki Voice", value: "mizuki" },
    { label: "海音みずき", value: "mione" },
  ]);
  voiceActor.dataset.dropdownSearch = "";
  enhanceDropdowns(document.body, "");

  const trigger = document.body.querySelector("#filter-va-trigger");
  send(trigger, "click");
  const menu = document.body.querySelector(".dropdown-menu-searchable");
  assert.ok(menu, "expected the voice actor menu to offer search");
  const input = menu.querySelector(".dropdown-search");
  const listbox = menu.querySelector(".dropdown-results");

  viewport.height = 310;
  send(document.defaultView, "resize");
  send(viewport, "resize");
  assert.equal(trigger.getAttribute("aria-expanded"), "true");
  assert.equal(document.activeElement, input);
  assert.equal(menu.style.maxHeight, "206px");

  typeSearch(input, "ＭＩＺＵＫＩ");
  assert.deepEqual(visibleOptionLabels(listbox), ["Mizuki Voice"]);
  typeSearch(input, "mizki");
  assert.deepEqual(visibleOptionLabels(listbox), ["Mizuki Voice"]);

  send(input, "compositionstart");
  typeSearch(input, "みずき", { isComposing: true });
  assert.deepEqual(visibleOptionLabels(listbox), ["Mizuki Voice"]);
  assert.equal(send(input, "keydown", { key: "Enter", isComposing: true }).defaultPrevented, false);
  assert.equal(voiceActor.value, "");
  send(input, "compositionend");
  assert.deepEqual(visibleOptionLabels(listbox), ["海音みずき"]);

  assert.equal(send(input, "keydown", { key: "Escape" }).defaultPrevented, true);
  assert.equal(menu.isConnected, false);
  assert.equal(document.activeElement, trigger);
  assert.equal(voiceActor.value, "");
});

test("enhancement retains the native select and dispatches only actual value changes", (context) => {
  context.after(() => closeDropdown());
  const { document, addSelect } = fixture();
  const select = addSelect("filter-circle", "社团", [
    { label: "全部社团", value: "", count: 2 },
    { label: "海岸 & 雨", value: "海岸 & 雨", count: 17 },
    { label: "不可用", value: "disabled", disabled: true },
  ]);
  const changes = [];
  select.addEventListener("change", (event) => changes.push({ value: select.value, bubbles: event.bubbles }));

  enhanceDropdowns(document.body, "<svg></svg>");
  enhanceDropdowns(document.body, "<svg></svg>");
  const wrapper = document.body.querySelector(".dropdown");
  const trigger = wrapper.querySelector("#filter-circle-trigger");
  const caption = trigger.querySelector(".dropdown-caption");
  assert.deepEqual(wrapper.children, [select, trigger]);
  assert.equal(select.id, "filter-circle");
  assert.equal(caption.textContent, "全部社团 (2)");

  send(trigger, "click");
  const menu = document.body.querySelector(".dropdown-menu");
  const [, available, disabled] = menu.children;
  assert.equal(menu.getAttribute("role"), "listbox");
  assert.equal(available.querySelector(".dropdown-option-text").textContent, "海岸 & 雨");
  assert.equal(available.querySelector(".dropdown-option-count").textContent, "17");
  assert.equal(disabled.getAttribute("aria-disabled"), "true");
  send(menu, "click", { target: disabled });
  assert.equal(select.value, "");
  assert.equal(trigger.getAttribute("aria-expanded"), "true");
  assert.equal(changes.length, 0);

  send(menu, "click", { target: available.querySelector(".dropdown-option-text") });
  assert.equal(select.value, "海岸 & 雨");
  assert.equal(caption.textContent, "海岸 & 雨 (17)");
  assert.equal(menu.isConnected, false);
  assert.equal(document.activeElement, trigger);
  assert.deepEqual(changes, [{ value: "海岸 & 雨", bubbles: true }]);

  send(trigger, "click");
  const reopened = document.body.querySelector(".dropdown-menu");
  send(reopened, "click", { target: reopened.children[1] });
  assert.equal(changes.length, 1);
});

test("switching to all age ratings keeps the replacement circle menu enhanced after settlement", (context) => {
  context.after(() => closeDropdown());
  const { document, addSelect } = fixture();
  const first = addSelect("filter-circle", "社团", [{ label: "全部社团", value: "", count: 0 }]);
  first.className = "previous-select";
  enhanceDropdowns(document.body, "");

  const replacement = addSelect("filter-circle", "社团", [
    { label: "全部社团", value: "", count: 1 },
    { label: "RaRo", value: "RaRo", count: 332 },
  ]);
  // HTMX temporarily carries over the classes of elements with matching IDs.
  const incomingClass = replacement.className;
  replacement.className = first.className;
  first.parentElement.remove();
  enhanceDropdowns(document.body, "");
  replacement.className = incomingClass;

  const trigger = document.body.querySelector("#filter-circle-trigger");
  assert.ok(trigger);
  assert.equal(replacement.getAttribute("aria-hidden"), "true");
  send(trigger, "click");
  const menu = document.body.querySelector(".dropdown-menu");
  assert.equal(menu.children[1].querySelector(".dropdown-option-count").textContent, "332");
  send(menu, "click", { target: menu.children[1] });
  assert.equal(replacement.value, "RaRo");
});

test("opening another dropdown and Escape close the previous menu, and chapter changes retain the reset caption", (context) => {
  context.after(() => closeDropdown());
  const { document, addSelect } = fixture();
  addSelect("filter-tag", "标签", [{ label: "全部标签", value: "" }]);
  const chapter = addSelect("player-chapter", "跳转章节", [
    { label: "跳转章节", value: "" },
    { label: "第二段", value: "12" },
  ]);
  const positions = [];
  chapter.addEventListener("change", (event) => {
    positions.push(event.target.value);
    event.target.value = "";
  });
  enhanceDropdowns(document.body, "");
  const filterTrigger = document.body.querySelector("#filter-tag-trigger");
  const chapterTrigger = document.body.querySelector("#player-chapter-trigger");

  send(filterTrigger, "click");
  const initialMenu = document.body.querySelector(".dropdown-menu");
  send(chapterTrigger, "click");
  const chapterMenu = document.body.querySelector(".dropdown-menu");
  assert.equal(initialMenu.isConnected, false);
  assert.equal(filterTrigger.getAttribute("aria-expanded"), "false");
  assert.equal(document.activeElement, chapterMenu);
  assert.equal(send(chapterMenu, "keydown", { key: "Escape" }).defaultPrevented, true);
  assert.equal(chapterMenu.isConnected, false);
  assert.equal(document.activeElement, chapterTrigger);

  send(chapterTrigger, "click");
  const reopened = document.body.querySelector(".dropdown-menu");
  send(reopened, "keydown", { key: "ArrowDown" });
  assert.equal(reopened.getAttribute("aria-activedescendant"), reopened.children[1].id);
  send(reopened, "keydown", { key: "Enter" });
  assert.deepEqual(positions, ["12"]);
  assert.equal(chapter.value, "");
  assert.equal(chapterTrigger.querySelector(".dropdown-caption").textContent, "跳转章节");
});

test("a dismissed menu stops receiving events while its exit finishes", async (context) => {
  context.after(() => closeDropdown(false, { immediate: true }));
  const { document, addSelect } = fixture();
  const select = addSelect("filter-tag", "标签", [
    { label: "全部标签", value: "" },
    { label: "雨声", value: "rain" },
  ]);
  enhanceDropdowns(document.body, "");
  const trigger = document.body.querySelector("#filter-tag-trigger");
  send(trigger, "click");
  const menu = document.body.querySelector(".dropdown-menu");
  const { promise: finished, resolve } = Promise.withResolvers();
  menu.animations = [{ finished }];

  send(menu, "keydown", { key: "Escape" });
  assert.equal(menu.isConnected, true);
  assert.equal(menu.inert, true);
  assert.equal(trigger.getAttribute("aria-expanded"), "false");
  assert.equal(document.activeElement, trigger);
  send(menu, "click", { target: menu.children[1] });
  assert.equal(select.value, "");

  resolve();
  await Promise.allSettled([finished]);
  assert.equal(menu.isConnected, false);
});

test("a fading menu cannot outlive its anchor or affect a new dropdown", async (context) => {
  context.after(() => closeDropdown(false, { immediate: true }));
  const { document, addSelect } = fixture();
  addSelect("filter-tag", "标签", [{ label: "全部标签", value: "" }]);
  addSelect("filter-circle", "社团", [{ label: "全部社团", value: "" }]);
  enhanceDropdowns(document.body, "");
  const tagTrigger = document.body.querySelector("#filter-tag-trigger");
  const circleTrigger = document.body.querySelector("#filter-circle-trigger");
  send(tagTrigger, "click");
  const first = document.body.querySelector(".dropdown-menu");
  const { promise: finished, resolve } = Promise.withResolvers();
  first.animations = [{ finished }];
  send(tagTrigger, "click");
  assert.equal(first.isConnected, true);

  tagTrigger.parentElement.remove();
  enhanceDropdowns(document.body, "");
  assert.equal(first.isConnected, false);
  send(circleTrigger, "click");
  const second = document.body.querySelector(".dropdown-menu");
  resolve();
  await Promise.allSettled([finished]);
  assert.equal(second.isConnected, true);
  assert.equal(circleTrigger.getAttribute("aria-expanded"), "true");

  second.animations = [{ finished: new Promise(() => {}) }];
  send(document.defaultView, "resize");
  assert.equal(second.isConnected, false);
  assert.equal(circleTrigger.getAttribute("aria-expanded"), "false");
});
