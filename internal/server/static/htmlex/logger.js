// src/logger.js
/**
 * @module Logger
 * @description Provides logging with configurable levels, a global on/off switch,
 * and separate namespaces for system logs and element-scoped logs.
 */

/** @enum {string} */
export const LogLevel = {
  DEBUG: 'debug',
  INFO: 'info',
  WARN: 'warn',
  ERROR: 'error'
};

const MAX_LOG_ENTRIES = 250;
const MAX_DIAGNOSTIC_DEPTH = 4;
const MAX_DIAGNOSTIC_ARRAY_ITEMS = 50;
const MAX_DIAGNOSTIC_OBJECT_KEYS = 50;
const LOG_EVENT_NAME = 'htmlex:log';
const DIAGNOSTICS_GLOBAL = '__HTMLEX_DIAGNOSTICS__';
let logEntrySequence = 0;

function safeString(value, fallback = '[Unstringifiable]') {
  try {
    return String(value ?? fallback);
  } catch {
    return fallback;
  }
}

function getObjectField(value, fieldName, fallback = undefined) {
  try {
    return value?.[fieldName] ?? fallback;
  } catch {
    return fallback;
  }
}

function setObjectField(value, fieldName, fieldValue) {
  try {
    if (value && typeof value === 'object') {
      value[fieldName] = fieldValue;
      return true;
    }
  } catch {
    // Diagnostics and startup logging configuration are best-effort.
  }
  return false;
}

function isKnownLogLevel(value) {
  return (
    value === LogLevel.DEBUG ||
    value === LogLevel.INFO ||
    value === LogLevel.WARN ||
    value === LogLevel.ERROR
  );
}

function getWindow() {
  try {
    return typeof window !== 'undefined' ? window : null;
  } catch {
    return null;
  }
}

function readStoredLogLevel(runtimeWindow) {
  try {
    return getObjectField(runtimeWindow, 'localStorage', null)?.getItem?.('HTMLEX_LOG_LEVEL') ?? null;
  } catch {
    return null;
  }
}

function readLocationSearch(runtimeWindow) {
  try {
    return getObjectField(getObjectField(runtimeWindow, 'location', null), 'search', '');
  } catch {
    return '';
  }
}

function decodeSearchPart(value) {
  try {
    return decodeURIComponent(safeString(value).replace(/\+/g, ' '));
  } catch {
    return safeString(value);
  }
}

function searchHasDebugFlag(search) {
  const searchText = safeString(search, '');
  try {
    if (typeof globalThis.URLSearchParams === 'function') {
      const params = new globalThis.URLSearchParams(searchText);
      if (params.get('htmlexDebug') === '1') return true;
    }
  } catch {
    // Fall back to manual parsing below.
  }

  const queryText = searchText.startsWith('?') ? searchText.slice(1) : searchText;
  if (!queryText) return false;

  for (const pair of queryText.split('&')) {
    const separatorIndex = pair.indexOf('=');
    const name = decodeSearchPart(separatorIndex >= 0 ? pair.slice(0, separatorIndex) : pair);
    const value = decodeSearchPart(separatorIndex >= 0 ? pair.slice(separatorIndex + 1) : '');
    if (name === 'htmlexDebug' && value === '1') return true;
  }
  return false;
}

function getInitialLogLevel() {
  const runtimeWindow = getWindow();

  const normalizedConfigured = safeString(readStoredLogLevel(runtimeWindow), '').trim().toLowerCase();
  if (isKnownLogLevel(normalizedConfigured)) return normalizedConfigured;

  try {
    if (searchHasDebugFlag(readLocationSearch(runtimeWindow))) return LogLevel.DEBUG;
  } catch {
    // Logging configuration should never block app startup.
  }

  return LogLevel.WARN;
}

function getDiagnosticsStoreField(runtimeWindow) {
  return getObjectField(runtimeWindow, DIAGNOSTICS_GLOBAL, null);
}

function setDiagnosticsStoreField(runtimeWindow, store) {
  return setObjectField(runtimeWindow, DIAGNOSTICS_GLOBAL, store);
}

function isDiagnosticsStore(value) {
  if (!value || typeof value !== 'object') return false;
  return Array.isArray(getObjectField(value, 'entries', null));
}

