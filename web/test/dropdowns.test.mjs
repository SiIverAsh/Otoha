import assert from "node:assert/strict";
import test from "node:test";
import { closeDropdown, enhanceDropdowns } from "../src/dropdowns.js";

class TestElement extends EventTarget {
  children = [];
  parentElement = null;
  className = "";
  id = "";
  dataset = {};
  attributes = new Map();
  style = {};
  offsetWidth = 250;
  offsetHeight = 120;
  offsetTop = 0;
  clientHeight = 120;
  scrollTop = 0;

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
