const MENU_GAP = 6;
const VIEWPORT_MARGIN = 8;
const MENU_MAX_HEIGHT = 360;
const TYPEAHEAD_TIMEOUT = 600;
const MIN_TYPO_LENGTH = 4;
const LATIN_WORDS = /[\p{Script=Latin}\p{Mark}]+/gu;
const LATIN_TERM = /^[\p{Script=Latin}\p{Mark}]+$/u;

let openDropdown = null;
let closingDropdown = null;

function syncCaption(select, trigger, caption, label) {
  const option = select.selectedOptions[0];
  const count = option?.dataset.count;
  const selection = count === undefined ? option?.label ?? "" : `${option.label} (${count})`;
  caption.textContent = selection;
  trigger.title = selection;
  trigger.setAttribute("aria-label", label && selection ? `${label}，${selection}` : label || selection);
  trigger.disabled = select.matches(":disabled");
}

function optionElement(menu, option, selected) {
  const document = menu.ownerDocument;
  const item = document.createElement("div");
  item.className = "dropdown-option";
  item.id = `${menu.id}-option-${option.index}`;
  item.title = option.label;
  item.dataset.index = String(option.index);
  item.setAttribute("role", "option");
  item.setAttribute("aria-selected", String(selected));
  if (option.matches(":disabled")) item.setAttribute("aria-disabled", "true");

  const label = document.createElement("span");
  label.className = "dropdown-option-label";
  const text = document.createElement("span");
  text.className = "dropdown-option-text";
  text.textContent = option.label;
  label.append(text);
  item.append(label);
  if (option.dataset.count !== undefined) {
    const count = document.createElement("span");
    count.className = "dropdown-option-count";
    count.textContent = option.dataset.count;
    item.append(count);
  }
  return item;
}

function positionMenu(trigger, menu) {
  const document = trigger.ownerDocument;
  const window = document.defaultView;
  const anchor = trigger.getBoundingClientRect();
  const viewport = window.visualViewport;
  const viewportLeft = viewport?.offsetLeft ?? 0;
  const viewportTop = viewport?.offsetTop ?? 0;
  const viewportWidth = viewport?.width ?? document.documentElement.clientWidth;
  const viewportHeight = viewport?.height ?? window.innerHeight;
  const maxLeft = viewportLeft + viewportWidth - menu.offsetWidth - VIEWPORT_MARGIN;
  const left = Math.max(viewportLeft + VIEWPORT_MARGIN, Math.min(anchor.left, maxLeft));
  let bottom = viewportTop + viewportHeight - VIEWPORT_MARGIN;
  const player = document.querySelector('#player-bar[aria-hidden="false"], #player-dock[aria-hidden="false"]');
  const playerRect = player?.getBoundingClientRect();
  if (playerRect && anchor.bottom <= playerRect.top &&
      left < playerRect.right && left + menu.offsetWidth > playerRect.left) {
    bottom = Math.min(bottom, playerRect.top);
  }

  const below = bottom - anchor.bottom - MENU_GAP;
  const above = anchor.top - viewportTop - VIEWPORT_MARGIN - MENU_GAP;
  const openAbove = below < MENU_MAX_HEIGHT && below < above;
  menu.dataset.side = openAbove ? "above" : "below";
  menu.style.maxHeight = `${Math.max(0, Math.min(MENU_MAX_HEIGHT, openAbove ? above : below))}px`;
  menu.style.left = `${left}px`;
  const height = menu.offsetHeight;
  menu.style.top = `${openAbove ? anchor.top - MENU_GAP - height : anchor.bottom + MENU_GAP}px`;
}

function syncOptionMotion(item, visible) {
  const text = item.querySelector(".dropdown-option-text");
  const overflow = visible ? text.scrollWidth - text.parentElement.clientWidth : 0;
  text.classList.toggle("is-scrolling", overflow > 0);
  if (overflow > 0) {
    text.style.setProperty("--dropdown-scroll-offset", `-${overflow}px`);
    text.style.setProperty("--dropdown-scroll-duration", `${Math.max(3, overflow / 32 + 1.5)}s`);
  }
}

function normalizeName(name) {
  return name.normalize("NFKC").toLowerCase();
}

function isWithinOneEdit(term, word) {
  const lengthDifference = word.length - term.length;
  if (Math.abs(lengthDifference) > 1) return false;
  const overlap = Math.min(term.length, word.length);
  let index = 0;
  while (index < overlap && term[index] === word[index]) index++;
  // Skip one character on the longer side, or on both sides for a substitution.
  const termSuffix = lengthDifference <= 0 ? index + 1 : index;
  const wordSuffix = lengthDifference >= 0 ? index + 1 : index;
  return term.slice(termSuffix) === word.slice(wordSuffix);
}

