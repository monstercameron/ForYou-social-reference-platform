/**
 * @module Actions
 * @description Handles API calls, lifecycle hooks, request side effects, and response processing.
 */

import { Logger } from './logger.js';
import { getCache, setCache } from './cache.js';
import { scheduleUpdate, isSequential } from './utils.js';
import { parseTargets, querySelectorAllResult, querySelectorSafe, updateTarget } from './dom.js';
import { fetchWithTimeout } from './fetchHelper.js';
import { handleURLState } from './urlState.js';
import { processFragmentBuffer } from './fragments.js';
import { emitSignal } from './signals.js';
import { runLifecycleHook } from './hooks.js';

export const DEFAULT_RESPONSE_BUFFER_LIMIT_CHARS = 1024 * 1024;
const RESERVED_FETCH_OPTION_KEYS = new Set(['__proto__', 'constructor', 'prototype']);

class ResponseBufferLimitError extends Error {
  constructor(limitChars) {
    super(`HTMLeX response exceeded the ${limitChars} character safety limit.`);
    this.name = 'ResponseBufferLimitError';
    this.limitChars = limitChars;
  }
}

function safeString(value, fallback = '') {
  try {
    return String(value ?? fallback);
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to coerce value to string.', error);
    return fallback;
  }
}

function escapeHtml(value) {
  return safeString(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function getErrorMessage(error) {
  if (isInstanceOf(error, getGlobalField('Error'))) {
    return safeString(getObjectField(error, 'message', 'Unknown error'), 'Unknown error');
  }

  return safeString(error, 'Unknown error');
}

function getGlobalField(name) {
  try {
    return globalThis[name];
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read global ${name}.`, error);
    return undefined;
  }
}

function getGlobalFunction(name) {
  const value = getGlobalField(name);
  return typeof value === 'function' ? value : null;
}

function getObjectField(value, fieldName, fallback = undefined) {
  try {
    return value?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read ${fieldName}.`, error);
    return fallback;
  }
}

function hasElementAttribute(element, attributeName) {
  try {
    return Boolean(element?.hasAttribute?.(attributeName));
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to check ${attributeName} attribute.`, error);
    return false;
  }
}

function getElementAttribute(element, attributeName) {
  try {
    return element?.getAttribute?.(attributeName) ?? null;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read ${attributeName} attribute.`, error);
    return null;
  }
}

function normalizeFormDataEntry(entry) {
  try {
    if (!entry || typeof entry[Symbol.iterator] !== 'function') return null;
    const iterator = entry[Symbol.iterator]();
    const key = iterator.next();
    const value = iterator.next();
    if (key?.done) return null;
    return [key.value, value?.done ? '' : value?.value];
  } catch (error) {
    Logger.system.warn('[HTMLeX] Ignoring malformed FormData entry.', error);
    return null;
  }
}

function getFormDataEntries(formData) {
  const normalizedEntries = [];
  let entries;
  try {
    entries = formData?.entries?.();
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read FormData entries.', error);
    return normalizedEntries;
  }
  if (!entries) return normalizedEntries;

  try {
    for (const entry of entries) {
      const normalizedEntry = normalizeFormDataEntry(entry);
      if (normalizedEntry) normalizedEntries.push(normalizedEntry);
    }
  } catch (error) {
    Logger.system.warn('[HTMLeX] Stopped reading malformed FormData entries.', error);
  }
  return normalizedEntries;
}

function appendFormDataValue(formData, key, value) {
  try {
    formData?.append?.(key, value);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to append form value "${safeString(key)}".`, error);
  }
}

function getIterable(value, label) {
  try {
    if (value && typeof value[Symbol.iterator] === 'function') return value;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to inspect iterable ${label}.`, error);
  }

  return [];
}

function scheduleTimeout(callback, delayMs, label) {
  if (delayMs <= 0) return null;
  const setTimeoutFn = getGlobalFunction('setTimeout');
  if (!setTimeoutFn) {
    Logger.system.warn(`[HTMLeX] setTimeout is unavailable; ${label} cannot be delayed.`);
    return null;
  }

  try {
    return setTimeoutFn(callback, delayMs);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to schedule ${label}.`, error);
    return null;
  }
}

function clearScheduledTimeout(timeoutId, label) {
  if (timeoutId === null) return;
  const clearTimeoutFn = getGlobalFunction('clearTimeout');
  if (!clearTimeoutFn) return;

  try {
    clearTimeoutFn(timeoutId);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to clear ${label}.`, error);
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
        Logger.system.warn('[HTMLeX] Failed to remove retry abort listener.', error);
      }
    };
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to attach retry abort listener.', error);
    return () => {};
  }
}

