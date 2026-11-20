import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { Script } from "node:vm";

const source = readFileSync(new URL("../src/app.js", import.meta.url), "utf8")
  .replaceAll("\r\n", "\n")
  .replace(/^import \{[^}]*\} from "\.\/(?:library|dropdowns)\.js";\n/gm, "")
  .replace(/\nstartApp\(\);\s*$/, "");
const navigationScript = new Script(`${source}
  render = recordRender;
  notify = failOnNotification;
  ({ state, syncSidebarPresentation, captureDrawerInputs, restoreDrawerInputs });
`, { filename: "web/src/app.js" });

function loadNavigation({
  preferences = new Map(),
  elements = new Map(),
  responses = new Map(),
  drawers = new Map(),
  pathname = "/",
  search = "",
} = {}) {
  const documentListeners = new Map();
  const fragmentRequests = [];
  const posts = [];
  let renderCount = 0;
  const drawerTarget = {
    id: "drawer-region",
    classList: { contains: () => false },
    querySelector: () => elements.get(".drawer") ?? null,
  };
  const navigation = navigationScript.runInNewContext({
    Audio: EventTarget,
    FormData,
    URL,
    URLSearchParams,
    closeDropdown: () => false,
    document: {
      get activeElement() { return elements.get(":focus") ?? null; },
      addEventListener: (event, callback) => documentListeners.set(event, callback),
      getElementById: (id) => elements.get(id),
      querySelector: (selector) => elements.get(selector) ?? null,
      querySelectorAll: () => [],
    },
    enhanceDropdowns() {},
    failOnNotification(message) { assert.fail(message); },
    async fetch(url, options) {
      posts.push({ url, method: options.method, body: JSON.parse(options.body) });
      return { ok: true, json: async () => responses.get(url) ?? {} };
    },
    handleLibraryAction: () => false,
    localStorage: {
      getItem: (key) => preferences.get(key) ?? null,
      setItem: (key, value) => preferences.set(key, value),
    },
    location: { pathname, search },
    recordRender() { renderCount++; },
    window: {
      addEventListener() {},
      matchMedia: () => Object.assign(new EventTarget(), { matches: true }),
      htmx: {
        async ajax(method, path, options) {
          const url = new URL(path, "http://otoha.test");
          fragmentRequests.push({ method, path, url, target: options.target, headers: options.headers });
          if (options.target !== "#drawer-region") return;

          const work = drawers.get(url.pathname);
          assert.ok(work, `Missing drawer fixture for ${url.pathname}`);
          const detail = {
            target: drawerTarget,
            requestConfig: { headers: options.headers },
            shouldSwap: true,
            xhr: { status: 200 },
          };
          documentListeners.get("htmx:beforeSwap")({ detail });
          if (!detail.shouldSwap) return;
          elements.set(".drawer", {
            dataset: { workno: work.workno, detail: JSON.stringify(work) },
            scrollTop: 0,
            contains: () => false,
            querySelectorAll: () => [],
            remove: () => elements.delete(".drawer"),
          });
          documentListeners.get("htmx:afterSwap")({ detail });
        },
      },
    },
  });
  const clickOn = (target, preventDefault = () => {}) => documentListeners.get("click")({ target, preventDefault });
  return {
    ...navigation,
    fragmentRequests,
    posts,
    get renderCount() { return renderCount; },
    clickOn,
    click(action, fields = {}) {
      const target = clickTarget("[data-action]");
      target.dataset = { action, ...fields };
      return clickOn(target);
    },
  };
}

function clickTarget(...matchingSelectors) {
  return {
    dataset: { action: "unhandled" },
    closest(selector) {
      return selector.split(",").some((part) => matchingSelectors.includes(part.trim())) ? this : null;
    },
  };
}

