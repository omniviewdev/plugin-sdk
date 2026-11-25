package networker

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"unicode/utf8"
)

const (
	// localhostName is the one name, besides an IP address, that a forwarder may
	// state as the host its listener is bound to. It is the same name in any case
	// of ASCII letters.
	localhostName = "localhost"

	// maxLocalHostBytes bounds a stated host. The longest IP address in text,
	// zone included, is well under it.
	maxLocalHostBytes = 255

	// maxZoneBytes bounds the zone of a stated IPv6 address, an interface name
	// or number: at most 15 bytes on Linux, and the other systems' are no longer.
	maxZoneBytes = 64
)

// Why a value cannot be stated as a session's local host. The class is what the
// manager logs, with the length of the value, in place of the value: a forwarder
// that stated a credential by mistake must not find it in the log.
const (
	localHostTooLong      = "too-long"
	localHostNotUTF8      = "not-utf8"
	localHostBadZone      = "bad-zone"
	localHostNotAnAddress = "not-an-address"
)

// LocalHostOf names the host a forwarder's listeners are bound to, from the
// addresses they really bound (net.Listener.Addr), for ForwarderResult.LocalHost.
//
// It states what the addresses say and nothing else: one address gives its host
// ("127.0.0.1", "::1", "0.0.0.0", "::", or an address with a zone); addresses
// that share a host give that host; loopback addresses of different hosts, as
// 127.0.0.1 and ::1 (which is what a listener on "localhost" binds), give
// "localhost". It returns "" (the forwarder does not say) for no address, for an
// address that is not a TCP or UDP address or has no IP, for an address whose
// zone is not an interface name of the kinds a session states (letters, digits,
// '.', '_' and '-', at most 64 bytes), and for addresses on different hosts that
// are not all loopback: a forward that listens on a loopback address and on a
// wildcard one is reachable beyond this computer, which no single host states.
func LocalHostOf(addrs ...net.Addr) string {
	host := ""
	for i, addr := range addrs {
		one := hostOf(addr)
		switch {
		case one == "":
			return ""
		case i == 0:
			host = one
		case one != host:
			if !isLoopbackHost(one) || !isLoopbackHost(host) {
				return ""
			}

			host = localhostName
		}
	}

	return host
}

// hostOf is the host of a listener's bound address, or "" when it has none or
// has one that a session cannot state.
func hostOf(addr net.Addr) string {
	var bound netip.AddrPort
	switch a := addr.(type) {
	case *net.TCPAddr:
		if a == nil {
			return ""
		}

		bound = a.AddrPort()
	case *net.UDPAddr:
		if a == nil {
			return ""
		}

		bound = a.AddrPort()
	default:
		return ""
	}

	if !bound.Addr().IsValid() {
		return ""
	}

	host := bound.Addr().Unmap().String()
	if localHostFault(host) != "" {
		return ""
	}

	return host
}

// isLoopbackHost is the rule by which a reader of a session's local host decides
// that the forward is reachable only from this computer (ForwarderResult.LocalHost
// and docs/capabilities.md state it): the host is "localhost" in any case, or an
// IP address that is loopback once an IPv4-mapped IPv6 address is unmapped
// (127.0.0.0/8, ::1, ::ffff:127.x.y.z). Everything else is not loopback:
// 0.0.0.0 and :: (every interface), a LAN or link-local address, a name, and
// the empty host, which says nothing.
func isLoopbackHost(host string) bool {
	if isLocalhostName(host) {
		return true
	}

	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}

// isLocalhostName reports whether host is "localhost" in any case of ASCII
// letters. The length is compared first: a letter that Unicode folds to an ASCII
// one (a long s) takes more bytes than the letter it stands for.
func isLocalhostName(host string) bool {
	return len(host) == len(localhostName) && strings.EqualFold(host, localhostName)
}

// localHostFault says why host cannot be stated as a session's local host, as one
// of the classes above, or is "" when it can: "localhost", or an IP address (an
// IPv6 address may have a zone of letters, digits, '.', '_' and '-'), as valid
// UTF-8 of at most maxLocalHostBytes. A value that fails any of these is the
// forwarder's mistake, and one that reached the protobuf encoder would make the
// plugin's Get and List fail for as long as the session lives. The length is
// checked first, so a huge value is not scanned.
func localHostFault(host string) string {
	switch {
	case len(host) > maxLocalHostBytes:
		return localHostTooLong
	case !utf8.ValidString(host):
		return localHostNotUTF8
	case isLocalhostName(host):
		return ""
	}

	if _, zone, zoned := strings.Cut(host, "%"); zoned && !validZone(zone) {
		return localHostBadZone
	}

	if _, err := netip.ParseAddr(host); err != nil {
		return localHostNotAnAddress
	}

	return ""
}

// validZone reports whether zone is a name or number of an interface of the kinds
// a session states: letters, digits, '.', '_' and '-', not empty, at most
// maxZoneBytes. The bytes of a zone are the operating system's, not text.
func validZone(zone string) bool {
	if zone == "" || len(zone) > maxZoneBytes {
		return false
	}

	for i := range len(zone) {
		c := zone[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		if !letter && !digit && c != '.' && c != '_' && c != '-' {
			return false
		}
	}

	return true
}

// statedLocalHost is the host a forwarder stated for a session, as the session
// states it: the same host when it is "localhost" or an IP address, and "" when
// the forwarder stated none or stated something that cannot be stated. The manager
// never supplies a host of its own, and never logs a value that was rejected: it
// logs how long it was and which class of mistake it is.
func (m *Manager) statedLocalHost(ctx context.Context, sessionID, host string) string {
	if host == "" {
		return ""
	}

	fault := localHostFault(host)
	if fault == "" {
		return host
	}

	m.log.Warnw(ctx, "port forwarder stated a local host that cannot be stated; the session states none",
		"session_id", sessionID, "local_host_class", fault, "local_host_length", len(host))
	return ""
}
