package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/transport"
)

func TestFlagValidationAndHelp(t *testing.T) {
	for _, tt := range []struct {
		name  string
		args  []string
		token string
		want  string
	}{
		{"missing_token", []string{"-insecure"}, "", "SDN_TOKEN"},
		{"short_token", []string{"-insecure"}, "short-secret", "SDN_TOKEN"},
		{"explicit_tls_required", nil, strings.Repeat("t", 24), "CA required"},
		{"empty_target", []string{"-addr", " "}, strings.Repeat("t", 24), "addr"},
		{"zero_timeout", []string{"-timeout", "0s"}, strings.Repeat("t", 24), "timeout"},
		{"negative_timeout", []string{"-timeout", "-1s"}, strings.Repeat("t", 24), "timeout"},
		{"positional_command", []string{"state"}, strings.Repeat("t", 24), "positional"},
		{"unknown_flag", []string{"-execute-shell"}, strings.Repeat("t", 24), "flag provided but not defined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := run(context.Background(), tt.args, func(key string) string {
				if key == "SDN_TOKEN" {
					return tt.token
				}
				return ""
			}, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v want=%q", err, tt.want)
			}
			if tt.token != "" && strings.Contains(err.Error()+stderr.String(), tt.token) {
				t.Fatal("diagnostic exposed token")
			}
		})
	}
	var help bytes.Buffer
	if err := run(context.Background(), []string{"-h"}, func(string) string { return "" }, &help); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"MCP stdio", "allow-writes", "SDN_TOKEN", "read-only"} {
		if !strings.Contains(help.String(), text) {
			t.Errorf("help missing %q", text)
		}
	}
}

// The child exits explicitly so the Go test harness never writes PASS to MCP
// stdout. Its stdin/stdout are the actual SDK stdio transport, not mock streams.
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("SDN_MCP_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if err := run(context.Background(), args, os.Getenv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type stdioAgent struct {
	pb.UnimplementedAgentServer
	begins atomic.Int32
}

func (*stdioAgent) GetCapabilities(context.Context, *pb.NodeRequest) (*pb.Capabilities, error) {
	return &pb.Capabilities{NodeId: "stdio-node", ProtocolVersion: 1}, nil
}
func (*stdioAgent) GetState(context.Context, *pb.NodeRequest) (*pb.State, error) {
	return &pb.State{NodeId: "stdio-node", NodeState: "RUNNING", Revision: 9007199254740993}, nil
}
func (a *stdioAgent) Begin(_ context.Context, q *pb.BeginRequest) (*pb.Transaction, error) {
	a.begins.Add(1)
	return &pb.Transaction{Id: "stdio-transaction", NodeId: q.NodeId, BaseRevision: q.BaseRevision}, nil
}

func makeTLSFiles(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MCP ephemeral test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	crt := filepath.Join(dir, "agent-cert.pem")
	pk := filepath.Join(dir, "agent-key.pem")
	if err = os.WriteFile(crt, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(pk, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return crt, pk
}

func TestStdioProtocolWithAuthenticatedAgent(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		writes, tls, badToken bool
	}{
		{"readonly_plaintext", false, false, false},
		{"enabled_writes", true, false, false},
		{"tls", false, true, false},
		{"invalid_bearer", false, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const token = "mcp-test-token-24-characters"
			var cert, key string
			if tt.tls {
				cert, key = makeTLSFiles(t)
			}
			gs, err := transport.Server(token, cert, key, !tt.tls)
			if err != nil {
				t.Fatal(err)
			}
			a := &stdioAgent{}
			pb.RegisterAgentServer(gs, a)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = gs.Serve(listener) }()
			t.Cleanup(func() { gs.Stop(); _ = listener.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestMCPHelperProcess$", "--", "-addr", listener.Addr().String(), "-timeout", "2s"}
			if tt.tls {
				args = append(args, "-ca", cert)
			} else {
				args = append(args, "-insecure")
			}
			if tt.writes {
				args = append(args, "-allow-writes")
			}
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "SDN_TOKEN=") && !strings.HasPrefix(value, "SDN_MCP_TEST_HELPER=") {
					cmd.Env = append(cmd.Env, value)
				}
			}
			clientToken := token
			if tt.badToken {
				clientToken = "different-test-token-24-chars"
			}
			cmd.Env = append(cmd.Env, "SDN_MCP_TEST_HELPER=1", "SDN_TOKEN="+clientToken)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			cs, err := sdk.NewClient(&sdk.Implementation{Name: "stdio-integration-test", Version: "1"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
			if err != nil {
				t.Fatalf("initialize: %v stderr=%s", err, &stderr)
			}
			closed := false
			defer func() {
				if !closed {
					_ = cs.Close()
				}
			}()
			listed, err := cs.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 6
			if tt.writes {
				want = 12
			}
			if len(listed.Tools) != want {
				t.Fatalf("listed %d tools, want %d", len(listed.Tools), want)
			}
			r, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "sdn_state", Arguments: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			if tt.badToken {
				if !r.IsError || !strings.Contains(fmt.Sprint(r.StructuredContent), "Unauthenticated") {
					t.Fatalf("invalid token was not rejected by Agent: %+v", r)
				}
			} else {
				if r.IsError {
					t.Fatalf("state error: %+v", r)
				}
				data, ok := r.StructuredContent.(map[string]any)
				if !ok || data["node_id"] != "stdio-node" || data["revision"] != "9007199254740993" {
					t.Fatalf("stdio state corrupted: %#v", r.StructuredContent)
				}
				if tt.writes {
					r, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "sdn_begin", Arguments: map[string]any{"base_revision": "9007199254740993", "request_id": "stdio-request"}})
					if err != nil || r.IsError || a.begins.Load() != 1 {
						t.Fatalf("write forwarding: result=%+v err=%v count=%d", r, err, a.begins.Load())
					}
				}
			}
			if err = cs.Close(); err != nil {
				t.Fatalf("stdin close did not cleanly exit: %v", err)
			}
			closed = true
			if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
				t.Fatalf("unclean child exit: %v stderr=%s", cmd.ProcessState, &stderr)
			}
			if strings.Contains(stderr.String(), token) || strings.Contains(stderr.String(), clientToken) {
				t.Fatal("child diagnostic exposed credential")
			}
		})
	}
}
