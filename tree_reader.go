package dalgo2ghingitdb

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"context"

	"github.com/google/go-github/v88/github"
)

// treeReader reads repository file content through the Git Data API
// (branch head → recursive tree → blob by SHA) instead of the Contents API
// (Repositories.GetContents).
//
// Why: GitHub's Contents API is eventually consistent — a read issued
// immediately after a commit can return stale content or a spurious 404. The
// Git Data API resolves the branch's current head commit, its tree, and the
// blob by SHA, which is immediately consistent with commits. The BatchingGitHubDB
// commits via the Git tree API and the conformance suite does tight
// write-then-read, so the batching variant's reads must go through this reader.
//
// treeReader embeds a *TreeWriter purely to reuse its resolveBranch /
// headTreeForBranch head-resolution helpers and its *github.Client; it performs
// no writes.
//
// head is a read-your-writes hint: after a local commit the BatchingGitHubDB
// records the new commit SHA here, and reads resolve the tree from that commit
// (all fetches by SHA, which are immediately consistent) instead of via
// Git.GetRef. GitHub's ref→SHA lookup is briefly eventually-consistent after an
// UpdateRef, so a read issued immediately after a write can otherwise resolve
// the pre-commit head. This is correct under inGitDB's single-writer contract
// (ovdb is the sole writer of the branch); if another client advances the
// branch, the hint may lag until the next local commit.
type treeReader struct {
	writer *TreeWriter
	head   *sharedHead
}

// sharedHead holds the last locally-committed commit SHA, guarded for
// concurrent reads and the write that updates it after a commit.
type sharedHead struct {
	mu  sync.Mutex
	sha string
}

func (h *sharedHead) set(sha string) {
	h.mu.Lock()
	h.sha = sha
	h.mu.Unlock()
}

func (h *sharedHead) get() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sha
}

func newTreeReader(cfg Config) (*treeReader, error) {
	w, err := NewTreeWriter(cfg)
	if err != nil {
		return nil, err
	}
	return &treeReader{writer: w, head: &sharedHead{}}, nil
}

// headTreeSHA resolves the tree SHA to read from. When a locally-committed head
// SHA is known it fetches that commit's tree directly (by SHA, immediately
// consistent); otherwise it falls back to resolving the branch ref.
func (r *treeReader) headTreeSHA(ctx context.Context) (string, error) {
	if r.head != nil {
		if sha := r.head.get(); sha != "" {
			commit, _, err := r.writer.client.Git.GetCommit(ctx, r.writer.cfg.Owner, r.writer.cfg.Repo, sha)
			if err != nil {
				return "", wrapGitHubError("GetCommit "+sha, err, nil)
			}
			return commit.GetTree().GetSHA(), nil
		}
	}
	treeSHA, _, err := r.writer.headTree(ctx)
	return treeSHA, err
}

// readFile resolves the current head tree (recursively), finds the blob at
// cleanPath, fetches its content via Git.GetBlob, and base64-decodes it.
// A missing path returns (nil, false, nil) — not an error.
func (r *treeReader) readFile(ctx context.Context, path string) (content []byte, found bool, err error) {
	cleanPath := strings.TrimPrefix(path, "/")

	treeSHA, err := r.headTreeSHA(ctx)
	if err != nil {
		return nil, false, err
	}
	tree, _, treeErr := r.writer.client.Git.GetTree(ctx, r.writer.cfg.Owner, r.writer.cfg.Repo, treeSHA, true)
	if treeErr != nil {
		return nil, false, wrapGitHubError(treeSHA, treeErr, nil)
	}
	if tree.GetTruncated() {
		return nil, false, fmt.Errorf("repository tree at %s is truncated; ingitdb does not support consistent reads on trees larger than the github api limit", treeSHA)
	}

	var blobSHA string
	for _, entry := range tree.Entries {
		if entry.GetType() != "blob" {
			continue
		}
		if entry.GetPath() == cleanPath {
			blobSHA = entry.GetSHA()
			break
		}
	}
	if blobSHA == "" {
		return nil, false, nil // missing path is not an error
	}

	blob, _, blobErr := r.writer.client.Git.GetBlob(ctx, r.writer.cfg.Owner, r.writer.cfg.Repo, blobSHA)
	if blobErr != nil {
		return nil, false, wrapGitHubError(cleanPath, blobErr, nil)
	}
	decoded, decodeErr := decodeBlobContent(blob)
	if decodeErr != nil {
		return nil, false, decodeErr
	}
	return decoded, true, nil
}

// decodeBlobContent decodes a Git blob's content. GitHub returns blob content
// base64-encoded (encoding == "base64"); an unexpected encoding is an error.
func decodeBlobContent(blob *github.Blob) ([]byte, error) {
	if blob == nil {
		return nil, fmt.Errorf("github returned a nil blob")
	}
	switch blob.GetEncoding() {
	case "base64", "":
		// GitHub uses base64 for blobs; the base64 payload may contain newlines.
		raw := strings.ReplaceAll(blob.GetContent(), "\n", "")
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("failed to base64-decode github blob content: %w", err)
		}
		return decoded, nil
	case "utf-8":
		return []byte(blob.GetContent()), nil
	default:
		return nil, fmt.Errorf("unsupported github blob encoding %q", blob.GetEncoding())
	}
}
