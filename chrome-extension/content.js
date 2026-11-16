/**
 * MonoAgent Bridge — Content Script
 *
 * Handles all DOM operations dispatched from the background service worker.
 * Maintains an element registry using WeakRef for GC-friendly element tracking.
 */

// ---------------------------------------------------------------------------
// Element registry
// ---------------------------------------------------------------------------

const elementRegistry = new Map(); // id -> WeakRef<Element>
let nextElementId = 1;

/**
 * Register a DOM element and return a stable string ID.
 */
function registerElement(el) {
  const id = `el_${nextElementId++}`;
  elementRegistry.set(id, new WeakRef(el));
  return id;
}

/**
 * Retrieve a previously registered element by ID.
 * Returns null if the element has been garbage-collected or was never registered.
 */
function getElement(id) {
  const ref = elementRegistry.get(id);
  if (!ref) return null;
  const el = ref.deref();
  if (!el) {
    elementRegistry.delete(id);
    return null;
  }
  return el;
}

/**
 * Periodically clean up stale entries where the WeakRef target has been collected.
 */
function cleanupRegistry() {
  for (const [id, ref] of elementRegistry) {
    if (!ref.deref()) {
      elementRegistry.delete(id);
    }
  }
}

setInterval(cleanupRegistry, 30000);

// ---------------------------------------------------------------------------
// Message listener
// ---------------------------------------------------------------------------

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  handleMessage(msg)
    .then((result) => sendResponse(result))
    .catch((err) => sendResponse({ error: err.message }));
  return true; // keep channel open for async response
});

// ---------------------------------------------------------------------------
// Command router
// ---------------------------------------------------------------------------

async function handleMessage(cmd) {
  const params = cmd.params || {};

  switch (cmd.type) {
    case "element":
      return findElement(params);
    case "elements":
      return findElements(params);
    case "has":
      return hasElement(params);
    case "click":
      return clickElement(params);
    case "input":
      return inputElement(params);
    case "insert_text":
      return insertText(params);
    case "text":
      return getElementText(params);
    case "attribute":
      return getElementAttribute(params);
    case "scroll":
      return scrollAction(params);
    case "keyboard_type":
      return keyboardType(params);
    case "keyboard_press":
      return keyboardPress(params);
    case "wait_element":
      return waitForElement(params);
    case "race":
      return raceElements(params);
    case "focus":
      return focusElement(params);
    case "html":
      return getElementHTML(params);
    case "property":
      return getElementProperty(params);
    case "scroll_into_view":
      return scrollIntoView(params);
    case "get_rect":
      return getRect(params);
    case "set_files":
      return setFiles(params);
    case "eval":
      return evalCode(params);
    case "query_count":
      return queryCount(params);
    case "query_text":
      return queryText(params);
    case "fetch_image_base64":
      return fetchImageBase64(params);
    default:
      throw new Error(`Unknown content command: ${cmd.type}`);
  }
}

// ---------------------------------------------------------------------------
// Element resolution helper
// ---------------------------------------------------------------------------

/**
 * Resolve an element from either an elementId (registry lookup) or a
 * selector/xpath (live DOM query). Throws if not found.
 */
function resolveElement({ elementId, selector, xpath }) {
  if (elementId) {
    const el = getElement(elementId);
    if (!el) throw new Error(`Element ${elementId} no longer exists in DOM`);
    return el;
  }
  if (xpath) {
    const result = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
    const el = result.singleNodeValue;
    if (!el) throw new Error(`Element not found for xpath: ${xpath}`);
    return el;
  }
  if (selector) {
    const el = document.querySelector(selector);
    if (!el) throw new Error(`Element not found for selector: ${selector}`);
    return el;
  }
  throw new Error("No elementId, selector, or xpath provided");
}

// ---------------------------------------------------------------------------
// Command implementations
// ---------------------------------------------------------------------------

/**
 * Find a single element by CSS selector or XPath with polling.
 * Returns { elementId } on success.
 */
async function findElement({ selector, xpath, timeout = 10000 }) {
  if (!selector && !xpath) throw new Error("selector or xpath is required");

  const deadline = Date.now() + timeout;

  while (Date.now() < deadline) {
    let el = null;
    if (xpath) {
      const result = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
      el = result.singleNodeValue;
    } else {
      el = document.querySelector(selector);
    }

    if (el) {
      return { elementId: registerElement(el) };
    }

    await sleep(200);
  }

  throw new Error(`Element not found within ${timeout}ms: ${selector || xpath}`);
}

