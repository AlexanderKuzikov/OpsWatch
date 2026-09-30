//go:build windows

package probe

func diskSpace(path string) (free, total int64, err error) { return 0, 0, errNoStatfs }