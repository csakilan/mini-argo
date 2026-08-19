package drift

import (
	"strings"
	"testing"
)

func m(pairs ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i < len(pairs); i += 2 {
		out[pairs[i].(string)] = pairs[i+1]
	}
	return out
}

func joined(fields []string) string { return strings.Join(fields, ",") }

func TestFields_IdenticalObjectsHaveNoDrift(t *testing.T) {
	o := m("spec", m("replicas", int64(2), "image", "nginx"))
	if got := Fields(o, o); len(got) != 0 {
		t.Fatalf("expected no drift, got %v", got)
	}
}

func TestFields_ChangedScalar(t *testing.T) {
	desired := m("spec", m("replicas", int64(2)))
	live := m("spec", m("replicas", int64(5)))

	got := Fields(desired, live)
	if joined(got) != "spec.replicas" {
		t.Fatalf("got %v, want [spec.replicas]", got)
	}
}

func TestFields_MissingInLive(t *testing.T) {
	desired := m("spec", m("replicas", int64(2), "paused", true))
	live := m("spec", m("replicas", int64(2)))

	got := Fields(desired, live)
	if joined(got) != "spec.paused" {
		t.Fatalf("got %v, want [spec.paused]", got)
	}
}

// The API server defaults dozens of fields we never declared. Those are not
// drift: we only own what we assert.
func TestFields_ExtraFieldsInLiveAreNotDrift(t *testing.T) {
	desired := m("spec", m("replicas", int64(2)))
	live := m("spec", m(
		"replicas", int64(2),
		"revisionHistoryLimit", int64(10),
		"progressDeadlineSeconds", int64(600),
	))

	if got := Fields(desired, live); len(got) != 0 {
		t.Fatalf("defaulted fields must not count as drift, got %v", got)
	}
}

func TestFields_NestedChangeReportsFullPath(t *testing.T) {
	desired := m("spec", m("template", m("spec", m("serviceAccountName", "web"))))
	live := m("spec", m("template", m("spec", m("serviceAccountName", "other"))))

	got := Fields(desired, live)
	if joined(got) != "spec.template.spec.serviceAccountName" {
		t.Fatalf("got %v", got)
	}
}

func TestFields_ListLengthChange(t *testing.T) {
	desired := m("spec", m("ports", []any{int64(80), int64(443)}))
	live := m("spec", m("ports", []any{int64(80)}))

	got := Fields(desired, live)
	if joined(got) != "spec.ports" {
		t.Fatalf("got %v, want [spec.ports]", got)
	}
}

func TestFields_ListElementChangeReportsIndex(t *testing.T) {
	desired := m("spec", m("containers", []any{m("name", "web", "image", "nginx:1.29")}))
	live := m("spec", m("containers", []any{m("name", "web", "image", "nginx:1.20")}))

	got := Fields(desired, live)
	if joined(got) != "spec.containers[0].image" {
		t.Fatalf("got %v", got)
	}
}

// The realistic case: a container in the manifest declares three fields, and
// the live one has fifteen because the API server filled the rest in.
func TestFields_DefaultedFieldsInsideListElements(t *testing.T) {
	desired := m("spec", m("containers", []any{
		m("name", "web", "image", "nginx:1.29"),
	}))
	live := m("spec", m("containers", []any{
		m(
			"name", "web",
			"image", "nginx:1.29",
			"imagePullPolicy", "IfNotPresent",
			"terminationMessagePath", "/dev/termination-log",
			"resources", m(),
		),
	}))

	if got := Fields(desired, live); len(got) != 0 {
		t.Fatalf("defaults inside list elements must not count as drift, got %v", got)
	}
}

func TestFields_IgnoresStatus(t *testing.T) {
	desired := m("status", m("replicas", int64(2)))
	live := m("status", m("replicas", int64(0)))

	if got := Fields(desired, live); len(got) != 0 {
		t.Fatalf("status is never managed by us, got %v", got)
	}
}

func TestFields_IgnoresServerPopulatedMetadata(t *testing.T) {
	desired := m("metadata", m(
		"name", "web",
		"creationTimestamp", nil,
		"resourceVersion", "1",
		"uid", "aaa",
		"generation", int64(1),
		"managedFields", []any{},
	))
	live := m("metadata", m(
		"name", "web",
		"creationTimestamp", "2026-01-01T00:00:00Z",
		"resourceVersion", "999",
		"uid", "bbb",
		"generation", int64(7),
		"managedFields", []any{m("manager", "mini-argo")},
	))

	if got := Fields(desired, live); len(got) != 0 {
		t.Fatalf("server-populated metadata must be ignored, got %v", got)
	}
}

func TestFields_DetectsLabelChange(t *testing.T) {
	desired := m("metadata", m("labels", m("app", "web")))
	live := m("metadata", m("labels", m("app", "tampered")))

	got := Fields(desired, live)
	if joined(got) != "metadata.labels.app" {
		t.Fatalf("got %v", got)
	}
}

func TestFields_TypeChangeIsDrift(t *testing.T) {
	desired := m("spec", m("value", m("nested", true)))
	live := m("spec", m("value", "a string"))

	got := Fields(desired, live)
	if joined(got) != "spec.value" {
		t.Fatalf("got %v", got)
	}
}

// Numbers can arrive as int64 from one decoder and float64 from another.
// Equal values must not read as drift.
func TestFields_NumericTypesAreComparedByValue(t *testing.T) {
	desired := m("spec", m("replicas", int64(2)))
	live := m("spec", m("replicas", float64(2)))

	if got := Fields(desired, live); len(got) != 0 {
		t.Fatalf("2 and 2.0 are the same number, got %v", got)
	}
}

func TestFields_ReportsSeveralChangesSorted(t *testing.T) {
	desired := m("spec", m("replicas", int64(2), "paused", false))
	live := m("spec", m("replicas", int64(5), "paused", true))

	got := Fields(desired, live)
	if joined(got) != "spec.paused,spec.replicas" {
		t.Fatalf("expected sorted paths, got %v", got)
	}
}

func TestFields_EmptyDesiredHasNoDrift(t *testing.T) {
	if got := Fields(map[string]any{}, m("spec", m("replicas", int64(5)))); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestSummarise_ListsShortSets(t *testing.T) {
	if got := Summarise([]string{"a", "b"}, 3); got != "a, b" {
		t.Fatalf("got %q", got)
	}
}

func TestSummarise_TruncatesLongSets(t *testing.T) {
	got := Summarise([]string{"a", "b", "c", "d", "e"}, 2)
	if got != "a, b and 3 more" {
		t.Fatalf("got %q", got)
	}
}

func TestSummarise_Empty(t *testing.T) {
	if got := Summarise(nil, 3); got != "" {
		t.Fatalf("got %q", got)
	}
}
