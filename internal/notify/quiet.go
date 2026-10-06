package notify

import "time"

// ParseClock reads a time of day written "HH:MM" (24 hours) as minutes
// after midnight. It is the stored form of a profile's quiet hours.
func ParseClock(s string) (minutes int, ok bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	h, m := int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// InQuietHours reports whether t, read as local time in loc (UTC when nil),
// lies in the daily window from start to end, both "HH:MM": start is in,
// end is out. A window whose end is not after its start runs across
// midnight (23:00 to 07:00). Only the clock is compared, so a window keeps
// its local times through a change of daylight saving. Equal or unreadable
// bounds are no window at all: nothing is quiet.
func InQuietHours(start, end string, t time.Time, loc *time.Location) bool {
	from, ok1 := ParseClock(start)
	to, ok2 := ParseClock(end)
	if !ok1 || !ok2 || from == to {
		return false
	}
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	now := local.Hour()*60 + local.Minute()
	if from < to {
		return from <= now && now < to
	}
	return now >= from || now < to
}
