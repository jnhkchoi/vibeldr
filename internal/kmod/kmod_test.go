package kmod

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"vibeldr/internal/lzma"
	"vibeldr/internal/ramdisk"
)

func TestParseModinfo(t *testing.T) {
	blob := []byte("alias=pci:v00008086d000010C9sv*sd*bc*sc*i*\x00" +
		"alias=pci:v00008086d00001526sv*sd*bc*sc*i*\x00" +
		"depends=i2c-algo-bit,dca\x00" +
		"license=GPL\x00")
	got := parseModinfo(blob)
	if len(got["alias"]) != 2 {
		t.Fatalf("got %d aliases, want 2", len(got["alias"]))
	}
	if got["depends"][0] != "i2c-algo-bit,dca" {
		t.Errorf("depends = %q", got["depends"][0])
	}
	if got["license"][0] != "GPL" {
		t.Errorf("license = %q", got["license"][0])
	}
}

func TestParseModinfoIgnoresJunk(t *testing.T) {
	// Padding NULs and a field with no "=" must not produce entries.
	// 채움 NUL 과 "=" 없는 필드는 항목을 만들면 안 된다.
	got := parseModinfo([]byte("\x00\x00nokeyhere\x00license=GPL\x00\x00"))
	if len(got) != 1 {
		t.Fatalf("got %v, want only license", got)
	}
}

// The kernel treats hyphens and underscores in module names as the same
// character, and the two spellings genuinely do appear in the same ramdisk:
// the file is i2c-algo-bit.ko and /proc/modules says i2c_algo_bit.
//
// 커널은 모듈 이름의 하이픈과 밑줄을 같은 문자로 본다. 그리고 두 표기는 실제로
// 한 램디스크 안에 같이 나온다. 파일은 i2c-algo-bit.ko 이고 /proc/modules 는
// i2c_algo_bit 이라고 한다.
func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"i2c-algo-bit.ko": "i2c_algo_bit",
		"igb.ko":          "igb",
		"i2c_algo_bit":    "i2c_algo_bit",
		"virtio-rng":      "virtio_rng",
	} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatches(t *testing.T) {
	m := Module{Aliases: []string{"pci:v00008086d000010C9sv*sd*bc*sc*i*"}}
	// What QEMU's emulated 82576 reports.
	// QEMU 가 흉내 내는 82576 이 내놓는 값.
	if !m.Matches("pci:v00008086d000010C9sv00008086sd0000A03Cbc02sc00i00") {
		t.Error("should match the 82576 this alias names")
	}
	if m.Matches("pci:v00001AF4d00001000sv00001AF4sd00000001bc02sc00i00") {
		t.Error("should not match a virtio device")
	}
}

func TestMatchesWithNoAliases(t *testing.T) {
	if (Module{}).Matches("pci:v00008086d000010C9sv*sd*bc*sc*i*") {
		t.Error("a module with no aliases claims nothing")
	}
}

func TestResolveOrdersDependenciesFirst(t *testing.T) {
	idx := Index{
		"igb":          {Name: "igb", Depends: []string{"i2c_algo_bit"}, Aliases: []string{"pci:v8086d10C9*"}},
		"i2c_algo_bit": {Name: "i2c_algo_bit"},
		"ixgbe":        {Name: "ixgbe", Aliases: []string{"pci:v8086d10FB*"}},
	}
	got := idx.Resolve([]string{"pci:v8086d10C9sv0sd0"}, nil)
	var names []string
	for _, m := range got {
		names = append(names, m.Name)
	}
	if !reflect.DeepEqual(names, []string{"i2c_algo_bit", "igb"}) {
		t.Fatalf("load order = %v, want [i2c_algo_bit igb]", names)
	}
}

func TestResolveSkipsLoaded(t *testing.T) {
	idx := Index{
		"igb":          {Name: "igb", Depends: []string{"i2c_algo_bit"}, Aliases: []string{"pci:v8086d10C9*"}},
		"i2c_algo_bit": {Name: "i2c_algo_bit"},
	}
	got := idx.Resolve([]string{"pci:v8086d10C9sv0sd0"}, map[string]bool{"i2c_algo_bit": true})
	if len(got) != 1 || got[0].Name != "igb" {
		t.Fatalf("got %v, want only igb", got)
	}
	if len(idx.Resolve([]string{"pci:v8086d10C9sv0sd0"}, map[string]bool{"igb": true})) != 0 {
		t.Error("an already-loaded module must not be loaded again")
	}
}

