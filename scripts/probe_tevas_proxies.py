#!/usr/bin/env python3
# Проверка доступности ТЕВАС (pult.tevas.dev / tevas.team / tevas.tech + CDN
# bigsgppgs.tevas.dev) через локальные SOCKS5-прокси (по умолчанию 40001..40008)
# + direct.
#
# ТЕВАС режет datacenter-IP НА УРОВНЕ САЙТА: гео-блок → 301 на ad-парковку
# (quickresultseeker.com), а tevas.tech ещё и за Cloudflare Turnstile. CDN при
# этом открыт миру. Поэтому решает столбец `site`: нужен прокси, через который
# сайт отдаёт реальные результаты поиска (а не parking/cf-block).
#
# Колонки:
#   site = ok:<mirror> | parking | cf-block | no-result | fail
#   cdn  = 206/200 (играет) | код | -  (вторично: CDN обычно открыт)
#
# Запуск:
#   python3 scripts/probe_tevas_proxies.py             # таблица, порты 40001..40008
#   python3 scripts/probe_tevas_proxies.py 40004 40007 # только эти порты
#   python3 scripts/probe_tevas_proxies.py --q "Матрица" 40004   # ТРЕЙС титула
#       (подробно: сколько хитов в поиске, какой выбран, грузится ли страница
#        фильма, какой путь к файлу извлечён, статус CDN) — чтобы понять, почему
#        «белым горит, но результатов нет».
import subprocess, re, json, urllib.parse, socket, sys

# ТЕВАС фильтрует поиск по UA: macOS/Windows Chrome → «нечего показать».
# Linux X11 UA проходит. Тот же UA, что в modules/tevas/index.js.
UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36'
MIRRORS = ['https://pult.tevas.dev', 'https://tevas.team', 'https://tevas.tech']
CDN_MOVIE_HOST = 'bigsgppgs.tevas.dev'    # ротируется; при 404 обнови из DevTools
CDN_SERIAL_HOST = 'bigjjxjjs.tevas.dev'   # для сериалов (/serial/...)
QUERY = 'Матрица'

PARKING_RE = re.compile(r'quickresultseeker\.com|cdn-fileserver\.com|_ol_one_|_ol_lg_')
CF_RE = re.compile(r'cf-mitigated|Just a moment|challenge-platform', re.I)
HIT_RE = re.compile(r'href="(/kino/download/\?f=[^"]+?\.mp4[^"]*)"')
# с лейблом (как searchMovies в модуле): href, file, poster, span-label
SEARCH_RE = re.compile(r'href="(/kino/download/\?f=([^"&]+)\.mp4[^"]*)"[\s\S]*?<img[^>]+src="[^"]+"[\s\S]*?<span>([^<]+)</span>')
FILE_RE = re.compile(r'file:"//\{v\d\}/([^"\s]+\.mp4)', re.I)
# сериалы (как в модуле): карточка каталога, сезоны, эпизоды
SERIAL_BLOCK_RE = re.compile(r'<div\s+id="series">([\s\S]*?)<div\s+class="clear">', re.I)
SERIAL_CARD_RE = re.compile(r'<a\s+href="([^"#]+)"[^>]*>\s*<div\s+class="v\s+vi\s+p">\s*<img[^>]*src="[^"]*"[^>]*>\s*<span>([^<]+)</span>')
SEASON_RE = re.compile(r'href="(\d{1,2})(?:_([a-z0-9]+))?/\?big=(\d+)"', re.I)
EP_RE = re.compile(r'href="(?:\.\.?/[^"]*)?\?f=([^"&]+)\.mp4(?:&[^"]*)?"')


def normalize(s):
    s = (s or '').lower().replace('ё', 'е')
    s = re.sub(r'[^a-zа-я0-9 ]+', ' ', s)
    return re.sub(r'\s+', ' ', s).strip()


def extract_slug(href):
    rel = href
    m = re.match(r'https?://[^/]+(/.+)', href, re.I)
    if m:
        rel = m.group(1)
    m = re.match(r'/?serial/([^?#]+?)/?(?:\?|$|#)', rel, re.I) or re.match(r'([^?#]+?)/?(?:\?|$|#)', rel, re.I)
    return m.group(1).rstrip('/') if m else ''

# argv: целые = порты; --q "X" (или первый не-числовой токен) = title для трейса;
# --orig "Y" = original_title (важно для сериалов, матчинг по обоим).
QUERY_TRACE = None
ORIG_TRACE = ''
PORTS = []
_a = sys.argv[1:]
_i = 0
while _i < len(_a):
    tok = _a[_i]
    if tok in ('-q', '--q', '--query') and _i + 1 < len(_a):
        QUERY_TRACE = _a[_i + 1]
        _i += 2
        continue
    if tok in ('--orig', '--original') and _i + 1 < len(_a):
        ORIG_TRACE = _a[_i + 1]
        _i += 2
        continue
    if tok.isdigit():
        PORTS.append(int(tok))
    else:
        QUERY_TRACE = tok
    _i += 1
