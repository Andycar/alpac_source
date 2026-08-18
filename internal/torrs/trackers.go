//go:build torrs

package torrs

// defaultPublicTrackers is a curated list of public BitTorrent trackers, used
// to accelerate peer discovery for DHT-only magnets.  Updated against
// https://github.com/ngosang/trackerslist (best/master) — keep the list short
// (<30) to avoid hammering trackers with no-op announces; the order is
// best-effort.
var defaultPublicTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.tracker.cl:1337/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://explodie.org:6969/announce",
	"udp://tracker.moeking.me:6969/announce",
	"udp://tracker.tiny-vps.com:6969/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://opentracker.i2p.rocks:6969/announce",
	"udp://9.rarbg.com:2810/announce",
	"udp://www.torrent.eu.org:451/announce",
	"udp://bt2.archive.org:6969/announce",
	"udp://bt1.archive.org:6969/announce",
	"udp://retracker01-msk-virt.corbina.net:80/announce",
	"udp://tracker.openchaintracker.com:6969/announce",
	"https://tracker.tamersunion.org:443/announce",
	"https://tracker.lilithraws.org:443/announce",
	"https://opentracker.i2p.rocks:443/announce",
	"http://tracker.opentrackr.org:1337/announce",
}

// announceTiers reshapes the flat tracker list as [][]string where each
// inner slice is one tier — needed by anacrolix Torrent.AddTrackers.
// Using one tracker per tier means the client probes them in parallel
// (good for cold starts).
func announceTiers() [][]string {
	tiers := make([][]string, len(defaultPublicTrackers))
	for i, tr := range defaultPublicTrackers {
		tiers[i] = []string{tr}
	}
	return tiers
}
