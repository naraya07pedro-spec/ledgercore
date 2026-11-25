package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/naraya07pedro-spec/ledgercore/internal/ledger"
)

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return ledger.ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil || !utf8.Valid(raw) {
		return ledger.ErrInvalid
	}
	syntax := json.NewDecoder(bytes.NewReader(raw))
	syntax.UseNumber()
	if !validValue(syntax, 0) {
		return ledger.ErrInvalid
	}
	if _, err := syntax.Token(); err != io.EOF {
		return ledger.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return ledger.ErrInvalid
	}
	return nil
}

// Reject duplicate keys, including aliases the typed decoder matches by case.
func validValue(d *json.Decoder, depth int) bool {
	if depth > 8 {
		return false
	}
	value, err := d.Token()
	if err != nil {
		return false
	}
	switch value {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			folded := strings.ToLower(name)
			if err != nil || !ok || seen[folded] || !validValue(d, depth+1) {
				return false
			}
			seen[folded] = true
		}
		end, err := d.Token()
		return err == nil && end == json.Delim('}')
	case json.Delim('['):
		for d.More() {
			if !validValue(d, depth+1) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && end == json.Delim(']')
	default:
		return true
	}
}
