// R50: the signed-in user's view preferences (chart range, tabs, filters, sort order, folded
// cards…), kept on the server so they follow the user to any browser. Loaded once after
// sign-in, before the pages render; changes are saved together after a short pause. A failed
// load or save never blocks the app: the last copy in this browser (localStorage, an offline
// cache only) or the defaults are used, and a small note says the choices are not saved.
// Nothing here decides anything about trading: these are view choices only.
import {
  createContext, useCallback, useContext, useEffect, useState, useSyncExternalStore, type ReactNode,
} from "react";
import { api, ApiError } from "./api";

type Values = Record<string, unknown>;
type Patch = Record<string, unknown>; // null deletes a key

// SAVE_DELAY_MS: changes made within this pause go to the server in one request.
export const SAVE_DELAY_MS = 500;
// RETRY_MS: a failed save is tried again after this long (and with the next change).
export const RETRY_MS = 15_000;

const cacheKey = (userId: string) => `cactustrade.prefs.${userId}`;

function clearCache(userId: string) {
  try {
    localStorage.removeItem(cacheKey(userId));
  } catch {
    /* storage off: nothing to clear */
  }
}

function readCache(userId: string): Values {
  try {
    const v = JSON.parse(localStorage.getItem(cacheKey(userId)) ?? "{}");
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Values) : {};
  } catch {
    return {};
  }
}

// PrefsStore holds one user's preferences for this page: what the server sent, with this
// browser's changes on top, and the changes not yet saved.
export class PrefsStore {
  private values: Values;
  private pending: Patch = {};
  private timer: ReturnType<typeof setTimeout> | undefined;
  // chain: every save runs after the one before it (never two requests at once, so an older
  // patch can never land after a newer one); flush and sign-out wait on it.
  private chain: Promise<void> = Promise.resolve();
  private listeners = new Set<() => void>();
  private failed = false;
  private disposed = false;

  constructor(private userId: string, initial: Values) {
    this.values = { ...initial };
  }

  get(key: string): unknown {
    return this.values[key];
  }

  // saveFailed is whether the last save (or the load) did not reach the server.
  saveFailed(): boolean {
    return this.failed;
  }

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  // set changes a preference now (every user of the key re-renders) and saves it shortly.
  // undefined or null (back to the default) deletes the stored key.
  set(key: string, value: unknown) {
    if (value === undefined || value === null) {
      if (!(key in this.values)) return;
      const next = { ...this.values };
      delete next[key];
      this.values = next;
      this.pending[key] = null;
    } else {
      if (JSON.stringify(this.values[key]) === JSON.stringify(value)) return;
      this.values = { ...this.values, [key]: value };
      this.pending[key] = value;
    }
    this.writeCache();
    this.notify();
    this.schedule(SAVE_DELAY_MS);
  }

  // markLoadFailed: the server could not be read; the cache or the defaults are in use.
  markLoadFailed() {
    this.setFailed(true);
  }

  // flush sends what is pending now, after any save already on its way, and resolves once
  // both are done (on leaving the page, keepalive lets it finish). Nothing pending at the
  // time of a change made during an in-flight save is lost: it goes with this request.
  flush(keepalive = false): Promise<void> {
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
    return this.enqueue(keepalive);
  }

  // signOut is the last save before signing out: everything pending reaches the server
  // (after any save in flight), then this browser's copy is removed, so the next person on
  // this browser never sees it. It never rejects.
  async signOut(): Promise<void> {
    try {
      await this.flush();
    } finally {
      this.dispose();
      clearCache(this.userId);
    }
  }

  // dispose stops the timer (the provider unmounts: signed out or another user).
  dispose() {
    this.disposed = true;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
  }

  private schedule(ms: number) {
    if (this.timer !== undefined) clearTimeout(this.timer);
    if (this.disposed) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.enqueue(false);
    }, ms);
  }

  // enqueue chains a save after the one before it; the result settles when it is done.
  private enqueue(keepalive: boolean): Promise<void> {
    this.chain = this.chain.then(() => this.save(keepalive));
    return this.chain;
  }

  // save sends what is pending when it runs (never rejects: the chain must go on).
  private async save(keepalive: boolean): Promise<void> {
    const patch = this.pending;
    if (Object.keys(patch).length === 0) return;
    this.pending = {};
    try {
      await api.setMyPreferences(patch, keepalive);
      this.setFailed(false);
    } catch (e) {
      this.setFailed(true);
      // refused (too many, not allowed) or signed out: trying again would not help; the
      // choices stay in this page and in this browser's copy
      if (e instanceof ApiError && e.status >= 400 && e.status < 500 && e.status !== 408 && e.status !== 429) return;
      // otherwise keep what was not saved, unless changed again since; try again later, quietly
      this.pending = { ...patch, ...this.pending };
      this.schedule(RETRY_MS);
    }
  }

  private setFailed(v: boolean) {
    if (this.failed === v) return;
    this.failed = v;
    this.notify();
  }

  private writeCache() {
    try {
      localStorage.setItem(cacheKey(this.userId), JSON.stringify(this.values));
    } catch {
      /* storage off: the server copy is what counts */
    }
  }

  private notify() {
    for (const fn of this.listeners) fn();
  }

  // load reads the user's preferences from the server (the cache when it cannot).
  static async load(userId: string): Promise<PrefsStore> {
    try {
      const v = await api.myPreferences();
      const store = new PrefsStore(userId, v && typeof v === "object" ? (v as Values) : {});
      store.writeCache();
      return store;
    } catch {
      const store = new PrefsStore(userId, readCache(userId));
      store.markLoadFailed();
      return store;
    }
  }
}

