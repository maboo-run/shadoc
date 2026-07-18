package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/controlplane"
	"github.com/maboo-run/shadoc/internal/store"
)

type recoveryControlPlaneManager struct {
	exportBundle     []byte
	exportErr        error
	exportPassphrase string
	preview          controlplane.ImportPreview
	preflightErr     error
	preflightBundle  []byte
	preflightPhrase  string
	importResult     controlplane.ImportResult
	importErr        error
	importBundle     []byte
	importPhrase     string
	importPreviewID  string
	importStarted    chan struct{}
	importRelease    chan struct{}
}

func (manager *recoveryControlPlaneManager) Export(_ context.Context, passphrase string) ([]byte, error) {
	manager.exportPassphrase = passphrase
	return append([]byte(nil), manager.exportBundle...), manager.exportErr
}

func (manager *recoveryControlPlaneManager) PreflightImport(_ context.Context, bundle []byte, passphrase string) (controlplane.ImportPreview, error) {
	manager.preflightBundle = append([]byte(nil), bundle...)
	manager.preflightPhrase = passphrase
	return manager.preview, manager.preflightErr
}

func (manager *recoveryControlPlaneManager) Import(ctx context.Context, bundle []byte, passphrase, previewID string) (controlplane.ImportResult, error) {
	manager.importBundle = append([]byte(nil), bundle...)
	manager.importPhrase = passphrase
	manager.importPreviewID = previewID
	if manager.importStarted != nil {
		close(manager.importStarted)
	}
	if manager.importRelease != nil {
		select {
		case <-manager.importRelease:
		case <-ctx.Done():
			return controlplane.ImportResult{}, ctx.Err()
		}
	}
	return manager.importResult, manager.importErr
}

