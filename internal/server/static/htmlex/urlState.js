// src/urlState.js
/**
 * @module URLState
 * @description Updates the browser's URL state based on element attributes.
 */

import { Logger } from './logger.js';

function getRuntimeWindow() {
  try {
    return typeof window !== 'undefined' ? window : globalThis.window;
  } catch (error) {
    Logger.system.warn("[URLState] Failed to read window; skipping URL state update.", error);
    return null;
  }
}

function safeString(value, fallback = '') {
  try {
    return String(value ?? fallback);
  } catch (error) {
    Logger.system.warn("[URLState] Failed to convert URL attribute value to text.", error);
    return fallback;
  }
}

function getLocationHref(runtimeWindow) {
  try {
    return runtimeWindow?.location?.href || '';
  } catch (error) {
    Logger.system.warn("[URLState] Failed to read current location; skipping URL state update.", error);
    return '';
  }
}

function hasElementAttribute(element, attributeName) {
  try {
    return Boolean(element?.hasAttribute?.(attributeName));
  } catch (error) {
    Logger.system.warn(`[URLState] Failed to read '${attributeName}' attribute; skipping it.`, error);
    return false;
  }
}

function getElementAttribute(element, attributeName) {
  try {
    return element?.getAttribute?.(attributeName) ?? null;
  } catch (error) {
    Logger.system.warn(`[URLState] Failed to read '${attributeName}' attribute; using an empty value.`, error);
    return null;
  }
}

function getBrowserHistory(runtimeWindow) {
  try {
    return runtimeWindow?.history || globalThis.history || null;
  } catch (error) {
    Logger.system.warn("[URLState] Failed to read History API; skipping URL state update.", error);
    return null;
  }
}

function parseKeyValueList(value) {
  return safeString(value).split(/\s+/).filter(Boolean).map(pair => {
    const separatorIndex = pair.indexOf('=');
    return {
      key: separatorIndex >= 0 ? pair.slice(0, separatorIndex) : pair,
      value: separatorIndex >= 0 ? pair.slice(separatorIndex + 1) : ''
    };
  }).filter(({ key }) => key);
}

function getSearchParams(url) {
  try {
    return url.searchParams;
  } catch (error) {
    Logger.system.warn("[URLState] Failed to access URL search parameters.", error);
    return null;
  }
}

function setSearchParam(url, key, value) {
  try {
    getSearchParams(url)?.set(key, value);
  } catch (error) {
    Logger.system.warn(`[URLState] Failed to set search parameter: ${key}`, error);
  }
}

function deleteSearchParam(url, key) {
  try {
    getSearchParams(url)?.delete(key);
  } catch (error) {
    Logger.system.warn(`[URLState] Failed to delete search parameter: ${key}`, error);
  }
}

function setUrlPathname(url, pathname) {
  try {
    url.pathname = pathname;
  } catch (error) {
    Logger.system.warn("[URLState] Failed to update URL path.", error);
  }
}

function getUrlText(url) {
  try {
    return url.toString();
  } catch (error) {
    Logger.system.warn("[URLState] Failed to serialize URL state.", error);
    return '';
  }
}

function readHistoryMethod(element) {
  const historyValue = safeString(getElementAttribute(element, 'history') || 'replace').trim().toLowerCase();
  if (['none', 'push', 'replace'].includes(historyValue)) {
    return historyValue;
  }

  Logger.system.warn("[URLState] Invalid history method; replacing URL state instead:", historyValue);
  return 'replace';
}

function callHistoryMethod(browserHistory, methodName, urlText) {
  if (!urlText) return;

  try {
    browserHistory[methodName](null, '', urlText);
    Logger.system.info(`[URLState] ${methodName === 'pushState' ? 'Pushed' : 'Replaced'} new URL state: ${urlText}`);
  } catch (error) {
    Logger.system.warn(`[URLState] Failed to ${methodName === 'pushState' ? 'push' : 'replace'} URL state: ${urlText}`, error);
  }
}

/**
 * Updates the URL state using attributes such as push, pull, and path.
 * @param {Element} element - The element with URL state attributes.
 */
export function handleURLState(element) {
  Logger.system.debug("[URLState] Starting URL state update for element:", element);
  const runtimeWindow = getRuntimeWindow();
  const locationHref = getLocationHref(runtimeWindow);
  if (!locationHref) {
    Logger.system.warn("[URLState] Window location is unavailable; skipping URL state update.");
    return;
  }
  if (typeof globalThis.URL !== 'function') {
    Logger.system.warn("[URLState] URL API is unavailable; skipping URL state update.");
    return;
  }

  let newUrl;
  try {
    newUrl = new globalThis.URL(locationHref);
  } catch (error) {
    Logger.system.warn("[URLState] Current URL is invalid; skipping URL state update.", error);
    return;
  }
  Logger.system.debug("[URLState] Current URL:", locationHref);

  if (hasElementAttribute(element, 'push')) {
    const pushValue = getElementAttribute(element, 'push');
    Logger.system.debug("[URLState] Found 'push' attribute with value:", pushValue);
    for (const { key, value } of parseKeyValueList(pushValue)) {
      Logger.system.debug(`[URLState] Setting search parameter: ${key}=${value}`);
      setSearchParam(newUrl, key, value);
    }
  }

  if (hasElementAttribute(element, 'pull')) {
    const pullValue = getElementAttribute(element, 'pull');
    Logger.system.debug("[URLState] Found 'pull' attribute with value:", pullValue);
    const keys = safeString(pullValue).split(/\s+/).filter(Boolean);
    for (const key of keys) {
      Logger.system.debug(`[URLState] Removing search parameter: ${key}`);
      deleteSearchParam(newUrl, key);
    }
  }

  if (hasElementAttribute(element, 'path')) {
    const newPath = safeString(getElementAttribute(element, 'path')).trim();
    Logger.system.debug("[URLState] Found 'path' attribute with value:", newPath);
    if (newPath) {
      setUrlPathname(newUrl, newPath);
    }
  }

  if (hasElementAttribute(element, 'push') || hasElementAttribute(element, 'pull') || hasElementAttribute(element, 'path')) {
    const browserHistory = getBrowserHistory(runtimeWindow);
    if (!browserHistory) {
      Logger.system.warn(`[URLState] History API is unavailable for ${getUrlText(newUrl)}`);
      return;
    }

    const historyMethod = readHistoryMethod(element);
    const urlText = getUrlText(newUrl);
    Logger.system.debug("[URLState] History method set to:", historyMethod);
    if (historyMethod === 'none') {
      Logger.system.info(`[URLState] History update skipped for ${urlText}`);
    } else if (historyMethod === 'push') {
      callHistoryMethod(browserHistory, 'pushState', urlText);
    } else {
      callHistoryMethod(browserHistory, 'replaceState', urlText);
    }
  } else {
    Logger.system.debug("[URLState] No URL state attributes found; no changes made.");
  }
}
