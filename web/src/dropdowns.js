const MENU_GAP = 6;
const VIEWPORT_MARGIN = 8;
const MENU_MAX_HEIGHT = 360;
const TYPEAHEAD_TIMEOUT = 600;

let openDropdown = null;

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
  const maxLeft = document.documentElement.clientWidth - menu.offsetWidth - VIEWPORT_MARGIN;
  const left = Math.max(VIEWPORT_MARGIN, Math.min(anchor.left, maxLeft));
  let bottom = window.innerHeight - VIEWPORT_MARGIN;
  const player = document.querySelector('#player-bar[aria-hidden="false"], #player-dock[aria-hidden="false"]');
  const playerRect = player?.getBoundingClientRect();
  if (playerRect && anchor.bottom <= playerRect.top &&
      left < playerRect.right && left + menu.offsetWidth > playerRect.left) {
    bottom = Math.min(bottom, playerRect.top);
  }

  const below = bottom - anchor.bottom - MENU_GAP;
  const above = anchor.top - VIEWPORT_MARGIN - MENU_GAP;
  const openAbove = below < MENU_MAX_HEIGHT && below < above;
  menu.style.maxHeight = `${Math.max(0, Math.min(MENU_MAX_HEIGHT, openAbove ? above : below))}px`;
  menu.style.left = `${left}px`;
  menu.style.top = `${openAbove ? anchor.top - MENU_GAP - menu.offsetHeight : anchor.bottom + MENU_GAP}px`;
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

function activateOption(dropdown, index) {
  const { menu, enabledOptions } = dropdown;
  const next = enabledOptions[index];
  if (!next) return;
  enabledOptions[dropdown.activeIndex]?.removeAttribute("data-active");
  dropdown.activeIndex = index;
  next.setAttribute("data-active", "");
  menu.setAttribute("aria-activedescendant", next.id);
  const top = next.offsetTop;
  const bottom = top + next.offsetHeight;
  if (top < menu.scrollTop) menu.scrollTop = top;
  else if (bottom > menu.scrollTop + menu.clientHeight) menu.scrollTop = bottom - menu.clientHeight;
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
  if (!item || item.getAttribute("aria-disabled") === "true") return;
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
      event.preventDefault();
      selectOption(dropdown, dropdown.enabledOptions[dropdown.activeIndex]);
      return;
    case "ArrowDown": index = dropdown.activeIndex + 1; break;
    case "ArrowUp": index = dropdown.activeIndex - 1; break;
    case "Home": index = 0; break;
    case "End": index = dropdown.enabledOptions.length - 1; break;
    default:
      if (Array.from(event.key).length !== 1) return;
      index = matchingOption(dropdown, event.key);
  }
  event.preventDefault();
  activateOption(dropdown, index);
}

/** Dismiss the open listbox and optionally return focus to its trigger. */
export function closeDropdown(restoreFocus = false) {
  if (!openDropdown) return false;
  const { menu, trigger, observer, listeners } = openDropdown;
  openDropdown = null;
  listeners.abort();
  observer?.disconnect();
  menu.remove();
  trigger.setAttribute("aria-expanded", "false");
  if (restoreFocus && trigger.isConnected) trigger.focus({ preventScroll: true });
  return true;
}

function showDropdown(control, preferLast = false) {
  closeDropdown();
  const { select, trigger, label, caption } = control;
  if (select.matches(":disabled")) return;
  const document = trigger.ownerDocument;
  const window = document.defaultView;
  const menu = document.createElement("div");
  menu.id = `${select.id}-menu`;
  menu.className = "dropdown-menu";
  menu.tabIndex = -1;
  menu.setAttribute("role", "listbox");
  menu.setAttribute("aria-label", label || caption.textContent);
  for (const option of select.options) {
    if (!option.hidden) menu.append(optionElement(menu, option, option.index === select.selectedIndex));
  }
  const enabledOptions = Array.from(menu.querySelectorAll('.dropdown-option:not([aria-disabled="true"])'));
  const listeners = new window.AbortController();
  const dropdown = {
    ...control, menu, listeners, enabledOptions, observer: null, activeIndex: -1,
    typedAt: -Infinity, typedText: "",
  };
  openDropdown = dropdown;
  document.body.append(menu);
  trigger.setAttribute("aria-expanded", "true");
  positionMenu(trigger, menu);
  const selectedIndex = enabledOptions.findIndex((item) => item.getAttribute("aria-selected") === "true");
  const fallbackIndex = preferLast ? enabledOptions.length - 1 : 0;
  activateOption(dropdown, selectedIndex >= 0 ? selectedIndex : fallbackIndex);

  if (window.IntersectionObserver) {
    dropdown.observer = new window.IntersectionObserver((entries) => {
      if (dropdown !== openDropdown) return;
      for (const entry of entries) syncOptionMotion(entry.target, entry.isIntersecting);
    }, { root: menu });
    for (const item of menu.children) dropdown.observer.observe(item);
  }

  const { signal } = listeners;
  menu.addEventListener("keydown", (event) => handleMenuKey(event, dropdown), { signal });
  menu.addEventListener("click", (event) => selectOption(dropdown, event.target.closest(".dropdown-option")), { signal });
  document.addEventListener("click", (event) => {
    const target = event.target;
    const label = target.closest?.("label");
    if (!menu.contains(target) && !trigger.contains(target) && label?.control !== trigger) closeDropdown();
  }, { capture: true, signal });
  document.addEventListener("scroll", (event) => {
    if (event.target === document || event.target.contains?.(trigger)) closeDropdown();
  }, { capture: true, signal });
  window.addEventListener("resize", () => closeDropdown(), { signal });
  menu.focus({ preventScroll: true });
}

/** Replace each single-select UI while retaining its native form control. */
export function enhanceDropdowns(root, arrowHTML) {
  if (openDropdown && !openDropdown.trigger.isConnected) closeDropdown();
  // A data attribute survives HTMX class settlement without being copied to new selects.
  for (const select of root.querySelectorAll("select[id]:not([multiple]):not([data-dropdown-source])")) {
    if (select.size > 1) continue;
    const document = select.ownerDocument;
    const labels = Array.from(select.labels ?? []);
    const associatedLabel = labels[0];
    const labelText = associatedLabel && Array.from(associatedLabel.childNodes)
      .filter((node) => node !== select).map((node) => node.textContent).join("").trim();
    const label = select.getAttribute("aria-label") || labelText || "";

    const wrapper = document.createElement("span");
    wrapper.className = "dropdown";
    const trigger = document.createElement("button");
    trigger.type = "button";
    trigger.id = `${select.id}-trigger`;
    trigger.className = "dropdown-trigger";
    trigger.dataset.dropdownFor = select.id;
    trigger.setAttribute("aria-haspopup", "listbox");
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

    const control = { select, trigger, caption, label };
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
