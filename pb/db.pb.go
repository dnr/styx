// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.35.1
// 	protoc        v5.28.3
// source: db.proto

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

type MountState int32

const (
	MountState_Unknown          MountState = 0 // initial value
	MountState_Requested        MountState = 1 // requested but not mounted yet
	MountState_Mounted          MountState = 2 // mounted
	MountState_MountError       MountState = 3 // got error mounting
	MountState_UnmountRequested MountState = 4 // was mounted then requested unmount
	MountState_Unmounted        MountState = 5 // unmounted
	MountState_Deleted          MountState = 6 // deleted (not used now)
	MountState_Materialized     MountState = 7 // not mounted but copied out to fs
)

// Enum value maps for MountState.
var (
	MountState_name = map[int32]string{
		0: "Unknown",
		1: "Requested",
		2: "Mounted",
		3: "MountError",
		4: "UnmountRequested",
		5: "Unmounted",
		6: "Deleted",
		7: "Materialized",
	}
	MountState_value = map[string]int32{
		"Unknown":          0,
		"Requested":        1,
		"Mounted":          2,
		"MountError":       3,
		"UnmountRequested": 4,
		"Unmounted":        5,
		"Deleted":          6,
		"Materialized":     7,
	}
)

func (x MountState) Enum() *MountState {
	p := new(MountState)
	*p = x
	return p
}

func (x MountState) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (MountState) Descriptor() protoreflect.EnumDescriptor {
	return file_db_proto_enumTypes[0].Descriptor()
}

func (MountState) Type() protoreflect.EnumType {
	return &file_db_proto_enumTypes[0]
}

func (x MountState) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use MountState.Descriptor instead.
func (MountState) EnumDescriptor() ([]byte, []int) {
	return file_db_proto_rawDescGZIP(), []int{0}
}

// key: "image" / <store path hash (nix base32)>
// value:
type DbImage struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// full store path including name
	StorePath string `protobuf:"bytes,2,opt,name=store_path,json=storePath,proto3" json:"store_path,omitempty"`
	// which upstream this was from
	Upstream string `protobuf:"bytes,3,opt,name=upstream,proto3" json:"upstream,omitempty"`
	// system id from syschecker
	SyscheckerSystem int64 `protobuf:"varint,4,opt,name=syschecker_system,json=syscheckerSystem,proto3" json:"syschecker_system,omitempty"`
	// is it mounted and where?
	MountState     MountState `protobuf:"varint,5,opt,name=mount_state,json=mountState,proto3,enum=pb.MountState" json:"mount_state,omitempty"`
	MountPoint     string     `protobuf:"bytes,6,opt,name=mount_point,json=mountPoint,proto3" json:"mount_point,omitempty"`
	LastMountError string     `protobuf:"bytes,7,opt,name=last_mount_error,json=lastMountError,proto3" json:"last_mount_error,omitempty"`
	// size of erofs image
	ImageSize int64 `protobuf:"varint,1,opt,name=image_size,json=imageSize,proto3" json:"image_size,omitempty"`
	IsBare    bool  `protobuf:"varint,10,opt,name=is_bare,json=isBare,proto3" json:"is_bare,omitempty"`
	// nar size, if known
	NarSize int64 `protobuf:"varint,11,opt,name=nar_size,json=narSize,proto3" json:"nar_size,omitempty"`
}

func (x *DbImage) Reset() {
	*x = DbImage{}
	mi := &file_db_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DbImage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DbImage) ProtoMessage() {}

func (x *DbImage) ProtoReflect() protoreflect.Message {
	mi := &file_db_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DbImage.ProtoReflect.Descriptor instead.
func (*DbImage) Descriptor() ([]byte, []int) {
	return file_db_proto_rawDescGZIP(), []int{0}
}

func (x *DbImage) GetStorePath() string {
	if x != nil {
		return x.StorePath
	}
	return ""
}

func (x *DbImage) GetUpstream() string {
	if x != nil {
		return x.Upstream
	}
	return ""
}

func (x *DbImage) GetSyscheckerSystem() int64 {
	if x != nil {
		return x.SyscheckerSystem
	}
	return 0
}

func (x *DbImage) GetMountState() MountState {
	if x != nil {
		return x.MountState
	}
	return MountState_Unknown
}

func (x *DbImage) GetMountPoint() string {
	if x != nil {
		return x.MountPoint
	}
	return ""
}

func (x *DbImage) GetLastMountError() string {
	if x != nil {
		return x.LastMountError
	}
	return ""
}

func (x *DbImage) GetImageSize() int64 {
	if x != nil {
		return x.ImageSize
	}
	return 0
}

func (x *DbImage) GetIsBare() bool {
	if x != nil {
		return x.IsBare
	}
	return false
}

func (x *DbImage) GetNarSize() int64 {
	if x != nil {
		return x.NarSize
	}
	return 0
}

// key: "meta" / "params"
type DbParams struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Params *DaemonParams `protobuf:"bytes,1,opt,name=params,proto3" json:"params,omitempty"`
	Pubkey []string      `protobuf:"bytes,2,rep,name=pubkey,proto3" json:"pubkey,omitempty"`
}

