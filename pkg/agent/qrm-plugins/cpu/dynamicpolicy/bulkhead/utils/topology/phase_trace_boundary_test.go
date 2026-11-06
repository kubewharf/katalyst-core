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

package topology

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParentSafeValidatedTraceProductionBoundary(t *testing.T) {
	t.Parallel()
	files := parseTopologyProductionFiles(t)

	functions := make(map[string]*ast.FuncDecl)
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			functions[function.Name.Name] = function
		}
	}

	for _, name := range []string{
		"ReservePhaseTrace",
		"preflightFrozenTrace",
		"preflightFrozenTraceOperations",
		"executeFrozenTrace",
	} {
		require.NotContains(t, functions, name,
			"production reservation, preflight, and execution must not accept raw traces")
	}
	for _, name := range []string{
		"reserveValidatedPhaseTrace",
		"preflightValidatedTraceOperations",
		"executeValidatedFrozenTrace",
	} {
		require.True(t, functionAcceptsValidatedCarrier(functions[name]),
			"%s must accept *validatedPhaseTrace", name)
		require.True(t, functionAcceptsContext(functions[name]),
			"%s must accept context.Context", name)
		require.Empty(t, validatedConsumerConstructionViolations(functions[name]),
			"%s must only consume its validated carrier", name)
	}

	require.Equal(t,
		[]string{"compileValidatedFixedPointTrace"},
		validatedPhaseTraceFreezerCallers(files),
		"only the compiler may invoke the validated carrier freezer",
	)
	require.Empty(t, validatedPhaseTraceFreezerReferenceViolations(files),
		"the freezer must only be referenced by its declaration and direct compiler call")
	require.Equal(t,
		[]string{"freezeValidatedPhaseTrace"},
		validatedPhaseTraceLiteralOwners(files),
		"only the validated carrier freezer may construct the carrier",
	)
	require.Empty(t, validatedPhaseTraceAliases(files),
		"validatedPhaseTrace aliases bypass the compiler-owned boundary")
	require.Equal(t, []string{"frozen"}, productionStructFieldNames(t, files, "validatedPhaseTrace"))
	fields := productionStructFieldNames(t, files, "coordinatorRound")
	require.NotContains(t, fields, "frozenTrace")
	require.NotContains(t, fields, "executionTicket")
}

func validatedPhaseTraceFreezerReferenceViolations(files []*ast.File) []string {
	var violations []string
	for _, file := range files {
		var stack []ast.Node
		ast.Inspect(file, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			parent := ast.Node(nil)
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			stack = append(stack, node)

			identifier, ok := node.(*ast.Ident)
			if !ok || identifier.Name != "freezeValidatedPhaseTrace" {
				return true
			}
			if declaration, ok := parent.(*ast.FuncDecl); ok && declaration.Name == identifier {
				return true
			}
			call, directCall := parent.(*ast.CallExpr)
			if directCall && call.Fun == identifier &&
				enclosingFunctionName(stack) == "compileValidatedFixedPointTrace" {
				return true
			}
			violations = append(violations, enclosingFunctionName(stack))
			return true
		})
	}
	return violations
}

func enclosingFunctionName(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if function, ok := stack[i].(*ast.FuncDecl); ok {
			return function.Name.Name
		}
	}
	return "<package>"
}

func validatedPhaseTraceFreezerCallers(files []*ast.File) []string {
	var callers []string
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				identifier, ok := call.Fun.(*ast.Ident)
				if ok && identifier.Name == "freezeValidatedPhaseTrace" {
					callers = append(callers, function.Name.Name)
				}
				return true
			})
		}
	}
	return callers
}

func validatedPhaseTraceLiteralOwners(files []*ast.File) []string {
	var owners []string
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				typ := literal.Type
				if pointer, ok := typ.(*ast.StarExpr); ok {
					typ = pointer.X
				}
				identifier, ok := typ.(*ast.Ident)
				if ok && identifier.Name == "validatedPhaseTrace" {
					owners = append(owners, function.Name.Name)
				}
				return true
			})
		}
	}
	return owners
}

func validatedPhaseTraceAliases(files []*ast.File) []string {
	var aliases []string
	for _, file := range files {
		for _, declaration := range file.Decls {
			typeDeclaration, ok := declaration.(*ast.GenDecl)
			if !ok || typeDeclaration.Tok != token.TYPE {
				continue
			}
			for _, spec := range typeDeclaration.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || !typeSpec.Assign.IsValid() {
					continue
				}
				if expressionReferencesType(typeSpec.Type, "validatedPhaseTrace") {
					aliases = append(aliases, typeSpec.Name.Name)
				}
			}
		}
	}
	return aliases
}

func expressionReferencesType(expression ast.Expr, typeName string) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && identifier.Name == typeName {
			found = true
			return false
		}
		return !found
	})
	return found
}

func validatedConsumerConstructionViolations(function *ast.FuncDecl) []string {
	if function == nil || function.Body == nil {
		return []string{"missing function body"}
	}
	var violations []string
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			identifier, ok := typed.Fun.(*ast.Ident)
			if ok && (identifier.Name == "FreezePhaseTrace" ||
				identifier.Name == "freezeValidatedPhaseTrace") {
				violations = append(violations, identifier.Name+" call")
			}
		case *ast.CompositeLit:
			typ := typed.Type
			if pointer, ok := typ.(*ast.StarExpr); ok {
				typ = pointer.X
			}
			identifier, ok := typ.(*ast.Ident)
			if ok && identifier.Name == "validatedPhaseTrace" {
				violations = append(violations, "validatedPhaseTrace literal")
			}
		}
		return true
	})
	return violations
}

func parseTopologyProductionFiles(t *testing.T) []*ast.File {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	entries, err := os.ReadDir(filepath.Dir(currentFile))
	require.NoError(t, err)

	fileSet := token.NewFileSet()
	files := make([]*ast.File, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(
			fileSet, filepath.Join(filepath.Dir(currentFile), entry.Name()), nil, 0)
		require.NoError(t, err)
		files = append(files, file)
	}
	return files
}

func functionAcceptsValidatedCarrier(function *ast.FuncDecl) bool {
	if function == nil || function.Type.Params == nil {
		return false
	}
	for _, field := range function.Type.Params.List {
		pointer, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		identifier, ok := pointer.X.(*ast.Ident)
		if ok && identifier.Name == "validatedPhaseTrace" {
			return true
		}
	}
	return false
}

func functionAcceptsContext(function *ast.FuncDecl) bool {
	if function == nil || function.Type.Params == nil {
		return false
	}
	for _, field := range function.Type.Params.List {
		selector, ok := field.Type.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Context" {
			continue
		}
		pkg, ok := selector.X.(*ast.Ident)
		if ok && pkg.Name == "context" {
			return true
		}
	}
	return false
}

func productionStructFieldNames(t *testing.T, files []*ast.File, typeName string) []string {
	t.Helper()
	for _, file := range files {
		for _, declaration := range file.Decls {
			typeDeclaration, ok := declaration.(*ast.GenDecl)
			if !ok || typeDeclaration.Tok != token.TYPE {
				continue
			}
			for _, spec := range typeDeclaration.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != typeName {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				require.True(t, ok, "%s must remain a struct", typeName)
				var fields []string
				for _, field := range structType.Fields.List {
					for _, name := range field.Names {
						fields = append(fields, name.Name)
					}
				}
				return fields
			}
		}
	}
	t.Fatalf("production type %s not found", typeName)
	return nil
}
