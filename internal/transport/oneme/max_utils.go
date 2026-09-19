package oneme

import (
	"crypto/rand"
	"fmt"

	"openflux/internal/utils"
)

// All MAX logging goes through utils.Debugf, so it is silent unless --debug
// (or OpenFluxSetDebug) is on. The transport used to print unconditionally to
// stdout, which leaked contact phone numbers, call endpoints with auth tokens,
// and full SDP into server logs.

func logDebug(format string, args ...interface{}) { utils.Debugf("[MAX/dbg] "+format, args...) }
func logInfo(format string, args ...interface{})  { utils.Debugf("[MAX] "+format, args...) }
func logError(format string, args ...interface{}) { utils.Debugf("[MAX/err] "+format, args...) }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func genUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// maskURL hides the query string (which carries auth tokens) of a call
// endpoint before it is logged.
func maskURL(u string) string {
	if i := indexByte(u, '?'); i >= 0 {
		return u[:i] + "?<redacted>"
	}
	return u
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
