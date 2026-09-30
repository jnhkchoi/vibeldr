package hwscan

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// A virtio card is named by its PCI device, not by the virtioN between; a USB
// card by its controller and port.
//
// virtio 카드는 사이의 virtioN 이 아니라 PCI 장치로, USB 카드는 컨트롤러와 포트로
// 부른다.
func TestNICKeyFromPath(t *testing.T) {
	for path, want := range map[string]string{
		"/sys/devices/pci0000:00/0000:00:12.0/virtio2":                  "0000:00:12.0",
		"/sys/devices/pci0000:00/0000:00:1c.0/0000:03:00.0":             "0000:03:00.0",
		"/sys/devices/pci0000:00/0000:00:14.0/usb2/2-1/2-1.3/2-1.3:1.0": "0000:00:14.0/2-1/2-1.3",
		"/sys/devices/virtual/net/br0":                                  "",
	} {
		if got := nicKeyFromPath(path); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// NICKey follows the device symlink the way sysfs lays it out.
// NICKey 는 sysfs 가 까는 대로 device 심볼릭 링크를 따라간다.
func TestNICKeySysfs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	root := t.TempDir()
	dev := filepath.Join(root, "devices", "pci0000:00", "0000:00:12.0", "virtio0")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	netDir := filepath.Join(root, "class", "net", "eth0")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dev, filepath.Join(netDir, "device")); err != nil {
		t.Fatal(err)
	}
	if got := NICKey(root, "eth0"); got != "0000:00:12.0" {
		t.Fatalf("got %q", got)
	}
	if got := NICKey(root, "lo"); got != "" {
		t.Fatalf("lo got %q", got)
	}
}

// Planned cards come first in plan order, new cards after them in the order
// given, and cards that are gone drop out.
//
// 계획된 카드가 계획 순서로 먼저 오고, 새 카드는 그 뒤에 받은 순서대로 오며,
// 없어진 카드는 빠진다.
func TestOrderNICs(t *testing.T) {
	got := OrderNICs([]string{"mac:bc2411001923", "0000:00:12.0", "mac:bc2411001921"}, []string{"mac:bc2411001921", "mac:bc2411009999"})
	want := []string{"mac:bc2411001921", "mac:bc2411001923", "0000:00:12.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if plan := ParseNICPlan(string(RenderNICPlan(want)) + "\n \n"); !reflect.DeepEqual(plan, want) {
		t.Fatalf("round trip %v", plan)
	}
}

// Swapping two cards goes through temporary names; cards already in place
// need nothing; a card the plan does not name keeps its place after the rest.
//
// 두 카드를 맞바꾸면 임시 이름을 거친다. 이미 제자리인 카드는 할 일이 없다.
// 계획에 없는 카드는 나머지 뒤에서 자기 자리를 지킨다.
func TestNICRenames(t *testing.T) {
	cur := map[string]string{"eth0": "0000:00:12.0", "eth1": "0000:03:00.0", "eth2": "0000:00:14.0/1-2"}
	got := NICRenames(cur, []string{"0000:03:00.0", "0000:00:12.0"})
	want := []NICRename{
		{"eth1", "vibeldr0"}, {"eth0", "vibeldr1"}, {"eth2", "vibeldr2"},
		{"vibeldr0", "eth0"}, {"vibeldr1", "eth1"}, {"vibeldr2", "eth2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if r := NICRenames(cur, []string{"0000:00:12.0", "0000:03:00.0"}); r != nil {
		t.Fatalf("in place already, got %v", r)
	}
	if r := NICRenames(cur, nil); r != nil {
		t.Fatalf("no plan, got %v", r)
	}
}

// With a plan each MAC follows its card even when DSM has named the cards in
// another order; without one it goes by position. A planned card that is gone
// gets no interface.
//
// 계획이 있으면 DSM 이 카드 이름을 다른 순서로 붙였어도 MAC 이 자기 카드를
// 따라간다. 없으면 자리대로 간다. 계획된 카드가 없으면 인터페이스를 받지 못한다.
func TestMACTargets(t *testing.T) {
	ifaces := []string{"eth0", "eth1", "eth2"}
	keys := map[string]string{"eth0": "0000:06:13.0", "eth1": "0000:06:14.0", "eth2": "0000:06:12.0"}
	plan := []string{"0000:06:12.0", "0000:06:13.0", "0000:06:14.0"}
	if got := MACTargets(ifaces, keys, plan, 3); !reflect.DeepEqual(got, []string{"eth2", "eth0", "eth1"}) {
		t.Fatalf("by card: %v", got)
	}
	if got := MACTargets(ifaces, keys, nil, 2); !reflect.DeepEqual(got, []string{"eth0", "eth1"}) {
		t.Fatalf("by position: %v", got)
	}
	if got := MACTargets(ifaces, keys, []string{"0000:09:00.0", "0000:06:13.0"}, 2); !reflect.DeepEqual(got, []string{"", "eth0"}) {
		t.Fatalf("missing card: %v", got)
	}
}
