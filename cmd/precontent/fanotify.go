package main

type fanotify_event_metadata struct {
	EventLen    uint32
	Vers        uint8
	Reserved    uint8
	MetadataLen uint16
	Mask        uint64
	Fd          int32
	Pid         int32
}

type fanotify_event_info_header struct {
	InfoType uint8
	Pad      uint8
	Len      uint16
}

type fanotify_event_info_fid struct {
	// Hdr  fanotify_event_info_header
	Fsid uint64
	// Handle [...]byte
}

type fanotify_event_info_pidfd struct {
	// Hdr   fanotify_event_info_header
	PidFd int32
}

type fanotify_event_info_error struct {
	// Hdr        fanotify_event_info_header
	Error      int32
	ErrorCount uint32
}

type fanotify_event_info_range struct {
	// Hdr    fanotify_event_info_header
	Pad    uint32
	Offset uint64
	Count  uint64
}

type fanotify_response struct {
	Fd       int32
	Response uint32
}

type fanotify_response_info_header struct {
	Type uint8
	Pad  uint8
	Len  uint16
}
