//go:build linux

// grub_rewrite_linux_test.go is the unit test for rewriting partition 1's
// grub.cfg. It verifies the rendered result, the backup, idempotency and the
// microcode toggle against a fake mount directory. It never starts a real GRUB.
//
// grub_rewrite_linux_test.go - 파티션 1 grub.cfg 재작성 유닛 테스트.
// fake mount 디렉터리에 대고 렌더 결과 / 백업 / idempotency / microcode
// 토글을 검증한다. 실제 GRUB 을 띄우지는 않는다.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readGrub(t *testing.T, mount string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(mount, "boot", "grub", "grub.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestRewriteGrubConfig_ThreeEntries: whether all three of dsm, junior and
// reconfigure come out, each carrying its label, command line and label
// reference.
//
// TestRewriteGrubConfig_ThreeEntries - dsm/junior/reconfigure 세 엔트리가
// 다 나오고 각각의 라벨/커맨드라인/라벨 참조가 담기는지 본다.
func TestRewriteGrubConfig_ThreeEntries(t *testing.T) {
	mount := t.TempDir()
	params := GrubParams{
		Model:         "SA6400",
		DSMVersion:    "7.4.1-90080",
		KernelCmdline: "syno_hw_version=SA6400 console=ttyS0,115200n8 root=/dev/synoboot",
		Microcode:     false,
	}
	if err := RewriteGrubConfig(mount, params); err != nil {
		t.Fatal(err)
	}

	body := readGrub(t, mount)
	for _, want := range []string{
		"set default=dsm",
		"set timeout=5",
		"--id dsm",
		"--id junior",
		"--id reconfigure",
		"'DSM SA6400 7.4.1-90080'",
		"'DSM SA6400 7.4.1-90080 (reinstall)'",
		"'vibeldr - reconfigure",
		"--label VIBELDR3",
		"--label VIBELDR1",
		"syno_hw_version=SA6400 console=ttyS0,115200n8 root=/dev/synoboot",
		"force_junior",
		"initrd /initrd-dsm",
		"initrd /initrd-vibeldr",
		"linux /vmlinuz",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("grub.cfg missing %q\n---\n%s", want, body)
		}
	}
}

// TestRewriteGrubConfig_ReconfigureAlwaysPresent: the reconfigure entry has to
// always be there. With the way back in to rebuild gone, the user cannot change
// the model or the version.
//
// TestRewriteGrubConfig_ReconfigureAlwaysPresent - reconfigure 엔트리는
// 항상 있어야 한다. 재빌드 진입점이 사라지면 사용자가 모델·버전을 못 바꾼다.
func TestRewriteGrubConfig_ReconfigureAlwaysPresent(t *testing.T) {
	mount := t.TempDir()
	params := GrubParams{
		Model:         "DS918+",
		DSMVersion:    "7.2.2-72806",
		KernelCmdline: "x=1",
	}
	if err := RewriteGrubConfig(mount, params); err != nil {
		t.Fatal(err)
	}
	body := readGrub(t, mount)
	if !strings.Contains(body, "--id reconfigure") {
		t.Errorf("reconfigure 엔트리가 있어야 함\n%s", body)
	}
	if !strings.Contains(body, "--label VIBELDR1") {
		t.Errorf("reconfigure 에서 파티션 1 라벨을 잡아야 함\n%s", body)
	}
	if !strings.Contains(body, "--id dsm") || !strings.Contains(body, "--id junior") {
		t.Errorf("dsm / junior 엔트리가 있어야 함\n%s", body)
	}
}

// TestRewriteGrubConfig_MicrocodeToggle: how the initrd line differs with
// Microcode on and off.
//
// TestRewriteGrubConfig_MicrocodeToggle - Microcode on/off 에 따라 initrd
// 라인이 어떻게 달라지는지 본다.
func TestRewriteGrubConfig_MicrocodeToggle(t *testing.T) {
	base := GrubParams{
		Model:         "SA6400",
		DSMVersion:    "7.4.1-90080",
		KernelCmdline: "x=1",
	}

	off := t.TempDir()
	base.Microcode = false
	if err := RewriteGrubConfig(off, base); err != nil {
		t.Fatal(err)
	}
	bodyOff := readGrub(t, off)
	if strings.Contains(bodyOff, "intel-ucode.img") || strings.Contains(bodyOff, "amd-ucode.img") {
		t.Errorf("Microcode=false 에 마이크로코드 파일이 들어감\n%s", bodyOff)
	}
	if !strings.Contains(bodyOff, "initrd /initrd-dsm") {
		t.Errorf("Microcode=false 인데 initrd /initrd-dsm 라인이 없음\n%s", bodyOff)
	}

	on := t.TempDir()
	base.Microcode = true
	if err := RewriteGrubConfig(on, base); err != nil {
		t.Fatal(err)
	}
	bodyOn := readGrub(t, on)
	if !strings.Contains(bodyOn, "initrd /intel-ucode.img /amd-ucode.img /initrd-dsm") {
		t.Errorf("Microcode=true 인데 순서 (intel,amd,initrd-dsm) 가 안 맞음\n%s", bodyOn)
	}
	if !strings.Contains(bodyOn, "initrd /intel-ucode.img /amd-ucode.img /initrd-vibeldr") {
		t.Errorf("Microcode=true 인데 build 엔트리에 마이크로코드 접두가 없음\n%s", bodyOn)
	}
}

