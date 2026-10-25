package webui

import (
	"io"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func get(t *testing.T, path string) (int, string, string, string) {
	t.Helper()
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	body, _ := io.ReadAll(w.Result().Body)
	return w.Code, w.Header().Get("Content-Type"), w.Header().Get("Content-Security-Policy"), string(body)
}

func TestServesThePageWithSecurityHeaders(t *testing.T) {
	code, ctype, csp, body := get(t, "/")
	if code != 200 || !strings.HasPrefix(ctype, "text/html") || !strings.Contains(body, "/ui/app.js") {
		t.Fatalf("GET / = %d %q; want the HTML page", code, ctype)
	}
	for _, rule := range []string{"script-src 'self'", "frame-ancestors 'none'", "connect-src 'self'"} {
		if !strings.Contains(csp, rule) {
			t.Errorf("CSP %q lacks %q", csp, rule)
		}
	}
	// The CSP forbids inline scripts, so the page must not contain any:
	// every <script> needs a src attribute.
	if inline := regexp.MustCompile(`<script(?:[^>]*)>`).FindAllString(body, -1); len(inline) == 0 {
		t.Error("no script tag found")
	} else {
		for _, tag := range inline {
			if !strings.Contains(tag, "src=") {
				t.Errorf("inline script %q would be blocked by the CSP", tag)
			}
		}
	}
}

func TestServesAssetsAndNothingElse(t *testing.T) {
	if code, ctype, _, body := get(t, "/ui/app.js"); code != 200 || !strings.Contains(ctype, "javascript") || !strings.Contains(body, "textContent") {
		t.Errorf("app.js: %d %q", code, ctype)
	}
	if code, ctype, _, _ := get(t, "/ui/app.css"); code != 200 || !strings.HasPrefix(ctype, "text/css") {
		t.Errorf("app.css: %d %q", code, ctype)
	}
	if code, _, _, _ := get(t, "/ui/missing.js"); code != 404 {
		t.Errorf("missing asset: %d, want 404", code)
	}
}

// TestNoInnerHTML enforces the client's rule: text is only ever written with
// textContent, so nothing from the server can be injected as HTML.
func TestNoInnerHTML(t *testing.T) {
	_, _, _, js := get(t, "/ui/app.js")
	// Look for the CODE that parses HTML, not the words in comments.
	for _, sink := range []string{".innerHTML", ".outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(js, sink) {
			t.Errorf("app.js uses %s; use textContent and createElement", sink)
		}
	}
}
