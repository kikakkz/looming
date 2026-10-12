// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e_test

// ociRegistry is the release-l1 §5 R4 registry fixture: a minimal OCI
// distribution endpoint (push + pull, anonymous, single-arch) standing
// in for ghcr so the artifact-only installer path runs end to end
// without touching the real registry. The suite PUBLISHES its built
// component images into it (fixture preparation — the e2e layer drives
// source builds; the installer under test never builds), then the
// installer's `looming apply` pulls the pinned refs back out of it.
// A hand-rolled fixture (rather than a registry:2 container) keeps the
// suite hermetic — no docker-hub dependency — and turns digest
// mismatches into loud test failures instead of silent layer reuse.
//
// Handlers answer error statuses instead of calling into the test:
// they run on the server's goroutines, where require.FailNow would be
// illegal (and a stalled client). Unexpected paths and mismatched
// digests land in failures, asserted empty at the end of the test.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registryAddr is the fixture's fixed listen address: the suite owns
// this port for the whole run (tests are serial) and nothing else may
// claim it.
const registryAddr = "0.0.0.0:15500"

// registryHost is the daemon-side name of the fixture registry:
// loopback is implicitly insecure for the docker daemon, so plain HTTP
// push/pull needs no daemon configuration.
const registryHost = "127.0.0.1:15500/looming"

// ociManifest is one stored manifest: the bytes the pusher uploaded
// plus the media type it declared (pulled back verbatim on GET).
type ociManifest struct {
	contentType string
	body        []byte
}

type ociRegistry struct {
	t *testing.T

	srv *http.Server
	mu  sync.Mutex
	// blobs are content-addressed across the whole fixture: the docker
	// daemon dedups layers across repositories, so sharing one store
	// mirrors engine behavior.
	blobs     map[string][]byte
	manifests map[string]ociManifest // "<repo>:<tag-or-digest>"
	uploads   map[string]*bytes.Buffer
	failures  []string
}

// newOCIRegistry starts the fixture on the fixed suite port and waits
// for its ping endpoint before returning — a registry the daemon
// cannot reach is a loud failure here, not a stalled pull later.
func newOCIRegistry(t *testing.T) *ociRegistry {
	t.Helper()
	r := &ociRegistry{
		t:         t,
		blobs:     map[string][]byte{},
		manifests: map[string]ociManifest{},
		uploads:   map[string]*bytes.Buffer{},
	}
	ln, err := net.Listen("tcp", registryAddr)
	require.NoError(t, err, "bind the registry fixture's fixed port")
	r.srv = &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		// Serve always returns a non-nil error (Close on cleanup);
		// the fixture never reports it as a failure.
		_ = r.srv.Serve(ln)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.srv.Shutdown(ctx)
	})

	waitFor(t, 30*time.Second, 200*time.Millisecond, "the registry fixture serving", func() bool {
		resp, err := http.Get("http://" + registryAddr + "/v2/")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusOK
	})
	return r
}

// fail records an internal fixture problem and answers the client with
// a 5xx so its command fails loudly too.
func (r *ociRegistry) fail(w http.ResponseWriter, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.t.Errorf("registry fixture: %s", msg)
	r.mu.Lock()
	r.failures = append(r.failures, msg)
	r.mu.Unlock()
	writeDistError(w, http.StatusInternalServerError, "FIXTURE_FAILURE")
}

// assertClean is the teardown assertion: every request the engine made
// matched the fixture's expectations (no unexpected route, no digest
// drift) — anything recorded in failures already failed the test via
// t.Errorf, this makes the count explicit.
func (r *ociRegistry) assertClean(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	assert.Empty(t, r.failures, "the registry fixture saw %d unexpected interaction(s)", len(r.failures))
}

// ServeHTTP dispatches the OCI distribution routes the docker engine
// exercises: ping, blob upload (POST/PATCH/PUT), blob download, and
// manifest get/put. Anything else is a loud 404 so an unexpected
// client path fails here, in the test, with the route named.
func (r *ociRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rest := strings.TrimPrefix(req.URL.Path, "/v2/")
	if rest == "" || rest == "/" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
		return
	}
	switch {
	case strings.HasSuffix(rest, "/blobs/uploads") || strings.HasSuffix(rest, "/blobs/uploads/"):
		repo := strings.TrimSuffix(strings.TrimSuffix(rest, "/blobs/uploads/"), "/blobs/uploads")
		r.startUpload(w, repo)
	case strings.Contains(rest, "/blobs/uploads/"):
		idx := strings.Index(rest, "/blobs/uploads/")
		repo, id := rest[:idx], rest[idx+len("/blobs/uploads/"):]
		switch req.Method {
		case http.MethodPatch:
			r.patchUpload(w, req, repo, id)
		case http.MethodPut:
			r.finishUpload(w, req, repo, id)
		default:
			http.Error(w, "unsupported upload method", http.StatusMethodNotAllowed)
		}
	case strings.Contains(rest, "/blobs/"):
		idx := strings.Index(rest, "/blobs/")
		r.serveBlob(w, req, rest[idx+len("/blobs/"):])
	case strings.Contains(rest, "/manifests/"):
		idx := strings.Index(rest, "/manifests/")
		repo, ref := rest[:idx], rest[idx+len("/manifests/"):]
		r.serveManifest(w, req, repo, ref)
	default:
		http.NotFound(w, req)
	}
}

