package mobile

import (
	"net"
	"sort"
	"strings"
)

type scoredIP struct {
	ip    string
	score int
}

// DiscoverLANIPs enumerates active non-loopback IPv4 addresses sorted by usefulness for
// LAN phone pairing (physical Wi-Fi/Ethernet LAN subnets first, Tailscale/VPN next, virtual bridges last).
func DiscoverLANIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var cands []scoredIP
	seen := make(map[string]bool)

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		nameLower := strings.ToLower(iface.Name)
		isVirtual := strings.HasPrefix(nameLower, "docker") ||
			strings.HasPrefix(nameLower, "br-") ||
			strings.HasPrefix(nameLower, "veth") ||
			strings.HasPrefix(nameLower, "virbr") ||
			strings.HasPrefix(nameLower, "cni")

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			ipStr := ip4.String()
			if seen[ipStr] {
				continue
			}
			seen[ipStr] = true

			score := 10
			switch {
			case ip4[0] == 192 && ip4[1] == 168:
				score = 100
			case ip4[0] == 10:
				score = 90
			case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
				score = 75
			case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // Tailscale / CGNAT
				score = 85
			}
			if isVirtual {
				score -= 60
			}
			cands = append(cands, scoredIP{ip: ipStr, score: score})
		}
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].ip < cands[j].ip
	})

	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.ip)
	}
	return out
}
