package hwscan

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// fixture builds a sysfs tree shaped like the real thing: one AHCI controller
// on the root bus, one behind a PCIe bridge, and a graphics card that must be
// ignored.
//
// fixture - 실제와 같은 모양의 sysfs 트리를 만든다. 루트 버스의 AHCI
// 컨트롤러 하나, PCIe 브리지 뒤의 하나, 그리고 무시되어야 할 그래픽 카드.
func fixture() fstest.MapFS {
	m := fstest.MapFS{}
	dir := func(p string) { m[p] = &fstest.MapFile{Mode: fs.ModeDir} }
	class := func(p, v string) { m[path.Join(p, "class")] = &fstest.MapFile{Data: []byte(v + "\n")} }
	port := func(ctrl, ata, block string) {
		if block == "" {
			dir(path.Join(ctrl, ata))
			return
		}
		dir(path.Join(ctrl, ata, "host0", "target0:0:0", "0:0:0:0", "block", block))
	}

	const ich9 = "devices/pci0000:00/0000:00:1f.2"
	class(ich9, "0x010601")
	port(ich9, "ata1", "sda")
	port(ich9, "ata2", "sdb")
	// ata10 must sort after ata2, not before it.
	// ata10 은 ata2 앞이 아니라 뒤로 정렬되어야 한다.
	port(ich9, "ata10", "sdc")

	class("devices/pci0000:00/0000:00:02.0", "0x030000") // graphics / 그래픽

	class("devices/pci0000:80/0000:80:08.2", "0x060400") // PCIe bridge / PCIe 브리지
	const bridged = "devices/pci0000:80/0000:80:08.2/0000:81:00.0"
	class(bridged, "0x010601")
	port(bridged, "ata3", "")
	port(bridged, "ata4", "sdd")

	return m
}

func TestScanFindsSATAControllers(t *testing.T) {
	cs, err := Scan(fixture())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(cs) != 2 {
		t.Fatalf("found %d controllers, want 2: %v", len(cs), cs)
	}
	if cs[0].Address != "0000:00:1f.2" || cs[1].Address != "0000:81:00.0" {
		t.Fatalf("addresses = %q, %q", cs[0].Address, cs[1].Address)
	}
}

// TestScanRendersDeviceTreePaths is the part that has to match Synology's
// notation exactly: a device on the root bus keeps its full address, and one
// behind a bridge is written as the bridge plus a device.function hop.
//
// TestScanRendersDeviceTreePaths - 시놀로지 표기와 정확히 맞아야 하는 부분.
// 루트 버스의 장치는 전체 주소를 유지하고, 브리지 뒤의 장치는 브리지 주소에
// device.function 홉을 붙여 적는다.
func TestScanRendersDeviceTreePaths(t *testing.T) {
	cs, err := Scan(fixture())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := cs[0].PCIeRoot; got != "0000:00:1f.2" {
		t.Errorf("root-bus controller = %q, want 0000:00:1f.2", got)
	}
	if got := cs[1].PCIeRoot; got != "0000:80:08.2,00.0" {
		t.Errorf("bridged controller = %q, want 0000:80:08.2,00.0", got)
	}
}

func TestScanNumbersPortsWithinTheController(t *testing.T) {
	cs, err := Scan(fixture())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []Port{
		{Name: "ata1", Index: 0, Block: "sda"},
		{Name: "ata2", Index: 1, Block: "sdb"},
		{Name: "ata10", Index: 2, Block: "sdc"},
	}
	if len(cs[0].Ports) != len(want) {
		t.Fatalf("got %d ports, want %d: %v", len(cs[0].Ports), len(want), cs[0].Ports)
	}
	for i, p := range cs[0].Ports {
		if p != want[i] {
			t.Errorf("port %d = %+v, want %+v", i, p, want[i])
		}
	}
	// The second controller restarts at zero; indices are per controller.
	// 두 번째 컨트롤러는 0 부터 다시 센다. 인덱스는 컨트롤러별이다.
	if cs[1].Ports[0].Index != 0 || cs[1].Ports[0].Block != "" {
		t.Errorf("bridged port 0 = %+v, want index 0 with no disk", cs[1].Ports[0])
	}
	if cs[1].Ports[1].Index != 1 || cs[1].Ports[1].Block != "sdd" {
		t.Errorf("bridged port 1 = %+v", cs[1].Ports[1])
	}
}