function ensureDiagnosticsClear(store) {
  if (typeof getObjectField(store, 'clear') === 'function') return;
  setObjectField(store, 'clear', clearDiagnosticsStore);
}

function getDiagnosticsEntries(store) {
  const entries = getObjectField(store, 'entries', null);
  return Array.isArray(entries) ? entries : [];
}

function getDiagnosticsStore() {
  const runtimeWindow = getWindow();
  if (!runtimeWindow) return null;

  let store = getDiagnosticsStoreField(runtimeWindow);
  if (!isDiagnosticsStore(store)) {
    store = createEmptyDiagnosticsStore();
    if (!setDiagnosticsStoreField(runtimeWindow, store)) {
      return null;
    }
  }

  ensureDiagnosticsClear(store);
  return isDiagnosticsStore(store) ? store : null;
}

function appendArrayItem(array, value) {
  try {
    array[array.length] = value;
    return true;
  } catch {
    return false;
  }
}

function clearArray(array) {
  try {
    array.length = 0;
  } catch {
    // Diagnostics are best-effort and must not throw.
  }
}

function clearDiagnosticsStore() {
  clearArray(getDiagnosticsEntries(this));
}

function trimArrayToLastItems(array, maxItems) {
  const length = getArrayLength(array);
  if (length <= maxItems) return;

  const startIndex = length - maxItems;
  try {
    for (let index = 0; index < maxItems; index += 1) {
      array[index] = array[index + startIndex];
    }
    array.length = maxItems;
  } catch {
    try {
      array.length = maxItems;
    } catch {
      // Diagnostics are best-effort and must not throw.
    }
  }
}

function cloneSerializedValue(value, seen = new WeakMap()) {
  if (!value || typeof value !== 'object') return value;
  if (seen.has(value)) return '[Circular]';

  if (Array.isArray(value)) {
    const clonedArray = [];
    seen.set(value, clonedArray);
    const length = getArrayLength(value);
    for (let index = 0; index < length; index += 1) {
      appendArrayItem(clonedArray, cloneSerializedValue(getArrayItem(value, index), seen));
    }
    return clonedArray;
  }

  const clonedObject = {};
  seen.set(value, clonedObject);

  let keys;
  try {
    keys = Object.keys(value);
  } catch (error) {
    return `[Unserializable: ${safeErrorMessage(error)}]`;
  }

  for (const key of keys) {
    try {
      clonedObject[key] = cloneSerializedValue(value[key], seen);
    } catch (error) {
      clonedObject[key] = `[Unserializable: ${safeErrorMessage(error)}]`;
    }
  }

  return clonedObject;
}

function cloneDiagnosticEntry(entry) {
  return cloneSerializedValue(entry);
}

function createDiagnosticsSnapshot() {
  const entries = getDiagnosticsEntries(getDiagnosticsStore());
  const snapshot = [];
  const length = getArrayLength(entries);
  for (let index = 0; index < length; index += 1) {
    appendArrayItem(snapshot, cloneDiagnosticEntry(getArrayItem(entries, index)));
  }
  return snapshot;
}

function serializeDate(date) {
  try {
    const timestamp = date.getTime();
    return Number.isFinite(timestamp) ? date.toISOString() : '[Invalid Date]';
  } catch {
    return '[Invalid Date]';
  }
}

function getCurrentTimestampMs() {
  try {
    const timestamp = Date.now();
    return Number.isFinite(timestamp) ? timestamp : 0;
  } catch {
    return 0;
  }
}

function getCurrentIsoTimestamp() {
  try {
    return new Date().toISOString();
  } catch {
    return '1970-01-01T00:00:00.000Z';
  }
}

function createLogEntryId() {
  logEntrySequence = (logEntrySequence + 1) % Number.MAX_SAFE_INTEGER;
  let randomPart = 'fallback';
  try {
    const randomValue = Math.random();
    if (Number.isFinite(randomValue)) {
      randomPart = randomValue.toString(36).slice(2) || randomPart;
    }
  } catch {
    randomPart = 'fallback';
  }
  return `${getCurrentTimestampMs()}-${randomPart}-${logEntrySequence}`;
}

function safeErrorMessage(error) {
  try {
    return safeString(error?.message || error, 'Unknown error');
  } catch {
    return 'Unknown error';
  }
}

