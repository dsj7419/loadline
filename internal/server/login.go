package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

const (
	loginCodeBytes = 16
	loginCodeTTL   = 60 * time.Second
	// maxLoginCodes bounds the codes outstanding; minting another forgets
	// the oldest.
	maxLoginCodes = 16
	// uiCookieMaxAge keeps the dashboard signed in for 30 days.
	uiCookieMaxAge = 30 * 24 * 60 * 60
)

// loginRefusedPage is what a browser shows for a code that is unknown,
// expired or already used.
const loginRefusedPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Loadline</title></head>
<body><p>This sign-in link has expired or was already used. Run <code>loadline open</code> to open the dashboard.</p></body>
</html>
`

// loginCodes are the codes minted and not yet redeemed, oldest first.
type loginCodes struct {
	mu    sync.Mutex
	codes []loginCode
}

type loginCode struct {
	code     string
	mintedAt time.Time
}

func (c *loginCodes) mint(now time.Time) string {
	raw := make([]byte, loginCodeBytes)
	_, _ = rand.Read(raw) // never fails: crypto/rand stops the program instead
	code := hex.EncodeToString(raw)

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.codes) == maxLoginCodes {
		c.codes = slices.Delete(c.codes, 0, 1)
	}
	c.codes = append(c.codes, loginCode{code: code, mintedAt: now})
	return code
}

// redeem reports whether code was minted less than loginCodeTTL before now.
// A code is spent on its first redemption, in time or not.
func (c *loginCodes) redeem(code string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	found := -1
	for i, minted := range c.codes {
		if subtle.ConstantTimeCompare([]byte(minted.code), []byte(code)) == 1 {
			found = i
		}
	}
	if found < 0 {
		return false
	}
	mintedAt := c.codes[found].mintedAt
	c.codes = slices.Delete(c.codes, found, found+1)
	return !now.Before(mintedAt) && now.Before(mintedAt.Add(loginCodeTTL))
}

// MintLoginCode returns a code that signs a browser in to the dashboard once,
// within a minute, through /login?code=.
func (s *Server) MintLoginCode() string { return s.codes.mint(s.clock()) }

func (s *Server) handleLoginCode(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, struct {
		Code string `json:"code"`
	}{Code: s.MintLoginCode()})
}

// handleLogin trades a login code for the UI cookie. The code travels in the
// URL the browser opens, so it lives for a minute and works once; the cookie
// it buys never appears in a URL or in history.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// The route for GET also takes HEAD, which no browser sends to follow a
	// link, but a link checker or a preview might; it must not spend a code.
	if r.Method == http.MethodHead {
		w.Header().Set("Allow", http.MethodGet)
		s.reject(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.codes.redeem(r.URL.Query().Get("code"), s.clock()) {
		s.log.Debug("login refused")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, loginRefusedPage) // a client that hung up has nothing to be told
		return
	}
	// No Secure attribute: the dashboard is served over plain HTTP on the
	// loopback interface, where a browser need not send a Secure cookie.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: not Secure, for the reason above
		Name:     uiCookieName,
		Value:    string(s.uiCookie),
		Path:     "/",
		MaxAge:   uiCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
