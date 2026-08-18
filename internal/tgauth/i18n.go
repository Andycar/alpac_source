package tgauth

import "fmt"

// Lang represents a supported UI language.
type Lang string

const (
	LangRU Lang = "ru"
	LangUK Lang = "uk"
	LangEN Lang = "en"
)

// ValidLang returns true if the language code is supported.
func ValidLang(l Lang) bool {
	return l == LangRU || l == LangUK || l == LangEN
}

// LangFromTG maps Telegram's language_code to our Lang.
func LangFromTG(code string) Lang {
	switch code {
	case "uk":
		return LangUK
	case "en":
		return LangEN
	default:
		return LangRU
	}
}

// Messages holds all bot UI strings for a single language.
type Messages struct {
	// ── Menu buttons (reply keyboard) ──
	BtnProfile      string
	BtnDevices      string
	BtnSettings     string
	BtnFeedback     string
	BtnLanguage     string
	BtnTickets      string
	BtnRemote       string
	BtnSubscription string

	// ── /start ──
	StartWelcome    string // has %s (first_name), %s (expiry), %d (devices)
	StartWelcomeNew string // has %s (first_name)

	// ── Language ──
	LangSelectTitle string
	LangChanged     string // has %s (lang flag+name)

	// ── Profile ──
	// Legacy template-based fields. Kept for back-compat but the v2 profile
	// renderer in handleProfile composes the message from the labels below.
	ProfileTitle    string
	ProfileStatus   string // has %s (status), %s (days)
	ProfileTelegram string // has %s (username)
	ProfileTgID     string // has %d (id)
	ProfileExpiry   string // has %s (date)
	ProfileCreated  string // has %s (date)
	ProfileDevices  string // has %d (count)
	ProfileActive   string
	ProfileExpired  string
	ProfileDaysLeft string // has %d (days)
	ProfileNoAccess string

	// ── Profile v2 labels (used by the rewritten handleProfile) ──
	// Compact words — full message is composed in code, not a template.
	ProfileBaseTitle          string // "Стандарт" / "Standard" — header for the base ExpiresAt block
	ProfilePremiumBadge       string // "Премиум" — short pill next to the status icon
	ProfilePremiumTitle       string // "Премиум-доступ" — heading for the overlay block
	ProfileDevicesTitle       string // "Устройства" / "Devices"
	ProfileIntegrationsTitle  string // "Интеграции" / "Integrations"
	ProfileUntilWord          string // "до" / "until"
	ProfileSinceWord          string // "с" / "since"
	ProfileRemainingPrefix    string // "ещё" / "still" — precedes "<N> days"
	ProfileExpiredSince       string // "истёк" / "expired" (used adjectivally with a date)
	ProfileCalendarSeriesWord string // "сериалов" / "shows" (plural noun, used after a number)
	ProfileCalendarNotifyOn   string // "уведомления вкл" / "notify on"
	ProfileCalendarNotifyOff  string // "уведомления выкл" / "notify off"
	ProfileAliceLabel         string // "Алиса" / "Alice"
	ProfileGroupFallback      string // "Группа" / "Group" — used when group name not set
	BtnCubUnlink              string // "Отвязать CUB" — inline button, shown only when a CUB account is linked
	CubUnlinkedToast          string // callback toast after unlinking the CUB anchor
	// Day-form plural words. Russian / Ukrainian have 3 forms; English has 2.
	ProfileDayForms [3]string // {1, 2-4, 5+} → e.g. ["день", "дня", "дней"]

	// ── Devices ──
	DevicesTitle     string // has %d (count)
	DevicesNone      string
	DeviceUnbound    string
	DeviceRenamed    string // has %s (new name)
	DeviceNotFound   string
	DeviceBoundAuto  string // has %s (label), %s (uid)
	DeviceMigrated   string // has %s (label), %s (old uid), %s (new uid)
	DeviceVerifyCode string // has %s (label), %s (code)
	DeviceLimitFmt   string // has %d (limit)
	DeviceRenameAsk  string // has %s (label), %s (uid)
	DeviceUnbindBtn  string
	// «Отвязать все» — одна кнопка вместо восьми одинаковых, с подтверждением:
	// операция выкидывает КАЖДОЕ устройство, а код для новой привязки показать
	// может только устройство, которое ещё работает.
	DeviceUnbindAllBtn  string
	DeviceUnbindAllAsk  string
	DeviceUnbindAllYes  string
	DeviceUnbindAllDone string
	DeviceUsage         string

	// ── Auth codes ──
	CodeAccepted     string
	CodeAutoApproved string // has %s (duration)
	CodeWaitAdmin    string
	CodeWrongFormat  string
	CodeNotFound     string
	CodeOtherAccount string
	CodeErrorApprove string
	CodeBindError    string
	CodeDeviceBound  string // has %s (label)

	// ── Admin ──
	AdminOnly         string
	AdminPanelNone    string
	AdminPanelLink    string // has %s (path)
	AdminAutoInfo     string // has %s (username), %s (duration), %s (ip)
	AdminRequest      string // has %s (username), %d (tgid), %s (ip), %s (code)
	AdminRejected     string // has %s (username)
	AdminApproved     string // has %s (username), %s (duration)
	AdminTokenCreated string // has %s (dur), %s (token), %s (token)

	// ── Admin approval buttons ──
	Approve1d     string
	Approve7d     string
	Approve30d    string
	Approve90d    string
	Approve365d   string
	ApproveReject string

	// ── Access granted/denied (to user) ──
	AccessApproved string // has %s (duration)
	AccessRejected string

	// ── Feedback ──
	FeedbackTitle string
	FeedbackStep1 string
	// ── Где именно проблема: без этого тикеты приходили без контекста, и первым
	//    ответом всегда был вопрос «а у вас плагин или приложение и на чём?».
	FeedbackWhere        string
	FeedbackPlatform     string
	CliLampa             string
	CliApp               string
	CliMsx               string
	CliOther             string
	PlatAndroidTv        string
	PlatPhone            string
	PlatSamsung          string
	PlatLg               string
	PlatHisense          string
	PlatAppleTv          string
	PlatBrowser          string
	PlatOther            string
	FeedbackStep2        string
	FeedbackStep3        string
	FeedbackCreated      string
	FeedbackCancelled    string
	FeedbackUnavailable  string
	FeedbackNoTickets    string
	FeedbackTicketsTitle string // has %d (count)
	FeedbackTicketView   string
	FeedbackReplyAsk     string
	FeedbackReplySent    string
	FeedbackReplyNotify  string // has %s (subject), %s (message)
	FeedbackAdminNew     string // has %s (user), %d (id), %s (cat), %s (subj), %s (msg)
	FeedbackAdminReply   string // has %s (user), %s (id), %s (subj), %s (msg)
	FeedbackSubjectLen   string
	FeedbackMsgLen       string
	FeedbackYourMsg      string
	FeedbackThread       string // has %d (count)
	FeedbackNoReplies    string
	FeedbackSupport      string // has %s (date)
	FeedbackYou          string // has %s (author), %s (date)
	FeedbackViewTktBtn   string
	FeedbackListBtn      string
	FeedbackReplyBtn     string

	// ── Feedback categories ──
	CatHelp    string
	CatBug     string
	CatFeature string
	CatThanks  string
	CatOther   string
	CatCancel  string

	// ── Feedback statuses ──
	StatusOpen string
	// Пользовательские статусы: человек спрашивает «мне ответили?», а не «в каком
	// состоянии тикет в админской воронке». Админские метки остаются для админки.
	StatusUserAnswered string
	StatusUserWaiting  string
	StatusUserClosed   string
	StatusInProgress   string
	StatusResolved     string
	StatusClosed       string

	// ── Kit ──
	KitUnavailable  string
	KitOpenBtn      string
	KitPrompt       string
	RoomOpenBtn     string
	RoomPrompt      string
	RoomCodeLine    string
	RoomUnavailable string
	RemoteOpenBtn   string
	RemotePrompt    string

	// ── Generic ──
	ErrorGeneric  string
	ErrorData     string
	NoAccess      string
	Cancelled     string
	EnterNewName  string
	ReturnToLampa string
	ClickBelow    string
	// Раздел «Обратная связь» — хаб: создать обращение / посмотреть свои.
	// Раньше кнопка сразу бросала в «Шаг 1/3», и вернуться к уже написанным
	// обращениям было неоткуда.
	FeedbackHubTitle string
	FeedbackHubEmpty string
	BtnFbNew         string
	BtnFbMine        string
	BtnFbBack        string
	Settings         string // menu button text for Kit WebApp
}