function scheduleTargetUpdate(element, target, content, sequentialEntry = null, resolvedElement = null, afterUpdate = null, requestId = null) {
  const updateFn = () => {
    if (!sequentialEntry && requestId !== null && element._htmlexRequestId !== requestId) {
      if (afterUpdate) afterUpdate();
      return;
    }
    updateTarget(target, content, resolvedElement);
    if (afterUpdate) afterUpdate();
  };

  if (sequentialEntry) {
    sequentialEntry.updates ||= [];
    sequentialEntry.updates.push(updateFn);
    return;
  }

  scheduleUpdate(updateFn, isSequential(element));
}

function getTargetSelector(target) {
  return safeString(getObjectField(target, 'selector', '')).trim();
}

function isSelfTarget(target) {
  return getTargetSelector(target).toLowerCase() === 'this';
}

function resolveTargetElement(target, fallbackElement) {
  if (isSelfTarget(target)) {
    return fallbackElement;
  }

  return querySelectorSafe(getTargetSelector(target)) || fallbackElement;
}

function resolveSelfTargetElement(target, fallbackElement) {
  return isSelfTarget(target) ? fallbackElement : null;
}

function appendControlValue(formData, control) {
  const controlName = getObjectField(control, 'name', '');
  if (!controlName || getObjectField(control, 'disabled', false)) return;
  const controlType = safeString(getObjectField(control, 'type', '')).toLowerCase();
  if ((controlType === 'checkbox' || controlType === 'radio') && !getObjectField(control, 'checked', false)) return;
  if (isSelectControl(control) && getObjectField(control, 'multiple', false)) {
    for (const option of getIterable(getObjectField(control, 'selectedOptions', []), 'selectedOptions')) {
      appendFormDataValue(formData, controlName, getObjectField(option, 'value', ''));
    }
    return;
  }
  if (isInputControl(control) && controlType === 'file') {
    for (const file of getIterable(getObjectField(control, 'files', []), 'files')) {
      appendFormDataValue(formData, controlName, file);
    }
    return;
  }
  appendFormDataValue(formData, controlName, getObjectField(control, 'value', ''));
}

function isInstanceOf(value, constructorValue) {
  if (typeof constructorValue !== 'function') return false;
  try {
    return value instanceof constructorValue;
  } catch {
    return false;
  }
}

function getElementTagName(element) {
  return safeString(getObjectField(element, 'tagName', '')).trim().toLowerCase();
}

function isInputControl(control) {
  return getElementTagName(control) === 'input' || isInstanceOf(control, getGlobalField('HTMLInputElement'));
}

function isSelectControl(control) {
  return getElementTagName(control) === 'select' || isInstanceOf(control, getGlobalField('HTMLSelectElement'));
}

function isFormElement(element) {
  return getElementTagName(element) === 'form';
}

function isSubmittableControl(element) {
  const tagName = getElementTagName(element);
  return tagName === 'input' || tagName === 'select' || tagName === 'textarea' ||
    isInputControl(element) ||
    isSelectControl(element);
}

function matchesSubmittableControl(element) {
  try {
    return typeof element?.matches === 'function'
      ? element.matches('input, select, textarea')
      : isSubmittableControl(element);
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to test whether an element is submittable.', error);
    return isSubmittableControl(element);
  }
}

function querySubmittableControls(element) {
  try {
    return typeof element?.querySelectorAll === 'function'
      ? element.querySelectorAll('input, select, textarea')
      : [];
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to collect descendant form controls.', error);
    return [];
  }
}

function appendElementValues(formData, element) {
  if (!element) return;

  if (isFormElement(element)) {
    const FormDataConstructor = getGlobalField('FormData');
    if (typeof FormDataConstructor !== 'function') {
      Logger.system.warn('[HTMLeX] FormData is unavailable; skipping form element serialization.');
      return;
    }
    try {
      for (const [key, value] of new FormDataConstructor(element)) {
        appendFormDataValue(formData, key, value);
      }
    } catch (error) {
      Logger.system.warn('[HTMLeX] Failed to read FormData from form element.', error);
    }
    return;
  }

  if (matchesSubmittableControl(element)) {
    appendControlValue(formData, element);
  }

  for (const input of querySubmittableControls(element)) {
    appendControlValue(formData, input);
  }
}

const collectSelectorResults = selectors => (
  selectors.map(selector => querySelectorAllResult(selector))
);

const flattenSelectorMatches = results => results.flatMap(({ matches }) => matches);

