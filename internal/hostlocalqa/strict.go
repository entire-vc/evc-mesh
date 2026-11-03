package hostlocalqa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrInvalid marks a body that is not exactly the frozen wire shape.
var ErrInvalid = errors.New("invalid host-local QA body")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

const maxDepth = 6

// parseObject decodes one JSON object and refuses duplicate keys at every depth
// and trailing data. Numbers stay json.Number so that bool, string and float
// are never mistaken for an integer. Go's own struct decoding would match keys
// case-insensitively and ignore unknown ones, so it is deliberately not used.
func parseObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return nil, invalid("%v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("trailing data after the JSON object")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, invalid("a JSON object is required")
	}
	return m, nil
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("nesting is too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	switch d {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := kt.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			if _, dup := obj[key]; dup {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			if obj[key], err = decodeValue(dec, depth+1); err != nil {
				return nil, err
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := decodeValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return arr, nil
	}
	return nil, errors.New("unexpected delimiter")
}

// exactKeys requires m to have exactly the listed keys.
func exactKeys(m map[string]any, where string, keys ...string) error {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
		if _, ok := m[k]; !ok {
			return invalid("%s.%s is required", where, k)
		}
	}
	for k := range m {
		if !want[k] {
			return invalid("%s.%s is not allowed", where, k)
		}
	}
	return nil
}

var (
	uuidRE   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	intRE    = regexp.MustCompile(`^\d{1,18}$`)
	ttyRE    = regexp.MustCompile(`^/dev/ttys[0-9A-Za-z]{1,16}$`)
)

func str(m map[string]any, key string) (string, error) {
	s, ok := m[key].(string)
	if !ok {
		return "", invalid("%s must be a string", key)
	}
	return s, nil
}

func bounded(m map[string]any, key string, limit int) (string, error) {
	s, err := str(m, key)
	if err != nil {
		return "", err
	}
	if s == "" || len(s) > limit || !utf8.ValidString(s) {
		return "", invalid("%s must be 1..%d bytes of UTF-8", key, limit)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", invalid("%s has a control character", key)
		}
	}
	return s, nil
}

func uuidField(m map[string]any, key string) (uuid.UUID, error) {
	s, err := str(m, key)
	if err != nil {
		return uuid.Nil, err
	}
	return parseUUID(key, s)
}

func parseUUID(key, s string) (uuid.UUID, error) {
	if !uuidRE.MatchString(s) {
		return uuid.Nil, invalid("%s must be a canonical lowercase UUID", key)
	}
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, invalid("%s must be a non-nil UUID", key)
	}
	return id, nil
}

func hashField(m map[string]any, key string) (string, error) {
	s, err := str(m, key)
	if err != nil {
		return "", err
	}
	if !sha256RE.MatchString(s) {
		return "", invalid("%s must be 64 lowercase hex characters", key)
	}
	return s, nil
}

// intField accepts only a JSON integer literal: a bool, a string, a float or an
// exponent form is refused, and so is a negative or oversized number.
func intField(m map[string]any, key string) (int64, error) {
	n, ok := m[key].(json.Number)
	if !ok || !intRE.MatchString(n.String()) {
		return 0, invalid("%s must be a non-negative integer", key)
	}
	v, err := n.Int64()
	if err != nil {
		return 0, invalid("%s must be a non-negative integer", key)
	}
	return v, nil
}

func positive(m map[string]any, key string) (int64, error) {
	v, err := intField(m, key)
	if err != nil {
		return 0, err
	}
	if v < 1 {
		return 0, invalid("%s must be positive", key)
	}
	return v, nil
}

func object(m map[string]any, key string) (map[string]any, error) {
	o, ok := m[key].(map[string]any)
	if !ok {
		return nil, invalid("%s must be an object", key)
	}
	return o, nil
}

func constInt(m map[string]any, key string, want int64) error {
	v, err := intField(m, key)
	if err != nil {
		return err
	}
	if v != want {
		return invalid("%s does not match the frozen contract", key)
	}
	return nil
}

func constStr(m map[string]any, key, want string) error {
	s, err := str(m, key)
	if err != nil {
		return err
	}
	if s != want {
		return invalid("%s does not match the frozen contract", key)
	}
	return nil
}
