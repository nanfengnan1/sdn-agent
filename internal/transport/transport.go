package transport

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func authenticated(ctx context.Context, token string) error {
	m, _ := metadata.FromIncomingContext(ctx)
	v := m.Get("authorization")
	if len(v) != 1 || subtle.ConstantTimeCompare([]byte(v[0]), []byte("Bearer "+token)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	return nil
}
func Server(token, cert, key string, allowInsecure bool) (*grpc.Server, error) {
	if len(token) < 16 {
		return nil, fmt.Errorf("SDN_TOKEN must contain at least 16 bytes")
	}
	opts := []grpc.ServerOption{grpc.MaxRecvMsgSize(4 << 20), grpc.UnaryInterceptor(func(ctx context.Context, r any, i *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if e := authenticated(ctx, token); e != nil {
			return nil, e
		}
		return h(ctx, r)
	}), grpc.StreamInterceptor(func(s any, stream grpc.ServerStream, i *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		if e := authenticated(stream.Context(), token); e != nil {
			return e
		}
		return h(s, stream)
	})}
	if cert != "" && key != "" {
		c, e := credentials.NewServerTLSFromFile(cert, key)
		if e != nil {
			return nil, e
		}
		opts = append(opts, grpc.Creds(c))
	} else if !allowInsecure {
		return nil, fmt.Errorf("TLS cert/key required; --insecure is only for an isolated lab")
	}
	return grpc.NewServer(opts...), nil
}
func Dial(addr, token, ca string, allowInsecure bool) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if ca != "" {
		c, e := credentials.NewClientTLSFromFile(ca, "")
		if e != nil {
			return nil, e
		}
		creds = c
	} else if allowInsecure {
		creds = insecure.NewCredentials()
	} else {
		return nil, fmt.Errorf("CA required; use --insecure explicitly for lab")
	}
	authorize := func(ctx context.Context) context.Context {
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+strings.TrimSpace(token))
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(creds), grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return inv(authorize(ctx), m, req, reply, cc, opts...)
	}), grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, m string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(authorize(ctx), desc, cc, m, opts...)
	}))
}
