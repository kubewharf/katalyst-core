/*
Copyright 2026 The Katalyst Authors.

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

package rdt

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/consts"
)

// CATCapability describes the L3 cache bit mask capability of one resctrl
// cache domain.
type CATCapability struct {
	CBMMask        uint64
	MinCBMBits     int
	BitUsageByType map[string]uint64
}

// CATCapabilityProvider reads the L3 CAT capabilities used to construct a
// complete per-domain L3 schemata target.
type CATCapabilityProvider interface {
	GetCATCapabilities() (map[int]CATCapability, error)
}

type catCapabilityProvider struct {
	root     string
	readFile func(string) ([]byte, error)
}

// NewCATCapabilityProvider returns the Linux resctrl capability provider.
// Calls return a descriptive error on hosts where resctrl L3 CAT is absent.
func NewCATCapabilityProvider() CATCapabilityProvider {
	return newCATCapabilityProvider(consts.DefaultResctrlRootDir)
}

func newCATCapabilityProvider(root string) *catCapabilityProvider {
	return &catCapabilityProvider{
		root:     root,
		readFile: os.ReadFile,
	}
}

func (p *catCapabilityProvider) GetCATCapabilities() (map[int]CATCapability, error) {
	mask, err := readHexFile(p.readFile, filepath.Join(p.root, "info", "L3", "cbm_mask"))
	if err != nil {
		if isCDPEnabled(p.root) {
			return nil, fmt.Errorf("%w: L3CODE/L3DATA requires separate CAT updates", ErrCATUnsupported)
		}
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: read L3 cbm_mask: %v", ErrCATUnsupported, err)
		}
		return nil, fmt.Errorf("read L3 cbm_mask: %w", err)
	}
	minBits, err := readDecimalFile(p.readFile, filepath.Join(p.root, "info", "L3", "min_cbm_bits"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: read L3 min_cbm_bits: %v", ErrCATUnsupported, err)
		}
		return nil, fmt.Errorf("read L3 min_cbm_bits: %w", err)
	}
	if mask == 0 || minBits < 0 {
		return nil, fmt.Errorf("invalid L3 capability cbm_mask=%x min_cbm_bits=%d", mask, minBits)
	}

	schemata, err := p.readFile(filepath.Join(p.root, schemataFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: read root schemata for L3 domains: %v", ErrCATUnsupported, err)
		}
		return nil, fmt.Errorf("read root schemata for L3 domains: %w", err)
	}
	domains, err := l3Domains(string(schemata))
	if err != nil {
		return nil, err
	}
	bitUsageByDomain, err := p.readBitUsageByDomain()
	if err != nil {
		return nil, err
	}
	capabilities := make(map[int]CATCapability, len(domains))
	for _, domain := range domains {
		capabilities[domain] = CATCapability{
			CBMMask:        mask,
			MinCBMBits:     minBits,
			BitUsageByType: bitUsageByDomain[domain],
		}
	}
	return capabilities, nil
}

func (p *catCapabilityProvider) readBitUsageByDomain() (map[int]map[string]uint64, error) {
	data, err := p.readFile(filepath.Join(p.root, "info", "L3", "bit_usage"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read L3 bit_usage: %w", err)
	}

	return parseBitUsageByDomain(string(data))
}

func parseBitUsageByDomain(raw string) (map[int]map[string]uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("parse L3 bit_usage: empty content")
	}

	result := map[int]map[string]uint64{}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		domain, usage, found := strings.Cut(entry, "=")
		if !found {
			return nil, fmt.Errorf("parse L3 bit_usage: invalid domain entry %q", entry)
		}
		id, err := strconv.Atoi(domain)
		if err != nil || id < 0 {
			return nil, fmt.Errorf("parse L3 bit_usage: invalid domain %q", domain)
		}
		if usage == "" {
			return nil, fmt.Errorf("parse L3 bit_usage: empty usage for domain %d", id)
		}

		masks := map[string]uint64{}
		for offsetFromRight, usageType := range reverseStringRunes(usage) {
			if offsetFromRight >= 64 {
				return nil, fmt.Errorf("parse L3 bit_usage: usage for domain %d is too wide", id)
			}
			masks[string(usageType)] |= uint64(1) << offsetFromRight
		}
		result[id] = masks
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("parse L3 bit_usage: no domains")
	}
	return result, nil
}

func reverseStringRunes(value string) []rune {
	runes := []rune(value)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return runes
}

func isCDPEnabled(root string) bool {
	for _, resource := range []string{"L3CODE", "L3DATA"} {
		if _, err := os.Stat(filepath.Join(root, "info", resource, "cbm_mask")); err != nil {
			return false
		}
	}
	return true
}

func readHexFile(readFile func(string) ([]byte, error), path string) (uint64, error) {
	data, err := readFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q as hexadecimal: %w", path, err)
	}
	return value, nil
}

func readDecimalFile(readFile func(string) ([]byte, error), path string) (int, error) {
	data, err := readFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse %q as decimal: %w", path, err)
	}
	return value, nil
}

func l3Domains(schemata string) ([]int, error) {
	for _, line := range strings.Split(schemata, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, l3Resource+":") {
			continue
		}
		values := strings.TrimPrefix(line, l3Resource+":")
		var domains []int
		for _, value := range strings.Split(values, ";") {
			if value == "" {
				continue
			}
			domain, _, found := strings.Cut(value, "=")
			if !found {
				return nil, fmt.Errorf("invalid L3 domain entry %q", value)
			}
			id, err := strconv.Atoi(domain)
			if err != nil || id < 0 {
				return nil, fmt.Errorf("invalid L3 domain %q", domain)
			}
			domains = append(domains, id)
		}
		if len(domains) == 0 {
			return nil, fmt.Errorf("L3 schemata has no domains")
		}
		return domains, nil
	}
	return nil, fmt.Errorf("L3 schemata line not found")
}
