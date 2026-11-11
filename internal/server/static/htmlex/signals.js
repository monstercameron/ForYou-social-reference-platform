// src/signals.js
/**
 * @module Signals
 * @description Provides a signal bus for chaining actions.
 */

import { Logger } from './logger.js';

/** @type {Map<string, Set<Function>>} */
const signalBus = new Map();

function normalizeSignalName(signalName) {
  try {
    return String(signalName ?? '').trim();
  } catch (error) {
    Logger.system.warn('[SIGNALS] Ignoring signal name that could not be converted to text.', error);
    return '';
  }
}

function observeAsyncListenerResult(result, signalName) {
  let thenMethod;
  try {
    thenMethod = result?.then;
  } catch (error) {
    Logger.system.error(`[SIGNALS] Error reading async listener result for "${signalName}":`, error);
    return;
  }

  if (typeof thenMethod !== 'function') return;

  try {
    thenMethod.call(result, undefined, error => {
      Logger.system.error(`[SIGNALS] Async error in signal listener for "${signalName}":`, error);
    });
  } catch (error) {
    Logger.system.error(`[SIGNALS] Error observing async listener for "${signalName}":`, error);
  }
}

/**
 * Registers a listener for a given signal.
 * @param {string} signalName - The name of the signal (e.g., "@todosLoaded").
 * @param {Function} callback - The callback to invoke when the signal is emitted.
 * @returns {Function} Unregisters the listener.
 */
export function registerSignalListener(signalName, callback) {
  const normalizedSignalName = normalizeSignalName(signalName);
  if (!normalizedSignalName) {
    Logger.system.warn("[SIGNALS] Ignoring empty signal listener registration.");
    return () => {};
  }
  if (typeof callback !== 'function') {
    Logger.system.warn(`[SIGNALS] Ignoring listener for "${normalizedSignalName}" because callback is not a function.`);
    return () => {};
  }

  let listeners = signalBus.get(normalizedSignalName);
  if (!listeners) {
    listeners = new Set();
    signalBus.set(normalizedSignalName, listeners);
    Logger.system.debug(`[SIGNALS] Created new signal bus for "${normalizedSignalName}".`);
  }

  listeners.add(callback);
  Logger.system.debug(`[SIGNALS] Registered listener for signal "${normalizedSignalName}".`);
  return () => {
    const listeners = signalBus.get(normalizedSignalName);
    if (!listeners) return;
    if (listeners.delete(callback)) {
      Logger.system.debug(`[SIGNALS] Unregistered listener for signal "${normalizedSignalName}".`);
    }
    if (!listeners.size) {
      signalBus.delete(normalizedSignalName);
    }
  };
}

// Internal diagnostic used by the browser e2e suite to verify cleanup behavior.
export function __getSignalListenerCount(signalName) {
  return signalBus.get(normalizeSignalName(signalName))?.size || 0;
}

/**
 * Emits a signal to all registered listeners.
 * @param {string} signalName - The signal to emit.
 */
export function emitSignal(signalName) {
  const normalizedSignalName = normalizeSignalName(signalName);
  if (!normalizedSignalName) {
    Logger.system.warn("[SIGNALS] Ignoring empty signal emission.");
    return;
  }

  Logger.system.debug(`[SIGNALS] Emitting signal "${normalizedSignalName}".`);
  const listeners = signalBus.get(normalizedSignalName);
  if (listeners) {
    for (const callback of [...listeners]) {
      try {
        const result = callback();
        observeAsyncListenerResult(result, normalizedSignalName);
        Logger.system.debug(`[SIGNALS] Signal "${normalizedSignalName}" listener executed successfully.`);
      } catch (error) {
        Logger.system.error(`[SIGNALS] Error in signal listener for "${normalizedSignalName}":`, error);
      }
    }
  } else {
    Logger.system.warn(`[SIGNALS] No listeners registered for signal "${normalizedSignalName}".`);
  }
}
