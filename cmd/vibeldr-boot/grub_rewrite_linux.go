//go:build linux

// grub_rewrite_linux.go rewrites partition 1's grub.cfg from the bootstrap's
// entries into the DSM ones (dsm / junior / reconfigure).
//
// The bootstrap image's grub.cfg knows only vibeldr-boot. Once the fetch and
// build pipeline has put zImage-dsm and initrd-dsm on partition 3, this rewrites
// grub.cfg so that DSM comes up by default from the next boot.
//
// After the rewrite the bootstrap's Alpine kernel and initrd are still on
// partition 1 (`vmlinuz`, `initrd-vibeldr`), so a "reconfigure" entry always
// goes out alongside. Choosing it from the GRUB menu boots the Alpine kernel
// again and lands in the TUI, where the model and version can be changed.
// Without it there is no way to rebuild once a build has happened, short of
// wiping the stored DSM entirely.
//
// cmd/vibeldr's image builder has a similar renderer (grubConfig), but that one
// runs while the image is first being created and carries other conditions with
// it - overriding the identity variables, laying cmdline.txt down at the same
// time. Here the identity is already settled and the command line arrives whole,
// so this is written separately.
//
// grub_rewrite_linux.go - 파티션 1 의 grub.cfg 를 부트스트랩의 엔트리에서
// DSM 용 엔트리 (dsm / junior / reconfigure) 로 재작성한다.
//
// 부트스트랩 이미지의 grub.cfg 는 vibeldr-boot 만 안다. Fetch and build
// 파이프라인이 파티션 3 에 zImage-dsm / initrd-dsm 을 심고 나면, 다음
// 부팅부터 DSM 이 기본으로 뜨도록 이 함수가 grub.cfg 를 다시 쓴다.
//
// 재작성 뒤에도 부트스트랩의 알파인 커널·initrd 는 파티션 1 에 그대로
// 남아 있다 (`vmlinuz`, `initrd-vibeldr`). 그래서 "reconfigure" 엔트리를
// 항상 함께 낸다. 사용자가 GRUB 메뉴에서 그걸 고르면 알파인 커널로 다시
// 부팅해 TUI 로 들어가 모델·버전을 바꿀 수 있다. 이게 없으면 한 번 빌드된
// 뒤에는 재빌드가 불가능해 저장된 DSM 을 통째로 갈아엎는 수밖에 없다.
//
// cmd/vibeldr 의 이미지 빌더에도 유사한 렌더러 (grubConfig) 가 있지만,
// 저쪽은 최초 이미지 생성 컨텍스트라 identity 변수 override, cmdline.txt
// 동시 배치 등 다른 조건이 함께 걸린다. 여기 재작성 컨텍스트는 identity 가
// 이미 확정된 상태로 커맨드라인이 통째로 넘어오므로 별개로 새로 쓴다.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"vibeldr/internal/image"
	"vibeldr/internal/synoboot"
)

// grubConfigRelPath is grub.cfg's path relative to the mounted partition 1.
// grubConfigRelPath - 마운트된 파티션 1 안 grub.cfg 의 상대 경로.
const grubConfigRelPath = "boot/grub/grub.cfg"

// grubBootstrapBackupSuffix is the suffix the original (bootstrap) grub.cfg is
// kept under on the first rewrite, so a reinstall or a rollback can go back to it.
//
// grubBootstrapBackupSuffix - 첫 재작성 시 원본 (부트스트랩) grub.cfg 를
// 이 접미사가 붙은 이름으로 보존한다. 재설치나 롤백 때 참고할 수 있다.
const grubBootstrapBackupSuffix = ".bootstrap.bak"

// GrubParams are the render parameters passed to RewriteGrubConfig.
// GrubParams - RewriteGrubConfig 에 넘길 렌더 파라미터.
type GrubParams struct {
	// Model is the Synology model being imitated (SA6400, say), shown on the
	// menu label.
	//
	// Model - 흉내내는 시놀로지 모델 (예: SA6400). 메뉴 라벨에 표시.
	Model string
	// DSMVersion is the DSM release (7.4.1-90080, say), shown on the menu label.
	// DSMVersion - DSM 릴리스 (예: 7.4.1-90080). 메뉴 라벨에 표시.
	DSMVersion string
	// KernelCmdline is the final kernel command line, already rendered.
	// KernelCmdline - 이미 렌더된 최종 커널 커맨드라인 문자열.
	KernelCmdline string
}