function createSearchInput(menu, listbox, label) {
  const document = menu.ownerDocument;
  listbox.id = `${menu.id}-results`;
  listbox.className = "dropdown-results";
  listbox.setAttribute("role", "listbox");
  listbox.setAttribute("aria-label", label);

  const input = document.createElement("input");
  input.className = "dropdown-search";
  input.type = "search";
  input.autocomplete = "off";
  input.spellcheck = false;
  input.placeholder = `搜索${label}`;
  input.setAttribute("role", "combobox");
  input.setAttribute("aria-label", input.placeholder);
  input.setAttribute("aria-controls", listbox.id);
  input.setAttribute("aria-autocomplete", "list");
  input.setAttribute("aria-expanded", "true");

  const empty = document.createElement("div");
  empty.className = "dropdown-no-results";
  empty.setAttribute("role", "status");
  empty.textContent = `没有匹配的${label}`;
  empty.hidden = true;

  menu.append(input, listbox, empty);
  return { input, empty, options: [], keyword: "", isComposing: false };
}

function filterDropdown(dropdown) {
  const { search, listbox, menu, trigger } = dropdown;
  const keyword = normalizeName(search.input.value).trim();
  if (keyword === search.keyword) return;
  search.keyword = keyword;
  const terms = keyword ? keyword.split(/\s+/u).map((term) => ({
    term,
    allowsTypo: term.length >= MIN_TYPO_LENGTH && LATIN_TERM.test(term),
  })) : [];
  dropdown.enabledOptions[dropdown.activeIndex]?.removeAttribute("data-active");
  dropdown.activeIndex = -1;
  search.input.removeAttribute("aria-activedescendant");
  dropdown.enabledOptions = [];

  let hasMatches = false;
  for (const { item, name, words, isReset } of search.options) {
    const isMatch = terms.length === 0 || (!isReset && terms.every(({ term, allowsTypo }) =>
      name.includes(term) || (allowsTypo && words.some((word) => isWithinOneEdit(term, word)))));
    const isHidden = !isMatch;
    if (item.hidden !== isHidden) item.hidden = isHidden;
    if (!isMatch) continue;
    hasMatches = true;
    if (item.getAttribute("aria-disabled") !== "true") dropdown.enabledOptions.push(item);
  }
  search.empty.hidden = hasMatches;
  listbox.scrollTop = 0;
  positionMenu(trigger, menu);

  const selected = keyword ? -1 : dropdown.enabledOptions.findIndex((item) => item.getAttribute("aria-selected") === "true");
  activateOption(dropdown, Math.max(0, selected));
}

function activateOption(dropdown, index) {
  const { listbox, enabledOptions, focusTarget } = dropdown;
  const next = enabledOptions[index];
  if (!next) return;
  enabledOptions[dropdown.activeIndex]?.removeAttribute("data-active");
  dropdown.activeIndex = index;
  next.setAttribute("data-active", "");
  focusTarget.setAttribute("aria-activedescendant", next.id);
  const top = next.offsetTop;
  const bottom = top + next.offsetHeight;
  if (top < listbox.scrollTop) listbox.scrollTop = top;
  else if (bottom > listbox.scrollTop + listbox.clientHeight) listbox.scrollTop = bottom - listbox.clientHeight;
}

function matchingOption(dropdown, key) {
  const now = performance.now();
  const prefix = now - dropdown.typedAt < TYPEAHEAD_TIMEOUT ? dropdown.typedText : "";
  const lowerKey = key.toLocaleLowerCase();
  const repeated = prefix === lowerKey;
  dropdown.typedText = repeated ? lowerKey : prefix + lowerKey;
  dropdown.typedAt = now;
  const { enabledOptions, activeIndex } = dropdown;
  const from = Math.max(0, activeIndex + (prefix && !repeated ? 0 : 1));
  for (let offset = 0; offset < enabledOptions.length; offset++) {
    const index = (from + offset) % enabledOptions.length;
    const text = enabledOptions[index].querySelector(".dropdown-option-text");
    if (text.textContent.toLocaleLowerCase().startsWith(dropdown.typedText)) return index;
  }
  return -1;
}

