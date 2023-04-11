// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rdt

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	clos            = "fake-clos"
	tasks           = []string{"0", "1"}
	defaultCATValue = "7ff"
	defaultMBAValue = 100
)

func TestNewDefaultManager(t *testing.T) {
	defaultManager := NewDefaultManager()
	assert.NotNil(t, defaultManager)
}

func TestCheckSupportRDT(t *testing.T) {
	defaultManager := NewDefaultManager()
	assert.NotNil(t, defaultManager)

	isSupport, err := defaultManager.CheckSupportRDT()
	assert.Error(t, err)
	assert.False(t, isSupport)
}

func TestInitRDT(t *testing.T) {
	defaultManager := NewDefaultManager()
	assert.NotNil(t, defaultManager)

	err := defaultManager.InitRDT()
	assert.Error(t, err)
}

func TestApplyTasks(t *testing.T) {
	defaultManager := NewDefaultManager()
	assert.NotNil(t, defaultManager)

	err := defaultManager.ApplyTasks(clos, tasks)
	assert.Error(t, err)
}

func TestApplyCAT(t *testing.T) {
	defaultManager := NewDefaultManager()
	assert.NotNil(t, defaultManager)

	catInt64, err := strconv.ParseInt(defaultCATValue, 16, 32)
	assert.NoError(t, err)

	cat := map[int]int{
		0: int(catInt64),
		1: int(catInt64),
	}
	err = defaultManager.ApplyCAT(clos, cat)
	assert.Error(t, err)

	return
}

func TestApplyMBA(t *testing.T) {
	defaultManager := NewDefaultManager()
	assert.NotNil(t, defaultManager)

	mba := map[int]int{
		0: defaultMBAValue,
		1: defaultMBAValue,
	}
	err := defaultManager.ApplyMBA(clos, mba)
	assert.Error(t, err)

	return
}
