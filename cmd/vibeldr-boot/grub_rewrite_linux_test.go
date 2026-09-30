//go:build linux

// grub_rewrite_linux_test.go is the unit test for rewriting partition 1's
// grub.cfg. It verifies the rendered result, the backup and idempotency
// against a fake mount directory. It never starts a real GRUB.
//
// grub_rewrite_linux_test.go - 파티션 1 grub.cfg 재작성 유닛 테스트.
// fake mount 디렉터리에 대고 렌더 결과 / 백업 / idempotency 를 검증한다.
// 실제 GRUB 을 띄우지는 않는다.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibeldr/internal/synoboot"
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
		t.Errorf("reconfigure entry missing\n%s", body)
	}
	if !strings.Contains(body, "--label VIBELDR1") {
		t.Errorf("reconfigure must pick up the partition 1 label\n%s", body)
	}
	if !strings.Contains(body, "--id dsm") || !strings.Contains(body, "--id junior") {
		t.Errorf("dsm / junior entries missing\n%s", body)
	}
}

// TestRewriteGrubConfig_NextBootFile: grub.cfg sources the next-boot script
// from partition 1 after `set default=dsm`, so that the script wins; the
// script is created idle; and no `[` test is used, which the BIOS GRUB here
// cannot run.
//
// TestRewriteGrubConfig_NextBootFile - grub.cfg 가 `set default=dsm` 뒤에서
// 파티션 1 의 다음 부팅 스크립트를 source 해 스크립트가 이기고, 스크립트는 평소
// 내용으로 만들어지며, 이 BIOS GRUB 이 돌리지 못하는 `[` 검사를 쓰지 않는다.
func TestRewriteGrubConfig_NextBootFile(t *testing.T) {
	mount := t.TempDir()
	if err := RewriteGrubConfig(mount, GrubParams{Model: "DS918+", DSMVersion: "7.4.1-90080"}); err != nil {
		t.Fatal(err)
	}
	body := readGrub(t, mount)
	src := "search --set=vibeldr_p1 --label VIBELDR1 --no-floppy\nsource ($vibeldr_p1)/" + synoboot.NextBootFile + "\n"
	i := strings.Index(body, src)
	if i < 0 {
		t.Fatalf("next-boot source missing\n%s", body)
	}
	if d := strings.Index(body, "set default=dsm"); d < 0 || d > i {
		t.Errorf("set default=dsm must come before the source\n%s", body)
	}
	if strings.Contains(body, "[ ") || strings.Contains(body, "insmod test") {
		t.Errorf("grub.cfg uses test\n%s", body)
	}
	raw, err := os.ReadFile(filepath.Join(mount, synoboot.NextBootFile))
	if err != nil || string(raw) != synoboot.NextBootIdle {
		t.Errorf("%s = %q, %v; want the idle content", synoboot.NextBootFile, raw, err)
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
		t.Fatalf("backup file missing: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("backup differs from the original\nwant: %q\ngot:  %q", original, got)
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
		t.Errorf("second call overwrote the backup\nwant: %q\ngot:  %q", original, got2)
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
		t.Errorf("not idempotent\nfirst:\n%s\nsecond:\n%s", first, second)
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
		t.Fatalf("not rewritten\n%s", body)
	}

	if err := RestoreGrubBackup(mount); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("restore result differs from the original\nwant: %q\ngot:  %q", original, got)
	}
}

// TestRewriteGrubConfig_EmptyMountRejected: so that bad input does not quietly
// write to some strange path.
//
// TestRewriteGrubConfig_EmptyMountRejected - 잘못된 입력에 대해 조용히
// 이상한 경로에 쓰지 않게 한다.
func TestRewriteGrubConfig_EmptyMountRejected(t *testing.T) {
	if err := RewriteGrubConfig("", GrubParams{Model: "x"}); err == nil {
		t.Error("an empty mount path must not succeed")
	}
}

// TestRewriteGrubConfig_NoEFIFlag: the DSM kernel lines carry neither withefi
// nor noefi, and the config does not look at grub_platform, which would take a
// `[` test the BIOS GRUB here cannot run.
//
// TestRewriteGrubConfig_NoEFIFlag - DSM 커널 줄에 withefi 도 noefi 도 없고,
// 설정이 grub_platform 을 보지 않는다. 그것을 보려면 이 BIOS GRUB 이 돌리지 못하는
// `[` 검사가 필요하다.
func TestRewriteGrubConfig_NoEFIFlag(t *testing.T) {
	mount := t.TempDir()
	if err := RewriteGrubConfig(mount, GrubParams{Model: "DS918+", DSMVersion: "7.4.1-90080", KernelCmdline: "x=1"}); err != nil {
		t.Fatal(err)
	}
	body := readGrub(t, mount)
	for _, bad := range []string{"withefi", "noefi", "grub_platform"} {
		if strings.Contains(body, bad) {
			t.Errorf("grub.cfg has %q\n%s", bad, body)
		}
	}
	for _, want := range []string{"linux /zImage-dsm x=1\n", "linux /zImage-dsm x=1 force_junior\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("grub.cfg missing %q\n%s", want, body)
		}
	}
}
