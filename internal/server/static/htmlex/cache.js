// src/cache.js
/**
 * @module Cache
 * @description Provides caching functionality for API responses.
 *
 * @typedef {Object} CacheEntry
 * @property {string} response - The cached response.
 * @property {number} expireAt - The expiration time in milliseconds.
 */

import { Logger } from './logger.js';

/** @type {Map<string, CacheEntry>} */
const cacheStore = new Map();
const MAX_CACHE_ENTRIES = 100;

function getCurrentTimeMs() {
  try {
    const timestamp = Date.now();
    return Number.isFinite(timestamp) ? timestamp : 0;
  } catch (error) {
    Logger.system.warn("[CACHE] Failed to read current time; using fallback timestamp.", error);
    return 0;
  }
}

function pruneCache() {
  const now = getCurrentTimeMs();
  for (const [key, { expireAt }] of cacheStore) {
    if (Number.isFinite(expireAt) && now >= expireAt) {
      cacheStore.delete(key);
    }
  }

  while (cacheStore.size > MAX_CACHE_ENTRIES) {
    const oldestKey = cacheStore.keys().next().value;
    cacheStore.delete(oldestKey);
  }
}

/**
 * Caches a response.
 * @param {string} key - The cache key.
 * @param {string} response - The response to cache.
 * @param {number} ttl - Time to live (milliseconds).
 */
export function setCache(key, response, ttl) {
  pruneCache();
  const ttlMs = Number(ttl);
  const expireAt = Number.isFinite(ttlMs) && ttlMs > 0
    ? getCurrentTimeMs() + ttlMs
    : Infinity;
  cacheStore.set(key, { response, expireAt });
  pruneCache();
  Logger.system.debug("[CACHE] Cached response for key:", key, "TTL:", ttl, "Expires at:", expireAt);
}

/**
 * Retrieves a cached response if available and unexpired.
 * @param {string} key - The cache key.
 * @returns {string|null} The cached response or null if not found/expired.
 */
export function getCache(key) {
  pruneCache();

  const entry = cacheStore.get(key);
  if (!entry) {
    Logger.system.debug("[CACHE] Cache miss for key:", key);
    return null;
  }

  const { response, expireAt } = entry;
  if (getCurrentTimeMs() < expireAt) {
    Logger.system.debug("[CACHE] Cache hit for key:", key);
    return response;
  }

  Logger.system.warn("[CACHE] Cache expired for key:", key, "Deleting entry.");
  cacheStore.delete(key);
  return null;
}
