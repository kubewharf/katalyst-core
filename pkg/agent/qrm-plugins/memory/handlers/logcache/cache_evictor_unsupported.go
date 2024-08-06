//go:build !linux

package logcache

func EvictFileCache(_ string, _ int64) error {
	return nil
}
