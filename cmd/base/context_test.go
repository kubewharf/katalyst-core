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

package katalyst_base

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeDebugFlagsHTTP(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	serveDebugFlagsHTTP(mux)

	originalVerbosity := getKlogVerbosity()
	if originalVerbosity == "" {
		t.Fatal("expected klog verbosity flag to be available")
	}

	defer func() {
		if err := setKlogVerbosity(originalVerbosity); err != nil {
			t.Fatalf("restore verbosity failed: %v", err)
		}
	}()

	setResp := httptest.NewRecorder()
	setReq := httptest.NewRequest(http.MethodPut, debugVerbosityVPath, strings.NewReader("5"))
	mux.ServeHTTP(setResp, setReq)
	if setResp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", setResp.Code, setResp.Body.String())
	}
	if !strings.Contains(setResp.Body.String(), `"value":"5"`) {
		t.Fatalf("expected response to contain updated verbosity, got %s", setResp.Body.String())
	}

	getResp := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, debugVerbosityVPath, nil)
	mux.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", getResp.Code, getResp.Body.String())
	}
	if !strings.Contains(getResp.Body.String(), `"value":"5"`) {
		t.Fatalf("expected response to contain current verbosity, got %s", getResp.Body.String())
	}

	invalidResp := httptest.NewRecorder()
	invalidReq := httptest.NewRequest(http.MethodPut, debugVerbosityVPath, strings.NewReader("invalid"))
	mux.ServeHTTP(invalidResp, invalidReq)
	if invalidResp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d, body: %s", invalidResp.Code, invalidResp.Body.String())
	}

	missingResp := httptest.NewRecorder()
	missingReq := httptest.NewRequest(http.MethodPut, debugVerbosityVPath, nil)
	mux.ServeHTTP(missingResp, missingReq)
	if missingResp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d, body: %s", missingResp.Code, missingResp.Body.String())
	}

	methodResp := httptest.NewRecorder()
	methodReq := httptest.NewRequest(http.MethodDelete, debugVerbosityVPath, nil)
	mux.ServeHTTP(methodResp, methodReq)
	if methodResp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d, body: %s", methodResp.Code, methodResp.Body.String())
	}
}

func TestServeDebugFlagsHTTPVModule(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	serveDebugFlagsHTTP(mux)

	originalVModule, err := getKlogFlagValue("vmodule")
	if err != nil {
		t.Fatalf("expected klog vmodule flag to be available: %v", err)
	}

	defer func() {
		if restoreErr := setKlogFlagValue("vmodule", originalVModule); restoreErr != nil {
			t.Fatalf("restore vmodule failed: %v", restoreErr)
		}
	}()

	setResp := httptest.NewRecorder()
	setReq := httptest.NewRequest(http.MethodPut, debugVModulePath, strings.NewReader("*controller=3"))
	mux.ServeHTTP(setResp, setReq)
	if setResp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", setResp.Code, setResp.Body.String())
	}
	if !strings.Contains(setResp.Body.String(), `"name":"vmodule"`) || !strings.Contains(setResp.Body.String(), `"value":"*controller=3"`) {
		t.Fatalf("expected response to contain updated vmodule, got %s", setResp.Body.String())
	}

	getResp := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, debugVModulePath, nil)
	mux.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", getResp.Code, getResp.Body.String())
	}
	if !strings.Contains(getResp.Body.String(), `"value":"*controller=3"`) {
		t.Fatalf("expected response to contain current vmodule, got %s", getResp.Body.String())
	}
}
