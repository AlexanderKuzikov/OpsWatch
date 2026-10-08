//go:build windows

package probe

// Load and Mem read /proc, which does not exist here. The watched server is
// Linux, so on Windows these are honest skips, not failures.
func Load() Result {
	return Skip("load", "host-local check: this host is not the watched server")
}

// Mem reports RAM usage.
func Mem() Result {
	return Skip("mem", "host-local check: this host is not the watched server")
}
