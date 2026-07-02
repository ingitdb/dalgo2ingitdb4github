package dalgo2ghingitdb

import (
	"context"
	"fmt"
	"net/http"
)

// TokenProvider supplies the GitHub token used to authorize API calls.
// It is invoked once per outgoing HTTP request, so implementations can rotate
// tokens (e.g. mint short-lived GitHub App installation tokens on demand)
// without rebuilding the adapter. Returning an empty token with a nil error
// sends the request unauthenticated (fine for public-repo reads).
//
// See docs/roadmaps/ovdb-access-tokens-grants.md in sneat-co/backstage
// (Decision 3.4): the sneat-go backend injects a provider that mints 1-hour
// installation tokens with an in-memory cache; CLI/dev injects
// StaticTokenProvider with a PAT.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// TokenProviderFunc adapts a plain function to the TokenProvider interface.
type TokenProviderFunc func(ctx context.Context) (string, error)

// Token implements TokenProvider.
func (f TokenProviderFunc) Token(ctx context.Context) (string, error) {
	return f(ctx)
}

// StaticTokenProvider returns a TokenProvider that always yields the given
// fixed token (e.g. a personal access token for CLI/dev usage). It preserves
// the legacy Config.Token behavior: a non-empty Config.Token is wrapped in a
// StaticTokenProvider automatically when Config.TokenProvider is nil.
func StaticTokenProvider(token string) TokenProvider {
	return staticTokenProvider(token)
}

type staticTokenProvider string

func (p staticTokenProvider) Token(context.Context) (string, error) {
	return string(p), nil
}

// tokenProviderTransport is an http.RoundTripper that asks the TokenProvider
// for a token on every request and injects it as a Bearer Authorization
// header. Provider errors abort the request and surface to the caller as
// regular operation errors (wrapped by the http.Client, then by
// wrapGitHubError at the call sites).
type tokenProviderTransport struct {
	base     http.RoundTripper
	provider TokenProvider
}

func (t tokenProviderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.provider.Token(req.Context())
	if err != nil {
		return nil, fmt.Errorf("token provider failed: %w", err)
	}
	if token != "" {
		// RoundTrippers must not mutate the caller's request; clone before
		// setting the header.
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return t.base.RoundTrip(req)
}
