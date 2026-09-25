package ramdisk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibeldr/internal/lzma"
)

func TestProbeDataDevice(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "work", "dsm-real", "rd.gz"))
	if err != nil {
		t.Skip("no rd.gz")
	}
	cpio, _ := lzma.Decode(raw)
	a, err := ReadCPIO(cpio)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := a.Get("usr/syno/share/dsmupdate/datadevice.sh")
	if !ok {
		t.Fatal("datadevice.sh missing")
	}
	for i, l := range strings.Split(string(e.Data), "\n") {
		t.Logf("%4d| %s", i+1, strings.TrimRight(l, " \t"))
	}
}
