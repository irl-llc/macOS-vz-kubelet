package probes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/agoda-com/macOS-vz-kubelet/internal/node"
	corev1 "k8s.io/api/core/v1"
)

// ExecRunner executes a command in a container and returns an error on failure.
type ExecRunner func(ctx context.Context, ns, pod, container string, cmd []string, attach *node.ExecIO) error

// IPResolver returns the IP address reachable for a given container.
type IPResolver func(ctx context.Context, ns, pod, container string) (string, error)

// RunExecProbe executes a command probe and returns the outcome.
func RunExecProbe(ctx context.Context, exec ExecRunner, ns, pod, container string, action *corev1.ExecAction) Outcome {
	buf := &bytes.Buffer{}
	attach := node.NewExecIO(false, nil, nopWriteCloser{buf}, nopWriteCloser{io.Discard}, nil)
	err := exec(ctx, ns, pod, container, action.Command, attach)
	if err != nil {
		return Failure
	}
	return Success
}

// RunHTTPProbe performs an HTTP GET probe and returns the outcome.
func RunHTTPProbe(ctx context.Context, resolver IPResolver, ns, pod, container string, action *corev1.HTTPGetAction, timeout time.Duration) Outcome {
	ip, err := resolver(ctx, ns, pod, container)
	if err != nil || ip == "" {
		return Failure
	}
	url := buildHTTPURL(action, ip)
	return doHTTPGet(ctx, url, action.HTTPHeaders, timeout)
}

func buildHTTPURL(action *corev1.HTTPGetAction, ip string) string {
	scheme := "http"
	if action.Scheme == corev1.URISchemeHTTPS {
		scheme = "https"
	}
	port := action.Port.String()
	path := action.Path
	return fmt.Sprintf("%s://%s:%s%s", scheme, ip, port, path)
}

func doHTTPGet(ctx context.Context, url string, headers []corev1.HTTPHeader, timeout time.Duration) Outcome {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Failure
	}
	for _, h := range headers {
		req.Header.Set(h.Name, h.Value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Failure
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return Success
	}
	return Failure
}

// RunTCPProbe attempts a TCP connection and returns the outcome.
func RunTCPProbe(ctx context.Context, resolver IPResolver, ns, pod, container string, action *corev1.TCPSocketAction, timeout time.Duration) Outcome {
	ip, err := resolver(ctx, ns, pod, container)
	if err != nil || ip == "" {
		return Failure
	}
	addr := net.JoinHostPort(ip, action.Port.String())
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return Failure
	}
	conn.Close()
	return Success
}

// nopWriteCloser wraps an io.Writer to satisfy io.WriteCloser.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
