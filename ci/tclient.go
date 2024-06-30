package ci

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"

	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func getTemporalClient(ctx context.Context, paramSrc string) (client.Client, string, error) {
	params, err := getParams(paramSrc)
	if err != nil {
		return nil, "", err
	}
	parts := strings.SplitN(params, "~", 3)
	if len(parts) < 3 {
		return nil, "", errors.New("bad params format")
	}
	hostPort, namespace, apiKey := parts[0], parts[1], parts[2]

	co := client.Options{
		HostPort:  hostPort,
		Namespace: namespace,
	}
	if apiKey != "" {
		co.Credentials = client.NewAPIKeyStaticCredentials(apiKey)
		// TODO: remove after go sdk does this automatically
		co.ConnectionOptions = client.ConnectionOptions{
			TLS: &tls.Config{},
			DialOptions: []grpc.DialOption{
				grpc.WithUnaryInterceptor(
					func(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
						return invoker(
							metadata.AppendToOutgoingContext(ctx, "temporal-namespace", namespace),
							method,
							req,
							reply,
							cc,
							opts...,
						)
					},
				),
			},
		}
	}
	c, err := client.DialContext(ctx, co)
	return c, namespace, err
}
