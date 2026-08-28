package daemon

import "context"

type (
	allocateContext struct {
		sph         Sph
		forManifest bool
	}

	daemonCtxKey int
)

var (
	allocateCtxKey any = daemonCtxKey(1)
)

func withAllocateCtx(ctx context.Context, sph Sph, forManifest bool) context.Context {
	return context.WithValue(ctx, allocateCtxKey, allocateContext{sph: sph, forManifest: forManifest})
}

func fromAllocateCtx(ctx context.Context) (Sph, bool, bool) {
	actx, ok := ctx.Value(allocateCtxKey).(allocateContext)
	return actx.sph, actx.forManifest, ok
}