// writeDistError answers with a distribution-style error envelope so
// the engine surfaces the code in its push/pull logs.
func writeDistError(w http.ResponseWriter, code int, distCode string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":%q}]}`, distCode, distCode)
}

func (r *ociRegistry) startUpload(w http.ResponseWriter, repo string) {
	if repo == "" {
		writeDistError(w, http.StatusNotFound, "NAME_UNKNOWN")
		return
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		r.fail(w, "mint upload id: %v", err)
		return
	}
	id := hex.EncodeToString(buf)

	r.mu.Lock()
	r.uploads[id] = &bytes.Buffer{}
	r.mu.Unlock()

	w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/"+id)
	w.Header().Set("Range", "0-0")
	w.WriteHeader(http.StatusAccepted)
}

func (r *ociRegistry) patchUpload(w http.ResponseWriter, req *http.Request, repo, id string) {
	r.mu.Lock()
	up, ok := r.uploads[id]
	r.mu.Unlock()
	if !ok {
		writeDistError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
		return
	}
	if _, err := io.Copy(up, req.Body); err != nil {
		r.fail(w, "accept a chunk of %s: %v", id, err)
		return
	}
	w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/"+id)
	w.Header().Set("Range", fmt.Sprintf("0-%d", up.Len()-1))
	w.WriteHeader(http.StatusAccepted)
}

func (r *ociRegistry) finishUpload(w http.ResponseWriter, req *http.Request, repo, id string) {
	r.mu.Lock()
	up, ok := r.uploads[id]
	r.mu.Unlock()
	if !ok {
		writeDistError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
		return
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, 1<<30))
	if err != nil {
		r.fail(w, "read the final chunk of %s: %v", id, err)
		return
	}
	full := make([]byte, 0, up.Len()+len(data))
	full = append(full, up.Bytes()...)
	full = append(full, data...)
	sum := sha256.Sum256(full)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	want := req.URL.Query().Get("digest")
	if want == "" {
		r.fail(w, "finalize %s without a digest query", id)
		return
	}
	if want != digest {
		r.fail(w, "blob %s digest drift: client declared %s, fixture received %s", id, want, digest)
		return
	}

	r.mu.Lock()
	r.blobs[digest] = full
	delete(r.uploads, id)
	r.mu.Unlock()

	w.Header().Set("Location", "/v2/"+repo+"/blobs/"+digest)
	w.WriteHeader(http.StatusCreated)
}

func (r *ociRegistry) serveBlob(w http.ResponseWriter, req *http.Request, digest string) {
	r.mu.Lock()
	data, ok := r.blobs[digest]
	r.mu.Unlock()
	if !ok {
		writeDistError(w, http.StatusNotFound, "BLOB_UNKNOWN")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	if req.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func (r *ociRegistry) serveManifest(w http.ResponseWriter, req *http.Request, repo, ref string) {
	key := repo + ":" + ref
	switch req.Method {
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<24))
		if err != nil {
			r.fail(w, "read the manifest for %s: %v", key, err)
			return
		}
		contentType := req.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/vnd.docker.distribution.manifest.v2+json"
		}
		sum := sha256.Sum256(body)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		r.mu.Lock()
		m := ociManifest{contentType: contentType, body: body}
		r.manifests[key] = m
		r.manifests[repo+":"+digest] = m
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet, http.MethodHead:
		r.mu.Lock()
		m, ok := r.manifests[key]
		r.mu.Unlock()
		if !ok {
			writeDistError(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
			return
		}
		sum := sha256.Sum256(m.body)
		w.Header().Set("Content-Type", m.contentType)
		w.Header().Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.body)))
		if req.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(m.body)
	default:
		http.Error(w, "unsupported manifest method", http.StatusMethodNotAllowed)
	}
}
