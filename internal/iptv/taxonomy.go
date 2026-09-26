package iptv

import (
	"regexp"
	"strings"
)

// taxonomy.go — страна и жанр как ДВА НЕЗАВИСИМЫХ измерения.
//
// У канала исторически было одно поле Group, и в него сваливалось всё подряд:
// «Русские» рядом с «Музыка», «Итальянские» рядом со «Спорт». Смешение видно
// зрителю — в списке жанров всплывают страны, а музыкальный канал теряет
// страну, потому что место занято жанром. Плюс группа приходит из донора,
// а доноры размечают кто во что горазд («HD Orig», «другие», «Undefined»).
//
// Поэтому канал хранит Country и Genre раздельно, а Group остаётся
// производным полем для клиентов, которые про новую схему не знают.

// countryByCode maps the iptv-org tvg-id country suffix ("BBCNews.uk@SD" → uk)
// to the registry's group name. Названия — прилагательные во мн. числе: так
// уже были заведены «Русские»/«Польские», и ломать привычный вид ради
// единообразия с ISO смысла нет.
var countryByCode = map[string]string{
	"ru": "Русские", "ua": "Украинские", "by": "Белорусские", "kz": "Казахские",
	"az": "Азербайджанские", "am": "Армянские", "ge": "Грузинские",
	"md": "Молдавские", "lt": "Литовские", "lv": "Латвийские", "ee": "Эстонские",
	"uz": "Узбекские", "tj": "Таджикские", "tm": "Туркменские", "kg": "Киргизские",
	"pl": "Польские", "it": "Итальянские", "tr": "Турецкие", "us": "Американские",
	"gb": "Британские", "uk": "Британские", "de": "Немецкие", "fr": "Французские",
	"es": "Испанские", "pt": "Португальские", "nl": "Нидерландские",
	"be": "Бельгийские", "at": "Австрийские", "ch": "Швейцарские",
	"gr": "Греческие", "bg": "Болгарские", "ro": "Румынские", "rs": "Сербские",
	"hr": "Хорватские", "si": "Словенские", "sk": "Словацкие", "cz": "Чешские",
	"hu": "Венгерские", "al": "Албанские", "mk": "Македонские", "ba": "Боснийские",
	"se": "Шведские", "no": "Норвежские", "dk": "Датские", "fi": "Финские",
	"ie": "Ирландские", "is": "Исландские", "ca": "Канадские", "mx": "Мексиканские",
	"br": "Бразильские", "ar": "Аргентинские", "cl": "Чилийские", "pe": "Перуанские",
	"co": "Колумбийские", "ve": "Венесуэльские", "ec": "Эквадорские",
	"bo": "Боливийские", "hn": "Гондурасские", "ht": "Гаитянские",
	"cn": "Китайские", "tw": "Тайваньские", "jp": "Японские", "kr": "Корейские",
	"in": "Индийские", "id": "Индонезийские", "vn": "Вьетнамские",
	"th": "Тайские", "ph": "Филиппинские", "my": "Малайзийские", "la": "Лаосские",
	"pk": "Пакистанские", "af": "Афганские", "ir": "Иранские", "iq": "Иракские",
	"il": "Израильские", "sa": "Саудовские", "qa": "Катарские", "ae": "Эмиратские",
	"eg": "Египетские", "ma": "Марокканские", "dz": "Алжирские", "au": "Австралийские",
	"nz": "Новозеландские",
}

// otherCountriesGroup — страны, которых слишком мало для собственной группы.
const otherCountriesGroup = "Другие страны"

// preferredCode разрешает неоднозначность обратного отображения: у «Британских»
// в countryByCode два ключа (gb и uk), а клиенту нужен ОДИН код — он рисует по
// нему флаг.
var preferredCode = map[string]string{"Британские": "gb"}

// codeByCountry — обратная карта: название группы → ISO-код. Нужна клиентам,
// которые показывают флаг: рисовать его по русскому прилагательному нельзя.
var codeByCountry = func() map[string]string {
	m := make(map[string]string, len(countryByCode))
	for code, name := range countryByCode {
		if pref, ok := preferredCode[name]; ok {
			m[name] = pref
			continue
		}
		// Карта обходится в случайном порядке, поэтому при двух кандидатах
		// берём лексикографически меньший — иначе код скакал бы между стартами.
		if cur, ok := m[name]; !ok || code < cur {
			m[name] = code
		}
	}
	return m
}()

