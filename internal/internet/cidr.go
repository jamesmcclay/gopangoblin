package internet

import (
	"fmt"
	"net"
	"strings"
)

// parseStaticCIDR parses value as a literal "x.x.x.x/nn" CIDR. ok is
// false (with no error) if value isn't a literal CIDR at all -- e.g. a
// "$variable" -- since that's an expected, non-error case for callers
// deciding whether something can be auto-derived from it.
func parseStaticCIDR(value string) (ip net.IP, ipnet *net.IPNet, ok bool) {
	if value == "" || strings.HasPrefix(value, "$") {
		return nil, nil, false
	}
	ip, ipnet, err := net.ParseCIDR(value)
	if err != nil {
		return nil, nil, false
	}
	return ip, ipnet, true
}

func netmaskDotted(ipnet *net.IPNet) string {
	return net.IP(ipnet.Mask).String()
}

func addOffset(base net.IP, offset uint32) net.IP {
	ip4 := base.To4()
	v := uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
	v += offset
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).To4()
}

// lastHalfPool returns a DHCP ip_pool range string covering the upper
// half of ipnet's usable host addresses (e.g. "10.0.0.128-10.0.0.254" for
// a /24), excluding the broadcast address.
func lastHalfPool(ipnet *net.IPNet) (string, error) {
	network := ipnet.IP.To4()
	if network == nil {
		return "", fmt.Errorf("only IPv4 is supported")
	}
	ones, bits := ipnet.Mask.Size()
	hostBits := bits - ones
	if hostBits < 2 {
		return "", fmt.Errorf("network %s is too small to derive a DHCP pool", ipnet.String())
	}

	total := uint32(1) << uint(hostBits)
	start := addOffset(network, total/2)
	end := addOffset(network, total-2) // exclude the broadcast address
	return start.String() + "-" + end.String(), nil
}
