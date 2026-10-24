package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

const maxBody = 16 * 1024

// decodeFields accepts only a single flat object of required string fields.
func decodeFields(w http.ResponseWriter, r *http.Request, names ...string) (map[string]string, bool) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		fail(w, 415, "application/json required")
		return nil, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, 413, "body too large")
		} else {
			fail(w, 400, "invalid body")
		}
		return nil, false
	}
	allowed := map[string]bool{}
	if !utf8.Valid(raw) {
		fail(w, 400, "invalid UTF-8")
		return nil, false
	}
	for _, n := range names {
		allowed[n] = true
	}
	out := map[string]string{}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		fail(w, 400, "object required")
		return nil, false
	}
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			fail(w, 400, "invalid JSON")
			return nil, false
		}
		key, ok := tok.(string)
		if !ok || !allowed[key] {
			fail(w, 400, "unknown field")
			return nil, false
		}
		if _, exists := out[key]; exists {
			fail(w, 400, "duplicate field")
			return nil, false
		}
		v, err := d.Token()
		str, ok := v.(string)
		if err != nil || !ok {
			fail(w, 400, "string field required")
			return nil, false
		}
		out[key] = str
	}
	if _, err = d.Token(); err != nil {
		fail(w, 400, "invalid JSON")
		return nil, false
	}
	if _, err = d.Token(); err != io.EOF {
		fail(w, 400, "trailing JSON")
		return nil, false
	}
	if len(out) != len(names) {
		fail(w, 400, "missing field")
		return nil, false
	}
	return out, true
}
func validKey(k string) bool {
	if len(k) < 1 || len(k) > 64 {
		return false
	}
	for i := range k {
		if k[i] < 32 || k[i] > 126 {
			return false
		}
	}
	return true
}
func mutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := r.Header.Values("Idempotency-Key")
	if len(v) != 1 || !validKey(v[0]) {
		fail(w, 400, "Idempotency-Key must be 1..64 printable ASCII bytes")
		return "", false
	}
	return v[0], true
}
