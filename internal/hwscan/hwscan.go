// Package hwscan reads sysfs to find the storage hardware this machine
// actually has.
//
// This is what replaces guessing. DSM's device tree names the controllers of
// the original appliance, so matching it to this machine means obtaining the
// same facts by discovery rather than from configuration: which SATA
// controllers are present, where they are on the PCI bus, how many ports they
// have.
//
// The scan reads through an fs.FS rather than the filesystem directly, so the
// whole thing can be tested from fixtures on any platform - including ones
// where a colon cannot appear in a file name.
//
// Package hwscan - sysfs 를 읽어 이 머신에 실제로 붙어있는 스토리지
// 하드웨어를 파악.
//
// 추측을 대체하는 부분이다. DSM 의 device tree 는 원래 어플라이언스의
// 컨트롤러 이름을 담고 있으니, 지금 이 머신에 맞추려면 같은 사실을 설정이
// 아닌 발견으로 얻어야 한다: SATA 컨트롤러가 뭐뭐 있는지, PCI 어디에
// 있는지, 포트가 몇 개인지.
//
// 스캔은 파일시스템을 직접 읽지 않고 fs.FS 를 통해 읽는다. 그래서 어느
// 플랫폼에서도 픽스처로 전체 테스트가 가능하다 (파일명에 콜론이 못 들어가는
// OS 포함).
package hwscan

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// DefaultSysfs is where sysfs is mounted on a booted system.
// DefaultSysfs - 부팅된 시스템에서 sysfs 가 마운트된 위치.
const DefaultSysfs = "/sys"

// classSATA is the PCI class/subclass pair that means "SATA controller". The
// programming-interface byte that follows it names the detailed mode (AHCI is
// 0x01) and is left out of the match here.
//
// classSATA - SATA 컨트롤러를 나타내는 PCI class/subclass 조합. 뒤에 오는
// programming-interface 바이트는 AHCI(0x01) 등 세부 모드를 가리키는데
// 여기 매칭에는 넣지 않는다.
const classSATA = 0x0106

// The PCI class/subclass pairs for SCSI-family controllers. They do not go
// through libata, so they create no ata* ports in sysfs; their disks hang
// directly under host*/target*/...
//
//	0x0100 SCSI - virtio-scsi, older SCSI HBAs
//	0x0104 RAID - LSI MegaRAID (megaraid_sas), Adaptec, Areca
//	0x0107 SAS  - LSI SAS (mpt3sas) and most other HBAs
//
// Counting only ata* ports and ignoring these three classes gives a scan
// result of zero even when the driver is loaded and /dev/sda is right there.
//
// SCSI 계열 컨트롤러의 PCI class/subclass. libata 를 안 거치므로 sysfs 에
// ata* 포트를 만들지 않고, 디스크가 host*/target*/... 아래에 바로 달린다.
//
// 이 세 class 를 안 보고 ata* 포트만 세면, 드라이버가 멀쩡히 올라와
// /dev/sda 가 있어도 스캔 결과가 0 개로 나온다.
const (
	classSCSI = 0x0100
	classRAID = 0x0104
	classSAS  = 0x0107
)

// Kind is the controller family. SataPortMap and DiskIdxMap only mean anything
// for SATA ports, and mixing the two families throws the whole mapping out, so
// they are kept apart.
//
// Kind - 컨트롤러 계열. SataPortMap/DiskIdxMap 은 SATA 포트에만 의미가
// 있어서, 둘을 섞으면 매핑이 통째로 어긋난다. 그래서 구분해 둔다.
type Kind uint8

const (
	KindSATA Kind = iota // libata; creates ata* ports / ata* 포트를 만든다
	KindSCSI             // SAS/RAID/SCSI HBA; no ata* ports / ata* 포트가 없다
)

// Port is one port on a controller.
// Port - 컨트롤러 위의 포트 하나.
type Port struct {
	// Name is what the kernel calls the port. For SATA it is an ata number
	// such as "ata3", numbered across the whole machine, so the name alone
	// does not say where the port sits on its controller. SCSI-family
	// controllers have no ata number, so the SCSI address ("0:2:0:0") is used.
	//
	// Name - 커널이 부르는 포트 이름. SATA 는 "ata3" 처럼 ata 번호이고,
	// 번호가 머신 전체에 걸쳐 매겨지므로 컨트롤러 안에서의 위치는 이
	// 이름만으로 알 수 없다. SCSI 계열은 ata 번호가 없어서 SCSI 주소
	// ("0:2:0:0") 를 그대로 쓴다.
	Name string
	// Index is the position on the controller, counted from zero. It is the
	// value the device tree's ata_port property holds.
	//
	// Index - 컨트롤러 안에서의 위치 (0 부터). device tree 의 ata_port
	// 속성이 담는 값.
	Index uint32
	// Block is the disk attached to the port, such as "sda". Empty when the
	// port is unpopulated.
	//
	// Block - 이 포트에 붙은 디스크 ("sda" 등). 비어 있으면 빈 포트다.
	Block string
}

