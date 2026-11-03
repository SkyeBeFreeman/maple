package config

import "testing"

func TestPeekPort(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     int
	}{
		{"set", "port: 48000\nservers: {}\n", 48000},
		{"absent", "servers: {}\n", DefaultPort},
		{"invalid server section", "port: 48001\nservers:\n  bad name!: {url: x}\nunknown: 1\n", 48001},
		{"not a number", "port: abc\n", DefaultPort},
		{"out of range", "port: 70000\n", DefaultPort},
		{"zero", "port: 0\n", DefaultPort},
		{"broken yaml", "port: 48002\nservers: [\n", DefaultPort},
		{"empty", "", DefaultPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PeekPort([]byte(tc.in)); got != tc.want {
				t.Errorf("PeekPort(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
