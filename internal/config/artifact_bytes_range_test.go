package config

import (
	"strconv"
	"testing"
)

func TestGetEnvInt64InRangeArtifactBounds(t *testing.T) {
	const def = 25 << 20
	cases := map[string]int64{
		"":        def,
		"1":       1,
		"1048576": 1 << 20,
		strconv.FormatInt(maxArtifactBytesLimit, 10):   maxArtifactBytesLimit,
		strconv.FormatInt(maxArtifactBytesLimit+1, 10): def,
		"9223372036854775807":                          def,
		"9223372036854775808":                          def,
		"0":                                            def,
		"-1":                                           def,
		"junk":                                         def,
	}
	for in, want := range cases {
		if in == "" {
			t.Setenv("ARTIFACT_MAX_BYTES", "")
			// empty string parses as malformed -> default
		} else {
			t.Setenv("ARTIFACT_MAX_BYTES", in)
		}
		if got := getEnvInt64InRange("ARTIFACT_MAX_BYTES", def, 1, maxArtifactBytesLimit); got != want {
			t.Errorf("%q: got %d, want %d", in, got, want)
		}
	}
}

// Load must route both artifact size settings through the bounded helper.
func TestLoadBoundsArtifactSizeSettings(t *testing.T) {
	const (
		defMax  = 25 << 20
		defTask = 250 << 20
	)
	cases := []struct {
		name, max, task   string
		wantMax, wantTask int64
	}{
		{"defaults", "", "", defMax, defTask},
		{"valid", "1048576", "2097152", 1 << 20, 2 << 20},
		{"over ceiling", "9223372036854775807", "9223372036854775807", defMax, defTask},
		{"ceiling+1", strconv.FormatInt(maxArtifactBytesLimit+1, 10), strconv.FormatInt(maxArtifactBytesLimit+1, 10), defMax, defTask},
		{"zero and negative", "0", "-1", defMax, defTask},
		{"only task out of range", "1048576", "9223372036854775807", 1 << 20, defTask},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARTIFACT_MAX_BYTES", tc.max)
			t.Setenv("ARTIFACT_TASK_MAX_BYTES", tc.task)
			cfg := Load()
			if cfg.ArtifactMaxBytes != tc.wantMax {
				t.Errorf("ArtifactMaxBytes = %d, want %d", cfg.ArtifactMaxBytes, tc.wantMax)
			}
			if cfg.ArtifactTaskMaxBytes != tc.wantTask {
				t.Errorf("ArtifactTaskMaxBytes = %d, want %d", cfg.ArtifactTaskMaxBytes, tc.wantTask)
			}
		})
	}
}
