function FindProxyForURL(url, host) {
    if (host.indexOf("obrut.show") !== -1) return "SOCKS5 127.0.0.1:40007";
    if (host.indexOf("superdupercdn") !== -1) return "SOCKS5 127.0.0.1:40007";
    if (host.indexOf("videoframe2.com") !== -1) return "SOCKS5 127.0.0.1:40007";
    if (host.indexOf("kinescopecdn.net") !== -1) return "SOCKS5 127.0.0.1:40007";
    if (host.indexOf("zonasearch.com") !== -1) return "SOCKS5 127.0.0.1:40007";
    if (host.indexOf("mzona.net") !== -1) return "SOCKS5 127.0.0.1:40007";
    if (host.indexOf("zona.im") !== -1) return "SOCKS5 127.0.0.1:40007";
    if (host.indexOf("interkh.com") !== -1) return "SOCKS5 127.0.0.1:40007";
    return "DIRECT";
}
