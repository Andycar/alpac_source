/**
 * AudioOutput — picks the audio output device (speakers / headphones /
 * Bluetooth) via HTMLMediaElement.setSinkId().
 *
 * Chromium-only as of 2026 (Safari + Firefox don't ship the picker API).
 * Caller MUST check hasOutputDevicePicker() before exposing the UI.
 *
 * Pack 3 polish.
 */

import { hasOutputDevicePicker } from '../../utils/support';

export interface AudioOutputDevice {
  deviceId: string;
  label: string;
}

/** Enumerate available audio output devices. */
export async function listAudioOutputs(): Promise<AudioOutputDevice[]> {
  if (!hasOutputDevicePicker()) return [];
  try {
    // Some UAs require a short mic/output grant before labels are populated.
    // We don't force a gUM prompt — labels might be empty if the user hasn't
    // granted permission, in which case we show deviceId fragments.
    const list = await navigator.mediaDevices.enumerateDevices();
    return list
      .filter((d) => d.kind === 'audiooutput')
      .map((d) => ({
        deviceId: d.deviceId,
        label: d.label || `Устройство ${d.deviceId.substring(0, 6)}`,
      }));
  } catch {
    return [];
  }
}

/** Assign a device to a video element. Returns true on success. */
export async function assignOutputDevice(
  video: HTMLMediaElement,
  deviceId: string,
): Promise<boolean> {
  if (!hasOutputDevicePicker()) return false;
  try {
    await (video as any).setSinkId(deviceId);
    return true;
  } catch (err) {
    console.warn('[LWP] setSinkId failed:', err);
    return false;
  }
}