// CountryCode returns the ISO code for a country group name ("Русские" → "ru").
// Пустая строка для «Других стран» — это сборная группа, у неё кода нет.
func CountryCode(country string) string {
	return codeByCountry[strings.TrimSpace(country)]
}

// countryGroups — множество всех валидных страновых названий (значения карты
// плюс сборная группа). Нужно, чтобы отличить страну от жанра в СТАРОМ поле
// Group, где лежит и то и другое вперемешку.
var countryGroups = func() map[string]struct{} {
	m := map[string]struct{}{otherCountriesGroup: {}}
	for _, v := range countryByCode {
		m[v] = struct{}{}
	}
	return m
}()

// genreAliases приводит разнобой донорской разметки к одному набору жанров.
// Ключи — в нижнем регистре: доноры пишут и «Музыка», и «музыка», и «Music».
var genreAliases = map[string]string{
	"музыка": "Музыка", "music": "Музыка", "музыкальные": "Музыка",
	"детские": "Детские", "kids": "Детские", "children": "Детские",
	"дети": "Детские", "animation": "Детские", "мультфильмы": "Детские",
	"спорт": "Спорт", "sport": "Спорт", "sports": "Спорт", "спортивные": "Спорт",
	"новости": "Новости", "news": "Новости", "информационные": "Новости",
	"кино": "Кино", "movies": "Кино", "movie": "Кино", "фильмы": "Кино",
	"cinema": "Кино", "series": "Кино", "сериалы": "Кино",
	"познавательные": "Познавательные", "documentary": "Познавательные",
	"education": "Познавательные", "образовательные": "Познавательные",
	"наука": "Познавательные", "history": "Познавательные",
	"развлекательные": "Развлекательные", "entertainment": "Развлекательные",
	"comedy": "Развлекательные", "юмор": "Развлекательные",
	"взрослые": "Взрослые", "adult": "Взрослые", "xxx": "Взрослые",
	"эротика": "Взрослые", "18+": "Взрослые",
	"религиозные": "Религиозные", "religious": "Религиозные",
	"религия": "Религиозные", "travel": "Путешествия", "путешествия": "Путешествия",
	"регионы": "Региональные", "региональные": "Региональные", "regional": "Региональные",
}

// junkGroups — донорские «группы», которые не несут ни страны, ни жанра.
// Качество (HD/4K) живёт в Channel.Quality, а «другие»/«Undefined» не значат
// ничего: канал с такой группой должен остаться БЕЗ жанра, а не получить
// мусорный.
var junkGroups = map[string]struct{}{
	"": {}, "другие": {}, "other": {}, "others": {}, "прочее": {},
	"undefined": {}, "ungrouped": {}, "general": {}, "общие": {},
	"hd": {}, "hd orig": {}, "4k": {}, "uhd": {}, "sd": {}, "fhd": {},
}

// GenreOf normalizes a donor group label to a canonical genre.
// Пустая строка — «жанра нет», и это законный ответ: лучше не показать жанр,
// чем показать выдуманный.
func GenreOf(group string) string {
	g := strings.ToLower(strings.TrimSpace(group))
	if g == "" {
		return ""
	}
	if _, junk := junkGroups[g]; junk {
		return ""
	}
	if v, ok := genreAliases[g]; ok {
		return v
	}
	// Составные метки доноров: "Animation;Kids", "Comedy;Family;Movies" —
	// берём первый распознанный кусок.
	for _, part := range strings.FieldsFunc(g, func(r rune) bool {
		return r == ';' || r == ',' || r == '|' || r == '/'
	}) {
		if v, ok := genreAliases[strings.TrimSpace(part)]; ok {
			return v
		}
	}
	return ""
}

// IsCountryGroup reports whether a legacy Group value names a country.
func IsCountryGroup(group string) bool {
	_, ok := countryGroups[strings.TrimSpace(group)]
	return ok
}

