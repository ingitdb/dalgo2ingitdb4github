package dalgo2ghingitdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v88/github"
)

// Config defines connection settings for reading an inGitDB repository from GitHub.
type Config struct {
	Owner string
	Repo  string
	Ref   string

	// Token is a static GitHub token (e.g. a classic/fine-grained PAT).
	// If TokenProvider is nil and Token is non-empty, Token is wrapped in
	// StaticTokenProvider automatically, preserving legacy behavior.
	Token string

	// TokenProvider, when set, supplies the token per request and takes
	// precedence over Token. Use it to inject rotating credentials such as
	// short-lived GitHub App installation tokens.
	TokenProvider TokenProvider

	APIBaseURL string
	HTTPClient *http.Client
}

func (c Config) validate() error {
	if c.Owner == "" {
		return errors.New("owner is required")
	}
	if c.Repo == "" {
		return errors.New("repo is required")
	}
	return nil
}

// tokenProvider resolves the effective TokenProvider: an explicit
// Config.TokenProvider wins; otherwise a non-empty Config.Token is wrapped in
// a StaticTokenProvider; nil means unauthenticated requests.
func (c Config) tokenProvider() TokenProvider {
	if c.TokenProvider != nil {
		return c.TokenProvider
	}
	if c.Token != "" {
		return StaticTokenProvider(c.Token)
	}
	return nil
}

// FileReader reads repository files by path from GitHub.
type FileReader interface {
	ReadFile(ctx context.Context, path string) (content []byte, found bool, err error)
	ListDirectory(ctx context.Context, dirPath string) (entries []string, err error)
}

type githubFileReader struct {
	cfg    Config
	client *github.Client
}

func NewGitHubFileReader(cfg Config) (FileReader, error) {
	err := cfg.validate()
	if err != nil {
		return nil, err
	}
	client, err := newGitHubAPIClient(cfg)
	if err != nil {
		return nil, err
	}
	return &githubFileReader{cfg: cfg, client: client}, nil
}

func (r githubFileReader) ReadFile(ctx context.Context, path string) (content []byte, found bool, err error) {
	cleanPath := strings.TrimPrefix(path, "/")
	opts := github.RepositoryContentGetOptions{}
	if r.cfg.Ref != "" {
		opts.Ref = r.cfg.Ref
	}
	fileContent, _, resp, err := r.client.Repositories.GetContents(ctx, r.cfg.Owner, r.cfg.Repo, cleanPath, &opts)
	if err != nil {
		if isGitHubNotFound(err, resp) {
			return nil, false, nil
		}
		wrappedErr := wrapGitHubError(cleanPath, err, resp)
		return nil, false, wrappedErr
	}
	if fileContent == nil {
		return nil, false, fmt.Errorf("path is not a file: %s", cleanPath)
	}
	decodedContent, err := fileContent.GetContent()
	if err != nil {
		return nil, false, fmt.Errorf("failed to decode github file content: %w", err)
	}
	return []byte(decodedContent), true, nil
}

func isGitHubNotFound(err error, resp *github.Response) bool {
	var errResp *github.ErrorResponse
	if errors.As(err, &errResp) {
		if errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotFound {
			return true
		}
	}
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return true
	}
	return false
}

func (r githubFileReader) ListDirectory(ctx context.Context, dirPath string) (entries []string, err error) {
	cleanPath := strings.TrimPrefix(dirPath, "/")
	opts := github.RepositoryContentGetOptions{}
	if r.cfg.Ref != "" {
		opts.Ref = r.cfg.Ref
	}
	_, directoryContent, resp, err := r.client.Repositories.GetContents(ctx, r.cfg.Owner, r.cfg.Repo, cleanPath, &opts)
	if err != nil {
		if isGitHubNotFound(err, resp) {
			return nil, nil
		}
		wrappedErr := wrapGitHubError(cleanPath, err, resp)
		return nil, wrappedErr
	}
	result := make([]string, 0, len(directoryContent))
	for _, entry := range directoryContent {
		result = append(result, entry.GetName())
	}
	return result, nil
}

func (r githubFileReader) readFileWithSHA(ctx context.Context, filePath string) (content []byte, sha string, found bool, err error) {
	cleanPath := strings.TrimPrefix(filePath, "/")
	opts := github.RepositoryContentGetOptions{}
	if r.cfg.Ref != "" {
		opts.Ref = r.cfg.Ref
	}
	fileContent, _, resp, err := r.client.Repositories.GetContents(ctx, r.cfg.Owner, r.cfg.Repo, cleanPath, &opts)
	if err != nil {
		if isGitHubNotFound(err, resp) {
			return nil, "", false, nil
		}
		wrappedErr := wrapGitHubError(cleanPath, err, resp)
		return nil, "", false, wrappedErr
	}
	if fileContent == nil {
		return nil, "", false, fmt.Errorf("path is not a file: %s", cleanPath)
	}
	decodedContent, decodeErr := fileContent.GetContent()
	if decodeErr != nil {
		return nil, "", false, fmt.Errorf("failed to decode github file content: %w", decodeErr)
	}
	return []byte(decodedContent), fileContent.GetSHA(), true, nil
}

func (r githubFileReader) writeFile(ctx context.Context, filePath, message string, content []byte, sha string) error {
	cleanPath := strings.TrimPrefix(filePath, "/")
	opts := &github.RepositoryContentFileOptions{
		Message: &message,
		Content: content,
	}
	if r.cfg.Ref != "" {
		opts.Branch = &r.cfg.Ref
	}
	var err error
	if sha == "" {
		_, _, err = r.client.Repositories.CreateFile(ctx, r.cfg.Owner, r.cfg.Repo, cleanPath, opts)
	} else {
		opts.SHA = &sha
		_, _, err = r.client.Repositories.UpdateFile(ctx, r.cfg.Owner, r.cfg.Repo, cleanPath, opts)
	}
	if err != nil {
		wrappedErr := wrapGitHubError(cleanPath, err, nil)
		return wrappedErr
	}
	return nil
}

func (r githubFileReader) deleteFile(ctx context.Context, filePath, message, sha string) error {
	cleanPath := strings.TrimPrefix(filePath, "/")
	opts := &github.RepositoryContentFileOptions{
		Message: &message,
		SHA:     &sha,
	}
	if r.cfg.Ref != "" {
		opts.Branch = &r.cfg.Ref
	}
	_, _, err := r.client.Repositories.DeleteFile(ctx, r.cfg.Owner, r.cfg.Repo, cleanPath, opts)
	if err != nil {
		wrappedErr := wrapGitHubError(cleanPath, err, nil)
		return wrappedErr
	}
	return nil
}

func wrapGitHubError(path string, err error, resp *github.Response) error {
	var rateLimitErr *github.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return fmt.Errorf("github api rate limit exceeded while reading %q: %w", path, err)
	}
	var abuseErr *github.AbuseRateLimitError
	if errors.As(err, &abuseErr) {
		return fmt.Errorf("github api secondary rate limit while reading %q: %w", path, err)
	}
	var errResp *github.ErrorResponse
	if errors.As(err, &errResp) {
		if errResp.Response != nil {
			statusCode := errResp.Response.StatusCode
			if statusCode == http.StatusForbidden {
				return fmt.Errorf("github api forbidden while reading %q: %w", path, err)
			}
			return fmt.Errorf("github api error status %d while reading %q: %w", statusCode, path, err)
		}
	}
	if resp != nil {
		return fmt.Errorf("github api response status %d while reading %q: %w", resp.StatusCode, path, err)
	}
	return fmt.Errorf("github api request failed while reading %q: %w", path, err)
}