function resolveSourceElements(sourceAttribute) {
  const sourceExpression = safeString(sourceAttribute).trim();
  if (!sourceExpression) return [];

  const selectors = sourceExpression.includes(',')
    ? sourceExpression.split(',').map(selector => selector.trim()).filter(Boolean)
    : [sourceExpression];
  const results = collectSelectorResults(selectors);
  const matchedElements = flattenSelectorMatches(results);

  if (matchedElements.length || sourceExpression.includes(',') || results.every(result => result.valid)) {
    return matchedElements;
  }

  const fallbackResults = collectSelectorResults(sourceExpression.split(/\s+/).filter(Boolean));
  return fallbackResults.every(result => result.valid)
    ? flattenSelectorMatches(fallbackResults)
    : [];
}

function appendExtras(formData, extrasAttribute) {
  for (const pair of safeString(extrasAttribute).split(/\s+/).filter(Boolean)) {
    const separatorIndex = pair.indexOf('=');
    const key = separatorIndex >= 0 ? pair.slice(0, separatorIndex) : pair;
    const value = separatorIndex >= 0 ? pair.slice(separatorIndex + 1) : '';
    Logger.system.debug(`Processing extra: ${key} = ${value}`);
    if (key) {
      appendFormDataValue(formData, key, value);
    }
  }
}

function serializeFormDataValue(value) {
  const FileConstructor = getGlobalField('File');
  if (isInstanceOf(value, FileConstructor)) {
    return {
      file: getObjectField(value, 'name', ''),
      size: getObjectField(value, 'size', 0),
      type: getObjectField(value, 'type', ''),
      lastModified: getObjectField(value, 'lastModified', 0)
    };
  }

  return safeString(value);
}

function encodeQueryComponent(value) {
  const text = safeString(value);
  try {
    return encodeURIComponent(text);
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to encode query value; replacing invalid Unicode.', error);
  }

  try {
    return encodeURIComponent(text.replace(/[\uD800-\uDFFF]/gu, '\uFFFD'));
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to encode sanitized query value; using an empty component.', error);
    return '';
  }
}

function isBinaryFormDataValue(value) {
  const FileConstructor = getGlobalField('File');
  const BlobConstructor = getGlobalField('Blob');
  return (
    isInstanceOf(value, FileConstructor) ||
    isInstanceOf(value, BlobConstructor)
  );
}

function formDataHasBinaryValue(formData) {
  for (const [, value] of getFormDataEntries(formData)) {
    if (isBinaryFormDataValue(value)) return true;
  }
  return false;
}

function buildCacheKey(method, url, formData) {
  if (method === 'GET') {
    return `${method} ${url}`;
  }

  const entries = [];
  for (const [key, value] of getFormDataEntries(formData)) {
    entries.push([key, serializeFormDataValue(value)]);
  }
  return `${method} ${url} ${JSON.stringify(entries)}`;
}

function createFormData() {
  const FormDataConstructor = getGlobalField('FormData');
  if (typeof FormDataConstructor === 'function') {
    try {
      return new FormDataConstructor();
    } catch (error) {
      Logger.system.warn('[HTMLeX] Failed to create FormData; using fallback form data.', error);
    }
  }

  const entries = [];
  return {
    append(key, value) {
      entries.push([key, value]);
    },
    entries() {
      return entries[Symbol.iterator]();
    },
    [Symbol.iterator]() {
      return this.entries();
    }
  };
}

function encodeFormDataParams(formData) {
  const URLSearchParamsConstructor = getGlobalField('URLSearchParams');
  if (typeof URLSearchParamsConstructor === 'function') {
    try {
      return new URLSearchParamsConstructor(formData).toString();
    } catch (error) {
      Logger.system.warn('[HTMLeX] URLSearchParams failed for FormData; falling back to manual query serialization.', error);
    }
  }

  const encodedPairs = [];
  for (const [key, value] of getFormDataEntries(formData)) {
    encodedPairs.push(`${encodeQueryComponent(key)}=${encodeQueryComponent(value)}`);
  }
  return encodedPairs.join('&');
}

function resetResponseState(element) {
  element._htmlexFragmentsProcessed = false;
  element._htmlexFallbackUpdated = false;
  element._htmlexDefaultUpdated = false;
  element._htmlexFragmentErrorStatus = null;
  element._htmlexStreamingActive = false;
  element._htmlexStreaming = false;
}

function runHook(element, hookName, event = null) {
  runLifecycleHook(element, hookName, event);
}