// CountryByTvgID extracts the country from an iptv-org style tvg-id
// ("BBCNews.uk@SD" → "Британские"). Единственный надёжный источник страны для
// канала, чья группа занята жанром.
func CountryByTvgID(tvgID string) string {
	s := strings.TrimSpace(tvgID)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	i := strings.LastIndexByte(s, '.')
	if i < 0 || i == len(s)-1 {
		return ""
	}
	return countryByCode[strings.ToLower(s[i+1:])]
}

// countryByHostSuffix — страна по домену вещателя. Третий источник после
// группы и tvg-id, и для части парка единственный: канал вроде «Матч ТВ» или
// «Карусель» приходит из донора с жанровой группой и без tvg-id, так что
// страну видно только по тому, чей CDN его отдаёт.
var countryByHostSuffix = []struct {
	suffix  string
	country string
}{
	// Вещательные CDN — вернее любого TLD: ngenix.net и cdnvideo.ru живут на
	// нейтральных доменах, но раздают именно российское вещание.
	{"ngenix.net", "Русские"}, {"cdnvideo.ru", "Русские"},
	{"matchtv.ru", "Русские"}, {"1tv.ru", "Русские"}, {"1internet.tv", "Русские"},
	{"ntv.ru", "Русские"}, {"smotrim.ru", "Русские"}, {"vgtrk.ru", "Русские"},
	{"rutube.ru", "Русские"}, {"gpmradio.ru", "Русские"},
	{"beltelecom.by", "Белорусские"}, {"kaztrk.kz", "Казахские"},
	{"cinerama.uz", "Узбекские"},
	// Затем — национальные домены. Список намеренно короткий: сюда попадают
	// только те ccTLD, что почти не используются как «красивые» домены. .co,
	// .tv, .me, .io, .la, .am, .fm отсутствуют СПЕЦИАЛЬНО — по ним страну
	// определять нельзя, домен .tv это не Тувалу, а телевидение вообще.
	{".ru", "Русские"}, {".by", "Белорусские"}, {".ua", "Украинские"},
	{".kz", "Казахские"}, {".uz", "Узбекские"}, {".az", "Азербайджанские"},
	{".ge", "Грузинские"}, {".pl", "Польские"}, {".it", "Итальянские"},
	{".de", "Немецкие"}, {".fr", "Французские"}, {".es", "Испанские"},
	{".nl", "Нидерландские"}, {".gr", "Греческие"}, {".bg", "Болгарские"},
	{".ro", "Румынские"}, {".rs", "Сербские"}, {".cz", "Чешские"},
	{".sk", "Словацкие"}, {".hu", "Венгерские"}, {".tr", "Турецкие"},
	{".ee", "Эстонские"}, {".lv", "Латвийские"}, {".lt", "Литовские"},
}

// CountryByHost derives the country from a stream host.
func CountryByHost(rawURL string) string {
	host := hostOf(rawURL)
	if host == "" {
		return ""
	}
	for _, e := range countryByHostSuffix {
		if strings.HasSuffix(host, e.suffix) || strings.Contains(host, e.suffix+"/") {
			return e.country
		}
	}
	return ""
}

// genreByNameHint — последний рубеж: жанр из САМОГО НАЗВАНИЯ канала. Работает
// только на однозначных словах, иначе легко приписать жанр по случайному
// совпадению («Спас» ≠ спорт). Порядок важен: более специфичное раньше.
var genreByNameHint = []struct {
	needles []string
	genre   string
}{
	{[]string{" sport", "sport ", "спорт", "футбол", "football", "matchtv", "матч!"}, "Спорт"},
	{[]string{"kids", "детск", "мульт", "cartoon", "baby", "junior"}, "Детские"},
	{[]string{"music", "музык", "hits", "хит", "radio "}, "Музыка"},
	{[]string{"news", "новости", "24 news"}, "Новости"},
	{[]string{"cinema", "кино", "movie", "фильм"}, "Кино"},
	{[]string{"discovery", "nat geo", "history", "познават", "наука"}, "Познавательные"},
}