function getArrayLength(value) {
  try {
    const length = value?.length;
    return Number.isSafeInteger(length) && length > 0 ? length : 0;
  } catch {
    return 0;
  }
}

function getArrayItem(value, index) {
  try {
    return value[index];
  } catch (error) {
    return `[Unserializable: ${safeErrorMessage(error)}]`;
  }
}

function arrayIncludesValue(array, expectedValue) {
  const length = getArrayLength(array);
  for (let index = 0; index < length; index += 1) {
    if (getArrayItem(array, index) === expectedValue) return true;
  }
  return false;
}

function isInstanceOf(value, constructorValue) {
  if (typeof constructorValue !== 'function') return false;
  try {
    return value instanceof constructorValue;
  } catch {
    return false;
  }
}

function isElementLike(value) {
  if (!value || typeof value !== 'object') return false;
  if (isInstanceOf(value, globalThis.Element) || isInstanceOf(value, globalThis.HTMLElement)) return true;
  try {
    return getObjectField(value, 'nodeType') === (globalThis.Node?.ELEMENT_NODE ?? 1) &&
      typeof getObjectField(value, 'hasAttribute') === 'function';
  } catch {
    return false;
  }
}

function isEventLike(value) {
  if (!value || typeof value !== 'object') return false;
  if (isInstanceOf(value, globalThis.Event)) return true;
  return typeof getObjectField(value, 'type') === 'string' &&
    (
      typeof getObjectField(value, 'preventDefault') === 'function' ||
      typeof getObjectField(value, 'stopPropagation') === 'function' ||
      typeof getObjectField(value, 'stopImmediatePropagation') === 'function'
    );
}

function getElementSummary(element) {
  return {
    element: safeString(getObjectField(element, 'tagName'), 'element').toLowerCase(),
    id: safeString(getObjectField(element, 'id', ''), '') || undefined,
    classes: safeString(getObjectField(element, 'className', ''), '') || undefined,
  };
}

function hasDebugAttribute(element) {
  try {
    return Boolean(element?.hasAttribute?.('debug'));
  } catch {
    return false;
  }
}

function getConstructorName(value) {
  try {
    return value?.constructor?.name || 'Object';
  } catch {
    return 'Object';
  }
}

function findLastDiagnosticEntry(level = null) {
  const entries = getDiagnosticsStore()?.entries || [];
  const normalizedLevel = safeString(level, '').trim().toLowerCase();
  const length = getArrayLength(entries);
  if (!normalizedLevel) return length > 0 ? getArrayItem(entries, length - 1) : null;

  for (let index = length - 1; index >= 0; index -= 1) {
    const entry = getArrayItem(entries, index);
    if (getObjectField(entry, 'level') === normalizedLevel) return entry;
  }

  return null;
}

function createEmptyDiagnosticsStore() {
  return {
    entries: [],
    clear: clearDiagnosticsStore
  };
}

