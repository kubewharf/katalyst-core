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

package rapl

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/afero"
	"k8s.io/klog/v2"
)

const (
	rapl_short_term_path_template = "/sys/class/powercap/intel-rapl:%d/constraint_1_power_limit_uw"
	rapl_long_term_path_template  = "/sys/class/powercap/intel-rapl:%d/constraint_0_power_limit_uw"

	min_long_term_power_limit_watt = 80
)

type RAPLLimiter interface {
	SetLimitOnBasis(limit, base int) error
	InitResetter
}

type RAPL struct {
	getSetter    GetSetter
	initResetter InitResetter
}

func (r RAPL) Init() error {
	return r.initResetter.Init()
}

func (r RAPL) Reset() {
	r.initResetter.Reset()
}

// SetLimitOnBasis assuming the current power usage is the base,
// to set the target limit, by adjusting the RAPL settings
func (r RAPL) SetLimitOnBasis(limit, base int) error {
	// adjustment formula: settings = readings + limit - base
	// assuming N packages equally applied to
	reading, err := r.getSetter.Get()
	if err != nil {
		return err
	}

	setting := reading + (limit - base)

	return r.getSetter.Set(setting)
}

type GetSetter interface {
	Get() (int, error)
	Set(int) error
}

type InitResetter interface {
	Init() error
	Reset()
}

type raplGetSetter struct {
	fs       afero.Fs
	packages int
}

func (r *raplGetSetter) Init() error {
	// to detect packages under control of RAPL
	i := 0
	for {
		path := fmt.Sprintf(rapl_short_term_path_template, i)
		if _, err := r.fs.Stat(path); err != nil {
			break
		}

		i += 1
	}

	if i <= 0 {
		return errors.New("RAPL capacity not found")
	}

	r.packages = i
	return nil
}

func (r *raplGetSetter) Reset() {
	for i := 0; i < r.packages; i++ {
		if err := r.resetLimit(i); err != nil {
			klog.Errorf("pap: failed to reset short term limit of package %d: %v", i, err)
			continue
		}
	}
}

func (r *raplGetSetter) resetLimit(packageID int) error {
	// get the long term value
	// the default short term = 1.20 * long term
	longTerm, err := r.getLongTermWattOfPackage(packageID)
	if err != nil {
		return err
	}

	if longTerm <= min_long_term_power_limit_watt {
		return fmt.Errorf("pap: invalid long term power limit setting:  %d of package %d", longTerm, packageID)
	}

	defaultShorTerm := longTerm * 120 / 100
	return r.setShortTermWattPackage(packageID, defaultShorTerm)
}

func (r *raplGetSetter) Get() (int, error) {
	sum := 0
	for i := 0; i < r.packages; i++ {
		value, err := r.getShortTermWattOfPackage(i)
		if err != nil {
			return sum, err
		}

		sum += value
	}

	return sum, nil
}

func (r *raplGetSetter) Set(limit int) error {
	value := limit / r.packages
	for i := 0; i < r.packages; i++ {
		if err := r.setShortTermWattPackage(i, value); err != nil {
			return err
		}
	}

	return nil
}

var (
	_ GetSetter    = &raplGetSetter{}
	_ InitResetter = &raplGetSetter{}
)

func (r *raplGetSetter) getShortTermWattOfPackage(id int) (int, error) {
	path := fmt.Sprintf(rapl_short_term_path_template, id)
	return r.readWattFromFile(path)
}

func (r *raplGetSetter) getLongTermWattOfPackage(id int) (int, error) {
	path := fmt.Sprintf(rapl_long_term_path_template, id)
	return r.readWattFromFile(path)
}

func (r *raplGetSetter) setShortTermWattPackage(packageID, limit int) error {
	path := fmt.Sprintf(rapl_short_term_path_template, packageID)
	f, err := r.fs.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	uw := limit * 1000000
	// put trailing newline to avoid leftover side effect
	uwStr := fmt.Sprintf("%d\n", uw)
	buff := []byte(uwStr)
	n, err := f.Write(buff)
	if err != nil {
		return err
	}
	if n != len(buff) {
		return errors.New("partial write")
	}

	return nil
}

func (r *raplGetSetter) readWattFromFile(path string) (int, error) {
	f, err := r.fs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buff := make([]byte, 32)
	n, err := f.Read(buff)
	if err != nil {
		return 0, err
	}

	reading, err := strconv.Atoi(strings.TrimSpace(string(buff[:n])))
	if err != nil {
		return 0, err
	}

	// convert to watt
	reading /= 1000000
	return reading, nil
}

func NewLimiter() RAPLLimiter {
	raplHandler := &raplGetSetter{
		fs: afero.NewOsFs(),
	}

	return &RAPL{
		getSetter:    raplHandler,
		initResetter: raplHandler,
	}
}
