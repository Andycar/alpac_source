package rutracker

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The listing contract below is a straight port of the reference parser in
// this tree — JacRed/Controllers/RutrackerController.cs:83-107. Rows are split
// on the row class instead of being DOM-parsed: the forum's markup is not
// well-formed enough for a strict parser, and every other tracker parser in
// lampac-go is regex-based too.
const rowSeparator = `class="tCenter hl-tr"`

// loggedInMarker is present on every page rendered for an authenticated user
// (RutrackerController.cs:75). Its absence is the definition of "our session
// died", and it is the only reliable signal — rutracker answers 200 for guests.
const loggedInMarker = `id="logged-in-username"`

var (
	reTopicID  = regexp.MustCompile(`(?i)href="viewtopic\.php\?t=([0-9]+)"`)
	reTitle    = regexp.MustCompile(`(?i)href="viewtopic\.php\?t=[0-9]+">([^\n\r]+)</a>`)
	reForumID  = regexp.MustCompile(`(?i)href="tracker\.php\?f=([0-9]+)`)
	reSeeders  = regexp.MustCompile(`(?i)class="seedmed[^"]*"[^>]*>([0-9]+)`)
	reLeechers = regexp.MustCompile(`(?i)title="Личи"[^>]*>([0-9]+)`)
	// Fallback for markup variants that drop the Russian title attribute.
	reLeechersAlt = regexp.MustCompile(`(?i)class="leechmed[^"]*"[^>]*>([0-9]+)`)
	reSize        = regexp.MustCompile(`(?i)href="dl\.php\?t=[0-9]+"[^>]*>([^<]+)</a>`)
	// The sortable table carries the exact byte count on the size cell
	// (data-ts_text). Preferred over parsing "49.6 GB", which is lossy.
	reSizeExact = regexp.MustCompile(`(?i)class="[^"]*tor-size"[^>]*data-ts_text="([0-9]+)"`)
	reDate     = regexp.MustCompile(`(?i)<p>([0-9]{2}-[^-<]+-[0-9]{2})</p>`)
	reTags     = regexp.MustCompile(`<[^>]+>`)
	reSpaces   = regexp.MustCompile(`[\n\r\t ]+`)
	reMagnet   = regexp.MustCompile(`(?i)href="(magnet:[^"]+)"\s+class="(?:med )?med magnet-link"`)
	reBTIH     = regexp.MustCompile(`(?i)magnet:\?xt=urn:btih:([a-z0-9]+)`)
)

// parseListing extracts releases from a tracker.php page. It does not judge
// authorization — callers check loggedIn first (an unauthorized page simply
// has no rows, which would otherwise be indistinguishable from "not found").
func parseListing(page, host string) []Release {
	parts := strings.Split(page, rowSeparator)
	if len(parts) < 2 {
		return nil
	}
	out := make([]Release, 0, len(parts)-1)
	for _, row := range parts[1:] {
		if strings.TrimSpace(row) == "" {
			continue
		}
		// A row's markup ends where the next table starts; cutting at the
		// closing tag keeps a malformed page from bleeding fields across rows.
		if i := strings.Index(row, "</tr>"); i > 0 {
			row = row[:i]
		}
		rel, ok := parseRow(row, host)
		if !ok {
			continue
		}
		out = append(out, rel)
	}
	return out
}

func parseRow(row, host string) (Release, bool) {
	match := func(re *regexp.Regexp) string {
		m := re.FindStringSubmatch(row)
		if len(m) < 2 {
			return ""
		}
		v := html.UnescapeString(strings.TrimSpace(m[1]))
		return strings.TrimSpace(reSpaces.ReplaceAllString(v, " "))
	}

	topicID, _ := strconv.Atoi(match(reTopicID))
	forumID, _ := strconv.Atoi(match(reForumID))
	title := strings.TrimSpace(reTags.ReplaceAllString(match(reTitle), ""))
	if topicID == 0 || forumID == 0 || title == "" {
		return Release{}, false
	}

	seeders, _ := strconv.Atoi(match(reSeeders))
	leechers, _ := strconv.Atoi(match(reLeechers))
	if leechers == 0 {
		leechers, _ = strconv.Atoi(match(reLeechersAlt))
	}

	// "4.5&nbsp;GB &#8595;" — after unescaping that is a non-breaking space
	// (U+00A0, which \s does not cover) plus a down arrow.
	sizeName := strings.TrimSpace(strings.NewReplacer("↓", "", " ", " ").Replace(match(reSize)))
	sizeName = strings.TrimSpace(reSpaces.ReplaceAllString(sizeName, " "))

	// Exact bytes when the sortable table exposes them, the human string
	// otherwise (lossy by ~1%, so a size filter can misjudge a borderline row).
	sizeBytes, _ := strconv.ParseInt(match(reSizeExact), 10, 64)
	if sizeBytes <= 0 {
		sizeBytes = parseSize(sizeName)
	}
	if sizeName == "" && sizeBytes > 0 {
		sizeName = humanSize(sizeBytes)
	}

	return Release{
		TopicID:   topicID,
		ForumID:   forumID,
		Title:     title,
		URL:       host + "/forum/viewtopic.php?t=" + strconv.Itoa(topicID),
		SizeName:  sizeName,
		SizeBytes: sizeBytes,
		Seeders:   seeders,
		Leechers:  leechers,
		CreatedAt: parseDate(match(reDate)),
		Types:     forumTypes(forumID),
	}, true
}

// parseMagnetLink pulls the magnet off a topic page
// (RutrackerController.cs:45).
func parseMagnetLink(page string) string {
	m := reMagnet.FindStringSubmatch(page)
	if len(m) < 2 {
		return ""
	}
	return html.UnescapeString(m[1])
}

