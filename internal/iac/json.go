package iac

import (
	"encoding/json"
	"sort"
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

func SortedKeys[V any](object map[string]V) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// StableStringify renders value as compact JSON with object keys sorted.
// encoding/json already sorts map keys, so this is plain marshalling that
// never fails.
func StableStringify(value any) string {
	out, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(out)
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
