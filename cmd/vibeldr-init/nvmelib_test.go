package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The DS918+ slot addresses become the machine's NVMe root-bus addresses, the
// untouched library is kept, and a machine with no NVMe gets it back.
//
// DS918+ 슬롯 주소는 이 기계의 NVMe 루트 버스 주소가 되고, 손대지 않은
// 라이브러리는 보관되며, NVMe 가 없는 기계는 그것을 되찾는다.
func TestFitNVMeLibrary(t *testing.T) {
	root := t.TempDir()
	lib := root + nvmeLib
	if err := os.MkdirAll(filepath.Dir(lib), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "head\x000000:00:13.1\x000000:00:13.2\x000000:00:03.2\x00tail"
	if err := os.WriteFile(lib, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := fitNVMeLibrary(root, []string{"0000:00:02.0", "0000:00:1c.0,00.0"}); !strings.Contains(msg, "0000:00:02.0 0000:00:1c.0") {
		t.Fatalf("msg %q", msg)
	}
	got, _ := os.ReadFile(lib)
	if string(got) != "head\x000000:00:02.0\x000000:00:1c.0\x000000:00:03.2\x00tail" {
		t.Fatalf("library %q", got)
	}
	if kept, _ := os.ReadFile(root + nvmeLibOrig); string(kept) != orig {
		t.Fatalf("kept %q", kept)
	}
	if msg := fitNVMeLibrary(root, []string{"0000:00:02.0", "0000:00:1c.0,00.0"}); msg != "" {
		t.Fatalf("second run %q", msg)
	}
	fitNVMeLibrary(root, nil)
	if back, _ := os.ReadFile(lib); string(back) != orig {
		t.Fatalf("not put back: %q", back)
	}
}
