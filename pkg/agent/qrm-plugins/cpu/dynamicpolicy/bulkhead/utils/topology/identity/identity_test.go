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

package identity

import "testing"

func TestChildrenFingerprint(t *testing.T) {
	t.Parallel()
	refs := []ChildRef{
		{Name: "pod-b", Identity: CgroupIdentity{Device: 1, Inode: 22}},
		{Name: "pod-a", Identity: CgroupIdentity{Device: 1, Inode: 11}},
	}

	first := ChildrenFingerprint(refs)
	second := ChildrenFingerprint([]ChildRef{refs[1], refs[0]})
	if first != second {
		t.Fatalf("fingerprints differ by input order: %q != %q", first, second)
	}
	if refs[0].Name != "pod-b" || refs[1].Name != "pod-a" {
		t.Fatalf("ChildrenFingerprint mutated input: %+v", refs)
	}

	changed := ChildrenFingerprint([]ChildRef{
		{Name: "pod-a", Identity: CgroupIdentity{Device: 1, Inode: 11}},
		{Name: "pod-b", Identity: CgroupIdentity{Device: 1, Inode: 23}},
	})
	if first == changed {
		t.Fatalf("fingerprint did not change with child identity: %q", first)
	}
}