// Controller is one storage controller.
// Controller - 스토리지 컨트롤러 하나.
type Controller struct {
	// Address is the PCI address, in "0000:00:1f.2" form.
	// Address - PCI 주소 ("0000:00:1f.2" 형식).
	Address string
	// PCIeRoot is the path in the notation a Synology device tree uses: the
	// device on the root bus, plus one "dd.f" hop per bridge below it.
	//
	// PCIeRoot - 시놀로지 device tree 가 쓰는 표기의 경로: 루트 버스 위의
	// 장치 + 아래 브리지마다 "dd.f" 홉 하나씩.
	PCIeRoot string
	// Class is the full PCI class word (AHCI is 0x010601).
	// Class - 전체 PCI class word (예: AHCI 는 0x010601).
	Class uint32
	// Kind is SATA or SCSI-family, which is also how the ports were read.
	// Kind - SATA 인지 SCSI 계열인지. 포트를 어떻게 읽었는지와 같다.
	Kind  Kind
	Ports []Port
}

func (c Controller) String() string {
	return fmt.Sprintf("%s (%s) class 0x%06x, %d port(s)", c.Address, c.PCIeRoot, c.Class, len(c.Ports))
}

// ScanSysfs scans the machine this is running on.
// ScanSysfs - 현재 도는 머신을 스캔.
func ScanSysfs() ([]Controller, error) { return Scan(os.DirFS(DefaultSysfs)) }

// Scan finds every storage controller under a sysfs tree, in PCI address order.
// Scan - sysfs 트리 아래에서 모든 스토리지 컨트롤러를 PCI 주소 순으로 찾는다.
func Scan(fsys fs.FS) ([]Controller, error) {
	roots, err := fs.Glob(fsys, "devices/pci*")
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("hwscan: no PCI host bridges under devices/")
	}

	var found []Controller
	for _, root := range roots {
		descend(fsys, root, root, &found)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Address < found[j].Address })
	return found, nil
}

// descend walks the PCI hierarchy only: down through bridges, stopping
// anywhere else. Walking the whole subtree would mean rummaging through every
// device's attributes for nothing, because the PCI topology is exactly "the
// chain of directories whose names are PCI addresses".
//
// descend - PCI 계층만 탐색한다. 브리지로 내려가고 그 외에선 멈춘다. 전체
// 서브트리를 훑으면 장치별 속성을 다 뒤지게 되는데 얻는 게 없다. PCI
// 토폴로지는 정확히 "이름이 PCI 주소인 디렉터리의 사슬" 이다.
func descend(fsys fs.FS, hostBridge, dir string, out *[]Controller) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !isPCIAddress(e.Name()) {
			continue
		}
		child := path.Join(dir, e.Name())
		if class, ok := readClass(fsys, child); ok {
			c := Controller{
				Address:  e.Name(),
				PCIeRoot: pcieRoot(hostBridge, child),
				Class:    class,
			}
			switch class >> 8 {
			case classSATA:
				c.Kind, c.Ports = KindSATA, readPorts(fsys, child)
				*out = append(*out, c)
				continue
			case classSCSI, classRAID, classSAS:
				c.Kind, c.Ports = KindSCSI, readSCSIPorts(fsys, child)
				*out = append(*out, c)
				continue
			}
		}
		descend(fsys, hostBridge, child, out)
	}
}

// isPCIAddress reports whether a sysfs directory name is a PCI address in the
// usual domain:bus:device.function form.
//
// isPCIAddress - sysfs 디렉터리 이름이 통상의 domain:bus:device.function
// 형태의 PCI 주소인지.
func isPCIAddress(name string) bool {
	colon := strings.Split(name, ":")
	if len(colon) != 3 {
		return false
	}
	if len(colon[0]) != 4 || len(colon[1]) != 2 {
		return false
	}
	dot := strings.Split(colon[2], ".")
	if len(dot) != 2 || len(dot[0]) != 2 {
		return false
	}
	for _, s := range []string{colon[0], colon[1], dot[0], dot[1]} {
		if _, err := strconv.ParseUint(s, 16, 32); err != nil {
			return false
		}
	}
	return true
}

