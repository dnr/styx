package daemon

import "context"

type (
	allocateContext struct {
		sph         Sph
		forManifest bool
	}

	mountContext struct {
		imageSize int64
		isBare    bool
		imageData []byte
	}
)

var (
	allocateCtxKey any = allocateContext{}
	mountCtxKey    any = mountContext{}
)

func withAllocateCtx(ctx context.Context, sph Sph, forManifest bool) context.Context {
	return context.WithValue(ctx, allocateCtxKey, allocateContext{sph: sph, forManifest: forManifest})
}

func fromAllocateCtx(ctx context.Context) (Sph, bool, bool) {
	actx, ok := ctx.Value(allocateCtxKey).(allocateContext)
	return actx.sph, actx.forManifest, ok
}

func withMountContext(ctx context.Context, mctx *mountContext) context.Context {
	return context.WithValue(ctx, mountCtxKey, mctx)
}

func fromMountCtx(ctx context.Context) (*mountContext, bool) {
	mctx, ok := ctx.Value(mountCtxKey).(*mountContext)
	return mctx, ok
}
