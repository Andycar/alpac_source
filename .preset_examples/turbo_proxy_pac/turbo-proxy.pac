function FindProxyForURL(url, host) {
    if (host.indexOf("obrut.show") !== -1) return "SOCKS5 {{socks_host}}:{{socks_port}}";
    if (host.indexOf("superdupercdn") !== -1) return "SOCKS5 {{socks_host}}:{{socks_port}}";
    if (host.indexOf("videoframe2.com") !== -1) return "SOCKS5 {{socks_host}}:{{socks_port}}";
    if (host.indexOf("kinescopecdn.net") !== -1) return "SOCKS5 {{socks_host}}:{{socks_port}}";
    if (host.indexOf("zonasearch.com") !== -1) return "SOCKS5 {{socks_host}}:{{socks_port}}";
    if (host.indexOf("mzona.net") !== -1) return "SOCKS5 {{socks_host}}:{{socks_port}}";
    if (host.indexOf("zona.im") !== -1) return "SOCKS5 {{socks_host}}:{{socks_port}}";
    if (host.indexOf("interkh.com") !== -1) return "SOCKS5 {{socks_host}}:{{socks_port}}";
    return "DIRECT";
}
