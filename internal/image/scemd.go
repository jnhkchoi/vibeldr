// scemd.go pulls Synology's own extractor (scemd) and the files it needs out of
// a seed DSM, in the shape that goes onto the boot image.
//
// It happens on the developer's machine because the boot environment is an
// initramfs and nothing else - the worst possible place to be, with no network
// and no libc. Rather than download and extract hundreds of megabytes there,
// this extracts once and puts the result on the image, and the booted machine
// just uses it.
//
// What is taken:
//   - usr/syno/bin/scemd
//   - every file directly under usr/lib. scemd looks its crypto libraries up by
//     name at run time, so the ELF dependencies do not say what it will need.
//     Subdirectories are charset conversion tables and are left out.
//   - the dynamic linker PT_INTERP points at. The kernel opens that absolute
//     path itself when it execs, so it has to be at exactly that path.
//
// scemd.go - 시놀로지 자체 추출기 (scemd) 와 그것이 필요로 하는 파일들을
// 씨앗 DSM 에서 뽑아 부트 이미지에 실을 형태로 돌려준다.
//
// 이 작업을 개발자 머신에서 하는 이유: 부트 환경은 initramfs 하나뿐이라
// 네트워크도 libc 도 없는 가장 열악한 자리다. 거기서 수백 MB 를 받아
// 추출하는 대신, 여기서 한 번 뽑아 이미지에 실어 두면 부팅한 기계는 바로
// 쓴다.
//
// 뽑는 것:
//   - usr/syno/bin/scemd
//   - usr/lib 바로 아래의 파일 전부. scemd 는 암호 라이브러리를 실행 중에
//     이름으로 찾으므로, ELF 의존성만으로는 무엇이 필요할지 알 수 없다.
//     하위 디렉터리는 문자셋 변환 테이블이라 뺀다.
//   - PT_INTERP 가 가리키는 동적 링커. 커널이 exec 할 때 그 절대 경로를 직접
//     열므로 정확히 그 경로에 있어야 한다.

package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"vibeldr/internal/catalog"
	"vibeldr/internal/lzma"
	"vibeldr/internal/ramdisk"
)

// CachedScemdBundleName is where the extraction result is kept inside cacheDir.
// CachedScemdBundleName - cacheDir 안의 추출 결과 보관 파일.
const CachedScemdBundleName = "scemd-bundle.cpio"

// FetchScemdBundle obtains the scemd bundle from a seed DSM. The key is the
// path it will sit at inside the image, the value is the content. With a cache
// present it does not touch the network.
//
// FetchScemdBundle - 씨앗 DSM 에서 scemd 묶음을 확보한다.
//
// 키는 이미지 안에 놓일 경로, 값은 내용. 캐시가 있으면 네트워크를 쓰지
// 않는다.
func FetchScemdBundle(cacheDir string) (map[string][]byte, error) {
	return FetchScemdBundleFromSeeds(context.Background(), cacheDir, catalog.ScemdSeeds, http.DefaultClient)
}

// FetchScemdBundleFromSeeds is the test hook, complete with httptest.
// FetchScemdBundleFromSeeds - 테스트 훅. httptest 로 완결된다.
func FetchScemdBundleFromSeeds(ctx context.Context, cacheDir string, seeds []catalog.ScemdSeed, client *http.Client) (map[string][]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if len(seeds) == 0 {
		return nil, errors.New("scemd 씨앗 후보가 비어 있음")
	}

	var cached string
	if cacheDir != "" {
		cached = filepath.Join(cacheDir, CachedScemdBundleName)
		if blob, err := os.ReadFile(cached); err == nil {
			if files, err := unpackBundle(blob); err == nil {
				return files, nil
			}
		}
	}

	var lastErr error
	for _, seed := range seeds {
		files, err := fetchScemdFromSeed(ctx, client, seed)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", seed.Label, err)
			continue
		}
		if cached != "" {
			if blob, err := packBundle(files); err == nil {
				_ = os.MkdirAll(cacheDir, 0o755)
				_ = os.WriteFile(cached, blob, 0o644)
			}
		}
		return files, nil
	}
	return nil, fmt.Errorf("모든 씨앗에서 scemd 를 얻지 못함: %w", lastErr)
}

