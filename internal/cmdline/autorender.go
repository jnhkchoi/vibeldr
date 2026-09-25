package cmdline

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"vibeldr/internal/catalog"
	"vibeldr/internal/config"
	"vibeldr/internal/hwscan"
)

// StoragePolicy is Build's decision about which storage mapping parameters go
// on the command line. Exactly one of the two mechanisms - the
// SataPortMap+DiskIdxMap pair, or sata_remap - must be emitted, or they
// conflict at boot.
//
// A non-empty Portmap emits SataPortMap and DiskIdxMap. UseRemap emits
// sata_remap, and an empty Remap emits it with no value, which means "remap
// nothing, explicitly".
//
// StoragePolicy - Build 가 커맨드라인에 어떤 스토리지 매핑 파라미터를
// 낼지 결정한 결과. 두 방식 (SataPortMap+DiskIdxMap 쌍 / sata_remap) 중
// 정확히 하나만 방출되어야 부팅 시 서로 충돌하지 않는다.
//
// Portmap 이 비어있지 않으면 SataPortMap+DiskIdxMap 을 방출한다.
// UseRemap 이 true 면 sata_remap 을 방출한다 (Remap 이 빈 문자열이면
// "명시적으로 재매핑 안 함" 뜻으로 값 없는 파라미터를 낸다).
type StoragePolicy struct {
	Portmap  string
	IdxMap   string
	Remap    string
	UseRemap bool
}

// legacyPortmapPlatforms are the x86 generations that use no device tree and
// have to map slots statically with SataPortMap and DiskIdxMap. Anything not
// listed here falls back to sata_remap as the default mechanism. DT=true
// platforms (geminilake, broadwellnkv2 and so on) are not listed, because
// SelectStoragePolicy below checks plat.DT first. bromolow is not in the
// catalog but the generation name is widely used, so it is kept.
//
// legacyPortmapPlatforms - 디바이스 트리를 안 쓰고 SataPortMap+DiskIdxMap
// 로 슬롯을 정적 매핑해야 하는 x86 세대들. 여기 없으면 sata_remap 을 기본
// 매커니즘으로 삼는다. DT=true 플랫폼 (geminilake, broadwellnkv2 등) 은
// 아래 SelectStoragePolicy 가 plat.DT 를 먼저 보고 걸러내므로 여기 넣지
// 않는다. bromolow 는 카탈로그에 없지만 세대 이름이 널리 통해 유지한다.
var legacyPortmapPlatforms = map[string]bool{
	"bromolow":       true,
	"apollolake":     true,
	"broadwell":      true,
	"broadwellnk":    true,
	"broadwellntbap": true,
	"denverton":      true,
}

// ataNumOf pulls the number out of a port name like "ata7". An unreadable name
// gives a large value, so it sorts to the back.
//
// ataNumOf - "ata7" 같은 포트 이름에서 번호를 뽑는다. 못 읽으면 큰 값을
// 돌려 뒤로 밀리게 한다.
func ataNumOf(name string) int {
	n := 0
	seen := false
	for i := 0; i < len(name); i++ {
		if name[i] >= '0' && name[i] <= '9' {
			n = n*10 + int(name[i]-'0')
			seen = true
		} else if seen {
			break
		}
	}
	if !seen {
		return 1 << 30
	}
	return n
}

