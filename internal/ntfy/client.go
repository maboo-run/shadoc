package ntfy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Config struct{ BaseURL, Topic, Token string }
type Event struct{ Title, Message, Severity string }
type Client struct{ http *http.Client }

func New(client *http.Client) *Client {
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{http: client}
}
func (c *Client) Publish(ctx context.Context, config Config, event Event) error {
	base, err := url.Parse(config.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || strings.TrimSpace(config.Topic) == "" {
		return errors.New("valid ntfy HTTP endpoint and topic are required")
	}
	if event.Message == "" {
		return errors.New("notification message is required")
	}
	if config.Token != "" && strings.Contains(event.Message, config.Token) {
		return errors.New("notification contains secret token")
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/" + url.PathEscape(config.Topic)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(event.Message))
	if err != nil {
		return err
	}
	request.Header.Set("Title", event.Title)
	switch event.Severity {
	case "error":
		request.Header.Set("Priority", "high")
		request.Header.Set("Tags", "warning")
	case "success":
		request.Header.Set("Tags", "white_check_mark")
	}
	if config.Token != "" {
		request.Header.Set("Authorization", "Bearer "+config.Token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned status %d", response.StatusCode)
	}
	return nil
}