// fetchScemdFromSeed downloads one seed and pulls the needed files out of it.
// fetchScemdFromSeed - 씨앗 하나를 받아 필요한 파일들을 뽑는다.
func fetchScemdFromSeed(ctx context.Context, client *http.Client, seed catalog.ScemdSeed) (map[string][]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, seed.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}

	// A .pat is a plain tar; only rd.gz is taken from it into memory.
	// .pat 은 평문 tar. 그 안에서 rd.gz 하나만 메모리로 담는다.
	var rdgz []byte
	tr := tar.NewReader(resp.Body)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w (평문 tar 가 아닐 수 있음)", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != "rd.gz" {
			continue
		}
		rdgz, err = io.ReadAll(io.LimitReader(tr, 256*1024*1024))
		if err != nil {
			return nil, err
		}
		break
	}
	if len(rdgz) == 0 {
		return nil, errors.New("tar 안에 rd.gz 가 없음")
	}

	arc, err := readRamdisk(rdgz)
	if err != nil {
		return nil, err
	}
	return collectScemdBundle(arc)
}

// readRamdisk decompresses rd.gz and parses it as cpio. Despite the name the
// compression is usually LZMA1 alone (0x5d); it can also be gzip, or already
// decompressed.
//
// readRamdisk - rd.gz 를 풀어 cpio 로 파싱한다. 이름과 달리 압축은 LZMA1
// alone (0x5d) 인 경우가 많고, gzip 이거나 이미 풀려 있기도 하다.
func readRamdisk(rdgz []byte) (*ramdisk.Archive, error) {
	if len(rdgz) < 4 {
		return nil, errors.New("rd.gz 가 너무 짧음")
	}
	var raw []byte
	switch {
	case rdgz[0] == 0x5d:
		dec, err := lzma.Decode(rdgz)
		if err != nil {
			return nil, fmt.Errorf("lzma: %w", err)
		}
		raw = dec
	case rdgz[0] == 0x1f && rdgz[1] == 0x8b:
		gz, err := gzip.NewReader(bytes.NewReader(rdgz))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		dec, err := io.ReadAll(gz)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		raw = dec
	case rdgz[0] == 0x30 && rdgz[1] == 0x37:
		raw = rdgz
	default:
		return nil, fmt.Errorf("rd.gz 압축 형식 미지원 (%x)", rdgz[:4])
	}
	return ramdisk.ReadCPIO(raw)
}

// collectScemdBundle picks the files to put on the image out of the ramdisk.
// collectScemdBundle - 램디스크에서 이미지에 실을 파일들을 고른다.
func collectScemdBundle(arc *ramdisk.Archive) (map[string][]byte, error) {
	scemd, ok := resolvePath(arc, catalog.ScemdMemberPath)
	if !ok {
		return nil, fmt.Errorf("%s 가 없음", catalog.ScemdMemberPath)
	}

	// Put it under the name that makes it behave as the extractor.
	// 추출 동작을 하는 이름으로 놓는다.
	out := map[string][]byte{catalog.ScemdBinPath: scemd}

	// Every file directly under usr/lib. A symlink is filled in with the
	// content it points at, so the name alone finds it without having to
	// recreate links inside the image.
	//
	// usr/lib 직속 파일 전부. 심볼릭 링크는 가리키는 실제 내용으로 채운다 -
	// 이미지 안에서 링크를 다시 만들지 않아도 이름만으로 찾아지게.
	prefix := catalog.ScemdBundleDir + "/"
	for _, e := range arc.Entries {
		name := strings.TrimPrefix(e.Name, "./")
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if strings.Contains(name[len(prefix):], "/") {
			continue // subdirectories (charset tables and so on) are left out / 하위 디렉터리 (문자셋 테이블 등) 는 제외
		}
		if e.IsDir() {
			continue
		}
		if data, ok := resolvePath(arc, name); ok && len(data) > 0 {
			out[name] = data
		}
	}

	// Put the dynamic linker at the absolute path PT_INTERP names.
	// 동적 링커를 PT_INTERP 가 말하는 절대 경로에 놓는다.
	interp := elfInterp(scemd)
	if interp == "" {
		return nil, errors.New("scemd 에 PT_INTERP 가 없음 (동적 링커를 특정할 수 없음)")
	}
	linker, ok := resolvePath(arc, strings.TrimPrefix(interp, "/"))
	if !ok {
		return nil, fmt.Errorf("동적 링커 %s 를 램디스크에서 찾지 못함", interp)
	}
	out[strings.TrimPrefix(interp, "/")] = linker
	return out, nil
}