if not PORTS:
    PORTS = list(range(40001, 40009))


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


def short(mirror):
    return mirror.split('.')[0].replace('https://', '')


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
        return f"{d.get('ip','?')} {d.get('country','?')}/{d.get('org','?') or '?'}"[:30]
    except Exception:
        return '(no ip)'


def test_tevas(proxy):
    site_fail = 'fail'
    for mirror in MIRRORS:
        html = curl(mirror + '/search/search.php?q=' + urllib.parse.quote(QUERY),
                    proxy=proxy, ref=mirror + '/', follow=True, timeout=15)
        if not html:
            continue
        if PARKING_RE.search(html):
            site_fail = 'parking'
            continue
        if CF_RE.search(html[:4096]):
            site_fail = 'cf-block'
            continue
        m = HIT_RE.search(html)
        if not m:
            site_fail = 'no-result'
            continue
        # сайт пустил — резолвим страницу фильма и CDN-путь
        href = m.group(1).replace('&amp;', '&')
        page = curl(mirror + href, proxy=proxy, ref=mirror + '/', follow=True, timeout=15)
        fm = FILE_RE.search(page or '')
        cdn = '-'
        if fm:
            cdn_url = 'https://' + CDN_MOVIE_HOST + '/' + fm.group(1)
            cdn = curl(cdn_url, proxy=proxy, rng='0-1', ref='https://pult.tevas.dev/',
                       follow=True, timeout=15, code_only=True).strip() or 'timeout'
        else:
            cdn = 'no-path'
        return ('ok:' + short(mirror), cdn)
    return (site_fail, '-')


# Подробный трейс одного титула через прокси: видно, на каком шаге обрыв
# (почему «белым горит, но результатов нет»).
def trace_tevas(proxy, query):
    for mirror in MIRRORS:
        html = curl(mirror + '/search/search.php?q=' + urllib.parse.quote(query),
                    proxy=proxy, ref=mirror + '/', follow=True, timeout=15)
        if not html:
            print(f"    {short(mirror):6} поиск: пусто/недоступен")
            continue
        if PARKING_RE.search(html):
            print(f"    {short(mirror):6} поиск: ad-parking (гео-блок)")
            continue
        if CF_RE.search(html[:4096]):
            print(f"    {short(mirror):6} поиск: Cloudflare challenge")
            continue
        hits = SEARCH_RE.findall(html)
        if not hits:
            print(f"    {short(mirror):6} поиск: 0 результатов (титула нет в каталоге?)")
            continue
        labels = ', '.join(f"{lbl.strip()}" for _, _, lbl in hits[:5])
        print(f"    {short(mirror):6} поиск: {len(hits)} хит(ов) → {labels}")
        href = hits[0][0].replace('&amp;', '&')
        page = curl(mirror + href, proxy=proxy, ref=mirror + '/', follow=True, timeout=15)
        if not page:
            print(f"    {short(mirror):6} страница фильма: не загрузилась")
            return
        fm = FILE_RE.search(page)
        if not fm:
            extra = ' (parking)' if PARKING_RE.search(page) else (' (CF)' if CF_RE.search(page[:4096]) else '')
            print(f"    {short(mirror):6} страница фильма: file:\"//{{vN}}/…\" НЕ найден{extra} ← вот почему пусто")
            return
        rel = fm.group(1)
        cdn_url = 'https://' + CDN_MOVIE_HOST + '/' + rel
        code = curl(cdn_url, proxy=proxy, rng='0-1', ref='https://pult.tevas.dev/',
                    follow=True, timeout=15, code_only=True).strip() or 'timeout'
        print(f"    {short(mirror):6} путь: {rel}")
        print(f"    {short(mirror):6} CDN ({CDN_MOVIE_HOST}): {code}" + ('  ← играет' if code in ('200', '206') else '  ← не отдаёт (обнови CDN_MOVIE_HOST)'))
        return
    print("    все зеркала недоступны/заблокированы через этот прокси")


