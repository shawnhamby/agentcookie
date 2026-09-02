package livecdp

import "strings"

// ClearanceCookieNames are bot-clearance cookies bound to the browser
// fingerprint that earned them. Syncing them from another browser (e.g. daily
// Chrome into an agent-owned profile) overwrites freshly earned tokens and
// breaks the owned profile's clearance state.
var ClearanceCookieNames = []string{
	"datadome",
	"cf_clearance",
	"__cf_bm",
	"_px2",
	"_px3",
	"_pxvid",
	"_pxhd",
	"ak_bmsc",
	"bm_sz",
	"bm_sv",
	"_abck",
	"bm_mi",
}

// IsClearanceCookie reports whether name is a bot-clearance cookie that
// agent-sync must never inject from an external source.
func IsClearanceCookie(name string) bool {
	lower := strings.ToLower(name)
	for _, n := range ClearanceCookieNames {
		if lower == n {
			return true
		}
	}
	return false
}
