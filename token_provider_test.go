package dalgo2ghingitdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestStaticTokenProvider(t *testing.T) {
	t.Parallel()
	provider := StaticTokenProvider("my-static-token")
	for i := 0; i < 2; i++ {
		token, err := provider.Token(context.Background())
		if err != nil {
			t.Fatalf("Token() unexpected error: %v", err)
		}
		if token != "my-static-token" {
			t.Errorf("Token() = %q, want %q", token, "my-static-token")
		}
	}
}

func TestTokenProviderFunc(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	provider := TokenProviderFunc(func(ctx context.Context) (string, error) {
		return "fn-token", wantErr
	})
	token, err := provider.Token(context.Background())
	if token != "fn-token" {
		t.Errorf("Token() = %q, want %q", token, "fn-token")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Token() error = %v, want %v", err, wantErr)
	}
}

func TestConfig_TokenProvider_Resolution(t *testing.T) {
	t.Parallel()
	explicit := StaticTokenProvider("explicit")
	tests := []struct {
		name string
		cfg  Config
		want string // "" means expect nil provider
	}{
		{name: "no token no provider", cfg: Config{}, want: ""},
		{name: "static token auto-wrapped", cfg: Config{Token: "legacy-pat"}, want: "legacy-pat"},
		{name: "explicit provider", cfg: Config{TokenProvider: explicit}, want: "explicit"},
		{name: "provider wins over token", cfg: Config{Token: "legacy-pat", TokenProvider: explicit}, want: "explicit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := tc.cfg.tokenProvider()
			if tc.want == "" {
				if provider != nil {
					t.Fatalf("tokenProvider() = %v, want nil", provider)
				}
				return
			}
			if provider == nil {
				t.Fatal("tokenProvider() = nil, want non-nil")
			}
			token, err := provider.Token(context.Background())
			if err != nil {
				t.Fatalf("Token() unexpected error: %v", err)
			}
			if token != tc.want {
				t.Errorf("Token() = %q, want %q", token, tc.want)
			}
		})
	}
}

// authRecordingServer records the Authorization header of every request and
// serves a minimal GitHub Contents API file response.
func authRecordingServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"type":     "file",
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte("hello")),
			"sha":      "abc123",
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), authHeaders...)
	}
}

func TestFileReader_StaticConfigToken_SendsBearerHeader(t *testing.T) {
	t.Parallel()
	server, headers := authRecordingServer(t)

	cfg := Config{Owner: "test", Repo: "test", Token: "legacy-pat", APIBaseURL: server.URL + "/"}
	reader, err := NewGitHubFileReader(cfg)
	if err != nil {
		t.Fatalf("NewGitHubFileReader: %v", err)
	}
	if _, _, err = reader.ReadFile(context.Background(), "test.txt"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := headers()
	if len(got) != 1 {
		t.Fatalf("expected 1 request, got %d", len(got))
	}
	if got[0] != "Bearer legacy-pat" {
		t.Errorf("Authorization = %q, want %q", got[0], "Bearer legacy-pat")
	}
}

func TestFileReader_TokenProvider_CalledPerOperation(t *testing.T) {
	t.Parallel()
	server, headers := authRecordingServer(t)

	var mu sync.Mutex
	calls := 0
	provider := TokenProviderFunc(func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return fmt.Sprintf("tok-%d", calls), nil
	})

	cfg := Config{Owner: "test", Repo: "test", TokenProvider: provider, APIBaseURL: server.URL + "/"}
	reader, err := NewGitHubFileReader(cfg)
	if err != nil {
		t.Fatalf("NewGitHubFileReader: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, _, readErr := reader.ReadFile(ctx, "test.txt"); readErr != nil {
			t.Fatalf("ReadFile #%d: %v", i+1, readErr)
		}
	}

	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 2 {
		t.Errorf("provider called %d times, want 2 (once per operation)", gotCalls)
	}
	got := headers()
	want := []string{"Bearer tok-1", "Bearer tok-2"}
	if len(got) != len(want) {
		t.Fatalf("expected %d requests, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request #%d Authorization = %q, want %q (token must rotate per operation)", i+1, got[i], want[i])
		}
	}
}