/**
 * Find all elements matching a CSS selector or XPath.
 * Returns { elementIds: string[] }.
 */
async function findElements({ selector, xpath, timeout = 5000 }) {
  if (!selector && !xpath) throw new Error("selector or xpath is required");

  const deadline = Date.now() + timeout;

  while (Date.now() < deadline) {
    let els = [];
    if (xpath) {
      const result = document.evaluate(xpath, document, null, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
      for (let i = 0; i < result.snapshotLength; i++) {
        els.push(result.snapshotItem(i));
      }
    } else {
      els = Array.from(document.querySelectorAll(selector));
    }

    if (els.length > 0) {
      return { elementIds: els.map(registerElement) };
    }

    await sleep(200);
  }

  // Return empty array rather than throwing — caller can decide if 0 results is an error
  return { elementIds: [] };
}

/**
 * Check if an element exists in the DOM right now (no waiting).
 * Returns { exists: boolean, elementId?: string }.
 */
function hasElement({ selector, xpath }) {
  if (!selector && !xpath) throw new Error("selector or xpath is required");

  let el = null;
  if (xpath) {
    const result = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
    el = result.singleNodeValue;
  } else {
    el = document.querySelector(selector);
  }

  if (el) {
    return { exists: true, elementId: registerElement(el) };
  }
  return { exists: false };
}

/**
 * Click an element. Uses both dispatchEvent and el.click() for React/Angular compat.
 */
function clickElement({ elementId, selector, xpath }) {
  const el = resolveElement({ elementId, selector, xpath });

  // Scroll into view first so the click target is visible
  el.scrollIntoView({ block: "center", behavior: "instant" });

  // Dispatch full mouse event sequence for framework compatibility
  const rect = el.getBoundingClientRect();
  const x = rect.left + rect.width / 2;
  const y = rect.top + rect.height / 2;
  const eventInit = { bubbles: true, cancelable: true, clientX: x, clientY: y, view: window };

  el.dispatchEvent(new MouseEvent("mousedown", eventInit));
  el.dispatchEvent(new MouseEvent("mouseup", eventInit));

  // el.click() doesn't exist on SVG elements — use dispatchEvent fallback
  try {
    if (typeof el.click === 'function') {
      el.click();
    } else {
      el.dispatchEvent(new MouseEvent("click", eventInit));
    }
  } catch(e) {
    el.dispatchEvent(new MouseEvent("click", eventInit));
  }

  return { clicked: true };
}

/**
 * Input text into an element. Simulates character-by-character entry for
 * React synthetic event compatibility.
 */
async function inputElement({ elementId, selector, xpath, text, clearFirst = true, delay = 10 }) {
  const el = resolveElement({ elementId, selector, xpath });

  el.focus();
  el.dispatchEvent(new FocusEvent("focus", { bubbles: true }));

  const isContentEditable = el.isContentEditable;

  if (clearFirst) {
    if (isContentEditable) {
      // Select all text and delete it
      const sel = window.getSelection();
      sel.selectAllChildren(el);
      document.execCommand('delete', false);
    } else {
      el.value = "";
      el.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "deleteContentBackward" }));
    }
  }

  if (isContentEditable) {
    // For contenteditable (Instagram Lexical, etc.):
    // Method 1: Simulate paste via clipboard — Lexical handles paste events
    let pasted = false;
    try {
      el.focus();
      const dt = new DataTransfer();
      dt.setData('text/plain', text);
      const pasteEvent = new ClipboardEvent('paste', {
        bubbles: true, cancelable: true, clipboardData: dt,
      });
      el.dispatchEvent(pasteEvent);
      // Check if text appeared
      await sleep(200);
      if (el.textContent && el.textContent.trim().length > 0) {
        pasted = true;
      }
    } catch(e) {}

    // Method 2: execCommand insertText (works for Quill but not Lexical)
    if (!pasted) {
      document.execCommand('insertText', false, text);
      await sleep(200);
      if (el.textContent && el.textContent.trim().length > 0) {
        pasted = true;
      }
    }

    // Method 3: Direct DOM manipulation + input event
    if (!pasted) {
      // Set text content directly and notify via input event
      el.textContent = text;
      el.dispatchEvent(new InputEvent("input", {
        bubbles: true, inputType: "insertText", data: text,
      }));
    }
  } else {
    // Regular input/textarea
    for (const char of text) {
      el.value += char;
      el.dispatchEvent(new InputEvent("input", { bubbles: true, data: char, inputType: "insertText" }));
      if (delay > 0) await sleep(delay);
    }
    el.dispatchEvent(new Event("change", { bubbles: true }));
  }

  return { typed: true, length: text.length };
}

