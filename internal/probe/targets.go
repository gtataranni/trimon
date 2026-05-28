package probe

import (
	"context"
	"net"
)

type WorkItem struct {
	IP   string
	FQDN string
}

// Unresolvable FQDNs return a WorkItem with IP == FQDN so callers can emit
// StatusError rather than silently dropping the target.
func ExpandTargets(ctx context.Context, targets []string, maxResolvedIPs int) []WorkItem {
	var items []WorkItem
	for _, entry := range targets {
		if net.ParseIP(entry) != nil {
			items = append(items, WorkItem{IP: entry})
			continue
		}
		addrs, err := net.DefaultResolver.LookupHost(ctx, entry)
		if err != nil || len(addrs) == 0 {
			items = append(items, WorkItem{IP: entry, FQDN: entry})
			continue
		}
		seen := make(map[string]bool, len(addrs))
		for _, addr := range addrs {
			if seen[addr] {
				continue
			}
			seen[addr] = true
			items = append(items, WorkItem{IP: addr, FQDN: entry})
			if maxResolvedIPs > 0 && len(seen) >= maxResolvedIPs {
				break
			}
		}
	}
	return items
}
