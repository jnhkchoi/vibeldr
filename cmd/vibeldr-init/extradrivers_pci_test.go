package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseHexID pins the parsing explicitly. The hex ID files in sysfs are a
// contract we cannot change, so only the parsing is ours to check.
//
// TestParseHexID 는 파싱을 명시적으로 잡는다. sysfs 의 hex ID 파일은 우리가
// 손댈 수 없는 계약이라, 확인할 것은 파싱뿐이다.
func TestParseHexID(t *testing.T) {
	cases := []struct {
		in      string
		want    uint32
		wantErr bool
	}{
		{"0x1d6a\n", 0x1d6a, false},
		{"0x15b3", 0x15b3, false},
		{"0x10DE\n", 0x10de, false}, // upper case is seen in practice / 대문자도 실사용
		{"1d6a", 0x1d6a, false},     // lenient without the prefix too / 접두사 없어도 관대하게
		{"", 0, true},
		{"0x\n", 0, true},
		{"nope", 0, true},
	}
	for _, c := range cases {
		got, err := parseHexID(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseHexID(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseHexID(%q) = %#x, want %#x", c.in, got, c.want)
		}
	}
}

// TestScanPCIDevices fakes sysfs with real directories and checks that the
// scan puts the values together correctly. It plants three devices - Aquantia
// AQC107 (1d6a:00b1), Mellanox ConnectX-5 (15b3:1017) and NVIDIA T4
// (10de:1eb8) - and sees that all of them come back.
//
// Real sysfs uses colons, as in "0000:03:00.0", but a Windows filesystem does
// not allow a colon in a file name. The scanner does not parse the directory
// name's format; it only passes it on as sysfsAddress. So here the colons are
// planted as underscores, and the test only checks that the name comes back
// as it is.
//
// TestScanPCIDevices 는 sysfs 를 실제 디렉터리로 흉내내서 scan 이 값을
// 올바르게 조립하는지 본다. Aquantia AQC107 (1d6a:00b1) + Mellanox
// ConnectX-5 (15b3:1017) + NVIDIA T4 (10de:1eb8) 세 개를 심고 다 회수되는지
// 본다.
//
// 실제 sysfs 는 "0000:03:00.0" 처럼 콜론을 쓰지만 Windows 파일시스템은
// 파일명에 콜론을 못 쓴다. 스캐너는 디렉터리 이름의 형식을 파싱하지 않고
// 그대로 sysfsAddress 에 담아 넘길 뿐이라, 여기서는 콜론을 언더스코어로
// 바꿔 심고 그 이름이 그대로 회수되는지만 본다.
func TestScanPCIDevices(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bus", "pci", "devices")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	fixtures := []struct {
		addr           string
		vendor, device string
	}{
		{"0000_03_00.0", "0x1d6a\n", "0x00b1\n"},
		{"0000_04_00.0", "0x15b3\n", "0x1017\n"},
		{"0000_82_00.0", "0x10de\n", "0x1eb8\n"},
	}
	for _, f := range fixtures {
		d := filepath.Join(dir, f.addr)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "vendor"), []byte(f.vendor), 0o644)
		os.WriteFile(filepath.Join(d, "device"), []byte(f.device), 0o644)
	}

	// A device missing one of its files has to be skipped quietly by the scan.
	// 파일이 하나 빠진 장치는 스캔에서 조용히 스킵되어야 한다.
	broken := filepath.Join(dir, "0000_05_00.0")
	os.MkdirAll(broken, 0o755)
	os.WriteFile(filepath.Join(broken, "vendor"), []byte("0xffff\n"), 0o644)
	// The device file is deliberately not created.
	// device 파일을 일부러 안 만든다.

	devs := scanPCIDevices(root)
	if len(devs) != 3 {
		t.Fatalf("scanPCIDevices got %d devices, want 3: %+v", len(devs), devs)
	}
	byVendor := func(v uint32) *pciDevice {
		for i := range devs {
			if devs[i].vendor == v {
				return &devs[i]
			}
		}
		return nil
	}
	if d := byVendor(0x1d6a); d == nil || d.device != 0x00b1 || d.sysfsAddress != "0000_03_00.0" {
		t.Errorf("Aquantia device parse wrong: %+v", d)
	}
	if d := byVendor(0x15b3); d == nil || d.device != 0x1017 {
		t.Errorf("Mellanox device parse wrong: %+v", d)
	}
	if d := byVendor(0x10de); d == nil || d.device != 0x1eb8 {
		t.Errorf("NVIDIA device parse wrong: %+v", d)
	}

	mlx := pciByVendor(devs, 0x15b3)
	if len(mlx) != 1 {
		t.Errorf("pciByVendor(mlx) got %d, want 1", len(mlx))
	}
	none := pciByVendor(devs, 0x8086)
	if len(none) != 0 {
		t.Errorf("pciByVendor(intel) got %d, want 0", len(none))
	}
}