// RewriteGrubConfig rewrites the grub.cfg inside the mounted partition 1 into
// the dsm / reconfigure / reconfigure-text / junior entries.
//
// The first call backs the existing grub.cfg up as <grub.cfg>.bootstrap.bak.
// Later calls do not overwrite that backup, so the reinstall original stays
// intact. The write goes to <path>.new, is fsynced and then renamed, so it is
// atomic.
//
// RewriteGrubConfig - 파티션 1 마운트 안의 grub.cfg 를 dsm / reconfigure /
// reconfigure-text / junior 엔트리로 재작성한다.
//
// 첫 호출은 기존 grub.cfg 를 <grub.cfg>.bootstrap.bak 로 백업한다. 이후
// 호출은 백업을 덮어쓰지 않아 재설치 원본이 그대로 남는다. 쓰기는
// <path>.new 로 쓰고 fsync 뒤 rename 하는 원자적 방식이다.
func RewriteGrubConfig(mountPath string, params GrubParams) error {
	if mountPath == "" {
		return fmt.Errorf("grub rewrite: empty mount path")
	}
	target := filepath.Join(mountPath, grubConfigRelPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("grub rewrite: mkdir %s: %w", filepath.Dir(target), err)
	}

	// The backup is only made on the first rewrite. Overwriting it on a later
	// call would lose the bootstrap original, so its existence is the guard.
	//
	// 백업은 첫 재작성에서만 만든다. 두 번째 이후 호출에서 백업을 덮으면
	// 부트스트랩 원본이 사라지므로 존재 여부로 가드한다.
	backup := target + grubBootstrapBackupSuffix
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if src, err := os.ReadFile(target); err == nil {
			if err := writeAtomic(backup, src, 0o644); err != nil {
				return fmt.Errorf("grub rewrite: backup: %w", err)
			}
		}
	}

	rendered := renderGrubConfig(params)
	if err := writeAtomic(target, rendered, 0o644); err != nil {
		return fmt.Errorf("grub rewrite: write %s: %w", target, err)
	}
	// The script the new grub.cfg sources has to exist.
	// 새 grub.cfg 가 source 하는 스크립트가 있어야 한다.
	next := filepath.Join(mountPath, synoboot.NextBootFile)
	if _, err := os.Stat(next); os.IsNotExist(err) {
		if err := writeAtomic(next, []byte(synoboot.NextBootIdle), 0o644); err != nil {
			return fmt.Errorf("grub rewrite: %s: %w", synoboot.NextBootFile, err)
		}
	}
	return nil
}

// resetNextBoot puts synoboot.NextBootIdle back into synoboot.NextBootFile on
// partition 1, so a boot DSM asked to open the loader is followed by DSM
// again. It writes only when the content differs.
//
// resetNextBoot - 파티션 1 의 synoboot.NextBootFile 에 synoboot.NextBootIdle 을
// 되돌려 놓는다. 그래서 DSM 이 로더를 열어 달라고 한 부팅 다음에는 다시 DSM
// 이다. 내용이 다를 때만 쓴다.
func resetNextBoot() {
	path := filepath.Join(loaderMount, synoboot.NextBootFile)
	if raw, err := os.ReadFile(path); err == nil && string(raw) == synoboot.NextBootIdle {
		return
	}
	if err := writeAtomic(path, []byte(synoboot.NextBootIdle), 0o644); err != nil {
		say("loader disk: %s: %v", synoboot.NextBootFile, err)
		return
	}
	say("loader disk: %s reset", synoboot.NextBootFile)
}

// RestoreGrubBackup puts the bootstrap backup back as grub.cfg. It is called
// when a rewrite failed part way through, or when the user wants a rollback.
//
// RestoreGrubBackup - 부트스트랩 백업을 grub.cfg 로 되돌린다. 재작성 도중
// 실패했거나 사용자가 롤백을 원할 때 부른다.
func RestoreGrubBackup(mountPath string) error {
	if mountPath == "" {
		return fmt.Errorf("grub restore: empty mount path")
	}
	target := filepath.Join(mountPath, grubConfigRelPath)
	backup := target + grubBootstrapBackupSuffix
	src, err := os.ReadFile(backup)
	if err != nil {
		return fmt.Errorf("grub restore: read backup: %w", err)
	}
	if err := writeAtomic(target, src, 0o644); err != nil {
		return fmt.Errorf("grub restore: %w", err)
	}
	return nil
}

