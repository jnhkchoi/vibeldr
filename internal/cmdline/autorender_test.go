package cmdline

import (
	"fmt"
	"strings"
	"testing"

	"vibeldr/internal/catalog"
	"vibeldr/internal/config"
	"vibeldr/internal/hwscan"
)

// TestSelectStoragePolicyByPlatformClass checks that exactly one of the two
// mechanisms is chosen per platform class. It covers only the automatic
// decision, where the user set nothing; a user override is a separate case.
//
// TestSelectStoragePolicyByPlatformClass - 플랫폼 클래스마다 두 방식 중 정확히
// 하나의 매커니즘이 골라지는지 확인한다. 사용자가 직접 값을 넣지 않은 경우의
// 자동 결정만 다룬다 (사용자 오버라이드는 별도 케이스).
func TestSelectStoragePolicyByPlatformClass(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		wantPortmap bool // true = SataPortMap+DiskIdxMap; false = sata_remap
	}{
		// The DT platforms emit sata_remap, with an empty value.
		// DT 플랫폼들: sata_remap 을 (빈 값으로) 방출한다.
		{"epyc7002-DT", "SA6400", false},
		{"geminilake-DT", "DS920+", false},
		{"broadwellnkv2-DT", "SA3410", false},
		// The non-DT x86 generations emit SataPortMap and DiskIdxMap.
		// 비-DT x86 세대들: SataPortMap+DiskIdxMap 을 방출한다.
		{"apollolake", "DS918+", true},
		{"broadwell", "DS3617xs", true},
		{"broadwellnk", "DS3018xs", true},
		{"denverton", "DS1618+", true},
	}

	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Model = tc.model
			plat, ok := cat.PlatformForModel(tc.model)
			if !ok {
				t.Fatalf("model %s not in catalog", tc.model)
			}
			pol := SelectStoragePolicy(cfg, plat, nil, 0)
			gotPortmap := pol.Portmap != ""
			if gotPortmap != tc.wantPortmap {
				t.Fatalf("policy = %+v; wanted portmap=%v", pol, tc.wantPortmap)
			}
			// The symmetric condition too: one of the two and no more.
			// 대칭 조건도 확인한다: 둘 중 하나만.
			if tc.wantPortmap {
				if pol.UseRemap {
					t.Errorf("portmap 을 골랐는데 UseRemap 도 true 로 남았다: %+v", pol)
				}
				if pol.IdxMap == "" {
					t.Errorf("portmap 을 골랐으면 IdxMap 이 함께 채워져야 한다: %+v", pol)
				}
			} else {
				if !pol.UseRemap {
					t.Errorf("remap 을 골랐어야 하는데 UseRemap=false: %+v", pol)
				}
				if pol.Portmap != "" {
					t.Errorf("remap 을 골랐는데 Portmap 이 남아있다: %+v", pol)
				}
			}
		})
	}
}

// TestSelectStoragePolicyUserOverrideWins: stating any one of the three leaves
// that one alive and discards the rest, which rules out two surviving together
// and fighting each other.
//
// TestSelectStoragePolicyUserOverrideWins - 사용자가 셋 중 하나라도
// 명시하면 그것만 살고 나머지는 버려진다 (동시에 두 개가 남아 서로 다투는
// 상황을 원천 차단한다).
func TestSelectStoragePolicyUserOverrideWins(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	plat, _ := cat.PlatformForModel("DS918+") // apollolake, non-DT / apollolake, 비-DT

	t.Run("사용자가 sata_remap 만 넣음", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS918+"
		cfg.Storage.SataRemap = "0>4:1>5"
		pol := SelectStoragePolicy(cfg, plat, nil, 0)
		if pol.Portmap != "" || pol.IdxMap != "" {
			t.Errorf("사용자가 remap 만 넣었는데 portmap 이 방출되려 함: %+v", pol)
		}
		if !pol.UseRemap || pol.Remap != "0>4:1>5" {
			t.Errorf("사용자의 remap 값이 반영되지 않음: %+v", pol)
		}
	})

	t.Run("사용자가 SataPortMap 만 넣음", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS918+"
		cfg.Storage.SataPortMap = "4"
		cfg.Storage.DiskIdxMap = "00"
		pol := SelectStoragePolicy(cfg, plat, nil, 0)
		if pol.UseRemap {
			t.Errorf("사용자가 portmap 만 넣었는데 UseRemap 이 남았다: %+v", pol)
		}
		if pol.Portmap != "4" || pol.IdxMap != "00" {
			t.Errorf("사용자 portmap 값이 반영되지 않음: %+v", pol)
		}
	})
}

