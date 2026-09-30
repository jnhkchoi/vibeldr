//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstallFirmware: what the loader placed goes into the installed system,
// with its subpath; a file DSM already has is left as it is, and a name that
// climbs out of /lib/firmware is refused.
//
// TestInstallFirmware - 로더가 놓은 것이 하위 경로째 설치된 시스템으로 간다. DSM
// 이 이미 가진 파일은 그대로 두고, /lib/firmware 밖으로 나가는 이름은 거부한다.
func TestInstallFirmware(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "ramdisk", "lib", "firmware")
	root := filepath.Join(dir, "tmpRoot")
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(from, "rtl_nic", "rtl8168h-2.fw"), "ours")
	write(filepath.Join(from, "bnx2", "bnx2-mips-09-6.2.1b.fw"), "ours")
	write(filepath.Join(root, "lib", "firmware", "bnx2", "bnx2-mips-09-6.2.1b.fw"), "dsm")
	list := filepath.Join(dir, "list")
	recordFirmware(list, []string{"rtl_nic/rtl8168h-2.fw", "bnx2/bnx2-mips-09-6.2.1b.fw", "../../etc/passwd"})

	if n := installFirmware(list, from, root); n != 1 {
		t.Fatalf("installed %d files, want 1", n)
	}
	got, err := os.ReadFile(filepath.Join(root, "lib", "firmware", "rtl_nic", "rtl8168h-2.fw"))
	if err != nil || string(got) != "ours" {
		t.Errorf("rtl8168h-2.fw = %q, %v", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "lib", "firmware", "bnx2", "bnx2-mips-09-6.2.1b.fw")); string(got) != "dsm" {
		t.Errorf("DSM's own file was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "passwd")); err == nil {
		t.Error("a name with .. was followed out of /lib/firmware")
	}
	if n := installFirmware(filepath.Join(dir, "missing"), from, root); n != 0 {
		t.Errorf("no list gave %d", n)
	}
}