// T returns the Messages for the given language.
func T(lang Lang) *Messages {
	switch lang {
	case LangUK:
		return &messagesUK
	case LangEN:
		return &messagesEN
	default:
		return &messagesRU
	}
}

// ── Russian ──

var messagesRU = Messages{
	BtnProfile:      "👤 Профиль",
	BtnDevices:      "📱 Устройства",
	BtnSettings:     "⚙️ Настройки",
	BtnFeedback:     "📝 Обратная связь",
	BtnLanguage:     "🌐 Язык",
	BtnTickets:      "📋 Мои обращения",
	BtnRemote:       "🎮 Пульт",
	BtnSubscription: "💳 Подписка",

	StartWelcome:    "👋 Привет, <b>%s</b>!\n\nТвой доступ активен до %s\nУстройств: %d\n\nИспользуй кнопки ниже для управления.",
	StartWelcomeNew: "👋 Привет, <b>%s</b>!\n\nЧтобы привязать устройство, отправьте сюда 6-значный код — он показан в приложении на экране входа.",

	LangSelectTitle: "🌐 <b>Выберите язык:</b>",
	LangChanged:     "✅ Язык изменён: %s",

	ProfileTitle:    "━━━━━━━━━━━━━━━\n👤 <b>Профиль</b>\n━━━━━━━━━━━━━━━\n\n",
	ProfileStatus:   "%s%s\n\n",
	ProfileTelegram: "┌ 💬  <b>Telegram</b>\n│  @%s\n",
	ProfileTgID:     "│  ID: <code>%d</code>\n└\n\n",
	ProfileExpiry:   "┌ 📅  <b>Подписка</b>\n│  до <b>%s</b>\n",
	ProfileCreated:  "│  создан %s\n└\n\n",
	ProfileDevices:  "┌ 📱  <b>Устройства:</b> %d",
	ProfileActive:   "Активен",
	ProfileExpired:  "Истёк",
	ProfileDaysLeft: " · %d дн.",
	ProfileNoAccess: "━━━━━━━━━━━━━━━\n❌ <b>Нет доступа</b>\n━━━━━━━━━━━━━━━\n\nЧтобы привязать устройство, отправьте 6-значный код с экрана входа в приложении.",

	ProfileBaseTitle:          "Стандарт",
	ProfilePremiumBadge:       "Премиум",
	ProfilePremiumTitle:       "Премиум-доступ",
	ProfileDevicesTitle:       "Устройства",
	ProfileIntegrationsTitle:  "Интеграции",
	ProfileUntilWord:          "до",
	ProfileSinceWord:          "с",
	ProfileRemainingPrefix:    "ещё",
	ProfileExpiredSince:       "истёк",
	ProfileCalendarSeriesWord: "сериалов",
	ProfileCalendarNotifyOn:   "уведомления вкл",
	ProfileCalendarNotifyOff:  "уведомления выкл",
	ProfileAliceLabel:         "Алиса",
	ProfileGroupFallback:      "Группа",
	BtnCubUnlink:              "☁️ Отвязать CUB",
	CubUnlinkedToast:          "CUB-аккаунт отвязан",
	ProfileDayForms:           [3]string{"день", "дня", "дней"},

	DevicesTitle:        "📱 <b>Устройства</b> (%d)\n\n",
	DevicesNone:         "📱 Нет привязанных устройств.\n\nУстройство привязывается, когда вы присылаете сюда код с экрана входа в приложении.",
	DeviceUnbound:       "✅ Устройство отвязано.",
	DeviceRenamed:       "✅ Устройство переименовано: <b>%s</b>",
	DeviceNotFound:      "❌ Устройство не найдено.",
	DeviceBoundAuto:     "📱 Устройство привязано автоматически\n\n<b>%s</b>\nUID: <code>%s</code>\n\nДля управления: /devices",
	DeviceMigrated:      "🔄 Устройство переподключено\n\n<b>%s</b>\nUID: <code>%s</code> → <code>%s</code>\n\nОпознано по отпечатку устройства.",
	DeviceVerifyCode:    "📱 Новое устройство: <b>%s</b>\n\nДля привязки отправьте код:\n<code>%s</code>",
	DeviceLimitFmt:      "❌ Лимит устройств (%d). Отвяжите старое: /devices",
	DeviceRenameAsk:     "✏️ Введите новое имя для устройства <b>%s</b> (<code>%s</code>):",
	DeviceUnbindBtn:     "🗑 Отвязать",
	DeviceUnbindAllBtn:  "🧹 Отвязать все",
	DeviceUnbindAllAsk:  "🧹 Отвязать <b>все устройства</b> (%d)?\n\nПридётся заново привязывать каждое: код с экрана входа → сюда, в бот.",
	DeviceUnbindAllYes:  "🧹 Да, отвязать все",
	DeviceUnbindAllDone: "✅ Отвязано устройств: %d.",
	DeviceUsage:         "Использование: /unbind <code>UID</code>",

	CodeAccepted:     "✅ Код принят, устройство привязано.\n\nВернитесь в приложение — вход выполнится сам.",
	CodeAutoApproved: "✅ Код принят, устройство привязано на %s.\n\nВернитесь в приложение — вход выполнится сам.",
	CodeWaitAdmin:    "✅ Код принят. Больше ничего делать не нужно — осталось дождаться подтверждения администратора, сообщим здесь же.",
	CodeWrongFormat:  "❌ Это не похоже на код. Нужны 6 цифр с экрана входа в приложении.",
	CodeNotFound:     "❌ Код не найден или уже использован.",
	CodeOtherAccount: "❌ Этот код привязан к другому аккаунту.",
	CodeErrorApprove: "❌ Ошибка авто-одобрения.",
	CodeBindError:    "❌ Ошибка привязки устройства.",
	CodeDeviceBound:  "✅ Устройство <b>%s</b> привязано!",

	AdminOnly:         "⛔ Только администратор.",
	AdminPanelNone:    "❌ Админ-панель не настроена.",
	AdminPanelLink:    "🔧 Админ-панель:\n%s",
	AdminAutoInfo:     "ℹ️ Авто-одобрение: %s — %s\nIP: %s",
	AdminRequest:      "🔐 Запрос на доступ\n\nПользователь: %s\nTelegram ID: %d\nIP: %s\nКод: %s",
	AdminRejected:     "❌ Отклонено: %s",
	AdminApproved:     "✅ Одобрено: %s — %s",
	AdminTokenCreated: "📺 Device-токен создан (%s):\n\n<code>%s</code>\n\nДля Lampa:\n<code>/on/js/%s</code>",

	Approve1d:     "1 день",
	Approve7d:     "7 дней",
	Approve30d:    "30 дней",
	Approve90d:    "90 дней",
	Approve365d:   "365 дней",
	ApproveReject: "❌ Отклонить",

	AccessApproved: "✅ Доступ открыт на %s!\n\nВернитесь в приложение — вход выполнится сам.",
	AccessRejected: "❌ Администратор отклонил ваш запрос на доступ.",

	FeedbackTitle:        "📝 <b>Обратная связь</b>\n\n<b>Шаг 1/3.</b> Выберите тему обращения:",
	FeedbackWhere:        "📍 <b>Где именно?</b> Так мы сразу поймём, что смотреть — плагин и приложение устроены по-разному:",
	FeedbackPlatform:     "📱 <b>На чём?</b> Выберите устройство или способ просмотра:",
	CliLampa:             "🧩 Плагин для Lampa",
	CliApp:               "📺 Приложение ALPAC",
	CliMsx:               "🖥 MSX",
	CliOther:             "❓ Не знаю / другое",
	PlatAndroidTv:        "Android TV / приставка",
	PlatPhone:            "Телефон / планшет",
	PlatSamsung:          "Samsung (Tizen)",
	PlatLg:               "LG (webOS)",
	PlatHisense:          "Hisense (VIDAA)",
	PlatAppleTv:          "Apple TV",
	PlatBrowser:          "Браузер на компьютере",
	PlatOther:            "Другое",
	FeedbackStep2:        "✍️ <b>Шаг 2/3.</b> Введите тему обращения (кратко, одной строкой):",
	FeedbackStep3:        "📄 <b>Шаг 3/3.</b> Опишите подробно вашу проблему или предложение:\n\n<i>(или /cancel для отмены)</i>",
	FeedbackCreated:      "✅ <b>Обращение создано!</b>\n\n",
	FeedbackCancelled:    "❌ Обращение отменено.",
	FeedbackUnavailable:  "❌ Обратная связь временно недоступна.",
	FeedbackNoTickets:    "📋 У вас пока нет обращений.\n\nНажмите «📝 Обратная связь» для создания.",
	FeedbackTicketsTitle: "📋 <b>Мои обращения</b> (%d)\n\n",
	FeedbackReplyAsk:     "✍️ Введите ваш ответ:",
	FeedbackReplySent:    "✅ Ответ отправлен!",
	FeedbackReplyNotify:  "📨 <b>Ответ от поддержки</b>\n\n📌 %s\n\n🛡 <b>Администратор:</b>\n%s",
	FeedbackAdminNew:     "📨 <b>Новое обращение</b>\n\n👤 %s (ID: <code>%d</code>)\n📂 %s\n📌 <b>%s</b>\n\n💬 %s",
	FeedbackAdminReply:   "💬 <b>Ответ пользователя</b>\n\n👤 %s (ID: <code>%s</code>)\n📌 %s\n\n%s",
	FeedbackSubjectLen:   "⚠️ Тема должна быть от 1 до 200 символов. Попробуйте ещё раз:",
	FeedbackMsgLen:       "⚠️ Сообщение должно быть от 1 до 5000 символов. Попробуйте ещё раз:",
	FeedbackYourMsg:      "\n💬 <b>Ваше обращение:</b>\n%s\n",
	FeedbackThread:       "\n─────────────────\n📨 <b>Переписка</b> (%d)\n\n",
	FeedbackNoReplies:    "\n<i>Ответов пока нет. Мы скоро рассмотрим ваше обращение.</i>\n",
	FeedbackSupport:      "🛡 <b>Поддержка</b> · %s\n",
	FeedbackYou:          "👤 <b>%s</b> · %s\n",
	FeedbackViewTktBtn:   "👁 Открыть тикет",
	FeedbackListBtn:      "📋 К списку",
	FeedbackReplyBtn:     "✍️ Ответить",
	FeedbackTicketView:   "📋 Мои обращения",

	CatHelp:    "❓ Помощь",
	CatBug:     "🐛 Баг / Ошибка",
	CatFeature: "💡 Идея / Фича",
	CatThanks:  "🙏 Благодарность",
	CatOther:   "📎 Другое",
	CatCancel:  "❌ Отмена",

	StatusOpen:         "🟢 Открыт",
	StatusUserAnswered: "💬 Есть ответ",
	StatusUserWaiting:  "⏳ Ждёт ответа поддержки",
	StatusUserClosed:   "✅ Закрыто",
	StatusInProgress:   "🟡 В работе",
	StatusResolved:     "🟣 Решён",
	StatusClosed:       "⚪ Закрыт",

	KitUnavailable:  "❌ Настройки Kit недоступны. Обратитесь к администратору.",
	KitOpenBtn:      "⚙️ Открыть настройки",
	KitPrompt:       "⚙️ Нажмите кнопку ниже, чтобы открыть персональные настройки балансеров:",
	RoomOpenBtn:     "🎬 Войти в кинозал",
	RoomPrompt:      "🎬 Вас пригласили в совместный просмотр! Нажмите кнопку ниже, чтобы смотреть вместе синхронно:",
	RoomCodeLine:    "Код комнаты: <b>%s</b>\nНажмите кнопку выше, либо введите этот код вручную в приложении (экран «Кинозал»).",
	RoomUnavailable: "❌ Кинозал недоступен. Обратитесь к администратору.",
	RemoteOpenBtn:   "🎮 Открыть пульт",
	RemotePrompt:    "🎮 Управляйте Lampa прямо из Telegram — навигация, поиск, плеер:",
	Settings:        "Настройки",

	ErrorGeneric:     "❌ Ошибка: ",
	ErrorData:        "Ошибка данных",
	NoAccess:         "❌ Нет доступа",
	Cancelled:        "❌ Отменено.",
	EnterNewName:     "Введите новое имя",
	ReturnToLampa:    "\n\nВернитесь в приложение — вход выполнится сам.",
	ClickBelow:       "👇 Нажмите на тикет, чтобы посмотреть ответы и продолжить диалог:",
	FeedbackHubTitle: "📮 <b>Обратная связь</b>\n\nНапишите нам — или откройте свои обращения и посмотрите ответы.\n\nВаших обращений: <b>%d</b>%s",
	FeedbackHubEmpty: "📮 <b>Обратная связь</b>\n\nРасскажите, что не работает или чего не хватает. Ответим здесь же, в этом чате.",
	BtnFbNew:         "📝 Новое обращение",
	BtnFbMine:        "📋 Мои обращения",
	BtnFbBack:        "← Обратная связь",
}

