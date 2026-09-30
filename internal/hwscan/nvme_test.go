package hwscan

import (
	"io/fs"
	"path"
	"reflect"
	"testing"
	"testing/fstest"
)

// NVMe controllers are found on the root bus and behind a bridge, and other
// devices are not.
//
// NVMe 컨트롤러는 루트 버스와 브리지 뒤에서 찾고, 다른 장치는 찾지 않는다.
func TestNVMeRoots(t *testing.T) {
	m := fstest.MapFS{}
	class := func(p, v string) {
		m[p] = &fstest.MapFile{Mode: fs.ModeDir}
		m[path.Join(p, "class")] = &fstest.MapFile{Data: []byte(v + "\n")}
	}
	class("devices/pci0000:00/0000:00:1f.2", "0x010601")
	class("devices/pci0000:00/0000:00:02.0", "0x010802")
	class("devices/pci0000:00/0000:00:1c.0", "0x060400")
	class("devices/pci0000:00/0000:00:1c.0/0000:05:00.0", "0x010802")
	if got := NVMeRoots(m); !reflect.DeepEqual(got, []string{"0000:00:02.0", "0000:00:1c.0,00.0"}) {
		t.Fatalf("roots %v", got)
	}
}
