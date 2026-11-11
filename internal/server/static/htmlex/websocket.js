// src/websocket.js
/**
 * @module WebSocketHandler
 * @description Handles Socket.IO connections and integrates incoming messages.
 */

import { Logger } from './logger.js';
import { scheduleUpdate, isSequential } from './utils.js';
import { parseTargets, updateTarget } from './dom.js';

const ELEMENT_NODE_TYPE = 1;

function safeString(value, fallback = '') {
  try {
    return String(value ?? fallback);
  } catch (error) {
    Logger.system.warn('[Socket.IO] Failed to coerce value to string.', error);
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

function getGlobalField(name) {
  try {
    return globalThis[name];
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to read global ${name}.`, error);
    return undefined;
  }
}

function getRuntimeDocument() {
  try {
    return typeof document === 'undefined' ? getGlobalField('document') : document;
  } catch (error) {
    Logger.system.warn('[Socket.IO] Failed to read document.', error);
    return null;
  }
}

function getDocumentBody() {
  try {
    return getRuntimeDocument()?.body || null;
  } catch (error) {
    Logger.system.warn('[Socket.IO] Failed to read document body.', error);
    return null;
  }
}

function getObjectField(value, fieldName, fallback = undefined) {
  try {
    return value?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to read ${fieldName}.`, error);
    return fallback;
  }
}

function getElementField(element, fieldName) {
  try {
    return element?.[fieldName];
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to read element field ${fieldName}.`, error);
    return undefined;
  }
}

function setElementField(element, fieldName, value) {
  try {
    element[fieldName] = value;
    return true;
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to set element field ${fieldName}.`, error);
    return false;
  }
}

function deleteElementField(element, fieldName) {
  try {
    delete element[fieldName];
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to delete element field ${fieldName}.`, error);
  }
}

function hasElementAttribute(element, attributeName) {
  try {
    return Boolean(element?.hasAttribute?.(attributeName));
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to check ${attributeName} attribute.`, error);
    return false;
  }
}

function getElementAttribute(element, attributeName) {
  try {
    return element?.getAttribute?.(attributeName) ?? null;
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to read ${attributeName} attribute.`, error);
    return null;
  }
}

function renderChatMessage(message) {
  const username = getObjectField(message, 'username', 'Anonymous') || 'Anonymous';
  const text = getObjectField(message, 'text', '');

  return `
    <div class="surface-muted p-3 small mb-2">
      <strong class="me-2 text-primary">${escapeHtml(username)}</strong>
      <span>${escapeHtml(text)}</span>
    </div>
  `;
}

function renderChatHistory(data) {
  const history = getObjectField(data, 'history', []);
  if (!Array.isArray(history)) return '';

  let length;
  try {
    length = history.length;
  } catch (error) {
    Logger.system.warn('[Socket.IO] Failed to read chat history length.', error);
    return '';
  }

  const renderedMessages = [];
  for (let index = 0; index < length; index += 1) {
    try {
      renderedMessages.push(renderChatMessage(history[index] || {}));
    } catch (error) {
      Logger.system.warn('[Socket.IO] Failed to render chat history item.', error);
    }
  }
  return renderedMessages.join('');
}

function normalizeSocketPayload(eventName, data) {
  if (eventName === 'chatMessage') {
    return renderChatMessage(data || {});
  }

  if (eventName === 'chatHistory') {
    const renderedHistory = renderChatHistory(data);
    if (!renderedHistory) {
      return '<p class="text-center small text-subtle mb-0">Waiting for messages...</p>';
    }
    return renderedHistory;
  }

  if (typeof data === 'string') {
    return data;
  }

  try {
    return escapeHtml(JSON.stringify(data));
  } catch {
    return escapeHtml(data);
  }
}

function isElementLike(value) {
  if (!value || typeof value !== 'object') return false;

  const ElementConstructor = getGlobalField('Element');
  if (typeof ElementConstructor === 'function') {
    try {
      if (value instanceof ElementConstructor) return true;
    } catch {
      // Fall through to structural detection for hostile constructors.
    }
  }

  const NodeConstructor = getGlobalField('Node');
  const elementNodeType = getObjectField(NodeConstructor, 'ELEMENT_NODE', ELEMENT_NODE_TYPE);
  return getElementField(value, 'nodeType') === elementNodeType &&
    typeof getElementField(value, 'hasAttribute') === 'function';
}

function isElementConnected(element) {
  try {
    const body = getDocumentBody();
    if (typeof body?.contains !== 'function') return true;
    return body.contains(element);
  } catch (error) {
    Logger.system.warn('[Socket.IO] Failed to determine whether an element is connected.', error);
    return false;
  }
}

function getSocketMethod(socket, methodName) {
  try {
    const method = socket?.[methodName];
    return typeof method === 'function' ? method : null;
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to read socket.${methodName}.`, error);
    return null;
  }
}