// resolvePath follows a path through the archive to the actual content.
//
// A symlink can sit anywhere along the path, not only at the end. A DSM ramdisk
// chains them two deep - lib64 -> usr/lib, ld-linux-x86-64.so.2 -> ld-2.26.so -
// so following only one step never reaches the real file.
//
// resolvePath - 아카이브 안에서 경로를 따라가 실제 내용을 얻는다.
//
// 경로 중간과 끝 어디든 심볼릭 링크가 있을 수 있다. DSM 램디스크는
// lib64 -> usr/lib, ld-linux-x86-64.so.2 -> ld-2.26.so 처럼 두 단계로
// 걸어 두기 때문에, 한 단계만 보면 실제 파일에 닿지 못한다.
func resolvePath(arc *ramdisk.Archive, name string) ([]byte, bool) {
	index := map[string]*ramdisk.Entry{}
	for i := range arc.Entries {
		index[strings.TrimPrefix(arc.Entries[i].Name, "./")] = &arc.Entries[i]
	}
	return resolveWith(index, name, 0)
}

// resolveWith is the recursive half of resolvePath, working off a prebuilt index.
// resolveWith - resolvePath 의 재귀 부분. 미리 만든 색인 위에서 돈다.
func resolveWith(index map[string]*ramdisk.Entry, name string, depth int) ([]byte, bool) {
	if depth > 16 {
		return nil, false // a loop / 순환
	}
	name = path.Clean(strings.TrimPrefix(name, "/"))
	if e, ok := index[name]; ok {
		if e.Mode&ramdisk.ModeFileTypeMask == ramdisk.ModeSymlink {
			target := string(bytes.TrimRight(e.Data, "\x00"))
			if !strings.HasPrefix(target, "/") {
				target = path.Join(path.Dir(name), target)
			}
			return resolveWith(index, target, depth+1)
		}
		return e.Data, true
	}
	// Not there under that name. Walk the leading path components from the
	// front to see whether one of them is a link.
	//
	// 이름 그대로는 없다. 상위 경로 중 링크가 있는지 앞에서부터 확인한다.
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		head := strings.Join(parts[:i], "/")
		e, ok := index[head]
		if !ok || e.Mode&ramdisk.ModeFileTypeMask != ramdisk.ModeSymlink {
			continue
		}
		target := string(bytes.TrimRight(e.Data, "\x00"))
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(head), target)
		}
		rest := strings.Join(parts[i:], "/")
		return resolveWith(index, path.Join(target, rest), depth+1)
	}
	return nil, false
}

// elfInterp returns an ELF's PT_INTERP, the absolute path of its dynamic linker.
// elfInterp - ELF 의 PT_INTERP (동적 링커 절대 경로).
func elfInterp(bin []byte) string {
	f, err := elf.NewFile(bytes.NewReader(bin))
	if err != nil {
		return ""
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		raw, err := io.ReadAll(p.Open())
		if err != nil {
			return ""
		}
		return string(bytes.TrimRight(raw, "\x00"))
	}
	return ""
}

// packBundle and unpackBundle are the cache file format. Keeping the files in
// one blob means a cache can never be left half written.
//
// packBundle / unpackBundle - 캐시 파일 형식. 파일 여러 개를 한 덩어리로
// 두면 부분적으로만 남은 캐시가 생기지 않는다.
func packBundle(files map[string][]byte) ([]byte, error) {
	a := ramdisk.NewArchive()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := a.Add(n, ramdisk.ModeRegular|0o755, files[n]); err != nil {
			return nil, err
		}
	}
	return a.Bytes()
}

func unpackBundle(blob []byte) (map[string][]byte, error) {
	arc, err := ramdisk.ReadCPIO(blob)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range arc.Entries {
		if e.IsRegular() {
			out[strings.TrimPrefix(e.Name, "./")] = e.Data
		}
	}
	if len(out[catalog.ScemdBinPath]) == 0 {
		return nil, errors.New("캐시에 scemd 가 없음")
	}
	return out, nil
}