func (x *DbParams) Reset() {
	*x = DbParams{}
	mi := &file_db_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DbParams) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DbParams) ProtoMessage() {}

func (x *DbParams) ProtoReflect() protoreflect.Message {
	mi := &file_db_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DbParams.ProtoReflect.Descriptor instead.
func (*DbParams) Descriptor() ([]byte, []int) {
	return file_db_proto_rawDescGZIP(), []int{1}
}

func (x *DbParams) GetParams() *DaemonParams {
	if x != nil {
		return x.Params
	}
	return nil
}

func (x *DbParams) GetPubkey() []string {
	if x != nil {
		return x.Pubkey
	}
	return nil
}

var File_db_proto protoreflect.FileDescriptor

var file_db_proto_rawDesc = []byte{
	0x0a, 0x08, 0x64, 0x62, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x02, 0x70, 0x62, 0x1a, 0x0c,
	0x70, 0x61, 0x72, 0x61, 0x6d, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0xc6, 0x02, 0x0a,
	0x07, 0x44, 0x62, 0x49, 0x6d, 0x61, 0x67, 0x65, 0x12, 0x1d, 0x0a, 0x0a, 0x73, 0x74, 0x6f, 0x72,
	0x65, 0x5f, 0x70, 0x61, 0x74, 0x68, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x09, 0x73, 0x74,
	0x6f, 0x72, 0x65, 0x50, 0x61, 0x74, 0x68, 0x12, 0x1a, 0x0a, 0x08, 0x75, 0x70, 0x73, 0x74, 0x72,
	0x65, 0x61, 0x6d, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x75, 0x70, 0x73, 0x74, 0x72,
	0x65, 0x61, 0x6d, 0x12, 0x2b, 0x0a, 0x11, 0x73, 0x79, 0x73, 0x63, 0x68, 0x65, 0x63, 0x6b, 0x65,
	0x72, 0x5f, 0x73, 0x79, 0x73, 0x74, 0x65, 0x6d, 0x18, 0x04, 0x20, 0x01, 0x28, 0x03, 0x52, 0x10,
	0x73, 0x79, 0x73, 0x63, 0x68, 0x65, 0x63, 0x6b, 0x65, 0x72, 0x53, 0x79, 0x73, 0x74, 0x65, 0x6d,
	0x12, 0x2f, 0x0a, 0x0b, 0x6d, 0x6f, 0x75, 0x6e, 0x74, 0x5f, 0x73, 0x74, 0x61, 0x74, 0x65, 0x18,
	0x05, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x0e, 0x2e, 0x70, 0x62, 0x2e, 0x4d, 0x6f, 0x75, 0x6e, 0x74,
	0x53, 0x74, 0x61, 0x74, 0x65, 0x52, 0x0a, 0x6d, 0x6f, 0x75, 0x6e, 0x74, 0x53, 0x74, 0x61, 0x74,
	0x65, 0x12, 0x1f, 0x0a, 0x0b, 0x6d, 0x6f, 0x75, 0x6e, 0x74, 0x5f, 0x70, 0x6f, 0x69, 0x6e, 0x74,
	0x18, 0x06, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0a, 0x6d, 0x6f, 0x75, 0x6e, 0x74, 0x50, 0x6f, 0x69,
	0x6e, 0x74, 0x12, 0x28, 0x0a, 0x10, 0x6c, 0x61, 0x73, 0x74, 0x5f, 0x6d, 0x6f, 0x75, 0x6e, 0x74,
	0x5f, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x18, 0x07, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0e, 0x6c, 0x61,
	0x73, 0x74, 0x4d, 0x6f, 0x75, 0x6e, 0x74, 0x45, 0x72, 0x72, 0x6f, 0x72, 0x12, 0x1d, 0x0a, 0x0a,
	0x69, 0x6d, 0x61, 0x67, 0x65, 0x5f, 0x73, 0x69, 0x7a, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x03,
	0x52, 0x09, 0x69, 0x6d, 0x61, 0x67, 0x65, 0x53, 0x69, 0x7a, 0x65, 0x12, 0x17, 0x0a, 0x07, 0x69,
	0x73, 0x5f, 0x62, 0x61, 0x72, 0x65, 0x18, 0x0a, 0x20, 0x01, 0x28, 0x08, 0x52, 0x06, 0x69, 0x73,
	0x42, 0x61, 0x72, 0x65, 0x12, 0x19, 0x0a, 0x08, 0x6e, 0x61, 0x72, 0x5f, 0x73, 0x69, 0x7a, 0x65,
	0x18, 0x0b, 0x20, 0x01, 0x28, 0x03, 0x52, 0x07, 0x6e, 0x61, 0x72, 0x53, 0x69, 0x7a, 0x65, 0x4a,
	0x04, 0x08, 0x08, 0x10, 0x0a, 0x22, 0x4c, 0x0a, 0x08, 0x44, 0x62, 0x50, 0x61, 0x72, 0x61, 0x6d,
	0x73, 0x12, 0x28, 0x0a, 0x06, 0x70, 0x61, 0x72, 0x61, 0x6d, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28,
	0x0b, 0x32, 0x10, 0x2e, 0x70, 0x62, 0x2e, 0x44, 0x61, 0x65, 0x6d, 0x6f, 0x6e, 0x50, 0x61, 0x72,
	0x61, 0x6d, 0x73, 0x52, 0x06, 0x70, 0x61, 0x72, 0x61, 0x6d, 0x73, 0x12, 0x16, 0x0a, 0x06, 0x70,
	0x75, 0x62, 0x6b, 0x65, 0x79, 0x18, 0x02, 0x20, 0x03, 0x28, 0x09, 0x52, 0x06, 0x70, 0x75, 0x62,
	0x6b, 0x65, 0x79, 0x2a, 0x89, 0x01, 0x0a, 0x0a, 0x4d, 0x6f, 0x75, 0x6e, 0x74, 0x53, 0x74, 0x61,
	0x74, 0x65, 0x12, 0x0b, 0x0a, 0x07, 0x55, 0x6e, 0x6b, 0x6e, 0x6f, 0x77, 0x6e, 0x10, 0x00, 0x12,
	0x0d, 0x0a, 0x09, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x65, 0x64, 0x10, 0x01, 0x12, 0x0b,
	0x0a, 0x07, 0x4d, 0x6f, 0x75, 0x6e, 0x74, 0x65, 0x64, 0x10, 0x02, 0x12, 0x0e, 0x0a, 0x0a, 0x4d,
	0x6f, 0x75, 0x6e, 0x74, 0x45, 0x72, 0x72, 0x6f, 0x72, 0x10, 0x03, 0x12, 0x14, 0x0a, 0x10, 0x55,
	0x6e, 0x6d, 0x6f, 0x75, 0x6e, 0x74, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x65, 0x64, 0x10,
	0x04, 0x12, 0x0d, 0x0a, 0x09, 0x55, 0x6e, 0x6d, 0x6f, 0x75, 0x6e, 0x74, 0x65, 0x64, 0x10, 0x05,
	0x12, 0x0b, 0x0a, 0x07, 0x44, 0x65, 0x6c, 0x65, 0x74, 0x65, 0x64, 0x10, 0x06, 0x12, 0x10, 0x0a,
	0x0c, 0x4d, 0x61, 0x74, 0x65, 0x72, 0x69, 0x61, 0x6c, 0x69, 0x7a, 0x65, 0x64, 0x10, 0x07, 0x42,
	0x18, 0x5a, 0x16, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x64, 0x6e,
	0x72, 0x2f, 0x73, 0x74, 0x79, 0x78, 0x2f, 0x70, 0x62, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x33,
}

var (
	file_db_proto_rawDescOnce sync.Once
	file_db_proto_rawDescData = file_db_proto_rawDesc
)

func file_db_proto_rawDescGZIP() []byte {
	file_db_proto_rawDescOnce.Do(func() {
		file_db_proto_rawDescData = protoimpl.X.CompressGZIP(file_db_proto_rawDescData)
	})
	return file_db_proto_rawDescData
}

var file_db_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_db_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_db_proto_goTypes = []any{
	(MountState)(0),      // 0: pb.MountState
	(*DbImage)(nil),      // 1: pb.DbImage
	(*DbParams)(nil),     // 2: pb.DbParams
	(*DaemonParams)(nil), // 3: pb.DaemonParams
}
var file_db_proto_depIdxs = []int32{
	0, // 0: pb.DbImage.mount_state:type_name -> pb.MountState
	3, // 1: pb.DbParams.params:type_name -> pb.DaemonParams
	2, // [2:2] is the sub-list for method output_type
	2, // [2:2] is the sub-list for method input_type
	2, // [2:2] is the sub-list for extension type_name
	2, // [2:2] is the sub-list for extension extendee
	0, // [0:2] is the sub-list for field type_name
}

func init() { file_db_proto_init() }
func file_db_proto_init() {
	if File_db_proto != nil {
		return
	}
	file_params_proto_init()
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_db_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_db_proto_goTypes,
		DependencyIndexes: file_db_proto_depIdxs,
		EnumInfos:         file_db_proto_enumTypes,
		MessageInfos:      file_db_proto_msgTypes,
	}.Build()
	File_db_proto = out.File
	file_db_proto_rawDesc = nil
	file_db_proto_goTypes = nil
	file_db_proto_depIdxs = nil
}
