package probes

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agoda-com/macOS-vz-kubelet/internal/node"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func succeedingExec(_ context.Context, _, _, _ string, _ []string, _ *node.ExecIO) error {
	return nil
}

func failingExec(_ context.Context, _, _, _ string, _ []string, _ *node.ExecIO) error {
	return fmt.Errorf("command failed")
}

func TestRunExecProbe_Success(t *testing.T) {
	action := &corev1.ExecAction{Command: []string{"true"}}
	out := RunExecProbe(context.Background(), succeedingExec, "ns", "pod", "c", action)
	assert.Equal(t, Success, out)
}

func TestRunExecProbe_Failure(t *testing.T) {
	action := &corev1.ExecAction{Command: []string{"false"}}
	out := RunExecProbe(context.Background(), failingExec, "ns", "pod", "c", action)
	assert.Equal(t, Failure, out)
}

func TestRunHTTPProbe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Extract host and port from test server
	host, port := splitHostPort(t, srv.Listener.Addr().String())
	resolver := staticResolver(host)
	action := &corev1.HTTPGetAction{
		Port: intstr.FromString(port),
		Path: "/healthz",
	}
	out := RunHTTPProbe(context.Background(), resolver, "ns", "pod", "c", action, time.Second)
	assert.Equal(t, Success, out)
}

func TestRunHTTPProbe_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.Listener.Addr().String())
	resolver := staticResolver(host)
	action := &corev1.HTTPGetAction{
		Port: intstr.FromString(port),
		Path: "/healthz",
	}
	out := RunHTTPProbe(context.Background(), resolver, "ns", "pod", "c", action, time.Second)
	assert.Equal(t, Failure, out)
}

func TestRunHTTPProbe_NoIP(t *testing.T) {
	resolver := func(_ context.Context, _, _, _ string) (string, error) {
		return "", fmt.Errorf("no ip")
	}
	action := &corev1.HTTPGetAction{Port: intstr.FromInt32(80), Path: "/"}
	out := RunHTTPProbe(context.Background(), resolver, "ns", "pod", "c", action, time.Second)
	assert.Equal(t, Failure, out)
}

func TestRunTCPProbe_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	host, port := splitHostPort(t, ln.Addr().String())
	resolver := staticResolver(host)
	action := &corev1.TCPSocketAction{Port: intstr.FromString(port)}
	out := RunTCPProbe(context.Background(), resolver, "ns", "pod", "c", action, time.Second)
	assert.Equal(t, Success, out)
}

func TestRunTCPProbe_Refused(t *testing.T) {
	resolver := staticResolver("127.0.0.1")
	// Use a port that is almost certainly not listening
	action := &corev1.TCPSocketAction{Port: intstr.FromInt32(1)}
	out := RunTCPProbe(context.Background(), resolver, "ns", "pod", "c", action, 100*time.Millisecond)
	assert.Equal(t, Failure, out)
}

func staticResolver(ip string) IPResolver {
	return func(_ context.Context, _, _, _ string) (string, error) {
		return ip, nil
	}
}

func splitHostPort(t *testing.T, addr string) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}
