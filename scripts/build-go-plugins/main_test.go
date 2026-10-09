package main

import (
	"path/filepath"
	"testing"
)

func TestOutputPath(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, entry string
		valid       bool
	}{
		{"binary", "dist/plugin", true},
		{"nested binary", "dist/linux/plugin", true},
		{"empty", "", false},
		{"absolute", "/tmp/plugin", false},
		{"traversal", "../plugin", false},
		{"normalized traversal", "dist/../../plugin", false},
		{"source overwrite", "main.go", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := outputPath("/plugins/example-go", test.entry)
			if (err == nil) != test.valid {
				t.Fatalf("outputPath(%q) = %q, %v; valid=%v", test.entry, got, err, test.valid)
			}
			if test.valid && got != filepath.Join("/plugins/example-go", test.entry) {
				t.Fatalf("output path = %q", got)
			}
		})
	}
}
