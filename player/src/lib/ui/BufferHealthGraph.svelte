<script lang="ts">
  /**
   * BufferHealthGraph — small canvas line chart of "buffer ahead" seconds
   * over the last ~60 seconds. Embedded inside StatsOverlay so it only
   * runs when the stats panel is visible.
   *
   * Pack 6 polish.
   */

  import { engine } from '../stores/player';
  import { onMount, onDestroy } from 'svelte';

  const SAMPLE_INTERVAL_MS = 1000;
  const HISTORY_SECONDS = 60;
  const MAX_SAMPLES = HISTORY_SECONDS;

  let canvasEl = $state<HTMLCanvasElement | null>(null);
  let samples: number[] = [];
  let timer: ReturnType<typeof setInterval> | null = null;

  function readBufferAhead(): number {
    const eng = $engine;
    if (!eng) return 0;
    const buf = eng.buffered;
    if (buf.length === 0) return 0;
    const ahead = buf.end(buf.length - 1) - eng.currentTime;
    return Math.max(0, ahead);
  }

  function tick() {
    samples.push(readBufferAhead());
    if (samples.length > MAX_SAMPLES) samples.shift();
    draw();
  }

  function draw() {
    const c = canvasEl;
    if (!c) return;
    const dpr = window.devicePixelRatio || 1;
    const cssW = 280;
    const cssH = 60;
    if (c.width !== cssW * dpr) {
      c.width = cssW * dpr;
      c.height = cssH * dpr;
      c.style.width = cssW + 'px';
      c.style.height = cssH + 'px';
    }
    const ctx = c.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, cssW, cssH);

    if (samples.length < 2) return;

    const accent = getComputedStyle(document.documentElement)
      .getPropertyValue('--lwp-amb-accent')
      || getComputedStyle(document.documentElement).getPropertyValue('--lwp-accent')
      || '#4fc3f7';

    // Scale: max value seen, but at least 30s for stable axis.
    const maxSampleVal = Math.max(30, ...samples);
    const stepX = cssW / (MAX_SAMPLES - 1);

    // Grid lines at 10s/20s/30s
    ctx.strokeStyle = 'rgba(255,255,255,0.06)';
    ctx.lineWidth = 1;
    for (let v = 10; v <= maxSampleVal; v += 10) {
      const y = cssH - (v / maxSampleVal) * cssH;
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(cssW, y);
      ctx.stroke();
    }

    // Filled area under curve
    ctx.beginPath();
    ctx.moveTo(0, cssH);
    for (let i = 0; i < samples.length; i++) {
      const x = i * stepX;
      const y = cssH - (samples[i] / maxSampleVal) * cssH;
      ctx.lineTo(x, y);
    }
    ctx.lineTo((samples.length - 1) * stepX, cssH);
    ctx.closePath();
    ctx.fillStyle = `${accent.trim()}33`; // 20% alpha
    ctx.fill();

    // Stroke line
    ctx.beginPath();
    for (let i = 0; i < samples.length; i++) {
      const x = i * stepX;
      const y = cssH - (samples[i] / maxSampleVal) * cssH;
      if (i === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    }
    ctx.strokeStyle = accent.trim() || '#4fc3f7';
    ctx.lineWidth = 1.5;
    ctx.stroke();

    // Threshold line at "buffering goal" 30s
    const ty = cssH - (30 / maxSampleVal) * cssH;
    ctx.strokeStyle = 'rgba(255,213,79,0.5)';
    ctx.setLineDash([3, 3]);
    ctx.beginPath();
    ctx.moveTo(0, ty);
    ctx.lineTo(cssW, ty);
    ctx.stroke();
    ctx.setLineDash([]);

    // Current value text
    const cur = samples[samples.length - 1];
    ctx.fillStyle = '#fff';
    ctx.font = '11px SFMono-Regular, Menlo, monospace';
    ctx.fillText(`${cur.toFixed(1)}с буфер впереди`, 6, 14);
  }

  onMount(() => {
    samples = [];
    tick();
    timer = setInterval(tick, SAMPLE_INTERVAL_MS);
  });

  onDestroy(() => {
    if (timer) clearInterval(timer);
    timer = null;
  });
</script>

<div class="lwp-buffer-graph">
  <canvas bind:this={canvasEl} aria-label="Buffer health graph"></canvas>
</div>