/**
 * Get the trimmed text content of an element.
 */
function getElementText({ elementId, selector, xpath }) {
  const el = resolveElement({ elementId, selector, xpath });
  return { text: (el.textContent || "").trim() };
}

/**
 * Get an attribute value from an element.
 */
function getElementAttribute({ elementId, selector, xpath, name }) {
  if (!name) throw new Error("attribute name is required");
  const el = resolveElement({ elementId, selector, xpath });
  return { value: el.getAttribute(name) };
}

/**
 * Get a JS property value from an element.
 */
function getElementProperty({ elementId, selector, xpath, name }) {
  if (!name) throw new Error("property name is required");
  const el = resolveElement({ elementId, selector, xpath });
  return { value: el[name] };
}

/**
 * Get innerHTML or outerHTML.
 */
function getElementHTML({ elementId, selector, xpath, outer = false }) {
  const el = resolveElement({ elementId, selector, xpath });
  return { html: outer ? el.outerHTML : el.innerHTML };
}

/**
 * Focus an element.
 */
function focusElement({ elementId, selector, xpath }) {
  const el = resolveElement({ elementId, selector, xpath });
  el.focus();
  el.dispatchEvent(new FocusEvent("focus", { bubbles: true }));
  return { focused: true };
}

/**
 * Scroll the page or an element.
 * If elementId/selector/xpath is given, scroll that element into view.
 * Otherwise, scroll the window by (x, y) pixels.
 */
function scrollAction({ elementId, selector, xpath, x = 0, y = 0, behavior = "smooth" }) {
  if (elementId || selector || xpath) {
    const el = resolveElement({ elementId, selector, xpath });
    el.scrollIntoView({ behavior, block: "center" });
    return { scrolled: true };
  }
  window.scrollBy({ left: x, top: y, behavior });
  return { scrolled: true, x, y };
}

/**
 * Scroll an element into view.
 */
function scrollIntoView({ elementId, selector, xpath, block = "center", behavior = "smooth" }) {
  const el = resolveElement({ elementId, selector, xpath });
  el.scrollIntoView({ block, behavior });
  return { scrolled: true };
}

/**
 * Type a string of text via keyboard events, character by character.
 * Dispatches keydown, keypress, input events per character for React compat.
 */
async function keyboardType({ elementId, selector, xpath, text, delay = 30 }) {
  if (!text) throw new Error("text is required");

  // If a target element is provided, focus it first
  let target = document.activeElement || document.body;
  if (elementId || selector || xpath) {
    target = resolveElement({ elementId, selector, xpath });
    target.focus();
  }

  for (const char of text) {
    const keyEventInit = {
      key: char,
      code: `Key${char.toUpperCase()}`,
      keyCode: char.charCodeAt(0),
      charCode: char.charCodeAt(0),
      which: char.charCodeAt(0),
      bubbles: true,
      cancelable: true,
    };

    target.dispatchEvent(new KeyboardEvent("keydown", keyEventInit));
    target.dispatchEvent(new KeyboardEvent("keypress", keyEventInit));

    // Update value if the target is an input/textarea
    if ("value" in target) {
      target.value += char;
    }

    target.dispatchEvent(new InputEvent("input", { bubbles: true, data: char, inputType: "insertText" }));
    target.dispatchEvent(new KeyboardEvent("keyup", keyEventInit));

    if (delay > 0) await sleep(delay);
  }

  return { typed: true, length: text.length };
}

/**
 * Press a single key (e.g., Enter, Tab, Escape, ArrowDown).
 */
function keyboardPress({ elementId, selector, xpath, key, code, keyCode }) {
  let target = document.activeElement || document.body;
  if (elementId || selector || xpath) {
    target = resolveElement({ elementId, selector, xpath });
    target.focus();
  }

  // Derive reasonable defaults from the key name
  const resolvedCode = code || keyNameToCode(key);
  const resolvedKeyCode = keyCode || keyNameToKeyCode(key);

  const keyEventInit = {
    key,
    code: resolvedCode,
    keyCode: resolvedKeyCode,
    which: resolvedKeyCode,
    bubbles: true,
    cancelable: true,
  };

  target.dispatchEvent(new KeyboardEvent("keydown", keyEventInit));
  target.dispatchEvent(new KeyboardEvent("keypress", keyEventInit));
  target.dispatchEvent(new KeyboardEvent("keyup", keyEventInit));

  return { pressed: true, key };
}

