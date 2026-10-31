package handler

import (
	"encoding/base64"
	"testing"
)

func TestBase64PaddedLen(t *testing.T) {
	// Small sizes are checked against the stdlib; int(n) is safe here on
	// every platform.
	for _, n := range []int64{0, 1, 2, 3, 4, 5, 1 << 20} {
		if got, want := base64PaddedLen(n), int64(base64.StdEncoding.EncodedLen(int(n))); got != want {
			t.Errorf("n=%d: got %d, want %d", n, got, want)
		}
	}
	// Large sizes use literal expectations so the test itself never narrows.
	for n, want := range map[int64]int64{
		25 << 20:  34952536,
		1<<31 - 2: 2863311528,
		1 << 40:   1466015503704,
		-5:        0,
	} {
		if got := base64PaddedLen(n); got != want {
			t.Errorf("n=%d: got %d, want %d", n, got, want)
		}
	}
}
