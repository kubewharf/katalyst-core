/*
Copyright 2026 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dynamicpolicy

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedBulkheadBoundaryImports is the exact surface the reviewable main flow
// may depend on. It is the code form of the "Bulkhead boundary" section in
// doc.go: the main flow must not import bulkhead's internal planning / partition
// / solver packages. The empty key is the root bulkhead package itself.
var allowedBulkheadBoundaryImports = map[string]struct{}{
	"":      {}, // bulkhead (root facade)
	"model": {},
	"utils": {},
}

// TestDynamicPolicyRespectsBulkheadBoundary fails when a non-test main-flow file
// under dynamicpolicy/ (outside the bulkhead/ subtree) imports any bulkhead
// package beyond the documented facade surface. It exists so the architectural
// boundary pinned in doc.go cannot silently rot as bulkhead internals move.
func TestDynamicPolicyRespectsBulkheadBoundary(t *testing.T) {
	t.Parallel()

	root := "."
	allowedPrefix := "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead"

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dynamicpolicy dir: %v", err)
	}

	fset := token.NewFileSet()
	violations := make(map[string][]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range f.Imports {
			importPath := strings.Trim(spec.Path.Value, `"`)
			if !strings.HasPrefix(importPath, allowedPrefix) {
				continue
			}
			rel := strings.TrimPrefix(importPath, allowedPrefix)
			// exact "bulkhead" package has rel == ""
			rel = strings.TrimPrefix(rel, "/")
			if _, ok := allowedBulkheadBoundaryImports[rel]; !ok {
				violations[entry.Name()] = append(violations[entry.Name()], importPath)
			}
		}
	}

	if len(violations) > 0 {
		for file, imports := range violations {
			t.Errorf("%s imports disallowed bulkhead internals: %v (allowed: bulkhead root, bulkhead/model, bulkhead/utils)",
				file, imports)
		}
	}
}
