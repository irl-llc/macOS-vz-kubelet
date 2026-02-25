package provider

import (
	"context"
	"fmt"
	"io"
	"net"
)

// proxyTCP dials the given IP:port and bidirectionally copies data to/from
// the stream, implementing kubectl port-forward.
func proxyTCP(ctx context.Context, ip string, port int32, stream io.ReadWriteCloser) error {
	defer stream.Close()

	addr := fmt.Sprintf("%s:%d", ip, port)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(conn, stream); errCh <- err }()
	go func() { _, err := io.Copy(stream, conn); errCh <- err }()

	// Wait for first completion or context cancellation
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