// portMapFor builds SataPortMap and DiskIdxMap from a controller list.
//
// SataPortMap is one digit per controller, saying how many of its ports to use.
// DiskIdxMap is two hex digits per controller, saying which disk index that
// controller's first port is.
//
// Two things matter:
//
//  1. The order must be DSM's controller enumeration order. That order is the
//     order the kernel assigns ata numbers in, so the controllers are sorted by
//     their lowest ata number. Get this wrong and the whole mapping lands on
//     different controllers.
//  2. The start index is derived backwards from the bays the user chose.
//     bayOrder is a list of port keys in bay order, so if some port is bay n
//     then its controller must start at n-1 minus the port's position within
//     the controller for that port to come out as bay n. Controllers with no
//     assignment are placed in the remaining slots in turn.
//
// maxBays is the bay count this model admits to (maxdisks in synoinfo).
// Anything past it is ignored by DSM, so that is where it stops.
//
// portMapFor - 컨트롤러 목록에서 SataPortMap 과 DiskIdxMap 을 만든다.
//
// SataPortMap 은 컨트롤러마다 쓸 포트 수를 한 자리씩, DiskIdxMap 은 컨트롤러
// 마다 첫 포트가 몇 번 디스크인지를 16진수 두 자리씩 이어 붙인 문자열이다.
//
// 두 가지가 중요하다:
//
//  1. 순서가 DSM 의 컨트롤러 열거 순서와 같아야 한다. 커널이 ata 번호를
//     매기는 순서가 곧 그 순서라, 컨트롤러를 가장 작은 ata 번호로 정렬한다.
//     이걸 안 맞추면 매핑이 통째로 다른 컨트롤러에 적용된다.
//  2. 시작 인덱스는 사용자가 고른 베이에서 역산한다. bayOrder 는 "포트 키를
//     베이 순서대로 적은 목록" 이므로, 어떤 포트가 n 번 베이면 그 컨트롤러는
//     n-1-(컨트롤러 안 포트 위치) 에서 시작해야 그 포트가 n 번이 된다.
//     지정이 없는 컨트롤러는 남은 자리에 차례로 놓는다.
//
// maxBays 는 이 모델이 인정하는 베이 수(synoinfo 의 maxdisks) 다. 그보다
// 뒤는 DSM 이 무시하므로 거기서 끊는다.
func portMapFor(cs []hwscan.Controller, bayOrder []string, maxBays int) (portmap, idxmap string) {
	// Only SATA controllers are counted. SataPortMap records "how many SATA
	// ports per controller", so slipping in a SAS, RAID or virtio-scsi HBA -
	// none of which have ata ports - shifts the digits and lands every later
	// controller's mapping somewhere else entirely. Disks on those are
	// enumerated by DSM through the SCSI layer instead.
	//
	// SATA 컨트롤러만 센다. SataPortMap 은 "컨트롤러마다 SATA 포트 몇 개" 를
	// 적는 값이라, ata 포트가 없는 SAS/RAID/virtio-scsi HBA 를 끼워 넣으면
	// 자릿수가 밀려 뒤 컨트롤러의 매핑이 통째로 엉뚱한 데 붙는다. 그쪽
	// 디스크는 DSM 이 SCSI 계층에서 직접 열거한다.
	sata := make([]hwscan.Controller, 0, len(cs))
	for _, c := range cs {
		if c.Kind == hwscan.KindSATA {
			sata = append(sata, c)
		}
	}
	cs = sata
	if len(cs) == 0 {
		return "", ""
	}
	if maxBays <= 0 {
		maxBays = 0xff
	}

	// The bay assignment: port key to bay number, counted from 1.
	// 베이 지정: "포트 키" → 베이 번호(1 부터).
	bay := map[string]int{}
	for i, line := range bayOrder {
		if k := strings.TrimSpace(line); k != "" {
			bay[k] = i + 1
		}
	}

	// DSM's enumeration order is the kernel's ata number order.
	// DSM 열거 순서 = 커널 ata 번호 순서.
	ordered := make([]hwscan.Controller, len(cs))
	copy(ordered, cs)
	sort.SliceStable(ordered, func(i, j int) bool {
		return minAta(ordered[i]) < minAta(ordered[j])
	})

	// Pass 1: derive the start index of every controller that has a bay
	// assignment.
	//
	// 1차: 베이 지정이 있는 컨트롤러의 시작 인덱스를 역산한다.
	start := make([]int, len(ordered))
	for i := range start {
		start[i] = -1
	}
	used := map[int]bool{}
	assigned := make([]bool, len(ordered)) // controllers with a bay assignment / 베이 지정이 있는 컨트롤러
	for i, c := range ordered {
		for _, pt := range c.Ports {
			n, ok := bay[fmt.Sprintf("%s %d", c.PCIeRoot, pt.Index)]
			if !ok || n < 1 {
				continue
			}
			s := n - 1 - int(pt.Index)
			if s < 0 {
				s = 0
			}
			start[i] = s
			assigned[i] = true
			break
		}
		if start[i] >= 0 {
			for k := 0; k < len(c.Ports); k++ {
				used[start[i]+k] = true
			}
		}
	}

	// Pass 2: an unassigned controller goes in the earliest slot still free.
	// 2차: 지정이 없는 컨트롤러는 아직 안 쓴 가장 앞자리에 놓는다.
	for i, c := range ordered {
		if start[i] >= 0 {
			continue
		}
		s := 0
		for {
			free := true
			for k := 0; k < len(c.Ports); k++ {
				if used[s+k] {
					free = false
					break
				}
			}
			if free {
				break
			}
			s++
		}
		start[i] = s
		for k := 0; k < len(c.Ports); k++ {
			used[s+k] = true
		}
	}

	// Position in SataPortMap and DiskIdxMap is fixed by controller
	// enumeration order, so a controller outside the bay range
	// (start >= maxBays) or with no ports must still occupy its digit.
	// Skipping one shifts every later controller's value forward and applies
	// it to the wrong controller. A "0" port count holds the place instead.
	// For example, with disks only on the second controller and four bays,
	// the result is "04".
	//
	// SataPortMap/DiskIdxMap 은 컨트롤러 열거 순서대로 위치가 정해진다.
	// 그래서 베이 범위 밖(start >= maxBays)이거나 포트가 없는 컨트롤러라도
	// 자릿수를 건너뛰면 안 된다. 건너뛰면 그 뒤 컨트롤러 값이 앞으로 밀려
	// 엉뚱한 컨트롤러에 적용된다. 대신 "0" 포트로 자리를 채워 정렬을 지킨다.
	// (예: 디스크가 두 번째 컨트롤러에만 있고 베이가 4 개면 "04".)
	for i, c := range ordered {
		n := len(c.Ports)
		// Only one digit is available, so 9 is the ceiling.
		// 한 자리로만 적을 수 있어 9 가 상한이다.
		if n > 9 {
			n = 9
		}
		if n <= 0 || start[i] >= maxBays {
			n = 0
		} else if left := maxBays - start[i]; n > left {
			n = left
		}
		// With disks spread across several controllers, the ports this one
		// exposes must not run into the disk index of a later assigned
		// controller; that would push its disk into the wrong bay or make it
		// disappear. So the exposed port count is capped at the next assigned
		// controller's start index. With one disk per controller each becomes
		// "1", giving "1111". With the disks all on one controller there is no
		// assigned controller after it, no cap applies, and it fills up to the
		// bay count.
		//
		// 여러 컨트롤러에 디스크가 흩어졌을 때, 이 컨트롤러가 노출하는 포트가
		// 뒤 컨트롤러(배정된)의 디스크 인덱스를 침범하면 안 된다. 침범하면
		// 그 디스크가 엉뚱한 베이로 밀리거나 사라진다. 그래서 노출 포트 수를
		// "다음 배정 컨트롤러의 시작 인덱스" 까지로 제한한다.
		// (컨트롤러마다 디스크 1개씩이면 각각 "1" 이 되어 "1111" 이 나온다.
		//  디스크가 한 컨트롤러에 모여 있으면 뒤에 배정 컨트롤러가 없어
		//  제한이 안 걸리고 베이 수까지 채운다.)
		for j := range ordered {
			if !assigned[j] || start[j] <= start[i] {
				continue
			}
			if room := start[j] - start[i]; n > room {
				n = room
			}
		}
		portmap += strconv.Itoa(n)
		idxmap += fmt.Sprintf("%02x", start[i])
	}
	// Trailing "0" controllers carry no meaning and are trimmed; a 0 in the
	// middle is needed to keep the alignment and stays. All zeros gives an
	// empty string.
	//
	// 뒤쪽의 의미 없는 "0" 컨트롤러는 잘라낸다 (중간의 0 은 정렬에 필요해
	// 남긴다). 전부 0 이면 빈 문자열이다.
	for len(portmap) > 0 && portmap[len(portmap)-1] == '0' {
		portmap = portmap[:len(portmap)-1]
		idxmap = idxmap[:len(idxmap)-2]
	}
	// A leading "0" in SataPortMap panics the kernel. It happens when the first controller has no bays, because
	// the disks are all on a later one. The first digit becomes a throwaway
	// 1-port entry; its disk index is already outside maxBays (which is why n
	// was 0), so it occupies no real bay. On a DS918+ that turns "04" into
	// "14": the first controller is one discarded port and the disk on the
	// second controller is bay 1.
	//
	// SataPortMap 첫 자리가 "0" 이면 커널 패닉이 난다.
	// 첫 컨트롤러에 베이가 없을 때(디스크가 뒤 컨트롤러에만 있을 때) 생긴다.
	// 첫 자리를 버리는 1 포트로 바꾸고, 그 디스크 인덱스는 이미 maxBays 밖이라
	// (start >= maxBays 여서 n=0 이었음) 실제 베이를 차지하지 않는 throwaway 다.
	// 예: 918 "04" -> "14" (첫 컨트롤러=버리는 1포트, 디스크는 두 번째에서 베이1).
	if len(portmap) > 0 && portmap[0] == '0' {
		portmap = "1" + portmap[1:]
	} else if portmap == "" {
		// A minimum value, so that SCSI mapping does not break when there is
		// no SATA mapping at all.
		//
		// SATA 매핑이 하나도 없을 때 SCSI 매핑이 깨지지 않게 최소값을 둔다.
		portmap, idxmap = "1", "00"
	}
	return portmap, idxmap
}