function getResponseBufferLimit(element) {
  const rawLimit = getElementAttribute(element, 'maxresponsechars') ??
    getElementAttribute(element, 'max-response-chars') ??
    getElementAttribute(element, 'maxresponsebuffer') ??
    getElementAttribute(element, 'max-response-buffer');
  const limit = parseNonNegativeInteger(rawLimit, 0);
  return Number.isFinite(limit) && limit > 0 ? limit : DEFAULT_RESPONSE_BUFFER_LIMIT_CHARS;
}

function parseNonNegativeInteger(value, defaultValue = 0) {
  const normalizedValue = safeString(value).trim();
  if (!/^\d+$/u.test(normalizedValue)) return defaultValue;

  const parsed = Number.parseInt(normalizedValue, 10);
  return Number.isSafeInteger(parsed) ? parsed : defaultValue;
}

function parseNonNegativeNumber(value, defaultValue = 0) {
  const normalizedValue = safeString(value).trim();
  if (!/^(?:\d+|\d*\.\d+)$/u.test(normalizedValue)) return defaultValue;

  const parsed = Number(normalizedValue);
  return Number.isFinite(parsed) ? parsed : defaultValue;
}

function normalizeActionOptions(extraOptions) {
  const htmlexSequentialEntry = getObjectField(extraOptions, 'htmlexSequentialEntry', null);
  const htmlexEvent = getObjectField(extraOptions, 'htmlexEvent', null);
  const fetchOptions = {};

  if (!extraOptions || typeof extraOptions !== 'object') {
    return { htmlexSequentialEntry, htmlexEvent, fetchOptions };
  }

  let optionKeys;
  try {
    optionKeys = Object.keys(extraOptions);
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to inspect action options.', error);
    optionKeys = [];
  }

  for (const optionKey of optionKeys) {
    if (optionKey === 'htmlexSequentialEntry' || optionKey === 'htmlexEvent') continue;
    if (RESERVED_FETCH_OPTION_KEYS.has(optionKey)) {
      Logger.system.warn(`[HTMLeX] Ignoring unsafe fetch option key "${optionKey}".`);
      continue;
    }
    fetchOptions[optionKey] = getObjectField(extraOptions, optionKey);
  }

  return { htmlexSequentialEntry, htmlexEvent, fetchOptions };
}

function appendChunkWithLimit(chunks, currentLength, chunk, limitChars) {
  const nextLength = currentLength + chunk.length;
  if (nextLength > limitChars) {
    throw new ResponseBufferLimitError(limitChars);
  }

  chunks.push(chunk);
  return nextLength;
}

function appendBufferWithLimit(buffer, chunk, limitChars) {
  if (buffer.length + chunk.length > limitChars) {
    throw new ResponseBufferLimitError(limitChars);
  }

  return buffer + chunk;
}

function createSwapLifecycle(element, afterSwapComplete = null, event = null) {
  if (!hasElementAttribute(element, 'onafterSwap') && !afterSwapComplete) return null;

  let pending = 0;
  let scheduled = false;
  let schedulingFinished = false;
  let completed = false;

  const maybeComplete = () => {
    if (!schedulingFinished || pending > 0 || completed || !scheduled) return;
    completed = true;
    element._htmlexOnAfterDeferred = false;
    runHook(element, 'onafterSwap', event);
    if (afterSwapComplete) {
      afterSwapComplete();
    }
  };

  return {
    createUpdateCallback() {
      scheduled = true;
      pending += 1;
      if (afterSwapComplete) {
        element._htmlexOnAfterDeferred = true;
      }

      let called = false;
      return () => {
        if (called) return;
        called = true;
        pending -= 1;
        maybeComplete();
      };
    },
    finishScheduling() {
      schedulingFinished = true;
      maybeComplete();
    }
  };
}

function replayResponseText(element, responseText, sequentialEntry = null, afterSwapComplete = null, event = null, requestId = null) {
  resetResponseState(element);
  const swapLifecycle = createSwapLifecycle(element, afterSwapComplete, event);
  const remainingContent = processFragmentBuffer(responseText, element, sequentialEntry, swapLifecycle);

  if (!element._htmlexFragmentsProcessed && safeString(remainingContent).trim() !== '' && hasElementAttribute(element, 'target')) {
    const targets = parseTargets(getElementAttribute(element, 'target'));
    for (const target of targets) {
      const resolvedElement = resolveTargetElement(target, element);
      const afterSwap = swapLifecycle?.createUpdateCallback();
      scheduleTargetUpdate(element, target, remainingContent, sequentialEntry, resolvedElement, afterSwap, requestId);
    }
    element._htmlexFallbackUpdated = true;
  }

  swapLifecycle?.finishScheduling();
  return responseText;
}

