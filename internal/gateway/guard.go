package gateway

import (
	"crypto/subtle"
	"net/http"
	"strconv"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

// Guard rejects requests that are not from a local agent: a foreign Host
// (DNS rebinding), any Origin (browsers always send one cross-site) or a
// wrong token (an empty token rejects all rather than accepting a bare
// "Bearer "). Rejections carry no body so nothing internal leaks.
func Guard(port int, token string, next http.Handler) http.Handler {
	hosts := map[string]bool{config.LoopbackAddr(port): true, "localhost:" + strconv.Itoa(port): true}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !hosts[r.Host], len(r.Header.Values("Origin")) > 0:
			w.WriteHeader(http.StatusForbidden)
		case token == "", subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1:
			w.WriteHeader(http.StatusUnauthorized)
		default:
			next.ServeHTTP(w, r)
		}
	})
}
