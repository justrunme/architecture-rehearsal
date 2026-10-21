package impact

import "testing"

func TestParseMaxUnavailable_KubernetesRounding(t *testing.T) {
	// Percent maxUnavailable rounds DOWN; default is 25%.
	cases := []struct {
		ms       string
		replicas int
		want     int
	}{
		// defaults (25%)
		{"", 1, 0},
		{"", 2, 0},
		{"", 3, 0},
		{"", 4, 1},
		{"", 10, 2},
		// explicit 25%
		{"25%", 1, 0},
		{"25%", 2, 0},
		{"25%", 3, 0},
		{"25%", 4, 1},
		{"25%", 10, 2},
		// 100% / absolute
		{"100%", 3, 3},
		{"100%", 1, 1},
		{"0%", 10, 0},
		{"1", 3, 1},
		{"3", 3, 3},
	}
	for _, tc := range cases {
		got := parseMaxUnavailable(tc.ms, tc.replicas)
		if got != tc.want {
			t.Errorf("parseMaxUnavailable(%q, %d)=%d want %d", tc.ms, tc.replicas, got, tc.want)
		}
	}
}

func TestParseMaxSurge_KubernetesRounding(t *testing.T) {
	// Percent maxSurge rounds UP; default is 25%.
	cases := []struct {
		ms       string
		replicas int
		want     int
	}{
		{"", 1, 1},
		{"", 2, 1},
		{"", 3, 1},
		{"", 4, 1},
		{"", 10, 3},
		{"25%", 1, 1},
		{"25%", 2, 1},
		{"25%", 3, 1},
		{"25%", 4, 1},
		{"25%", 10, 3},
		{"0%", 3, 0},
		{"100%", 3, 3},
		{"0", 3, 0},
		{"2", 3, 2},
	}
	for _, tc := range cases {
		got := parseMaxSurge(tc.ms, tc.replicas)
		if got != tc.want {
			t.Errorf("parseMaxSurge(%q, %d)=%d want %d", tc.ms, tc.replicas, got, tc.want)
		}
	}
}
