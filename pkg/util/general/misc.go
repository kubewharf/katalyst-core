package general

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

func ParseLinuxListFormat(listStr string) ([]int64, error) {
	if strings.TrimSpace(listStr) == "" {
		return nil, nil
	}

	sections := strings.Split(listStr, ",")
	if len(sections) == 0 {
		return nil, fmt.Errorf("%s content is empty", listStr)
	}

	var list []int64
	for _, sec := range sections {
		boundaries := strings.Split(sec, "-")
		if len(boundaries) == 1 {
			val, err := strconv.ParseInt(boundaries[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid section %s in %s", sec, listStr)
			}
			list = append(list, val)
		} else if len(boundaries) == 2 {
			start, err := strconv.ParseInt(boundaries[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid section %s in %s", sec, listStr)
			}
			end, err := strconv.ParseInt(boundaries[1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid section %s in %s", sec, listStr)
			}
			if start >= end {
				return nil, fmt.Errorf("invalid section %s in %s", sec, listStr)
			}
			for start <= end {
				list = append(list, start)
				start++
			}
		} else {
			return nil, fmt.Errorf("%s contains strange section %s", listStr, sec)
		}
	}

	SortInt64Slice(list)
	return list, nil
}

func ParseLinuxListFormatFromFile(filePath string) ([]int64, error) {
	b, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadFile %s, err %s", filePath, err)
	}

	s := strings.TrimSpace(strings.TrimRight(string(b), "\n"))
	if len(s) == 0 {
		return nil, nil
	}
	return ParseLinuxListFormat(s)
}

// copied from https://code.byted.org/kubewharf/katalyst-core/blob/master/pkg/util/machine/cpuset.go
func ConvertLinuxListToString(numbers []int64) string {
	if len(numbers) == 0 {
		return ""
	}

	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })

	type rng struct {
		start int64
		end   int64
	}

	ranges := []rng{{numbers[0], numbers[0]}}
	for i := 1; i < len(numbers); i++ {
		lastRange := &ranges[len(ranges)-1]
		// if this element is adjacent to the high end of the last range
		if numbers[i] == lastRange.end+1 {
			// then extend the last range to include this element
			lastRange.end = numbers[i]
			continue
		}
		// otherwise, start a new range beginning with this element
		ranges = append(ranges, rng{numbers[i], numbers[i]})
	}

	// construct string from ranges
	var result bytes.Buffer
	for _, r := range ranges {
		if r.start == r.end {
			result.WriteString(strconv.Itoa(int(r.start)))
		} else {
			result.WriteString(fmt.Sprintf("%d-%d", r.start, r.end))
		}
		result.WriteString(",")
	}
	return strings.TrimRight(result.String(), ",")
}

func GetSlicesIntersection(a []int64, b []int64) []int64 {
	var c []int64

	for _, i := range a {
		for _, j := range b {
			if i == j {
				c = append(c, i)
				break
			}
		}
	}

	return c
}

func GetSlicesDiff(a []int64, b []int64) []int64 {
	var c []int64

	for _, i := range a {
		found := false
		for _, j := range b {
			if i == j {
				found = true
				break
			}
		}
		if !found {
			c = append(c, i)
		}
	}

	return c
}

func ConvertIntSliceToBitmapString(nums []int64) (string, error) {
	if len(nums) == 0 {
		return "", nil
	}

	maxVal := int64(-1)
	for _, v := range nums {
		if v < 0 {
			return "", fmt.Errorf("nums contains negtive value")
		}

		if v > maxVal {
			maxVal = v
		}
	}

	length := (maxVal + 31) / 32
	bitmap := make([]uint32, length)

	for _, i := range nums {
		index := i / 32
		shift := i % 32
		subBitmap := bitmap[index]
		subBitmap |= (1 << shift)
		bitmap[index] = subBitmap
	}

	bitmapStr := ""
	for i := len(bitmap) - 1; i >= 0; i-- {
		if bitmapStr == "" {
			bitmapStr = fmt.Sprintf("%08x", bitmap[i])
		} else {
			bitmapStr = fmt.Sprintf("%s,%08x", bitmapStr, bitmap[i])
		}
	}
	return bitmapStr, nil
}

func ReadLines(file string) ([]string, error) {
	f, err := os.OpenFile(file, os.O_RDONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines := make([]string, 0)
	scanner := bufio.NewScanner(f)

	maxCapacity := 1024 * 1024
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func ReadInt64FromFile(file string) (int64, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return -1, fmt.Errorf("failed to read(%s), err %v", file, err)
	}

	s := strings.TrimSpace(strings.TrimRight(string(b), "\n"))

	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1, fmt.Errorf("failed to ParseInt(%s), err %v", s, err)
	}
	return val, nil
}

func ReadUint64FromFile(file string) (uint64, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0, fmt.Errorf("failed to read(%s), err %v", file, err)
	}

	s := strings.TrimSpace(strings.TrimRight(string(b), "\n"))

	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to ParseInt(%s), err %v", s, err)
	}
	return val, nil
}

func GetFileInode(file string) (uint64, error) {
	fileInfo, err := os.Stat(file)
	if err != nil {
		return 0, fmt.Errorf("failed to stat(%s), err %v", file, err)
	}

	// Type assertion to get syscall.Stat_t which contains inode information
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unable to get inode information for %s", file)
	}

	return stat.Ino, nil
}

// Int64Slice attaches the methods of Interface to []int64, sorting in increasing order.
type Int64Slice []int64

func (x Int64Slice) Len() int           { return len(x) }
func (x Int64Slice) Less(i, j int) bool { return x[i] < x[j] }
func (x Int64Slice) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }

// SortInt64Slice sorts a slice of int64 in increasing order.
func SortInt64Slice(x []int64) {
	sort.Sort(Int64Slice(x))
}

func IntSliceHasOverlap(a, b []int) bool {
	hasOverlap := false

	for _, i := range a {
		for _, j := range b {
			if i == j {
				hasOverlap = true
				break
			}
		}
		if hasOverlap {
			break
		}
	}

	return hasOverlap
}

func GetIntersectionOfTwoIntSlices(a, b []int) []int {
	var intersection []int

	for _, i := range a {
		for _, j := range b {
			if i == j {
				intersection = append(intersection, i)
			}
		}
	}

	return intersection
}

func ConvertInt64SliceToIntSlice(a []int64) []int {
	var b []int
	for _, i := range a {
		b = append(b, int(i))
	}
	return b
}