function emitSignalWithDelay(element, signalName, delay, context) {
  if (!signalName) return;
  if (delay > 0) {
    element._htmlexDelayedSignalTimers ||= new Set();
    const registrationToken = element._htmlexRegistrationToken;
    const timerId = scheduleTimeout(() => {
      element._htmlexDelayedSignalTimers?.delete(timerId);
      if (!isElementConnected(element)) return;
      if (registrationToken && element._htmlexRegistrationToken !== registrationToken) return;
      Logger.system.info(`Emitting signal "${safeString(signalName)}" after ${delay}ms delay (${safeString(context)}).`);
      emitSignal(signalName);
    }, delay, `delayed signal "${safeString(signalName)}"`);
    if (timerId === null) return;
    element._htmlexDelayedSignalTimers.add(timerId);
    return;
  }

  Logger.system.info(`Emitting signal "${safeString(signalName)}" immediately (${safeString(context)}).`);
  emitSignal(signalName);
}

function emitHeaderSignal(element, response) {
  if (!response?.headers) return;

  let headerValue;
  try {
    headerValue = response.headers.get?.('Emit');
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read Emit response header.', error);
    return;
  }
  if (!headerValue) return;

  Logger.system.info(`Received Emit header: ${safeString(headerValue)}`);
  const headerParts = safeString(headerValue).split(';').map(part => part.trim()).filter(Boolean);
  const signalName = headerParts[0] || '';
  let delayMs = 0;

  for (const param of headerParts.slice(1)) {
    const [name, value] = param.split('=');
    if (name.trim().toLowerCase() === 'delay') {
      delayMs = parseNonNegativeInteger(value, 0);
    }
  }

  emitSignalWithDelay(element, signalName, delayMs, 'Emit header');
}

function emitPublishSignal(element) {
  if (!hasElementAttribute(element, 'publish')) return;

  const publishSignal = getElementAttribute(element, 'publish');
  Logger.system.info(`Emitting signal "${safeString(publishSignal)}" after successful API call.`);
  emitSignal(publishSignal);

  if (!hasElementAttribute(element, 'timer')) return;

  const timerAttribute = getElementAttribute(element, 'timer');
  const delay = parseNonNegativeInteger(timerAttribute, null);
  if (delay === null) {
    Logger.system.warn(`[HTMLeX Warning] Ignoring invalid delayed publish timer "${safeString(timerAttribute)}".`);
    return;
  }

  emitSignalWithDelay(element, publishSignal, delay, 'publish timer');
}

function runSuccessSideEffects(element, response = null) {
  handleURLState(element);
  emitHeaderSignal(element, response);
  emitPublishSignal(element);
}

function getFragmentErrorStatus(element) {
  const status = parseNonNegativeInteger(element?._htmlexFragmentErrorStatus, null);
  return status !== null && status >= 400 ? status : null;
}

function getNumericAttribute(element, attributeNames, defaultValue, validator = value => Number.isFinite(value)) {
  for (const attributeName of attributeNames) {
    if (!hasElementAttribute(element, attributeName)) continue;
    const value = parseNonNegativeNumber(getElementAttribute(element, attributeName), defaultValue);
    return validator(value) ? value : defaultValue;
  }

  return defaultValue;
}

function createAbortError(reason) {
  if (isInstanceOf(reason, getGlobalField('Error'))) return reason;
  const error = new Error('Retry delay aborted');
  error.name = 'AbortError';
  return error;
}

function isSignalAborted(signal) {
  try {
    return Boolean(signal?.aborted);
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read retry abort signal state.', error);
    return false;
  }
}

function getSignalReason(signal) {
  try {
    return signal?.reason;
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read retry abort signal reason.', error);
    return undefined;
  }
}

function isElementConnected(element) {
  try {
    if (typeof document === 'undefined' || typeof document.body?.contains !== 'function') return true;
    return document.body.contains(element);
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to determine whether an element is connected.', error);
    return false;
  }
}

function waitForRetryDelay(delayMs, signal = null) {
  if (delayMs <= 0) return;

  return new Promise((resolve, reject) => {
    if (isSignalAborted(signal)) {
      reject(createAbortError(getSignalReason(signal)));
      return;
    }

    let removeAbortListener = () => {};
    const timeoutId = scheduleTimeout(() => {
      removeAbortListener();
      resolve();
    }, delayMs, 'retry delay');
    if (timeoutId === null) {
      resolve();
      return;
    }
    const abortListener = () => {
      clearScheduledTimeout(timeoutId, 'retry delay');
      removeAbortListener();
      reject(createAbortError(getSignalReason(signal)));
    };
    removeAbortListener = addAbortListener(signal, abortListener);
  });
}

