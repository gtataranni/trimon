package probe

import (
	"context"
	"testing"
)

func TestExpandTargets(t *testing.T) {
	ctx := context.Background()

	t.Run("bare IP", func(t *testing.T) {
		items := ExpandTargets(ctx, []string{"8.8.8.8"}, 0)
		if len(items) != 1 {
			t.Fatalf("want 1 item, got %d", len(items))
		}
		if items[0].IP != "8.8.8.8" {
			t.Errorf("IP: got %q, want %q", items[0].IP, "8.8.8.8")
		}
		if items[0].FQDN != "" {
			t.Errorf("FQDN should be empty for bare IP, got %q", items[0].FQDN)
		}
	})

	t.Run("FQDN hit", func(t *testing.T) {
		items := ExpandTargets(ctx, []string{"localhost"}, 0)
		if len(items) == 0 {
			t.Fatal("want at least one item for localhost")
		}
		for _, item := range items {
			if item.FQDN != "localhost" {
				t.Errorf("FQDN: got %q, want %q", item.FQDN, "localhost")
			}
			if item.IP == "localhost" {
				t.Errorf("IP should be a resolved address, not the hostname itself")
			}
		}
	})

	t.Run("FQDN miss emits sentinel", func(t *testing.T) {
		// invalid.. is an illegal label; LookupHost fails immediately without a network call.
		items := ExpandTargets(ctx, []string{"invalid..hostname"}, 0)
		if len(items) != 1 {
			t.Fatalf("want 1 sentinel item, got %d", len(items))
		}
		if items[0].IP != items[0].FQDN {
			t.Errorf("sentinel must have IP == FQDN, got IP=%q FQDN=%q", items[0].IP, items[0].FQDN)
		}
	})

	t.Run("maxResolvedIPs cap", func(t *testing.T) {
		// localhost may resolve to multiple addresses (127.0.0.1, ::1, …); cap at 1.
		items := ExpandTargets(ctx, []string{"localhost"}, 1)
		if len(items) != 1 {
			t.Errorf("want 1 item with cap=1, got %d", len(items))
		}
	})
}
