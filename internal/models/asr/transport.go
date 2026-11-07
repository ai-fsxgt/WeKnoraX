package asr

import (
	"fmt"
	"net/http"
	"time"

	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func validateASRBaseURL(baseURL string) error {
	if baseURL == "" {
		return nil
	}
	if err := secutils.ValidateURLForSSRF(baseURL); err != nil {
		return fmt.Errorf("base URL SSRF check failed: %w", err)
	}
	return nil
}

// sharedASRHTTPTransport is shared so ASR clients created per request reuse one
// keep-alive pool instead of each leaking idle connections.
var sharedASRHTTPTransport = secutils.NewSSRFSafeTransport(
	secutils.DefaultSSRFSafeHTTPClientConfig(),
)

func newASRHTTPClient(timeout time.Duration) *http.Client {
	cfg := secutils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = timeout
	return secutils.NewSSRFSafeHTTPClientWithTransport(cfg, sharedASRHTTPTransport)
}
