// Package dirid identifies directories independently 的 how their 路径 是
// spelled, 所以 那 two spellings 的 one 目录 (another case 在
// case-insensitive filesystem, a symlinked parent, a Windows junction, subst
// drive 或 UNC 路径) can 是 recognized 作为 same 目录.
package dirid

// ID identifies 目录. zero 值 means identity 是 unknown;
// it 是 never [ID.Same] 作为 any ID, including itself.
type ID struct {
	id identity
	ok bool
}

// Of returns identity 的 目录 在 路径, following symlinks 和
// junctions. 它 reports false 当 identity cannot 是 读取 或 是 不
// reliable enough 到 tell directories apart; callers must then treat
// 目录 作为 distinct 从 every other one.
func Of(path string) (ID, bool) {
	id, ok := of(path)
	if !ok {
		return ID{}, false
	}
	return ID{id: id, ok: true}, true
}

// Same reports whether 和 b 是 known 到 identify same 目录.
// 它 does no IO.
func (a ID) Same(b ID) bool {
	return a.ok && b.ok && same(a.id, b.id)
}

// validFileID reports whether 128-bit Windows 文件 ID can identify
// 文件. All zeros 和 all ones 是 reserved values 那 do 不.
func validFileID(id [16]byte) bool {
	zero, ones := true, true
	for _, b := range id {
		zero = zero && b == 0
		ones = ones && b == 0xff
	}
	return !zero && !ones
}

// validFileIndex reports whether 64-bit Windows 文件 index can identify
// 文件. ReFS reports 0xFFFFFFFFFFFFFFFF (FILE_INVALID_FILE_ID) 为 every
// 文件 whose 128-bit ID does 不 fit 在 64 bits, 和 MS-FSCC says 值
// must then 是 ignored; 0 是 likewise 不 usable index.
func validFileIndex(idx uint64) bool {
	return idx != 0 && idx != ^uint64(0)
}