/**
 * Wait for an element to appear using polling + MutationObserver.
 * Returns { elementId } once found.
 */
function waitForElement({ selector, xpath, timeout = 10000 }) {
  if (!selector && !xpath) throw new Error("selector or xpath is required");

  return new Promise((resolve, reject) => {
    // Check immediately
    const immediate = queryElement(selector, xpath);
    if (immediate) {
      resolve({ elementId: registerElement(immediate) });
      return;
    }

    const timer = setTimeout(() => {
      observer.disconnect();
      reject(new Error(`Timed out waiting for element: ${selector || xpath}`));
    }, timeout);

    const observer = new MutationObserver(() => {
      const el = queryElement(selector, xpath);
      if (el) {
        observer.disconnect();
        clearTimeout(timer);
        resolve({ elementId: registerElement(el) });
      }
    });

    observer.observe(document.documentElement, {
      childList: true,
      subtree: true,
      attributes: true,
    });
  });
}

/**
 * Wait for the first of multiple selectors to appear.
 * Returns { index, selector, elementId } for the winning selector.
 */
function raceElements({ selectors, timeout = 10000 }) {
  if (!selectors || !Array.isArray(selectors) || selectors.length === 0) {
    throw new Error("selectors array is required and must be non-empty");
  }

  return new Promise((resolve, reject) => {
    // Check immediately
    for (let i = 0; i < selectors.length; i++) {
      const el = document.querySelector(selectors[i]);
      if (el) {
        resolve({ index: i, selector: selectors[i], elementId: registerElement(el) });
        return;
      }
    }

    const timer = setTimeout(() => {
      observer.disconnect();
      reject(new Error(`None of the selectors matched within ${timeout}ms`));
    }, timeout);

    const observer = new MutationObserver(() => {
      for (let i = 0; i < selectors.length; i++) {
        const el = document.querySelector(selectors[i]);
        if (el) {
          observer.disconnect();
          clearTimeout(timer);
          resolve({ index: i, selector: selectors[i], elementId: registerElement(el) });
          return;
        }
      }
    });

    observer.observe(document.documentElement, {
      childList: true,
      subtree: true,
      attributes: true,
    });
  });
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

/**
 * Query an element by CSS selector or XPath.
 */
function queryElement(selector, xpath) {
  if (xpath) {
    const result = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
    return result.singleNodeValue;
  }
  if (selector) {
    return document.querySelector(selector);
  }
  return null;
}

/**
 * Map common key names to DOM key codes.
 */
function keyNameToCode(key) {
  const map = {
    Enter: "Enter",
    Tab: "Tab",
    Escape: "Escape",
    Backspace: "Backspace",
    Delete: "Delete",
    ArrowUp: "ArrowUp",
    ArrowDown: "ArrowDown",
    ArrowLeft: "ArrowLeft",
    ArrowRight: "ArrowRight",
    Home: "Home",
    End: "End",
    PageUp: "PageUp",
    PageDown: "PageDown",
    Space: "Space",
    " ": "Space",
  };
  return map[key] || `Key${key.toUpperCase()}`;
}

function keyNameToKeyCode(key) {
  const map = {
    Enter: 13,
    Tab: 9,
    Escape: 27,
    Backspace: 8,
    Delete: 46,
    ArrowUp: 38,
    ArrowDown: 40,
    ArrowLeft: 37,
    ArrowRight: 39,
    Home: 36,
    End: 35,
    PageUp: 33,
    PageDown: 34,
    Space: 32,
    " ": 32,
  };
  return map[key] || key.toUpperCase().charCodeAt(0);
}

// ---------------------------------------------------------------------------
// Eval — execute JS code in the page context
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// DOM query commands — content scripts CAN read DOM in their isolated world
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Set files on a file input — for image/video upload
// ---------------------------------------------------------------------------

// Click `trigger` and take the file input the click would open the chooser
// for: a capture listener on the window cancels the chooser (preventDefault)
// and hands the input back. A page that opens its chooser from a detached
// input or showOpenFilePicker() never reaches the window, so it times out.
function openedFileInput(trigger, timeoutMs) {
  return new Promise((resolve, reject) => {
    let done = false;
    const finish = (fn, v) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      window.removeEventListener('click', onClick, true);
      fn(v);
    };
    const onClick = (e) => {
      const t = e.target;
      if (t instanceof HTMLInputElement && t.type === 'file') {
        e.preventDefault();
        finish(resolve, t);
      }
    };
    const timer = setTimeout(
      () => finish(reject, new Error('set_files: clicking it did not open a file chooser')),
      timeoutMs || 3000,
    );
    window.addEventListener('click', onClick, true);
    trigger.click();
  });
}

