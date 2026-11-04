package icon

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// A Windows file that is sure to have icons. It is only read.
var systemExe = filepath.Join(os.Getenv("WINDIR"), "explorer.exe")

func TestExtractWritesAValidIcon(t *testing.T) {
	out := filepath.Join(t.TempDir(), "icons", "app.ico")
	if err := Extract(systemExe, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 22 || binary.LittleEndian.Uint16(data[0:]) != 0 || binary.LittleEndian.Uint16(data[2:]) != 1 {
		t.Fatalf("bad header: % x", data[:min(len(data), 22)])
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	if count == 0 {
		t.Fatal("no images")
	}
	end := 6 + 16*count
	for i := range count {
		entry := data[6+i*16:]
		size := int(binary.LittleEndian.Uint32(entry[8:]))
		offset := int(binary.LittleEndian.Uint32(entry[12:]))
		if offset != end || offset+size > len(data) {
			t.Fatalf("image %d: offset %d, size %d, file %d", i, offset, size, len(data))
		}
		image := data[offset:]
		png := image[0] == 0x89 && image[1] == 'P'
		bitmap := binary.LittleEndian.Uint32(image) == 40
		if !png && !bitmap {
			t.Fatalf("image %d is neither PNG nor bitmap: % x", i, image[:8])
		}
		end += size
	}
	if end != len(data) {
		t.Fatalf("images end at %d, file is %d", end, len(data))
	}
}

func TestExtractSurvivesDamagedFiles(t *testing.T) {
	good, err := os.ReadFile(systemExe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.exe"), filepath.Join(dir, "out.ico")
	inputs := [][]byte{nil, []byte("EXE"), []byte("MZ"), good[:512], good[:4096], good[:len(good)/2]}
	// Corrupt the file at many positions, including the resource tree's offsets.
	for step := 1; step < 40; step++ {
		damaged := append([]byte(nil), good...)
		for i := len(good) * step / 40; i < len(good) && i < len(good)*step/40+4096; i++ {
			damaged[i] = 0xFF
		}
		inputs = append(inputs, damaged)
	}
	for i, input := range inputs {
		if err := os.WriteFile(in, input, 0o666); err != nil {
			t.Fatal(err)
		}
		Extract(in, out) // an error is fine; a crash or a hang isn't
		if i < 4 {
			if err := Extract(in, out); err == nil {
				t.Errorf("input %d produced an icon", i)
			}
		}
	}
}

func TestExtractMissingFile(t *testing.T) {
	if err := Extract(filepath.Join(t.TempDir(), "none.exe"), filepath.Join(t.TempDir(), "o.ico")); err == nil {
		t.Fatal("no error")
	}
}
