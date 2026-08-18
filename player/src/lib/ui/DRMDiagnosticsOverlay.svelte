<script lang="ts">
  /**
   * DRMDiagnosticsOverlay — surfaces what we know about the active DRM
   * session. Useful when content fails to play and you need to figure
   * out whether it's a license issue, robustness mismatch, or
   * output-restricted key. Wave 2 C4.
   *
   * Reuses the .lwp-stats-overlay styles from StatsOverlay for a
   * consistent debug-panel look — no extra CSS required.
   */

  import { drmDiagnostics } from '../stores/drm';
  import { showDrmInfo } from '../stores/ui';

  function close() {
    showDrmInfo.set(false);
  }

  function relAge(at: number): string {
    if (!at) return '—';
    const sec = Math.max(0, (Date.now() - at) / 1000);
    if (sec < 60) return `${sec.toFixed(0)}с назад`;
    return `${(sec / 60).toFixed(1)}мин назад`;
  }
</script>

{#if $showDrmInfo}
  <div class="lwp-stats-overlay">
    <div class="lwp-stats-header">
      <span class="lwp-stats-title">DRM диагностика</span>
      <button class="lwp-stats-close" onclick={close} aria-label="Close">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
          <path d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
        </svg>
      </button>
    </div>
    <div class="lwp-stats-body">
      {#if !$drmDiagnostics}
        <div class="lwp-stats-row">
          <span class="lwp-stats-key">Статус</span>
          <span class="lwp-stats-value">Сессия DRM не активна</span>
        </div>
      {:else}
        <div class="lwp-stats-row">
          <span class="lwp-stats-key">Key System</span>
          <span class="lwp-stats-value">{$drmDiagnostics.keySystem || '—'}</span>
        </div>
        {#if $drmDiagnostics.robustnessAchieved}
          <div class="lwp-stats-row">
            <span class="lwp-stats-key">Robustness</span>
            <span class="lwp-stats-value">{$drmDiagnostics.robustnessAchieved}</span>
          </div>
        {/if}
        {#if $drmDiagnostics.licenseRequestTimeMs !== undefined}
          <div class="lwp-stats-row">
            <span class="lwp-stats-key">License запрос</span>
            <span class="lwp-stats-value">{$drmDiagnostics.licenseRequestTimeMs.toFixed(0)}мс</span>
          </div>
        {/if}
        <div class="lwp-stats-row">
          <span class="lwp-stats-key">Последнее событие</span>
          <span class="lwp-stats-value">{relAge($drmDiagnostics.lastEventAt)}</span>
        </div>
        {#if Object.keys($drmDiagnostics.keyStatuses).length > 0}
          <div class="lwp-stats-row" style="font-weight: 600; margin-top: 8px;">
            <span class="lwp-stats-key">Key Statuses</span>
            <span class="lwp-stats-value"></span>
          </div>
          {#each Object.entries($drmDiagnostics.keyStatuses) as [keyId, status]}
            <div class="lwp-stats-row">
              <span class="lwp-stats-key" style="font-family: monospace; font-size: 11px;">{keyId.substring(0, 16)}…</span>
              <span class="lwp-stats-value">{status}</span>
            </div>
          {/each}
        {/if}
        {#if $drmDiagnostics.errors.length > 0}
          <div class="lwp-stats-row" style="font-weight: 600; margin-top: 8px; color: #ff8a80;">
            <span class="lwp-stats-key">Ошибки</span>
            <span class="lwp-stats-value">{$drmDiagnostics.errors.length}</span>
          </div>
          {#each $drmDiagnostics.errors.slice(0, 5) as err}
            <div class="lwp-stats-row" style="border-color: rgba(255,138,128,0.2);">
              <span class="lwp-stats-key" style="color: #ff8a80;">{err.code ?? '—'}</span>
              <span class="lwp-stats-value" style="font-family: monospace; font-size: 11px;">{err.message.substring(0, 60)}</span>
            </div>
          {/each}
        {/if}
      {/if}
    </div>
  </div>
{/if}