async function setFiles({ elementId, selector, xpath, fileData, viaClick, strict }) {
  let el;
  try {
    el = resolveElement({ elementId, selector, xpath });
  } catch (e) {
    // Nothing matched. The content script runs in the top document only (the
    // manifest has no all_frames), so an input inside an iframe lands here too.
    if (!/not found|no longer exists/.test(e.message)) throw e;
    throw new Error(
      'set_files: no element matched ' + (selector || xpath || elementId) +
      ' in this frame (file inputs inside iframes are not supported)',
    );
  }
  if (viaClick && el) el = await openedFileInput(el);
  if (!el || el.tagName !== 'INPUT' || el.type !== 'file') {
    throw new Error('set_files: the element matched is not a file input');
  }
  // `monoagentcli browser upload` is strict: a page that takes one file is not given two.
  if (strict && !el.multiple && fileData && fileData.length > 1) {
    throw new Error('set_files: this file input takes one file, not ' + fileData.length);
  }

  // fileData is an array of {name, data (base64), mimeType} sent from Go.
  if (!fileData || fileData.length === 0) {
    throw new Error('set_files: no file data provided');
  }

  const files = [];
  for (const fd of fileData) {
    const byteString = atob(fd.data);
    const ab = new ArrayBuffer(byteString.length);
    const ia = new Uint8Array(ab);
    for (let i = 0; i < byteString.length; i++) {
      ia[i] = byteString.charCodeAt(i);
    }
    const blob = new Blob([ab], { type: fd.mimeType || 'image/png' });
    const file = new File([blob], fd.name || 'image.png', { type: blob.type });
    files.push(file);
  }

  const dt = new DataTransfer();
  for (const file of files) {
    dt.items.add(file);
  }
  el.files = dt.files;
  el.dispatchEvent(new Event('change', { bubbles: true }));
  el.dispatchEvent(new Event('input', { bubbles: true }));

  return { filesSet: true, count: files.length };
}

// ---------------------------------------------------------------------------
// Get bounding rect of an element (for CDP click coordinates)
// ---------------------------------------------------------------------------

function getRect({ elementId }) {
  const el = getElement(elementId);
  if (!el) throw new Error("Element not found for get_rect");
  const r = el.getBoundingClientRect();
  return { x: r.x, y: r.y, width: r.width, height: r.height };
}

// ---------------------------------------------------------------------------
// Insert text — works with contenteditable (Quill, ProseMirror, etc.)
// ---------------------------------------------------------------------------

