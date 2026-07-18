package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesEmbeddedAppAndSPAFallback(t *testing.T) {
	handler := Handler()
	for _, path := range []string{"/", "/backups/tasks"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		body, err := io.ReadAll(rec.Result().Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "<title>影刻 · Shadoc</title>") {
			t.Fatalf("%s did not serve application shell", path)
		}
	}
}
