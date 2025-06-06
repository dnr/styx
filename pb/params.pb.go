// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.35.1
// 	protoc        v5.28.3
// source: params.proto

package pb

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// Parameters that have to be agreed on by manifester and daemon.
type GlobalParams struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Digest algorithm, e.g. "sha256"
	DigestAlgo string `protobuf:"bytes,2,opt,name=digest_algo,json=digestAlgo,proto3" json:"digest_algo,omitempty"`
	// Bits of digest used, e.g. 192
	DigestBits int32 `protobuf:"varint,3,opt,name=digest_bits,json=digestBits,proto3" json:"digest_bits,omitempty"`
}

func (x *GlobalParams) Reset() {
	*x = GlobalParams{}
	mi := &file_params_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GlobalParams) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GlobalParams) ProtoMessage() {}

func (x *GlobalParams) ProtoReflect() protoreflect.Message {
	mi := &file_params_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GlobalParams.ProtoReflect.Descriptor instead.
func (*GlobalParams) Descriptor() ([]byte, []int) {
	return file_params_proto_rawDescGZIP(), []int{0}
}

func (x *GlobalParams) GetDigestAlgo() string {
	if x != nil {
		return x.DigestAlgo
	}
	return ""
}

func (x *GlobalParams) GetDigestBits() int32 {
	if x != nil {
		return x.DigestBits
	}
	return 0
}

// Parameters that can be used to configure a styx daemon.
type DaemonParams struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Params *GlobalParams `protobuf:"bytes,1,opt,name=params,proto3" json:"params,omitempty"`
	// URL for manifester service, chunk reads, and chunk diffs.
	ManifesterUrl    string `protobuf:"bytes,2,opt,name=manifester_url,json=manifesterUrl,proto3" json:"manifester_url,omitempty"`
	ManifestCacheUrl string `protobuf:"bytes,5,opt,name=manifest_cache_url,json=manifestCacheUrl,proto3" json:"manifest_cache_url,omitempty"`
	ChunkReadUrl     string `protobuf:"bytes,3,opt,name=chunk_read_url,json=chunkReadUrl,proto3" json:"chunk_read_url,omitempty"`
	ChunkDiffUrl     string `protobuf:"bytes,4,opt,name=chunk_diff_url,json=chunkDiffUrl,proto3" json:"chunk_diff_url,omitempty"`
	// Size to shard manifest. If missing, daemon uses a default.
	ShardManifestBytes int64 `protobuf:"varint,6,opt,name=shard_manifest_bytes,json=shardManifestBytes,proto3" json:"shard_manifest_bytes,omitempty"`
}

func (x *DaemonParams) Reset() {
	*x = DaemonParams{}
	mi := &file_params_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DaemonParams) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DaemonParams) ProtoMessage() {}

func (x *DaemonParams) ProtoReflect() protoreflect.Message {
	mi := &file_params_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DaemonParams.ProtoReflect.Descriptor instead.
func (*DaemonParams) Descriptor() ([]byte, []int) {
	return file_params_proto_rawDescGZIP(), []int{1}
}

func (x *DaemonParams) GetParams() *GlobalParams {
	if x != nil {
		return x.Params
	}
	return nil
}

func (x *DaemonParams) GetManifesterUrl() string {
	if x != nil {
		return x.ManifesterUrl
	}
	return ""
}

func (x *DaemonParams) GetManifestCacheUrl() string {
	if x != nil {
		return x.ManifestCacheUrl
	}
	return ""
}

func (x *DaemonParams) GetChunkReadUrl() string {
	if x != nil {
		return x.ChunkReadUrl
	}
	return ""
}

func (x *DaemonParams) GetChunkDiffUrl() string {
	if x != nil {
		return x.ChunkDiffUrl
	}
	return ""
}

func (x *DaemonParams) GetShardManifestBytes() int64 {
	if x != nil {
		return x.ShardManifestBytes
	}
	return 0
}

type SignedMessage struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Params for hashing/chunking contained data, and also used for signature.
	Params *GlobalParams `protobuf:"bytes,1,opt,name=params,proto3" json:"params,omitempty"`
	// Single entry representing contained data.
	// Type must be REGULAR.
	// Path should represent message type and context.
	Msg *Entry `protobuf:"bytes,2,opt,name=msg,proto3" json:"msg,omitempty"`
	// These should be the same length:
	KeyId     []string `protobuf:"bytes,3,rep,name=key_id,json=keyId,proto3" json:"key_id,omitempty"`
	Signature [][]byte `protobuf:"bytes,4,rep,name=signature,proto3" json:"signature,omitempty"`
}

