package cmdline

import (
	"strings"
	"testing"

	"vibeldr/internal/catalog"
	"vibeldr/internal/config"
)

func TestBuilderPreservesInsertionOrder(t *testing.T) {
	b := New()
	b.Set("z", "1").Set("a", "2").Flag("m").Set("b", "3")

	want := "z=1 a=2 m b=3"
	if got := b.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestBuilderSetReplacesInPlace(t *testing.T) {
	b := New()
	b.Set("a", "1").Set("b", "2").Set("a", "9")

	if got := b.String(); got != "a=9 b=2" {
		t.Errorf("String() = %q, want %q", got, "a=9 b=2")
	}
}

func TestAppendCSVDeduplicates(t *testing.T) {
	b := New()
	b.AppendCSV("modprobe.blacklist", "evbug")
	b.AppendCSV("modprobe.blacklist", "cdc_ether")
	b.AppendCSV("modprobe.blacklist", "evbug") // already present / 이미 있는 값

	got, _ := b.Get("modprobe.blacklist")
	if got != "evbug,cdc_ether" {
		t.Errorf("blacklist = %q, want %q", got, "evbug,cdc_ether")
	}
}

func TestValidateRequiresIdentityParameters(t *testing.T) {
	b := New()
	problems := b.Validate()
	if len(problems) == 0 {
		t.Fatal("an empty command line should report missing required parameters")
	}
	missing := map[string]bool{}
	for _, p := range problems {
		missing[p.Key] = true
	}
	for _, key := range []string{"syno_hw_version", "sn", "netif_num", "vid", "pid"} {
		if !missing[key] {
			t.Errorf("missing required parameter %q was not reported", key)
		}
	}
}

func TestValidateCatchesNICCountMismatch(t *testing.T) {
	b := New()
	b.Set("syno_hw_version", "SA6400")
	b.Set("sn", "2350W8RA1234B")
	b.Set("vid", "0x46f4")
	b.Set("pid", "0x0001")
	b.Set("mac1", "001132a1b2c3")
	b.Set("netif_num", "2") // says two, only one mac present / 둘이라 했지만 mac 은 하나뿐

	var found bool
	for _, p := range b.Validate() {
		if p.Key == "netif_num" && strings.Contains(p.Message, "mac parameter") {
			found = true
		}
	}
	if !found {
		t.Error("a netif_num that disagrees with the mac count should be reported")
	}
}

func TestValidateCatchesWhitespaceInValue(t *testing.T) {
	b := New()
	b.Set("extra", "two words")

	var found bool
	for _, p := range b.Validate() {
		if p.Key == "extra" && strings.Contains(p.Message, "whitespace") {
			found = true
		}
	}
	if !found {
		t.Error("a value containing a space should be reported")
	}
}

// fixture is a valid config/platform pair for Build tests.
// fixture - Build 테스트용으로 올바른 config/플랫폼 짝을 만든다.
func fixture(t *testing.T, model, dsmVersion string) (*config.Config, *catalog.Platform, *catalog.Catalog) {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Model = model
	cfg.DSM.Version = dsmVersion
	plat, ok := cat.PlatformForModel(model)
	if !ok {
		t.Fatalf("model %s not in catalog", model)
	}
	return cfg, plat, cat
}

func TestBuildIsDeterministic(t *testing.T) {
	cfg, plat, _ := fixture(t, "SA6400", "7.4.1-90080")
	id := Identity{Serial: "2350W8RA1234B", MACs: []string{"001132a1b2c3"}}

	first, err := Build(cfg, plat, id, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	// Run it many times: a map-iteration bug shows up as an occasional
	// difference, not a consistent one.
	//
	// 여러 번 돌린다. 맵 순회 버그는 매번이 아니라 가끔 나는 차이로 드러난다.
	for i := 0; i < 20; i++ {
		again, err := Build(cfg, plat, id, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if first.String() != again.String() {
			t.Fatalf("Build is not deterministic:\n  %s\n  %s", first.String(), again.String())
		}
	}
}

func TestBuildKernel5Platform(t *testing.T) {
	cfg, plat, _ := fixture(t, "SA6400", "7.4.1-90080") // epyc7002, kernel 5.10.55, DT / epyc7002, 커널 5.10.55, DT
	id := Identity{Serial: "2350W8RA1234B", MACs: []string{"001132a1b2c3"}}

	b, err := Build(cfg, plat, id, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}

	if v, _ := b.Get("split_lock_detect"); v != "off" {
		t.Error("kernel 5 needs split_lock_detect=off")
	}
	if b.Has("elevator") {
		t.Error("elevator is a kernel 4 parameter and must not appear on kernel 5")
	}
	if b.Has("synoboot_satadom") {
		t.Error("synoboot_satadom is a kernel 4 parameter")
	}
	// Device-tree platform: serial ports declared, no legacy hdd parameters.
	// device tree 플랫폼: 시리얼 포트는 선언하고 옛 hdd 파라미터는 없다.
	if !b.Has("syno_ttyS0") {
		t.Error("device-tree platform should declare syno_ttyS0")
	}
	if b.Has("SMBusHddDynamicPower") {
		t.Error("SMBusHddDynamicPower is for non device-tree platforms only")
	}
	// epyc7002 has a working mpt3sas, so it must not be blacklisted.
	// epyc7002 는 mpt3sas 가 제대로 돌므로 blacklist 하면 안 된다.
	bl, _ := b.Get("modprobe.blacklist")
	if strings.Contains(bl, "mpt3sas") {
		t.Errorf("epyc7002 should not blacklist mpt3sas, got %q", bl)
	}

	if problems := b.Validate(); len(problems) != 0 {
		t.Errorf("a complete command line should validate cleanly, got %+v", problems)
	}
}

func TestBuildKernel4NonDTPlatform(t *testing.T) {
	cfg, plat, _ := fixture(t, "DS918+", "7.4.1-90080") // apollolake, kernel 4.4.302, non-DT / apollolake, 커널 4.4.302, 비-DT
	id := Identity{Serial: "1910PDN123456", MACs: []string{"001132a1b2c3"}}

	b, err := Build(cfg, plat, id, Options{EFI: true, LoaderBus: "sata", LoaderSizeMB: 1024})
	if err != nil {
		t.Fatal(err)
	}

	if v, _ := b.Get("elevator"); v != "elevator" {
		t.Error("kernel 4 needs the elevator parameter")
	}
	if v, _ := b.Get("dom_szmax"); v != "1024" {
		t.Errorf("dom_szmax = %q, want 1024", v)
	}
	if !b.Has("SMBusHddDynamicPower") {
		t.Error("non device-tree platform needs SMBusHddDynamicPower")
	}
	if !b.Has("nox2apic") {
		t.Error("apollolake masks off x2apic")
	}
	if v, _ := b.Get("intel_iommu"); v != "igfx_off" {
		t.Error("apollolake needs intel_iommu=igfx_off for its iGPU")
	}
	if !b.Has("withefi") || b.Has("noefi") {
		t.Error("EFI machine should get withefi and not noefi")
	}
}

func TestBuildUSBLoaderSkipsSATADOM(t *testing.T) {
	cfg, plat, _ := fixture(t, "DS918+", "7.4.1-90080")
	id := Identity{Serial: "1910PDN123456", MACs: []string{"001132a1b2c3"}}

	b, err := Build(cfg, plat, id, Options{LoaderBus: "usb"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Has("synoboot_satadom") {
		t.Error("a USB loader must not be presented as a SATA disk-on-module")
	}
}

func TestBuildUserCmdlineWins(t *testing.T) {
	cfg, plat, _ := fixture(t, "SA6400", "7.4.1-90080")
	cfg.Cmdline["pcie_aspm"] = "force"
	cfg.Cmdline["mitigations"] = "off"
	id := Identity{Serial: "2350W8RA1234B", MACs: []string{"001132a1b2c3"}}

	b, err := Build(cfg, plat, id, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := b.Get("pcie_aspm"); v != "force" {
		t.Errorf("user override should win, got pcie_aspm=%q", v)
	}
	if v, _ := b.Get("mitigations"); v != "off" {
		t.Error("user cmdline entry is missing")
	}
}

func TestBuildMultiNIC(t *testing.T) {
	cfg, plat, _ := fixture(t, "SA6400", "7.4.1-90080")
	id := Identity{
		Serial: "2350W8RA1234B",
		MACs:   []string{"001132a1b2c3", "001132a1b2c4", "001132a1b2c5"},
	}

	b, err := Build(cfg, plat, id, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := b.Get("netif_num"); v != "3" {
		t.Errorf("netif_num = %q, want 3", v)
	}
	if v, _ := b.Get("skip_vender_mac_interfaces"); v != "0,1,2" {
		t.Errorf("skip_vender_mac_interfaces = %q, want 0,1,2", v)
	}
	if problems := b.Validate(); len(problems) != 0 {
		t.Errorf("multi-NIC command line should validate, got %+v", problems)
	}
}

func TestBuildAlwaysEmitsSkip(t *testing.T) {
	// skip_vender_mac_interfaces always lists every interface, whether or not
	// ForceMAC is set. It is what keeps the assistant from dropping the
	// connection when mac1 differs from the real NIC. An empty value, or going
	// out as a bare flag, loses that effect, so the value is pinned along with
	// it.
	//
	// skip_vender_mac_interfaces 는 ForceMAC 여부와 무관하게 항상 모든
	// 인터페이스를 나열한다. 그래야 mac1 이 실제 NIC 과
	// 달라도 어시스턴트가 접속을 끊지 않는다. 값이 비거나 맨 플래그로
	// 나가면 그 효과가 사라지므로 값까지 함께 고정한다.
	cfg, plat, _ := fixture(t, "SA6400", "7.4.1-90080")
	cfg.Identity.ForceMAC = true
	id := Identity{Serial: "2350W8RA1234B", MACs: []string{"001132a1b2c3"}}

	b, err := Build(cfg, plat, id, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := b.Get("skip_vender_mac_interfaces"); v != "0" {
		t.Errorf("skip_vender_mac_interfaces = %q, want 0 (빈 값/생략 금지)", v)
	}
	if v, _ := b.Get("mac1"); v != "001132a1b2c3" {
		t.Errorf("mac1 = %q, want 001132a1b2c3", v)
	}
}

func TestBuildKeepMACEmitsSkip(t *testing.T) {
	// Without ForceMAC the MACs are the cards' own addresses, and the skip list
	// still names every interface, the same as with ForceMAC.
	//
	// ForceMAC 이 아니면 MAC 은 카드가 원래 가진 주소이고, skip 목록은
	// ForceMAC 일 때와 똑같이 모든 인터페이스를 담는다.
	cfg, plat, _ := fixture(t, "SA6400", "7.4.1-90080")
	cfg.Identity.ForceMAC = false
	id := Identity{Serial: "2350W8RA1234B", MACs: []string{"001132a1b2c3"}}

	b, err := Build(cfg, plat, id, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := b.Get("skip_vender_mac_interfaces"); v != "0" {
		t.Errorf("skip_vender_mac_interfaces = %q, want 0", v)
	}
}

func TestBuildRejectsUnsupportedDSMVersion(t *testing.T) {
	cfg, plat, _ := fixture(t, "SA6400", "6.2.4-25556") // epyc7002 has no 6.2 / epyc7002 에는 6.2 가 없다
	id := Identity{Serial: "2350W8RA1234B", MACs: []string{"001132a1b2c3"}}

	if _, err := Build(cfg, plat, id, DefaultOptions()); err == nil {
		t.Error("building for a DSM version the platform does not support should fail")
	}
}

// TestPlatformQuirks: whether the per-platform boot workarounds attach only to
// the models they belong to.
//
// SA6400 on epyc7002 is in neither list and must get neither - pinning both
// what attaches and what does not is what catches the list being widened by
// mistake.
//
// TestPlatformQuirks - 플랫폼별 부팅 장애물 회피값이 해당 모델에만 붙는지.
//
// SA6400(epyc7002) 은 어느 목록에도 없으니 둘 다 안 붙어야 한다 - 붙는 쪽과 안 붙는 쪽을 같이 고정해야 목록을 잘못 넓혔을 때 잡힌다.
func TestPlatformQuirks(t *testing.T) {
	cases := []struct {
		model    string
		x2apic   bool // whether nox2apic is expected / nox2apic 이 붙어야 하는가
		i2cBlack bool // whether initcall_blacklist is expected / initcall_blacklist 가 붙어야 하는가
	}{
		{"DS918+", true, false},    // apollolake
		{"DS3622xs+", false, true}, // broadwellnk
		{"SA6400", false, false},   // epyc7002 - in neither list / 어느 목록에도 없음
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			cfg, plat, _ := fixture(t, tc.model, "7.4.1-90080")
			b, err := Build(cfg, plat, Identity{
				Serial: "2350W8RA1234B",
				MACs:   []string{"001132a1b2c3"},
			}, DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}

			_, got := b.Get("nox2apic")
			if got != tc.x2apic {
				t.Errorf("nox2apic 방출 = %v, want %v (플랫폼 %s)", got, tc.x2apic, plat.Name)
			}

			v, got := b.Get("initcall_blacklist")
			if got != tc.i2cBlack {
				t.Errorf("initcall_blacklist 방출 = %v, want %v (플랫폼 %s)", got, tc.i2cBlack, plat.Name)
			}
			if got && v != "i2c_i801_init" {
				t.Errorf("initcall_blacklist = %q, want i2c_i801_init", v)
			}

			// pcie_aspm has to be off whatever the platform.
			// pcie_aspm 은 플랫폼과 무관하게 항상 꺼야 한다.
			if v, _ := b.Get("pcie_aspm"); v != "off" {
				t.Errorf("pcie_aspm = %q, want off", v)
			}
		})
	}
}
