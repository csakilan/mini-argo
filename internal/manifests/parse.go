// Package manifests turns raw YAML bytes into Kubernetes objects.
//
// Nothing here talks to a cluster. Parsing is a pure function, which is what
// lets it be tested with plain `go test` and no API server.
package manifests

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Parse converts YAML documents into unstructured Kubernetes objects.
//
// Each input may itself contain several documents separated by "---". Empty
// documents, whether blank, whitespace, or comments only, are skipped rather
// than producing phantom objects.
//
// Objects are returned as unstructured rather than typed values because a
// GitSource may point at a repository containing any kind at all, including
// custom resources whose Go types this binary has never seen.
func Parse(docs [][]byte) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured

	for i, doc := range docs {
		reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(doc)))

		for n := 0; ; n++ {
			chunk, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("input %d, document %d: reading YAML: %w", i, n, err)
			}

			// Cheap skip before doing any work, catching blank and whitespace-only
			// documents produced by leading or trailing separators.
			if len(bytes.TrimSpace(chunk)) == 0 {
				continue
			}

			// YAML to JSON first, then apimachinery's decoder. Plain
			// encoding/json turns every number into a float64, and every
			// Kubernetes helper expects int64, so spec.replicas would be
			// unreadable later.
			jsonBytes, err := yaml.YAMLToJSON(chunk)
			if err != nil {
				return nil, fmt.Errorf("input %d, document %d: parsing YAML: %w", i, n, err)
			}

			obj := &unstructured.Unstructured{}
			if err := utiljson.Unmarshal(jsonBytes, &obj.Object); err != nil {
				return nil, fmt.Errorf("input %d, document %d: decoding JSON: %w", i, n, err)
			}

			// A comment-only document unmarshals into nothing at all.
			if len(obj.Object) == 0 {
				continue
			}

			if obj.GetKind() == "" {
				return nil, fmt.Errorf("input %d, document %d: object has no kind", i, n)
			}
			if obj.GetName() == "" {
				return nil, fmt.Errorf("input %d, document %d: %s has no metadata.name", i, n, obj.GetKind())
			}

			out = append(out, obj)
		}
	}

	return out, nil
}