function serializeArg(arg, seen = new WeakSet(), depth = 0) {
  if (arg === null || arg === undefined) return arg;

  const argType = typeof arg;
  if (argType === 'string' || argType === 'number' || argType === 'boolean') {
    return arg;
  }
  if (argType === 'bigint') {
    return `${safeString(arg)}n`;
  }
  if (argType === 'symbol' || argType === 'function') {
    return safeString(arg);
  }
  if (isInstanceOf(arg, globalThis.Error)) {
    return {
      name: safeString(getObjectField(arg, 'name'), 'Error'),
      message: safeString(getObjectField(arg, 'message'), ''),
      stack: safeString(getObjectField(arg, 'stack'), '')
    };
  }
  if (isElementLike(arg)) {
    return getElementSummary(arg);
  }
  if (isEventLike(arg)) {
    return {
      event: safeString(getObjectField(arg, 'type'), ''),
      target: isElementLike(getObjectField(arg, 'target'))
        ? serializeArg(getObjectField(arg, 'target'), seen, depth + 1)
        : undefined,
    };
  }
  if (isInstanceOf(arg, globalThis.Date)) {
    return serializeDate(arg);
  }
  if (argType !== 'object') {
    return safeString(arg);
  }
  if (seen.has(arg)) {
    return '[Circular]';
  }
  if (depth >= MAX_DIAGNOSTIC_DEPTH) {
    return `[MaxDepth:${getConstructorName(arg)}]`;
  }

  seen.add(arg);

  if (Array.isArray(arg)) {
    const length = getArrayLength(arg);
    const normalized = [];
    for (let index = 0; index < Math.min(length, MAX_DIAGNOSTIC_ARRAY_ITEMS); index += 1) {
      appendArrayItem(normalized, serializeArg(getArrayItem(arg, index), seen, depth + 1));
    }
    if (length > MAX_DIAGNOSTIC_ARRAY_ITEMS) {
      appendArrayItem(normalized, `[${length - MAX_DIAGNOSTIC_ARRAY_ITEMS} more item(s)]`);
    }
    seen.delete(arg);
    return normalized;
  }

  let keys;
  try {
    keys = Object.keys(arg);
  } catch (error) {
    seen.delete(arg);
    return `[Unserializable: ${safeErrorMessage(error)}]`;
  }
  const normalized = {};
  const keyCount = getArrayLength(keys);
  const keyLimit = Math.min(keyCount, MAX_DIAGNOSTIC_OBJECT_KEYS);
  for (let index = 0; index < keyLimit; index += 1) {
    const key = getArrayItem(keys, index);
    try {
      normalized[key] = serializeArg(arg[key], seen, depth + 1);
    } catch (error) {
      normalized[key] = `[Unserializable: ${safeErrorMessage(error)}]`;
    }
  }
  if (keyCount > MAX_DIAGNOSTIC_OBJECT_KEYS) {
    normalized.__truncatedKeys = keyCount - MAX_DIAGNOSTIC_OBJECT_KEYS;
  }

  seen.delete(arg);
  return normalized;
}

function recordLog(level, scope, message, args) {
  const store = getDiagnosticsStore();
  if (!store) return;
  const runtimeWindow = getWindow();

  const entry = {
    id: createLogEntryId(),
    timestamp: getCurrentIsoTimestamp(),
    level,
    scope,
    message: safeString(message),
    args: serializeLogArgs(args),
  };

  const entries = getDiagnosticsEntries(store);
  if (appendArrayItem(entries, entry)) {
    trimArrayToLastItems(entries, MAX_LOG_ENTRIES);
  }

  try {
    if (typeof runtimeWindow?.dispatchEvent === 'function' && typeof CustomEvent === 'function') {
      runtimeWindow.dispatchEvent(new CustomEvent(LOG_EVENT_NAME, { detail: entry }));
    }
  } catch {
    // Diagnostics should never make application logging throw.
  }
}

function serializeLogArgs(args) {
  const serializedArgs = [];
  const length = getArrayLength(args);
  for (let index = 0; index < length; index += 1) {
    appendArrayItem(serializedArgs, serializeArg(getArrayItem(args, index)));
  }
  return serializedArgs;
}

function shouldLog(levels) {
  return Logger.enabled && Logger.namespaces.system && arrayIncludesValue(levels, Logger.logLevel);
}

function shouldLogElement(element, levels) {
  return (
    Logger.enabled &&
    isElementLike(element) &&
    hasDebugAttribute(element) &&
    arrayIncludesValue(levels, Logger.logLevel)
  );
}

function shouldLogElementError(element) {
  return (
    Logger.enabled &&
    isElementLike(element) &&
    hasDebugAttribute(element)
  );
}

function callConsole(level, prefix, message, args) {
  const writer = console?.[level] || console?.log;
  if (typeof writer !== 'function') return;
  try {
    writer.call(console, prefix, message, ...args);
  } catch {
    // Logging must not throw back into application code.
  }
}

function writeConsole(level, prefix, message, args) {
  recordLog(level, prefix, message, args);

  if (level === LogLevel.DEBUG) {
    callConsole('debug', prefix, message, args);
    return;
  }

  if (level === LogLevel.INFO) {
    callConsole('info', prefix, message, args);
    return;
  }

  if (level === LogLevel.WARN) {
    callConsole('warn', prefix, message, args);
    return;
  }

  callConsole('error', prefix, message, args);
}

