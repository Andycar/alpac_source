/**
 * TranscodingFallback — server-side transcoding for unsupported codecs.
 *
 * Flow:
 * 1. GET /ffprobe?media=URL → detect video/audio codecs
 * 2. If codecs not supported by browser → POST /transcoding/start
 * 3. Returns HLS playlist URL + optional subtitles
 * 4. Heartbeat loop keeps transcoding session alive
 */

import { codecProbe, type CodecSupport } from './CodecProbe';

export interface TranscodingResult {
  playlistUrl: string;
  subtitlesUrl?: string;
  streamId: string;
}

export interface FFProbeResult {
  streams: Array<{
    codec_type: string;
    codec_name: string;
    width?: number;
    height?: number;
    index: number;
  }>;
}

/**
 * Check if a media URL needs transcoding by probing its codecs.
 * Returns null if no transcoding needed, or TranscodingResult if started.
 */
export async function checkAndStartTranscoding(
  mediaUrl: string,
  codecSupport: CodecSupport,
  audioIndex?: number,
): Promise<TranscodingResult | null> {
  // Only check non-HLS/DASH URLs (those are already transcoded/segmented)
  if (/\.(m3u8|mpd)(\?|$)/i.test(mediaUrl)) {
    return null;
  }

  // Probe codecs
  const probe = await ffprobe(mediaUrl);
  if (!probe) return null;

  const videoStream = probe.streams?.find((s) => s.codec_type === 'video');
  const audioStream = probe.streams?.find((s) => s.codec_type === 'audio');

  if (!videoStream) return null;

  const needsTranscoding = !isCodecSupported(
    videoStream.codec_name,
    audioStream?.codec_name,
    codecSupport,
  );

  if (!needsTranscoding) return null;

  console.log(
    '[LWP] Codec not supported:',
    videoStream.codec_name,
    audioStream?.codec_name,
    '→ starting transcoding',
  );

  return startTranscoding(mediaUrl, audioIndex);
}

/**
 * Check if video+audio codec combo is browser-supported.
 */
function isCodecSupported(
  videoCodec: string,
  audioCodec: string | undefined,
  support: CodecSupport,
): boolean {
  const vc = videoCodec?.toLowerCase() || '';
  const ac = audioCodec?.toLowerCase() || '';

  // Video codec check
  let videoOk = false;
  if (['h264', 'avc1', 'avc'].includes(vc)) videoOk = support.h264;
  else if (['hevc', 'h265', 'hev1', 'hvc1'].includes(vc)) videoOk = support.hevc;
  else if (['av1', 'av01'].includes(vc)) videoOk = support.av1;
  else if (['vp9', 'vp09'].includes(vc)) videoOk = support.vp9;
  else videoOk = false; // Unknown codec — transcode

  if (!videoOk) return false;

  // Audio codec check (if present)
  if (!ac) return true;
  if (['aac', 'mp4a'].includes(ac)) return support.aac;
  if (['ac3', 'ac-3', 'a52'].includes(ac)) return support.ac3;
  if (['eac3', 'ec-3'].includes(ac)) return support.eac3;
  if (['opus'].includes(ac)) return support.opus;
  if (['mp3', 'mp2', 'mp1'].includes(ac)) return true; // Universally supported
  if (['vorbis'].includes(ac)) return true;
  if (['flac'].includes(ac)) return true;

  // Unknown audio codec — may work, don't force transcode for audio alone
  return true;
}

/**
 * Call /ffprobe?media=URL to get stream info.
 */
async function ffprobe(mediaUrl: string): Promise<FFProbeResult | null> {
  try {
    const resp = await fetch(
      `/ffprobe?media=${encodeURIComponent(mediaUrl)}`,
      { signal: AbortSignal.timeout(15000) },
    );
    if (!resp.ok) return null;
    return await resp.json();
  } catch {
    console.warn('[LWP] ffprobe failed for', mediaUrl);
    return null;
  }
}

/**
 * POST /transcoding/start to begin server-side transcoding.
 */
async function startTranscoding(
  src: string,
  audioIndex?: number,
): Promise<TranscodingResult | null> {
  try {
    const body: any = {
      src,
      subtitles: true,
    };
    if (audioIndex !== undefined) {
      body.audio = { index: audioIndex };
    }

    const resp = await fetch('/transcoding/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(10000),
    });

    if (!resp.ok) {
      console.warn('[LWP] transcoding/start failed:', resp.status);
      return null;
    }

    const data = await resp.json();
    return {
      playlistUrl: data.playlistUrl,
      subtitlesUrl: data.subtitlesUrl,
      streamId: data.streamId,
    };
  } catch (e) {
    console.warn('[LWP] transcoding/start error:', e);
    return null;
  }
}

/**
 * Heartbeat loop — keeps transcoding session alive.
 * Call stop() to terminate.
 */
export function startHeartbeat(streamId: string): { stop: () => void } {
  let active = true;

  const beat = async () => {
    if (!active) return;
    try {
      await fetch(`/transcoding/${streamId}/heartbeat`, {
        method: 'POST',
        signal: AbortSignal.timeout(5000),
      });
    } catch {
      // Non-critical
    }
    if (active) {
      setTimeout(beat, 10000);
    }
  };

  // Start first heartbeat after 5s
  setTimeout(beat, 5000);

  return {
    stop: () => {
      active = false;
      // Send stop signal
      fetch(`/transcoding/${streamId}/stop`, { method: 'POST' }).catch(() => {});
    },
  };
}