// ── Ukrainian ──

var messagesUK = Messages{
	BtnProfile:      "👤 Профіль",
	BtnDevices:      "📱 Пристрої",
	BtnSettings:     "⚙️ Налаштування",
	BtnFeedback:     "📝 Зворотний зв'язок",
	BtnLanguage:     "🌐 Мова",
	BtnTickets:      "📋 Мої звернення",
	BtnRemote:       "🎮 Пульт",
	BtnSubscription: "💳 Підписка",

	StartWelcome:    "👋 Привіт, <b>%s</b>!\n\nТвій доступ активний до %s\nПристроїв: %d\n\nВикористовуй кнопки нижче для керування.",
	StartWelcomeNew: "👋 Привіт, <b>%s</b>!\n\nЩоб привʼязати пристрій, надішліть сюди 6-значний код — він показаний у застосунку на екрані входу.",

	LangSelectTitle: "🌐 <b>Оберіть мову:</b>",
	LangChanged:     "✅ Мову змінено: %s",

	ProfileTitle:    "━━━━━━━━━━━━━━━\n👤 <b>Профіль</b>\n━━━━━━━━━━━━━━━\n\n",
	ProfileStatus:   "%s%s\n\n",
	ProfileTelegram: "┌ 💬  <b>Telegram</b>\n│  @%s\n",
	ProfileTgID:     "│  ID: <code>%d</code>\n└\n\n",
	ProfileExpiry:   "┌ 📅  <b>Підписка</b>\n│  до <b>%s</b>\n",
	ProfileCreated:  "│  створено %s\n└\n\n",
	ProfileDevices:  "┌ 📱  <b>Пристрої:</b> %d",
	ProfileActive:   "Активний",
	ProfileExpired:  "Закінчився",
	ProfileDaysLeft: " · %d дн.",
	ProfileNoAccess: "━━━━━━━━━━━━━━━\n❌ <b>Немає доступу</b>\n━━━━━━━━━━━━━━━\n\nЩоб привʼязати пристрій, надішліть 6-значний код з екрана входу в застосунку.",

	ProfileBaseTitle:          "Стандарт",
	ProfilePremiumBadge:       "Преміум",
	ProfilePremiumTitle:       "Преміум-доступ",
	ProfileDevicesTitle:       "Пристрої",
	ProfileIntegrationsTitle:  "Інтеграції",
	ProfileUntilWord:          "до",
	ProfileSinceWord:          "з",
	ProfileRemainingPrefix:    "ще",
	ProfileExpiredSince:       "закінчився",
	ProfileCalendarSeriesWord: "серіалів",
	ProfileCalendarNotifyOn:   "сповіщення увімк.",
	ProfileCalendarNotifyOff:  "сповіщення вимк.",
	ProfileAliceLabel:         "Аліса",
	ProfileGroupFallback:      "Група",
	BtnCubUnlink:              "☁️ Відв'язати CUB",
	CubUnlinkedToast:          "CUB-акаунт відв'язано",
	ProfileDayForms:           [3]string{"день", "дні", "днів"},

	DevicesTitle:        "📱 <b>Пристрої</b> (%d)\n\n",
	DevicesNone:         "📱 Немає прив'язаних пристроїв.\n\nПристрій привʼязується, коли ви надсилаєте сюди код з екрана входу в застосунку.",
	DeviceUnbound:       "✅ Пристрій від'єднано.",
	DeviceRenamed:       "✅ Пристрій перейменовано: <b>%s</b>",
	DeviceNotFound:      "❌ Пристрій не знайдено.",
	DeviceBoundAuto:     "📱 Пристрій прив'язано автоматично\n\n<b>%s</b>\nUID: <code>%s</code>\n\nДля керування: /devices",
	DeviceMigrated:      "🔄 Пристрій перепідключено\n\n<b>%s</b>\nUID: <code>%s</code> → <code>%s</code>\n\nРозпізнано за відбитком пристрою.",
	DeviceVerifyCode:    "📱 Новий пристрій: <b>%s</b>\n\nДля прив'язки надішліть код:\n<code>%s</code>",
	DeviceLimitFmt:      "❌ Ліміт пристроїв (%d). Від'єднайте старий: /devices",
	DeviceRenameAsk:     "✏️ Введіть нову назву для пристрою <b>%s</b> (<code>%s</code>):",
	DeviceUnbindBtn:     "🗑 Від'єднати",
	DeviceUnbindAllBtn:  "🧹 Від'єднати всі",
	DeviceUnbindAllAsk:  "🧹 Від'єднати <b>всі пристрої</b> (%d)?\n\nКожен доведеться прив'язувати заново: код з екрана входу → сюди, в бот.",
	DeviceUnbindAllYes:  "🧹 Так, від'єднати всі",
	DeviceUnbindAllDone: "✅ Від'єднано пристроїв: %d.",
	DeviceUsage:         "Використання: /unbind <code>UID</code>",

	CodeAccepted:     "✅ Код прийнято, пристрій привʼязано.\n\nПоверніться до застосунку — вхід виконається сам.",
	CodeAutoApproved: "✅ Код прийнято, пристрій привʼязано на %s.\n\nПоверніться до застосунку — вхід виконається сам.",
	CodeWaitAdmin:    "✅ Код прийнято! Очікуйте підтвердження адміністратора.",
	CodeWrongFormat:  "❌ Це не схоже на код. Потрібні 6 цифр з екрана входу в застосунку.",
	CodeNotFound:     "❌ Код не знайдено або вже використано.",
	CodeOtherAccount: "❌ Цей код прив'язано до іншого акаунту.",
	CodeErrorApprove: "❌ Помилка авто-схвалення.",
	CodeBindError:    "❌ Помилка прив'язки пристрою.",
	CodeDeviceBound:  "✅ Пристрій <b>%s</b> прив'язано!",

	AdminOnly:         "⛔ Тільки адміністратор.",
	AdminPanelNone:    "❌ Адмін-панель не налаштована.",
	AdminPanelLink:    "🔧 Адмін-панель:\n%s",
	AdminAutoInfo:     "ℹ️ Авто-схвалення: %s — %s\nIP: %s",
	AdminRequest:      "🔐 Запит на доступ\n\nКористувач: %s\nTelegram ID: %d\nIP: %s\nКод: %s",
	AdminRejected:     "❌ Відхилено: %s",
	AdminApproved:     "✅ Схвалено: %s — %s",
	AdminTokenCreated: "📺 Device-токен створено (%s):\n\n<code>%s</code>\n\nДля Lampa:\n<code>/on/js/%s</code>",

	Approve1d:     "1 день",
	Approve7d:     "7 днів",
	Approve30d:    "30 днів",
	Approve90d:    "90 днів",
	Approve365d:   "365 днів",
	ApproveReject: "❌ Відхилити",

	AccessApproved: "✅ Доступ відкрито на %s!\n\nПоверніться до застосунку — вхід виконається сам.",
	AccessRejected: "❌ Адміністратор відхилив ваш запит на доступ.",

	FeedbackTitle:        "📝 <b>Зворотний зв'язок</b>\n\n<b>Крок 1/3.</b> Оберіть тему звернення:",
	FeedbackWhere:        "📍 <b>Де саме?</b> Так ми одразу зрозуміємо, що дивитися — плагін і застосунок влаштовані по-різному:",
	FeedbackPlatform:     "📱 <b>На чому?</b> Оберіть пристрій або спосіб перегляду:",
	CliLampa:             "🧩 Плагін для Lampa",
	CliApp:               "📺 Застосунок ALPAC",
	CliMsx:               "🖥 MSX",
	CliOther:             "❓ Не знаю / інше",
	PlatAndroidTv:        "Android TV / приставка",
	PlatPhone:            "Телефон / планшет",
	PlatSamsung:          "Samsung (Tizen)",
	PlatLg:               "LG (webOS)",
	PlatHisense:          "Hisense (VIDAA)",
	PlatAppleTv:          "Apple TV",
	PlatBrowser:          "Браузер на компʼютері",
	PlatOther:            "Інше",
	FeedbackStep2:        "✍️ <b>Крок 2/3.</b> Введіть тему звернення (коротко, одним рядком):",
	FeedbackStep3:        "📄 <b>Крок 3/3.</b> Опишіть детально вашу проблему або пропозицію:\n\n<i>(або /cancel для скасування)</i>",
	FeedbackCreated:      "✅ <b>Звернення створено!</b>\n\n",
	FeedbackCancelled:    "❌ Звернення скасовано.",
	FeedbackUnavailable:  "❌ Зворотний зв'язок тимчасово недоступний.",
	FeedbackNoTickets:    "📋 У вас поки немає звернень.\n\nНатисніть «📝 Зворотний зв'язок» для створення.",
	FeedbackTicketsTitle: "📋 <b>Мої звернення</b> (%d)\n\n",
	FeedbackReplyAsk:     "✍️ Введіть вашу відповідь:",
	FeedbackReplySent:    "✅ Відповідь надіслано!",
	FeedbackReplyNotify:  "📨 <b>Відповідь від підтримки</b>\n\n📌 %s\n\n🛡 <b>Адміністратор:</b>\n%s",
	FeedbackAdminNew:     "📨 <b>Нове звернення</b>\n\n👤 %s (ID: <code>%d</code>)\n📂 %s\n📌 <b>%s</b>\n\n💬 %s",
	FeedbackAdminReply:   "💬 <b>Відповідь користувача</b>\n\n👤 %s (ID: <code>%s</code>)\n📌 %s\n\n%s",
	FeedbackSubjectLen:   "⚠️ Тема має бути від 1 до 200 символів. Спробуйте ще раз:",
	FeedbackMsgLen:       "⚠️ Повідомлення має бути від 1 до 5000 символів. Спробуйте ще раз:",
	FeedbackYourMsg:      "\n💬 <b>Ваше звернення:</b>\n%s\n",
	FeedbackThread:       "\n─────────────────\n📨 <b>Листування</b> (%d)\n\n",
	FeedbackNoReplies:    "\n<i>Відповідей поки немає. Ми скоро розглянемо ваше звернення.</i>\n",
	FeedbackSupport:      "🛡 <b>Підтримка</b> · %s\n",
	FeedbackYou:          "👤 <b>%s</b> · %s\n",
	FeedbackViewTktBtn:   "👁 Відкрити тікет",
	FeedbackListBtn:      "📋 До списку",
	FeedbackReplyBtn:     "✍️ Відповісти",
	FeedbackTicketView:   "📋 Мої звернення",

	CatHelp:    "❓ Допомога",
	CatBug:     "🐛 Баг / Помилка",
	CatFeature: "💡 Ідея / Фіча",
	CatThanks:  "🙏 Подяка",
	CatOther:   "📎 Інше",
	CatCancel:  "❌ Скасувати",

	StatusOpen:         "🟢 Відкритий",
	StatusUserAnswered: "💬 Є відповідь",
	StatusUserWaiting:  "⏳ Чекає на відповідь підтримки",
	StatusUserClosed:   "✅ Закрито",
	StatusInProgress:   "🟡 В роботі",
	StatusResolved:     "🟣 Вирішено",
	StatusClosed:       "⚪ Закрито",

	KitUnavailable:  "❌ Налаштування Kit недоступні. Зверніться до адміністратора.",
	KitOpenBtn:      "⚙️ Відкрити налаштування",
	KitPrompt:       "⚙️ Натисніть кнопку нижче, щоб відкрити персональні налаштування балансерів:",
	RoomOpenBtn:     "🎬 Увійти в кінозал",
	RoomPrompt:      "🎬 Вас запросили до спільного перегляду! Натисніть кнопку нижче, щоб дивитися разом синхронно:",
	RoomCodeLine:    "Код кімнати: <b>%s</b>\nНатисніть кнопку вище, або введіть цей код вручну в застосунку (екран «Кінозал»).",
	RoomUnavailable: "❌ Кінозал недоступний. Зверніться до адміністратора.",
	RemoteOpenBtn:   "🎮 Відкрити пульт",
	RemotePrompt:    "🎮 Керуйте Lampa прямо з Telegram — навігація, пошук, плеєр:",
	Settings:        "Налаштування",

	ErrorGeneric:     "❌ Помилка: ",
	ErrorData:        "Помилка даних",
	NoAccess:         "❌ Немає доступу",
	Cancelled:        "❌ Скасовано.",
	EnterNewName:     "Введіть нову назву",
	ReturnToLampa:    "\n\nПоверніться до застосунку — вхід виконається сам.",
	ClickBelow:       "👇 Натисніть на тікет, щоб переглянути відповіді та продовжити діалог:",
	FeedbackHubTitle: "📮 <b>Зворотний зв’язок</b>\n\nНапишіть нам — або відкрийте свої звернення та подивіться відповіді.\n\nВаших звернень: <b>%d</b>%s",
	FeedbackHubEmpty: "📮 <b>Зворотний зв’язок</b>\n\nРозкажіть, що не працює або чого бракує. Відповімо тут само, у цьому чаті.",
	BtnFbNew:         "📝 Нове звернення",
	BtnFbMine:        "📋 Мої звернення",
	BtnFbBack:        "← Зворотний зв’язок",
}