test("sidebar preferences update the mounted layout and survive a reload", async () => {
  const attributes = () => ({
    values: new Map(),
    setAttribute(name, value) { this.values.set(name, value); },
  });
  const preferences = new Map();
  const elements = new Map();
  const collapse = attributes();
  const visibility = {
    ...attributes(),
    focus(options) {
      assert.equal(options.preventScroll, true);
      elements.set(":focus", this);
    },
  };
  const classes = new Set();
  const layout = {
    classList: {
      toggle(name, enabled) {
        if (enabled) classes.add(name);
        else classes.delete(name);
      },
    },
  };
  const sidebar = {
    ...attributes(),
    inert: false,
    toggleAttribute(name, enabled) {
      assert.equal(name, "inert");
      this.inert = enabled;
    },
    contains: (target) => target === collapse,
    querySelector: (selector) => selector === ".sidebar-toggle" ? collapse : null,
    closest: (selector) => selector === ".layout" ? layout : null,
  };
  elements.set("library-sidebar", sidebar);
  elements.set(".sidebar-visibility-toggle", visibility);
  elements.set(":focus", collapse);

  const navigation = loadNavigation({ preferences, elements });
  await navigation.click("toggle-sidebar");
  assert.equal(classes.has("sidebar-collapsed"), true);
  assert.equal(collapse.values.get("aria-expanded"), "false");

  await navigation.click("toggle-sidebar-visibility");
  assert.equal(sidebar.inert, true);
  assert.equal(sidebar.values.get("aria-hidden"), "true");
  assert.equal(visibility.values.get("aria-expanded"), "false");
  assert.equal(elements.get(":focus"), visibility);

  const reloaded = loadNavigation({ preferences, elements });
  reloaded.syncSidebarPresentation();
  assert.equal(reloaded.state.sidebarHidden, true);
  assert.equal(reloaded.state.sidebarCollapsed, true);
  await reloaded.click("toggle-sidebar-visibility");
  assert.equal(sidebar.inert, false);
  assert.equal(sidebar.values.get("aria-hidden"), "false");
  assert.equal(visibility.values.get("aria-expanded"), "true");
  assert.equal(classes.has("sidebar-collapsed"), true);
  await reloaded.click("toggle-sidebar");
  assert.equal(classes.has("sidebar-collapsed"), false);
  assert.equal(collapse.values.get("aria-expanded"), "true");
  assert.equal(navigation.renderCount + reloaded.renderCount, 0);
});

test("local work links open the drawer fragment, and an external link preserves native navigation", async () => {
  const original = { workno: "RJ000002", title: "原作 <雨> & 海" };
  const workPath = `/ui/works/${original.workno}`;
  const navigation = loadNavigation({ drawers: new Map([[workPath, original]]) });
  await navigation.click("open-work", { workno: original.workno });
  assert.equal(navigation.state.detail.title, original.title);
  assert.equal(navigation.state.selectedWorkno, original.workno);
  assert.deepEqual(navigation.fragmentRequests.map(({ path }) => path), [workPath]);

  const link = clickTarget(".workspace", "a.dlsite-link", "a", "[data-action]");
  link.dataset = { action: "open-work", workno: "RJ000001" };
  await navigation.clickOn(link, () => assert.fail("native link navigation was intercepted"));
  assert.equal(navigation.state.detail.workno, original.workno);
  assert.equal(navigation.fragmentRequests.length, 1);
  assert.deepEqual(navigation.posts, []);
});

test("a drawer tag resets pagination and keeps the drawer while navigating from works or favorites", async () => {
  const name = "雨 & 睡眠";
  for (const tab of ["works", "favorites"]) {
    const navigation = loadNavigation({ search: "?page=4&view=table&source=asmrone" });
    const detail = { workno: "RJ000001", title: "海声", tags: [{ name, source: "user" }] };
    Object.assign(navigation.state, { tab, detail, selectedWorkno: detail.workno, browseQuery: "?view=table&source=asmrone" });
    const button = clickTarget(".workspace", ".drawer", "button", "[data-action]");
    button.dataset = { action: "filter-tag", val: name };

    await navigation.clickOn(button);

    const [request] = navigation.fragmentRequests;
    assert.equal(request.target, "#page-content");
    assert.equal(request.url.pathname, "/");
    assert.equal(request.url.searchParams.get("tag"), name);
    assert.equal(request.url.searchParams.get("source"), "asmrone");
    assert.equal(request.url.searchParams.get("view"), "table");
    assert.equal(request.url.searchParams.has("page"), false);
    assert.equal(request.headers["X-Otoha-Navigate"], "1");
    assert.equal(request.headers["X-Otoha-Keep-Drawer"], "1");
    assert.equal(navigation.state.detail, detail);
  }
});