// TestSelectStoragePolicyDerivesBays: where the bay count is drawn from when it
// is derived automatically.
//
// The order of preference is storage.bays, then the model's real maxdisks, then
// synoinfo.maxdisks. The middle one is the point: DS918+ is a 4-bay model, so
// even with 12 written in synoinfo it must not be read as 12. A fixed value in
// one place throws the bay count out completely on a model with a different one.
//
// TestSelectStoragePolicyDerivesBays - 자동 파생 때 베이 수를 어디서 끌어오는지.
//
// 우선순위는 storage.bays -> 모델의 실제 maxdisks -> synoinfo.maxdisks 다.
// 가운데가 핵심이다. DS918+ 는 4 베이 모델이라, synoinfo 에 12 가 적혀
// 있어도 12 로 읽으면 안 된다. 한 자리에서 고정값을 쓰면 베이 수가 다른
// 모델에서 통째로 어긋난다.
func TestSelectStoragePolicyDerivesBays(t *testing.T) {
	cat, _ := catalog.Load()
	plat, _ := cat.PlatformForModel("DS918+")

	t.Run("storage.bays 가 가장 세다", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS918+"
		cfg.Storage.Bays = 8
		if pol := SelectStoragePolicy(cfg, plat, nil, 0); pol.Portmap != "8" {
			t.Errorf("Bays=8 → Portmap 이 \"8\" 이어야 하는데 %q", pol.Portmap)
		}
	})

	t.Run("모델이 synoinfo 를 이긴다", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS918+"
		cfg.Synoinfo["maxdisks"] = "12"
		if pol := SelectStoragePolicy(cfg, plat, nil, 0); pol.Portmap != "4" {
			t.Errorf("DS918+ 는 4 베이인데 Portmap 이 %q", pol.Portmap)
		}
	})

	t.Run("모르는 모델이면 synoinfo 로 내려간다", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS3617xs" // not in the catalog bay table / 카탈로그 베이 표에 없는 모델
		cfg.Synoinfo["maxdisks"] = "12"
		if pol := SelectStoragePolicy(cfg, plat, nil, 0); pol.Portmap != "12" {
			t.Errorf("maxdisks=12 → Portmap 이 \"12\" 이어야 하는데 %q", pol.Portmap)
		}
	})
}

// TestBuildEmitsExactlyOneStorageMechanism: whether only one of the two shows
// up in Build's output too. Two appearing at once risks them cancelling each
// other out.
//
// TestBuildEmitsExactlyOneStorageMechanism - Build 출력에도 둘 중 하나만
// 나타나는지 본다 (두 개가 동시에 나오면 서로 무효화될 위험이 있다).
func TestBuildEmitsExactlyOneStorageMechanism(t *testing.T) {
	cases := []struct {
		name  string
		model string
		dsm   string
	}{
		{"DT-epyc7002", "SA6400", "7.4.1-90080"},
		{"DT-geminilake", "DS920+", "7.4.1-90080"},
		{"nonDT-apollolake", "DS918+", "7.4.1-90080"},
		{"nonDT-broadwellnk", "DS3018xs", "7.4.1-90080"},
	}
	cat, _ := catalog.Load()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Model = tc.model
			cfg.DSM.Version = tc.dsm
			plat, ok := cat.PlatformForModel(tc.model)
			if !ok {
				t.Fatalf("model %s not in catalog", tc.model)
			}
			id := Identity{Serial: "1234567890123", MACs: []string{"001132a1b2c3"}}
			b, err := Build(cfg, plat, id, DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			hasPortmap := b.Has("SataPortMap")
			hasIdx := b.Has("DiskIdxMap")
			hasRemap := b.Has("sata_remap")

			// SataPortMap and DiskIdxMap are one set and have to be handled together.
			// SataPortMap 과 DiskIdxMap 은 한 세트이므로 함께 다뤄야 한다.
			if hasPortmap != hasIdx {
				t.Errorf("SataPortMap 과 DiskIdxMap 은 항상 함께 나와야 한다: portmap=%v idx=%v", hasPortmap, hasIdx)
			}
			mechCount := 0
			if hasPortmap {
				mechCount++
			}
			if hasRemap {
				mechCount++
			}
			if mechCount != 1 {
				t.Errorf("정확히 하나의 매커니즘만 방출되어야 하는데 %d 개: %s", mechCount, b.String())
			}
		})
	}
}

