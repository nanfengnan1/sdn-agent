// sdn-agent-mcp serves MCP over stdio and forwards tools to one Agent over gRPC.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/transport"
	sdnmcp "sdn-agent/mcp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sdn-agent-mcp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	flags := flag.NewFlagSet("sdn-agent-mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	addr := flags.String("addr", "127.0.0.1:9090", "fixed Agent gRPC address")
	node := flags.String("node", "", "expected node UUID (discover when empty)")
	ca := flags.String("ca", "", "Agent CA PEM file for TLS")
	insecure := flags.Bool("insecure", false, "allow plaintext gRPC in an isolated lab")
	timeout := flags.Duration("timeout", 30*time.Second, "maximum duration of each tool call")
	writes := flags.Bool("allow-writes", false, "expose configuration transaction and rollback tools")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: sdn-agent-mcp [flags]\nMCP stdio server. Set SDN_TOKEN in the process environment. Default: read-only tools.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments; this command serves MCP over stdin/stdout")
	}
	if strings.TrimSpace(*addr) == "" {
		return fmt.Errorf("-addr must not be empty")
	}
	if *timeout <= 0 {
		return fmt.Errorf("-timeout must be positive")
	}
	token := strings.TrimSpace(getenv("SDN_TOKEN"))
	if len(token) < 16 {
		return fmt.Errorf("SDN_TOKEN must contain at least 16 bytes")
	}
	conn, err := transport.Dial(strings.TrimSpace(*addr), token, *ca, *insecure)
	if err != nil {
		return err
	}
	defer conn.Close()
	server, err := sdnmcp.NewServer(pb.NewAgentClient(conn), sdnmcp.Options{
		NodeID: strings.TrimSpace(*node), AllowWrites: *writes, Timeout: *timeout,
	})
	if err != nil {
		return err
	}
	// stdout belongs exclusively to MCP; diagnostics and flag help use stderr.
	err = server.Run(ctx, &sdk.StdioTransport{MaxLineLength: 4 << 20})
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}
