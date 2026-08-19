// Package drift compares a desired object against the live one.
//
// This exists only so that drift can be *reported*. It plays no part in
// correcting it: server-side apply does that on its own, without ever reading
// the live object. Detection and correction are two separate jobs, and mixing
// them up is what makes the read in the reconcile loop look redundant.
package drift

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// ignored holds paths that are never ours to manage. Comparing them would
// report drift on every single object, forever.
var ignored = map[string]bool{
	"status":                     true,
	"metadata.creationTimestamp": true,
	"metadata.resourceVersion":   true,
	"metadata.uid":               true,
	"metadata.generation":        true,
	"metadata.managedFields":     true,
	"metadata.selfLink":          true,
}

// Fields returns the dotted paths present in desired whose value differs from
// live, sorted.
//
// Only fields asserted in desired are considered. The API server defaults
// dozens of fields nobody wrote down, and those are not drift: a field we
// never declared is not a field we own.
func Fields(desired, live map[string]any) []string {
	var out []string
	walkMap("", desired, live, &out)
	slices.Sort(out)
	return out
}

func walkMap(prefix string, desired, live map[string]any, out *[]string) {
	for key, want := range desired {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if ignored[path] {
			continue
		}

		got, present := live[key]
		if !present {
			*out = append(*out, path)
			continue
		}
		compare(path, want, got, out)
	}
}

func compare(path string, want, got any, out *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, path)
			return
		}
		walkMap(path, w, g, out)

	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			*out = append(*out, path)
			return
		}
		// Recurse per element rather than comparing the slices wholesale, so
		// that fields the API server defaulted inside a list element (a
		// container's imagePullPolicy, for instance) are not read as drift.
		for i := range w {
			compare(fmt.Sprintf("%s[%d]", path, i), w[i], g[i], out)
		}

	default:
		if !equalScalar(want, got) {
			*out = append(*out, path)
		}
	}
}

// equalScalar compares two leaf values, treating numbers by value rather than
// by Go type. The same YAML can decode to int64 through one path and float64
// through another, and 2 is not drift from 2.0.
func equalScalar(want, got any) bool {
	if wf, ok := asFloat(want); ok {
		if gf, ok := asFloat(got); ok {
			return wf == gf
		}
		return false
	}
	return reflect.DeepEqual(want, got)
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// Summarise renders a short, readable list of changed paths for a status
// message, truncating so one badly drifted object cannot fill the field.
func Summarise(fields []string, max int) string {
	if len(fields) <= max {
		return strings.Join(fields, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(fields[:max], ", "), len(fields)-max)
}