# Трейс СЕРИАЛЬНОЙ ветки (serial=1): каталог /serial/ → совпадение → сезоны →
# эпизоды → CDN. Матчинг по title И original_title (как pickSerial в модуле).
def trace_serial(proxy, title, orig):
    for mirror in MIRRORS:
        html = curl(mirror + '/serial/', proxy=proxy, ref=mirror + '/', follow=True, timeout=20)
        if not html:
            print(f"    {short(mirror):6} /serial/: пусто/недоступен")
            continue
        if PARKING_RE.search(html):
            print(f"    {short(mirror):6} /serial/: ad-parking (гео-блок)")
            continue
        if CF_RE.search(html[:4096]):
            print(f"    {short(mirror):6} /serial/: Cloudflare challenge")
            continue
        block = SERIAL_BLOCK_RE.search(html)
        src = block.group(1) if block else html
        items = []
        for m in SERIAL_CARD_RE.finditer(src):
            slug = extract_slug(m.group(1).strip())
            if slug:
                items.append((slug, m.group(2).strip()))
        if not items:
            print(f"    {short(mirror):6} /serial/: каталог не распарсился")
            continue
        keys = [normalize(title)] + ([normalize(orig)] if orig else [])
        match = next(((s, n) for s, n in items if normalize(n) in keys), None)
        if not match:
            match = next(((s, n) for s, n in items for k in keys
                          if len(k) >= 3 and (k in normalize(n) or normalize(n) in k)), None)
        if not match:
            words = set(w for w in (' '.join(keys)).split() if len(w) > 2)
            cands = [n for s, n in items if any(w in normalize(n) for w in words)]
            print(f"    {short(mirror):6} /serial/: {len(items)} тайтлов, '{title}'/'{orig}' НЕ найден ← вот почему пусто")
            print(f"    {short(mirror):6} похожие в каталоге: {cands[:8] or '—'}")
            return
        slug, name = match
        print(f"    {short(mirror):6} /serial/: {len(items)} тайтлов, совпадение {name!r} (slug={slug})")
        shtml = curl(mirror + '/serial/' + slug + '/', proxy=proxy, ref=mirror + '/', follow=True, timeout=15)
        seasons = []
        seen = set()
        for m in SEASON_RE.finditer(shtml or ''):
            key = (m.group(1), (m.group(2) or '').lower())
            if key in seen:
                continue
            seen.add(key)
            seasons.append((int(m.group(1)), m.group(1) + ('_' + m.group(2).lower() if m.group(2) else ''), m.group(3)))
        if not seasons:
            print(f"    {short(mirror):6} сезоны: не найдены на /serial/{slug}/")
            return
        print(f"    {short(mirror):6} сезоны: {[s[0] for s in seasons]} (big={seasons[0][2]})")
        snum, sdir, big = seasons[0]
        ehtml = curl(mirror + '/serial/' + slug + '/' + sdir + '/?big=' + big, proxy=proxy, ref=mirror + '/', follow=True, timeout=15)
        eps = [m.group(1) + '.mp4' for m in EP_RE.finditer(ehtml or '')]
        if not eps:
            print(f"    {short(mirror):6} эпизоды: не найдены в сезоне {snum}")
            return
        rel = 'serial/' + slug + '/' + sdir + '/' + eps[0]
        cdn_url = 'https://' + CDN_SERIAL_HOST + '/' + rel
        code = curl(cdn_url, proxy=proxy, rng='0-1', ref='https://pult.tevas.dev/', follow=True, timeout=15, code_only=True).strip() or 'timeout'
        print(f"    {short(mirror):6} сезон {snum}: {len(eps)} эп., путь {rel}")
        print(f"    {short(mirror):6} CDN ({CDN_SERIAL_HOST}): {code}" + ('  ← играет' if code in ('200', '206') else '  ← не отдаёт (обнови CDN_SERIAL_HOST)'))
        return
    print("    все зеркала /serial/ недоступны/заблокированы через этот прокси")


def main():
    if QUERY_TRACE:
        print(f"ТРЕЙС {QUERY_TRACE!r}" + (f" / {ORIG_TRACE!r}" if ORIG_TRACE else "") + " (как модуль: поиск→страница→file→CDN)\n")
        targets = [('direct', None)] + [(f'127.0.0.1:{p}', f'127.0.0.1:{p}') for p in PORTS]
        for label, proxy in targets:
            if proxy and not port_open(int(proxy.split(':')[1])):
                print(f"{label} (порт закрыт)")
                continue
            print(f"{label}  [{egress(proxy)}]")
            print("  ФИЛЬМ:")
            trace_tevas(proxy, QUERY_TRACE)
            print("  СЕРИАЛ:")
            trace_serial(proxy, QUERY_TRACE, ORIG_TRACE)
            print()
        return

    print('Решает столбец site: нужен ok:<mirror> (сайт пустил, не parking/cf).')
    print('cdn 206/200 = поток играет (CDN обычно открыт; 404 → обнови CDN_MOVIE_HOST).\n')
    hdr = f"{'PROXY':<16}{'EGRESS':<32}{'site':<14}{'cdn':<8}"
    print(hdr)
    print('-' * len(hdr))
    targets = [('direct', None)] + [(f'127.0.0.1:{p}', f'127.0.0.1:{p}') for p in PORTS]
    for label, proxy in targets:
        if proxy and not port_open(int(proxy.split(':')[1])):
            print(f"{label:<16}{'(порт закрыт)':<32}{'-':<14}{'-':<8}")
            continue
        ip = egress(proxy)
        site, cdn = test_tevas(proxy)
        print(f"{label:<16}{ip:<32}{site[:13]:<14}{cdn[:7]:<8}")


if __name__ == '__main__':
    main()
