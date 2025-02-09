package ci

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/dnr/styx/common"
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

	dc := converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), zstdcodec{})
	fc := temporal.NewDefaultFailureConverter(temporal.DefaultFailureConverterOptions{DataConverter: dc})

	co := client.Options{
		HostPort:         hostPort,
		Namespace:        namespace,
		DataConverter:    dc,
		FailureConverter: fc,
		Logger:           log.NewStructuredLogger(slog.Default()),
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

type zstdcodec struct{}

func (zstdcodec) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	z := common.GetZstdCtxPool().Get()
	defer common.GetZstdCtxPool().Put(z)
	out := slices.Clone(payloads)
	for i, p := range payloads {
		zd, err := z.Compress(nil, p.Data)
		if err != nil {
			return nil, err
		}
		if len(zd)+24 >= len(p.Data) {
			continue
		}
		np := &commonpb.Payload{
			Metadata: maps.Clone(p.Metadata),
			Data:     zd,
		}
		if np.Metadata == nil {
			np.Metadata = make(map[string][]byte)
		}
		np.Metadata["styx/cmp"] = []byte("zst")
		payloads[i] = np
	}
	return out, nil
}

func (zstdcodec) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	z := common.GetZstdCtxPool().Get()
	defer common.GetZstdCtxPool().Put(z)
	out := slices.Clone(payloads)
	for i, p := range payloads {
		cmp := string(p.Metadata["styx/cmp"])
		if cmp != "zstd" && cmp != "zst" {
			continue
		}
		d, err := z.Decompress(nil, p.Data)
		if err != nil {
			return nil, err
		}
		np := &commonpb.Payload{
			Metadata: maps.Clone(p.Metadata),
			Data:     d,
		}
		delete(np.Metadata, "styx/cmp")
		payloads[i] = np
	}
	return out, nil
}
