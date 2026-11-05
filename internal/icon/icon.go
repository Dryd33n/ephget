// Package icon copies an exe's main icon into a .ico file. The exe is untrusted and
// is only parsed, never run: every offset it contains is checked before use.
package icon

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Ephget/ephget/internal/fsutil"
)

const (
	typeIcon      = 3
	typeGroupIcon = 14

	resourceDirectory = 2
	highBit           = 0x80000000
	maxSection        = 64 << 20
	maxImages         = 64
)

var errNoIcon = errors.New("the program has no icon")

// Extract writes the first icon in exe to out, which is how Windows chooses too.
func Extract(exe, out string) (err error) {
	// debug/pe isn't built for hostile files, and an icon is never worth a crash.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("read icon: %v", r)
		}
	}()
	data, err := read(exe)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(out, data)
}

func read(exe string) ([]byte, error) {
	file, err := pe.Open(exe)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	res, err := openResources(file)
	if err != nil {
		return nil, err
	}

	group := res.first(typeGroupIcon)
	if len(group) < 6 {
		return nil, errNoIcon
	}
	count := int(binary.LittleEndian.Uint16(group[4:]))
	if count > maxImages {
		count = maxImages
	}
	type image struct {
		header []byte
		data   []byte
	}
	var images []image
	for i := range count {
		start := 6 + i*14
		if start+14 > len(group) {
			break
		}
		entry := group[start : start+14]
		data := res.byID(typeIcon, uint32(binary.LittleEndian.Uint16(entry[12:])))
		if data != nil {
			images = append(images, image{entry[:8], data})
		}
	}
	if len(images) == 0 {
		return nil, errNoIcon
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(images))})
	offset := uint32(6 + 16*len(images))
	for _, img := range images {
		out.Write(img.header)
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(img.data)), offset})
		offset += uint32(len(img.data))
	}
	for _, img := range images {
		out.Write(img.data)
	}
	return out.Bytes(), nil
}

// resources reads a PE resource tree: type, then name, then language.
type resources struct {
	data []byte
	root uint32 // the tree's start within data
	base uint32 // the virtual address of data[0]
}

func openResources(file *pe.File) (*resources, error) {
	var dir pe.DataDirectory
	switch h := file.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		dir = h.DataDirectory[resourceDirectory]
	case *pe.OptionalHeader64:
		dir = h.DataDirectory[resourceDirectory]
	default:
		return nil, errNoIcon
	}
	if dir.VirtualAddress == 0 {
		return nil, errNoIcon
	}
	for _, s := range file.Sections {
		size := max(s.VirtualSize, s.Size)
		if dir.VirtualAddress < s.VirtualAddress || dir.VirtualAddress-s.VirtualAddress >= size {
			continue
		}
		if s.Size > maxSection {
			return nil, errors.New("the program's resource section is too large")
		}
		data, err := s.Data()
		if err != nil {
			return nil, err
		}
		return &resources{data, dir.VirtualAddress - s.VirtualAddress, s.VirtualAddress}, nil
	}
	return nil, errNoIcon
}

func (r *resources) u32(at uint32) (uint32, bool) {
	if uint64(at)+4 > uint64(len(r.data)) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(r.data[at:]), true
}

// children lists a directory's (id, offset) pairs. Offsets are relative to the root.
func (r *resources) children(dir uint32) [][2]uint32 {
	at := uint64(r.root) + uint64(dir)
	if at+16 > uint64(len(r.data)) {
		return nil
	}
	head := r.data[at:]
	count := int(binary.LittleEndian.Uint16(head[12:])) + int(binary.LittleEndian.Uint16(head[14:]))
	var out [][2]uint32
	for i := range count {
		entry := at + 16 + uint64(i)*8
		if entry+8 > uint64(len(r.data)) {
			break
		}
		out = append(out, [2]uint32{
			binary.LittleEndian.Uint32(r.data[entry:]),
			binary.LittleEndian.Uint32(r.data[entry+4:]),
		})
	}
	return out
}

func (r *resources) typeDir(kind uint32) (uint32, bool) {
	for _, c := range r.children(0) {
		if c[0] == kind && c[1]&highBit != 0 {
			return c[1] &^ highBit, true
		}
	}
	return 0, false
}

// bytesOf follows a name entry down to its first language's bytes.
func (r *resources) bytesOf(nameOffset uint32) []byte {
	if nameOffset&highBit == 0 {
		return nil
	}
	languages := r.children(nameOffset &^ highBit)
	if len(languages) == 0 || languages[0][1]&highBit != 0 {
		return nil
	}
	entry := uint64(r.root) + uint64(languages[0][1])
	if entry > uint64(^uint32(0)) {
		return nil
	}
	rva, ok := r.u32(uint32(entry))
	if !ok {
		return nil
	}
	size, ok := r.u32(uint32(entry) + 4)
	if !ok || rva < r.base {
		return nil
	}
	start := uint64(rva - r.base)
	if start+uint64(size) > uint64(len(r.data)) {
		return nil
	}
	return r.data[start : start+uint64(size)]
}

func (r *resources) first(kind uint32) []byte {
	dir, ok := r.typeDir(kind)
	if !ok {
		return nil
	}
	for _, c := range r.children(dir) {
		if data := r.bytesOf(c[1]); data != nil {
			return data
		}
	}
	return nil
}

func (r *resources) byID(kind, id uint32) []byte {
	dir, ok := r.typeDir(kind)
	if !ok {
		return nil
	}
	for _, c := range r.children(dir) {
		if c[0] == id {
			return r.bytesOf(c[1])
		}
	}
	return nil
}