func TestScanWithoutPCI(t *testing.T) {
	if _, err := Scan(fstest.MapFS{}); err == nil {
		t.Error("expected an error when sysfs has no PCI host bridges")
	}
}

func TestSummary(t *testing.T) {
	cs, _ := Scan(fixture())
	if got := Summary(cs); got == "" {
		t.Error("Summary produced nothing")
	}
	if got := Summary(nil); got != "no SATA controllers" {
		t.Errorf("Summary(nil) = %q", got)
	}
}

func TestIsPCIAddress(t *testing.T) {
	good := []string{"0000:00:1f.2", "0000:81:00.0", "ffff:ff:1f.7"}
	bad := []string{"ata1", "pci0000:00", "0000:00:1f", "00:1f.2", "zzzz:00:1f.2", "0000:00:1f.x"}
	for _, s := range good {
		if !isPCIAddress(s) {
			t.Errorf("%q should be a PCI address", s)
		}
	}
	for _, s := range bad {
		if isPCIAddress(s) {
			t.Errorf("%q should not be a PCI address", s)
		}
	}
}

func TestExcludePortKeepsRemainingIndices(t *testing.T) {
	cs := []Controller{{
		Address: "0000:00:1f.2",
		Ports: []Port{
			{Name: "ata1", Index: 0, Block: "sda"},
			{Name: "ata2", Index: 1, Block: "sdb"},
			{Name: "ata3", Index: 2, Block: "sdc"},
		},
	}}
	got := ExcludePort(cs, "0000:00:1f.2", 0)
	if len(got) != 1 || len(got[0].Ports) != 2 {
		t.Fatalf("got %v", got)
	}
	// Indices must not be closed up: index 1 is still hardware port 1.
	// 인덱스를 당겨 채우면 안 된다. 인덱스 1 은 여전히 하드웨어 포트 1 이다.
	if got[0].Ports[0].Index != 1 || got[0].Ports[1].Index != 2 {
		t.Fatalf("indices = %d, %d; want 1, 2", got[0].Ports[0].Index, got[0].Ports[1].Index)
	}
}

func TestExcludePortDropsEmptyController(t *testing.T) {
	cs := []Controller{
		{Address: "0000:00:1f.2", Ports: []Port{{Name: "ata1", Index: 0}}},
		{Address: "0000:81:00.0", Ports: []Port{{Name: "ata2", Index: 0}}},
	}
	got := ExcludePort(cs, "0000:00:1f.2", 0)
	if len(got) != 1 || got[0].Address != "0000:81:00.0" {
		t.Fatalf("got %v", got)
	}
}

