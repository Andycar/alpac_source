#!/usr/bin/env python3
"""
FlixCDN Turnstile solver — HTTP microservice on port {{listen_port}}.
Uses undetected-chromedriver to bypass Cloudflare Turnstile.

Install:  pip install undetected-chromedriver selenium
Run:      DISPLAY=:{{display_num}} python3 flixcdn_solver.py

Protocol (as of 2026-05): the player page (`/show/<id>` on player0.flixcdn.space,
which redirects to tarantino.factorios.live or similar) embeds metadata in
`window.__PLAYER_PAYLOAD__` and resolves streams via POST `/api/player/files`
with body `{id, translation, season_number, episode_number, force_cdn,
turnstile_token}`. Response shape: `{files: "<playerjs-string>", ...}`.
"""
import json
import sys
import time
import logging
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs

logging.basicConfig(level=logging.INFO, format="%(asctime)s [flixcdn-solver] %(message)s")
log = logging.getLogger("solver")

try:
    import undetected_chromedriver as uc
except ImportError:
    print("ERROR: pip install undetected-chromedriver selenium", file=sys.stderr)
    sys.exit(1)


def solve(url, referer, payload_id, translation, season, episode):
    """Open FlixCDN page, solve Turnstile, POST /api/player/files, return file_string."""
    driver = None
    try:
        log.info("solving: %s (id=%s t=%s s=%s e=%s)", url, payload_id, translation, season, episode)

        options = uc.ChromeOptions()
        options.add_argument("--no-sandbox")
        options.add_argument("--disable-dev-shm-usage")
        options.add_argument("--disable-extensions")
        options.add_argument("--mute-audio")
        options.add_argument("--no-first-run")
        options.add_argument("--disable-default-apps")
        options.add_argument("--window-size=1280,720")

        driver = uc.Chrome(options=options, headless=False, version_main={{chrome_version}})

        # Step 1: Navigate to referer page (warms cookies + sets Referer for the click)
        log.info("step 1: opening referer %s", referer)
        driver.get(referer)
        time.sleep(2)

        # Step 2: Click a link to FlixCDN — browser sends Referer naturally,
        # avoiding the unapproved_domain rejection.
        log.info("step 2: clicking link to %s", url)
        driver.execute_script("""
            var a = document.createElement('a');
            a.href = arguments[0];
            a.target = '_self';
            document.body.appendChild(a);
            a.click();
        """, url)

        # Step 3: Wait for Turnstile token to appear in the hidden input
        log.info("step 3: waiting for Turnstile token...")
        token = ""
        for i in range(60):  # 60 * 0.5s = 30s
            try:
                token = driver.execute_script("""
                    var el = document.querySelector('[name="cf-turnstile-response"]');
                    return (el && el.value) ? el.value : '';
                """)
                if token:
                    log.info("turnstile solved in %.1fs (token %d chars)", (i + 1) * 0.5, len(token))
                    break
            except Exception:
                pass
            time.sleep(0.5)

        if not token:
            log.warning("turnstile timeout — no token")
            return ""

        # Step 4: POST /api/player/files with the captured token.
        log.info("step 4: calling /api/player/files with token")
        body = {
            "id": int(payload_id) if payload_id else 0,
            "translation": int(translation) if translation else None,
            "season_number": int(season) if season else None,
            "episode_number": int(episode) if episode else None,
            "force_cdn": "",
            "turnstile_token": token,
        }

        result = driver.execute_script("""
            var body = arguments[0];
            var token = arguments[1];
            try {
                var p = window.__PLAYER_PAYLOAD__ || {};
                if (!body.id) body.id = p.id || 0;
                if (body.translation == null && p.translate) body.translation = p.translate;
                if (body.season_number == null) body.season_number = p.season || null;
                if (body.episode_number == null) body.episode_number = p.episode || null;
                if (!body.force_cdn) body.force_cdn = p.force_cdn || '';
            } catch(e) {}
            body.turnstile_token = token;

            var xhr = new XMLHttpRequest();
            xhr.open("POST", "/api/player/files", false);
            xhr.setRequestHeader("Content-Type", "application/json");
            xhr.withCredentials = true;
            try {
                xhr.send(JSON.stringify(body));
            } catch(e) { return JSON.stringify({error: 'send_failed', msg: String(e)}); }
            return JSON.stringify({status: xhr.status, text: xhr.responseText});
        """, body, token)

        try:
            envelope = json.loads(result) if result else {}
        except Exception as e:
            log.error("step 4: response not json: %s", e)
            return ""

        if envelope.get("error"):
            log.error("step 4: %s", envelope.get("msg"))
            return ""

        status = envelope.get("status", 0)
        text = envelope.get("text", "") or ""
        if status < 200 or status >= 300:
            log.warning("step 4: HTTP %s — %s", status, text[:200])
            return ""

        try:
            data = json.loads(text)
        except Exception as e:
            log.error("step 4: body parse failed: %s — %s", e, text[:200])
            return ""

        # The API returns the Playerjs file_string under the key "file" (singular).
        # We also accept "files" and "file_string" as best-effort fallbacks in case
        # a different upstream version ever uses them.
        file_string = data.get("file") or data.get("files") or data.get("file_string") or ""
        if file_string:
            log.info("success: %s...", file_string[:100])
            return file_string

        log.warning("stream API returned empty file: %s", text[:200])
        return ""
    except Exception as e:
        log.error("error: %s", e)
        return ""
    finally:
        if driver:
            try:
                driver.quit()
            except Exception:
                pass


class SolverHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path != "/solve":
            self._respond(404, {"error": "not found"})
            return

        qs = parse_qs(parsed.query)
        url = qs.get("url", [""])[0]
        referer = qs.get("referer", ["https://hdplayer.click/"])[0]
        payload_id = qs.get("id", [""])[0]
        translation = qs.get("translation", [""])[0]
        season = qs.get("season", [""])[0]
        episode = qs.get("episode", [""])[0]

        if not url:
            self._respond(400, {"error": "missing url param"})
            return

        file_string = solve(url, referer, payload_id, translation, season, episode)
        self._respond(200, {
            "file_string": file_string,
            "error": "" if file_string else "turnstile_failed",
        })

    def _respond(self, code, data):
        body = json.dumps(data).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        log.info("%s %s", self.client_address[0], fmt % args)


if __name__ == "__main__":
    host, port = "127.0.0.1", {{listen_port}}
    server = HTTPServer((host, port), SolverHandler)
    log.info("listening on %s:%d", host, port)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        server.shutdown()
