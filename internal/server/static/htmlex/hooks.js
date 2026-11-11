/**
 * @module Hooks
 * @description Safe lifecycle hook registration and dispatch for HTMLeX actions.
 */

import { Logger } from './logger.js';

export const LIFECYCLE_HOOK_EVENT = 'htmlex:hook';

const GLOBAL_API_NAME = 'HTMLeX';
const DEFAULT_HOOK_SCOPE = 'global';
const VALID_HOOK_NAME_PATTERN = /^[A-Za-z][\w:.-]*$/u;
const SCRIPT_LIKE_HOOK_PATTERN = /[=;(){}]|\b(?:document|eval|fetch|Function|globalThis|import|localStorage|sessionStorage|window)\b/u;
const HOOK_SCOPE_ATTRIBUTE_NAMES = ['hookscope', 'data-htmlex-hook-scope'];
const lifecycleHooks = new Map();
const TEST_CLEAR_TOKEN = Symbol('HTMLeX.testClearToken');

function safeString(value, fallback = '') {
  try {
    return String(value ?? fallback);
  } catch (error) {
    Logger.system.warn('[HOOKS] Failed to coerce lifecycle hook value to string.', error);
    return fallback;
  }
}

function getObjectField(value, fieldName, fallback = undefined) {
  try {
    return value?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[HOOKS] Failed to read lifecycle hook field "${fieldName}".`, error);
    return fallback;
  }
}

function setObjectField(value, fieldName, nextValue) {
  try {
    value[fieldName] = nextValue;
    return true;
  } catch (error) {
    Logger.system.warn(`[HOOKS] Failed to set lifecycle hook field "${fieldName}".`, error);
    return false;
  }
}

function getHookOption(options, fieldName, fallback = undefined) {
  return getObjectField(normalizeHookOptions(options), fieldName, fallback);
}

function parseHookNames(rawValue) {
  return safeString(rawValue)
    .split(/[\s,]+/u)
    .map(name => name.trim())
    .filter(Boolean);
}

function normalizeHookEventName(hookAttributeName) {
  return `htmlex:${safeString(hookAttributeName).toLowerCase()}`;
}

function normalizeHookScope(scope = DEFAULT_HOOK_SCOPE) {
  const rawScope = scope || DEFAULT_HOOK_SCOPE;
  const normalizedScope = safeString(
    rawScope,
    rawScope === DEFAULT_HOOK_SCOPE ? DEFAULT_HOOK_SCOPE : ''
  ).trim();
  if (!VALID_HOOK_NAME_PATTERN.test(normalizedScope)) {
    throw new TypeError(`Invalid HTMLeX lifecycle hook scope "${normalizedScope}".`);
  }
  return normalizedScope;
}

function normalizeHookOptions(options) {
  return options && typeof options === 'object' ? options : {};
}

function getRuntimeWindow() {
  try {
    return typeof window === 'undefined' ? globalThis.window : window;
  } catch (error) {
    Logger.system.warn('[HOOKS] Failed to read runtime window.', error);
    return null;
  }
}

function hasElementAttribute(element, attributeName) {
  try {
    return Boolean(element?.hasAttribute?.(attributeName));
  } catch (error) {
    Logger.system.warn(`[HOOKS] Failed to read lifecycle hook attribute "${attributeName}".`, error);
    return false;
  }
}

function getElementAttribute(element, attributeName) {
  try {
    return element?.getAttribute?.(attributeName) ?? null;
  } catch (error) {
    Logger.system.warn(`[HOOKS] Failed to read lifecycle hook attribute "${attributeName}".`, error);
    return null;
  }
}

function getElementHookParent(element) {
  try {
    return getObjectField(element, 'parentElement', null) ||
      getObjectField(getObjectField(element, 'parentNode', null), 'host', null) ||
      null;
  } catch (error) {
    Logger.system.warn('[HOOKS] Failed to inspect lifecycle hook parent scope.', error);
    return null;
  }
}

function getHookKey(scope, hookName) {
  return `${scope}\u0000${hookName}`;
}

function getElementHookScope(element) {
  let current = element;
  while (current) {
    for (const attributeName of HOOK_SCOPE_ATTRIBUTE_NAMES) {
      if (hasElementAttribute(current, attributeName)) {
        const scope = safeString(getElementAttribute(current, attributeName)).trim();
        if (scope) {
          try {
            return normalizeHookScope(scope);
          } catch (error) {
            Logger.system.warn(`[HOOKS] Ignoring invalid lifecycle hook scope "${scope}".`, error);
            return DEFAULT_HOOK_SCOPE;
          }
        }
      }
    }
    current = getElementHookParent(current);
  }

  return DEFAULT_HOOK_SCOPE;
}

function dispatchLifecycleEvent(element, detail) {
  let CustomEventConstructor;
  try {
    CustomEventConstructor = globalThis.CustomEvent;
  } catch (error) {
    Logger.system.warn('[HOOKS] Failed to read CustomEvent constructor.', error);
    return;
  }
  const dispatchEvent = getObjectField(element, 'dispatchEvent', null);
  if (typeof CustomEventConstructor !== 'function' || typeof dispatchEvent !== 'function') return;

  try {
    dispatchEvent.call(element, new CustomEventConstructor(LIFECYCLE_HOOK_EVENT, {
      bubbles: true,
      detail
    }));
    dispatchEvent.call(element, new CustomEventConstructor(normalizeHookEventName(getObjectField(detail, 'hookAttributeName', '')), {
      bubbles: true,
      detail
    }));
  } catch (error) {
    Logger.system.warn('[HOOKS] Failed to dispatch lifecycle hook event.', error);
  }
}

function createHookContext({ element, event, hookAttributeName, hookName, hookScope, rawValue }) {
  return {
    element,
    event,
    hookAttributeName,
    hookName,
    hookScope,
    rawValue
  };
}

export function registerLifecycleHook(name, callback, options = {}) {
  const hookOptions = normalizeHookOptions(options);
  const hookName = safeString(name).trim();
  const hookScope = normalizeHookScope(getHookOption(hookOptions, 'scope'));
  if (!VALID_HOOK_NAME_PATTERN.test(hookName)) {
    throw new TypeError(`Invalid HTMLeX lifecycle hook name "${hookName}".`);
  }
  if (typeof callback !== 'function') {
    throw new TypeError(`HTMLeX lifecycle hook "${hookName}" must be a function.`);
  }
  const hookKey = getHookKey(hookScope, hookName);
  const existing = lifecycleHooks.get(hookKey);
  if (existing && !getHookOption(hookOptions, 'replace', false)) {
    throw new TypeError(
      `HTMLeX lifecycle hook "${hookName}" is already registered in scope "${hookScope}". ` +
      'Unregister it first or pass { replace: true }.'
    );
  }

  lifecycleHooks.set(hookKey, {
    callback,
    name: hookName,
    owner: getHookOption(hookOptions, 'owner', 'default') || 'default',
    scope: hookScope
  });
  Logger.system.debug(`[HOOKS] Registered lifecycle hook "${hookName}" in scope "${hookScope}".`);

  return () => unregisterLifecycleHook(hookName, { callback, scope: hookScope });
}

export function unregisterLifecycleHook(name, options = {}) {
  const hookOptions = normalizeHookOptions(options);
  const hookName = safeString(name).trim();
  const hookScope = normalizeHookScope(getHookOption(hookOptions, 'scope'));
  const hookKey = getHookKey(hookScope, hookName);
  const existing = lifecycleHooks.get(hookKey);
  const expectedCallback = getHookOption(hookOptions, 'callback', null);
  const expectedOwner = getHookOption(hookOptions, 'owner', null);
  if (expectedCallback && getObjectField(existing, 'callback', null) !== expectedCallback) {
    return false;
  }
  if (expectedOwner && getObjectField(existing, 'owner', null) !== expectedOwner) {
    return false;
  }
  if (lifecycleHooks.delete(hookKey)) {
    Logger.system.debug(`[HOOKS] Unregistered lifecycle hook "${hookName}" from scope "${hookScope}".`);
    return true;
  }
  return false;
}

function clearLifecycleHooks(token) {
  if (token !== TEST_CLEAR_TOKEN) {
    throw new TypeError('clearLifecycleHooks is reserved for tests and isolated runtime teardown.');
  }
  lifecycleHooks.clear();
  Logger.system.debug('[HOOKS] Cleared lifecycle hook registry.');
}

export function clearLifecycleHooksForTests() {
  clearLifecycleHooks(TEST_CLEAR_TOKEN);
}

export function getLifecycleHookNames(scope = DEFAULT_HOOK_SCOPE) {
  const hookScope = normalizeHookScope(scope);
  return [...lifecycleHooks.values()]
    .filter(record => getObjectField(record, 'scope') === hookScope)
    .map(record => getObjectField(record, 'name', ''))
    .filter(Boolean);
}

function getLifecycleHookRecord(hookName, hookScope) {
  return lifecycleHooks.get(getHookKey(hookScope, hookName)) ||
    (hookScope === DEFAULT_HOOK_SCOPE ? null : lifecycleHooks.get(getHookKey(DEFAULT_HOOK_SCOPE, hookName)));
}

export function createLifecycleHookScope(scope) {
  const hookScope = normalizeHookScope(scope);
  return Object.freeze({
    register(name, callback, options = {}) {
      const hookOptions = normalizeHookOptions(options);
      return registerLifecycleHook(name, callback, {
        owner: getHookOption(hookOptions, 'owner'),
        replace: getHookOption(hookOptions, 'replace'),
        scope: hookScope
      });
    },
    unregister(name, options = {}) {
      const hookOptions = normalizeHookOptions(options);
      return unregisterLifecycleHook(name, {
        callback: getHookOption(hookOptions, 'callback'),
        owner: getHookOption(hookOptions, 'owner'),
        scope: hookScope
      });
    },
    list() {
      return getLifecycleHookNames(hookScope);
    }
  });
}

export function runLifecycleHook(element, hookAttributeName, event = null) {
  const attributeName = safeString(hookAttributeName);
  if (!hasElementAttribute(element, attributeName)) return;

  const rawValue = getElementAttribute(element, attributeName);
  const hookNames = parseHookNames(rawValue);
  if (!hookNames.length) return;
  const hookScope = getElementHookScope(element);

  for (const hookName of hookNames) {
    const context = createHookContext({
      element,
      event,
      hookAttributeName: attributeName,
      hookName,
      hookScope,
      rawValue
    });

    if (!VALID_HOOK_NAME_PATTERN.test(hookName)) {
      Logger.system.warn(`[HOOKS] Ignoring invalid lifecycle hook name "${hookName}" on ${attributeName}.`);
      dispatchLifecycleEvent(element, context);
      continue;
    }

    const record = getLifecycleHookRecord(hookName, hookScope);
    const callback = getObjectField(record, 'callback', null);
    if (typeof callback === 'function') {
      try {
        Logger.system.debug(`[HOOKS] Running lifecycle hook "${hookName}" for ${attributeName}.`);
        callback(context);
      } catch (error) {
        Logger.system.error(`[HOOKS] Error in lifecycle hook "${hookName}":`, error);
      }
    } else if (SCRIPT_LIKE_HOOK_PATTERN.test(hookName)) {
      Logger.system.warn(
        `[HOOKS] Ignored script-like lifecycle hook value for ${attributeName}. ` +
        'Register a named hook with HTMLeX.hooks.register(name, callback) instead.'
      );
    } else {
      Logger.system.debug(`[HOOKS] No registered callback for lifecycle hook "${hookName}". Dispatching events only.`);
    }

    dispatchLifecycleEvent(element, context);
  }
}

export const hooks = Object.freeze({
  register: registerLifecycleHook,
  unregister: unregisterLifecycleHook,
  list: getLifecycleHookNames,
  scope: createLifecycleHookScope
});

export function installLifecycleHookGlobal() {
  const runtimeWindow = getRuntimeWindow();
  if (!runtimeWindow || typeof runtimeWindow !== 'object') return;

  try {
    const existingApi = getObjectField(runtimeWindow, GLOBAL_API_NAME, null);
    const nextApi = {};
    if (existingApi && typeof existingApi === 'object') {
      try {
        for (const key of Object.keys(existingApi)) {
          nextApi[key] = getObjectField(existingApi, key);
        }
      } catch (error) {
        Logger.system.warn('[HOOKS] Failed to copy existing lifecycle hook global API.', error);
      }
    }
    nextApi.hooks = hooks;
    setObjectField(runtimeWindow, GLOBAL_API_NAME, nextApi);
  } catch (error) {
    Logger.system.warn('[HOOKS] Failed to install lifecycle hook global API.', error);
  }
}