test("drawer saves post explicit fields and refresh the same work with draft preservation", async () => {
  const workno = "RJ000001";
  const apiPath = `/api/works/${workno}`;
  const drawerPath = `/ui/works/${workno}`;
  const tags = [{ id: 2, name: "雨", source: "user" }, { id: 1, name: "助眠", source: "dlsite" }];
  const savedState = { progress: "listening", rating: 4, review: "已保存" };
  const elements = new Map([
    ["state-progress", { value: "listening" }],
    ["state-rating", { value: "4" }],
    ["state-review", { value: "笔记" }],
    ["user-tags-input", { value: " 雨，海,, " }],
  ]);
  const drawers = new Map([[drawerPath, { workno, state: savedState, progress: savedState.progress }]]);
  const navigation = loadNavigation({ elements, drawers });
  navigation.state.detail = { workno };
  navigation.state.selectedWorkno = workno;

  await navigation.click("save-state");
  assert.equal(navigation.state.detail.state.review, "已保存");
  assert.equal(navigation.state.detail.progress, "listening");

  drawers.set(drawerPath, { ...drawers.get(drawerPath), tags });
  await navigation.click("save-user-tags");
  assert.deepEqual(navigation.posts, [
    { url: `${apiPath}/state`, method: "POST", body: { progress: "listening", rating: 4, review: "笔记" } },
    { url: `${apiPath}/tags`, method: "POST", body: { tags: ["雨", "海"] } },
  ]);
  assert.equal(navigation.state.detail.tags[0].name, "雨");
  assert.deepEqual(navigation.fragmentRequests.map(({ path }) => path), [drawerPath, "/", drawerPath, "/"]);
  for (const request of navigation.fragmentRequests.filter(({ target }) => target === "#drawer-region")) {
    assert.equal(request.headers["X-Otoha-Preserve-Input"], "1");
  }
});

test("a late tag save cannot replace a newly opened drawer", async () => {
  const original = { workno: "RJ000001", tags: [] };
  const next = { workno: "RJ000002", tags: [{ name: "原有标签", source: "user" }] };
  const saved = Promise.withResolvers();
  const elements = new Map([["user-tags-input", { value: "新的标签" }]]);
  const responses = new Map([[`/api/works/${original.workno}/tags`, saved.promise]]);
  const drawers = new Map([[`/ui/works/${next.workno}`, next]]);
  const navigation = loadNavigation({ elements, responses, drawers });
  navigation.state.detail = original;
  navigation.state.selectedWorkno = original.workno;

  const saving = navigation.click("save-user-tags");
  assert.equal(navigation.posts.length, 1);
  await navigation.click("open-work", { workno: next.workno });
  saved.resolve({ tags: [{ name: "新的标签", source: "user" }] });
  await saving;

  assert.equal(navigation.state.detail.workno, next.workno);
  assert.equal(navigation.state.selectedWorkno, next.workno);
  assert.equal(navigation.state.detail.tags[0].name, "原有标签");
  assert.equal(navigation.fragmentRequests.filter(({ target }) => target === "#drawer-region").length, 1);
});

test("drawer redraw restores untouched drafts and focus while keeping a new saved value", () => {
  const review = {
    id: "state-review", tagName: "INPUT", value: "草稿文字", defaultValue: "旧短评",
    selectionStart: 1, selectionEnd: 3,
  };
  const progress = {
    id: "state-progress", tagName: "SELECT", value: "listening",
    querySelector: () => ({ value: "marked" }),
  };
  const workno = "RJ000001";
  const selection = [];
  let focused = false;
  const replacementReview = {
    value: review.defaultValue,
    focus() { focused = true; },
    setSelectionRange(...range) { selection.push(...range); },
  };
  const replacementProgress = { value: "listened" };
  const elements = new Map([
    [":focus", review],
    [review.id, replacementReview],
    [progress.id, replacementProgress],
  ]);
  const { state, captureDrawerInputs, restoreDrawerInputs } = loadNavigation({ elements });
  state.detail = { workno };
  const drawer = { dataset: { workno }, querySelectorAll: () => [review, progress] };

  restoreDrawerInputs(captureDrawerInputs(drawer));

  assert.equal(replacementReview.value, review.value);
  assert.equal(focused, true);
  assert.deepEqual(selection, [1, 3]);
  assert.equal(replacementProgress.value, "listened");
});

test("blank space closes the drawer while controls and drawer content keep it open", async () => {
  const { state, clickOn, fragmentRequests, posts } = loadNavigation();
  const detail = { workno: "RJ000001" };
  state.detail = detail;

  await clickOn(clickTarget(".workspace", ".search-box"));
  await clickOn(clickTarget(".workspace", ".drawer"));
  await clickOn(clickTarget(".workspace", "select"));
  await clickOn(clickTarget(".sidebar", "button"));
  await clickOn(clickTarget(".workspace", "[data-action]"));
  await clickOn(clickTarget(".player-bar"));
  assert.equal(state.detail, detail);
  assert.deepEqual(fragmentRequests, []);
  assert.deepEqual(posts, []);

  for (const shell of [".workspace", ".topbar", ".sidebar"]) {
    state.detail = detail;
    const target = shell === ".topbar" ? clickTarget(".workspace", shell) : clickTarget(shell);
    await clickOn(target);
    assert.equal(state.detail, null);
  }
});