func TestFileReader_TokenProvider_WinsOverStaticToken(t *testing.T) {
	t.Parallel()
	server, headers := authRecordingServer(t)

	provider := StaticTokenProvider("provider-token")
	cfg := Config{Owner: "test", Repo: "test", Token: "static-token", TokenProvider: provider, APIBaseURL: server.URL + "/"}
	reader, err := NewGitHubFileReader(cfg)
	if err != nil {
		t.Fatalf("NewGitHubFileReader: %v", err)
	}
	if _, _, err = reader.ReadFile(context.Background(), "test.txt"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := headers()
	if len(got) != 1 || got[0] != "Bearer provider-token" {
		t.Errorf("Authorization headers = %v, want [%q]", got, "Bearer provider-token")
	}
}

func TestFileReader_TokenProvider_EmptyTokenMeansUnauthenticated(t *testing.T) {
	t.Parallel()
	server, headers := authRecordingServer(t)

	provider := TokenProviderFunc(func(ctx context.Context) (string, error) {
		return "", nil
	})
	cfg := Config{Owner: "test", Repo: "test", TokenProvider: provider, APIBaseURL: server.URL + "/"}
	reader, err := NewGitHubFileReader(cfg)
	if err != nil {
		t.Fatalf("NewGitHubFileReader: %v", err)
	}
	if _, _, err = reader.ReadFile(context.Background(), "test.txt"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := headers()
	if len(got) != 1 || got[0] != "" {
		t.Errorf("Authorization headers = %v, want one empty header", got)
	}
}

func TestFileReader_TokenProvider_ErrorPropagates(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	}))
	defer server.Close()

	providerErr := errors.New("installation token minting failed")
	provider := TokenProviderFunc(func(ctx context.Context) (string, error) {
		return "", providerErr
	})
	cfg := Config{Owner: "test", Repo: "test", TokenProvider: provider, APIBaseURL: server.URL + "/"}
	reader, err := NewGitHubFileReader(cfg)
	if err != nil {
		t.Fatalf("NewGitHubFileReader: %v", err)
	}

	_, _, err = reader.ReadFile(context.Background(), "test.txt")
	if err == nil {
		t.Fatal("ReadFile() expected error from token provider, got nil")
	}
	if !strings.Contains(err.Error(), "token provider failed") {
		t.Errorf("ReadFile() error = %q, want to contain 'token provider failed'", err.Error())
	}
	if !strings.Contains(err.Error(), providerErr.Error()) {
		t.Errorf("ReadFile() error = %q, want to contain %q", err.Error(), providerErr.Error())
	}
	if requests != 0 {
		t.Errorf("server received %d requests, want 0 (request must not fire without a token)", requests)
	}

	// ListDirectory goes through the same transport and must fail the same way.
	if _, err = reader.ListDirectory(context.Background(), "some/dir"); err == nil {
		t.Fatal("ListDirectory() expected error from token provider, got nil")
	} else if !strings.Contains(err.Error(), "token provider failed") {
		t.Errorf("ListDirectory() error = %q, want to contain 'token provider failed'", err.Error())
	}
}

func TestTreeWriter_TokenProvider_ErrorPropagates(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	providerErr := errors.New("grant revoked")
	provider := TokenProviderFunc(func(ctx context.Context) (string, error) {
		return "", providerErr
	})
	cfg := Config{Owner: "test", Repo: "test", Ref: "main", TokenProvider: provider, APIBaseURL: server.URL + "/"}
	writer, err := NewTreeWriter(cfg)
	if err != nil {
		t.Fatalf("NewTreeWriter: %v", err)
	}

	_, err = writer.CommitChanges(context.Background(), "msg", []TreeChange{{Path: "a.txt", Content: []byte("x")}})
	if err == nil {
		t.Fatal("CommitChanges() expected error from token provider, got nil")
	}
	if !strings.Contains(err.Error(), "token provider failed") {
		t.Errorf("CommitChanges() error = %q, want to contain 'token provider failed'", err.Error())
	}
	if !strings.Contains(err.Error(), providerErr.Error()) {
		t.Errorf("CommitChanges() error = %q, want to contain %q", err.Error(), providerErr.Error())
	}
}

func TestTreeWriter_TokenProvider_SendsBearerHeader(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()
		http.NotFound(w, r) // the operation may fail; we only assert auth
	}))
	defer server.Close()

	cfg := Config{Owner: "test", Repo: "test", Ref: "main", TokenProvider: StaticTokenProvider("writer-token"), APIBaseURL: server.URL + "/"}
	writer, err := NewTreeWriter(cfg)
	if err != nil {
		t.Fatalf("NewTreeWriter: %v", err)
	}

	_, _ = writer.ListFilesUnder(context.Background(), "dir")

	mu.Lock()
	got := append([]string(nil), authHeaders...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected at least one request to reach the server")
	}
	for i, h := range got {
		if h != "Bearer writer-token" {
			t.Errorf("request #%d Authorization = %q, want %q", i+1, h, "Bearer writer-token")
		}
	}
}

func TestFileReader_CustomHTTPClientNotMutated(t *testing.T) {
	t.Parallel()
	server, headers := authRecordingServer(t)

	custom := &http.Client{}
	cfg := Config{Owner: "test", Repo: "test", Token: "tok", HTTPClient: custom, APIBaseURL: server.URL + "/"}
	reader, err := NewGitHubFileReader(cfg)
	if err != nil {
		t.Fatalf("NewGitHubFileReader: %v", err)
	}
	if custom.Transport != nil {
		t.Error("caller-supplied http.Client.Transport was mutated; adapter must copy the client")
	}
	if _, _, err = reader.ReadFile(context.Background(), "test.txt"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := headers()
	if len(got) != 1 || got[0] != "Bearer tok" {
		t.Errorf("Authorization headers = %v, want [%q]", got, "Bearer tok")
	}
}
