// src/utils.js
/**
 * @module Utils
 * @description Provides utility functions for scheduling and update queue processing.
 */

import { Logger } from './logger.js';

/** @type {Array<Function>} */
const updateQueue = [];

/** @type {boolean} */
let processingQueue = false;

/** @type {number} */
let updateQueueCursor = 0;

function getGlobalFunction(name) {
  try {
    const value = globalThis[name];
    return typeof value === 'function' ? value : null;
  } catch (error) {
    Logger.system.warn(`[UTILS] Unable to read global ${name}.`, error);
    return null;
  }
}

function runUpdateSafely(updateFn, label) {
  try {
    updateFn();
    Logger.system.debug(`[UTILS] ${label} update function executed successfully.`);
  } catch (error) {
    Logger.system.error(`[UTILS] Error executing ${label} update function:`, error);
  }
}

function getQueueLength(queue) {
  try {
    const length = queue?.length;
    return Number.isSafeInteger(length) && length > 0 ? length : 0;
  } catch (error) {
    Logger.system.warn('[UTILS] Unable to read update queue length.', error);
    return 0;
  }
}

function getQueuedUpdate(queue, index) {
  try {
    return queue?.[index];
  } catch (error) {
    Logger.system.warn('[UTILS] Unable to read queued update.', error);
    return undefined;
  }
}

function appendQueuedUpdate(queue, updateFn) {
  try {
    queue[queue.length] = updateFn;
    return true;
  } catch (error) {
    Logger.system.warn('[UTILS] Unable to queue sequential update; running it immediately.', error);
    return false;
  }
}

function clearUpdateQueue(queue) {
  try {
    queue.length = 0;
  } catch (error) {
    Logger.system.warn('[UTILS] Unable to clear update queue.', error);
  }
}

function scheduleFrame(callback) {
  const requestFrame = getGlobalFunction('requestAnimationFrame');
  if (requestFrame) {
    try {
      return requestFrame(callback);
    } catch (error) {
      Logger.system.warn('[UTILS] requestAnimationFrame failed; falling back to setTimeout.', error);
    }
  }

  const setTimeoutFn = getGlobalFunction('setTimeout');
  if (setTimeoutFn) {
    try {
      return setTimeoutFn(callback, 0);
    } catch (error) {
      Logger.system.warn('[UTILS] setTimeout failed; running scheduled update synchronously.', error);
    }
  }

  try {
    callback();
  } catch (error) {
    Logger.system.error('[UTILS] Scheduled update callback failed.', error);
  }

  return null;
}

/**
 * Processes the update queue.
 * @private
 */
function processUpdateQueue() {
  const queueLength = getQueueLength(updateQueue);
  if (updateQueueCursor < queueLength) {
    Logger.system.debug("[UTILS] Processing update from queue. Remaining updates:", queueLength - updateQueueCursor);
    const updateFn = getQueuedUpdate(updateQueue, updateQueueCursor);
    updateQueueCursor += 1;

    runUpdateSafely(updateFn, 'queued');
    scheduleFrame(processUpdateQueue);
  } else {
    clearUpdateQueue(updateQueue);
    updateQueueCursor = 0;
    processingQueue = false;
    Logger.system.debug("[UTILS] Update queue empty. Stopping processing.");
  }
}

/**
 * Schedules an update function.
 * @param {Function} updateFn - The update function to run.
 * @param {boolean} sequential - If true, the function is queued for sequential processing.
 */
export function scheduleUpdate(updateFn, sequential) {
  if (sequential) {
    Logger.system.debug("[UTILS] Scheduling sequential update function.");
    if (!appendQueuedUpdate(updateQueue, updateFn)) {
      runUpdateSafely(updateFn, 'queued');
      return;
    }
    if (!processingQueue) {
      processingQueue = true;
      Logger.system.debug("[UTILS] Starting update queue processing.");
      scheduleFrame(processUpdateQueue);
    }
  } else {
    Logger.system.debug("[UTILS] Scheduling immediate (non-sequential) update function.");
    scheduleFrame(() => runUpdateSafely(updateFn, 'immediate'));
  }
}

/**
 * Checks if an element is set for sequential updates.
 * @param {Element} element - The DOM element.
 * @returns {boolean} True if the element has sequential updates enabled.
 */
export function isSequential(element) {
  let sequential = false;
  try {
    if (element?.hasAttribute?.('sequential')) {
      sequential = String(element.getAttribute?.('sequential') ?? '').trim().toLowerCase() !== 'false';
    }
  } catch (error) {
    Logger.system.warn('[UTILS] Unable to read sequential attribute; treating it as disabled.', error);
  }
  Logger.system.debug("[UTILS] Element", element, "sequential update:", sequential);
  return sequential;
}
