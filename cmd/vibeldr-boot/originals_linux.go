//go:build linux

// originals_linux.go takes Synology's own modules out of the unpacked .pat and
// merges them into the driver pack.
//
// The published pack holds our builds only. The .pat's hda1.tgz is DSM's root
// filesystem, and its usr/lib/modules holds the few hundred modules Synology
// built itself. They go into the pack here (kmod.MergeOriginals), so the pack
// on partition 4 is the one the modules were checked against: with a module in
// both, the loader prefers Synology's.
//
// Despite the name, hda1.tgz is xz, not gzip. It is opened with the xz that
// came on the image, streamed straight into the tar reader.
//
// originals_linux.go - 풀어 놓은 .pat 에서 시놀로지 자신의 모듈을 꺼내
// 드라이버 팩에 합친다.
//
// 공개하는 팩에는 우리 빌드만 있다. .pat 의 hda1.tgz 는 DSM 의 루트 파일시스템이고,
// 그 usr/lib/modules 에 시놀로지가 직접 빌드한 모듈 수백 개가 있다. 그것을 여기서
// 팩에 넣는다 (kmod.MergeOriginals). 그러면 파티션 4 의 팩이 모듈을 검사한 그
// 팩이 된다 - 같은 모듈이 둘 다 있으면 로더가 시놀 것을 고른다.
//
// 이름과 달리 hda1.tgz 는 gzip 이 아니라 xz 다. 이미지에 실려 온 xz 로 열어
// tar 리더로 바로 흘려 넣는다.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"vibeldr/internal/kmod"
	"vibeldr/internal/ui"
)

// hdaName is the .pat member holding DSM's root filesystem.
// hdaName - DSM 루트 파일시스템을 담은 .pat 멤버.
const hdaName = "hda1.tgz"

// hdaModuleDir is where the modules sit inside it.
// hdaModuleDir - 그 안에서 모듈이 있는 자리.
const hdaModuleDir = "usr/lib/modules/"

// mergePatOriginals returns pack with the originals from the .pat unpacked in
// dsmDir merged in. Without hda1.tgz, or when it cannot be read, pack comes
// back as it was: the install goes on with our builds alone.
//
// It clears st.packFromDisk when the merge changed the pack, so the new pack
// is written to partition 4.
//
// mergePatOriginals - dsmDir 에 풀린 .pat 의 원본을 합친 팩을 돌려준다.
// hda1.tgz 가 없거나 못 읽으면 pack 을 그대로 돌려준다 - 설치는 우리 빌드만으로
// 계속한다.
//
// 합쳐서 팩이 바뀌면 st.packFromDisk 를 꺼서 새 팩이 파티션 4 에 쓰이게 한다.
func mergePatOriginals(st *tuiState, pack kmod.Pack, dsmDir string) kmod.Pack {
	if len(pack) == 0 {
		return pack
	}
	hda := filepath.Join(dsmDir, hdaName)
	if _, err := os.Stat(hda); err != nil {
		ui.Warn("%s 가 없어 시놀 원본 없이 우리 빌드만 씀", hdaName)
		return pack
	}
	orig, err := originalsFromHDA(hda, xzPath, xzLibDir)
	if err != nil {
		ui.Warn("시놀 원본을 못 읽음 (%v) - 우리 빌드만 씀", err)
		return pack
	}
	// Nothing after this reads hda1.tgz, and it is DSM's whole root filesystem,
	// a few hundred MB of the RAM the install runs in.
	//
	// 이 뒤로는 hda1.tgz 를 읽는 곳이 없고, 이것은 DSM 루트 파일시스템 전체라 설치가
	// 도는 RAM 의 수백 MB 를 차지한다.
	if err := os.Remove(hda); err != nil {
		ui.Warn("%s 지우기: %v", hdaName, err)
	}
	merged, rep, err := kmod.MergeOriginals(pack, orig)
	if err != nil {
		ui.Warn("시놀 원본을 못 합침 (%v) - 우리 빌드만 씀", err)
		return pack
	}
	ui.OK("시놀 원본 %d 개 중 교체 %d / 추가 %d / 우리 것 유지 %d / 커널 다름 %d",
		len(orig), len(rep.Replaced), len(rep.Added), len(rep.Kept), len(rep.WrongRelease))
	if !samePack(pack, merged) {
		st.packFromDisk = false
	}
	return merged
}

// originalsFromHDA reads every .ko under usr/lib/modules out of hda1.tgz,
// keyed by base name.
//
// originalsFromHDA - hda1.tgz 의 usr/lib/modules 아래 .ko 를 전부 읽는다.
// 키는 경로 없는 파일 이름이다.
func originalsFromHDA(hda, xz, xzLib string) (kmod.Pack, error) {
	f, err := os.Open(hda)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var magic [6]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	var r io.Reader
	var wait func() error
	switch {
	case bytes.Equal(magic[:], []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		if _, err := os.Stat(xz); err != nil {
			return nil, fmt.Errorf("xz 가 이미지에 없음 (%s): %w", xz, err)
		}
		cmd := exec.Command(xz, "-dc")
		cmd.Stdin = f
		if xzLib != "" {
			cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+xzLib)
		}
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("xz 실행: %w", err)
		}
		r = out
		wait = func() error {
			if err := cmd.Wait(); err != nil {
				return fmt.Errorf("xz: %w (%s)", err, strings.TrimSpace(errBuf.String()))
			}
			return nil
		}
	case magic[0] == 0x1f && magic[1] == 0x8b:
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	default:
		return nil, fmt.Errorf("%s 가 xz 도 gzip 도 아님", hdaName)
	}

	pack := kmod.Pack{}
	tr := tar.NewReader(r)
	var terr error
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			terr = fmt.Errorf("tar: %w", err)
			break
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(name, hdaModuleDir) || !strings.HasSuffix(name, ".ko") {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			terr = fmt.Errorf("%s: %w", name, err)
			break
		}
		pack[path.Base(name)] = data
	}
	// Drain what xz still writes, every time. The tar reader stops at the end
	// marker, and whatever follows it (the padding up to the archive's record
	// size, or anything after a stop on error) stays in the pipe. More than a
	// pipe holds and xz blocks writing while we wait for it to exit.
	//
	// xz 가 아직 쓰는 것을 매번 끝까지 비운다. tar 리더는 끝 표시에서 멈추고, 그
	// 뒤에 오는 것(아카이브 레코드 크기까지의 패딩, 또는 오류로 멈춘 뒤의 나머지)은
	// 파이프에 남는다. 파이프 하나를 넘으면 xz 는 쓰기에서 막히고 우리는 xz 가
	// 끝나기를 기다리며 서로 멈춘다.
	_, _ = io.Copy(io.Discard, r)
	if wait != nil {
		if err := wait(); err != nil && terr == nil {
			terr = err
		}
	}
	if terr != nil {
		return nil, terr
	}
	if len(pack) == 0 {
		return nil, fmt.Errorf("%s 의 %s 에 .ko 가 없음", hdaName, hdaModuleDir)
	}
	return pack, nil
}

// samePack reports whether a and b hold the same entries with the same bytes.
// samePack - a 와 b 가 같은 항목을 같은 내용으로 갖는지.
func samePack(a, b kmod.Pack) bool {
	if len(a) != len(b) {
		return false
	}
	for name, data := range a {
		if other, ok := b[name]; !ok || !bytes.Equal(data, other) {
			return false
		}
	}
	return true
}
