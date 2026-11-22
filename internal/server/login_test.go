package server_test

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// login redeems code.
func login(h *harness, code string) *httptest.ResponseRecorder {
	return h.do(request(http.MethodGet, "/login?code="+code, nil))
}

func assertLoggedIn(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assertStatus(t, rec, http.StatusSeeOther)
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

func assertRefused(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assertStatus(t, rec, http.StatusForbidden)
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("Set-Cookie = %q on a refused login, want none", got)
	}
}

func TestLogin_CodeSingleUseAndExpires(t *testing.T) {
	h := newHarness(t)

	used := h.srv.MintLoginCode()
	h.clock.Advance(59 * time.Second)
	assertLoggedIn(t, login(h, used))
	assertRefused(t, login(h, used))

	expired := h.srv.MintLoginCode()
	h.clock.Advance(61 * time.Second)
	assertRefused(t, login(h, expired))

	atTheLimit := h.srv.MintLoginCode()
	h.clock.Advance(60 * time.Second)
	assertRefused(t, login(h, atTheLimit))

	// A clock that steps back must not stretch a code's minute.
	beforeItsTime := h.srv.MintLoginCode()
	h.clock.Advance(-time.Second)
	assertRefused(t, login(h, beforeItsTime))
}

func TestLogin_HeadIsRefusedAndSpendsNoCode(t *testing.T) {
	h := newHarness(t)
	code := h.srv.MintLoginCode()

	rec := h.do(request(http.MethodHead, "/login?code="+code, nil))

	assertStatus(t, rec, http.StatusMethodNotAllowed)
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Errorf("Allow = %q, want GET", got)
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("Set-Cookie = %q, want none", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	assertLoggedIn(t, login(h, code))
}

func TestLogin_SimultaneousRedemptionsSucceedOnce(t *testing.T) {
	h := newHarness(t)
	code := h.srv.MintLoginCode()

	codes := h.concurrently(32, func(int) *http.Request {
		return request(http.MethodGet, "/login?code="+code, nil)
	})

	if got := countCodes(codes); got[http.StatusSeeOther] != 1 || got[http.StatusForbidden] != 31 {
		t.Errorf("statuses = %v, want one 303 and 31 403s", got)
	}
}

func TestLogin_SetsStrictHttpOnlyCookie(t *testing.T) {
	h := newHarness(t)

	rec := login(h, h.srv.MintLoginCode())

	assertLoggedIn(t, rec)
	want := "loadline_ui=" + uiCookieValue(testToken) + "; Path=/; Max-Age=2592000; HttpOnly; SameSite=Strict"
	setCookie := rec.Header().Values("Set-Cookie")
	if len(setCookie) != 1 || setCookie[0] != want {
		t.Fatalf("Set-Cookie = %q, want [%q]", setCookie, want)
	}
	cookie, err := http.ParseSetCookie(setCookie[0])
	if err != nil {
		t.Fatalf("parse Set-Cookie: %v", err)
	}
	snapshot := request(http.MethodGet, "/api/snapshot", nil)
	addCookie(snapshot, cookie.Name, cookie.Value)
	assertStatus(t, h.do(snapshot), http.StatusOK)
}

func TestLogin_RefusesUnknownCodes(t *testing.T) {
	h := newHarness(t)
	valid := h.srv.MintLoginCode()

	for _, target := range []string{
		"/login",
		"/login?code=",
		"/login?code=" + strings.Repeat("0", 32),
		"/login?code=" + strings.ToUpper(valid),
		"/login?code=" + valid[:31],
		"/login?code=" + valid + "0",
		"/login?token=" + string(testToken),
	} {
		t.Run(target, func(t *testing.T) {
			rec := h.do(request(http.MethodGet, target, nil))

			assertRefused(t, rec)
			if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
			}
			if !strings.Contains(rec.Body.String(), "loadline open") {
				t.Errorf("body = %q, want it to tell the user to run loadline open", rec.Body.String())
			}
		})
	}

	assertLoggedIn(t, login(h, valid))
}

func TestLogin_AtMost16CodesOutstanding(t *testing.T) {
	h := newHarness(t)
	codes := make([]string, 17)
	for i := range codes {
		codes[i] = h.srv.MintLoginCode()
	}

	assertRefused(t, login(h, codes[0]))
	for _, code := range codes[1:] {
		assertLoggedIn(t, login(h, code))
	}
}

func TestLogin_CodesAre16RandomBytesInHex(t *testing.T) {
	h := newHarness(t)
	seen := make(map[string]bool)
	for range 100 {
		code := h.srv.MintLoginCode()
		raw, err := hex.DecodeString(code)
		if err != nil || len(raw) != 16 || code != strings.ToLower(code) {
			t.Fatalf("code %q is not 32 lowercase hex characters", code)
		}
		if seen[code] {
			t.Fatalf("code %q minted twice", code)
		}
		seen[code] = true
	}
}

func TestLoginCode_MintedWithTheToken(t *testing.T) {
	h := newHarness(t)

	rec := h.do(tokenRequest(http.MethodPost, "/api/login-code", nil))

	assertStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Code) != 32 {
		t.Fatalf("body = %q, want {\"code\":\"<32 hex>\"}", rec.Body.String())
	}
	assertLoggedIn(t, login(h, body.Code))
}
