package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibeldr/internal/catalog"
)

func mustCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDefaultConfigIsValid(t *testing.T) {
	cat := mustCatalog(t)
	cfg := Default()
	// Default() leaves DSM.Version empty, because cmdInit fills it per model from
	// the release catalogue. What is under test is the consistency of the other
	// fields, so a valid version is planted here for the test's sake.
	//
	// Default() 는 DSM.Version 을 비워둔다. cmdInit 이 릴리스 카탈로그에서
	// 모델별로 채우기 때문이다. 검증 대상은 나머지 필드의 정합성이므로 여기서
	// 테스트를 위해 유효한 버전을 하나 심는다.
	cfg.DSM.Version = "7.4.1-90080"
	if err := cfg.Validate(cat); err != nil {
		t.Fatalf("the shipped default config must validate, got: %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	cat := mustCatalog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "loader.yaml")

	orig := Default()
	orig.DSM.Version = "7.4.1-90080"
	orig.Identity.Serial = "2350W8RA1234B"
	orig.Cmdline["mitigations"] = "off"
	if err := orig.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path, cat)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Model != orig.Model {
		t.Errorf("model = %q, want %q", loaded.Model, orig.Model)
	}
	if loaded.Identity.Serial != orig.Identity.Serial {
		t.Errorf("serial = %q, want %q", loaded.Identity.Serial, orig.Identity.Serial)
	}
	if loaded.Cmdline["mitigations"] != "off" {
		t.Error("user cmdline entry did not survive the round trip")
	}
}

// A typo in a key must be an error. Silently ignoring it is the failure mode
// that makes "I changed the setting and nothing happened" so hard to diagnose.
//
// 키 오타는 오류여야 한다. 조용히 무시하면 "설정을 바꿨는데 아무 일도 안
// 일어났다" 가 되고, 그건 원인을 찾기가 아주 어렵다.
func TestLoadRejectsUnknownKeys(t *testing.T) {
	cat := mustCatalog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "loader.yaml")

	body := `version: 1
model: SA6400
dsm:
  version: "7.4.1-90080"
identiy:          # typo: should be "identity"
  serial: ""
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, cat); err == nil {
		t.Fatal("a misspelled key should be rejected")
	}
}

func problemsFor(t *testing.T, cfg *Config) []string {
	t.Helper()
	cat := mustCatalog(t)
	cfg.applyDefaults()
	err := cfg.Validate(cat)
	if err == nil {
		return nil
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a *ValidationError, got %T: %v", err, err)
	}
	return ve.Problems
}

func hasProblemContaining(problems []string, substr string) bool {
	for _, p := range problems {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

func TestValidateUnknownModel(t *testing.T) {
	cfg := Default()
	cfg.Model = "DS999+"
	if !hasProblemContaining(problemsFor(t, cfg), "unknown model") {
		t.Error("an unknown model should be reported")
	}
}

func TestValidateMalformedDSMVersion(t *testing.T) {
	cfg := Default()
	cfg.DSM.Version = "7.4"
	if !hasProblemContaining(problemsFor(t, cfg), "must look like") {
		t.Error("a malformed dsm.version should be reported")
	}
}

// TestValidateOnlyTargetDSMVersion: a DSM version outside 7.4 is refused, both
// one the platform never ran and an older one the catalog still lists.
//
// TestValidateOnlyTargetDSMVersion - 7.4 밖의 DSM 버전은 거부한다. 플랫폼이 돌린
// 적 없는 버전도, 카탈로그에 아직 올라 있는 옛 버전도.
func TestValidateOnlyTargetDSMVersion(t *testing.T) {
	for _, v := range []string{"6.2.4-25556", "7.2.2-72806"} {
		cfg := Default()
		cfg.DSM.Version = v
		if !hasProblemContaining(problemsFor(t, cfg), "only DSM 7.4 is supported") {
			t.Errorf("dsm.version %s should be refused", v)
		}
	}
}

func TestValidateNICCountMismatch(t *testing.T) {
	cfg := Default()
	cfg.Identity.NICCount = 2
	cfg.Identity.MACs = []string{"001132a1b2c3"}
	if !hasProblemContaining(problemsFor(t, cfg), "nic_count") {
		t.Error("nic_count disagreeing with the MAC list should be reported")
	}
}

func TestValidatePortMapOnDeviceTreePlatform(t *testing.T) {
	cfg := Default() // SA6400 is a device-tree platform / SA6400 은 device tree 플랫폼
	cfg.Storage.SataPortMap = "4"
	cfg.Storage.DiskIdxMap = "00"
	if !hasProblemContaining(problemsFor(t, cfg), "device tree") {
		t.Error("a port map on a device-tree platform should be reported as ignored")
	}
}

func TestValidatePortMapPairing(t *testing.T) {
	cfg := Default()
	cfg.Model = "DS918+" // non device-tree, so port maps are meaningful / 비-DT 라 포트맵이 의미가 있다
	cfg.Storage.SataPortMap = "4"
	// DiskIdxMap deliberately left empty.
	// DiskIdxMap 은 일부러 비워 둔다.
	if !hasProblemContaining(problemsFor(t, cfg), "must be set together") {
		t.Error("a half-configured port map should be reported")
	}
}

func TestValidateCmdlineWhitespace(t *testing.T) {
	cfg := Default()
	cfg.Cmdline["broken"] = "two words"
	if !hasProblemContaining(problemsFor(t, cfg), "whitespace") {
		t.Error("a cmdline value containing a space should be reported")
	}
}

func TestValidateBadSerial(t *testing.T) {
	cfg := Default()
	cfg.Identity.Serial = "NOTASERIAL123"
	if !hasProblemContaining(problemsFor(t, cfg), "identity.serial") {
		t.Error("a serial that does not match the model rule should be reported")
	}
}

func TestValidateCollectsEveryProblem(t *testing.T) {
	cfg := Default()
	cfg.Model = "DS999+"
	cfg.DSM.Version = "nope"
	cfg.Identity.NICCount = 3
	cfg.Identity.MACs = []string{"zz"}

	problems := problemsFor(t, cfg)
	if len(problems) < 4 {
		t.Errorf("expected every problem to be reported at once, got %d: %v", len(problems), problems)
	}
}

// TestLoadStorageOverrides: the bay override comes out of the YAML as it is,
// and the retired storage keys are still accepted.
//
// TestLoadStorageOverrides - 베이 override 가 YAML 에서 그대로 들어오고, 더
// 쓰지 않는 storage 키도 여전히 받아들여진다.
func TestLoadStorageOverrides(t *testing.T) {
	cat := mustCatalog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "loader.yaml")

	body := `version: 1
model: SA6400
dsm:
  version: "7.4.1-90080"
storage:
  bays: 16
  nvme_slots: 2
  expansion:
    - kind: DX513
      bays: 5
    - kind: RX415
      bays: 4
  usb_as_internal: false
  sas: true
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, cat)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Storage.Bays != 16 {
		t.Errorf("storage.bays = %d, want 16", loaded.Storage.Bays)
	}
}

