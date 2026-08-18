package manifests

import (
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
