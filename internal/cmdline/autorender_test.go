package cmdline

import (
	"fmt"
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
		wantPortmap bool // true = SataPortMap+DiskIdxMap; false = nothing / 아무것도 없음
	}{
		// SA6400's device tree holds the slots, so nothing is emitted.
		// SA6400 은 장치 트리가 슬롯을 들고 있어 아무것도 내지 않는다.
		{"epyc7002-DT", "SA6400", false},
		// The two non-DT models emit SataPortMap and DiskIdxMap.
		// 비-DT 두 모델은 SataPortMap+DiskIdxMap 을 낸다.
		{"apollolake", "DS918+", true},
		{"broadwellnk", "DS3622xs+", true},
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
					t.Errorf("picked portmap but UseRemap is still true: %+v", pol)
				}
				if pol.IdxMap == "" {
					t.Errorf("picked portmap, so IdxMap must be filled too: %+v", pol)
				}
			} else if pol != (StoragePolicy{}) {
				t.Errorf("a device-tree model emits no mapping, got %+v", pol)
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

	t.Run("user gave only sata_remap", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS918+"
		cfg.Storage.SataRemap = "0>4:1>5"
		pol := SelectStoragePolicy(cfg, plat, nil, 0)
		if pol.Portmap != "" || pol.IdxMap != "" {
			t.Errorf("user gave only remap but portmap is about to be emitted: %+v", pol)
		}
		if !pol.UseRemap || pol.Remap != "0>4:1>5" {
			t.Errorf("user's remap value not applied: %+v", pol)
		}
	})

	t.Run("user gave only SataPortMap", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS918+"
		cfg.Storage.SataPortMap = "4"
		cfg.Storage.DiskIdxMap = "00"
		pol := SelectStoragePolicy(cfg, plat, nil, 0)
		if pol.UseRemap {
			t.Errorf("user gave only portmap but UseRemap is left over: %+v", pol)
		}
		if pol.Portmap != "4" || pol.IdxMap != "00" {
			t.Errorf("user's portmap value not applied: %+v", pol)
		}
	})
}

// TestSelectStoragePolicyDerivesBays: where the bay count is drawn from when it
// is derived automatically.
//
// The order of preference is storage.bays, then the model's real maxdisks, and
// synoinfo.maxdisks never counts: DS918+ is a 4-bay model, so even with 12
// written in synoinfo it must not be read as 12. A fixed value in
// one place throws the bay count out completely on a model with a different one.
//
// TestSelectStoragePolicyDerivesBays - 자동 파생 때 베이 수를 어디서 끌어오는지.
//
// 우선순위는 storage.bays -> 모델의 실제 maxdisks 이고, synoinfo.maxdisks 는
// 세지 않는다. DS918+ 는 4 베이 모델이라, synoinfo 에 12 가 적혀 있어도 12 로
// 읽으면 안 된다. 한 자리에서 고정값을 쓰면 베이 수가 다른
// 모델에서 통째로 어긋난다.
func TestSelectStoragePolicyDerivesBays(t *testing.T) {
	cat, _ := catalog.Load()
	plat, _ := cat.PlatformForModel("DS918+")

	t.Run("storage.bays wins", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS918+"
		cfg.Storage.Bays = 8
		if pol := SelectStoragePolicy(cfg, plat, nil, 0); pol.Portmap != "8" {
			t.Errorf("Bays=8 → Portmap should be \"8\" but is %q", pol.Portmap)
		}
	})

	t.Run("model beats synoinfo", func(t *testing.T) {
		cfg := config.Default()
		cfg.Model = "DS918+"
		cfg.Synoinfo["maxdisks"] = "12"
		if pol := SelectStoragePolicy(cfg, plat, nil, 0); pol.Portmap != "4" {
			t.Errorf("DS918+ has 4 bays but Portmap is %q", pol.Portmap)
		}
	})

}

// TestBuildEmitsExactlyOneStorageMechanism: Build's output carries the
// SataPortMap/DiskIdxMap pair on the non-DT models and no mapping at all on
// SA6400. Two at once would risk cancelling each other out.
//
// TestBuildEmitsExactlyOneStorageMechanism - Build 출력에 비-DT 모델은
// SataPortMap/DiskIdxMap 쌍이, SA6400 은 아무 매핑도 없는지 본다 (두 개가
// 동시에 나오면 서로 무효화될 위험이 있다).
func TestBuildEmitsExactlyOneStorageMechanism(t *testing.T) {
	cases := []struct {
		name  string
		model string
		dsm   string
		want  int // mechanisms expected / 기대하는 방식 수
	}{
		{"DT-epyc7002", "SA6400", "7.4.1-90080", 0},
		{"nonDT-apollolake", "DS918+", "7.4.1-90080", 1},
		{"nonDT-broadwellnk", "DS3622xs+", "7.4.1-90080", 1},
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
				t.Errorf("SataPortMap and DiskIdxMap must always come together: portmap=%v idx=%v", hasPortmap, hasIdx)
			}
			mechCount := 0
			if hasPortmap {
				mechCount++
			}
			if hasRemap {
				mechCount++
			}
			if mechCount != tc.want {
				t.Errorf("%d mechanism(s) should be emitted, got %d: %s", tc.want, mechCount, b.String())
			}
		})
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
	// Two controllers: 1f.2 is ata1 to 6, 07.0 is ata7 to 12.
	// 두 컨트롤러: 1f.2 가 ata1~6, 07.0 이 ata7~12.
	late := hwscan.Controller{PCIeRoot: "00:07.0", Ports: ports(7, 6)}
	early := hwscan.Controller{PCIeRoot: "00:1f.2", Ports: ports(1, 6)}

	t.Run("sorts by ata number", func(t *testing.T) {
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

	t.Run("honours bay assignments", func(t *testing.T) {
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
			t.Errorf("DiskIdxMap = %q, want 0600 (07.0 starts at 0)", idx)
		}
	})

	t.Run("stops at maxdisks", func(t *testing.T) {
		pm, _ := portMapFor([]hwscan.Controller{early, late}, nil, 8)
		if pm != "62" {
			t.Errorf("SataPortMap = %q, want 62", pm)
		}
	})

	t.Run("skips controllers without ports", func(t *testing.T) {
		empty := hwscan.Controller{PCIeRoot: "00:02.0"}
		pm, idx := portMapFor([]hwscan.Controller{empty, early}, nil, 16)
		if pm != "6" || idx != "00" {
			t.Errorf("= (%q,%q), want (\"6\",\"00\")", pm, idx)
		}
	})
}