func readClass(fsys fs.FS, dir string) (uint32, bool) {
	b, err := fs.ReadFile(fsys, path.Join(dir, "class"))
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(string(b)), "0x"), 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// pcieRoot renders a device's position the way a Synology device tree does.
//
// sysfs already lays the topology out as nested directories, so the path from
// the host bridge to the device is the chain of bridges to traverse. The first
// hop keeps its full address because it identifies the root bus; every hop
// below it is relative and only needs device and function.
//
// pcieRoot - 장치의 위치를 시놀로지 device tree 표기로 렌더한다.
//
// sysfs 가 이미 토폴로지를 중첩 디렉터리로 펼쳐 두므로, host bridge 에서
// 장치까지의 경로가 곧 거쳐 갈 브리지의 사슬이다. 첫 홉은 루트 버스를
// 식별하므로 전체 주소를 유지하고, 그 아래는 상대 위치라 device 와
// function 만 있으면 된다.
func pcieRoot(hostBridge, dev string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(dev, hostBridge), "/")
	parts := strings.Split(rel, "/")

	out := make([]string, 0, len(parts))
	for i, p := range parts {
		if i == 0 {
			out = append(out, p)
			continue
		}
		// "0000:81:00.0" below a bridge is written as just "00.0".
		// 브리지 아래의 "0000:81:00.0" 은 "00.0" 으로만 적는다.
		if idx := strings.LastIndex(p, ":"); idx >= 0 {
			p = p[idx+1:]
		}
		out = append(out, p)
	}
	return strings.Join(out, ",")
}

// readPorts lists the ATA ports a controller owns, in port order.
// readPorts - 컨트롤러가 가진 ATA 포트를 포트 순서대로 나열한다.
func readPorts(fsys fs.FS, dir string) []Port {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	type named struct {
		name string
		num  int
	}
	var ata []named
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "ata") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "ata"))
		if err != nil {
			continue
		}
		ata = append(ata, named{e.Name(), n})
	}
	// The kernel numbers ATA ports in the order it registers them, which for a
	// given controller is port order. Sorting numerically recovers that; a
	// plain string sort would put ata10 before ata2.
	//
	// 커널은 ATA 포트를 등록 순서대로 번호 매기는데, 한 컨트롤러 안에서는
	// 그게 곧 포트 순서다. 숫자로 정렬해야 그 순서가 살아난다. 문자열
	// 정렬이면 ata10 이 ata2 앞에 온다.
	sort.Slice(ata, func(i, j int) bool { return ata[i].num < ata[j].num })

	ports := make([]Port, 0, len(ata))
	for _, a := range ata {
		block := blockDevice(fsys, path.Join(dir, a.name))
		// Leave DUMMY ports out. A port with no disk whose AHCI port command
		// register reads 0 is one the kernel registered but cannot actually
		// use. Counting it inflates SataPortMap and makes the DSM installer
		// report "SATA port disabled". A port with a disk on it is always
		// kept, so a misjudgement here cannot discard a real disk.
		//
		// DUMMY 포트 제외: 디스크도 없고 AHCI 포트 커맨드 레지스터가 0 이면
		// 커널이 등록만 하고 실제로는 못 쓰는 포트다. 세면 SataPortMap 이
		// 부풀고, DSM 설치기가 "SATA port disabled" 를 띄운다. 붙은 디스크가
		// 있으면 무조건 살린다 (판정 오류로 실제 디스크를 버리지 않게).
		if block == "" && ahciPortDummy(fsys, path.Join(dir, a.name)) {
			continue
		}
		ports = append(ports, Port{
			Name:  a.name,
			Index: uint32(len(ports)),
			Block: block,
		})
	}
	return ports
}