// minAta is the lowest ata number among a controller's ports.
// minAta - 컨트롤러가 가진 포트 중 가장 작은 ata 번호.
func minAta(c hwscan.Controller) int {
	best := 1 << 30
	for _, p := range c.Ports {
		if n := ataNumOf(p.Name); n < best {
			best = n
		}
	}
	return best
}

// SelectStoragePolicy picks one of the two mechanisms, preferring what the
// user set explicitly over what the platform class implies. Setting more than
// one is not merged: the kernel would quietly honour one and ignore the other,
// and the disks would come up in an order nobody asked for.
//
// SelectStoragePolicy - 사용자의 명시적 설정 → 플랫폼 클래스 순으로 두 방식
// 중 하나를 고른다. 사용자가 셋 중 하나라도 값을 넣었으면 그 선택을 존중하고
// 나머지는 버린다. 두 개를 동시에 내면 커널이 조용히 하나만 적용해서 디스크
// 순서가 예상과 달라진다.
func SelectStoragePolicy(cfg *config.Config, plat *catalog.Platform, controllers []hwscan.Controller, maxBays int) StoragePolicy {
	// If the user set any of the three, keep that one and drop the rest.
	// 사용자가 셋 중 아무거나 넣었으면 그것만 살리고 나머지는 버린다.
	if cfg.Storage.SataPortMap != "" || cfg.Storage.DiskIdxMap != "" {
		return StoragePolicy{
			Portmap: cfg.Storage.SataPortMap,
			IdxMap:  cfg.Storage.DiskIdxMap,
		}
	}
	if cfg.Storage.SataRemap != "" {
		return StoragePolicy{
			Remap:    cfg.Storage.SataRemap,
			UseRemap: true,
		}
	}

	// A DT platform already defines its slots in the device tree, so no static
	// mapping is needed. sata_remap is emitted with no value, which says
	// "remap nothing" explicitly.
	//
	// DT 플랫폼은 디바이스 트리로 슬롯이 이미 정의되므로 정적 매핑이
	// 필요없다. sata_remap 을 빈 값으로 방출해 "재매핑 안 함" 을 명시한다.
	if plat != nil && plat.DT {
		return StoragePolicy{UseRemap: true}
	}

	// Non-DT platforms (the generations in legacyPortmapPlatforms: bromolow,
	// apollolake, broadwell and so on) derive SataPortMap and DiskIdxMap from
	// the controllers and the bay count. The bay count is storage.bays first,
	// since setting it means the user narrowed it deliberately, then the
	// model's bays in the catalog, then synoinfo.maxdisks (config.MaxBays).
	//
	// 비-DT (legacyPortmapPlatforms 의 세대들: bromolow/apollolake/broadwell 등)
	// 는 SataPortMap + DiskIdxMap 을 컨트롤러와 베이 수에서 파생한다. 베이 수는
	// storage.bays 가 우선이고 (사용자가 명시적으로 좁혔다는 뜻), 없으면
	// 카탈로그의 모델 베이 수, 그다음 synoinfo.maxdisks 다 (config.MaxBays).
	if plat != nil && legacyPortmapPlatforms[plat.Name] {
		// Where the real controller layout is known, it is built from that.
		// SataPortMap is not a "total bay count" but the number of ports to use per
		// controller, one digit each, run together; DiskIdxMap says which disk number
		// each controller's first port is, as two hex digits each. That is why maxdisks
		// cannot simply go in: 16 reads as "1 port on controller 1, 6 ports on
		// controller 2", and with DiskIdxMap="00" both controllers start at the same
		// index and the disks are pushed into the wrong bays. The fallback below is
		// only used when the controllers could not be read.
		//
		// 실제 컨트롤러 구성을 알면 거기서 만든다. SataPortMap 은 "베이 총수"
		// 가 아니라 컨트롤러마다 쓸 포트 수를 한 자리씩 이어붙인 문자열이고,
		// DiskIdxMap 은 컨트롤러마다 첫 포트가 몇 번 디스크인지를 16진수 두
		// 자리씩 적은 것이다. maxdisks 를 그대로 넣으면 안 되는 이유가 여기
		// 있다. 16 은 "1번 컨트롤러 1포트 + 2번 컨트롤러 6포트" 로 읽히고,
		// DiskIdxMap="00" 이면 두 컨트롤러가 같은 인덱스에서 시작해 디스크가
		// 엉뚱한 베이로 밀린다. 아래 폴백은 컨트롤러를 못 읽었을 때만 쓴다.
		if pm, im := portMapFor(controllers, cfg.Storage.BayOrder, maxBays); pm != "" {
			return StoragePolicy{Portmap: pm, IdxMap: im}
		}
		bays := cfg.MaxBays()
		if bays <= 0 {
			bays = 1
		}
		return StoragePolicy{
			Portmap: strconv.Itoa(bays),
			IdxMap:  "00",
		}
	}

	// The safe fallback for an unknown platform. An empty sata_remap is
	// harmless.
	//
	// 알 수 없는 플랫폼의 안전한 폴백. sata_remap 빈 값은 해가 없다.
	return StoragePolicy{UseRemap: true}
}

