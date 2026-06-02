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

package validator

import (
	"context"
	"fmt"
)

type AnnotationValidator interface {
	ValidatePodAnnotation(ctx context.Context, podAnnotation map[string]string) (bool, error)
}

type DummyAnnotationValidator struct{}

func (d DummyAnnotationValidator) ValidatePodAnnotation(_ context.Context, _ map[string]string) (bool, error) {
	return true, nil
}

// ValidatePodAnnotations validates the pod annotations
// if any annotation is forbidden, it returns true and nil
// otherwise, it returns false and error
func ValidatePodAnnotations(podAnnotation map[string]string, forbiddenAnnos map[string]string) (bool, error) {
	if podAnnotation == nil {
		return true, nil
	}

	for key := range podAnnotation {
		if _, ok := forbiddenAnnos[key]; ok {
			return false, fmt.Errorf("the pod contains an invalid annotation key: %s", key)
		}
	}

	return true, nil
}
