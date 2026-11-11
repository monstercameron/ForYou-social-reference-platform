// src/fetchHelper.js
/**
 * @module FetchHelper
 * @description Provides a fetch wrapper with timeout functionality.
 */

import { Logger } from './logger.js';

function normalizeTimeoutMs(timeoutMs) {
  const timeout = Number(timeoutMs);
  return Number.isFinite(timeout) && timeout > 0 ? timeout : 0;
}

function normalizeFetchOptions(options) {
  return options && typeof options === 'object' ? options : {};
}

function getFetchImplementation() {
  try {
    return typeof globalThis.fetch === 'function' ? globalThis.fetch.bind(globalThis) : null;
  } catch (error) {
    Logger.system.warn('[FETCH] Failed to read fetch implementation.', error);
    return null;
  }
}

function getAbortControllerConstructor() {
  try {
    return typeof globalThis.AbortController === 'function' ? globalThis.AbortController : null;
  } catch (error) {
    Logger.system.warn('[FETCH] Failed to read AbortController constructor.', error);
    return null;
  }
}

function getSignalField(signal, fieldName, fallback = undefined) {
  try {
    return signal?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[FETCH] Failed to read abort signal field "${fieldName}".`, error);
    return fallback;
  }
}

function getOptionSignal(options) {
  try {
    return options?.signal ?? null;
  } catch (error) {
    Logger.system.warn('[FETCH] Failed to read upstream abort signal.', error);
    return null;
  }
}

function mergeOptionsWithSignal(options, signal) {
  try {
    return { ...options, signal };
  } catch (error) {
    Logger.system.warn('[FETCH] Failed to copy fetch options; sending only abort signal.', error);
    return { signal };
  }
}

function callFetch(url, options) {
  const fetchImplementation = getFetchImplementation();
  if (!fetchImplementation) {
    throw new Error('fetch is unavailable in this environment.');
  }
  return fetchImplementation(url, options);
}

function createAbortError(message = 'Request aborted', cause = undefined) {
  const error = new Error(message, cause === undefined ? undefined : { cause });
  error.name = 'AbortError';
  return error;
}

function scheduleTimeout(callback, delayMs) {
  if (delayMs <= 0) return null;
  if (typeof globalThis.setTimeout !== 'function') {
    Logger.system.warn('[FETCH] setTimeout is unavailable; request timeout cannot be scheduled.');
    return null;
  }

  try {
    return globalThis.setTimeout(callback, delayMs);
  } catch (error) {
    Logger.system.warn('[FETCH] Failed to schedule request timeout.', error);
    return null;
  }
}

function clearScheduledTimeout(timeoutId) {
  if (timeoutId === null) return;
  if (typeof globalThis.clearTimeout !== 'function') return;

  try {
    globalThis.clearTimeout(timeoutId);
  } catch (error) {
    Logger.system.warn('[FETCH] Failed to clear request timeout.', error);
  }
}

function addAbortListener(signal, listener) {
  try {
    if (typeof signal?.addEventListener !== 'function') return () => {};
    try {
      signal.addEventListener('abort', listener, { once: true });
    } catch {
      signal.addEventListener('abort', listener);
    }
    return () => {
      try {
        signal.removeEventListener?.('abort', listener);
      } catch (error) {
        Logger.system.warn('[FETCH] Failed to remove abort listener.', error);
      }
    };
  } catch (error) {
    Logger.system.warn('[FETCH] Failed to attach abort listener.', error);
    return () => {};
  }
}

async function fetchWithTimeoutFallback(url, options, timeout) {
  const fetchOptions = normalizeFetchOptions(options);
  const upstreamSignal = getOptionSignal(fetchOptions);
  if (getSignalField(upstreamSignal, 'aborted', false)) {
    throw createAbortError('Request aborted', getSignalField(upstreamSignal, 'reason'));
  }

  let timeoutId = null;
  let removeAbortListener = () => {};
  const timeoutPromise = new Promise((_, reject) => {
    if (timeout > 0) {
      timeoutId = scheduleTimeout(() => {
        Logger.system.error("[FETCH] Request timed out for URL:", url);
        reject(new Error("Request timed out"));
      }, timeout);
    }
    removeAbortListener = addAbortListener(upstreamSignal, () => {
      reject(createAbortError('Request aborted', getSignalField(upstreamSignal, 'reason')));
    });
  });

  try {
    const response = await (timeoutId !== null || upstreamSignal
      ? Promise.race([callFetch(url, fetchOptions), timeoutPromise])
      : callFetch(url, fetchOptions));
    Logger.system.debug("[FETCH] Successfully fetched URL:", url);
    return response;
  } finally {
    clearScheduledTimeout(timeoutId);
    removeAbortListener();
  }
}

/**
 * Fetches a resource with a timeout.
 * @param {string} url - The URL to fetch.
 * @param {RequestInit} options - The fetch options.
 * @param {number} timeoutMs - Timeout in milliseconds.
 * @returns {Promise<Response>} A promise that resolves with the fetch response.
 */
export async function fetchWithTimeout(url, options = {}, timeoutMs = 0) {
  Logger.system.debug("[FETCH] Initiating fetch for URL:", url, "with timeout:", timeoutMs);

  const timeout = normalizeTimeoutMs(timeoutMs);
  const fetchOptions = normalizeFetchOptions(options);
  const AbortControllerConstructor = getAbortControllerConstructor();
  if (!AbortControllerConstructor) {
    Logger.system.warn('[FETCH] AbortController is unavailable; using non-aborting timeout fallback.');
    return fetchWithTimeoutFallback(url, fetchOptions, timeout);
  }

  const upstreamSignal = getOptionSignal(fetchOptions);
  if (timeout <= 0 && !upstreamSignal) {
    try {
      const response = await callFetch(url, fetchOptions);
      Logger.system.debug("[FETCH] Successfully fetched URL:", url);
      return response;
    } catch (error) {
      Logger.system.error("[FETCH] Error fetching URL:", url, error);
      throw error;
    }
  }

  let controller;
  try {
    controller = new AbortControllerConstructor();
  } catch (error) {
    Logger.system.warn('[FETCH] Failed to create AbortController; using non-aborting timeout fallback.', error);
    return fetchWithTimeoutFallback(url, fetchOptions, timeout);
  }

  let didTimeout = false;
  let timeoutId = null;
  let removeAbortListener = () => {};

  if (upstreamSignal) {
    const abortFromUpstream = () => {
      try {
        controller.abort(getSignalField(upstreamSignal, 'reason'));
      } catch (error) {
        Logger.system.warn('[FETCH] Failed to abort request from upstream signal.', error);
      }
    };
    if (getSignalField(upstreamSignal, 'aborted', false)) {
      abortFromUpstream();
    } else {
      removeAbortListener = addAbortListener(upstreamSignal, abortFromUpstream);
    }
  }

  if (timeout > 0) {
    timeoutId = scheduleTimeout(() => {
      didTimeout = true;
      Logger.system.error("[FETCH] Request timed out for URL:", url);
      try {
        controller.abort(new Error("Request timed out"));
      } catch (error) {
        Logger.system.warn('[FETCH] Failed to abort timed-out request.', error);
      }
    }, timeout);
  }

  try {
    const response = await callFetch(url, mergeOptionsWithSignal(fetchOptions, controller.signal));
    Logger.system.debug("[FETCH] Successfully fetched URL:", url);
    return response;
  } catch (error) {
    Logger.system.error("[FETCH] Error fetching URL:", url, error);
    if (didTimeout) {
      throw new Error("Request timed out", { cause: error });
    }
    throw error;
  } finally {
    clearScheduledTimeout(timeoutId);
    removeAbortListener();
  }
}
