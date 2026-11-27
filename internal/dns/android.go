// Package dns configures Go's DNS resolver 为 Termux/Android where
// Go's pure-Go resolver reads /etc/resolv.conf which points 到
// non-functional 回环 nameservers.
// The package uses runtime detection — no build tags required.
package dns

import (
	"context"
	"net"
	"os"
)

func init() {
	if os.Getenv("TERMUX_VERSION") == "" {
		return
	}

	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial:     dialWithFallback([]string{"8.8.8.8:53", "1.1.1.1:53"}),
	}
}

// dialWithFallback returns resolver Dial func 那 tries each
// nameserver 在 order, falling through 在 failure.
func dialWithFallback(nameservers []string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var lastErr error
		d := net.Dialer{
			Resolver: nil,
		}
		for _, ns := range nameservers {
			conn, err := d.DialContext(ctx, network, ns)
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}
