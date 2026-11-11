/**
 * @module RateLimit
 * @description Provides functions to debounce and throttle calls.
 */

import { Logger } from './logger.js';

function getProperty(value, propertyName) {
  try {
    return value?.[propertyName];
  } catch (error) {
    Logger.system.warn(`[RATELIMIT] Failed to read event property "${propertyName}".`, error);
    return undefined;
  }
}

function callEventMethod(event, methodName) {
  try {
    return getProperty(event, methodName)?.();
  } catch (error) {
    Logger.system.warn(`[RATELIMIT] Failed to call event method "${methodName}".`, error);
    return undefined;
  }
}

function scheduleTimer(callback, delayMs) {
  const runSafely = () => {
    try {
      callback();
    } catch (error) {
      Logger.system.error('[RATELIMIT] Scheduled callback failed.', error);
    }
  };

  if (typeof globalThis.setTimeout !== 'function') {
    Logger.system.warn('[RATELIMIT] setTimeout is unavailable; running delayed callback immediately.');
    runSafely();
    return null;
  }

  try {
    return globalThis.setTimeout(runSafely, delayMs);
  } catch (error) {
    Logger.system.warn('[RATELIMIT] Failed to schedule delayed callback; running immediately.', error);
    runSafely();
    return null;
  }
}

function clearTimer(timeoutId) {
  if (timeoutId === null) return;
  if (typeof globalThis.clearTimeout !== 'function') return;

  try {
    globalThis.clearTimeout(timeoutId);
  } catch (error) {
    Logger.system.warn('[RATELIMIT] Failed to clear delayed callback.', error);
  }
}

function isEventLike(value) {
  if (!value || typeof value !== 'object') return false;

  if (typeof globalThis.Event === 'function') {
    try {
      if (value instanceof globalThis.Event) return true;
    } catch {
      // Fall through to structural detection for hostile constructors.
    }
  }

  return typeof getProperty(value, 'type') === 'string' &&
    (
      typeof getProperty(value, 'preventDefault') === 'function' ||
      typeof getProperty(value, 'stopPropagation') === 'function' ||
      typeof getProperty(value, 'stopImmediatePropagation') === 'function'
    );
}

function cloneEventArgs(args) {
  if (!isEventLike(args[0])) {
    return args;
  }

  const originalEvent = args[0];
  return [
    {
      type: getProperty(originalEvent, 'type'),
      target: getProperty(originalEvent, 'target'),
      currentTarget: getProperty(originalEvent, 'currentTarget'),
      defaultPrevented: getProperty(originalEvent, 'defaultPrevented'),
      preventDefault: () => callEventMethod(originalEvent, 'preventDefault'),
      stopPropagation: () => callEventMethod(originalEvent, 'stopPropagation'),
      stopImmediatePropagation: () => callEventMethod(originalEvent, 'stopImmediatePropagation')
    },
    ...args.slice(1)
  ];
}

function normalizeDelay(value) {
  const delay = Number(value);
  return Number.isFinite(delay) && delay > 0 ? delay : 0;
}

/**
 * Creates a debounced version of the function with trailing-edge behavior.
 * The latest call is executed after the wait period and earlier pending calls are cancelled.
 * If the first argument is an Event, a shallow copy of its key properties is made
 * to avoid issues with the event being reused.
 *
 * @param {Function} callback - The function to debounce.
 * @param {number} wait - The debounce interval in milliseconds.
 * @returns {Function} The debounced function.
 */
export function debounce(callback, wait) {
  const waitMs = normalizeDelay(wait);
  let timeoutId = null;
  let resolvePending = null;
  Logger.system.debug("[DEBOUNCE] Creating debounced function with wait period:", waitMs, "ms");

  const debounced = function(...args) {
    const context = this;
    Logger.system.debug("[DEBOUNCE] Debounced function invoked with arguments:", args, "and context:", context);
    const invocationArgs = cloneEventArgs(args);

    if (timeoutId !== null) {
      clearTimer(timeoutId);
      if (resolvePending) {
        resolvePending(undefined);
        resolvePending = null;
      }
      Logger.system.debug("[DEBOUNCE] Cleared pending debounce call.");
    }

    return new Promise(resolve => {
      resolvePending = resolve;
      timeoutId = scheduleTimer(async () => {
        timeoutId = null;
        resolvePending = null;
        try {
          const result = await callback.apply(context, invocationArgs);
          Logger.system.debug("[DEBOUNCE] Function executed successfully.");
          resolve(result);
        } catch (error) {
          Logger.system.error("[DEBOUNCE] Error executing debounced function:", error);
          resolve(undefined);
        }
      }, waitMs);
    });
  };

  debounced.cancel = () => {
    if (timeoutId !== null) {
      clearTimer(timeoutId);
      timeoutId = null;
    }
    if (resolvePending) {
      resolvePending(undefined);
      resolvePending = null;
    }
  };

  return debounced;
}

/**
 * Creates a throttled version of the function.
 * @param {Function} callback - The function to throttle.
 * @param {number} limit - The throttle interval in milliseconds.
 * @returns {Function} The throttled function.
 */
export function throttle(callback, limit) {
  const limitMs = normalizeDelay(limit);
  let inThrottle = false;
  let timeoutId = null;
  Logger.system.debug("[THROTTLE] Creating throttled function with limit:", limitMs, "ms");

  const throttled = async function(...args) {
    const context = this;
    Logger.system.debug("[THROTTLE] Throttled function invoked with arguments:", args, "and context:", context);

    if (limitMs <= 0) {
      try {
        const result = await callback.apply(context, cloneEventArgs(args));
        Logger.system.debug("[THROTTLE] Function executed without throttling.");
        return result;
      } catch (error) {
        Logger.system.error("[THROTTLE] Error executing unthrottled function:", error);
        return undefined;
      }
    }

    if (!inThrottle) {
      Logger.system.debug("[THROTTLE] Not in throttle; invoking function.");
      const invocationArgs = cloneEventArgs(args);
      inThrottle = true;
      timeoutId = scheduleTimer(() => {
        inThrottle = false;
        timeoutId = null;
        Logger.system.debug("[THROTTLE] Throttle period ended; ready for next call.");
      }, limitMs);
      try {
        const result = await callback.apply(context, invocationArgs);
        Logger.system.debug("[THROTTLE] Function executed successfully.");
        return result;
      } catch (error) {
        Logger.system.error("[THROTTLE] Error executing throttled function:", error);
        return undefined;
      }
    } else {
      Logger.system.warn("[THROTTLE] Function call throttled. Arguments:", args);
      return undefined;
    }
  };

  throttled.cancel = () => {
    if (timeoutId !== null) {
      clearTimer(timeoutId);
      timeoutId = null;
    }
    inThrottle = false;
  };

  return throttled;
}