// readSCSIPorts lays out the disks on a SCSI-family controller as ports.
//
// A controller that libata does not sit in front of creates no ata* directory.
// Its disks hang directly under a SCSI address, as in
// host0/target0:2:0/0:2:0:0/block/sda, and a SAS expander adds port- and
// end_device- hops in between. The depth varies with the layout, so the search
// looks for block/<name> anywhere inside the host* subtree.
//
// The walk is confined to host* because sysfs is large. Everything else met
// along the way - driver, subsystem and so on - is a symlink, so IsDir is false
// and the walk never follows it: it stays inside the real device tree.
//
// Unlike SATA there is no such thing as an empty port here. SCSI enumerates
// only the targets that answered, so the number of ports is the number of
// disks.
//
// readSCSIPorts - SCSI 계열 컨트롤러에 달린 디스크를 포트로 늘어놓는다.
//
// libata 가 안 끼는 컨트롤러는 ata* 디렉터리를 만들지 않는다. 디스크는
// host0/target0:2:0/0:2:0:0/block/sda 처럼 SCSI 주소 밑에 바로 달리고,
// SAS 익스팬더가 있으면 그 사이에 port-/end_device- 홉이 더 낀다. 깊이가
// 구성마다 달라서, host* 서브트리 안에서 block/<이름> 을 찾는다.
//
// 훑는 범위를 host* 아래로 묶어두는 이유는 sysfs 가 크기 때문이다. 그리고
// 여기서 만나는 driver/subsystem 따위는 전부 심볼릭 링크라 IsDir 이 거짓이
// 되어 들어가지 않는다 - 실제 장치 트리 안에만 머문다.
//
// SATA 와 달리 "비어있는 포트" 라는 개념이 없다. SCSI 는 응답한 타겟만
// 열거하므로 여기서 나오는 포트 수가 곧 붙어있는 디스크 수다.
func readSCSIPorts(fsys fs.FS, dir string) []Port {
	// host* is sometimes directly under the PCI directory (megaraid_sas,
	// mpt3sas) and sometimes one level further in (virtio-scsi puts it at
	// <PCI>/virtio1/host2/...). Looking only at the first misses virtio-scsi
	// disks entirely.
	//
	// host* 가 PCI 디렉터리 바로 아래 있는 경우(megaraid_sas, mpt3sas)와
	// 한 단계 더 들어간 경우(virtio-scsi 는 <PCI>/virtio1/host2/...)가 있다.
	// 앞의 것만 보면 virtio-scsi 디스크가 통째로 안 보인다.
	var hosts []string
	seenHost := map[string]bool{}
	for _, pat := range []string{path.Join(dir, "host*"), path.Join(dir, "*", "host*")} {
		m, err := fs.Glob(fsys, pat)
		if err != nil {
			continue
		}
		for _, h := range m {
			// The two patterns can match the same place. Walking it twice
			// counts the disk twice, which inflates the port count and throws
			// the bay assignment out.
			//
			// 두 패턴이 같은 자리를 잡을 수 있다. 중복으로 훑으면 디스크가
			// 두 번 세어져 포트 수가 부풀고 베이 배정이 어긋난다.
			if !seenHost[h] {
				seenHost[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	type disk struct {
		addr  string
		block string
	}
	var found []disk
	seenBlock := map[string]bool{}
	for _, h := range hosts {
		_ = fs.WalkDir(fsys, h, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			// Is this the last element of .../<SCSI address>/block/<name>?
			// .../<SCSI 주소>/block/<커널 이름> 의 마지막 칸인지.
			if path.Base(path.Dir(p)) != "block" {
				return nil
			}
			if !seenBlock[d.Name()] {
				seenBlock[d.Name()] = true
				found = append(found, disk{path.Base(path.Dir(path.Dir(p))), d.Name()})
			}
			return fs.SkipDir
		})
	}
	// A SCSI address is host:channel:target:lun, so a string sort puts 10
	// before 2. Comparing each field numerically is what matches the kernel's
	// enumeration order.
	//
	// SCSI 주소는 host:channel:target:lun 이라 문자열 정렬이면 10 번이 2 번
	// 앞에 온다. 칸마다 숫자로 비교해야 커널 열거 순서와 같아진다.
	sort.Slice(found, func(i, j int) bool { return scsiAddrLess(found[i].addr, found[j].addr) })

	ports := make([]Port, 0, len(found))
	for _, f := range found {
		ports = append(ports, Port{Name: f.addr, Index: uint32(len(ports)), Block: f.block})
	}
	return ports
}

// scsiAddrLess compares "0:2:0:0" style SCSI addresses field by field, as
// numbers.
//
// scsiAddrLess - "0:2:0:0" 형식의 SCSI 주소를 칸별 숫자로 비교.
func scsiAddrLess(a, b string) bool {
	as, bs := strings.Split(a, ":"), strings.Split(b, ":")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, errx := strconv.Atoi(as[i])
		y, erry := strconv.Atoi(bs[i])
		if errx != nil || erry != nil {
			return a < b
		}
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}

// ahciPortDummy reports whether an AHCI port is a DUMMY, non-functional one.
// A port command register (ahci_port_cmd) reading "0" means the port was never
// started, which is what DUMMY means. When the file is absent - on a non-AHCI
// controller, say - the port is not treated as DUMMY.
//
// ahciPortDummy - AHCI 포트가 DUMMY(비기능) 인지. 포트 커맨드 레지스터
// (ahci_port_cmd) 가 "0" 이면 포트가 시작되지 않은 것 = DUMMY. 파일이
// 없으면(비 AHCI 등) DUMMY 로 보지 않는다.
func ahciPortDummy(fsys fs.FS, portDir string) bool {
	matches, err := fs.Glob(fsys, path.Join(portDir, "host*", "scsi_host", "host*", "ahci_port_cmd"))
	if err != nil || len(matches) == 0 {
		return false
	}
	b, err := fs.ReadFile(fsys, matches[0])
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "0"
}

// blockDevice finds the disk attached to an ATA port, if any. The path runs
// ata3/host2/target2:0:0/2:0:0:0/block/sda.
//
// blockDevice - ATA 포트에 붙은 디스크를 찾는다. 경로는
// ata3/host2/target2:0:0/2:0:0:0/block/sda 형태다.
func blockDevice(fsys fs.FS, portDir string) string {
	matches, err := fs.Glob(fsys, path.Join(portDir, "host*", "target*", "*", "block", "*"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return path.Base(matches[0])
}

// Summary renders a scan for a boot-time log line.
// Summary - 스캔 결과를 부팅 로그 한 줄로 렌더한다.
func Summary(cs []Controller) string {
	if len(cs) == 0 {
		return "no SATA controllers"
	}
	var b strings.Builder
	for i, c := range cs {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s x%d", c.PCIeRoot, len(c.Ports))
		var disks []string
		for _, p := range c.Ports {
			if p.Block != "" {
				disks = append(disks, fmt.Sprintf("%d=%s", p.Index, p.Block))
			}
		}
		if len(disks) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(disks, " "))
		}
	}
	return b.String()
}

// HasDisk reports whether any of the controller's ports currently holds one.
// HasDisk - 이 컨트롤러의 포트 중 지금 디스크가 붙은 것이 있는지.
func (c Controller) HasDisk() bool {
	for _, p := range c.Ports {
		if p.Block != "" {
			return true
		}
	}
	return false
}

// OrderForBays puts the controllers that carry disks first, keeping hardware
// order within each group, and returns a new slice.
//
// Bay numbers follow this order, so it decides which bay the first disk lands
// in. Without it a machine whose chipset provides a SATA controller nobody uses
// - an empty 0000:00:1f.2, common on emulated chipsets - spends the first six
// bays on that controller and shows the only disk in bay seven.
//
// Ordering by controller rather than by port is what keeps this safe. Adding a
// disk to a controller that already has one changes nothing, so a storage pool
// cannot come back pointing at different drives. Only bringing a previously
// unused controller into service reorders anything, and that is a hardware
// change the machine would notice anyway.
//
// OrderForBays - 디스크가 붙은 컨트롤러를 앞으로 보내고, 각 그룹 안에서는
// 하드웨어 순서를 유지한 새 슬라이스를 돌려준다.
//
// 베이 번호가 이 순서를 따르므로, 첫 디스크가 몇 번 베이에 놓이는지를 이
// 함수가 정한다. 이게 없으면 아무도 안 쓰는 칩셋 SATA 컨트롤러 (에뮬레이션
// 칩셋에 흔한 빈 0000:00:1f.2) 가 앞 여섯 베이를 차지하고, 하나뿐인 디스크가
// 7 번 베이에 뜬다.
//
// 포트가 아니라 컨트롤러 단위로 정렬하는 것이 안전의 핵심이다. 이미 디스크가
// 있는 컨트롤러에 디스크를 하나 더 붙여도 순서는 그대로라, 스토리지 풀이
// 다른 드라이브를 가리키게 되는 일이 없다. 순서가 바뀌는 경우는 안 쓰던
// 컨트롤러를 새로 쓰기 시작할 때뿐이고, 그건 어차피 머신이 알아차릴 만한
// 하드웨어 변경이다.
func OrderForBays(cs []Controller) []Controller {
	out := make([]Controller, len(cs))
	copy(out, cs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].HasDisk() && !out[j].HasDisk() })
	return out
}