// A Synology original and one of ours can claim the same device - r8168 and
// r8169 both take 10ec:8168. Only the original may be loaded for it; ours is
// still loaded for a device only it claims.
//
// 시놀 원본과 우리 모듈이 같은 장치를 잡을 수 있다 - r8168 과 r8169 가 둘 다
// 10ec:8168 을 잡는다. 그 장치에는 원본만 올려야 하고, 우리 것만 잡는 장치에는
// 우리 것을 올린다.
func TestResolvePreferringOriginals(t *testing.T) {
	idx := Index{
		"r8168": {Name: "r8168", Aliases: []string{"pci:v000010ECd00008168sv*sd*bc*sc*i*"}},
		"r8169": {Name: "r8169", Aliases: []string{"pci:v000010ECd00008168sv*sd*bc*sc*i*", "pci:v000010ECd00008169sv*sd*bc*sc*i*"}},
	}
	prefer := map[string]bool{"r8168": true}
	names := func(ms []Module) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Name)
		}
		return out
	}
	card8168 := "pci:v000010ECd00008168sv00001458sd0000E000bc02sc00i00"
	card8169 := "pci:v000010ECd00008169sv00001458sd0000E000bc02sc00i00"
	if got := names(idx.ResolvePreferring([]string{card8168}, nil, prefer)); !reflect.DeepEqual(got, []string{"r8168"}) {
		t.Errorf("8168 card: %v, want [r8168]", got)
	}
	if got := names(idx.ResolvePreferring([]string{card8169}, nil, prefer)); !reflect.DeepEqual(got, []string{"r8169"}) {
		t.Errorf("8169 card: %v, want [r8169] - only ours claims it", got)
	}
	if got := names(idx.ResolvePreferring([]string{card8168}, map[string]bool{"r8168": true}, prefer)); len(got) != 0 {
		t.Errorf("original already loaded: %v, want nothing - ours must not take the device", got)
	}
	if got := names(idx.Resolve([]string{card8168}, nil)); !reflect.DeepEqual(got, []string{"r8168", "r8169"}) {
		t.Errorf("without a preference nothing changes: %v", got)
	}
}

// A dependency cycle in a hand-edited ramdisk must not hang the boot.
// 손으로 고친 램디스크의 의존 순환이 부팅을 멈추게 해서는 안 된다.
func TestResolveSurvivesACycle(t *testing.T) {
	idx := Index{
		"a": {Name: "a", Depends: []string{"b"}, Aliases: []string{"pci:*"}},
		"b": {Name: "b", Depends: []string{"a"}},
	}
	got := idx.Resolve([]string{"pci:v1d2"}, nil)
	if len(got) == 0 {
		t.Fatal("resolution gave up entirely")
	}
}

// realModules unpacks the .ko files out of Synology's own ramdisk.
// realModules 는 시놀로지 원본 램디스크에서 .ko 파일들을 풀어낸다.
func realModules(t *testing.T) Index {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "work", "dsm-real", "rd.gz"))
	if err != nil {
		t.Skip("work/dsm-real/rd.gz is not present")
	}
	cpio, err := lzma.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ramdisk.ReadCPIO(cpio)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var n int
	for _, e := range a.Entries {
		if !strings.HasSuffix(e.Name, ".ko") || !e.IsRegular() {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(e.Name)), e.Data, 0o644); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n == 0 {
		t.Skip("no modules in the ramdisk")
	}
	idx, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return idx
}

// TestRealRamdiskDriversParse reads the actual drivers Synology ships and
// checks the one that matters: a virtual machine with an emulated Intel 82576
// is served by igb, which is already in the ramdisk and which nothing loads.
//
// TestRealRamdiskDriversParse 는 시놀로지가 배포하는 실제 드라이버를 읽고, 중요한
// 한 가지를 확인한다. Intel 82576 을 흉내 내는 가상 머신은 igb 가 맡는데, igb 는
// 이미 램디스크에 있지만 아무도 로드하지 않는다.
func TestRealRamdiskDriversParse(t *testing.T) {
	idx := realModules(t)
	if len(idx) < 40 {
		t.Fatalf("parsed %d modules, expected around 47", len(idx))
	}

	igb, ok := idx["igb"]
	if !ok {
		t.Fatal("igb is missing from the ramdisk")
	}
	if len(igb.Aliases) == 0 {
		t.Fatal("igb has no alias table")
	}
	t.Logf("igb: %d aliases, depends on %v", len(igb.Aliases), igb.Depends)

	// The device id QEMU's igb model presents.
	// QEMU 의 igb 모델이 내놓는 장치 id.
	const qemu82576 = "pci:v00008086d000010C9sv00008086sd0000A03Cbc020000sc00i00"
	if !igb.Matches(qemu82576) {
		t.Fatalf("igb does not claim QEMU's emulated 82576 (%s)", qemu82576)
	}

	order := idx.Resolve([]string{qemu82576}, nil)
	var names []string
	for _, m := range order {
		names = append(names, m.Name)
	}
	t.Logf("load order for an emulated 82576: %v", names)
	if len(names) == 0 || names[len(names)-1] != "igb" {
		t.Fatalf("igb should be loaded last, after its dependencies; got %v", names)
	}
	for i, n := range names {
		if n == "igb" && i != len(names)-1 {
			t.Error("igb appears before one of its dependencies")
		}
	}
}

// Nothing in the ramdisk claims a virtio NIC, which is why an installer on
// virtio networking comes up with no network. The test does not fail on this;
// it only logs a note when a module in the ramdisk does claim one.
//
// 램디스크의 어떤 모듈도 virtio NIC 를 잡지 않는다. 그래서 virtio 네트워크 위의
// 인스톨러는 네트워크 없이 뜬다. 이 테스트는 실패하지 않고, 램디스크의 모듈이
// 그것을 잡으면 메모만 남긴다.
func TestRealRamdiskHasNoVirtioNet(t *testing.T) {
	idx := realModules(t)
	const virtioNet = "pci:v00001AF4d00001000sv00001AF4sd00000001bc02sc00i00"
	for name, m := range idx {
		if m.Matches(virtioNet) {
			t.Logf("note: %s now claims virtio-net", name)
		}
	}
	if _, ok := idx["virtio_net"]; ok {
		t.Log("note: virtio_net is present in this ramdisk")
	}
}