// ── English ──

var messagesEN = Messages{
	BtnProfile:      "👤 Profile",
	BtnDevices:      "📱 Devices",
	BtnSettings:     "⚙️ Settings",
	BtnFeedback:     "📝 Feedback",
	BtnLanguage:     "🌐 Language",
	BtnTickets:      "📋 My tickets",
	BtnRemote:       "🎮 Remote",
	BtnSubscription: "💳 Subscription",

	StartWelcome:    "👋 Hi, <b>%s</b>!\n\nYour access is active until %s\nDevices: %d\n\nUse the buttons below to manage your account.",
	StartWelcomeNew: "👋 Hi, <b>%s</b>!\n\nTo link this device, send me the 6-digit code shown on the app's sign-in screen.",

	LangSelectTitle: "🌐 <b>Choose language:</b>",
	LangChanged:     "✅ Language changed: %s",

	ProfileTitle:    "━━━━━━━━━━━━━━━\n👤 <b>Profile</b>\n━━━━━━━━━━━━━━━\n\n",
	ProfileStatus:   "%s%s\n\n",
	ProfileTelegram: "┌ 💬  <b>Telegram</b>\n│  @%s\n",
	ProfileTgID:     "│  ID: <code>%d</code>\n└\n\n",
	ProfileExpiry:   "┌ 📅  <b>Subscription</b>\n│  until <b>%s</b>\n",
	ProfileCreated:  "│  created %s\n└\n\n",
	ProfileDevices:  "┌ 📱  <b>Devices:</b> %d",
	ProfileActive:   "Active",
	ProfileExpired:  "Expired",
	ProfileDaysLeft: " · %d d.",
	ProfileNoAccess: "━━━━━━━━━━━━━━━\n❌ <b>No access</b>\n━━━━━━━━━━━━━━━\n\nTo link this device, send the 6-digit code from the app's sign-in screen.",

	ProfileBaseTitle:          "Standard",
	ProfilePremiumBadge:       "Premium",
	ProfilePremiumTitle:       "Premium access",
	ProfileDevicesTitle:       "Devices",
	ProfileIntegrationsTitle:  "Integrations",
	ProfileUntilWord:          "until",
	ProfileSinceWord:          "since",
	ProfileRemainingPrefix:    "still",
	ProfileExpiredSince:       "expired",
	ProfileCalendarSeriesWord: "shows",
	ProfileCalendarNotifyOn:   "notifications on",
	ProfileCalendarNotifyOff:  "notifications off",
	ProfileAliceLabel:         "Alice",
	ProfileGroupFallback:      "Group",
	BtnCubUnlink:              "☁️ Unlink CUB",
	CubUnlinkedToast:          "CUB account unlinked",
	ProfileDayForms:           [3]string{"day", "days", "days"},

	DevicesTitle:        "📱 <b>Devices</b> (%d)\n\n",
	DevicesNone:         "📱 No linked devices.\n\nA device gets linked when you send me the code from its sign-in screen.",
	DeviceUnbound:       "✅ Device unlinked.",
	DeviceRenamed:       "✅ Device renamed: <b>%s</b>",
	DeviceNotFound:      "❌ Device not found.",
	DeviceBoundAuto:     "📱 Device linked automatically\n\n<b>%s</b>\nUID: <code>%s</code>\n\nManage devices: /devices",
	DeviceMigrated:      "🔄 Device reconnected\n\n<b>%s</b>\nUID: <code>%s</code> → <code>%s</code>\n\nIdentified by device fingerprint.",
	DeviceVerifyCode:    "📱 New device: <b>%s</b>\n\nTo link it, send the code:\n<code>%s</code>",
	DeviceLimitFmt:      "❌ Device limit (%d). Unlink an old one: /devices",
	DeviceRenameAsk:     "✏️ Enter a new name for device <b>%s</b> (<code>%s</code>):",
	DeviceUnbindBtn:     "🗑 Unlink",
	DeviceUnbindAllBtn:  "🧹 Unlink all",
	DeviceUnbindAllAsk:  "🧹 Unlink <b>every device</b> (%d)?\n\nEach one has to be paired again: the code from its login screen, sent here.",
	DeviceUnbindAllYes:  "🧹 Yes, unlink all",
	DeviceUnbindAllDone: "✅ Devices unlinked: %d.",
	DeviceUsage:         "Usage: /unbind <code>UID</code>",

	CodeAccepted:     "✅ Code accepted, device linked.\n\nGo back to the app — it will sign in on its own.",
	CodeAutoApproved: "✅ Code accepted, device linked for %s.\n\nGo back to the app — it will sign in on its own.",
	CodeWaitAdmin:    "✅ Code accepted. Nothing else to do — the admin still has to approve it, and I will tell you here.",
	CodeWrongFormat:  "❌ That does not look like a code. I need the 6 digits from the app's sign-in screen.",
	CodeNotFound:     "❌ Code not found or already used.",
	CodeOtherAccount: "❌ This code is linked to another account.",
	CodeErrorApprove: "❌ Auto-approval error.",
	CodeBindError:    "❌ Device binding error.",
	CodeDeviceBound:  "✅ Device <b>%s</b> linked!",

	AdminOnly:         "⛔ Admin only.",
	AdminPanelNone:    "❌ Admin panel is not configured.",
	AdminPanelLink:    "🔧 Admin panel:\n%s",
	AdminAutoInfo:     "ℹ️ Auto-approved: %s — %s\nIP: %s",
	AdminRequest:      "🔐 Access request\n\nUser: %s\nTelegram ID: %d\nIP: %s\nCode: %s",
	AdminRejected:     "❌ Rejected: %s",
	AdminApproved:     "✅ Approved: %s — %s",
	AdminTokenCreated: "📺 Device token created (%s):\n\n<code>%s</code>\n\nFor Lampa:\n<code>/on/js/%s</code>",

	Approve1d:     "1 day",
	Approve7d:     "7 days",
	Approve30d:    "30 days",
	Approve90d:    "90 days",
	Approve365d:   "365 days",
	ApproveReject: "❌ Reject",

	AccessApproved: "✅ Access granted for %s!\n\nGo back to the app — it will sign in on its own.",
	AccessRejected: "❌ The administrator rejected your access request.",

	FeedbackTitle:        "📝 <b>Feedback</b>\n\n<b>Step 1/3.</b> Choose a topic:",
	FeedbackWhere:        "📍 <b>Where exactly?</b> The plugin and the app are built differently, so this tells us where to look:",
	FeedbackPlatform:     "📱 <b>On what?</b> Pick the device or the way you watch:",
	CliLampa:             "🧩 Lampa plugin",
	CliApp:               "📺 ALPAC app",
	CliMsx:               "🖥 MSX",
	CliOther:             "❓ Not sure / other",
	PlatAndroidTv:        "Android TV / box",
	PlatPhone:            "Phone / tablet",
	PlatSamsung:          "Samsung (Tizen)",
	PlatLg:               "LG (webOS)",
	PlatHisense:          "Hisense (VIDAA)",
	PlatAppleTv:          "Apple TV",
	PlatBrowser:          "Desktop browser",
	PlatOther:            "Other",
	FeedbackStep2:        "✍️ <b>Step 2/3.</b> Enter the subject (briefly, one line):",
	FeedbackStep3:        "📄 <b>Step 3/3.</b> Describe your issue or suggestion in detail:\n\n<i>(or /cancel to abort)</i>",
	FeedbackCreated:      "✅ <b>Ticket created!</b>\n\n",
	FeedbackCancelled:    "❌ Ticket cancelled.",
	FeedbackUnavailable:  "❌ Feedback is temporarily unavailable.",
	FeedbackNoTickets:    "📋 You have no tickets yet.\n\nPress «📝 Feedback» to create one.",
	FeedbackTicketsTitle: "📋 <b>My tickets</b> (%d)\n\n",
	FeedbackReplyAsk:     "✍️ Enter your reply:",
	FeedbackReplySent:    "✅ Reply sent!",
	FeedbackReplyNotify:  "📨 <b>Reply from support</b>\n\n📌 %s\n\n🛡 <b>Administrator:</b>\n%s",
	FeedbackAdminNew:     "📨 <b>New ticket</b>\n\n👤 %s (ID: <code>%d</code>)\n📂 %s\n📌 <b>%s</b>\n\n💬 %s",
	FeedbackAdminReply:   "💬 <b>User reply</b>\n\n👤 %s (ID: <code>%s</code>)\n📌 %s\n\n%s",
	FeedbackSubjectLen:   "⚠️ Subject must be 1–200 characters. Try again:",
	FeedbackMsgLen:       "⚠️ Message must be 1–5000 characters. Try again:",
	FeedbackYourMsg:      "\n💬 <b>Your message:</b>\n%s\n",
	FeedbackThread:       "\n─────────────────\n📨 <b>Thread</b> (%d)\n\n",
	FeedbackNoReplies:    "\n<i>No replies yet. We'll review your ticket soon.</i>\n",
	FeedbackSupport:      "🛡 <b>Support</b> · %s\n",
	FeedbackYou:          "👤 <b>%s</b> · %s\n",
	FeedbackViewTktBtn:   "👁 View ticket",
	FeedbackListBtn:      "📋 Back to list",
	FeedbackReplyBtn:     "✍️ Reply",
	FeedbackTicketView:   "📋 My tickets",

	CatHelp:    "❓ Help",
	CatBug:     "🐛 Bug / Error",
	CatFeature: "💡 Idea / Feature",
	CatThanks:  "🙏 Thank you",
	CatOther:   "📎 Other",
	CatCancel:  "❌ Cancel",

	StatusOpen:         "🟢 Open",
	StatusUserAnswered: "💬 Answered",
	StatusUserWaiting:  "⏳ Waiting for support",
	StatusUserClosed:   "✅ Closed",
	StatusInProgress:   "🟡 In progress",
	StatusResolved:     "🟣 Resolved",
	StatusClosed:       "⚪ Closed",

	KitUnavailable:  "❌ Kit settings are not available. Contact the administrator.",
	KitOpenBtn:      "⚙️ Open settings",
	KitPrompt:       "⚙️ Press the button below to open your personal balancer settings:",
	RoomOpenBtn:     "🎬 Join the cinema",
	RoomPrompt:      "🎬 You've been invited to a watch party! Tap below to watch together in sync:",
	RoomCodeLine:    "Room code: <b>%s</b>\nTap the button above, or enter this code manually in the app («Кинозал» screen).",
	RoomUnavailable: "❌ Watch party is unavailable. Contact the administrator.",
	RemoteOpenBtn:   "🎮 Open remote",
	RemotePrompt:    "🎮 Control Lampa from Telegram — navigation, search, player:",
	Settings:        "Settings",

	ErrorGeneric:     "❌ Error: ",
	ErrorData:        "Data error",
	NoAccess:         "❌ No access",
	Cancelled:        "❌ Cancelled.",
	EnterNewName:     "Enter new name",
	ReturnToLampa:    "\n\nGo back to the app — it will sign in on its own.",
	ClickBelow:       "👇 Click a ticket to view replies and continue the conversation:",
	FeedbackHubTitle: "📮 <b>Support</b>\n\nWrite to us — or open your tickets and read the replies.\n\nYour tickets: <b>%d</b>%s",
	FeedbackHubEmpty: "📮 <b>Support</b>\n\nTell us what is broken or missing. We answer right here, in this chat.",
	BtnFbNew:         "📝 New ticket",
	BtnFbMine:        "📋 My tickets",
	BtnFbBack:        "← Support",
}

