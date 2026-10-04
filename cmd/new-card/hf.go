// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	apiResponseLimit = 32 << 20
	smallFileLimit   = 32 << 20
	smallFileBudget  = 128 << 20
	treePageLimit    = 50
	requestTimeout   = 60 * time.Second
)

var (
	commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
	sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	repoSegment   = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
	// Mirrors the runtime pack hf_subdir component rule: '.' and '..' are never components.
	pathComponent = regexp.MustCompile(`^(?:[A-Za-z0-9_-][A-Za-z0-9._-]*|\.[A-Za-z0-9_-][A-Za-z0-9._-]*|\.\.[A-Za-z0-9._-]+)$`)
	nextLink      = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)
)

// fetcher performs bounded HTTPS requests. The Hugging Face token is sent only
// to hfBase and is never part of a URL or an error message.
type fetcher struct {
	client    *http.Client
	hfBase    string
	githubAPI string
	hfToken   string
	budget    int64
}

func newHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing non-HTTPS redirect to %s", req.URL.Redacted())
			}
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func (f *fetcher) get(ctx context.Context, rawURL, accept string, limit int64) ([]byte, http.Header, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil, nil, fmt.Errorf("refusing non-HTTPS URL %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if f.hfToken != "" && strings.HasPrefix(rawURL, f.hfBase+"/") {
		req.Header.Set("Authorization", "Bearer "+f.hfToken)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("GET %s: %s", u.Redacted(), resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("GET %s: %w", u.Redacted(), err)
	}
	if int64(len(data)) > limit {
		return nil, nil, fmt.Errorf("GET %s: response exceeds %d bytes", u.Redacted(), limit)
	}
	return data, resp.Header, nil
}

// hfRef names a Hugging Face repository location. file is set for blob URLs.
type hfRef struct {
	repo     string
	revision string
	dir      string
	file     string
}

func validRelativePath(p string) bool {
	if p == "" {
		return false
	}
	for _, component := range strings.Split(p, "/") {
		if !pathComponent.MatchString(component) {
			return false
		}
	}
	return true
}

func validRepo(repo string) bool {
	org, name, found := strings.Cut(repo, "/")
	return found && repoSegment.MatchString(org) && repoSegment.MatchString(name) &&
		!strings.Contains(name, "..") && !strings.Contains(org, "..")
}

// parseHFURL accepts https://huggingface.co/<org>/<repo>, …/tree/<rev>[/<dir>]
// and …/blob/<rev>/<file>.
func parseHFURL(raw string) (hfRef, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return hfRef{}, err
	}
	if u.Scheme != "https" || u.Host != "huggingface.co" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return hfRef{}, fmt.Errorf("source must be an https://huggingface.co/<org>/<repo> URL without query or fragment")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || !validRepo(parts[0]+"/"+parts[1]) {
		return hfRef{}, fmt.Errorf("invalid Hugging Face repository in %q", raw)
	}
	ref := hfRef{repo: parts[0] + "/" + parts[1], revision: "main"}
	switch {
	case len(parts) == 2:
	case parts[2] == "tree" && len(parts) >= 4:
		ref.revision = parts[3]
		ref.dir = strings.Join(parts[4:], "/")
		if ref.dir != "" && !validRelativePath(ref.dir) {
			return hfRef{}, fmt.Errorf("invalid subdirectory %q", ref.dir)
		}
	case parts[2] == "blob" && len(parts) >= 5:
		ref.revision = parts[3]
		ref.file = strings.Join(parts[4:], "/")
		if !validRelativePath(ref.file) {
			return hfRef{}, fmt.Errorf("invalid file path %q", ref.file)
		}
		if dir := path.Dir(ref.file); dir != "." {
			ref.dir = dir
		}
	default:
		return hfRef{}, fmt.Errorf("unsupported Hugging Face URL form %q; use the repository, /tree/<rev>/<dir>, or /blob/<rev>/<file>", raw)
	}
	if ref.revision == "" {
		return hfRef{}, fmt.Errorf("empty revision in %q", raw)
	}
	return ref, nil
}

type ggufInfo struct {
	Total         int64  `json:"total"`
	Architecture  string `json:"architecture"`
	ContextLength int64  `json:"context_length"`
	ChatTemplate  string `json:"chat_template"`
}

type modelInfo struct {
	SHA         string                 `json:"sha"`
	Tags        []string               `json:"tags"`
	PipelineTag string                 `json:"pipeline_tag"`
	LibraryName string                 `json:"library_name"`
	Config      map[string]any         `json:"config"`
	GGUF        *ggufInfo              `json:"gguf"`
	Safetensors *struct{ Total int64 } `json:"safetensors"`
	CardData    struct {
		License   json.RawMessage `json:"license"`
		BaseModel json.RawMessage `json:"base_model"`
	} `json:"cardData"`
}

type treeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

