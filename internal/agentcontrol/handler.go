package agentcontrol

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) http.Handler { return &Handler{service: service} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/enroll" {
		h.enroll(w, r)
		return
	}
	agentID, ok := h.authorize(w, r)
	if !ok {
		return
	}
	switch r.URL.Path {
	case "/heartbeat":
		var input agentprotocol.Heartbeat
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.AgentID != agentID || h.service.HeartbeatAuthenticated(r.Context(), input, r.TLS.PeerCertificates[0]) != nil {
			http.Error(w, "invalid heartbeat", http.StatusUnprocessableEntity)
			return
		}
		writeAgentJSON(w, http.StatusOK, map[string]string{"status": "online"})
	case "/renew":
		var input agentprotocol.RenewalRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.AgentID != agentID {
			http.Error(w, "invalid certificate renewal", http.StatusUnprocessableEntity)
			return
		}
		response, err := h.service.Renew(r.Context(), agentID, input)
		if err != nil {
			http.Error(w, "unable to renew certificate", http.StatusUnprocessableEntity)
			return
		}
		writeAgentJSON(w, http.StatusOK, response)
	case "/lease":
		assignment, err := h.service.Lease(r.Context(), agentID)
		if errors.Is(err, sql.ErrNoRows) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			http.Error(w, "unable to lease work", http.StatusServiceUnavailable)
			return
		}
		writeAgentJSON(w, http.StatusOK, assignment)
	case "/result":
		var input agentprotocol.Result
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.AgentID != agentID || input.Version != agentprotocol.Version || input.AssignmentID == "" || (input.Status != "succeeded" && input.Status != "failed") {
			http.Error(w, "invalid result", http.StatusUnprocessableEntity)
			return
		}
		if err := h.service.Complete(r.Context(), input); err != nil {
			http.Error(w, "unable to complete work", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "/filesystem/claim":
		assignment, err := h.service.ClaimFilesystem(r.Context(), agentID)
		if errors.Is(err, sql.ErrNoRows) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			http.Error(w, "unable to claim filesystem request", http.StatusServiceUnavailable)
			return
		}
		writeAgentJSON(w, http.StatusOK, assignment)
	case "/filesystem/result":
		var input agentprotocol.Result
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.AgentID != agentID || input.Version != agentprotocol.Version || input.AssignmentID == "" || input.Status != "succeeded" && input.Status != "failed" {
			http.Error(w, "invalid filesystem result", http.StatusUnprocessableEntity)
			return
		}
		if err := h.service.CompleteFilesystem(r.Context(), input); err != nil {
			http.Error(w, "unable to complete filesystem request", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "/restore/claim":
		assignment, err := h.service.ClaimRestore(r.Context(), agentID)
		if errors.Is(err, sql.ErrNoRows) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			http.Error(w, "unable to claim restore request", http.StatusServiceUnavailable)
			return
		}
		writeAgentJSON(w, http.StatusOK, assignment)
	case "/restore/result":
		var input agentprotocol.Result
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.AgentID != agentID || input.Version != agentprotocol.Version || input.AssignmentID == "" || input.Status != "succeeded" && input.Status != "failed" {
			http.Error(w, "invalid restore result", http.StatusUnprocessableEntity)
			return
		}
		if err := h.service.CompleteRestore(r.Context(), input); err != nil {
			http.Error(w, "unable to complete restore request", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.service == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "mutual TLS identity required", http.StatusUnauthorized)
		return "", false
	}
	certificate := r.TLS.PeerCertificates[0]
	agentID := certificate.Subject.CommonName
	active, err := h.service.Authenticate(r.Context(), agentID, certificate.SerialNumber.String())
	if err != nil || !active {
		http.Error(w, "agent certificate is not active", http.StatusUnauthorized)
		return "", false
	}
	return agentID, true
}

func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "agent control unavailable", http.StatusServiceUnavailable)
		return
	}
	var input agentprotocol.EnrollmentRequest
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "invalid enrollment", http.StatusBadRequest)
		return
	}
	response, err := h.service.Enroll(r.Context(), input.Token, input)
	if err != nil {
		http.Error(w, "invalid enrollment", http.StatusUnauthorized)
		return
	}
	writeAgentJSON(w, http.StatusCreated, response)
}

func writeAgentJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
