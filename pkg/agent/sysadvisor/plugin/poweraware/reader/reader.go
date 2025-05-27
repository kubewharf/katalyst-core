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

package reader

import (
	"context"
	"errors"
)

var errNotRealReader = errors.New("not a real power reader")

type PowerReader interface {
	MetricReader
}

type MetricReader interface {
	Init() error
	Get(ctx context.Context) (int, error)
	Cleanup()
}

// dummyPowerReader is the placeholder in case no real power reader is yet provided
type dummyPowerReader struct{}

func (d dummyPowerReader) Init() error {
	return nil
}

func (d dummyPowerReader) Get(ctx context.Context) (int, error) {
	return 0, errNotRealReader
}

func (d dummyPowerReader) Cleanup() {}

func NewDummyPowerReader() PowerReader {
	return &dummyPowerReader{}
}