function getRetryDelayForAttempt(element, failedAttemptIndex) {
  const baseDelayMs = getNumericAttribute(
    element,
    ['retrydelay', 'retry-delay'],
    0,
    value => Number.isFinite(value) && value >= 0
  );
  const backoff = getNumericAttribute(
    element,
    ['retrybackoff', 'retry-backoff'],
    1,
    value => Number.isFinite(value) && value >= 1
  );
  const maxDelayMs = getNumericAttribute(
    element,
    ['retrymaxdelay', 'retry-max-delay'],
    Number.POSITIVE_INFINITY,
    value => Number.isFinite(value) && value >= 0
  );
  const scaledDelay = baseDelayMs * (backoff ** failedAttemptIndex);
  return Math.min(scaledDelay, maxDelayMs);
}

/**
 * Processes a streaming API response.
 * Reads chunks as they arrive from an open connection, accumulating a buffer.
 * For every chunk, any complete <fragment> blocks are extracted and processed.
 * We count chunks so that if more than one chunk is received, we mark the response as streaming
 * (which will cause fragment updates to be applied immediately, bypassing sequential queuing).
 * After the stream ends, any remaining text is used as a fallback update.
 *
 * @param {Response} response - The fetch response.
 * @param {Element} triggeringElement - The element that triggered the API call.
 * @param {object|null} [sequentialEntry=null] - Request-specific queue for sequential updates.
 * @returns {Promise<string>} The final leftover text from the stream.
 */
export async function processResponse(response, triggeringElement, sequentialEntry = null, afterSwapComplete = null, event = null, requestId = null) {
  Logger.system.debug("Starting to process response stream.");

  let chunkCount = 0;
  resetResponseState(triggeringElement);
  triggeringElement._htmlexStreamingActive = true;

  if (!response.body) {
    triggeringElement._htmlexStreamingActive = false;
    triggeringElement._htmlexStreaming = false;
    return '';
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let fragmentBuffer = '';
  const responseTextChunks = [];
  let responseTextLength = 0;
  let retainResponseText = true;
  const shouldCacheResponse = hasElementAttribute(triggeringElement, 'cache');
  const responseBufferLimit = getResponseBufferLimit(triggeringElement);
  const swapLifecycle = createSwapLifecycle(triggeringElement, afterSwapComplete, event);
  const releaseResponseTextIfFragmentOnly = () => {
    if (shouldCacheResponse || !triggeringElement._htmlexFragmentsProcessed) return;
    responseTextChunks.length = 0;
    responseTextLength = 0;
    retainResponseText = false;
  };

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        Logger.system.debug("Stream reading complete.");
        break;
      }

      chunkCount += 1;
      triggeringElement._htmlexStreaming = chunkCount > 1;

      const chunk = decoder.decode(value, { stream: true });
      if (retainResponseText) {
        responseTextLength = appendChunkWithLimit(responseTextChunks, responseTextLength, chunk, responseBufferLimit);
      }
      fragmentBuffer = processFragmentBuffer(
        appendBufferWithLimit(fragmentBuffer, chunk, responseBufferLimit),
        triggeringElement,
        sequentialEntry,
        swapLifecycle
      );
      releaseResponseTextIfFragmentOnly();
      Logger.system.debug(`Processed chunk #${chunkCount}. Remaining buffer length:`, fragmentBuffer.length);
    }

    const finalChunk = decoder.decode();
    if (finalChunk) {
      if (retainResponseText) {
        responseTextLength = appendChunkWithLimit(responseTextChunks, responseTextLength, finalChunk, responseBufferLimit);
      }
      fragmentBuffer = processFragmentBuffer(
        appendBufferWithLimit(fragmentBuffer, finalChunk, responseBufferLimit),
        triggeringElement,
        sequentialEntry,
        swapLifecycle
      );
      releaseResponseTextIfFragmentOnly();
    }

    if (!triggeringElement._htmlexFragmentsProcessed && safeString(fragmentBuffer).trim() !== '' && hasElementAttribute(triggeringElement, 'target')) {
      Logger.system.debug("No fragments processed; performing fallback update with leftover text.");
      const targets = parseTargets(getElementAttribute(triggeringElement, 'target'));
      for (const target of targets) {
        const resolvedElement = resolveTargetElement(target, triggeringElement);

        if (resolvedElement === triggeringElement && !isSelfTarget(target)) {
          Logger.system.debug(`Fallback: No element found for selector "${getTargetSelector(target)}". Using triggering element.`);
        }

        Logger.system.debug("Applying fallback update to target:", target, "resolved as:", resolvedElement);
        const afterSwap = swapLifecycle?.createUpdateCallback();
        scheduleTargetUpdate(triggeringElement, target, fragmentBuffer, sequentialEntry, resolvedElement, afterSwap, requestId);
      }
      triggeringElement._htmlexFallbackUpdated = true;
    }

    swapLifecycle?.finishScheduling();
    Logger.system.debug("Completed processing response stream. Final leftover buffer:", fragmentBuffer);
    return retainResponseText ? responseTextChunks.join('') : '';
  } catch (error) {
    try {
      await reader.cancel(error);
    } catch (cancelError) {
      Logger.system.debug('Unable to cancel response reader after processing error:', cancelError);
    }
    throw error;
  } finally {
    triggeringElement._htmlexStreamingActive = false;
    triggeringElement._htmlexStreaming = false;
  }
}

