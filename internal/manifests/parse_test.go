package manifests

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParse_SingleDocument(t *testing.T) {
	doc := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
  namespace: web
data:
  key: value
`)

	objs, err := Parse([][]byte{doc})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	if got := objs[0].GetKind(); got != "ConfigMap" {
		t.Errorf("kind = %q, want ConfigMap", got)
	}
	if got := objs[0].GetAPIVersion(); got != "v1" {
		t.Errorf("apiVersion = %q, want v1", got)
	}
	if got := objs[0].GetName(); got != "demo" {
		t.Errorf("name = %q, want demo", got)
	}
	if got := objs[0].GetNamespace(); got != "web" {
		t.Errorf("namespace = %q, want web", got)
	}
}

func TestParse_MultiDocumentSeparator(t *testing.T) {
	doc := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: first
---
apiVersion: v1
kind: Service
metadata:
  name: second
`)

	objs, err := Parse([][]byte{doc})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objs))
	}
	if objs[0].GetName() != "first" || objs[1].GetName() != "second" {
		t.Errorf("got names %q and %q, want first and second",
			objs[0].GetName(), objs[1].GetName())
	}
}

// The bug everyone writes: a trailing separator producing a phantom object.
func TestParse_IgnoresEmptyDocuments(t *testing.T) {
	cases := map[string][]byte{
		"trailing separator": []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: only\n---\n"),
		"leading separator":  []byte("---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: only\n"),
		"blank between":      []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: only\n---\n\n---\n"),
		"comment only":       []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: only\n---\n# just a comment\n"),
		"whitespace only":    []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: only\n---\n   \n\t\n"),
	}

	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			objs, err := Parse([][]byte{doc})
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if len(objs) != 1 {
				t.Fatalf("expected 1 object, got %d", len(objs))
			}
			if objs[0].GetName() != "only" {
				t.Errorf("name = %q, want only", objs[0].GetName())
			}
		})
	}
}

func TestParse_EmptyInputYieldsNoObjects(t *testing.T) {
	objs, err := Parse([][]byte{[]byte(""), []byte("---\n"), []byte("# nothing here\n")})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(objs) != 0 {
		t.Fatalf("expected 0 objects, got %d", len(objs))
	}
}

func TestParse_CombinesMultipleInputs(t *testing.T) {
	a := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")
	b := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n")

	objs, err := Parse([][]byte{a, b})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(objs) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(objs))
	}
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if objs[i].GetName() != w {
			t.Errorf("objs[%d] name = %q, want %q", i, objs[i].GetName(), w)
		}
	}
}

func TestParse_InvalidYAMLReturnsError(t *testing.T) {
	_, err := Parse([][]byte{[]byte("apiVersion: v1\nkind: ConfigMap\n  bad indentation here\n")})
	if err == nil {
		t.Fatal("expected an error for malformed YAML, got nil")
	}
}

func TestParse_RejectsDocumentWithoutKind(t *testing.T) {
	_, err := Parse([][]byte{[]byte("apiVersion: v1\nmetadata:\n  name: nameless\n")})
	if err == nil {
		t.Fatal("expected an error for a document with no kind, got nil")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Errorf("error should mention kind, got: %v", err)
	}
}

func TestParse_RejectsDocumentWithoutName(t *testing.T) {
	_, err := Parse([][]byte{[]byte("apiVersion: v1\nkind: ConfigMap\n")})
	if err == nil {
		t.Fatal("expected an error for a document with no name, got nil")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention name, got: %v", err)
	}
}

