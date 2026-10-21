// Package podtemplate compares Pod templates to decide whether a sync will roll pods.
package podtemplate

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// SubsetChanged reports whether desired introduces a template change relative to
// live. Every field set in desired must equal the same field in live; fields
// that exist only in live (server defaults) are ignored.
//
// Fail-closed: a nil desired with non-nil live, or nil live with non-nil desired,
// counts as changed. Both nil → unchanged.
func SubsetChanged(desired, live map[string]any) bool {
	if desired == nil && live == nil {
		return false
	}
	if desired == nil || live == nil {
		return true
	}
	return !subsetEqual(desired, live)
}

// DeepEqual reports whether two templates are equal after scalar normalization
// (int/float) so server-side dry-run output can be compared to the live object.
func DeepEqual(a, b map[string]any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return reflect.DeepEqual(normalizeValue(a), normalizeValue(b))
}

func subsetEqual(desired, live any) bool {
	desired = normalizeScalar(desired)
	live = normalizeScalar(live)
	switch d := desired.(type) {
	case map[string]any:
		lm, ok := asMap(live)
		if !ok {
			return false
		}
		for k, dv := range d {
			lv, ok := lm[k]
			if !ok {
				return false
			}
			if !subsetEqual(dv, lv) {
				return false
			}
		}
		return true
	case []any:
		ll, ok := asSlice(live)
		if !ok {
			return false
		}
		if len(d) != len(ll) {
			return false
		}
		for i := range d {
			if !subsetEqual(d[i], ll[i]) {
				return false
			}
		}
		return true
	default:
		return scalarEqual(desired, live)
	}
}

func asMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	default:
		return nil, false
	}
}

func asSlice(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	default:
		return nil, false
	}
}

func scalarEqual(a, b any) bool {
	a = normalizeScalar(a)
	b = normalizeScalar(b)
	if a == nil && b == nil {
		return true
	}
	// JSON numbers / YAML ints: compare via Sprint after normalize.
	if reflect.TypeOf(a) != nil && reflect.TypeOf(b) != nil {
		switch a.(type) {
		case int64, float64:
			switch b.(type) {
			case int64, float64:
				return fmt.Sprint(a) == fmt.Sprint(b)
			}
		}
	}
	return reflect.DeepEqual(a, b)
}

func normalizeScalar(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case float32:
		f := float64(t)
		if f == float64(int64(f)) {
			return int64(f)
		}
		return f
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return string(t)
	default:
		return v
	}
}

func normalizeValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = normalizeValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeValue(val)
		}
		return out
	default:
		return normalizeScalar(t)
	}
}

// CloneMap deep-copies a generic JSON/YAML map tree.
func CloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := normalizeValue(deepCopyRaw(m)).(map[string]any)
	return out
}

func deepCopyRaw(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepCopyRaw(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = deepCopyRaw(val)
		}
		return out
	default:
		return t
	}
}

// sanitizeTemplateMeta drops volatile pod-template metadata that differs between
// live gets and server-side dry-run apply results without meaning a rollout.
func sanitizeTemplateMeta(tpl map[string]any) map[string]any {
	if tpl == nil {
		return nil
	}
	out := CloneMap(tpl)
	if meta, ok := out["metadata"].(map[string]any); ok {
		for _, k := range []string{
			"creationTimestamp", "resourceVersion", "uid", "generation",
			"managedFields", "selfLink", "ownerReferences",
		} {
			delete(meta, k)
		}
		if len(meta) == 0 {
			delete(out, "metadata")
		}
	}
	return out
}

// EvidenceAttrs returns optional human-readable image/env summaries for reports.
func EvidenceAttrs(template map[string]any) map[string]any {
	if template == nil {
		return nil
	}
	tspec, _ := template["spec"].(map[string]any)
	if tspec == nil {
		return nil
	}
	out := map[string]any{}
	if imgs := imagesFingerprint(tspec); imgs != "" {
		out["containerImages"] = imgs
	}
	if env := envFingerprint(tspec); env != "" {
		out["containerEnv"] = env
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