function selectOption(dropdown, item) {
  if (!item || item.hidden || item.getAttribute("aria-disabled") === "true") return;
  const { select, trigger, caption, label } = dropdown;
  const previousValue = select.value;
  select.selectedIndex = Number(item.dataset.index);
  closeDropdown(true);
  if (select.value !== previousValue) {
    select.dispatchEvent(new Event("change", { bubbles: true }));
  }
  syncCaption(select, trigger, caption, label);
}

function handleMenuKey(event, dropdown) {
  if (dropdown.search?.isComposing) {
    event.stopPropagation();
    return;
  }
  if (event.isComposing || event.ctrlKey || event.metaKey || event.altKey) return;
  let index;
  switch (event.key) {
    case "Tab":
      closeDropdown(true);
      return;
    case "Escape":
      event.preventDefault();
      closeDropdown(true);
      return;
    case "Enter":
    case " ":
      if (dropdown.search && event.key === " ") return;
      event.preventDefault();
      selectOption(dropdown, dropdown.enabledOptions[dropdown.activeIndex]);
      return;
    case "ArrowDown": index = dropdown.activeIndex + 1; break;
    case "ArrowUp": index = dropdown.activeIndex - 1; break;
    case "Home":
    case "End":
      if (dropdown.search) return;
      index = event.key === "Home" ? 0 : dropdown.enabledOptions.length - 1;
      break;
    default:
      if (dropdown.search || Array.from(event.key).length !== 1) return;
      index = matchingOption(dropdown, event.key);
  }
  event.preventDefault();
  activateOption(dropdown, index);
}

function removeClosingDropdown() {
  closingDropdown?.menu.remove();
  closingDropdown = null;
}

/** Dismiss the listbox, optionally restoring focus or bypassing the exit. */
export function closeDropdown(restoreFocus = false, { immediate = false } = {}) {
  if (immediate) removeClosingDropdown();
  if (!openDropdown) return false;
  const dropdown = openDropdown;
  const { menu, trigger, observer, listeners } = dropdown;
  openDropdown = null;
  listeners.abort();
  observer?.disconnect();
  trigger.setAttribute("aria-expanded", "false");
  if (restoreFocus && trigger.isConnected) trigger.focus({ preventScroll: true });
  if (immediate || !trigger.isConnected) {
    menu.remove();
    return true;
  }

  menu.inert = true;
  delete menu.dataset.open;
  const animations = menu.getAnimations();
  if (animations.length === 0) {
    menu.remove();
    return true;
  }
  closingDropdown = dropdown;
  void Promise.allSettled(animations.map((animation) => animation.finished)).then(() => {
    if (closingDropdown === dropdown) removeClosingDropdown();
  });
  return true;
}