func TestParse_PreservesNestedFields(t *testing.T) {
	doc := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: web
          image: nginx:1.29
`)

	objs, err := Parse([][]byte{doc})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	replicas, found, err := unstructured.NestedInt64(objs[0].Object, "spec", "replicas")
	if err != nil || !found {
		t.Fatalf("spec.replicas not found: found=%v err=%v", found, err)
	}
	if replicas != 2 {
		t.Errorf("spec.replicas = %d, want 2", replicas)
	}
}

// --- gap-closing tests added during Phase 3 review ---

func TestParse_HandlesCRLFLineEndings(t *testing.T) {
	doc := []byte("apiVersion: v1\r\nkind: ConfigMap\r\nmetadata:\r\n  name: windows\r\n")

	objs, err := Parse([][]byte{doc})
	if err != nil {
		t.Fatalf("a file edited on Windows should still parse: %v", err)
	}
	if len(objs) != 1 || objs[0].GetName() != "windows" {
		t.Fatalf("got %d objects", len(objs))
	}
}

func TestParse_HandlesMissingTrailingNewline(t *testing.T) {
	doc := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: nonewline")

	objs, err := Parse([][]byte{doc})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
}

// YAML is a superset of JSON, so a .yaml file containing JSON is legal.
func TestParse_AcceptsJSON(t *testing.T) {
	doc := []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"json-cm"}}`)

	objs, err := Parse([][]byte{doc})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(objs) != 1 || objs[0].GetName() != "json-cm" {
		t.Fatalf("expected the JSON document to parse")
	}
}

// Parse does not deduplicate. Recording that, because the controller applies
// in order and the last one silently wins.
func TestParse_KeepsDuplicatesInOrder(t *testing.T) {
	doc := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: twice
data:
  which: first
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: twice
data:
  which: second
`)

	objs, err := Parse([][]byte{doc})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("expected both duplicates returned, got %d", len(objs))
	}
}

func TestParse_RejectsTopLevelList(t *testing.T) {
	_, err := Parse([][]byte{[]byte("- apiVersion: v1\n  kind: ConfigMap\n")})
	if err == nil {
		t.Fatal("a top-level YAML list is not a Kubernetes object")
	}
}

func TestParse_RejectsScalarDocument(t *testing.T) {
	_, err := Parse([][]byte{[]byte("just a string\n")})
	if err == nil {
		t.Fatal("a bare scalar is not a Kubernetes object")
	}
}

// An object with no apiVersion cannot be applied, so it should fail here
// rather than deep inside the apply call.
func TestParse_RejectsDocumentWithoutAPIVersion(t *testing.T) {
	_, err := Parse([][]byte{[]byte("kind: ConfigMap\nmetadata:\n  name: versionless\n")})
	if err == nil {
		t.Fatal("expected an error for a document with no apiVersion, got nil")
	}
	if !strings.Contains(err.Error(), "apiVersion") {
		t.Errorf("error should mention apiVersion, got: %v", err)
	}
}

func TestParse_PreservesUnicode(t *testing.T) {
	doc := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: unicode\ndata:\n  greeting: \"你好 🌍\"\n")

	objs, err := Parse([][]byte{doc})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, found, err := unstructured.NestedString(objs[0].Object, "data", "greeting")
	if err != nil || !found {
		t.Fatalf("data.greeting missing: %v", err)
	}
	if got != "你好 🌍" {
		t.Errorf("greeting = %q, want the original", got)
	}
}

func TestParse_HandlesManyDocuments(t *testing.T) {
	var buf strings.Builder
	const n = 500
	for i := range n {
		fmt.Fprintf(&buf, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm-%d\n---\n", i)
	}

	objs, err := Parse([][]byte{[]byte(buf.String())})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(objs) != n {
		t.Fatalf("expected %d objects, got %d", n, len(objs))
	}
}

// Parse takes bytes straight from a repository, which is untrusted input.
// It may reject anything, but it must never panic.
func FuzzParse(f *testing.F) {
	f.Add([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n"))
	f.Add([]byte("---\n"))
	f.Add([]byte("a: {b: [c, d]}\n"))
	f.Add([]byte("apiVersion: v1\nkind: A\nmetadata:\n  name: a\n---\napiVersion: v1\nkind: B\nmetadata:\n  name: b\n"))
	f.Add([]byte("\x00\x01\x02"))
	f.Add([]byte("!!binary |\n  aGVsbG8=\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse([][]byte{data})
	})
}
