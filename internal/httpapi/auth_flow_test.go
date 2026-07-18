package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/store"
)

func newAuthTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	manager := auth.New(s, func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) })
	return NewWithAuth(s, manager)
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &payload)
	req.RemoteAddr = "127.0.0.1:12345"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
		if cookie.Raw != "" {
			req.Header.Set("X-CSRF-Token", cookie.Raw)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "rc_session" {
			return cookie
		}
	}
	t.Fatal("response did not set rc_session cookie")
	return nil
}

func TestAdministratorSetupLoginAndLogout(t *testing.T) {
	srv := newAuthTestServer(t)
	remote := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`))
	remote.Header.Set("Content-Type", "application/json")
	remote.RemoteAddr = "192.168.1.25:1234"
	remoteResult := httptest.NewRecorder()
	srv.ServeHTTP(remoteResult, remote)
	if remoteResult.Code != http.StatusForbidden {
		t.Fatalf("remote setup status=%d", remoteResult.Code)
	}
	malformed := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`))
	malformed.RemoteAddr = "untrusted"
	malformedResult := httptest.NewRecorder()
	srv.ServeHTTP(malformedResult, malformed)
	if malformedResult.Code != http.StatusForbidden {
		t.Fatalf("malformed remote address setup status=%d", malformedResult.Code)
	}

	unauthorized := requestJSON(t, srv, http.MethodGet, "/api/session", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated session status = %d", unauthorized.Code)
	}

	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{
		"username": "admin",
		"password": "correct horse battery staple",
	}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup status = %d body=%s", setup.Code, setup.Body.String())
	}
	cookie := sessionCookie(t, setup)
	csrf := setup.Header().Get("X-CSRF-Token")
	if csrf == "" {
		t.Fatal("setup did not return CSRF token")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe session cookie: %+v", cookie)
	}

	duplicate := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{
		"username": "other",
		"password": "another correct horse password",
	}, nil)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate setup status = %d", duplicate.Code)
	}

	session := requestJSON(t, srv, http.MethodGet, "/api/session", nil, cookie)
	if session.Code != http.StatusOK {
		t.Fatalf("session status = %d body=%s", session.Code, session.Body.String())
	}
	var current struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(session.Body.Bytes(), &current); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if current.Username != "admin" {
		t.Fatalf("session username = %q", current.Username)
	}
	csrfCookie := func() *http.Cookie {
		for _, candidate := range setup.Result().Cookies() {
			if candidate.Name == "rc_csrf" {
				return candidate
			}
		}
		return nil
	}()
	if csrfCookie == nil || csrfCookie.HttpOnly {
		t.Fatalf("missing readable CSRF cookie: %+v", csrfCookie)
	}

	logoutWithoutCSRF := requestJSON(t, srv, http.MethodPost, "/api/logout", nil, cookie)
	if logoutWithoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF status = %d", logoutWithoutCSRF.Code)
	}
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	logoutRequest.AddCookie(cookie)
	logoutRequest.Header.Set("X-CSRF-Token", csrf)
	logout := httptest.NewRecorder()
	srv.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", logout.Code)
	}
	if status := requestJSON(t, srv, http.MethodGet, "/api/session", nil, cookie).Code; status != http.StatusUnauthorized {
		t.Fatalf("logged out session status = %d", status)
	}

	badLogin := requestJSON(t, srv, http.MethodPost, "/api/login", map[string]string{
		"username": "admin", "password": "wrong password",
	}, nil)
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d", badLogin.Code)
	}

	login := requestJSON(t, srv, http.MethodPost, "/api/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	_ = sessionCookie(t, login)
	audits, err := srv.store.(*store.Store).ListAudits(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	foundSuccess, foundFailure, foundLogout := false, false, false
	for _, audit := range audits {
		foundSuccess = foundSuccess || (audit.Action == "auth.login.success" && audit.Actor == "admin")
		foundFailure = foundFailure || (audit.Action == "auth.login.failure" && audit.Actor == "admin")
		foundLogout = foundLogout || (audit.Action == "auth.logout" && audit.Actor == "admin")
	}
	if !foundSuccess || !foundFailure || !foundLogout {
		t.Fatalf("login audits=%+v", audits)
	}
}

func TestLANSetupRequiresConfiguredOneTimeToken(t *testing.T) {
	srv := newAuthTestServer(t)
	srv.setupToken = "installer-token"
	for _, test := range []struct {
		token string
		want  int
	}{
		{token: "wrong", want: http.StatusForbidden},
		{token: "installer-token", want: http.StatusCreated},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"username":"admin","password":"correct horse battery staple","token":"`+test.token+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.168.1.25:1234"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Fatalf("token %q status=%d body=%s", test.token, rec.Code, rec.Body.String())
		}
	}
}
