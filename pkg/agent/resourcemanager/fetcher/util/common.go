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

package util

import (
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
)

type GetContentFunc func() ([]*v1alpha1.ReportContent, error)

// AppendReportContent appends contents generated from multiple sources (each source
// is represented as a general getter function) into one single content slice.
func AppendReportContent(fn ...GetContentFunc) ([]*v1alpha1.ReportContent, error) {
	var (
		contents []*v1alpha1.ReportContent
		errList  []error
	)

	for _, f := range fn {
		c, err := f()
		if err != nil {
			errList = append(errList, err)
			continue
		}

		contents = append(contents, c...)
	}

	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}

	return contents, nil
}