func TestControlPlaneExportRequiresReauthenticationAndReturnsAttachment(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &recoveryControlPlaneManager{exportBundle: []byte("authenticated encrypted recovery bundle")}
	srv.controlPlane = manager
	cookie := setupSession(t, srv)

	response := requestJSON(t, srv, http.MethodPost, "/api/control-plane/export", map[string]any{
		"administratorPassword":          "correct horse battery staple",
		"recoveryPassphrase":             "recovery horse battery staple",
		"recoveryPassphraseConfirmation": "recovery horse battery staple",
	}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("content type=%q", got)
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, `attachment; filename="shadoc-recovery-`) || !strings.HasSuffix(disposition, `.rcbundle"`) {
		t.Fatalf("content disposition=%q", disposition)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Body.String() != string(manager.exportBundle) {
		t.Fatalf("headers=%v body=%q", response.Header(), response.Body.String())
	}
	if manager.exportPassphrase != "recovery horse battery staple" {
		t.Fatalf("passphrase=%q", manager.exportPassphrase)
	}

	encodedAudits, err := json.Marshal(mustAudits(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"correct horse battery staple", "recovery horse battery staple", "authenticated encrypted recovery bundle"} {
		if bytes.Contains(encodedAudits, []byte(forbidden)) {
			t.Fatalf("audit leaked protected input %q: %s", forbidden, encodedAudits)
		}
	}
	if !bytes.Contains(encodedAudits, []byte("control_plane.export")) {
		t.Fatalf("missing semantic export audit: %s", encodedAudits)
	}
}

func TestControlPlaneExportRejectsBadAdministratorOrPassphraseConfirmation(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &recoveryControlPlaneManager{exportBundle: []byte("bundle")}
	srv.controlPlane = manager
	cookie := setupSession(t, srv)

	badAdministrator := requestJSON(t, srv, http.MethodPost, "/api/control-plane/export", map[string]any{
		"administratorPassword": "wrong password", "recoveryPassphrase": "recovery horse battery staple", "recoveryPassphraseConfirmation": "recovery horse battery staple",
	}, cookie)
	if badAdministrator.Code != http.StatusUnauthorized {
		t.Fatalf("bad administrator status=%d body=%s", badAdministrator.Code, badAdministrator.Body.String())
	}
	mismatch := requestJSON(t, srv, http.MethodPost, "/api/control-plane/export", map[string]any{
		"administratorPassword": "correct horse battery staple", "recoveryPassphrase": "recovery horse battery staple", "recoveryPassphraseConfirmation": "different recovery phrase",
	}, cookie)
	if mismatch.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	if manager.exportPassphrase != "" {
		t.Fatalf("invalid request reached exporter: %q", manager.exportPassphrase)
	}
}

func TestControlPlaneImportPreflightReadsBoundedMultipartBundle(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &recoveryControlPlaneManager{preview: controlplane.ImportPreview{
		PreviewID: "preview-123", CanImport: true, SourceApplicationVersion: "1.2.3",
		ResourceCounts: map[string]int{"repositories": 2}, ExcludedTransientClasses: []string{"sessions", "operations"}, RestartRequired: true,
	}}
	srv.controlPlane = manager
	cookie := setupSession(t, srv)

	response := requestControlPlaneMultipart(t, srv, "/api/control-plane/import/preflight", cookie, []byte("sealed recovery bundle"), map[string]string{
		"recoveryPassphrase": "recovery horse battery staple",
	})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"previewId":"preview-123"`) || !strings.Contains(response.Body.String(), `"canImport":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if string(manager.preflightBundle) != "sealed recovery bundle" || manager.preflightPhrase != "recovery horse battery staple" {
		t.Fatalf("bundle=%q passphrase=%q", manager.preflightBundle, manager.preflightPhrase)
	}
	if !hasAuditAction(mustAudits(t, srv), "control_plane.import_preflight") {
		t.Fatalf("missing preflight audit: %+v", mustAudits(t, srv))
	}
}

func TestControlPlaneImportPreflightReturnsSafeErrorForInvalidBundle(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &recoveryControlPlaneManager{preflightErr: errors.New("invalid bundle contained protected-value")}
	srv.controlPlane = manager
	cookie := setupSession(t, srv)

	response := requestControlPlaneMultipart(t, srv, "/api/control-plane/import/preflight", cookie, []byte("protected-bundle-bytes"), map[string]string{
		"recoveryPassphrase": "protected-recovery-passphrase",
	})
	if response.Code != http.StatusUnprocessableEntity || strings.Contains(response.Body.String(), "protected") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestControlPlaneImportQueuesConfirmedPersistentOperationWithoutSecrets(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &recoveryControlPlaneManager{
		importResult: controlplane.ImportResult{
			ImportedCounts:  map[string]int{"repositories": 2, "tasks": 3},
			Revalidation:    []controlplane.RevalidationItem{{ResourceType: "repository", ResourceID: "repo-a", Action: "verify_existing"}},
			RestartRequired: true,
		},
		importStarted: make(chan struct{}), importRelease: make(chan struct{}),
	}
	srv.controlPlane = manager
	cookie := setupSession(t, srv)

	response := requestControlPlaneMultipart(t, srv, "/api/control-plane/import", cookie, []byte("sealed protected bundle"), map[string]string{
		"recoveryPassphrase":    "recovery horse battery staple",
		"previewId":             "preview-123",
		"administratorPassword": "correct horse battery staple",
		"impactConfirmed":       "true",
	})
	var accepted struct {
		OperationID string `json:"operationId"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || response.Code != http.StatusAccepted || accepted.OperationID == "" || accepted.Status != "queued" {
		t.Fatalf("status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	select {
	case <-manager.importStarted:
	case <-time.After(time.Second):
		t.Fatal("import operation did not start")
	}
	close(manager.importRelease)
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if operation.Kind != "control_plane_import" || operation.Detail["restartRequired"] != true {
		t.Fatalf("operation=%+v", operation)
	}
	if manager.importPhrase != "recovery horse battery staple" || manager.importPreviewID != "preview-123" || string(manager.importBundle) != "sealed protected bundle" {
		t.Fatalf("bundle=%q passphrase=%q preview=%q", manager.importBundle, manager.importPhrase, manager.importPreviewID)
	}
	encodedOperation, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	encodedAudits, err := json.Marshal(mustAudits(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"sealed protected bundle", "recovery horse battery staple", "correct horse battery staple", "preview-123"} {
		if bytes.Contains(encodedOperation, []byte(forbidden)) || bytes.Contains(encodedAudits, []byte(forbidden)) {
			t.Fatalf("protected input %q leaked; operation=%s audits=%s", forbidden, encodedOperation, encodedAudits)
		}
	}
	if !bytes.Contains(encodedAudits, []byte("control_plane.import")) {
		t.Fatalf("missing semantic import audit: %s", encodedAudits)
	}
}

func TestControlPlaneImportRequiresImpactConfirmationAndAdministratorPassword(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &recoveryControlPlaneManager{}
	srv.controlPlane = manager
	cookie := setupSession(t, srv)

	unconfirmed := requestControlPlaneMultipart(t, srv, "/api/control-plane/import", cookie, []byte("bundle"), map[string]string{
		"recoveryPassphrase": "recovery horse battery staple", "previewId": "preview-123", "administratorPassword": "correct horse battery staple",
	})
	if unconfirmed.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed status=%d body=%s", unconfirmed.Code, unconfirmed.Body.String())
	}
	badAdministrator := requestControlPlaneMultipart(t, srv, "/api/control-plane/import", cookie, []byte("bundle"), map[string]string{
		"recoveryPassphrase": "recovery horse battery staple", "previewId": "preview-123", "administratorPassword": "wrong password", "impactConfirmed": "true",
	})
	if badAdministrator.Code != http.StatusUnauthorized {
		t.Fatalf("bad administrator status=%d body=%s", badAdministrator.Code, badAdministrator.Body.String())
	}
	if manager.importPreviewID != "" {
		t.Fatalf("invalid request reached importer: %q", manager.importPreviewID)
	}
}

func requestControlPlaneMultipart(t *testing.T, handler http.Handler, path string, cookie *http.Cookie, bundle []byte, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("bundle", "recovery.rcbundle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(bundle); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", cookie.Raw)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func mustAudits(t *testing.T, srv *Server) []store.AuditRecord {
	t.Helper()
	audits, err := srv.store.(*store.Store).ListAudits(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return audits
}

func hasAuditAction(audits []store.AuditRecord, action string) bool {
	for _, audit := range audits {
		if audit.Action == action {
			return true
		}
	}
	return false
}
