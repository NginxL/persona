package devicewire

import "testing"

// validBuild reads a build number as update.ParseNumber does: twelve digits that are a time, YYYYMMDDHHMM.
func TestValidBuildIsAMinuteStamp(t *testing.T) {
	for build, want := range map[int64]bool{202611231200: true, 20261123120: false, 2026112312000: false,
		202613011200: false, 202611231260: false, -202611231200: false, 0: false} {
		if got := validBuild(build); got != want {
			t.Errorf("validBuild(%d) = %v, want %v", build, got, want)
		}
	}
}