// TestSortnetifWantedTable covers the conditions that turn sortnetif on.
// TestSortnetifWantedTable - sortnetif 를 켜는 조건.
func TestSortnetifWantedTable(t *testing.T) {
	cases := []struct {
		name     string
		profile  hwscan.NICProfile
		nicCount int
		want     bool
	}{
		{"단일 벤더 단일 NIC", hwscan.NICProfile{Vendors: []string{"0x8086"}, SinglePCIFamily: true}, 1, false},
		{"단일 벤더 다수 NIC", hwscan.NICProfile{Vendors: []string{"0x8086"}, SinglePCIFamily: true}, 4, false},
		{"두 벤더 단일 NIC 선언", hwscan.NICProfile{Vendors: []string{"0x10ec", "0x8086"}, MultiVendor: true}, 1, true},
		{"두 벤더 다수 NIC", hwscan.NICProfile{Vendors: []string{"0x10ec", "0x8086"}, MultiVendor: true}, 3, true},
		{"프로파일 비어있음", hwscan.NICProfile{SinglePCIFamily: true}, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SortnetifWanted(tc.profile, tc.nicCount); got != tc.want {
				t.Errorf("SortnetifWanted(%+v, %d) = %v, want %v", tc.profile, tc.nicCount, got, tc.want)
			}
		})
	}
}

// TestNVMeSystemWantedTable is the table of conditions deciding whether to emit
// the nvmesystem flag. Either source being true - stated by the user, or
// detected by hwscan - has to turn it on.
//
// TestNVMeSystemWantedTable - nvmesystem 플래그를 방출할지 판단하는
// 조건표. 두 소스 (사용자 명시 / hwscan 자동감지) 중 하나라도 참이면 켜야
// 한다.
func TestNVMeSystemWantedTable(t *testing.T) {
	cases := []struct {
		name     string
		cfgFlag  bool
		nvmeOnly bool
		want     bool
	}{
		{"기본값 (둘 다 off)", false, false, false},
		{"사용자가 명시적으로 켬", true, false, true},
		{"hwscan 이 NVMe-only 감지", false, true, true},
		{"둘 다 켜짐", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Storage.NVMeSystem = tc.cfgFlag
			opts := DefaultOptions()
			opts.NVMeOnly = tc.nvmeOnly
			if got := NVMeSystemWanted(cfg, opts); got != tc.want {
				t.Errorf("NVMeSystemWanted(cfgFlag=%v, NVMeOnly=%v) = %v, want %v",
					tc.cfgFlag, tc.nvmeOnly, got, tc.want)
			}
		})
	}
}

// TestNVMeSystemWantedNilCfgSafe: passing a nil config must not panic and must
// decide from opts alone. autorender can be called on the path where the config
// failed to load, so it is defensive.
//
// TestNVMeSystemWantedNilCfgSafe - nil config 를 넘겨도 panic 없이 opts 만으로
// 판단해야 한다. autorender 가 config 로드 실패 경로에서도 호출될 수 있어
// 방어적으로 처리한다.
func TestNVMeSystemWantedNilCfgSafe(t *testing.T) {
	if NVMeSystemWanted(nil, DefaultOptions()) {
		t.Error("nil cfg + 기본 opts 는 false 여야 함")
	}
	opts := DefaultOptions()
	opts.NVMeOnly = true
	if !NVMeSystemWanted(nil, opts) {
		t.Error("nil cfg 라도 NVMeOnly=true 면 true 여야 함")
	}
}

