package tgauth

import (
	"time"
	_ "time/tzdata" // embed the zone database: containers routinely ship without /usr/share/zoneinfo
)

// Dates the bot shows the user.
//
// Everything is stored in UTC, and every message used to print it that way. For
// an audience in Moscow that is UTC+3, so a subscription bought at 02:00 local
// time expires at 23:00 UTC the previous calendar day — and the bot answered
// "до 29.09" for what the user experiences as the 30th. That is the "даты
// подписок не сходятся" in the feedback: not a wrong deadline, a wrong DAY
// printed for a correct deadline.
//
// One display zone for the whole bot, and one place to change it.

// displayTZ is the zone every user-facing date is rendered in. Not the server's
// local zone: that drifts with the host and would silently change what people
// read.
const displayTZ = "Europe/Moscow"

var displayLoc = func() *time.Location {
	loc, err := time.LoadLocation(displayTZ)
	if err != nil {
		return time.UTC // no zone data: UTC is wrong by hours, but never wrong by a random amount
	}
	return loc
}()

// fmtDate renders a date the way the bot shows deadlines: 02.01.2006, local to
// the user's expected zone.
func fmtDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(displayLoc).Format("02.01.2006")
}

// fmtDateShort drops the year — for badges where the year is noise.
func fmtDateShort(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(displayLoc).Format("02.01")
}

// fmtDateTime is for activity timestamps (last seen), where the time of day is
// the informative part.
func fmtDateTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(displayLoc).Format("02.01 15:04")
}

// FormatDate is fmtDate for callers outside this package (the payment webhook
// sends its own "оплата получена" message and must print the same date the
// profile card does — the two disagreeing is precisely what got reported).
func FormatDate(t time.Time) string { return fmtDate(t) }

// PluralDaysRU is the Russian day-word for n, exported for the same reason.
func PluralDaysRU(n int) string { return pluralDays(n, messagesRU.ProfileDayForms) }

// fmtISODate / fmtISODateTime render an RFC3339 timestamp (how feedback tickets
// store their dates) in the same zone and shape as everything else.
//
// Before this, one bot printed three formats: «07.08.2026» on the profile card,
// «2026-08-07» in the ticket list, «2026-08-07T14:30» inside a ticket — all in
// UTC. Same data, three answers.
func fmtISODate(s string) string {
	if t, ok := parseISO(s); ok {
		return fmtDate(t)
	}
	return s
}

func fmtISODateTime(s string) string {
	if t, ok := parseISO(s); ok {
		return fmtDateTime(t)
	}
	return s
}

func parseISO(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