// renderGrubConfig builds the body of the multi-entry grub.cfg.
//
// The partition is found by label (VIBELDR3). Using the label rather than a
// disk number means the same files boot whichever bus the loader is on - SATA,
// USB or NVMe.
//
// renderGrubConfig - 다중 엔트리 grub.cfg 본문 문자열을 만든다.
//
// 파티션은 라벨 (VIBELDR3) 로 찾는다. 디스크 번호가 아니라 라벨을 쓰므로
// SATA/USB/NVMe 어느 버스에 붙어도 같은 파일이 부팅된다.
func renderGrubConfig(params GrubParams) []byte {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	model := strings.TrimSpace(params.Model)
	if model == "" {
		model = "DSM"
	}
	version := strings.TrimSpace(params.DSMVersion)
	if version == "" {
		version = "unknown"
	}
	kernelCmdline := strings.TrimSpace(params.KernelCmdline)

	dsmInitrd := "/initrd-dsm"

	w("# grub.cfg rewritten by vibeldr-boot after it updated partition 3.")
	w("# The bootstrap original is kept in grub.cfg%s.", grubBootstrapBackupSuffix)
	w("set default=dsm")
	w("set timeout=5")
	w("")
	w("insmod part_msdos")
	w("insmod fat")
	w("insmod search_label")
	w("insmod all_video")
	w("insmod gfxterm")
	w("")
	// The installer screen the reconfigure entry brings up is drawn on the
	// framebuffer too, so the graphics mode is set and handed to the kernel
	// the same way the bootstrap does it.
	//
	// reconfigure 엔트리가 띄우는 설치 화면도 프레임버퍼에 그린다.
	// 부트스트랩과 같은 방식으로 그래픽 모드를 세워 커널에 넘긴다.
	w("set gfxmode=%s", image.BootstrapGfxMode)
	w("set gfxpayload=keep")
	w("terminal_output --append gfxterm")
	w("")
	w("if serial --unit=0 --speed=115200 --word=8 --parity=no --stop=1; then")
	w("  terminal_input --append serial")
	w("  terminal_output --append serial")
	w("fi")
	w("")
	// The fallback uses search's own result rather than `[ -e ]`: the BIOS
	// GRUB core has no `test` command, and `[` fails there with "can't find
	// command".
	//
	// 폴백은 `[ -e ]` 가 아니라 search 자신의 결과를 쓴다. BIOS GRUB core 에는
	// `test` 명령이 없어 `[` 가 "can't find command" 로 실패한다.
	w("# The payload partition has label %s (partition 3).", imageLabels[2])
	w("# Fall back to the file name too, in case the label cannot be read.")
	w("if search --set=root --label %s --no-floppy; then", imageLabels[2])
	w("  set vibeldr_payload=label")
	w("else")
	w("  search --set=root --file /zImage-dsm --no-floppy")
	w("fi")
	w("")
	// synoboot.NextBootFile, on partition 1, may set the default to the
	// reconfigure entry for one boot (vibeldr-agent -next-boot-loader in DSM).
	// search sets only the named variable and leaves root alone.
	//
	// 파티션 1 의 synoboot.NextBootFile 이 한 번의 부팅 동안 기본값을
	// reconfigure 항목으로 바꿀 수 있다 (DSM 의 vibeldr-agent
	// -next-boot-loader). search 는 지정한 변수만 바꾸고 root 는 그대로 둔다.
	w("# 'vibeldr-agent -next-boot-loader' in DSM can open the loader once from here.")
	w("search --set=vibeldr_p1 --label %s --no-floppy", imageLabels[0])
	w("source ($vibeldr_p1)/%s", synoboot.NextBootFile)
	w("")

	w("menuentry 'DSM %s %s' --id dsm {", model, version)
	w("  echo 'Loading DSM kernel...'")
	if kernelCmdline != "" {
		w("  linux /zImage-dsm %s", kernelCmdline)
	} else {
		w("  linux /zImage-dsm")
	}
	w("  echo 'Loading DSM ramdisk...'")
	w("  initrd %s", dsmInitrd)
	// The Synology kernel writes nothing to the screen (VGA). The moment it
	// takes over, the display looks stuck and black at 'Booting the kernel.',
	// which reads as an install that has hung. This note is left right before
	// the handover so it stays on screen. The GRUB console font has no Korean
	// glyphs and Korean comes out as ??, so it is written in English.
	//
	// 시놀로지 커널은 화면(VGA)에 아무것도 못 쓴다. 커널로 넘어가는 순간
	// 화면이 'Booting the kernel.' 에서 검게 멈춘 것처럼 보여, 설치가 멈췄다고
	// 오해하기 쉽다. 넘어가기 직전 이 안내를 남겨 화면에 계속 보이게 한다.
	// GRUB 콘솔 폰트에는 한글 글리프가 없어 한글은 ?? 로 깨진다. 영어로 쓴다.
	w("  echo ''")
	w("  echo '=================================================================='")
	w("  echo ' Booting DSM now. The screen stays black from here - this is'")
	w("  echo ' normal. Synology kernels have no video console; DSM runs over'")
	w("  echo ' the network. From another PC on the same network, open:'")
	w("  echo '        http://find.synology.com'")
	w("  echo ' (or connect to this box IP on port 5000) to finish install.'")
	w("  echo '=================================================================='")
	w("}")
	w("")

	// The way back in to rebuild. The bootstrap's Alpine kernel and initrd are
	// still on partition 1, so root is pointed at partition 1 for a moment and
	// they are loaded from there. This entry is not the default, so it only
	// runs when the user picks it explicitly within the GRUB timeout - there is
	// no falling into it by accident.
	//
	// 재빌드 진입점 - 부트스트랩의 알파인 커널·initrd 는 파티션 1 에 그대로
	// 남아 있다. root 를 잠시 파티션 1 로 재지정해 그걸 로드한다. 이 엔트리는
	// default 가 아니므로 GRUB timeout 안에 사용자가 명시적으로 골라야
	// 실행된다 - 실수로 진입할 일이 없다.
	w("menuentry 'vibeldr - reconfigure (change model / DSM version)' --id reconfigure {")
	w("  # The Alpine kernel and initrd are on partition 1 (%s).", imageLabels[0])
	w("  search --set=root --label %s --no-floppy", imageLabels[0])
	w("  echo 'Loading generic kernel...'")
	w("  linux /vmlinuz console=ttyS0,115200n8 console=tty0 loglevel=4 panic=5")
	w("  echo 'Loading vibeldr-boot...'")
	w("  initrd /initrd-vibeldr")
	w("}")
	w("")
	w("menuentry 'vibeldr - reconfigure (text menu)' --id reconfigure-text {")
	w("  search --set=root --label %s --no-floppy", imageLabels[0])
	w("  linux /vmlinuz console=ttyS0,115200n8 console=tty0 loglevel=4 panic=5 %s", textMenuArg)
	w("  initrd /initrd-vibeldr")
	w("}")
	w("")

	w("menuentry 'DSM %s %s (reinstall)' --id junior {", model, version)
	w("  echo 'Loading DSM kernel...'")
	if kernelCmdline != "" {
		w("  linux /zImage-dsm %s force_junior", kernelCmdline)
	} else {
		w("  linux /zImage-dsm force_junior")
	}
	w("  echo 'Loading DSM ramdisk...'")
	w("  initrd %s", dsmInitrd)
	w("}")

	return []byte(b.String())
}

// writeAtomic writes to <path>.new, fsyncs and renames, so that a sudden power
// loss never leaves grub.cfg half written. The directory is fsynced too, to make
// the rename durable.
//
// writeAtomic - <path>.new 로 쓰고 fsync 뒤 rename 한다. 갑작스러운 전원
// 손실에도 grub.cfg 가 절반만 쓰인 상태가 되지 않게 하려는 것이다.
// 디렉터리도 fsync 해서 rename 을 영구화한다.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