export const Logger = {
  enabled: true,
  logLevel: getInitialLogLevel(),
  namespaces: {
    system: true,
  },

  diagnostics: {
    eventName: LOG_EVENT_NAME,
    globalName: DIAGNOSTICS_GLOBAL,
    get entries() {
      return createDiagnosticsSnapshot();
    },
    snapshot() {
      return createDiagnosticsSnapshot();
    },
    last(level = null) {
      const entry = findLastDiagnosticEntry(level);
      return entry ? cloneDiagnosticEntry(entry) : null;
    },
    clear() {
      try {
        getDiagnosticsStore()?.clear();
      } catch {
        // Diagnostics are best-effort and must not throw.
      }
    }
  },

  system: {
    debug: (message, ...args) => {
      if (shouldLog([LogLevel.DEBUG])) {
        writeConsole(LogLevel.DEBUG, '[HTMLeX SYSTEM DEBUG]', message, args);
      }
    },
    info: (message, ...args) => {
      if (shouldLog([LogLevel.DEBUG, LogLevel.INFO])) {
        writeConsole(LogLevel.INFO, '[HTMLeX SYSTEM INFO]', message, args);
      }
    },
    warn: (message, ...args) => {
      if (shouldLog([LogLevel.DEBUG, LogLevel.INFO, LogLevel.WARN])) {
        writeConsole(LogLevel.WARN, '[HTMLeX SYSTEM WARN]', message, args);
      }
    },
    error: (message, ...args) => {
      if (Logger.enabled && Logger.namespaces.system) {
        writeConsole(LogLevel.ERROR, '[HTMLeX SYSTEM ERROR]', message, args);
      }
    }
  },

  element: {
    debug: (element, message, ...args) => {
      if (shouldLogElement(element, [LogLevel.DEBUG])) {
        writeConsole(LogLevel.DEBUG, '[HTMLeX DEBUG]', message, [element, ...args]);
      }
    },
    info: (element, message, ...args) => {
      if (shouldLogElement(element, [LogLevel.DEBUG, LogLevel.INFO])) {
        writeConsole(LogLevel.INFO, '[HTMLeX INFO]', message, [element, ...args]);
      }
    },
    warn: (element, message, ...args) => {
      if (shouldLogElement(element, [LogLevel.DEBUG, LogLevel.INFO, LogLevel.WARN])) {
        writeConsole(LogLevel.WARN, '[HTMLeX WARN]', message, [element, ...args]);
      }
    },
    error: (element, message, ...args) => {
      if (shouldLogElementError(element)) {
        writeConsole(LogLevel.ERROR, '[HTMLeX ERROR]', message, [element, ...args]);
      }
    }
  }
};

let runtimeErrorBoundaryInstalled = false;

function getRuntimeEventField(event, fieldName) {
  try {
    return event?.[fieldName];
  } catch (error) {
    Logger.system.warn(`[Runtime] Failed to read runtime event field "${fieldName}".`, error);
    return undefined;
  }
}

function getRuntimeErrorDetails(event) {
  return {
    message: getRuntimeEventField(event, 'message'),
    source: getRuntimeEventField(event, 'filename'),
    line: getRuntimeEventField(event, 'lineno'),
    column: getRuntimeEventField(event, 'colno'),
    error: getRuntimeEventField(event, 'error'),
  };
}

export function installRuntimeErrorBoundary() {
  const runtimeWindow = typeof window === 'undefined' ? globalThis.window : window;
  if (runtimeErrorBoundaryInstalled || !runtimeWindow) return;

  let addEventListener;
  try {
    addEventListener = runtimeWindow.addEventListener;
  } catch (error) {
    Logger.system.warn('[Runtime] Failed to inspect runtime error boundary support.', error);
    return;
  }
  if (typeof addEventListener !== 'function') return;

  try {
    addEventListener.call(runtimeWindow, 'error', (event) => {
      Logger.system.error('[Runtime] Unhandled browser error:', getRuntimeErrorDetails(event));
    });
    addEventListener.call(runtimeWindow, 'unhandledrejection', (event) => {
      Logger.system.error('[Runtime] Unhandled promise rejection:', getRuntimeEventField(event, 'reason'));
    });
    runtimeErrorBoundaryInstalled = true;
  } catch (error) {
    Logger.system.warn('[Runtime] Failed to install runtime error boundary.', error);
  }
}