// ── Helper functions ──

// FbCatLabel returns the localized feedback category label.
func FbCatLabel(lang Lang, cat string) string {
	t := T(lang)
	switch cat {
	case "help":
		return t.CatHelp
	case "bug":
		return t.CatBug
	case "feature":
		return t.CatFeature
	case "thanks":
		return t.CatThanks
	case "other":
		return t.CatOther
	default:
		return cat
	}
}

// FbStatLabel returns the localized feedback status label.
func FbStatLabel(lang Lang, status string) string {
	t := T(lang)
	switch status {
	case "open":
		return t.StatusOpen
	case "in_progress":
		return t.StatusInProgress
	case "resolved":
		return t.StatusResolved
	case "closed":
		return t.StatusClosed
	default:
		return status
	}
}

// FbUserStatus is what the TICKET AUTHOR is told. The admin statuses answer
// "where is this in our queue"; the author is asking one question — did anybody
// answer me — and "🟡 В работе" does not answer it.
func FbUserStatus(lang Lang, status string, lastReplyAdmin bool) string {
	t := T(lang)
	switch status {
	case "resolved", "closed":
		return t.StatusUserClosed
	}
	if lastReplyAdmin {
		return t.StatusUserAnswered
	}
	return t.StatusUserWaiting
}

// FbUserStatusRank orders the list the way the author reads it: what needs
// reading first, then what is still waiting, then what is over.
func FbUserStatusRank(status string, lastReplyAdmin bool) int {
	switch status {
	case "resolved", "closed":
		return 2
	}
	if lastReplyAdmin {
		return 0
	}
	return 1
}

