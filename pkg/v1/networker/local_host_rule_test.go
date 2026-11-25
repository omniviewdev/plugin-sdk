package networker

import "testing"

// isLoopbackHost is the rule that ForwarderResult.LocalHost, the proto comment and
// docs/capabilities.md give a reader of a session's local host: loopback if and
// only if the host is not empty and is "localhost" (ASCII, any case) or an IP
// address that is loopback once an IPv4-mapped IPv6 address is unmapped.
func TestIsLoopbackHostIsTheRuleForReadingALocalHost(t *testing.T) {
	for _, tc := range []struct {
		host     string
		loopback bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"LocalHost", true},
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"::1%lo", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:127.9.9.9", true},

		{"", false},
		{"0.0.0.0", false},
		{"::", false},
		{"::ffff:0.0.0.0", false},
		{"10.0.0.7", false},
		{"192.168.1.5", false},
		{"::ffff:10.0.0.7", false},
		{"169.254.1.1", false},
		{"fe80::1", false},
		{"fe80::1%en0", false},
		{"localhoſt", false},
		{"localhost.", false},
		{" localhost", false},
		{"127.1", false},
		{"127.0.0.1:8080", false},
		{"[::1]", false},
		{"forward.example.com", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			if got := isLoopbackHost(tc.host); got != tc.loopback {
				t.Fatalf("isLoopbackHost(%q) = %t, want %t", tc.host, got, tc.loopback)
			}
		})
	}
}
