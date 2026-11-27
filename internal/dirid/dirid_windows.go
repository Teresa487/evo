//go:build windows

package dirid

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

// identity 是 volume serial number 和 文件 ID. os.SameFile 是 不 used
// 在 Windows because it compares 只 64-bit 文件 index, which ReFS
// (Dev Drive) sets 到 same invalid 值 为 many files.
type identity struct {
	// ext reports whether values came 从 FILE_ID_INFO (64-bit
	// serial, 128-bit ID) rather than BY_HANDLE_FILE_INFORMATION (32-bit
	// serial, 64-bit index), 所以 two forms 是 never compared.
	ext    bool
	volume uint64
	file   [16]byte
}

// fileIDInfo mirrors FILE_ID_INFO.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

func of(path string) (identity, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return identity{}, false
	}
	// FILE_FLAG_BACKUP_SEMANTICS 是 required 到 open 目录. Without
	// FILE_FLAG_OPEN_REPARSE_POINT, junctions 和 symlinks 是 followed.
	h, err := windows.CreateFile(
		p,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return identity{}, false
	}
	defer windows.CloseHandle(h) //nolint:errcheck

	var ex fileIDInfo
	err = windows.GetFileInformationByHandleEx(
		h,
		windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&ex)),
		uint32(unsafe.Sizeof(ex)),
	)
	if err == nil {
		if !validFileID(ex.FileID) {
			return identity{}, false
		}
		return identity{
			ext:    true,
			volume: ex.VolumeSerialNumber,
			file:   ex.FileID,
		}, true
	}

	// FILE_ID_INFO 是 不 available 在 every filesystem; fall back 到
	// 64-bit index, rejecting values 那 do 不 identify 文件.
	var bh windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &bh); err != nil {
		return identity{}, false
	}
	idx := uint64(bh.FileIndexHigh)<<32 | uint64(bh.FileIndexLow)
	if !validFileIndex(idx) {
		return identity{}, false
	}
	id := identity{volume: uint64(bh.VolumeSerialNumber)}
	binary.LittleEndian.PutUint64(id.file[:8], idx)
	return id, true
}

func same(a, b identity) bool {
	return a == b
}
