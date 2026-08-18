// vot.src.mjs — YouTube voice-over-translation sidecar (source).
//
// Bundled by build.sh into vot.bundle.cjs (a single self-contained CommonJS file with vot.js + deps
// inlined), which the Go server embeds (go:embed) and runs as `node vot.cjs <videoID> [from] [to]`.
// No npm install needed on the server — just a Node 18+ runtime, the same way yt-dlp ships as one file.
// (CJS output, not ESM, because a transitive dep — undici — uses dynamic require() of node: builtins
// which an ESM bundle can't do; CJS output keeps require working natively. Hence the async IIFE below
// instead of top-level await, which CJS output doesn't support.)
//
// It asks the (public) Yandex VOT service for a RU voice-over of a YouTube video and prints ONE line
// of JSON to stdout:
//   {"ok":true,"status":"success","url":"https://.../audio.mp3","remainingTime":0,"raw":{...}}
//   {"ok":true,"status":"waiting","remainingTime":42,"raw":{...}}     // still translating → poll again
//   {"ok":true,"status":"error","raw":{...}}                          // upstream said failed
//   {"ok":false,"status":"error","error":"..."}                       // exception
//
// Env VOT_WORKER_HOST (e.g. "vot-worker.toil.cc") routes through the public vot-worker proxy to
// bypass geo-block/CORS; empty → talk to Yandex directly.
import VOTClient, { VOTWorkerClient, videoData } from "@vot.js/node";

function emit(o) {
  process.stdout.write(JSON.stringify(o));
}

(async () => {
  const videoId = (process.argv[2] || "").trim();
  const fromLang = (process.argv[3] || "en").trim();
  const toLang = (process.argv[4] || "ru").trim();
  const workerHost = (process.env.VOT_WORKER_HOST || "").trim();

  if (!videoId) {
    emit({ ok: false, status: "error", error: "videoID required" });
    return;
  }

  const url = "https://www.youtube.com/watch?v=" + videoId;

  try {
    const opts = { requestLang: fromLang, responseLang: toLang };
    const client = workerHost
      ? new VOTWorkerClient({ host: workerHost, ...opts })
      : new VOTClient(opts);

    // getVideoData is a standalone util in @vot.js (not a client method); it resolves the service +
    // video id from the URL into the shape translateVideo expects.
    const info = await videoData.getVideoData(url);
    const res = (await client.translateVideo({ videoData: info })) || {};

    // Normalize across vot.js versions: the translated-audio URL and the "remaining time" hint can
    // live under a couple of names. Probe defensively; include `raw` so the first prod run reveals the
    // exact shape if this needs adjusting. Default to "waiting" (caller polls a bounded number of times).
    const audioUrl = res.url || res.translated_url || res.audioUrl || "";
    const remaining = res.remainingTime ?? res.remaining_time;
    let status = "waiting";
    if (audioUrl) status = "success";
    else if (res.status === "failed") status = "error";

    emit({
      ok: true,
      status,
      url: audioUrl || undefined,
      remainingTime: typeof remaining === "number" ? remaining : undefined,
      raw: res,
    });
  } catch (e) {
    emit({ ok: false, status: "error", error: String((e && e.message) || e) });
  }
})();