/**
 * Handles an API action including lifecycle hooks, extras, caching,
 * URL state updates, publish signal emission, and polling.
 *
 * @param {Element} element - The element triggering the action.
 * @param {string} method - The HTTP method (e.g., "GET", "POST").
 * @param {string} endpoint - The API endpoint.
 * @param {object} [extraOptions={}] - Extra options to merge into the fetch options.
 */
export async function handleAction(element, method, endpoint, extraOptions = {}) {
  Logger.system.debug("handleAction invoked for element:", element);
  const { htmlexSequentialEntry, htmlexEvent, fetchOptions } = normalizeActionOptions(extraOptions);
  const requestMethod = safeString(method).trim().toUpperCase();
  const requestEndpoint = safeString(endpoint).trim();
  element._htmlexOnAfterDeferred = false;
  const requestId = (element._htmlexRequestId || 0) + 1;
  element._htmlexRequestId = requestId;
  element._htmlexRequestPending = true;
  const completeCurrentRequest = () => {
    if (element._htmlexRequestId === requestId) {
      element._htmlexRequestPending = false;
    }
  };
  let afterHookRan = false;
  const runAfterHook = () => {
    if (getFragmentErrorStatus(element) !== null) return;
    if (afterHookRan) return;
    afterHookRan = true;
    runHook(element, 'onafter', htmlexEvent);
  };

  // Early guard: if polling is disabled, abort further API calls.
  if (element._pollDisabled) {
    Logger.system.info("Polling has been disabled for this element; aborting API call.");
    completeCurrentRequest();
    return;
  }

  // Lifecycle hook: onbefore (before API call starts)
  runHook(element, 'onbefore', htmlexEvent);

  Logger.system.info(`Handling ${requestMethod} action for endpoint: ${requestEndpoint}`);

  const formData = createFormData();
  appendElementValues(formData, element);

  if (hasElementAttribute(element, 'source')) {
    for (const sourceElement of resolveSourceElements(getElementAttribute(element, 'source'))) {
      appendElementValues(formData, sourceElement);
    }
  }

  // Process extras (inline parameters)
  if (hasElementAttribute(element, 'extras')) {
    appendExtras(formData, getElementAttribute(element, 'extras'));
  }

  // If a loading state is desired, update the loading target immediately.
  if (hasElementAttribute(element, 'loading')) {
    const loadingTargets = parseTargets(getElementAttribute(element, 'loading'));
    for (const target of loadingTargets) {
      Logger.system.debug("Updating loading target:", target);
      scheduleUpdate(() => {
        if (element._htmlexRequestId === requestId && element._htmlexRequestPending) {
          const resolvedElement = resolveSelfTargetElement(target, element);
          updateTarget(target, '<div class="loading">Loading...</div>', resolvedElement);
        }
      }, isSequential(element));
    }
  }

  // Merge caller-provided fetch options into our request options.
  const requestOptions = { ...fetchOptions, method: requestMethod };
  let url = requestEndpoint;
  if (requestMethod === 'GET') {
    const params = encodeFormDataParams(formData);
    if (params) {
      url += (url.includes('?') ? '&' : '?') + params;
    }
    Logger.system.debug("GET request URL with params:", url);
  } else {
    requestOptions.body = formData;
    Logger.system.debug("Non-GET request, using FormData body.");
  }
  const cacheKey = buildCacheKey(requestMethod, url, formData);
  const hasCacheAttribute = hasElementAttribute(element, 'cache');
  const canUseCache = hasCacheAttribute && !formDataHasBinaryValue(formData);

  // Caching support.
  if (canUseCache) {
    const cached = getCache(cacheKey);
    if (cached !== null) {
      Logger.system.info(`Using cached response for: ${safeString(url)}`);
      runHook(element, 'onbeforeSwap', htmlexEvent);
      replayResponseText(element, cached, htmlexSequentialEntry, runAfterHook, htmlexEvent, requestId);
      completeCurrentRequest();
      runSuccessSideEffects(element);
      if (!element._htmlexOnAfterDeferred) {
        runAfterHook();
      }
      return;
    }
  } else if (hasCacheAttribute) {
    Logger.system.debug('Skipping cache for request with binary FormData payload.');
  }

  const timeoutMs = parseNonNegativeInteger(getElementAttribute(element, 'timeout'), 0);
  const retryCount = parseNonNegativeInteger(getElementAttribute(element, 'retry'), 0);
  let responseText = null;
  let response = null;

  Logger.system.debug("Initiating fetch attempts. Timeout:", timeoutMs, "Retry count:", retryCount);
  for (let attempt = 0; attempt <= retryCount; attempt++) {
    try {
      Logger.system.debug(`Attempt ${attempt + 1}: Fetching URL ${safeString(url)}`);
      response = await fetchWithTimeout(url, requestOptions, timeoutMs);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status} ${response.statusText}`.trim());
      }

      // Lifecycle hook: onbeforeSwap (before DOM update)
      runHook(element, 'onbeforeSwap', htmlexEvent);

      // Process the response.
      responseText = await processResponse(response, element, htmlexSequentialEntry, runAfterHook, htmlexEvent, requestId);
      completeCurrentRequest();
      Logger.system.debug("Fetch and processing succeeded on attempt", attempt + 1);
      break;
    } catch (error) {
      const errorMessage = getErrorMessage(error);
      Logger.system.warn(`Attempt ${attempt + 1} failed: ${errorMessage}`);
      if (getObjectField(error, 'name') === 'AbortError' || isSignalAborted(fetchOptions.signal)) {
        completeCurrentRequest();
        return;
      }
      if (attempt === retryCount) {
        completeCurrentRequest();
        if (hasElementAttribute(element, 'onerror')) {
          const errorTargets = parseTargets(getElementAttribute(element, 'onerror'));
          for (const target of errorTargets) {
            const resolvedElement = resolveSelfTargetElement(target, element);
            Logger.system.debug("Updating error target after failure:", target);
            scheduleTargetUpdate(
              element,
              target,
              `<div class="error">Error: ${escapeHtml(errorMessage)}</div>`,
              htmlexSequentialEntry,
              resolvedElement,
              null,
              requestId
            );
          }
        }
        return;
      }

      try {
        const retryDelayMs = getRetryDelayForAttempt(element, attempt);
        if (retryDelayMs > 0) {
          Logger.system.info(`Waiting ${retryDelayMs}ms before retry attempt ${attempt + 2}.`);
          await waitForRetryDelay(retryDelayMs, fetchOptions.signal);
        }
      } catch (delayError) {
        if (getObjectField(delayError, 'name') === 'AbortError' || isSignalAborted(fetchOptions.signal)) {
          completeCurrentRequest();
          return;
        }
        throw delayError;
      }
    }
  }

  // Fallback update if streaming wasn't used.
  if (hasElementAttribute(element, 'target') && !element._htmlexFragmentsProcessed && !element._htmlexFallbackUpdated && responseText) {
    const swapLifecycle = createSwapLifecycle(element, runAfterHook, htmlexEvent);
    const targets = parseTargets(getElementAttribute(element, 'target'));
    for (const target of targets) {
      const resolvedElement = resolveTargetElement(target, element);
      if (isSelfTarget(target)) {
        Logger.system.debug("Fallback: target selector is 'this'; using triggering element.");
      } else if (resolvedElement === element) {
        Logger.system.debug(`No element found for selector "${getTargetSelector(target)}". Falling back to triggering element.`);
      }
      Logger.system.debug("Fallback updating target:", target, "resolved as:", resolvedElement);
      const afterSwap = swapLifecycle?.createUpdateCallback();
      scheduleTargetUpdate(element, target, responseText, htmlexSequentialEntry, resolvedElement, afterSwap, requestId);
    }
    swapLifecycle?.finishScheduling();
  }

  const fragmentErrorStatus = getFragmentErrorStatus(element);
  if (fragmentErrorStatus !== null) {
    Logger.system.warn(`Response fragment indicated error status ${fragmentErrorStatus}; skipping success side effects.`);
    return;
  }

  runSuccessSideEffects(element, response);

  if (canUseCache) {
    const cacheTtl = parseNonNegativeInteger(getElementAttribute(element, 'cache'), Number.NaN);
    setCache(cacheKey, responseText, cacheTtl);
    Logger.system.debug("Response cached with TTL:", cacheTtl);
  }

  if (!element._htmlexOnAfterDeferred) {
    runAfterHook();
  }

}