const PrefsContext = createContext<PrefsStore | null>(null);

// PrefsProvider loads the user's preferences once after sign-in and renders its children
// only then, so every page starts with the user's choices (no flash of the defaults).
export function PrefsProvider({ userId, children }: { userId: string; children: ReactNode }) {
  const [store, setStore] = useState<PrefsStore | null>(null);
  useEffect(() => {
    let live = true;
    let loaded: PrefsStore | null = null;
    void PrefsStore.load(userId).then((s) => {
      loaded = s;
      if (live) setStore(s);
    });
    return () => {
      live = false;
      if (loaded) {
        void loaded.flush();
        loaded.dispose();
      }
      setStore(null);
    };
  }, [userId]);
  useEffect(() => {
    if (!store) return;
    const leave = () => void store.flush(true);
    window.addEventListener("pagehide", leave);
    return () => window.removeEventListener("pagehide", leave);
  }, [store]);
  if (!store) return <div className="p-5 text-center text-secondary">Loading…</div>;
  return <PrefsStoreProvider store={store}>{children}</PrefsStoreProvider>;
}

// PrefsStoreProvider provides an already loaded store (PrefsProvider uses it; so can tests).
export function PrefsStoreProvider({ store, children }: { store: PrefsStore; children: ReactNode }) {
  return <PrefsContext.Provider value={store}>{children}</PrefsContext.Provider>;
}

const noop = () => () => {};

// sameShape is the default check of a stored value: of the default's kind (an array for an
// array, an object for an object, else the same typeof).
function sameShape(v: unknown, def: unknown): boolean {
  if (Array.isArray(def)) return Array.isArray(v);
  if (def === null) return v === null;
  if (typeof def === "object") return !!v && typeof v === "object" && !Array.isArray(v);
  return typeof v === typeof def;
}

// usePref is useState for a view choice the user keeps: key names it (page.component.what,
// e.g. "dashboard.liveInvest.range"), def is its default. A stored value that fails check
// (by default: not of the default's kind) is ignored. Setting the default removes the key.
// Outside a PrefsProvider (e.g. a component test) it is plain component state.
export function usePref<T>(key: string, def: T, check?: (v: unknown) => boolean): [T, (v: T | ((cur: T) => T)) => void] {
  const store = useContext(PrefsContext);
  const [local, setLocal] = useState<T>(def);
  const stored = useSyncExternalStore(store ? store.subscribe : noop, () => (store ? store.get(key) : undefined));
  const ok = stored !== undefined && (check ? check(stored) : sameShape(stored, def));
  const value = store ? (ok ? (stored as T) : def) : local;
  const defJSON = JSON.stringify(def);
  const set = useCallback((v: T | ((cur: T) => T)) => {
    if (!store) {
      setLocal(v);
      return;
    }
    const cur = store.get(key);
    const curT = cur !== undefined && (check ? check(cur) : sameShape(cur, JSON.parse(defJSON))) ? (cur as T) : (JSON.parse(defJSON) as T);
    const next = typeof v === "function" ? (v as (c: T) => T)(curT) : v;
    store.set(key, JSON.stringify(next) === defJSON ? undefined : next);
  }, [store, key, check, defJSON]);
  return [value, set];
}

// oneOf is a check for usePref: the stored value must be one of these.
export function oneOf<T>(allowed: readonly T[]): (v: unknown) => boolean {
  return (v) => allowed.includes(v as T);
}

// usePrefsSaveFailed is whether the preferences could not be loaded or saved just now
// (shown quietly; the app works on with the choices in this browser).
export function usePrefsSaveFailed(): boolean {
  const store = useContext(PrefsContext);
  return useSyncExternalStore(store ? store.subscribe : noop, () => (store ? store.saveFailed() : false));
}

// usePrefsFlush returns a function that saves pending choices now, after any save already
// on its way.
export function usePrefsFlush(): () => Promise<void> {
  const store = useContext(PrefsContext);
  return useCallback(() => (store ? store.flush() : Promise.resolve()), [store]);
}

// usePrefsSignOut returns what to await before signing out: every pending choice reaches the
// server, then this browser's copy of them is removed. It never rejects.
export function usePrefsSignOut(): () => Promise<void> {
  const store = useContext(PrefsContext);
  return useCallback(() => (store ? store.signOut() : Promise.resolve()), [store]);
}
