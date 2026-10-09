/*
Copyright 2022 The Katalyst Authors.

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

package api_test

// Boundary guard for the bulkhead api/manager layering.
//
// Allowed dependency edges:
//   registry -> {manager, api, plugins}   (wiring layer; may reach plugins)
//   manager  -> {api, model}               (+ registry for wiring)
//   api      -> {model}
//   plugins/cpusettopology -> {api, model, utils/topology}
//
// This test checks the DIRECT imports (`.Imports`) of the guarded root packages.
// Transitive reach-through via the wiring layer (manager -> registry -> plugin
// -> utils/topology) is intentional and allowed; what is forbidden is the
// root package itself directly importing an internal sibling it must not know
// about.
//
// Forbidden direct imports (this test fails on any of them):
//   manager -> utils/topology
//   api     -> utils/topology
//   manager -> plugins/cpusettopology
//   api     -> plugins/cpusettopology

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenImports lists substrings that must never appear in the DIRECT
// import list of the guarded packages.
var forbiddenImports = []string{
	"bulkhead/plugins/cpusettopology",
	"bulkhead/utils/topology",
}

// guardedPackages are the packages whose direct imports are checked.
var guardedPackages = []string{
	".",  // this api package
	"..", // the manager package (bulkhead root)
}

func TestBoundaryNoForbiddenDirectImports(t *testing.T) {
	t.Parallel()
	for _, pkg := range guardedPackages {
		cmd := exec.Command("go", "list", "-f", `{{ join .Imports "\n" }}`, pkg)
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("go list %s failed: %s", pkg, string(ee.Stderr))
			}
			t.Fatalf("go list %s failed: %v", pkg, err)
		}
		imports := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, forbidden := range forbiddenImports {
			for _, imp := range imports {
				if strings.Contains(imp, forbidden) {
					t.Fatalf("boundary violated: package %s directly imports %s (found %q)", pkg, forbidden, imp)
				}
			}
		}
	}
}
