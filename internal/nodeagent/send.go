package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Sender posts batches to the controller.
type Sender struct {
	Endpoint string
	Token    string
	Client   *http.Client
}

// Send posts one batch. An empty batch is a no-op.
func (s *Sender) Send(ctx context.Context, b Batch) error {
	if len(b.Lines) == 0 && len(b.Cgroups) == 0 {
		return nil
	}
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.Endpoint, "/")+"/v1/logs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("controller returned %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