// snapshot is one directory of a repository pinned at an exact commit.
type snapshot struct {
	ref     hfRef
	sha     string
	info    modelInfo
	files   []treeEntry
	content map[string][]byte
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (f *fetcher) resolve(ctx context.Context, ref hfRef) (*snapshot, error) {
	infoURL := fmt.Sprintf("%s/api/models/%s/revision/%s?expand[]=sha&expand[]=tags&expand[]=pipeline_tag&expand[]=library_name&expand[]=config&expand[]=gguf&expand[]=safetensors&expand[]=cardData",
		f.hfBase, escapePath(ref.repo), url.PathEscape(ref.revision))
	data, _, err := f.get(ctx, infoURL, "application/json", apiResponseLimit)
	if err != nil {
		return nil, fmt.Errorf("resolve %s@%s: %w", ref.repo, ref.revision, err)
	}
	s := &snapshot{ref: ref, content: make(map[string][]byte)}
	if err := json.Unmarshal(data, &s.info); err != nil {
		return nil, fmt.Errorf("resolve %s@%s: %w", ref.repo, ref.revision, err)
	}
	s.sha = s.info.SHA
	if !commitPattern.MatchString(s.sha) {
		return nil, fmt.Errorf("resolve %s@%s: API returned no exact commit SHA", ref.repo, ref.revision)
	}
	if commitPattern.MatchString(ref.revision) && ref.revision != s.sha {
		return nil, fmt.Errorf("resolve %s@%s: API resolved a different commit %s", ref.repo, ref.revision, s.sha)
	}
	treeURL := fmt.Sprintf("%s/api/models/%s/tree/%s", f.hfBase, escapePath(ref.repo), s.sha)
	if ref.dir != "" {
		treeURL += "/" + escapePath(ref.dir)
	}
	treeURL += "?expand=true"
	for page := 0; treeURL != ""; page++ {
		if page == treePageLimit {
			return nil, fmt.Errorf("tree of %s exceeds %d pages", ref.repo, treePageLimit)
		}
		data, header, err := f.get(ctx, treeURL, "application/json", apiResponseLimit)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", ref.repo, err)
		}
		var entries []treeEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("list %s: %w", ref.repo, err)
		}
		for _, e := range entries {
			if e.Type != "file" {
				continue
			}
			if !validRelativePath(e.Path) || path.Dir(e.Path) != dirOrDot(ref.dir) {
				return nil, fmt.Errorf("list %s: unexpected tree path %q", ref.repo, e.Path)
			}
			s.files = append(s.files, e)
		}
		treeURL = ""
		if m := nextLink.FindStringSubmatch(header.Get("Link")); m != nil {
			if !strings.HasPrefix(m[1], f.hfBase+"/") {
				return nil, fmt.Errorf("list %s: refusing pagination link outside %s", ref.repo, f.hfBase)
			}
			treeURL = m[1]
		}
	}
	if len(s.files) == 0 {
		return nil, fmt.Errorf("%s@%s has no files under %q", ref.repo, s.sha, ref.dir)
	}
	return s, nil
}

func dirOrDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

func (s *snapshot) entry(name string) (treeEntry, bool) {
	full := name
	if s.ref.dir != "" {
		full = s.ref.dir + "/" + name
	}
	for _, e := range s.files {
		if e.Path == full {
			return e, true
		}
	}
	return treeEntry{}, false
}

// readSmall downloads a non-LFS file and proves it against the tree's size and
// git blob ID. A missing file returns nil without error.
func (f *fetcher) readSmall(ctx context.Context, s *snapshot, e treeEntry) ([]byte, error) {
	if data, found := s.content[e.Path]; found {
		return data, nil
	}
	if e.LFS != nil {
		return nil, fmt.Errorf("%s is stored in LFS; refusing to download weights", e.Path)
	}
	if e.Size > smallFileLimit || f.budget+e.Size > smallFileBudget {
		return nil, fmt.Errorf("%s exceeds the %d-byte download budget", e.Path, smallFileBudget)
	}
	fileURL := fmt.Sprintf("%s/%s/resolve/%s/%s", f.hfBase, escapePath(s.ref.repo), s.sha, escapePath(e.Path))
	data, _, err := f.get(ctx, fileURL, "", smallFileLimit)
	if err != nil {
		return nil, err
	}
	f.budget += int64(len(data))
	blob := sha1.New()
	fmt.Fprintf(blob, "blob %d\x00", len(data))
	blob.Write(data)
	if int64(len(data)) != e.Size || hex.EncodeToString(blob.Sum(nil)) != e.OID {
		return nil, fmt.Errorf("%s does not match its pinned tree size and git blob ID", e.Path)
	}
	s.content[e.Path] = data
	return data, nil
}

// readNamed reads a small file in the snapshot directory; absent files return nil.
func (f *fetcher) readNamed(ctx context.Context, s *snapshot, name string) ([]byte, error) {
	e, found := s.entry(name)
	if !found {
		return nil, nil
	}
	return f.readSmall(ctx, s, e)
}

// pinFile returns a file's SHA-256 and size: the LFS object ID for LFS files,
// otherwise the hash of the verified downloaded bytes.
func (f *fetcher) pinFile(ctx context.Context, s *snapshot, e treeEntry) (cardFile, error) {
	if e.LFS != nil {
		if !sha256Pattern.MatchString(e.LFS.OID) || e.LFS.Size < 0 {
			return cardFile{}, fmt.Errorf("%s has no valid LFS SHA-256", e.Path)
		}
		return cardFile{Path: e.Path, SHA256: e.LFS.OID, Bytes: e.LFS.Size}, nil
	}
	data, err := f.readSmall(ctx, s, e)
	if err != nil {
		return cardFile{}, err
	}
	sum := sha256.Sum256(data)
	return cardFile{Path: e.Path, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}, nil
}

// githubHead pins the current default-branch commit of a GitHub repository.
func (f *fetcher) githubHead(ctx context.Context, sourceURL string) (string, error) {
	repo := strings.TrimPrefix(sourceURL, "https://github.com/")
	if repo == sourceURL || !validRepo(repo) {
		return "", fmt.Errorf("cannot resolve engine revision for %s; pass --engine-revision", sourceURL)
	}
	data, _, err := f.get(ctx, fmt.Sprintf("%s/repos/%s/commits/HEAD", f.githubAPI, repo), "application/vnd.github.sha", 4096)
	if err != nil {
		return "", fmt.Errorf("resolve engine revision: %w", err)
	}
	sha := strings.TrimSpace(string(data))
	if !commitPattern.MatchString(sha) {
		return "", fmt.Errorf("resolve engine revision: GitHub returned no commit SHA for %s", repo)
	}
	return sha, nil
}