// kidsChannelNames — детские каналы, которые надо узнавать ПО ИМЕНИ, потому что
// донорская разметка на них не работает. Список закрытый и проверяется по ТОЧНОМУ
// базовому имени, а не по вхождению: «Мама» подстрокой поймала бы «Мамбо ТВ»,
// а «О!» — вообще что угодно.
var kidsChannelNames = map[string]struct{}{
	"карусель": {}, "мульт": {}, "мультиландия": {}, "мультимузыка": {},
	"мультмузыка": {}, "лёва": {}, "лева": {}, "о!": {}, "рыжий": {},
	"солнце": {}, "мама": {}, "тлум": {}, "тлум хд": {}, "детский мир": {},
	"союзмультфильм": {}, "мультимания": {}, "киномульт": {}, "малыш": {},
	"nickelodeon": {}, "никелодеон": {}, "tiji": {}, "tiji tv": {},
	"disney channel": {}, "cartoon network": {}, "cartoonito": {},
	"boomerang": {}, "gulli": {}, "jim jam": {}, "baby tv": {}, "babytv": {},
	"da vinci": {}, "da vinci kids": {},
}

// kidsBaseNameCleanup убирает то, чем доноры украшают одно и то же имя:
// «Карусель orig», «Карусель +3», «Карусель (Ставрополь)», «Мульт HD».
var kidsBaseNameCleanup = regexp.MustCompile(`(?i)\s*(\(.*\)|\[.*\]|\borig\b|\bhd\b|\bfhd\b|\buhd\b|\bsd\b|\b4k\b|\b50fps\b|\+\d+)`)

// kidsGenreByName returns "Детские" for a known kids channel, else "".
func kidsGenreByName(name string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = strings.TrimSpace(kidsBaseNameCleanup.ReplaceAllString(base, ""))
	base = strings.Trim(base, " -–—.,")
	if base == "" {
		return ""
	}
	if _, ok := kidsChannelNames[base]; ok {
		return "Детские"
	}
	return ""
}

// GenreByName guesses a genre from the channel name. Использовать ТОЛЬКО когда
// донорская группа жанра не дала — догадка слабее разметки.
func GenreByName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return ""
	}
	for _, h := range genreByNameHint {
		for _, needle := range h.needles {
			if strings.Contains(n, needle) {
				return h.genre
			}
		}
	}
	return ""
}

// Classify derives (country, genre) for a channel from what is known about it:
// the legacy group label, the tvg-id and the name. Порядок источников — от
// самого достоверного к самому шаткому.
func Classify(group, tvgID, name string) (country, genre string) {
	group = strings.TrimSpace(group)
	if IsCountryGroup(group) {
		country = group
	} else {
		genre = GenreOf(group)
	}
	if country == "" {
		country = CountryByTvgID(tvgID)
	}
	// Именной список СИЛЬНЕЕ донорской группы. У доноров детские каналы размечены
	// как попало: «Солнце» лежит в «развлекательные», «Мама» — в мусорной «другие»,
	// а варианты с суффиксами («Карусель orig», «О! orig») вообще без группы. По
	// имени же они опознаются однозначно, поэтому здесь догадка надёжнее разметки.
	if g := kidsGenreByName(name); g != "" {
		genre = g
	}
	if genre == "" {
		genre = GenreByName(name)
	}
	return country, genre
}

// ClassifyChannel derives (country, genre) for a registry channel, using its
// SOURCES as the last resort for the country. Отдельно от Classify, потому что
// та работает со строками и не знает про источники.
func ClassifyChannel(c *RegChannel) (country, genre string) {
	country, genre = Classify(c.Group, c.TvgID, c.Name)
	if country != "" {
		return country, genre
	}
	for _, s := range c.Sources() {
		// Резолвер знает вещателя по имени, даже когда URL ещё не добыт.
		if s.Resolver != "" {
			if got := CountryByHost(s.Resolver + ".ru"); got != "" {
				return got, genre
			}
		}
		if got := CountryByHost(s.URL); got != "" {
			return got, genre
		}
	}
	return country, genre
}

// GroupFor renders the single legacy Group value for clients that know nothing
// about the split. Страна впереди жанра: канал ищут прежде всего по стране, а
// жанровая полка — уточнение.
func GroupFor(country, genre string) string {
	if country != "" {
		return country
	}
	return genre
}
