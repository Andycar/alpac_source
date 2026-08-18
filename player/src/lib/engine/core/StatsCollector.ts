/**
 * StatsCollector — produces the diagnostic key/value map shown in StatsOverlay.
 *
 * Refactored out of PlaybackEngine.ts (lines 691-812). All Russian labels
 * preserved as-is so the existing StatsOverlay.svelte continues to render
 * the same keys.
 */

export interface StatsContext {
  isTranscoding: boolean;
  manifestUri?: string;
}

export class StatsCollector {
  constructor(
    private player: any,
    private videoEl: HTMLVideoElement,
  ) {}

  collect(ctx: StatsContext): Record<string, string> {
    const stats: Record<string, string> = {};
    const v = this.videoEl;

    // Video element basics
    stats['Разрешение'] = v.videoWidth && v.videoHeight ? `${v.videoWidth}×${v.videoHeight}` : '—';
    stats['Позиция'] = `${isFinite(v.currentTime) ? v.currentTime.toFixed(1) : '0.0'}с / ${isFinite(v.duration) ? v.duration.toFixed(1) : '0.0'}с`;
    stats['Скорость'] = `${v.playbackRate}x`;
    stats['Состояние'] = ['HAVE_NOTHING', 'HAVE_METADATA', 'HAVE_CURRENT_DATA', 'HAVE_FUTURE_DATA', 'HAVE_ENOUGH_DATA'][v.readyState] || String(v.readyState);
    stats['Сеть'] = ['EMPTY', 'IDLE', 'LOADING', 'NO_SOURCE'][v.networkState] || String(v.networkState);
    stats['Громкость'] = `${Math.round(v.volume * 100)}%${v.muted ? ' (выкл)' : ''}`;

    // Buffered ranges
    if (v.buffered.length > 0) {
      const buffEnd = v.buffered.end(v.buffered.length - 1);
      const ahead = Math.max(0, buffEnd - v.currentTime);
      stats['Буфер впереди'] = `${ahead.toFixed(1)}с`;
      const ranges: string[] = [];
      for (let i = 0; i < v.buffered.length; i++) {
        ranges.push(`${v.buffered.start(i).toFixed(1)}-${v.buffered.end(i).toFixed(1)}`);
      }
      stats['Диапазоны буфера'] = ranges.join(', ');
    } else {
      stats['Буфер впереди'] = '0с';
    }

    // Shaka stats
    if (this.player && typeof this.player.getStats === 'function') {
      try {
        const s = this.player.getStats();
        stats['Битрейт потока'] = formatBitrate(s.streamBandwidth);
        stats['Оценка канала'] = formatBitrate(s.estimatedBandwidth);
        stats['Задержка загрузки'] = s.loadLatency ? `${(s.loadLatency * 1000).toFixed(0)}мс` : '—';
        stats['Время воспроизведения'] = isFinite(s.playTime) ? `${s.playTime.toFixed(1)}с` : '—';
        stats['Время буферизации'] = isFinite(s.bufferingTime) ? `${s.bufferingTime.toFixed(1)}с` : '—';
        stats['Задержка Live'] = s.liveLatency ? `${s.liveLatency.toFixed(2)}с` : '—';
        if (s.stateHistory?.length) {
          const last = s.stateHistory[s.stateHistory.length - 1];
          stats['Состояние плеера'] = last.state || '—';
        }
        if (typeof s.corruptedFrames !== 'undefined') {
          stats['Повреждённые кадры'] = String(s.corruptedFrames);
        }
        if (typeof s.droppedFrames !== 'undefined') {
          stats['Пропущенные кадры'] = String(s.droppedFrames);
          stats['Декодировано кадров'] = String(s.decodedFrames || 0);
        }
        if (typeof s.completionPercent !== 'undefined') {
          stats['Прогресс'] = `${s.completionPercent.toFixed(1)}%`;
        }
        if (typeof s.gapsJumped !== 'undefined') {
          stats['Пропуски'] = String(s.gapsJumped);
        }
        if (typeof s.stallsDetected !== 'undefined') {
          stats['Зависания'] = String(s.stallsDetected);
        }
        if (s.manifestTimeSeconds) {
          stats['Парсинг манифеста'] = `${(s.manifestTimeSeconds * 1000).toFixed(0)}мс`;
        }
        if (s.drmTimeSeconds) {
          stats['DRM лицензия'] = `${(s.drmTimeSeconds * 1000).toFixed(0)}мс`;
        }
      } catch {
        // ignore
      }
    }

    // Active variant
    if (this.player && typeof this.player.getVariantTracks === 'function') {
      try {
        const tracks = this.player.getVariantTracks();
        const active = tracks.find((t: any) => t.active);
        if (active) {
          stats['Видеокодек'] = active.videoCodec || '—';
          stats['Аудиокодек'] = active.audioCodec || '—';
          stats['Битрейт видео'] = active.videoBandwidth ? formatBitrate(active.videoBandwidth) : '—';
          stats['Битрейт аудио'] = active.audioBandwidth ? formatBitrate(active.audioBandwidth) : '—';
          stats['Частота кадров'] = active.frameRate ? `${active.frameRate} fps` : '—';
          stats['MIME тип'] = active.mimeType || '—';
          if (active.pixelAspectRatio) stats['Пиксельное соотн.'] = active.pixelAspectRatio;
          if (active.hdr) stats['HDR'] = active.hdr;
          if (active.channelsCount) stats['Аудиоканалы'] = String(active.channelsCount);
          if (active.audioSamplingRate) stats['Частота дискр.'] = `${active.audioSamplingRate} Гц`;
          if (active.spatialAudio) stats['Пространств. звук'] = 'Да';
        }
      } catch {
        // ignore
      }
    }

    // ABR config
    if (this.player && typeof this.player.getConfiguration === 'function') {
      try {
        const cfg = this.player.getConfiguration();
        stats['ABR'] = cfg.abr?.enabled ? 'Вкл' : 'Выкл';
      } catch {
        // ignore
      }
    }

    // Manifest type
    const uri = ctx.manifestUri || (this.player?.getAssetUri ? this.player.getAssetUri() : null);
    if (uri) {
      stats['URI манифеста'] = uri.length > 80 ? uri.substring(0, 77) + '...' : uri;
      if (/\.m3u8/i.test(uri)) stats['Тип манифеста'] = 'HLS';
      else if (/\.mpd/i.test(uri)) stats['Тип манифеста'] = 'DASH';
      else stats['Тип манифеста'] = 'Прямой';
    }

    // Transcoding flag (caller knows)
    stats['Транскодирование'] = ctx.isTranscoding ? 'Да' : 'Нет';

    if (v.videoWidth && v.videoHeight) {
      const display = v.getBoundingClientRect();
      stats['Размер на экране'] = `${Math.round(display.width)}×${Math.round(display.height)}`;
    }

    return stats;
  }
}

function formatBitrate(bps: number): string {
  if (!bps) return '—';
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(2)} Mbps`;
  if (bps >= 1_000) return `${(bps / 1_000).toFixed(0)} Kbps`;
  return `${bps} bps`;
}