// TestBuildEmitsNVMeSystemFlag: whether nvmesystem appears in or disappears
// from Build's result as the conditions say.
//
// TestBuildEmitsNVMeSystemFlag - Build 결과에 nvmesystem 이 조건대로
// 나타나거나 사라지는지 본다.
func TestBuildEmitsNVMeSystemFlag(t *testing.T) {
	cat, _ := catalog.Load()
	plat, _ := cat.PlatformForModel("SA6400")
	id := Identity{Serial: "2350W8RA1234B", MACs: []string{"001132a1b2c3"}}

	// The default, both off: it must not be emitted.
	// 기본 (둘 다 off) - 방출되지 않아야 한다.
	cfg := config.Default()
	cfg.Model = "SA6400"
	cfg.DSM.Version = "7.4.1-90080"
	b, err := Build(cfg, plat, id, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if b.Has("nvmesystem") {
		t.Errorf("기본 상태에서는 nvmesystem 이 방출되면 안 된다: %s", b.String())
	}

	// Turned on by the user: emitted.
	// 사용자가 켬 - 방출된다.
	cfg.Storage.NVMeSystem = true
	b, err = Build(cfg, plat, id, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !b.Has("nvmesystem") {
		t.Errorf("storage.nvme_system=true 인데 flag 가 없다: %s", b.String())
	}
	if v, _ := b.Get("nvmesystem"); v != "" {
		t.Errorf("nvmesystem 은 값 없는 flag 로 방출되어야 한다: got %q", v)
	}
	// It appears in the rendered result too, as a cross-check. What the kernel
	// actually tests for is `nvmesystem` itself, and `nvmesystem=1` also passes.
	// Keeping it as a Flag matches our Builder's convention for valueless
	// parameters.
	//
	// 확인차 렌더 결과에도 등장한다. 커널이 실제로 검사할 형태는 `nvmesystem`
	// 그 자체이며, `nvmesystem=1` 도 통용된다. Flag 로 두는 편이 우리
	// Builder 의 값 없는 파라미터 관용과 맞다.
	if !strings.Contains(b.String(), "nvmesystem") {
		t.Errorf("nvmesystem 이 렌더 결과에 없다: %s", b.String())
	}

	// hwscan detects NVMe-only: emitted even with cfg unset.
	// hwscan 이 NVMe-only 를 감지한 경우 - cfg 미설정이라도 방출된다.
	cfg.Storage.NVMeSystem = false
	opts := DefaultOptions()
	opts.NVMeOnly = true
	b, err = Build(cfg, plat, id, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Has("nvmesystem") {
		t.Errorf("NVMeOnly=true 인데 flag 가 없다: %s", b.String())
	}
}

// TestBuildFlipsSortnetifOnMixedVendors: given a profile with mixed vendors,
// sortnetif has to appear on the command line Build produces.
//
// TestBuildFlipsSortnetifOnMixedVendors - 벤더가 섞인 프로파일을 받으면
// Build 결과 커맨드라인에 sortnetif 가 나타나야 한다.
func TestBuildFlipsSortnetifOnMixedVendors(t *testing.T) {
	cat, _ := catalog.Load()
	plat, _ := cat.PlatformForModel("SA6400")
	cfg := config.Default()
	cfg.Model = "SA6400"
	cfg.DSM.Version = "7.4.1-90080"
	id := Identity{
		Serial: "2350W8RA1234B",
		MACs:   []string{"001132a1b2c3", "001132a1b2c4"},
	}

	// A single vendor means no sortnetif.
	// 단일 벤더 -> sortnetif 가 없어야 한다.
	opts := DefaultOptions()
	opts.NICProfile = hwscan.NICProfile{Vendors: []string{"0x8086"}, SinglePCIFamily: true}
	b, err := Build(cfg, plat, id, opts)
	if err != nil {
		t.Fatal(err)
	}
	if b.Has("sortnetif") {
		t.Errorf("단일 벤더에서는 sortnetif 가 나오면 안 된다: %s", b.String())
	}

	// Mixed vendors mean sortnetif has to be on.
	// 벤더 섞임 -> sortnetif 가 켜져야 한다.
	opts.NICProfile = hwscan.NICProfile{
		Vendors:     []string{"0x10ec", "0x8086"},
		MultiVendor: true,
	}
	b, err = Build(cfg, plat, id, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Has("sortnetif") {
		t.Errorf("벤더 섞인 프로파일에서는 sortnetif 가 켜져야 한다: %s", b.String())
	}
	// It is a flag, so the value has to be empty.
	// 플래그이므로 값은 비어 있어야 한다.
	if v, _ := b.Get("sortnetif"); v != "" {
		t.Errorf("sortnetif 는 값 없는 플래그여야 한다: got %q", v)
	}
	// A cross-check: it still shows up in String too.
	// 확인차: 여전히 String 에도 등장한다.
	if !strings.Contains(b.String(), "sortnetif") {
		t.Errorf("sortnetif 가 렌더 결과에 없다: %s", b.String())
	}
}

// TestPortMapFromControllers: whether SataPortMap and DiskIdxMap come out right
// from a detected controller layout.
//
// It exercises the two places this goes wrong on real hardware:
//  1. putting maxdisks (16) in as it is reads as "1 port plus 6 ports". The
//     value is not a total bay count but a list of ports per controller.
//  2. a controller order differing from DSM's enumeration order (the ata
//     numbers) pushes a disk plugged into ata7 out to bay 7.
//
// TestPortMapFromControllers - 감지된 컨트롤러 구성에서 SataPortMap 과
// DiskIdxMap 이 제대로 나오는지 본다.
//
// 실기에서 어긋나기 쉬운 두 지점을 확인한다:
//  1. maxdisks (16) 를 그대로 넣으면 "1 포트 + 6 포트" 로 읽힌다. 이 값은
//     베이 총수가 아니라 컨트롤러별 포트 수 목록이다.
//  2. 컨트롤러 순서가 DSM 의 열거 순서 (ata 번호) 와 다르면 ata7 에 꽂은
//     디스크가 7 번 베이로 밀려난다.
func TestPortMapFromControllers(t *testing.T) {
	ports := func(first int, n int) []hwscan.Port {
		out := make([]hwscan.Port, n)
		for i := range out {
			out[i] = hwscan.Port{Name: fmt.Sprintf("ata%d", first+i), Index: uint32(i)}
		}
		return out
	}
	// The real hardware layout: 1f.2 is ata1 to 6, 07.0 is ata7 to 12.
	// 실기 구성: 1f.2 가 ata1~6, 07.0 이 ata7~12.
	late := hwscan.Controller{PCIeRoot: "00:07.0", Ports: ports(7, 6)}
	early := hwscan.Controller{PCIeRoot: "00:1f.2", Ports: ports(1, 6)}

	t.Run("ata 번호 순으로 정렬한다", func(t *testing.T) {
		// The input order is reversed on purpose.
		// 입력 순서를 일부러 뒤집어 준다.
		pm, idx := portMapFor([]hwscan.Controller{late, early}, nil, 16)
		if pm != "66" {
			t.Errorf("SataPortMap = %q, want 66", pm)
		}
		// After sorting, 1f.2 (ata1 onwards) comes first and starts at 0, so 07.0
		// starts at 6.
		//
		// 정렬 후 첫째가 1f.2(ata1~) 라 0 부터, 07.0 이 6 부터 시작한다.
		if idx != "0006" {
			t.Errorf("DiskIdxMap = %q, want 0006", idx)
		}
	})

	t.Run("베이 지정을 반영한다", func(t *testing.T) {
		// ata7, port 0 of 07.0, is assigned to bay 1.
		// ata7 (07.0 의 0 번 포트) 을 1 번 베이로 지정한다.
		plan := []string{"00:07.0 0"}
		pm, idx := portMapFor([]hwscan.Controller{late, early}, plan, 16)
		if pm != "66" {
			t.Errorf("SataPortMap = %q, want 66", pm)
		}
		// 07.0 has to start at 0 for ata7 to be bay 1. The sort order puts 1f.2 first,
		// so DiskIdxMap reads "1f.2's slot, then 07.0's".
		//
		// 07.0 이 0 부터 시작해야 ata7 이 1 번 베이가 된다. 정렬 순서는
		// 1f.2 가 먼저이므로 DiskIdxMap 은 "1f.2 자리, 07.0 자리" 순이다.
		if idx != "0600" {
			t.Errorf("DiskIdxMap = %q, want 0600 (07.0 이 0 부터)", idx)
		}
	})

	t.Run("maxdisks 에서 끊는다", func(t *testing.T) {
		pm, _ := portMapFor([]hwscan.Controller{early, late}, nil, 8)
		if pm != "62" {
			t.Errorf("SataPortMap = %q, want 62", pm)
		}
	})

	t.Run("포트 없는 컨트롤러는 건너뛴다", func(t *testing.T) {
		empty := hwscan.Controller{PCIeRoot: "00:02.0"}
		pm, idx := portMapFor([]hwscan.Controller{empty, early}, nil, 16)
		if pm != "6" || idx != "00" {
			t.Errorf("= (%q,%q), want (\"6\",\"00\")", pm, idx)
		}
	})
}