function callSocketMethod(socket, methodName, ...args) {
  const method = getSocketMethod(socket, methodName);
  if (!method) return false;

  try {
    method.call(socket, ...args);
    return true;
  } catch (error) {
    Logger.system.error(`[Socket.IO] socket.${methodName} failed.`, error);
    return false;
  }
}

function disconnectSocket(socket, reason) {
  if (!socket) return;
  if (!callSocketMethod(socket, 'disconnect')) {
    Logger.system.warn(`[Socket.IO] Unable to disconnect socket during ${reason}.`);
  }
}

function disconnectObserver(observer, reason) {
  if (!observer) return;
  try {
    observer.disconnect?.();
  } catch (error) {
    Logger.system.warn(`[Socket.IO] Failed to disconnect observer during ${reason}.`, error);
  }
}

function parseTargetsSafely(targetAttribute) {
  try {
    return parseTargets(targetAttribute);
  } catch (error) {
    Logger.system.warn('[Socket.IO] Failed to parse target attribute.', error);
    return [];
  }
}

function runSocketUpdate(element, target, payloadHtml) {
  try {
    scheduleUpdate(() => {
      if (!isElementConnected(element)) return;
      Logger.system.debug("[Socket.IO] Inside scheduleUpdate callback. Updating target:", target, "with data:", payloadHtml);
      try {
        updateTarget(target, payloadHtml, element);
        Logger.system.debug("[Socket.IO] Target updated successfully:", target);
      } catch (updateError) {
        Logger.system.error("[Socket.IO] Error updating target:", target, updateError);
      }
    }, isSequential(element));
  } catch (error) {
    Logger.system.error('[Socket.IO] Failed to schedule socket update.', error);
  }
}

function setupSocketObserver(element, socket) {
  const MutationObserverConstructor = getGlobalField('MutationObserver');
  if (typeof MutationObserverConstructor !== 'function') {
    Logger.system.warn("[Socket.IO] MutationObserver is unavailable. Socket cleanup will rely on registration cleanup.");
    return;
  }

  const body = getDocumentBody();
  if (!body) {
    Logger.system.warn("[Socket.IO] document.body is unavailable. Socket cleanup will rely on registration cleanup.");
    return;
  }

  let observer = null;
  try {
    observer = new MutationObserverConstructor(() => {
      if (!isElementConnected(element)) {
        Logger.system.info("[Socket.IO] Element removed from DOM. Disconnecting socket.");
        const currentSocket = getElementField(element, '_htmlexSocket') || socket;
        disconnectSocket(currentSocket, 'element removal');
        Logger.system.debug("[Socket.IO] Socket disconnected.");
        deleteElementField(element, '_htmlexSocket');
        disconnectObserver(observer, 'element removal');
      }
    });
    observer.observe(body, { childList: true, subtree: true });
    setElementField(element, '_htmlexSocketObserver', observer);
  } catch (error) {
    Logger.system.warn("[Socket.IO] Failed to install socket cleanup observer.", error);
    disconnectObserver(observer, 'observer setup failure');
  }
}