function showDropdown(control, preferLast = false) {
  closeDropdown(false, { immediate: true });
  const { select, trigger, label, caption, isSearchable } = control;
  if (select.matches(":disabled")) return;
  const document = trigger.ownerDocument;
  const window = document.defaultView;
  const menu = document.createElement("div");
  menu.id = `${select.id}-menu`;
  menu.className = isSearchable ? "dropdown-menu dropdown-menu-searchable" : "dropdown-menu";
  menu.tabIndex = -1;
  menu.setAttribute("role", isSearchable ? "dialog" : "listbox");
  menu.setAttribute("aria-label", label || caption.textContent);
  const listbox = isSearchable ? document.createElement("div") : menu;
  const search = isSearchable ? createSearchInput(menu, listbox, label) : null;
  const focusTarget = search?.input ?? menu;
  for (const option of select.options) {
    if (option.hidden) continue;
    const item = optionElement(menu, option, option.index === select.selectedIndex);
    listbox.append(item);
    if (search) {
      const name = normalizeName(option.label);
      search.options.push({ item, name, words: name.match(LATIN_WORDS) ?? [], isReset: option.value === "" });
    }
  }
  const enabledOptions = Array.from(listbox.querySelectorAll('.dropdown-option:not([aria-disabled="true"])'));
  const listeners = new window.AbortController();
  const dropdown = {
    ...control, menu, listbox, search, focusTarget, listeners, enabledOptions, observer: null, activeIndex: -1,
    typedAt: -Infinity, typedText: "",
  };
  openDropdown = dropdown;
  document.body.append(menu);
  trigger.setAttribute("aria-expanded", "true");
  positionMenu(trigger, menu);
  menu.dataset.open = "";
  const selectedIndex = enabledOptions.findIndex((item) => item.getAttribute("aria-selected") === "true");
  const fallbackIndex = preferLast ? enabledOptions.length - 1 : 0;
  activateOption(dropdown, selectedIndex >= 0 ? selectedIndex : fallbackIndex);

  if (window.IntersectionObserver) {
    dropdown.observer = new window.IntersectionObserver((entries) => {
      if (dropdown !== openDropdown) return;
      for (const entry of entries) syncOptionMotion(entry.target, entry.isIntersecting);
    }, { root: listbox });
    for (const item of listbox.children) dropdown.observer.observe(item);
  }

  const { signal } = listeners;
  focusTarget.addEventListener("keydown", (event) => handleMenuKey(event, dropdown), { signal });
  if (search) {
    search.input.addEventListener("compositionstart", () => { search.isComposing = true; }, { signal });
    search.input.addEventListener("compositionend", () => {
      search.isComposing = false;
      filterDropdown(dropdown);
    }, { signal });
    search.input.addEventListener("input", (event) => {
      if (!search.isComposing && !event.isComposing) filterDropdown(dropdown);
    }, { signal });
    menu.addEventListener("focusout", (event) => {
      if (event.relatedTarget && !menu.contains(event.relatedTarget)) closeDropdown();
    }, { signal });
  }
  menu.addEventListener("click", (event) => selectOption(dropdown, event.target.closest(".dropdown-option")), { signal });
  document.addEventListener("click", (event) => {
    const target = event.target;
    const label = target.closest?.("label");
    if (!menu.contains(target) && !trigger.contains(target) && label?.control !== trigger) closeDropdown();
  }, { capture: true, signal });
  document.addEventListener("scroll", (event) => {
    if (event.target === document || event.target.contains?.(trigger)) closeDropdown(false, { immediate: true });
  }, { capture: true, signal });
  if (search) {
    // A touch keyboard can resize and pan the viewport while the input keeps focus.
    const reposition = () => positionMenu(trigger, menu);
    window.addEventListener("resize", reposition, { signal });
    window.visualViewport?.addEventListener("resize", reposition, { signal });
    window.visualViewport?.addEventListener("scroll", reposition, { signal });
  } else {
    window.addEventListener("resize", () => closeDropdown(false, { immediate: true }), { signal });
  }
  focusTarget.focus({ preventScroll: true });
}

/** Replace each single-select UI while retaining its native form control. */
export function enhanceDropdowns(root, arrowHTML) {
  if (openDropdown && !openDropdown.trigger.isConnected) closeDropdown(false, { immediate: true });
  if (closingDropdown && !closingDropdown.trigger.isConnected) removeClosingDropdown();
  // A data attribute survives HTMX class settlement without being copied to new selects.
  for (const select of root.querySelectorAll("select[id]:not([multiple]):not([data-dropdown-source])")) {
    if (select.size > 1) continue;
    const document = select.ownerDocument;
    const labels = Array.from(select.labels ?? []);
    const associatedLabel = labels[0];
    const labelText = associatedLabel && Array.from(associatedLabel.childNodes)
      .filter((node) => node !== select).map((node) => node.textContent).join("").trim();
    const label = select.getAttribute("aria-label") || labelText || "";
    const isSearchable = Object.hasOwn(select.dataset, "dropdownSearch");

    const wrapper = document.createElement("span");
    wrapper.className = "dropdown";
    const trigger = document.createElement("button");
    trigger.type = "button";
    trigger.id = `${select.id}-trigger`;
    trigger.className = "dropdown-trigger";
    trigger.dataset.dropdownFor = select.id;
    trigger.setAttribute("aria-haspopup", isSearchable ? "dialog" : "listbox");
    trigger.setAttribute("aria-controls", `${select.id}-menu`);
    trigger.setAttribute("aria-expanded", "false");
    const caption = document.createElement("span");
    caption.className = "dropdown-caption";
    trigger.append(caption);
    trigger.insertAdjacentHTML("beforeend", arrowHTML);
    select.dataset.dropdownSource = "";
    select.tabIndex = -1;
    select.setAttribute("aria-hidden", "true");
    select.before(wrapper);
    wrapper.append(select, trigger);
    for (const associated of labels) {
      if (associated.htmlFor === select.id) associated.htmlFor = trigger.id;
    }

    const control = { select, trigger, caption, label, isSearchable };
    const updateCaption = () => syncCaption(select, trigger, caption, label);
    updateCaption();
    select.addEventListener("change", updateCaption);
    trigger.addEventListener("click", () => {
      if (openDropdown?.trigger === trigger) closeDropdown(true);
      else showDropdown(control);
    });
    trigger.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
      event.preventDefault();
      showDropdown(control, event.key === "ArrowUp");
    });
  }
}
