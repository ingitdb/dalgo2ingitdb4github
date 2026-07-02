package dalgo2ghingitdb

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v88/github"
)

// newGitHubAPIClient builds the go-github client from a Config. Shared by
// NewGitHubFileReader and NewTreeWriter (which previously duplicated this
// construction logic inline).
//
// Fable refactoring: authorization moved from a construction-time
// github.WithAuthToken(cfg.Token) to the per-request tokenProviderTransport,
// so tokens can rotate per operation (short-lived GitHub App installation
// tokens; see TokenProvider). A bare Config.Token keeps working unchanged —
// Config.tokenProvider() wraps it in StaticTokenProvider.
func newGitHubAPIClient(cfg Config) (*github.Client, error) {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if provider := cfg.tokenProvider(); provider != nil {
		base := httpClient.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		// Shallow-copy the client so a caller-supplied http.Client is never
		// mutated by the adapter.
		authClient := *httpClient
		authClient.Transport = tokenProviderTransport{base: base, provider: provider}
		httpClient = &authClient
	}
	opts := make([]github.ClientOptionsFunc, 0, 2) // max 2: HTTPClient + APIBaseURL
	opts = append(opts, github.WithHTTPClient(httpClient))
	if cfg.APIBaseURL != "" {
		baseURL := cfg.APIBaseURL
		if !strings.HasSuffix(baseURL, "/") {
			baseURL += "/"
		}
		opts = append(opts, github.WithEnterpriseURLs(baseURL, baseURL))
	}
	client, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create github client: %w", err)
	}
	return client, nil
}
