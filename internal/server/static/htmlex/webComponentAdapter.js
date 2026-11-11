/**
 * @module WebComponentAdapter
 * @description Provides a formal custom-element bridge for HTMLeX registration.
 */

import { HTMLEX_ATTRIBUTE_NAMES } from './dom.js';
import { Logger } from './logger.js';
import { registerElement, unregisterElement } from './registration.js';

function getDefaultBaseElement() {
  try {
    if (typeof globalThis.HTMLElement === 'function') return globalThis.HTMLElement;
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read HTMLElement; using fallback base class.', error);
  }
  return class HTMLeXBaseElement {};
}

function safeString(value, fallback = '[Unstringifiable]') {
  try {
    return String(value ?? fallback);
  } catch {
    return fallback;
  }
}

function getField(target, fieldName, fallback = undefined) {
  try {
    return target?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read custom element field "${fieldName}".`, error);
    return fallback;
  }
}

function setField(target, fieldName, value) {
  try {
    if (target && typeof target === 'object') {
      target[fieldName] = value;
      return true;
    }
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to set custom element field "${fieldName}".`, error);
  }
  return false;
}

function isConnected(element) {
  try {
    return Boolean(element?.isConnected);
  } catch {
    return false;
  }
}

function queueTask(callback) {
  const runSafely = () => {
    try {
      callback();
    } catch (error) {
      Logger.system.error('[HTMLeX] Custom element registration task failed.', error);
    }
  };

  if (typeof globalThis.queueMicrotask === 'function') {
    try {
      globalThis.queueMicrotask(runSafely);
      return;
    } catch (error) {
      Logger.system.warn('[HTMLeX] queueMicrotask failed; falling back for custom element registration.', error);
    }
  }

  if (typeof globalThis.setTimeout === 'function') {
    try {
      globalThis.setTimeout(runSafely, 0);
      return;
    } catch (error) {
      Logger.system.warn('[HTMLeX] setTimeout failed; running custom element registration synchronously.', error);
    }
  }

  runSafely();
}

function scheduleRegistration(element) {
  if (getField(element, '_htmlexAdapterRegistrationQueued', false)) return;

  setField(element, '_htmlexAdapterRegistrationQueued', true);
  queueTask(() => {
    setField(element, '_htmlexAdapterRegistrationQueued', false);
    if (isConnected(element)) {
      safelyRunLifecycle('register queued custom element', () => registerElement(element));
    }
  });
}

function safelyRunLifecycle(description, callback) {
  try {
    callback();
  } catch (error) {
    Logger.system.error(`[HTMLeX] Failed to ${description}.`, error);
  }
}

function getCustomElementsRegistry() {
  try {
    return globalThis.customElements;
  } catch (error) {
    throw new Error('customElements is not available in this environment.', { cause: error });
  }
}

function getOptionField(options, fieldName) {
  try {
    return options?.[fieldName];
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read custom element option "${fieldName}".`, error);
    return undefined;
  }
}

export function createHTMLeXElementClass(BaseElement = getDefaultBaseElement()) {
  return class HTMLeXElement extends BaseElement {
    static get observedAttributes() {
      return [...HTMLEX_ATTRIBUTE_NAMES];
    }

    connectedCallback() {
      safelyRunLifecycle('register connected custom element', () => registerElement(this));
    }

    disconnectedCallback() {
      safelyRunLifecycle('unregister disconnected custom element', () => unregisterElement(this));
    }

    attributeChangedCallback() {
      if (isConnected(this)) {
        scheduleRegistration(this);
      }
    }

    htmlexRegister() {
      safelyRunLifecycle('manually register custom element', () => registerElement(this));
    }

    htmlexUnregister() {
      safelyRunLifecycle('manually unregister custom element', () => unregisterElement(this));
    }
  };
}

export function defineHTMLeXElement(name = 'htmlex-element', options = {}) {
  const registry = getCustomElementsRegistry();
  if (!registry || typeof registry.get !== 'function' || typeof registry.define !== 'function') {
    throw new Error('customElements is not available in this environment.');
  }
  const elementOptions = options && typeof options === 'object' ? options : {};

  let existingElement;
  try {
    existingElement = registry.get(name);
  } catch (error) {
    throw new Error(`Invalid custom element name "${safeString(name)}".`, { cause: error });
  }
  if (existingElement) return existingElement;

  const ElementClass = getOptionField(elementOptions, 'elementClass') ||
    createHTMLeXElementClass(getOptionField(elementOptions, 'baseClass'));
  try {
    registry.define(name, ElementClass);
  } catch (error) {
    throw new Error(`Unable to define custom element "${safeString(name)}".`, { cause: error });
  }
  return ElementClass;
}
