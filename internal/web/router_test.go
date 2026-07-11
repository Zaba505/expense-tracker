package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// testHandler is the real routing table, with logs thrown away.
func testHandler() http.Handler {
	return NewHandler(slog.New(slog.DiscardHandler))
}

// get runs a request against the real handler in-process, no listener.
func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, http.MethodGet, path)
}

func do(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	testHandler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	rec := get(t, "/healthz")

	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("GET /healthz body = %q, want %q", got, "ok")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("GET /healthz Content-Type = %q, want text/plain", got)
	}
}

// TestHealthz_RejectsNonGET pins the method to GET: the probe is a read,
// and ServeMux's "GET /healthz" pattern is what enforces it.
func TestHealthz_RejectsNonGET(t *testing.T) {
	t.Parallel()

	rec := do(t, http.MethodPost, "/healthz")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHome(t *testing.T) {
	t.Parallel()

	rec := get(t, "/")

	if rec.Code != http.StatusOK {
		t.Errorf("GET / status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", got)
	}
	if got := rec.Body.String(); !strings.Contains(got, "<!doctype html>") {
		t.Errorf("GET / body is not an HTML document:\n%s", got)
	}
}

// TestUnknownPathIs404 guards the "/{$}" root pattern: registered as a
// bare "/", the home page would match every unrouted path instead.
func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()

	rec := get(t, "/no-such-page")

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /no-such-page status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestStaticAssets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path            string
		wantContentType string
		wantInBody      string
	}{
		{path: "/static/app.css", wantContentType: "css", wantInBody: ".topbar"},
		{path: "/static/htmx.min.js", wantContentType: "javascript", wantInBody: "htmx"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			rec := get(t, tt.path)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", tt.path, rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tt.wantContentType) {
				t.Errorf("GET %s Content-Type = %q, want it to mention %q", tt.path, got, tt.wantContentType)
			}
			if got := rec.Body.String(); !strings.Contains(got, tt.wantInBody) {
				t.Errorf("GET %s body does not contain %q — wrong file served?", tt.path, tt.wantInBody)
			}
		})
	}
}

// assetRef finds every same-origin asset the page pulls in: the stylesheet
// href and the script src.
var assetRef = regexp.MustCompile(`(?:href|src)="(/static/[^"]+)"`)

// TestHomeAssetsAreServed is the drift test between the layout and the
// router. Renaming an asset, or moving where assets are mounted, breaks
// the page silently — the HTML still renders, it just arrives unstyled and
// without htmx. So: fetch what the page actually asks for and require a
// 200 for each.
func TestHomeAssetsAreServed(t *testing.T) {
	t.Parallel()

	home := get(t, "/").Body.String()

	refs := assetRef.FindAllStringSubmatch(home, -1)
	if len(refs) < 2 {
		t.Fatalf("home page references %d assets, want the stylesheet and htmx:\n%s", len(refs), home)
	}

	for _, ref := range refs {
		path := ref[1]
		if code := get(t, path).Code; code != http.StatusOK {
			t.Errorf("home page references %s, but GET %s = %d, want %d", path, path, code, http.StatusOK)
		}
	}
}
