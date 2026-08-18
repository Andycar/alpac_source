/**
 * NetworkProfile — keeps the `networkProfile` store updated with current
 * connection + battery info. Read by BufferStrategy.pick() and consulted
 * by PreloadEngine gates / CMCDReporter.
 *
 * Sources:
 *  - navigator.connection (NetworkInformation API) — Chromium/Edge/Opera
 *  - navigator.getBattery() — Chromium-only as of 2026
 *
 * Falls back to defaults when APIs are missing (Safari/Firefox).
 *
 * Polling: we don't actually poll — connection + battery both fire
 * 'change' events. We sample once at start and update on every change.
 * A 60s safety timer re-syncs in case events were missed.
 */

import { networkProfile } from '../../stores/network';
import type { NetworkProfile as NP } from '../core/types';

export class NetworkProfileMonitor {
  private connListener: (() => void) | null = null;
  private battery: any = null;
  private batteryListeners: Array<{ ev: string; cb: () => void }> = [];
  private periodicTimer: ReturnType<typeof setInterval> | null = null;

  start(): void {
    this.sample();
    this.attachConnectionListener();
    this.attachBatteryListeners().catch(() => { /* unsupported */ });
    // Safety re-sync — covers UAs that don't fire 'change' reliably.
    this.periodicTimer = setInterval(() => this.sample(), 60_000);
  }

  stop(): void {
    if (this.periodicTimer) {
      clearInterval(this.periodicTimer);
      this.periodicTimer = null;
    }
    const conn: any = (navigator as any).connection;
    if (conn && this.connListener) {
      try { conn.removeEventListener('change', this.connListener); } catch { /* ignore */ }
    }
    if (this.battery) {
      for (const { ev, cb } of this.batteryListeners) {
        try { this.battery.removeEventListener(ev, cb); } catch { /* ignore */ }
      }
    }
    this.connListener = null;
    this.battery = null;
    this.batteryListeners = [];
  }

  private attachConnectionListener(): void {
    const conn: any = (navigator as any).connection
      || (navigator as any).webkitConnection
      || (navigator as any).mozConnection;
    if (!conn?.addEventListener) return;
    this.connListener = () => this.sample();
    try {
      conn.addEventListener('change', this.connListener);
    } catch { /* ignore */ }
  }

  private async attachBatteryListeners(): Promise<void> {
    const getBattery: any = (navigator as any).getBattery;
    if (typeof getBattery !== 'function') return;
    try {
      this.battery = await getBattery.call(navigator);
    } catch {
      return;
    }
    if (!this.battery) return;
    const events = ['levelchange', 'chargingchange'];
    for (const ev of events) {
      const cb = () => this.sample();
      try {
        this.battery.addEventListener(ev, cb);
        this.batteryListeners.push({ ev, cb });
      } catch { /* ignore */ }
    }
    this.sample();
  }

  private sample(): void {
    const profile: NP = readProfile(this.battery);
    networkProfile.set(profile);
  }
}

function readProfile(battery: any): NP {
  const conn: any = (navigator as any).connection
    || (navigator as any).webkitConnection
    || (navigator as any).mozConnection;

  let effectiveType: NP['effectiveType'] = 'unknown';
  let downlinkMbps = 0;
  let rttMs = 0;
  let saveData = false;
  if (conn) {
    const et = String(conn.effectiveType || '').toLowerCase();
    if (['slow-2g', '2g', '3g', '4g', '5g'].includes(et)) {
      effectiveType = et as NP['effectiveType'];
    }
    if (typeof conn.downlink === 'number') downlinkMbps = conn.downlink;
    if (typeof conn.rtt === 'number') rttMs = conn.rtt;
    if (typeof conn.saveData === 'boolean') saveData = conn.saveData;
  }

  let batteryLevel = NaN;
  let batteryCharging = true;
  if (battery) {
    if (typeof battery.level === 'number') batteryLevel = battery.level;
    if (typeof battery.charging === 'boolean') batteryCharging = battery.charging;
  }

  return { effectiveType, downlinkMbps, rttMs, saveData, batteryLevel, batteryCharging };
}