// fakeDisk writes a disk image with one FAT32 partition carrying a label,
// which is exactly how the image builder lays the loader out.
//
// fakeDisk - 라벨이 붙은 FAT32 파티션 하나짜리 디스크 이미지를 쓴다. 이미지
// 빌더가 로더를 배치하는 모양과 똑같다.
func fakeDisk(t *testing.T, label string) string {
	t.Helper()
	const start = 2048
	img := make([]byte, (start+1)*sectorSize)

	img[mbrSignatureOffset] = 0x55
	img[mbrSignatureOffset+1] = 0xAA
	e := img[mbrPartitionTable:]
	e[4] = partTypeFAT32LBA
	binary.LittleEndian.PutUint32(e[8:], start)

	bpb := img[start*sectorSize:]
	copy(bpb[bpbFSTypeOffset:], "FAT32   ")
	copy(bpb[bpbLabelOffset:bpbLabelOffset+bpbLabelLength], label+"           ")

	p := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(p, img, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFirstPartitionLabel(t *testing.T) {
	got, err := FirstPartitionLabel(fakeDisk(t, "VIBELDR1"))
	if err != nil {
		t.Fatalf("FirstPartitionLabel: %v", err)
	}
	if got != "VIBELDR1" {
		t.Fatalf("label = %q, want VIBELDR1", got)
	}
}

// A data disk has no MBR signature at all, and must read as "no label" rather
// than as an error - otherwise one odd disk would abort the whole scan.
//
// 데이터 디스크에는 MBR 서명이 아예 없고, 오류가 아니라 "라벨 없음" 으로
// 읽혀야 한다. 그러지 않으면 이상한 디스크 하나가 스캔 전체를 멈춘다.
func TestFirstPartitionLabelOnBlankDisk(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blank.img")
	if err := os.WriteFile(p, make([]byte, 4096*sectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FirstPartitionLabel(p)
	if err != nil {
		t.Fatalf("FirstPartitionLabel: %v", err)
	}
	if got != "" {
		t.Fatalf("label = %q, want empty", got)
	}
}

func TestOrderForBaysPutsPopulatedControllersFirst(t *testing.T) {
	empty := Controller{Address: "0000:00:1f.2", PCIeRoot: "0000:00:1f.2",
		Ports: []Port{{Index: 0}, {Index: 1}}}
	used := Controller{Address: "0000:02:07.0", PCIeRoot: "0000:00:1e.0,01.0,07.0",
		Ports: []Port{{Index: 0, Block: "sata1"}, {Index: 1}}}

	got := OrderForBays([]Controller{empty, used})
	if got[0].PCIeRoot != used.PCIeRoot {
		t.Errorf("first controller is %s, want the one holding a disk", got[0].PCIeRoot)
	}

	// Two populated controllers keep hardware order between them, so the bay
	// numbering does not depend on which one happens to be scanned first.
	//
	// 디스크가 붙은 컨트롤러 둘은 서로 하드웨어 순서를 지킨다. 그래야 베이
	// 번호가 어느 쪽이 먼저 스캔됐는지에 좌우되지 않는다.
	also := Controller{Address: "0000:03:00.0", PCIeRoot: "0000:00:1c.0,00.0",
		Ports: []Port{{Index: 0, Block: "sata9"}}}
	got = OrderForBays([]Controller{used, also, empty})
	if got[0].PCIeRoot != used.PCIeRoot || got[1].PCIeRoot != also.PCIeRoot {
		t.Errorf("populated controllers were reordered: %v, %v", got[0].PCIeRoot, got[1].PCIeRoot)
	}
	if got[2].PCIeRoot != empty.PCIeRoot {
		t.Errorf("the empty controller is not last, got %s", got[2].PCIeRoot)
	}
}

// OrderForBays must not disturb the caller's slice: the scan result is also
// what the loader-disk exclusion works from.
//
// OrderForBays 는 호출자의 슬라이스를 흐트러뜨리면 안 된다. 로더 디스크
// 제외도 같은 스캔 결과를 바탕으로 동작한다.
func TestOrderForBaysDoesNotMutateItsInput(t *testing.T) {
	in := []Controller{
		{Address: "a", Ports: []Port{{Index: 0}}},
		{Address: "b", Ports: []Port{{Index: 0, Block: "sata1"}}},
	}
	_ = OrderForBays(in)
	if in[0].Address != "a" {
		t.Error("OrderForBays reordered the slice it was given")
	}
}

// TestScanFindsSCSIHBADisks: a SAS, RAID or virtio-scsi HBA creates no ata*
// ports. A scan that counts only ata* ports makes the install wizard show zero
// disks even while megaraid_sas is loaded and /dev/sda is right there.
//
// TestScanFindsSCSIHBADisks - SAS/RAID/virtio-scsi HBA 는 ata* 포트를 만들지
// 않는다. ata* 포트만 세는 스캔이면 megaraid_sas 가 로드되고 /dev/sda 가
// 있는데도 설치 마법사가 디스크 0 개를 띄운다.
func TestScanFindsSCSIHBADisks(t *testing.T) {
	m := fstest.MapFS{}
	dir := func(p string) { m[p] = &fstest.MapFile{Mode: fs.ModeDir} }
	class := func(p, v string) { m[path.Join(p, "class")] = &fstest.MapFile{Data: []byte(v + "\n")} }

	// LSI MegaRAID: host0/target0:2:<n>:0/0:2:<n>:0/block/sd*
	// LSI MegaRAID 는 위 경로 아래에 디스크가 달린다.
	const mega = "devices/pci0000:00/0000:00:05.0"
	class(mega, "0x010400")
	dir(path.Join(mega, "host0", "target0:2:0:0", "0:2:0:0", "block", "sda"))
	dir(path.Join(mega, "host0", "target0:2:10:0", "0:2:10:0", "block", "sdc"))
	dir(path.Join(mega, "host0", "target0:2:2:0", "0:2:2:0", "block", "sdb"))
	// A cross-reference through a symlink is not an IsDir, so it is not followed.
	// 심볼릭 링크로 걸린 교차 참조는 IsDir 이 아니므로 안 따라간다.
	m[path.Join(mega, "driver")] = &fstest.MapFile{Mode: fs.ModeSymlink}

	// virtio-scsi puts one more step, virtio<N>, between the PCI directory and
	// host*.
	//
	// virtio-scsi 는 PCI 디렉터리와 host* 사이에 virtio<N> 이 한 단계 낀다.
	const vscsi = "devices/pci0000:00/0000:00:07.0"
	class(vscsi, "0x010000")
	dir(path.Join(vscsi, "virtio1", "host2", "target2:0:0", "2:0:0:0", "block", "sde"))

	// A SAS expander in the path makes the hops deeper still.
	// SAS 익스팬더가 끼면 홉이 더 깊어진다.
	const sas = "devices/pci0000:00/0000:00:06.0"
	class(sas, "0x010700")
	dir(path.Join(sas, "host1", "port-1:0", "end_device-1:0", "target1:0:0", "1:0:0:0", "block", "sdd"))

	cs, err := Scan(m)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(cs) != 3 {
		t.Fatalf("found %d controllers, want 3: %v", len(cs), cs)
	}
	// 0000:00:07.0 is the virtio-scsi one. Sorted by address, it comes last,
	// after the MegaRAID (05.0) and SAS (06.0) controllers.
	//
	// 0000:00:07.0 이 virtio-scsi. 주소 순 정렬이라 MegaRAID(05.0), SAS(06.0)
	// 다음인 맨 끝이다.
	var vs *Controller
	for i := range cs {
		if cs[i].Address == "0000:00:07.0" {
			vs = &cs[i]
		}
	}
	if vs == nil || len(vs.Ports) != 1 || vs.Ports[0].Block != "sde" {
		t.Fatalf("virtio-scsi ports = %v", vs)
	}
	if cs[0].Kind != KindSCSI || cs[1].Kind != KindSCSI {
		t.Fatalf("kinds = %v, %v, want KindSCSI", cs[0].Kind, cs[1].Kind)
	}
	// Sorted as a string, 0:2:10:0 comes before 0:2:2:0. The comparison has to be
	// numeric, field by field.
	//
	// 0:2:10:0 은 문자열 정렬이면 0:2:2:0 앞에 온다. 칸별 숫자 비교여야 한다.
	var got []string
	for _, p := range cs[0].Ports {
		got = append(got, fmt.Sprintf("%d=%s", p.Index, p.Block))
	}
	if strings.Join(got, " ") != "0=sda 1=sdb 2=sdc" {
		t.Fatalf("megaraid ports = %q", strings.Join(got, " "))
	}
	if len(cs[1].Ports) != 1 || cs[1].Ports[0].Block != "sdd" {
		t.Fatalf("sas ports = %v", cs[1].Ports)
	}
}
