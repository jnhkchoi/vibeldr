package catalog

import (
	"strings"
	"testing"
)

func TestLoadEmbeddedCatalog(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(c.PlatformNames()); got < 10 {
		t.Fatalf("only %d platforms loaded, expected the full catalog", got)
	}
	if got := len(c.Models()); got < 50 {
		t.Fatalf("only %d models loaded, expected the full catalog", got)
	}
}

func TestPlatformForModel(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		model    string
		platform string
		dt       bool
		kernel74 string
	}{
		{"SA6400", "epyc7002", true, "5.10.55"},
		{"DS918+", "apollolake", false, "4.4.302"},
		{"DS923+", "r1000", true, "4.4.302"},
		{"DS3617xs", "broadwell", false, "4.4.302"},
	}
	for _, tc := range cases {
		p, ok := c.PlatformForModel(tc.model)
		if !ok {
			t.Errorf("%s: not found in catalog", tc.model)
			continue
		}
		if p.Name != tc.platform {
			t.Errorf("%s: platform = %s, want %s", tc.model, p.Name, tc.platform)
		}
		if p.DT != tc.dt {
			t.Errorf("%s: DT = %v, want %v", tc.model, p.DT, tc.dt)
		}
		if got := p.Kernels["7.4"]; got != tc.kernel74 {
			t.Errorf("%s: DSM 7.4 kernel = %q, want %q", tc.model, got, tc.kernel74)
		}
	}
}

func TestPlatformForModelIsCaseInsensitive(t *testing.T) {
	c, _ := Load()
	if _, ok := c.PlatformForModel("sa6400"); !ok {
		t.Error("lower case model name should resolve")
	}
	if _, ok := c.PlatformForModel("  SA6400  "); !ok {
		t.Error("surrounding whitespace should be tolerated")
	}
}

func TestKernelMajor(t *testing.T) {
	c, _ := Load()
	p, _ := c.PlatformForModel("SA6400")
	if got := p.KernelMajor("7.4"); got != 5 {
		t.Errorf("KernelMajor = %d, want 5", got)
	}
	if got := p.KernelMajor("6.2"); got != 0 {
		t.Errorf("unsupported version should give 0, got %d", got)
	}
}

// A generated serial must satisfy the very rule it was generated from.
// This is the check that catches a bad alphabet or an off-by-one length.
//
// 생성한 시리얼은 그걸 만든 규칙을 스스로 통과해야 한다. 잘못된 문자 집합이나
// 한 자리 어긋난 길이를 잡는 검사가 이것이다.
func TestGeneratedSerialsValidate(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// SA6400 uses the "alpha" suffix, DS918+ the "numeric" one.
	// SA6400 은 "alpha" 접미사, DS918+ 는 "numeric" 접미사를 쓴다.
	for _, model := range []string{"SA6400", "DS918+", "DS923+", "DS425+"} {
		for i := 0; i < 50; i++ {
			serial, err := c.GenerateSerial(model)
			if err != nil {
				t.Fatalf("%s: GenerateSerial: %v", model, err)
			}
			if len(serial) != 13 {
				t.Fatalf("%s: serial %q has length %d, want 13", model, serial, len(serial))
			}
			if err := c.ValidateSerial(model, serial); err != nil {
				t.Fatalf("%s: generated serial %q failed its own rule: %v", model, serial, err)
			}
			if strings.ContainsAny(serial, "IO") {
				// Synology never uses I or O; they are too easy to confuse
				// with 1 and 0 on a case badge.
				//
				// 시놀로지는 I 와 O 를 쓰지 않는다. 케이스 라벨에서 1, 0 과
				// 헷갈리기 쉽기 때문이다.
				t.Fatalf("%s: serial %q contains I or O", model, serial)
			}
		}
	}
}

func TestValidateSerialRejectsWrongPrefix(t *testing.T) {
	c, _ := Load()
	// Correct shape, but the prefix belongs to no SA6400.
	// 모양은 맞지만 접두사가 SA6400 의 것이 아니다.
	if err := c.ValidateSerial("SA6400", "0000W8RA1234B"); err == nil {
		t.Error("a serial with an unknown prefix should be rejected")
	}
	if err := c.ValidateSerial("SA6400", "TOOSHORT"); err == nil {
		t.Error("a short serial should be rejected")
	}
}

func TestGenerateMACs(t *testing.T) {
	c, _ := Load()

	macs, err := c.GenerateMACs("SA6400", 4)
	if err != nil {
		t.Fatalf("GenerateMACs: %v", err)
	}
	if len(macs) != 4 {
		t.Fatalf("got %d MACs, want 4", len(macs))
	}
	seen := map[string]bool{}
	for _, m := range macs {
		if len(m) != 12 {
			t.Errorf("MAC %q is not 12 hex digits", m)
		}
		if !strings.HasPrefix(m, DefaultMacPrefix) {
			t.Errorf("MAC %q does not use the Synology OUI %s", m, DefaultMacPrefix)
		}
		if seen[m] {
			t.Errorf("duplicate MAC %q", m)
		}
		seen[m] = true
	}
}

func TestGenerateMACsUsesModelSpecificOUI(t *testing.T) {
	c, _ := Load()
	macs, err := c.GenerateMACs("DS425+", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(macs[0], "9009d0") {
		t.Errorf("DS425+ MAC %q should use its own OUI 9009d0", macs[0])
	}
}

func TestGenerateMACsRejectsBadCount(t *testing.T) {
	c, _ := Load()
	if _, err := c.GenerateMACs("SA6400", 0); err == nil {
		t.Error("zero NICs should be rejected")
	}
	if _, err := c.GenerateMACs("SA6400", 9); err == nil {
		t.Error("more than 8 NICs should be rejected")
	}
}

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"00:11:32:A1:B2:C3": "001132a1b2c3",
		"00-11-32-a1-b2-c3": "001132a1b2c3",
		"001132A1B2C3":      "001132a1b2c3",
	}
	for in, want := range cases {
		got, err := NormalizeMAC(in)
		if err != nil {
			t.Errorf("NormalizeMAC(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "zz1132a1b2c3", "001132a1b2", "001132a1b2c3ff"} {
		if _, err := NormalizeMAC(bad); err == nil {
			t.Errorf("NormalizeMAC(%q) should have failed", bad)
		}
	}
}

func TestNoFlagsContains(t *testing.T) {
	c, _ := Load()
	p, ok := c.Platform("apollolake")
	if !ok {
		t.Fatal("apollolake missing")
	}
	if !p.NoFlagsContains("x2apic") {
		t.Error("apollolake should mask off x2apic")
	}
	if p.NoFlagsContains("avx512") {
		t.Error("unexpected flag reported")
	}
}
