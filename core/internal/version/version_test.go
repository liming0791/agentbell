package version

import "testing"

func TestCurrent(t *testing.T) {
	info := Current()
	if info.Version == "" || info.Commit == "" || info.GoVersion == "" ||
		info.Platform == "" || info.Arch == "" {
		t.Fatalf("incomplete version info: %#v", info)
	}
}
