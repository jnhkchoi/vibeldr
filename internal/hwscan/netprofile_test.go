package hwscan

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

// makeNetFS is the helper that plants a name to vendor mapping under
// /sys/class/net. An empty vendor imitates the case where device/vendor does
// not exist at all, as on a virtual interface.
//
// makeNetFS - /sys/class/net 아래에 이름 -> vendor 값 매핑을 심는 헬퍼.
// vendor 가 빈 문자열이면 "device/vendor 자체가 없는" (가상 인터페이스 같은)
// 경우를 흉내낸다.
func makeNetFS(ifaces map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	m["sys/class/net"] = &fstest.MapFile{Mode: fs.ModeDir}
	for name, vendor := range ifaces {
		m["sys/class/net/"+name] = &fstest.MapFile{Mode: fs.ModeDir}
		if vendor != "" {
			m["sys/class/net/"+name+"/device"] = &fstest.MapFile{Mode: fs.ModeDir}
			m["sys/class/net/"+name+"/device/vendor"] = &fstest.MapFile{Data: []byte(vendor + "\n")}
		}
	}
	return m
}

func TestDetectNICProfileSingleVendor(t *testing.T) {
	fs := makeNetFS(map[string]string{
		"lo":   "",       // loopback; no device/ at all / loopback — device/ 없음
		"eth0": "0x8086", // Intel
		"eth1": "0x8086", // Intel again; one vendor twice counts once / 같은 벤더 두 번은 하나로 취급
		"br0":  "",       // a bridge: virtual, ignored / 브리지 — 가상, 무시
	})
	got := DetectNICProfile(fs)
	if len(got.Vendors) != 1 || got.Vendors[0] != "0x8086" {
		t.Fatalf("Vendors = %v, want [0x8086]", got.Vendors)
	}
	if !got.SinglePCIFamily {
		t.Error("단일 벤더인데 SinglePCIFamily 가 false")
	}
	if got.MultiVendor {
		t.Error("단일 벤더인데 MultiVendor 가 true")
	}
}

func TestDetectNICProfileMultiVendor(t *testing.T) {
	fs := makeNetFS(map[string]string{
		"eth0": "0x8086", // Intel
		"eth1": "0x10ec", // Realtek
	})
	got := DetectNICProfile(fs)
	if len(got.Vendors) != 2 {
		t.Fatalf("Vendors = %v, want 2 entries", got.Vendors)
	}
	// It has to be sorted to be deterministic.
	// 정렬되어 있어야 결정론적이다.
	if got.Vendors[0] != "0x10ec" || got.Vendors[1] != "0x8086" {
		t.Errorf("Vendors = %v, want sorted [0x10ec 0x8086]", got.Vendors)
	}
	if got.SinglePCIFamily {
		t.Error("두 벤더인데 SinglePCIFamily 가 true")
	}
	if !got.MultiVendor {
		t.Error("두 벤더인데 MultiVendor 가 false")
	}
}

func TestDetectNICProfileNoSysfs(t *testing.T) {
	// The case where sys/class/net does not exist at all - a container, a failed
	// mount and so on.
	//
	// sys/class/net 자체가 없는 경우 (컨테이너, 마운트 실패 등).
	got := DetectNICProfile(fstest.MapFS{})
	if !got.SinglePCIFamily {
		t.Error("빈 fs → SinglePCIFamily=true 여야 함")
	}
	if got.MultiVendor {
		t.Error("빈 fs → MultiVendor=false 여야 함")
	}
	if len(got.Vendors) != 0 {
		t.Errorf("빈 fs → Vendors 는 비어야 함, got %v", got.Vendors)
	}
}
