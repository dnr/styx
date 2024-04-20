package erofs

import (
	"github.com/dnr/styx/common"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// I don't know why I can't write "proto.Message" in here, but expanding it works
func unmarshalAs[T any, PM interface {
	ProtoReflect() protoreflect.Message
	*T
}](b []byte) (*T, error) {
	var m PM = new(T)
	return common.ValOrErr(m, proto.Unmarshal(b, m))
}
