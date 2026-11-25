package networker_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker/networktest"
)

func tcpAddr(address string) *net.TCPAddr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(address))
}

// LocalHostOf states what the addresses a forwarder's listeners really bound
// say, and nothing it cannot read from them.
func TestLocalHostOf(t *testing.T) {
	mapped := &net.TCPAddr{IP: netip.MustParseAddr("::ffff:127.0.0.1").AsSlice(), Port: 8080}
	zoned := func(zone string) *net.TCPAddr {
		return &net.TCPAddr{IP: net.ParseIP("fe80::1"), Zone: zone, Port: 8080}
	}

	for _, tc := range []struct {
		name  string
		addrs []net.Addr
		want  string
	}{
		{"loopback v4", []net.Addr{tcpAddr("127.0.0.1:8080")}, "127.0.0.1"},
		{"loopback v6", []net.Addr{tcpAddr("[::1]:8080")}, "::1"},
		{"all interfaces v4", []net.Addr{tcpAddr("0.0.0.0:8080")}, "0.0.0.0"},
		{"all interfaces v6", []net.Addr{tcpAddr("[::]:8080")}, "::"},
		{"another loopback address", []net.Addr{tcpAddr("127.0.0.2:8080")}, "127.0.0.2"},
		{"a private address", []net.Addr{tcpAddr("10.0.0.7:8080")}, "10.0.0.7"},
		{"v4 held in 16 bytes", []net.Addr{&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}}, "127.0.0.1"},
		{"v4 mapped into v6", []net.Addr{mapped}, "127.0.0.1"},
		{"a zone is kept", []net.Addr{zoned("en0")}, "fe80::1%en0"},
		{"a zone with dots, digits, hyphens and underscores is kept", []net.Addr{zoned("br-0.1_a")}, "fe80::1%br-0.1_a"},
		{"a zone that is not UTF-8", []net.Addr{zoned("\xff\xfe")}, ""},
		{"a zone with control characters", []net.Addr{zoned("\x01\x02")}, ""},
		{"a zone with a space", []net.Addr{zoned("en 0")}, ""},
		{"a zone that is too long", []net.Addr{zoned(strings.Repeat("a", 65))}, ""},
		{"udp", []net.Addr{&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}}, "127.0.0.1"},
		{"the same host twice", []net.Addr{tcpAddr("127.0.0.1:8080"), tcpAddr("127.0.0.1:8081")}, "127.0.0.1"},
		{"both loopback families", []net.Addr{tcpAddr("127.0.0.1:8080"), tcpAddr("[::1]:8080")}, "localhost"},
		{"both loopback families, v6 first", []net.Addr{tcpAddr("[::1]:8080"), tcpAddr("127.0.0.1:8080")}, "localhost"},
		{
			"both loopback families and a repeat",
			[]net.Addr{tcpAddr("127.0.0.1:8080"), tcpAddr("[::1]:8080"), tcpAddr("127.0.0.1:8081")},
			"localhost",
		},
		{"loopback and all interfaces", []net.Addr{tcpAddr("127.0.0.1:8080"), tcpAddr("0.0.0.0:8080")}, ""},
		{"loopback v6 and all interfaces v6", []net.Addr{tcpAddr("[::1]:8080"), tcpAddr("[::]:8080")}, ""},
		{"two hosts that are not loopback", []net.Addr{tcpAddr("10.0.0.7:8080"), tcpAddr("10.0.0.8:8080")}, ""},
		{"no address", nil, ""},
		{"an address that is nil", []net.Addr{nil}, ""},
		{"a typed nil address", []net.Addr{(*net.TCPAddr)(nil)}, ""},
		{"an address with no IP", []net.Addr{&net.TCPAddr{Port: 8080}}, ""},
		{"a unix socket", []net.Addr{&net.UnixAddr{Name: "/tmp/forward.sock", Net: "unix"}}, ""},
		{
			"one that cannot be read among ones that can",
			[]net.Addr{tcpAddr("127.0.0.1:8080"), &net.UnixAddr{Name: "/tmp/forward.sock", Net: "unix"}},
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := networker.LocalHostOf(tc.addrs...); got != tc.want {
				t.Fatalf("LocalHostOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// The host comes from the address a listener really bound, as net reports it.
func TestLocalHostOfRealListeners(t *testing.T) {
	listen := func(t *testing.T, network, address string) net.Listener {
		t.Helper()
		var config net.ListenConfig
		listener, err := config.Listen(t.Context(), network, address)
		if err != nil {
			t.Skipf("cannot listen on %s: %v", address, err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		return listener
	}

	t.Run("loopback v4", func(t *testing.T) {
		listener := listen(t, "tcp4", "127.0.0.1:0")
		if got := networker.LocalHostOf(listener.Addr()); got != "127.0.0.1" {
			t.Fatalf("LocalHostOf(%v) = %q, want 127.0.0.1", listener.Addr(), got)
		}
	})

	t.Run("loopback v6", func(t *testing.T) {
		listener := listen(t, "tcp6", "[::1]:0")
		if got := networker.LocalHostOf(listener.Addr()); got != "::1" {
			t.Fatalf("LocalHostOf(%v) = %q, want ::1", listener.Addr(), got)
		}
	})

	t.Run("both loopback families", func(t *testing.T) {
		v4 := listen(t, "tcp4", "127.0.0.1:0")
		v6 := listen(t, "tcp6", "[::1]:0")
		if got := networker.LocalHostOf(v4.Addr(), v6.Addr()); got != "localhost" {
			t.Fatalf("LocalHostOf(%v, %v) = %q, want localhost", v4.Addr(), v6.Addr(), got)
		}
	})
}

// A forwarder states where it listens, and the session states what the forwarder
// stated: the manager adds nothing of its own.
func TestManagerSessionStatesTheHostTheForwarderStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
	}{
		{"loopback v4", "127.0.0.1"},
		{"loopback v6", "::1"},
		{"localhost", "localhost"},
		{"all interfaces", "0.0.0.0"},
		{"an address with a zone", "fe80::1%en0"},
		{"a zone with dots, digits, hyphens and underscores", "fe80::1%br-0.1_a"},
		{"localhost in capitals", "LOCALHOST"},
		{"an IPv4-mapped loopback address, as stated", "::ffff:127.0.0.1"},
		{"not stated", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := networktest.Mount(t,
				networktest.WithResourceForwarder("core::v1::Pod", &networktest.StubResourceForwarder{LocalHost: tc.host}),
				networktest.WithStaticForwarder("fixture", &networktest.StubStaticForwarder{LocalHost: tc.host}),
			)

			for _, options := range []networker.PortForwardSessionOptions{
				{
					ConnectionType: networker.PortForwardConnectionTypeResource,
					Connection:     networker.PortForwardResourceConnection{ResourceKey: "core::v1::Pod", ResourceID: "my-pod"},
					RemotePort:     8080,
					Protocol:       networker.PortForwardProtocolTCP,
				},
				{
					ConnectionType: networker.PortForwardConnectionTypeStatic,
					Connection:     networker.PortForwardStaticConnection{Address: "fixture"},
					RemotePort:     8080,
					Protocol:       networker.PortForwardProtocolTCP,
				},
			} {
				started, err := h.StartSession(options)
				if err != nil {
					t.Fatalf("StartSession(%s): %v", options.ConnectionType, err)
				}
				if started.LocalHost != tc.host {
					t.Fatalf("%s session starts with local host %q, want %q", options.ConnectionType, started.LocalHost, tc.host)
				}
				if started.LocalPort == 0 {
					t.Fatalf("%s session has no local port", options.ConnectionType)
				}

				read, err := h.GetSession(started.ID)
				if err != nil {
					t.Fatalf("GetSession: %v", err)
				}
				if read.LocalHost != tc.host {
					t.Fatalf("%s session reads back with local host %q, want %q", options.ConnectionType, read.LocalHost, tc.host)
				}
			}
		})
	}
}

// A port the manager picked says nothing about where the forwarder listens, so a
// forwarder that states no host leaves the session with none, and JSON says none.
func TestManagerSessionNeverDefaultsTheLocalHost(t *testing.T) {
	h := networktest.Mount(t, networktest.WithStaticForwarder("fixture", &networktest.StubStaticForwarder{}))
	started, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeStatic,
		Connection:     networker.PortForwardStaticConnection{Address: "fixture"},
		RemotePort:     8080,
		Protocol:       networker.PortForwardProtocolTCP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.LocalHost != "" {
		t.Fatalf("a forwarder that states no host left the session with local host %q", started.LocalHost)
	}

	data, err := json.Marshal(started)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "local_host") {
		t.Fatalf("a session with no local host writes one in JSON: %s", data)
	}
}

// What is not an address is the forwarder's mistake, not a host: the session
// states none and still has a frame that marshals (a rejected value that
// reached the encoder would fail Get and List for the plugin for as long as the
// session lives), and the manager says so once in the log, with the length of
// what was stated and a class, never the value.
func TestManagerSessionDropsALocalHostThatIsNotAnAddress(t *testing.T) {
	credential := "AKIAIOSFODNN7EXAMPLE:wJalrXUtnFEMI@forward.example.com"
	for _, tc := range []struct {
		name  string
		host  string
		class string
	}{
		{"an address with a port", "127.0.0.1:8080", "not-an-address"},
		{"an address in brackets", "[::1]", "not-an-address"},
		{"a URL", "http://127.0.0.1", "not-an-address"},
		{"an address with a space", " 127.0.0.1", "not-an-address"},
		{"a name that is not localhost", "forward.example.com", "not-an-address"},
		{"a lookalike of localhost that Unicode folds to it", "localho\u017ft", "not-an-address"},
		{"an IPv4 address with a zone", "127.0.0.1%en0", "not-an-address"},
		{"a value that looks like a credential", credential, "not-an-address"},
		{"too long", strings.Repeat("1", 300), "too-long"},
		{"text that is not UTF-8", "127.0.0.1\xff", "not-utf8"},
		{"a zone that is not UTF-8", "fe80::1%\xff\xfe", "not-utf8"},
		{"a zone with control characters", "fe80::1%\x01\x02", "bad-zone"},
		{"a zone with a space", "fe80::1%en 0", "bad-zone"},
		{"an empty zone", "fe80::1%", "bad-zone"},
		{"a zone that is too long", "fe80::1%" + strings.Repeat("a", 65), "bad-zone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &logtest.RecordingSink{}
			logger := logging.New(logging.Config{
				Backend: logging.NewSinkBackend(sink),
				Level:   logging.NewLevelController(logging.LevelDebug),
			})
			h := networktest.Mount(t,
				networktest.WithLogger(logger),
				networktest.WithStaticForwarder("fixture", &networktest.StubStaticForwarder{LocalHost: tc.host}),
			)

			started, err := h.StartSession(networker.PortForwardSessionOptions{
				ConnectionType: networker.PortForwardConnectionTypeStatic,
				Connection:     networker.PortForwardStaticConnection{Address: "fixture"},
				RemotePort:     8080,
				Protocol:       networker.PortForwardProtocolTCP,
			})
			if err != nil {
				t.Fatalf("StartSession: %v", err)
			}
			if started.LocalHost != "" {
				t.Fatalf("local host %q was stated for a session whose forwarder stated %q", started.LocalHost, tc.host)
			}

			frame, err := started.ToProto()
			if err != nil {
				t.Fatalf("ToProto: %v", err)
			}
			if _, err := proto.Marshal(frame); err != nil {
				t.Fatalf("the session's frame does not marshal: %v", err)
			}

			var warnings []logging.Record
			for _, record := range sink.Records() {
				if record.Level == logging.LevelWarn {
					warnings = append(warnings, record)
				}
			}
			if len(warnings) != 1 {
				t.Fatalf("expected one warning that the local host is not an address, got %d", len(warnings))
			}

			fields := map[string]any{}
			logged := warnings[0].Message
			for _, field := range warnings[0].Fields {
				fields[field.Key] = field.Value
				if field.Key != "session_id" {
					logged += " " + field.Key + "=" + fmt.Sprint(field.Value)
				}
			}
			if fields["local_host_class"] != tc.class || fields["local_host_length"] != len(tc.host) {
				t.Fatalf("the warning says %v of length %v, want %q of length %d",
					fields["local_host_class"], fields["local_host_length"], tc.class, len(tc.host))
			}
			if strings.Contains(logged, tc.host) {
				t.Fatalf("the warning carries the value stated: %q", logged)
			}
			if tc.host == credential {
				for _, piece := range []string{"AKIA", "wJalr", "EXAMPLE"} {
					if strings.Contains(logged, piece) {
						t.Fatalf("the warning carries part of the value stated (%q): %q", piece, logged)
					}
				}
			}
		})
	}
}