func (x *SignedMessage) Reset() {
	*x = SignedMessage{}
	mi := &file_params_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SignedMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SignedMessage) ProtoMessage() {}

func (x *SignedMessage) ProtoReflect() protoreflect.Message {
	mi := &file_params_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SignedMessage.ProtoReflect.Descriptor instead.
func (*SignedMessage) Descriptor() ([]byte, []int) {
	return file_params_proto_rawDescGZIP(), []int{2}
}

func (x *SignedMessage) GetParams() *GlobalParams {
	if x != nil {
		return x.Params
	}
	return nil
}

func (x *SignedMessage) GetMsg() *Entry {
	if x != nil {
		return x.Msg
	}
	return nil
}

func (x *SignedMessage) GetKeyId() []string {
	if x != nil {
		return x.KeyId
	}
	return nil
}

func (x *SignedMessage) GetSignature() [][]byte {
	if x != nil {
		return x.Signature
	}
	return nil
}

var File_params_proto protoreflect.FileDescriptor

var file_params_proto_rawDesc = []byte{
	0x0a, 0x0c, 0x70, 0x61, 0x72, 0x61, 0x6d, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x02,
	0x70, 0x62, 0x1a, 0x0b, 0x65, 0x6e, 0x74, 0x72, 0x79, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22,
	0x56, 0x0a, 0x0c, 0x47, 0x6c, 0x6f, 0x62, 0x61, 0x6c, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x73, 0x12,
	0x1f, 0x0a, 0x0b, 0x64, 0x69, 0x67, 0x65, 0x73, 0x74, 0x5f, 0x61, 0x6c, 0x67, 0x6f, 0x18, 0x02,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x0a, 0x64, 0x69, 0x67, 0x65, 0x73, 0x74, 0x41, 0x6c, 0x67, 0x6f,
	0x12, 0x1f, 0x0a, 0x0b, 0x64, 0x69, 0x67, 0x65, 0x73, 0x74, 0x5f, 0x62, 0x69, 0x74, 0x73, 0x18,
	0x03, 0x20, 0x01, 0x28, 0x05, 0x52, 0x0a, 0x64, 0x69, 0x67, 0x65, 0x73, 0x74, 0x42, 0x69, 0x74,
	0x73, 0x4a, 0x04, 0x08, 0x01, 0x10, 0x02, 0x22, 0x8b, 0x02, 0x0a, 0x0c, 0x44, 0x61, 0x65, 0x6d,
	0x6f, 0x6e, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x73, 0x12, 0x28, 0x0a, 0x06, 0x70, 0x61, 0x72, 0x61,
	0x6d, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x10, 0x2e, 0x70, 0x62, 0x2e, 0x47, 0x6c,
	0x6f, 0x62, 0x61, 0x6c, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x73, 0x52, 0x06, 0x70, 0x61, 0x72, 0x61,
	0x6d, 0x73, 0x12, 0x25, 0x0a, 0x0e, 0x6d, 0x61, 0x6e, 0x69, 0x66, 0x65, 0x73, 0x74, 0x65, 0x72,
	0x5f, 0x75, 0x72, 0x6c, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0d, 0x6d, 0x61, 0x6e, 0x69,
	0x66, 0x65, 0x73, 0x74, 0x65, 0x72, 0x55, 0x72, 0x6c, 0x12, 0x2c, 0x0a, 0x12, 0x6d, 0x61, 0x6e,
	0x69, 0x66, 0x65, 0x73, 0x74, 0x5f, 0x63, 0x61, 0x63, 0x68, 0x65, 0x5f, 0x75, 0x72, 0x6c, 0x18,
	0x05, 0x20, 0x01, 0x28, 0x09, 0x52, 0x10, 0x6d, 0x61, 0x6e, 0x69, 0x66, 0x65, 0x73, 0x74, 0x43,
	0x61, 0x63, 0x68, 0x65, 0x55, 0x72, 0x6c, 0x12, 0x24, 0x0a, 0x0e, 0x63, 0x68, 0x75, 0x6e, 0x6b,
	0x5f, 0x72, 0x65, 0x61, 0x64, 0x5f, 0x75, 0x72, 0x6c, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52,
	0x0c, 0x63, 0x68, 0x75, 0x6e, 0x6b, 0x52, 0x65, 0x61, 0x64, 0x55, 0x72, 0x6c, 0x12, 0x24, 0x0a,
	0x0e, 0x63, 0x68, 0x75, 0x6e, 0x6b, 0x5f, 0x64, 0x69, 0x66, 0x66, 0x5f, 0x75, 0x72, 0x6c, 0x18,
	0x04, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0c, 0x63, 0x68, 0x75, 0x6e, 0x6b, 0x44, 0x69, 0x66, 0x66,
	0x55, 0x72, 0x6c, 0x12, 0x30, 0x0a, 0x14, 0x73, 0x68, 0x61, 0x72, 0x64, 0x5f, 0x6d, 0x61, 0x6e,
	0x69, 0x66, 0x65, 0x73, 0x74, 0x5f, 0x62, 0x79, 0x74, 0x65, 0x73, 0x18, 0x06, 0x20, 0x01, 0x28,
	0x03, 0x52, 0x12, 0x73, 0x68, 0x61, 0x72, 0x64, 0x4d, 0x61, 0x6e, 0x69, 0x66, 0x65, 0x73, 0x74,
	0x42, 0x79, 0x74, 0x65, 0x73, 0x22, 0x8b, 0x01, 0x0a, 0x0d, 0x53, 0x69, 0x67, 0x6e, 0x65, 0x64,
	0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x12, 0x28, 0x0a, 0x06, 0x70, 0x61, 0x72, 0x61, 0x6d,
	0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x10, 0x2e, 0x70, 0x62, 0x2e, 0x47, 0x6c, 0x6f,
	0x62, 0x61, 0x6c, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x73, 0x52, 0x06, 0x70, 0x61, 0x72, 0x61, 0x6d,
	0x73, 0x12, 0x1b, 0x0a, 0x03, 0x6d, 0x73, 0x67, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x09,
	0x2e, 0x70, 0x62, 0x2e, 0x45, 0x6e, 0x74, 0x72, 0x79, 0x52, 0x03, 0x6d, 0x73, 0x67, 0x12, 0x15,
	0x0a, 0x06, 0x6b, 0x65, 0x79, 0x5f, 0x69, 0x64, 0x18, 0x03, 0x20, 0x03, 0x28, 0x09, 0x52, 0x05,
	0x6b, 0x65, 0x79, 0x49, 0x64, 0x12, 0x1c, 0x0a, 0x09, 0x73, 0x69, 0x67, 0x6e, 0x61, 0x74, 0x75,
	0x72, 0x65, 0x18, 0x04, 0x20, 0x03, 0x28, 0x0c, 0x52, 0x09, 0x73, 0x69, 0x67, 0x6e, 0x61, 0x74,
	0x75, 0x72, 0x65, 0x42, 0x18, 0x5a, 0x16, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f,
	0x6d, 0x2f, 0x64, 0x6e, 0x72, 0x2f, 0x73, 0x74, 0x79, 0x78, 0x2f, 0x70, 0x62, 0x62, 0x06, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_params_proto_rawDescOnce sync.Once
	file_params_proto_rawDescData = file_params_proto_rawDesc
)

func file_params_proto_rawDescGZIP() []byte {
	file_params_proto_rawDescOnce.Do(func() {
		file_params_proto_rawDescData = protoimpl.X.CompressGZIP(file_params_proto_rawDescData)
	})
	return file_params_proto_rawDescData
}

var file_params_proto_msgTypes = make([]protoimpl.MessageInfo, 3)
var file_params_proto_goTypes = []any{
	(*GlobalParams)(nil),  // 0: pb.GlobalParams
	(*DaemonParams)(nil),  // 1: pb.DaemonParams
	(*SignedMessage)(nil), // 2: pb.SignedMessage
	(*Entry)(nil),         // 3: pb.Entry
}
var file_params_proto_depIdxs = []int32{
	0, // 0: pb.DaemonParams.params:type_name -> pb.GlobalParams
	0, // 1: pb.SignedMessage.params:type_name -> pb.GlobalParams
	3, // 2: pb.SignedMessage.msg:type_name -> pb.Entry
	3, // [3:3] is the sub-list for method output_type
	3, // [3:3] is the sub-list for method input_type
	3, // [3:3] is the sub-list for extension type_name
	3, // [3:3] is the sub-list for extension extendee
	0, // [0:3] is the sub-list for field type_name
}

func init() { file_params_proto_init() }
func file_params_proto_init() {
	if File_params_proto != nil {
		return
	}
	file_entry_proto_init()
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_params_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   3,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_params_proto_goTypes,
		DependencyIndexes: file_params_proto_depIdxs,
		MessageInfos:      file_params_proto_msgTypes,
	}.Build()
	File_params_proto = out.File
	file_params_proto_rawDesc = nil
	file_params_proto_goTypes = nil
	file_params_proto_depIdxs = nil
}