// NVMeSystemWanted decides whether the nvmesystem flag goes on the kernel
// command line.
//
// DSM 7.2+ sells appliances that support M.2 (NVMe) slots as proper system
// volumes, but the default policy is still "there has to be at least one SATA
// or SAS disk to make a system partition". This flag is the switch that
// reverses it.
//
// The rules:
//
//   - the user set storage.nvme_system: true in loader.yaml, so true
//     unconditionally;
//   - hwscan found no SATA controller at all (opts.NVMeOnly), so true
//     automatically;
//   - otherwise false.
//
// Turning it on where hwscan did find SATA does no harm, but keeping the
// command line minimal makes it easier to read and to compare, so it is not
// emitted.
//
// NVMeSystemWanted - kernel cmdline 에 `nvmesystem` 플래그를 방출할지.
//
// DSM 7.2+ 는 M.2 (NVMe) 슬롯을 시스템 볼륨으로 정식 지원하는 어플라이언스
// 를 팔지만, 기본 정책은 여전히 "SATA/SAS 가 하나라도 있어야 시스템 파티션
// 을 만든다" 이다. 그 정책을 뒤집는 스위치가 이 플래그다.
//
// 결정 규칙:
//
//   - 사용자가 loader.yaml 에서 명시적으로 storage.nvme_system: true 로 켰다
//     → 무조건 true.
//   - hwscan 이 SATA 컨트롤러를 하나도 못 찾은 머신 (opts.NVMeOnly)
//     → 자동으로 true.
//   - 그 외 (기본) → false.
//
// hwscan 이 SATA 를 발견한 머신에서는 켜도 해는 없지만, cmdline 을 최소로
// 유지하는 편이 관찰·비교가 쉬워 굳이 방출하지 않는다.
func NVMeSystemWanted(cfg *config.Config, opts Options) bool {
	if cfg != nil && cfg.Storage.NVMeSystem {
		return true
	}
	if opts.NVMeOnly {
		return true
	}
	return false
}

// SortnetifWanted decides whether sortnetif should be turned on.
//
// With mixed vendors (Intel plus Realtek, for instance) the order the cards
// register in shifts from boot to boot, so the card mac1 actually lands on can
// change. sortnetif stabilises that order by name and bus, and with a single
// vendor there is no reason to turn it on.
//
// SortnetifWanted - sortnetif 를 켜야 하는지 판단한다.
//
// 벤더가 섞이면 (Intel + Realtek 처럼) 매 부팅마다 카드 등록 순서가
// 흔들려 mac1 이 실제로 붙는 카드가 달라질 수 있다. sortnetif 는 그
// 순서를 이름·버스 기준으로 안정화하는데, 단일 벤더면 켤 이유가 없다.
func SortnetifWanted(profile hwscan.NICProfile, nicCount int) bool {
	if len(profile.Vendors) >= 2 {
		return true
	}
	if nicCount > 1 && profile.MultiVendor {
		return true
	}
	return false
}
