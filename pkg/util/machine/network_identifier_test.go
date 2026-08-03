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

package machine

import "testing"

func TestParseNICIdentifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		identifier string
		wantNetNS  string
		wantNIC    string
		wantOK     bool
	}{
		{
			name:       "default namespace identifier",
			identifier: "eth0",
			wantNetNS:  DefaultNICNamespace,
			wantNIC:    "eth0",
			wantOK:     true,
		},
		{
			name:       "named namespace identifier",
			identifier: "ns1-eth0",
			wantNetNS:  "ns1",
			wantNIC:    "eth0",
			wantOK:     true,
		},
		{
			name:       "empty identifier",
			identifier: "",
			wantOK:     false,
		},
		{
			name:       "missing nic suffix",
			identifier: "ns1-",
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotNetNS, gotNIC, gotOK := ParseNICIdentifier(tt.identifier)
			if gotOK != tt.wantOK {
				t.Fatalf("unexpected ok, got=%t want=%t", gotOK, tt.wantOK)
			}
			if gotNetNS != tt.wantNetNS {
				t.Fatalf("unexpected netns, got=%q want=%q", gotNetNS, tt.wantNetNS)
			}
			if gotNIC != tt.wantNIC {
				t.Fatalf("unexpected nic, got=%q want=%q", gotNIC, tt.wantNIC)
			}
			if gotOK && FormatNICIdentifier(gotNetNS, gotNIC) != tt.identifier {
				t.Fatalf("round trip mismatch, got=%q want=%q", FormatNICIdentifier(gotNetNS, gotNIC), tt.identifier)
			}
		})
	}
}