function insertText({ text, elementId }) {
  // Focus the target element if specified
  let target = null;
  if (elementId) {
    target = getElement(elementId);
    if (target) {
      target.focus();
      target.click();
    }
  }
  if (!target) {
    target = document.activeElement;
  }

  // Method 1: Simulate paste — works for Lexical (Instagram) and most editors
  if (target) {
    try {
      target.focus();
      const dt = new DataTransfer();
      dt.setData('text/plain', text);
      const pasteEvent = new ClipboardEvent('paste', {
        bubbles: true, cancelable: true, clipboardData: dt,
      });
      target.dispatchEvent(pasteEvent);
      return { inserted: true, length: text.length, method: 'paste' };
    } catch(e) {}
  }

  // Method 2: execCommand('insertText') — works with Quill/Gemini
  const success = document.execCommand('insertText', false, text);
  if (success) {
    return { inserted: true, length: text.length, method: 'execCommand' };
  }

  // Method 3: For textareas and inputs, set .value directly
  if (target && (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT')) {
    target.value = text;
    target.dispatchEvent(new InputEvent('input', { bubbles: true, data: text, inputType: 'insertText' }));
    target.dispatchEvent(new Event('change', { bubbles: true }));
    return { inserted: true, length: text.length, method: 'value' };
  }

  // Method 4: Direct textContent + input event
  if (target && target.isContentEditable) {
    target.textContent = text;
    target.dispatchEvent(new InputEvent('input', { bubbles: true, data: text, inputType: 'insertText' }));
    return { inserted: true, length: text.length, method: 'textContent' };
  }

  return { inserted: false, error: 'No active element' };
}

// ---------------------------------------------------------------------------
// Fetch image as base64 — for downloading generated images from Gemini
// Works by drawing <img> to canvas (same-origin) or fetching blob URLs
// ---------------------------------------------------------------------------

async function fetchImageBase64({ selector }) {
  const imgs = document.querySelectorAll(selector || 'img');
  const results = [];
  for (const img of imgs) {
    const w = img.naturalWidth || img.width || 0;
    const h = img.naturalHeight || img.height || 0;
    if (w < 48 || h < 48) continue;
    const src = img.src || '';
    if (!src) continue;

    try {
      // Try canvas approach (works for same-origin and CORS-enabled images)
      const canvas = document.createElement('canvas');
      canvas.width = img.naturalWidth;
      canvas.height = img.naturalHeight;
      const ctx = canvas.getContext('2d');
      ctx.drawImage(img, 0, 0);
      const dataURL = canvas.toDataURL('image/png');
      if (dataURL && dataURL.length > 200) {
        results.push({ data: dataURL.split(',')[1], width: canvas.width, height: canvas.height });
        continue;
      }
    } catch(e) {
      // Canvas tainted by CORS — try fetch
    }

    // Fetch blob/data URLs
    if (src.startsWith('blob:') || src.startsWith('data:image')) {
      try {
        const resp = await fetch(src);
        const blob = await resp.blob();
        const reader = new FileReader();
        const b64 = await new Promise((resolve, reject) => {
          reader.onloadend = () => resolve(reader.result.split(',')[1]);
          reader.onerror = reject;
          reader.readAsDataURL(blob);
        });
        if (b64 && b64.length > 100) {
          results.push({ data: b64, width: w, height: h });
        }
      } catch(e) {}
    }
  }
  return { result: results };
}

// Deep query that traverses shadow DOM boundaries
function deepQueryAll(root, selector) {
  const results = [...root.querySelectorAll(selector)];
  // Also search inside shadow roots
  const allElements = root.querySelectorAll('*');
  for (const el of allElements) {
    if (el.shadowRoot) {
      results.push(...deepQueryAll(el.shadowRoot, selector));
    }
  }
  return results;
}

function queryCount({ selector }) {
  return { result: deepQueryAll(document, selector).length };
}

function queryText({ selector, index }) {
  const els = deepQueryAll(document, selector);
  const idx = (index !== undefined && index !== null) ? index : els.length - 1;
  if (idx < 0 || idx >= els.length) return { result: "" };
  return { result: (els[idx].textContent || "").trim() };
}

async function evalCode({ code, args }) {
  if (!code) throw new Error("eval: code is required");

  // Content script runs in an ISOLATED world — its `window` is separate from
  // the page's `window`. To execute arbitrary JS in the page's MAIN world, we
  // inject a <script> tag and communicate the result back via a CustomEvent
  // on the shared DOM.
  const resultKey = '__monoagent_' + Date.now() + '_' + Math.random().toString(36).slice(2);
  const argsJSON = JSON.stringify(args || []);

  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      document.removeEventListener('monoagent-eval-done', handler);
      reject(new Error('Eval timeout: 30s'));
    }, 30000);

    function handler(e) {
      let data;
      try { data = JSON.parse(e.detail); } catch { return; }
      if (data.key !== resultKey) return;

      document.removeEventListener('monoagent-eval-done', handler);
      clearTimeout(timeout);

      if (data.error) {
        reject(new Error('Eval error: ' + data.error));
      } else {
        resolve({ result: data.value });
      }
    }

    document.addEventListener('monoagent-eval-done', handler);

    const script = document.createElement('script');
    script.textContent = `
      (async function() {
        let __val, __err;
        try {
          const __fn = (${code});
          __val = typeof __fn === 'function' ? __fn(...${argsJSON}) : __fn;
          if (__val && typeof __val.then === 'function') __val = await __val;
        } catch(e) {
          __err = e.message;
        }
        document.dispatchEvent(new CustomEvent('monoagent-eval-done', {
          detail: JSON.stringify({
            key: '${resultKey}',
            value: __val === undefined ? null : __val,
            error: __err || null
          })
        }));
      })();
    `;
    document.documentElement.appendChild(script);
    script.remove();
  });
}