// MagnetHash returns the lowercase btih of a magnet URI, or "".
func MagnetHash(magnet string) string {
	m := reBTIH.FindStringSubmatch(magnet)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

// ---------------------------------------------------------------------------
//  size / date
// ---------------------------------------------------------------------------

var reSizeParts = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(TB|GB|MB|KB|B|ТБ|ГБ|МБ|КБ|Б)`)

// parseSize turns "4.5 GB" / "1,45 ГБ" into bytes. The listing has no exact
// byte count, so this is an approximation by design — it rounds DOWN so a
// size filter (pidtorFilterResults) errs on the permissive side rather than
// dropping a release that is actually within the limit.
func parseSize(s string) int64 {
	m := reSizeParts.FindStringSubmatch(strings.TrimSpace(s))
	if len(m) < 3 {
		return 0
	}
	num, err := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
	if err != nil || num <= 0 {
		return 0
	}
	var mult float64 = 1
	switch strings.ToUpper(m[2]) {
	case "TB", "ТБ":
		mult = 1 << 40
	case "GB", "ГБ":
		mult = 1 << 30
	case "MB", "МБ":
		mult = 1 << 20
	case "KB", "КБ":
		mult = 1 << 10
	}
	return int64(num * mult)
}

// humanSize renders bytes the way the listing would, for rows where only the
// exact count survived.
func humanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return strconv.FormatFloat(float64(b)/(1<<30), 'f', 2, 64) + " GB"
	case b >= 1<<20:
		return strconv.FormatFloat(float64(b)/(1<<20), 'f', 2, 64) + " MB"
	case b >= 1<<10:
		return strconv.FormatFloat(float64(b)/(1<<10), 'f', 2, 64) + " KB"
	default:
		return strconv.FormatInt(b, 10) + " B"
	}
}

// ruMonths maps rutracker's abbreviated Russian months ("15-Май-24").
var ruMonths = map[string]time.Month{
	"янв": time.January, "фев": time.February, "мар": time.March,
	"апр": time.April, "май": time.May, "июн": time.June,
	"июл": time.July, "авг": time.August, "сен": time.September,
	"окт": time.October, "ноя": time.November, "дек": time.December,
}

func parseDate(s string) time.Time {
	parts := strings.Split(strings.TrimSpace(s), "-")
	if len(parts) != 3 {
		return time.Time{}
	}
	day, err1 := strconv.Atoi(parts[0])
	year, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return time.Time{}
	}
	key := strings.ToLower(parts[1])
	if len([]rune(key)) > 3 {
		key = string([]rune(key)[:3])
	}
	mon, ok := ruMonths[key]
	if !ok {
		return time.Time{}
	}
	return time.Date(2000+year, mon, day, 0, 0, 0, 0, time.UTC)
}

// ---------------------------------------------------------------------------
//  forum → types
// ---------------------------------------------------------------------------

// forumTypeGroups is the category table from RutrackerController.cs:112-338.
// Lampa filters rails by these type names, so an unknown forum yields nil —
// the row still ships, it just carries no type.
var forumTypeGroups = []struct {
	types  []string
	forums []int
}{
	{[]string{"movie"}, []int{
		22, 1666, 941, 1950, 2090, 2221, 2091, 2092, 2093, 2200, 2540, 934,
		505, 124, 1457, 2199, 313, 312, 1247, 2201, 2339, 140, 252, 2198,
	}},
	{[]string{"multfilm"}, []int{2343, 930, 2365, 208, 539, 209}},
	{[]string{"multserial"}, []int{921, 815, 1460}},
	{[]string{"serial"}, []int{
		842, 235, 242, 819, 1531, 721, 1102, 1120, 1214, 489, 387, 9, 81, 119,
		1803, 266, 193, 1690, 1459, 825, 1248, 1288, 325, 534, 694, 704, 915, 1939,
	}},
	{[]string{"anime"}, []int{1105, 2491, 1389}},
	{[]string{"documovie"}, []int{709}},
	{[]string{"docuserial", "documovie"}, []int{
		46, 671, 2177, 2538, 251, 98, 97, 851, 2178, 821, 2076, 56, 2123, 876,
		2139, 1467, 1469, 249, 552, 500, 2112, 1327, 1468, 2168, 2160, 314,
		1281, 2110, 979, 2169, 2164, 2166, 2163,
	}},
	{[]string{"tvshow"}, []int{
		24, 1959, 939, 1481, 113, 115, 882, 1482, 393, 2537, 532, 827,
	}},
	{[]string{"sport"}, []int{
		2103, 2522, 2485, 2486, 2479, 2089, 1794, 845, 2312, 343, 2111, 1527,
		2069, 1323, 2009, 2000, 2010, 2006, 2007, 2005, 259, 2004, 1999, 2001,
		2002, 283, 1997, 2003, 1608, 1609, 2294, 1229, 1693, 2532, 136, 592,
		2533, 1952, 1621, 2075, 1668, 1613, 1614, 1623, 1615, 1630, 2425, 2514,
		1616, 2014, 1442, 1491, 1987, 1617, 1620, 1998, 1343, 751, 1697, 255,
		260, 261, 256, 1986, 660, 1551, 626, 262, 1326, 978, 1287, 1188, 1667,
		1675, 257, 875, 263, 2073, 550, 2124, 1470, 528, 486, 854, 2079, 1336,
		2171, 1339, 2455, 1434, 2350, 1472, 2068, 2016,
	}},
}

var forumTypeIndex = func() map[int][]string {
	m := make(map[int][]string, 256)
	for _, g := range forumTypeGroups {
		for _, f := range g.forums {
			m[f] = g.types
		}
	}
	return m
}()

func forumTypes(forumID int) []string { return forumTypeIndex[forumID] }
