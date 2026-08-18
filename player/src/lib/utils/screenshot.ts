/**
 * Capture current video frame and trigger download.
 */
export function captureFrame(video: HTMLVideoElement, filename?: string): void {
  const canvas = document.createElement('canvas');
  canvas.width = video.videoWidth;
  canvas.height = video.videoHeight;
  const ctx = canvas.getContext('2d');
  if (!ctx) return;

  ctx.drawImage(video, 0, 0);

  const a = document.createElement('a');
  a.href = canvas.toDataURL('image/png');
  a.download = filename || `frame-${Date.now()}.png`;
  a.click();
}
