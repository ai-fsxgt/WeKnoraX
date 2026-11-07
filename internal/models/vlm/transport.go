package vlm

import (
	"fmt"
	"net/http"
	"time"

	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func validateVLMBaseURL(baseURL string) error {
	if baseURL == "" {
		return nil
	}
	if err := secutils.ValidateURLForSSRF(baseURL); err != nil {
		return fmt.Errorf("base URL SSRF check failed: %w", err)
	}
	return nil
}

// sharedVLMHTTPTransport is shared because a VLM client is created per image;
// a per-client transport would leak its idle keep-alive connections.
var sharedVLMHTTPTransport = secutils.NewSSRFSafeTransport(
	secutils.DefaultSSRFSafeHTTPClientConfig(),
)

func newVLMHTTPClient(timeout time.Duration) *http.Client {
	cfg := secutils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = timeout
	return secutils.NewSSRFSafeHTTPClientWithTransport(cfg, sharedVLMHTTPTransport)
}
