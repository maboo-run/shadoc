package agentcontrol

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLeaseEndpointRejectsRequestWithoutMTLSIdentity(t *testing.T) {
	handler := NewHandler(nil)
	request := httptest.NewRequest(http.MethodPost, "/lease", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
