package agent

import "strings"

// Only known observation counters/countdowns are omitted. Configuration such as
// ageing_time, hello_time, max_age and multicast interval/count settings remains
// part of the fingerprint, as do resource identity, membership and route paths.
// The original observations are still returned to callers.
var volatileQueryFields = map[string]bool{
	"hellotimer": true, "tcntimer": true, "topologychangetimer": true, "gctimer": true,
	"forwarddelaytimer": true, "messageagetimer": true, "holdtimer": true,
	"fdbnlearned": true, "mcastngroups": true, "mcaststats": true, "stats": true, "stats64": true,
	"peeruptime": true, "peeruptimemsec": true, "uptime": true, "uptimeinmsec": true, "deadtime": true,
	"routerdeadintervaltimerduemsec": true, "timerhelloinmsecs": true, "expires": true,
	"bgptimerupmsec": true, "bgptimerupstring": true, "bgptimerlastread": true, "bgptimerlastwrite": true,
	"bgpinupdateelapsedtimemsecs": true, "lastresettimermsecs": true, "nextstarttimerdueinmsecs": true,
	"msgsent": true, "msgrcvd": true, "messagestats": true, "packetqueuelength": true,
	"spflastexecutedmsecs": true, "spflastexecutedsecs": true,
	"spflastexecutedmicrosecs": true,
	"pendinglsalsupdatetime":   true, "pendinglsalsacktime": true,
	"helloduemsec": true, "deadtimermsecs": true,
}

func stableQueryValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		stable := make(map[string]any, len(v))
		for key, item := range v {
			normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			if volatileQueryFields[normalized] {
				continue
			}
			if normalized == "running" {
				if _, ok := item.(string); ok {
					continue
				}
			}
			stable[key] = stableQueryValue(item)
		}
		return stable
	case []any:
		stable := make([]any, len(v))
		for i, item := range v {
			stable[i] = stableQueryValue(item)
		}
		return stable
	case []map[string]any:
		stable := make([]any, len(v))
		for i, item := range v {
			stable[i] = stableQueryValue(item)
		}
		return stable
	default:
		return value
	}
}
