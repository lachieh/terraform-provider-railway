// Ported from railwayapp/cli src/iac/json.rs (MIT, Copyright (c) Railway Corp.)

package iac

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

func Field(value any, name string) any {
	if object, ok := value.(map[string]any); ok {
		return object[name]
	}
	return nil
}

func FieldStr(value any, name string) string {
	text, _ := Field(value, name).(string)
	return text
}

// FieldStrOK distinguishes a missing or non-string field from an empty string.
func FieldStrOK(value any, name string) (string, bool) {
	text, ok := Field(value, name).(string)
	return text, ok
}

// get is serde_json's Value::get: it reports whether value is an object that
// has the key, so a key holding null is present.
func get(value any, name string) (any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	child, ok := object[name]
	return child, ok
}

func isObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// setIndex is serde_json's `value[name] = child`: it writes into an object and
// turns null into a new object. Rust panics on any other value; this returns
// it unchanged.
func setIndex(value any, name string, child any) any {
	switch typed := value.(type) {
	case map[string]any:
		typed[name] = child
		return typed
	case nil:
		return map[string]any{name: child}
	default:
		return value
	}
}

// asI64 is serde_json's Value::as_i64 for a decoded float64: only integral
// numbers in range convert.
func asI64(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || number < -9.223372036854775808e18 || number >= 9.223372036854775808e18 {
		return 0, false
	}
	return int64(number), true
}

// valueEqual is serde_json's Value equality, with numbers compared by value.
func valueEqual(left, right any) bool {
	return StableStringify(left) == StableStringify(right)
}

func SortedKeys[V any](object map[string]V) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// StableStringify renders value as compact JSON with object keys sorted, byte
// for byte as serde_json::to_string does: no HTML escaping, and integral
// numbers without a fraction.
func StableStringify(value any) string {
	var out strings.Builder
	writeJSON(&out, value)
	return out.String()
}

func writeJSON(out *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case float64:
		out.WriteString(formatNumber(typed))
	case int:
		out.WriteString(strconv.Itoa(typed))
	case int64:
		out.WriteString(strconv.FormatInt(typed, 10))
	case string:
		writeJSONString(out, typed)
	case []any:
		out.WriteByte('[')
		for index, child := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			writeJSON(out, child)
		}
		out.WriteByte(']')
	case []string:
		out.WriteByte('[')
		for index, child := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			writeJSONString(out, child)
		}
		out.WriteByte(']')
	case map[string]any:
		out.WriteByte('{')
		for index, key := range SortedKeys(typed) {
			if index > 0 {
				out.WriteByte(',')
			}
			writeJSONString(out, key)
			out.WriteByte(':')
			writeJSON(out, typed[key])
		}
		out.WriteByte('}')
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			out.WriteString("null")
			return
		}
		var decoded any
		if json.Unmarshal(encoded, &decoded) != nil {
			out.WriteString("null")
			return
		}
		writeJSON(out, decoded)
	}
}

func writeJSONString(out *strings.Builder, text string) {
	const hex = "0123456789abcdef"
	out.WriteByte('"')
	for index := 0; index < len(text); index++ {
		char := text[index]
		switch char {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if char < 0x20 {
				out.WriteString(`\u00`)
				out.WriteByte(hex[char>>4])
				out.WriteByte(hex[char&0xf])
			} else {
				out.WriteByte(char)
			}
		}
	}
	out.WriteByte('"')
}

// formatNumber renders a decoded JSON number the way serde_json printed the
// original: integers as integers, anything else in ryu's shortest form.
func formatNumber(number float64) string {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "null"
	}
	if number == math.Trunc(number) && number >= -9.223372036854775808e18 && number < 1.8446744073709551616e19 {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	scientific := strconv.FormatFloat(math.Abs(number), 'e', -1, 64)
	mantissa, exponentText, _ := strings.Cut(scientific, "e")
	exponent, _ := strconv.Atoi(exponentText)
	digits := strings.Replace(mantissa, ".", "", 1)
	sign := ""
	if number < 0 {
		sign = "-"
	}
	length := len(digits)
	point := exponent + 1
	switch {
	case point >= length && point <= 16:
		return sign + digits + strings.Repeat("0", point-length) + ".0"
	case 0 < point && point <= 16:
		return sign + digits[:point] + "." + digits[point:]
	case -5 < point && point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case length == 1:
		return sign + digits + "e" + strconv.Itoa(point-1)
	default:
		return sign + digits[:1] + "." + digits[1:] + "e" + strconv.Itoa(point-1)
	}
}

// Clone deep-copies a decoded JSON value.
func Clone(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = Clone(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = Clone(child)
		}
		return out
	case []string:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = child
		}
		return out
	default:
		return value
	}
}

func CloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	return Clone(value).(map[string]any)
}

// PruneEmpty drops nulls and empty objects, except the empty objects keyed
// under customDomains, serviceDomains and tcpProxies, which mean "exists with
// default settings".
func PruneEmpty(value any) any { return pruneEmptyAt(value, "") }

func pruneEmptyAt(value any, parent string) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = pruneEmptyAt(child, parent)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for _, key := range SortedKeys(typed) {
			child := pruneEmptyAt(typed[key], key)
			if child == nil {
				continue
			}
			if inner, ok := child.(map[string]any); ok && len(inner) == 0 {
				switch parent {
				case "customDomains", "serviceDomains", "tcpProxies":
				default:
					continue
				}
			}
			out[key] = child
		}
		return out
	default:
		return value
	}
}

// pruneEmptyMap is PruneEmpty for a value known to be an object.
func pruneEmptyMap(value map[string]any) map[string]any {
	return PruneEmpty(value).(map[string]any)
}

// MergeObjects shallow-merges extra over base when both are objects;
// otherwise extra wins.
func MergeObjects(base, extra any) any {
	left, leftOK := base.(map[string]any)
	right, rightOK := extra.(map[string]any)
	if !leftOK || !rightOK {
		return extra
	}
	out := CloneMap(left)
	for key, value := range right {
		out[key] = value
	}
	return out
}
