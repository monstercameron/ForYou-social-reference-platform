// src/htmlex.js
/**
 * @module HTMLeX
 * @description Entry point for HTMLeX, a declarative, server-driven UI framework.
 */

import { installRuntimeErrorBoundary, Logger } from './logger.js';
import { installLifecycleHookGlobal } from './hooks.js';

installRuntimeErrorBoundary();
installLifecycleHookGlobal();
Logger.system.debug("[HTMLeX] Entry point module loaded.");

export { initHTMLeX } from './registration.js';
export {
  createHTMLeXElementClass,
  defineHTMLeXElement
} from './webComponentAdapter.js';
export {
  createLifecycleHookScope,
  getLifecycleHookNames,
  hooks,
  registerLifecycleHook,
  unregisterLifecycleHook
} from './hooks.js';
