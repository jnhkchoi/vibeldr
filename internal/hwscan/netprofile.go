package hwscan

import (
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// NICProfile is this machine's network card vendor profile.
//
// DSM is very sensitive to the order NICs are enumerated in during boot. When
// vendors are mixed - Intel plus Realtek, say - the order the kernel registers
// the cards in shifts from boot to boot, so the card mac1 lands on changes, and
// MAC-based licensing or a DHCP reservation stops matching. sortnetif
// stabilises that order, but there is no reason to turn it on when there is
// only one vendor, which is why the hardware scan establishes the vendor mix
// first.
//
// NICProfile - 이 머신의 네트워크 카드 벤더 프로파일.
//
// DSM 은 부팅 도중 NIC 열거 순서에 매우 예민하다. 벤더가 섞여 있으면
// (예: Intel + Realtek) 커널이 카드를 등록하는 순서가 부팅마다 흔들려
// mac1 이 붙는 카드가 달라지고, 그러면 MAC 기반 라이선스나 DHCP 예약이
// 어긋난다. sortnetif 는 그 순서를 안정화하지만 단일 벤더만 있으면 켤 이유가
// 없다. 그래서 하드웨어 스캔으로 벤더 구성을 먼저 파악한다.
type NICProfile struct {
	// Vendors are the PCI vendor IDs found, in 0xNNNN form, sorted and deduped.
	// Vendors - 발견된 PCI 벤더 ID (0xNNNN 형식, 정렬됨, 중복 제거).
	Vendors []string
	// SinglePCIFamily is true when there are fewer than two vendor IDs: one, or
	// none found at all.
	// SinglePCIFamily - 벤더 ID 가 둘 미만인지 (하나이거나 하나도 못 찾음).
	// 2개 이상이면 false.
	SinglePCIFamily bool
	// MultiVendor is true when there are two or more vendor IDs. It is the
	// opposite of SinglePCIFamily, kept as its own field so that the intent
	// reads clearly at the call site.
	//
	// MultiVendor - 벤더 ID 가 2개 이상인지. SinglePCIFamily 의 반대말이지만
	// 호출 지점에서 의도를 뚜렷하게 읽히게 하려고 별도 필드로 둔다.
	MultiVendor bool
}

// DetectNICProfile builds the profile by reading /sys/class/net/*/device/vendor
// under the given fs. It takes an fs.FS, so a test can replace it entirely with
// an fstest.MapFS.
//
// DetectNICProfile - 주어진 fs 아래 /sys/class/net/*/device/vendor 를 읽어
// 프로파일을 구성한다. fs.FS 를 받으므로 테스트에서 fstest.MapFS 로 완전히
// 대체 가능하다.
func DetectNICProfile(sysfs fs.FS) NICProfile {
	// /sys/class/net holds lo, eth0, eth1 and so on. lo has no device symlink,
	// so it filters itself out.
	//
	// /sys/class/net 에는 lo, eth0, eth1, ... 이 있는데 lo 는 device 심볼릭
	// 링크가 없어서 자연스럽게 걸러진다.
	entries, err := fs.ReadDir(sysfs, "sys/class/net")
	if err != nil {
		// Not mounted, or inside a container. Either way the profile is empty.
		// 마운트 안 되었거나 컨테이너 안이거나 — 어느 쪽이든 프로파일은 빈 값.
		return NICProfile{SinglePCIFamily: true}
	}

	seen := make(map[string]struct{})
	for _, e := range entries {
		if e.Name() == "lo" {
			continue
		}
		b, err := fs.ReadFile(sysfs, path.Join("sys/class/net", e.Name(), "device", "vendor"))
		if err != nil {
			// Virtual interfaces (bridge, veth, tun) have no device/ - skip.
			// 가상 인터페이스 (bridge, veth, tun 등) 는 device/ 가 없다. 스킵.
			continue
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			continue
		}
		seen[strings.ToLower(v)] = struct{}{}
	}

	vendors := make([]string, 0, len(seen))
	for v := range seen {
		vendors = append(vendors, v)
	}
	sort.Strings(vendors)

	return NICProfile{
		Vendors:         vendors,
		SinglePCIFamily: len(vendors) < 2,
		MultiVendor:     len(vendors) >= 2,
	}
}

// DetectNICProfileSysfs is the convenience wrapper for the running machine's
// real sysfs.
//
// It uses os.DirFS("/") deliberately. Rooting at /sys would make the relative
// paths "class/net/...", which only works against a real sysfs mount and makes
// the fixture and production paths diverge. Rooting at "/" and using
// "sys/class/net/..." means the same logic runs against any fs.FS.
//
// DetectNICProfileSysfs - 도는 머신의 실제 sysfs 를 대상으로 하는 편의 래퍼.
//
// os.DirFS("/") 를 쓰는 이유: /sys 를 루트로 잡으면 상대 경로가
// "class/net/..." 이 되어 실제 sysfs 마운트에서만 도는 코드가 되고,
// 픽스처와 프로덕션 경로가 엇갈리게 된다. 항상 "/" 를 루트로 두고
// "sys/class/net/..." 경로를 쓰면 fs.FS 어떤 것으로도 동일 로직이 돈다.
func DetectNICProfileSysfs() NICProfile {
	return DetectNICProfile(os.DirFS("/"))
}
