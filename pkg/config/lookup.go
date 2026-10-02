package config

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Lookup resolves a dotted path such as "pricing.prompt" against decoded JSON.
// An empty path always reports missing, which lets a catalog describe a field a
// provider simply does not publish.
func Lookup(v any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// LookupString reads a string field, or "" when it is absent or not a string.
func LookupString(v any, path string) string {
	got, ok := Lookup(v, path)
	if !ok {
		return ""
	}
	s, _ := got.(string)
	return s
}

// LookupInt reads an integer field, tolerating the string-encoded numbers some
// providers use. It returns 0 when the field is absent or unparsable.
func LookupInt(v any, path string) int64 {
	got, ok := Lookup(v, path)
	if !ok {
		return 0
	}
	return ToInt64(got)
}

// LookupFloat reads a price. Providers publish these as strings ("0") or as
// numbers (0), so both have to be accepted.
func LookupFloat(v any, path string) (float64, bool) {
	got, ok := Lookup(v, path)
	if !ok {
		return 0, false
	}
	switch n := got.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// ToInt64 coerces a decoded JSON scalar to an integer, returning 0 on failure.
func ToInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return i
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0
		}
		return i
	default:
		return 0
	}
}
