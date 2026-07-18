package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
)

type HTTPControl struct {
	baseURL       string
	client        *http.Client
	credentialDir string
}

func NewHTTPControl(baseURL string, client *http.Client) (*HTTPControl, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("valid agent service URL is required")
	}
	if parsed.Scheme != "https" {
		return nil, errors.New("agent service URL must use HTTPS")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPControl{baseURL: strings.TrimRight(baseURL, "/"), client: client}, nil
}

func (c *HTTPControl) Heartbeat(ctx context.Context, heartbeat agentprotocol.Heartbeat) error {
	return c.post(ctx, "/heartbeat", heartbeat, nil, http.StatusOK)
}

func (c *HTTPControl) Lease(ctx context.Context) (agentprotocol.Assignment, bool, error) {
	var assignment agentprotocol.Assignment
	status, err := c.postStatus(ctx, "/lease", struct{}{}, &assignment)
	if err != nil {
		return agentprotocol.Assignment{}, false, err
	}
	if status == http.StatusNoContent {
		return agentprotocol.Assignment{}, false, nil
	}
	if status != http.StatusOK {
		return agentprotocol.Assignment{}, false, fmt.Errorf("agent service returned HTTP %d", status)
	}
	return assignment, true, nil
}

func (c *HTTPControl) Complete(ctx context.Context, result agentprotocol.Result) error {
	return c.post(ctx, "/result", result, nil, http.StatusNoContent)
}

func (c *HTTPControl) ClaimFilesystem(ctx context.Context) (agentprotocol.Assignment, bool, error) {
	var assignment agentprotocol.Assignment
	status, err := c.postStatus(ctx, "/filesystem/claim", struct{}{}, &assignment)
	if err != nil {
		return assignment, false, err
	}
	if status == http.StatusNoContent {
		return assignment, false, nil
	}
	if status != http.StatusOK {
		return assignment, false, fmt.Errorf("agent service returned HTTP %d", status)
	}
	return assignment, true, nil
}

func (c *HTTPControl) CompleteFilesystem(ctx context.Context, result agentprotocol.Result) error {
	return c.post(ctx, "/filesystem/result", result, nil, http.StatusNoContent)
}

func (c *HTTPControl) ClaimRestore(ctx context.Context) (agentprotocol.Assignment, bool, error) {
	var assignment agentprotocol.Assignment
	status, err := c.postStatus(ctx, "/restore/claim", struct{}{}, &assignment)
	if err != nil {
		return assignment, false, err
	}
	if status == http.StatusNoContent {
		return assignment, false, nil
	}
	if status != http.StatusOK {
		return assignment, false, fmt.Errorf("agent service returned HTTP %d", status)
	}
	return assignment, true, nil
}

func (c *HTTPControl) CompleteRestore(ctx context.Context, result agentprotocol.Result) error {
	return c.post(ctx, "/restore/result", result, nil, http.StatusNoContent)
}

func (c *HTTPControl) post(ctx context.Context, path string, input, output any, expected int) error {
	status, err := c.postStatus(ctx, path, input, output)
	if err != nil {
		return err
	}
	if status != expected {
		return fmt.Errorf("agent service returned HTTP %d", status)
	}
	return nil
}

func (c *HTTPControl) postStatus(ctx context.Context, path string, input, output any) (int, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if output != nil && response.StatusCode >= 200 && response.StatusCode < 300 && response.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output); err != nil {
			return response.StatusCode, err
		}
	}
	return response.StatusCode, nil
}
