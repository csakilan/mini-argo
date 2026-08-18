package manifests

import (
	"strings"
	"testing"
	"time"
)

func TestParse_YAMLBombDoesNotHang(t *testing.T) {
	bomb := `a: &a ["lol","lol","lol","lol","lol","lol","lol","lol","lol"]
b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]
c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]
d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]
e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]
f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]
g: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]
apiVersion: v1
kind: ConfigMap
metadata:
  name: bomb
`
	done := make(chan struct{})
	go func() { _, _ = Parse([][]byte{[]byte(bomb)}); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("YAML alias expansion did not terminate in 10s")
	}
}

func TestParse_DeepNestingDoesNotHang(t *testing.T) {
	deep := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: deep\ndata:\n  x: " +
		strings.Repeat("[", 100000) + strings.Repeat("]", 100000) + "\n"
	done := make(chan struct{})
	go func() { _, _ = Parse([][]byte{[]byte(deep)}); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deeply nested input did not terminate in 10s")
	}
}
