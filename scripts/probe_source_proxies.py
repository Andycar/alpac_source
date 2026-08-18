#!/usr/bin/env python3
# Проверка доступности awmzone (turboserial.com + CDN cdn.awmzone1.pro) и
# kinoteatr.kg через локальные SOCKS5-прокси (по умолчанию 40001..40008) + direct.
#
# Для awmzone токен CDN, вероятно, привязан к IP, поэтому свежая ссылка
# резолвится ЧЕРЕЗ ТОТ ЖЕ прокси, а затем тянется ЧЕРЕЗ НЕГО ЖЕ (end-to-end).
# Зелёный (200) = этот прокси годится для socks_proxy модуля и для
# [[proxy.direct.entries]] стрима.
#
# Запуск:
#   python3 scripts/probe_source_proxies.py            # порты 40001..40008
#   python3 scripts/probe_source_proxies.py 40003 40007  # только эти порты
import subprocess, re, base64, json, html as H, urllib.parse, socket, sys

UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36'
TURBO = 'https://turboserial.com'
KINO = 'https://kinoteatr.kg'
SALTS = ['//' + base64.b64encode(s.encode()).decode() for s in ('dvadolboeba', 'pososikloun', 'bibaiboba')]

PORTS = [int(a) for a in sys.argv[1:]] or list(range(40001, 40009))


def curl(url, proxy=None, ref=None, rng=None, follow=False, timeout=20, code_only=False):
    cmd = ['curl', '-sS', '-m', str(timeout), '--connect-timeout', '8', '-A', UA]
    if proxy:
        cmd += ['--socks5-hostname', proxy]
    if ref:
        cmd += ['-e', ref]
    if rng:
        cmd += ['-r', rng]
    if follow:
        cmd += ['-L']
    if code_only:
        cmd += ['-o', '/dev/null', '-w', '%{http_code}']
    cmd.append(url)
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout + 5).stdout
    except subprocess.TimeoutExpired:
        return ''


def deob(f):
    b = f[2:]
    for j in SALTS:
        b = b.replace(j, '')
    b = b.rstrip('=')
    b += '=' * ((4 - len(b) % 4) % 4)
    try:
        return base64.b64decode(b).decode('utf-8', 'replace')
    except Exception:
        return ''


def first_obf(x):
    if isinstance(x, dict):
        for v in x.values():
            r = first_obf(v)
            if r:
                return r
    elif isinstance(x, list):
        for v in x:
            r = first_obf(v)
            if r:
                return r
    elif isinstance(x, str) and x.startswith('#2'):
        return x
    return None


def port_open(port):
    s = socket.socket()
    s.settimeout(3)
    try:
        s.connect(('127.0.0.1', port))
        return True
    except Exception:
        return False
    finally:
        s.close()


def egress(proxy):
    out = curl('https://ipinfo.io/json', proxy=proxy, timeout=12)
    try:
        d = json.loads(out)
        s = f"{d.get('ip','?')} {d.get('country','?')}/{d.get('org','?') or '?'}"
        return s[:30]
    except Exception:
        return '(no ip)'


def test_awmzone(proxy):
    try:
        raw = curl(TURBO + '/mary/spotlight?search=' + urllib.parse.quote('Чужой'), proxy=proxy, timeout=15)
        if not raw.strip():
            return 'no-site'
        try:
            arr = json.loads(raw)
        except ValueError:
            return 'site-block'  # сайт вернул не-JSON (блок/челлендж/кривой прокси)
        if not arr:
            return 'no-search'
        link = next((a['link'] for a in arr if (a.get('name') or '').strip().lower() == 'чужой'), arr[0]['link'])
        watch = curl(TURBO + link, proxy=proxy, ref=TURBO + '/', timeout=15).replace('\\/', '/')
        m = re.search(r'"embedUrl":"(https:[^"]+embed-players[^"]+)"', watch)
        if not m:
            return 'no-embed'
        emb = H.unescape(curl(m.group(1), proxy=proxy, ref=TURBO + link, timeout=15))
        mm = re.search(r"atob\('([A-Za-z0-9+/=]+)'\)", emb)
        if not mm:
            return 'no-atob'
        obj = json.loads(base64.b64decode(mm.group(1)))
        f = obj.get('file')
        s = deob(f if isinstance(f, str) else first_obf(f))
        u = re.search(r'(https://[^;{}]+\.m3u8)', s)
        if not u:
            return 'no-url'
        return curl(u.group(1), proxy=proxy, ref=TURBO + '/', follow=True, timeout=15, code_only=True).strip() or 'timeout'
    except Exception as e:
        return 'ERR:' + type(e).__name__


def test_kino(proxy):
    try:
        s = curl(KINO + '/search?q=' + urllib.parse.quote('Чужой 3'), proxy=proxy, timeout=15)
        if not s:
            return 'no-search'
        idm = re.search(r'/site/view\?id=(\d+)', s)
        if not idm:
            return 'no-result'
        view = curl(KINO + '/site/view?id=' + idm.group(1), proxy=proxy, timeout=15)
        srcs = re.findall(r'<source[^>]*src="([^"]+\.mp4)"', view)
        mp4 = next((x for x in srcs if '/Trailer/' not in x), srcs[0] if srcs else None)
        if not mp4:
            return 'no-mp4'
        full = (KINO + mp4 if mp4.startswith('/') else mp4).replace(' ', '%20')
        return curl(full, proxy=proxy, rng='0-1', follow=True, timeout=15, code_only=True).strip() or 'timeout'
    except Exception as e:
        return 'ERR:' + type(e).__name__


def main():
    print('200 = играет. Резолв и фетч идут через один прокси (IP-consistent).\n')
    hdr = f"{'PROXY':<16}{'EGRESS':<32}{'awmzone':<12}{'kinoteatr':<12}"
    print(hdr)
    print('-' * len(hdr))
    targets = [('direct', None)] + [(f'127.0.0.1:{p}', f'127.0.0.1:{p}') for p in PORTS]
    for label, proxy in targets:
        if proxy and not port_open(int(proxy.split(':')[1])):
            print(f"{label:<16}{'(порт закрыт)':<32}{'-':<12}{'-':<12}")
            continue
        ip = egress(proxy)
        aw = test_awmzone(proxy)[:11]
        kn = test_kino(proxy)[:11]
        print(f"{label:<16}{ip:<32}{aw:<12}{kn:<12}")


if __name__ == '__main__':
    main()
