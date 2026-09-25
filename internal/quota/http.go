package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxQuotaBodyBytes bounds upstream quota responses against memory exhaustion.
const maxQuotaBodyBytes = 4 << 20

// DoJSON performs one upstream quota call and decodes a JSON response. It
// enforces the body limit and returns errors containing only method, URL, and
// status — never headers or bodies, which may carry credentials or PII.
func DoJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return fmt.Errorf("quota request %s %s: %w", method, url, err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("quota fetch %s %s: %w", method, url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("quota fetch %s %s: unexpected status %d", method, url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaBodyBytes+1))
	if err != nil {
		return fmt.Errorf("quota fetch %s %s: read body: %w", method, url, err)
	}
	if len(data) > maxQuotaBodyBytes {
		return fmt.Errorf("quota fetch %s %s: body exceeds %d bytes", method, url, maxQuotaBodyBytes)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("quota fetch %s %s: decode body: %w", method, url, err)
	}
	return nil
}
