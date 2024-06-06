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

package general

import "strings"

type CommonSuffixTranslator struct {
	suffix string
}

func NewCommonSuffixTranslator(suffix string) *CommonSuffixTranslator {
	return &CommonSuffixTranslator{
		suffix: suffix,
	}
}

func (cs *CommonSuffixTranslator) Translate(s string) string {
	if strings.Contains(s, cs.suffix) {
		return strings.SplitN(s, cs.suffix, 2)[0] + cs.suffix
	}
	return s
}