// A file with no storage field has to stay at its zero values and pass.
//
// storage 필드가 없는 파일은 zero 값으로 남고 검증을 통과해야 한다.
func TestStorageOmittedStillValidates(t *testing.T) {
	cfg := Default()
	cfg.DSM.Version = "7.4.1-90080" // Default() leaves it empty, so it is filled to pass / Default() 가 이 자리를 비우기 때문에 검증 통과 위해 채움
	if problems := problemsFor(t, cfg); len(problems) != 0 {
		t.Fatalf("bare default must validate, got: %v", problems)
	}
	if cfg.Storage.Bays != 0 {
		t.Errorf("Default() must leave storage overrides zero, got %+v", cfg.Storage)
	}
}

func TestValidateStorageBaysTooHigh(t *testing.T) {
	cfg := Default()
	cfg.Storage.Bays = 128
	if !hasProblemContaining(problemsFor(t, cfg), "storage.bays") {
		t.Error("bays > 64 should be rejected")
	}
}

func TestProductVersionAndBuildNumber(t *testing.T) {
	cfg := Default()
	cfg.DSM.Version = "7.4.1-90080"
	if got := cfg.ProductVersion(); got != "7.4" {
		t.Errorf("ProductVersion = %q, want 7.4", got)
	}
	if got := cfg.BuildNumber(); got != "90080" {
		t.Errorf("BuildNumber = %q, want 90080", got)
	}

	cfg.DSM.Version = "7.2-64570"
	if got := cfg.ProductVersion(); got != "7.2" {
		t.Errorf("ProductVersion = %q, want 7.2", got)
	}
}

// TestApplyDefaultsFillsSynoinfo: whether the defaults survive reading a
// loader.yaml with no synoinfo section.
//
// The loader reads p1's loader.yaml on every boot. Once a file with an empty
// synoinfo has been saved, that empty value keeps winning unless the missing
// keys are filled back in.
//
// TestApplyDefaultsFillsSynoinfo - synoinfo 항목이 없는 loader.yaml 을
// 읽어도 기본값이 살아남는지 본다.
//
// 로더는 부팅마다 p1 의 loader.yaml 을 읽는다. synoinfo 가 빈 파일이 한 번
// 저장되면, 빠진 키를 채워넣지 않는 한 그 빈 값이 계속 이긴다.
func TestApplyDefaultsFillsSynoinfo(t *testing.T) {
	c := &Config{Synoinfo: map[string]string{}}
	c.applyDefaults()
	// The disk compatibility check has to stay on. Turned off, the verdict is left
	// as "disabled", the rule engine fails to match and the pool drops to "at
	// risk".
	//
	// 디스크 호환성 검사는 켜야 한다. 끄면 판정이 "disabled" 로 남아
	// 규칙 엔진이 매칭에 실패하고 풀이 "위험" 으로 떨어진다.
	if v := c.Synoinfo["support_disk_compatibility"]; v != "yes" {
		t.Errorf("disk compatibility check is not turned on: %q", v)
	}
	if v := c.Synoinfo["support_oob_ctl"]; v != "no" {
		t.Errorf("support_oob_ctl = %q, want no", v)
	}

	// A value the user stated is not overwritten.
	// 사용자가 명시한 값은 덮어쓰지 않는다.
	c2 := &Config{Synoinfo: map[string]string{"support_disk_compatibility": "no"}}
	c2.applyDefaults()
	if v := c2.Synoinfo["support_disk_compatibility"]; v != "no" {
		t.Errorf("overwrote the user's value: %q", v)
	}
	if v := c2.Synoinfo["support_fan"]; v != "no" {
		t.Errorf("remaining defaults not filled in: %q", v)
	}
}