// DurationLabelLang returns a human-readable duration label for the given language.
func DurationLabelLang(lang Lang, days int) string {
	switch lang {
	case LangUK:
		return durationLabelUK(days)
	case LangEN:
		return durationLabelEN(days)
	default:
		return DurationLabel(days)
	}
}

func durationLabelUK(days int) string {
	switch {
	case days == 1:
		return "1 день"
	case days >= 2 && days <= 4:
		return fmt.Sprintf("%d дні", days)
	case days == 30:
		return "1 місяць"
	case days == 90:
		return "3 місяці"
	case days == 365:
		return "1 рік"
	default:
		return fmt.Sprintf("%d днів", days)
	}
}

func durationLabelEN(days int) string {
	switch {
	case days == 1:
		return "1 day"
	case days == 30:
		return "1 month"
	case days == 90:
		return "3 months"
	case days == 365:
		return "1 year"
	default:
		return fmt.Sprintf("%d days", days)
	}
}

// LangFlag returns the emoji flag for the language.
func LangFlag(lang Lang) string {
	switch lang {
	case LangUK:
		return "🇺🇦"
	case LangEN:
		return "🇬🇧"
	default:
		return "🇷🇺"
	}
}

// LangName returns a human-readable name for the language.
func LangName(lang Lang) string {
	switch lang {
	case LangUK:
		return "Українська"
	case LangEN:
		return "English"
	default:
		return "Русский"
	}
}

// LangFlagName returns "🇷🇺 Русский" etc.
func LangFlagName(lang Lang) string {
	return LangFlag(lang) + " " + LangName(lang)
}