/**
 * Establishes a Socket.IO connection for the given element.
 * Automatically cleans up the connection if the element is removed from the DOM.
 * @param {Element} element - The DOM element to attach the socket to.
 * @param {string} socketUrl - The Socket.IO URL (e.g., "https://localhost:5500/chat").
 */
export function handleWebSocket(element, socketUrl) {
  const normalizedSocketUrl = safeString(socketUrl).trim();
  Logger.system.debug("[Socket.IO] handleWebSocket called for element:", element, "with URL:", normalizedSocketUrl);

  if (!normalizedSocketUrl) {
    Logger.system.error("[Socket.IO] socketUrl is undefined or empty. Cannot create connection.");
    return;
  }
  if (!isElementLike(element)) {
    Logger.system.error("[Socket.IO] 'element' is not a valid DOM Element:", element);
    return;
  }
  const io = getGlobalField('io');
  if (typeof io !== 'function') {
    Logger.system.error("[Socket.IO] Socket.IO client is not available. Cannot create connection.");
    return;
  }

  let socket;
  try {
    Logger.system.debug("[Socket.IO] Attempting to connect to:", normalizedSocketUrl);
    socket = io(normalizedSocketUrl, {
      transports: ['websocket']
    });
  } catch (error) {
    Logger.system.error("[Socket.IO] Failed to establish connection:", error);
    return;
  }

  if (!getSocketMethod(socket, 'on') || !getSocketMethod(socket, 'onAny')) {
    Logger.system.error("[Socket.IO] Socket.IO client returned an invalid socket.");
    disconnectSocket(socket, 'invalid socket setup');
    return;
  }

  const handlersRegistered = [
    callSocketMethod(socket, 'on', 'connect', () => {
      Logger.system.info(`[Socket.IO] Connected to ${normalizedSocketUrl}`);
      Logger.system.debug("[Socket.IO] on connect event fired.");
    }),

    // Listen for all events and handle them uniformly.
    callSocketMethod(socket, 'onAny', (eventName, data) => {
      const normalizedEventName = safeString(eventName, '[unknown]');
      try {
        if (!isElementConnected(element)) {
          Logger.system.info("[Socket.IO] Ignoring event for removed element.");
          disconnectSocket(socket, 'removed element event');
          return;
        }

        Logger.system.info(`[Socket.IO] Event "${normalizedEventName}" received:`, data);

        const payloadHtml = normalizeSocketPayload(normalizedEventName, data);

        if (hasElementAttribute(element, 'target')) {
          const targetAttribute = getElementAttribute(element, 'target');
          Logger.system.debug("[Socket.IO] Element has 'target' attribute:", targetAttribute);
          const targets = parseTargetsSafely(targetAttribute);
          Logger.system.debug("[Socket.IO] Parsed targets:", targets);

          for (const target of targets) {
            Logger.system.debug("[Socket.IO] Scheduling update for target:", target);
            runSocketUpdate(element, target, payloadHtml);
          }
        } else {
          Logger.system.debug("[Socket.IO] Element does not have a 'target' attribute.");
        }
      } catch (error) {
        Logger.system.error("[Socket.IO] Socket event handler failed.", error);
      }
    }),

    callSocketMethod(socket, 'on', 'error', (error) => {
      Logger.system.error("[Socket.IO] Error:", error);
      Logger.system.debug("[Socket.IO] on error event fired.");
    }),

    callSocketMethod(socket, 'on', 'disconnect', (reason) => {
      Logger.system.info(`[Socket.IO] Connection disconnected for ${normalizedSocketUrl}. Reason: ${safeString(reason)}`);
      Logger.system.debug("[Socket.IO] on disconnect event fired.");
    })
  ];

  if (handlersRegistered.some(registered => !registered)) {
    disconnectSocket(socket, 'handler setup failure');
    return;
  }

  setElementField(element, '_htmlexSocket', socket);
  Logger.system.debug("[Socket.IO] Socket assigned to element._htmlexSocket:", socket);

  setupSocketObserver(element, socket);
}
