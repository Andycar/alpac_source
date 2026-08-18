/**
 * Typed event bus — replaces the ad-hoc Map<string, Set<Function>> in
 * the old PlaybackEngine. Each event has a typed payload (see EngineEvents
 * in ./types.ts).
 *
 * Backward compat: the PlaybackEngine facade keeps its `on(event, cb)` /
 * `off(event, cb)` signature. Existing call sites in Player.svelte
 * (line 144-227) pass callbacks like `(b: boolean) => void` for `buffering`
 * — the bus invokes them with the typed payload, which for `buffering` is
 * `boolean`, so the loose-typed call sites continue to work.
 */

export type Listener<P> = (payload: P) => void;

// Generic constraint is intentionally permissive: TS interfaces don't
// satisfy `Record<string, unknown>` even when their members are unknown,
// so we accept any object-shaped event map and rely on EngineEvents to
// be the canonical typing source for callers.
export class EventBus<E extends object> {
  private map = new Map<keyof E, Set<Listener<unknown>>>();

  on<K extends keyof E>(event: K, cb: Listener<E[K]>): () => void {
    let set = this.map.get(event);
    if (!set) {
      set = new Set();
      this.map.set(event, set);
    }
    set.add(cb as Listener<unknown>);
    return () => this.off(event, cb);
  }

  once<K extends keyof E>(event: K, cb: Listener<E[K]>): () => void {
    const off = this.on(event, ((payload: E[K]) => {
      off();
      cb(payload);
    }) as Listener<E[K]>);
    return off;
  }

  off<K extends keyof E>(event: K, cb: Listener<E[K]>): void {
    this.map.get(event)?.delete(cb as Listener<unknown>);
  }

  emit<K extends keyof E>(event: K, payload: E[K]): void {
    const set = this.map.get(event);
    if (!set) return;
    // Iterate over a snapshot so listeners can off() themselves safely.
    for (const cb of Array.from(set)) {
      try {
        (cb as Listener<E[K]>)(payload);
      } catch (err) {
        // Swallow listener errors — never let one subscriber kill the bus.
        console.warn('[LWP] EventBus listener error:', err);
      }
    }
  }

  clear(): void {
    this.map.clear();
  }

  /** Number of listeners for an event (test/debug helper). */
  count<K extends keyof E>(event: K): number {
    return this.map.get(event)?.size ?? 0;
  }
}