// TestRewriteGrubConfig_BackupPreservesBootstrap: whether the first rewrite
// saves the original as .bootstrap.bak, and whether a second call leaves that
// backup alone.
//
// TestRewriteGrubConfig_BackupPreservesBootstrap - 첫 재작성이 원본을
// .bootstrap.bak 로 저장하는지, 두 번째 호출이 그 백업을 안 덮어쓰는지 본다.
func TestRewriteGrubConfig_BackupPreservesBootstrap(t *testing.T) {
	mount := t.TempDir()
	dir := filepath.Join(mount, "boot", "grub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(
		"# bootstrap grub.cfg\n" +
			"set default=vibeldr\n" +
			"menuentry 'vibeldr - configure and install' {\n" +
			"  linux /zImage\n" +
			"  initrd /initrd\n" +
			"}\n")
	target := filepath.Join(dir, "grub.cfg")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	params := GrubParams{
		Model:         "SA6400",
		DSMVersion:    "7.4.1-90080",
		KernelCmdline: "x=1",
	}
	if err := RewriteGrubConfig(mount, params); err != nil {
		t.Fatal(err)
	}

	backupPath := target + grubBootstrapBackupSuffix
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("백업 파일이 없음: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("백업 내용이 원본과 다름\nwant: %q\ngot:  %q", original, got)
	}

	// The second rewrite; the backup stays as it was.
	// 두 번째 재작성. 백업은 그대로 유지된다.
	if err := RewriteGrubConfig(mount, params); err != nil {
		t.Fatal(err)
	}
	got2, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != string(original) {
		t.Errorf("두 번째 호출이 백업을 덮어씀\nwant: %q\ngot:  %q", original, got2)
	}
}

// TestRewriteGrubConfig_Idempotent: calling twice with the same parameters has
// to leave identical grub.cfg content.
//
// TestRewriteGrubConfig_Idempotent - 같은 파라미터로 두 번 부르면 최종
// grub.cfg 내용이 동일해야 한다.
func TestRewriteGrubConfig_Idempotent(t *testing.T) {
	mount := t.TempDir()
	params := GrubParams{
		Model:         "SA6400",
		DSMVersion:    "7.4.1-90080",
		KernelCmdline: "x=1 y=2",
		Microcode:     true,
	}
	if err := RewriteGrubConfig(mount, params); err != nil {
		t.Fatal(err)
	}
	first := readGrub(t, mount)
	if err := RewriteGrubConfig(mount, params); err != nil {
		t.Fatal(err)
	}
	second := readGrub(t, mount)
	if first != second {
		t.Errorf("idempotent 아님\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestRestoreGrubBackup: whether restoring the backup puts the original back
// exactly.
//
// TestRestoreGrubBackup - 백업 복원이 정확히 원본을 되돌리는지 본다.
func TestRestoreGrubBackup(t *testing.T) {
	mount := t.TempDir()
	dir := filepath.Join(mount, "boot", "grub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("# bootstrap only\n")
	target := filepath.Join(dir, "grub.cfg")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RewriteGrubConfig(mount, GrubParams{
		Model: "SA6400", DSMVersion: "7.4.1", KernelCmdline: "x=1",
	}); err != nil {
		t.Fatal(err)
	}
	// Confirm it was rewritten.
	// 재작성됐음을 확인한다.
	if body := readGrub(t, mount); !strings.Contains(body, "--id dsm") {
		t.Fatalf("재작성이 안 됨\n%s", body)
	}

	if err := RestoreGrubBackup(mount); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("복원 결과가 원본과 다름\nwant: %q\ngot:  %q", original, got)
	}
}

// TestRewriteGrubConfig_EmptyMountRejected: so that bad input does not quietly
// write to some strange path.
//
// TestRewriteGrubConfig_EmptyMountRejected - 잘못된 입력에 대해 조용히
// 이상한 경로에 쓰지 않게 한다.
func TestRewriteGrubConfig_EmptyMountRejected(t *testing.T) {
	if err := RewriteGrubConfig("", GrubParams{Model: "x"}); err == nil {
		t.Error("빈 마운트 경로가 성공하면 안 됨")
	}
}
