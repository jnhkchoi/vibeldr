// kernel_generic.go collects a generic Linux kernel for the bootstrap image.
//
// The sequence:
//   1) walk the mirror list. Fetch each mirror's APKINDEX.tar.gz and find the
//      linux-lts package record in the APKINDEX text inside to get the version.
//   2) download `<base>/linux-lts-<version>.apk`. An apk is a gzip tar.
//   3) extract the single file boot/vmlinuz-lts out of the tar into cacheDir.
//   4) later calls reuse the cached file.
//
// A failure moves on to the next mirror. If every mirror fails, the last error
// is wrapped and returned. The unit tests never reach a real CDN; they use a
// mock server.
//
// kernel_generic.go - 부트스트랩 이미지용 제네릭 리눅스 커널 수집.
//
// 순서:
//   1) 미러 목록을 돈다. 미러마다 APKINDEX.tar.gz 를 받아 안의 APKINDEX
//      텍스트에서 linux-lts 패키지 레코드를 찾아 버전을 얻는다.
//   2) `<base>/linux-lts-<version>.apk` 를 받는다. apk 는 gzip tar 다.
//   3) tar 에서 boot/vmlinuz-lts 한 파일만 cacheDir 에 풀어 둔다.
//   4) 이후 호출은 캐시된 파일을 다시 쓴다.
//
// 실패하면 다음 미러로 넘어간다. 모든 미러가 실패하면 마지막 에러를 감싸서
// 반환한다. 유닛테스트는 실제 CDN 에 가지 않고 mock 서버를 쓴다.

package image

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"vibeldr/internal/catalog"
)

// CachedGenericKernelName is the file name inside cacheDir. It stays the same
// whatever mirror or version was used, so a rebuild still hits the cache.
//
// CachedGenericKernelName - cacheDir 안 파일명. 미러/버전이 바뀌어도
// 파일명은 고정. 재빌드 캐시 히트를 유지하기 위함.
const CachedGenericKernelName = "vmlinuz-alpine-lts"

// ModuleFile is one kernel module to put on the bootstrap initrd. Name is the
// final path inside the initrd (for example
// "lib/modules/6.6.142-0-lts/kernel/drivers/virtio/virtio.ko"), and Data is the
// raw ELF (.ko), already decompressed.
//
// ModuleFile - 부트스트랩 initrd 에 실을 커널 모듈 하나. 이름은 initrd
// 안의 최종 경로이고, Data 는 이미 압축 해제된 raw ELF (.ko).
type ModuleFile struct {
	Name string
	Mode uint32
	Data []byte
}

// FetchGenericKernelAndModules obtains the Alpine LTS kernel together with the
// modules booting needs, putting the smallest set of drivers on the initrd that
// lets vibeldr-boot see disks and network on a VM (virtio) and on common
// physical hardware.
//
// The kernel is cached under CachedGenericKernelName. The modules are pulled
// out of the apk on every call: caching them would mean managing a much larger
// file, and reusing a cached apk would be the better optimisation anyway. So
// the apk is downloaded once into memory and used for both purposes.
//
// FetchGenericKernelAndModules - Alpine LTS 커널 + 부팅에 필수인 모듈들을
// 함께 확보. VM(virtio) 과 흔한 물리 하드웨어에서 vibeldr-boot 이 디스크와
// 네트워크를 볼 수 있게 최소한의 드라이버를 initrd 에 심는다.
//
// 커널은 CachedGenericKernelName 으로 캐시된다. 모듈은 매 호출마다 apk 에서
// 새로 뽑는다 - 캐시하려면 파일이 커져 관리가 번거롭고, 어차피 캐시할 값어치는
// apk 자체 쪽에 있다. 그래서 apk 를 메모리로 한 번 받아 두 목적에 다 쓴다.
func FetchGenericKernelAndModules(cacheDir string) (string, []ModuleFile, string, error) {
	return FetchKernelAndModulesFromMirrors(context.Background(), cacheDir, catalog.AlpineMirrors, http.DefaultClient)
}

// FetchKernelAndModulesFromMirrors is the test hook, complete with httptest.
// FetchKernelAndModulesFromMirrors - 테스트 훅. httptest 로 완결된다.
func FetchKernelAndModulesFromMirrors(ctx context.Context, cacheDir string, mirrors []catalog.AlpineMirror, client *http.Client) (string, []ModuleFile, string, error) {
	if cacheDir == "" {
		return "", nil, "", fmt.Errorf("빈 cacheDir 은 사용할 수 없음")
	}
	if client == nil {
		client = http.DefaultClient
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", nil, "", fmt.Errorf("cache dir 생성: %w", err)
	}
	if len(mirrors) == 0 {
		return "", nil, "", errors.New("사용 가능한 Alpine 미러가 없음")
	}
	var lastErr error
	for _, m := range mirrors {
		base := strings.TrimRight(m.Base, "/")
		version, err := latestKernelVersion(ctx, client, base)
		if err != nil {
			lastErr = fmt.Errorf("%s APKINDEX: %w", m.Label, err)
			continue
		}
		apkURL := fmt.Sprintf("%s/%s-%s.apk", base, catalog.AlpineKernelPackage, version)
		apk, err := httpGetBytes(ctx, client, apkURL)
		if err != nil {
			lastErr = fmt.Errorf("%s apk: %w", m.Label, err)
			continue
		}
		kernel, mods, kver, err := extractKernelAndModules(apk)
		if err != nil {
			lastErr = fmt.Errorf("%s 추출: %w", m.Label, err)
			continue
		}
		if len(kernel) < 64 {
			lastErr = fmt.Errorf("%s 커널 %d 바이트로 너무 작음", m.Label, len(kernel))
			continue
		}
		dest := filepath.Join(cacheDir, CachedGenericKernelName)
		tmp := dest + ".part"
		if err := os.WriteFile(tmp, kernel, 0o644); err != nil {
			return "", nil, "", fmt.Errorf("커널 캐시 쓰기: %w", err)
		}
		if err := os.Rename(tmp, dest); err != nil {
			_ = os.Remove(tmp)
			return "", nil, "", fmt.Errorf("커널 캐시 rename: %w", err)
		}
		return dest, mods, kver, nil
	}
	return "", nil, "", fmt.Errorf("모든 Alpine 미러에서 커널/모듈을 받지 못함: %w", lastErr)
}

// FetchCABundle downloads Alpine's CA certificate bundle and returns it.
// FetchCABundle - Alpine 의 CA 인증서 번들을 받아 내용을 돌려준다.
func FetchCABundle(cacheDir string) ([]byte, error) {
	return FetchCABundleFromMirrors(context.Background(), cacheDir, catalog.AlpineMirrors, http.DefaultClient)
}

// FetchCABundleFromMirrors is the test hook, complete with httptest.
// FetchCABundleFromMirrors - 테스트 훅. httptest 로 완결된다.
func FetchCABundleFromMirrors(ctx context.Context, cacheDir string, mirrors []catalog.AlpineMirror, client *http.Client) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if len(mirrors) == 0 {
		return nil, errors.New("사용 가능한 Alpine 미러가 없음")
	}
	cached := filepath.Join(cacheDir, "ca-certificates.crt")
	if b, err := os.ReadFile(cached); err == nil && len(b) > 1024 {
		return b, nil
	}
	var lastErr error
	for _, m := range mirrors {
		base := strings.TrimRight(m.Base, "/")
		version, err := latestPackageVersion(ctx, client, base, catalog.AlpineCACertPackage)
		if err != nil {
			lastErr = fmt.Errorf("%s APKINDEX: %w", m.Label, err)
			continue
		}
		apkURL := fmt.Sprintf("%s/%s-%s.apk", base, catalog.AlpineCACertPackage, version)
		apk, err := httpGetBytes(ctx, client, apkURL)
		if err != nil {
			lastErr = fmt.Errorf("%s apk: %w", m.Label, err)
			continue
		}
		pem, err := extractFromAPK(apk, catalog.AlpineCACertPath)
		if err != nil {
			lastErr = fmt.Errorf("%s 추출: %w", m.Label, err)
			continue
		}
		if len(pem) < 1024 {
			lastErr = fmt.Errorf("%s CA 번들이 %d 바이트로 너무 작음", m.Label, len(pem))
			continue
		}
		if cacheDir != "" {
			_ = os.WriteFile(cached, pem, 0o644)
		}
		return pem, nil
	}
	return nil, fmt.Errorf("모든 Alpine 미러에서 CA 번들을 받지 못함: %w", lastErr)
}

// FetchXZ obtains the xz binary and what it needs out of the apks. The key is
// the path it will sit at inside the image, the value is the content.
//
// FetchXZ - xz 실행 파일과 그것이 필요로 하는 것들을 apk 에서 확보한다.
//
// 키는 이미지 안에 놓일 경로, 값은 내용.
func FetchXZ(cacheDir string) (map[string][]byte, error) {
	return FetchXZFromMirrors(context.Background(), cacheDir, catalog.AlpineMirrors, http.DefaultClient)
}

// FetchXZFromMirrors is the test hook, complete with httptest.
// FetchXZFromMirrors - 테스트 훅. httptest 로 완결된다.
func FetchXZFromMirrors(ctx context.Context, cacheDir string, mirrors []catalog.AlpineMirror, client *http.Client) (map[string][]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if len(mirrors) == 0 {
		return nil, errors.New("사용 가능한 Alpine 미러가 없음")
	}
	out := map[string][]byte{}
	for _, want := range catalog.XZFiles {
		data, err := fetchAPKMember(ctx, client, mirrors, cacheDir, want.Package, want.Path)
		if err != nil {
			return nil, fmt.Errorf("%s 에서 %s: %w", want.Package, want.Path, err)
		}
		out[want.Path] = data
	}
	return out, nil
}

// fetchAPKMember walks the mirrors, downloads the latest apk for pkg and pulls
// member out of it. With cacheDir set the result is kept, so a rebuild does not
// touch the network.
//
// fetchAPKMember - 미러를 돌며 pkg 의 최신 apk 를 받아 그 안의 member 를 뽑는다.
// cacheDir 이 비어있지 않으면 결과를 캐시해 재빌드에서 네트워크를 안 쓴다.
func fetchAPKMember(ctx context.Context, client *http.Client, mirrors []catalog.AlpineMirror, cacheDir, pkg, member string) ([]byte, error) {
	var cached string
	if cacheDir != "" {
		cached = filepath.Join(cacheDir, "apk-"+pkg+"-"+path.Base(member))
		if b, err := os.ReadFile(cached); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	var lastErr error
	for _, m := range mirrors {
		base := strings.TrimRight(m.Base, "/")
		version, err := latestPackageVersion(ctx, client, base, pkg)
		if err != nil {
			lastErr = fmt.Errorf("%s APKINDEX: %w", m.Label, err)
			continue
		}
		apk, err := httpGetBytes(ctx, client, fmt.Sprintf("%s/%s-%s.apk", base, pkg, version))
		if err != nil {
			lastErr = fmt.Errorf("%s apk: %w", m.Label, err)
			continue
		}
		data, err := extractFromAPK(apk, member)
		if err != nil {
			lastErr = fmt.Errorf("%s 추출: %w", m.Label, err)
			continue
		}
		if cached != "" {
			_ = os.MkdirAll(cacheDir, 0o755)
			_ = os.WriteFile(cached, data, 0o644)
		}
		return data, nil
	}
	return nil, lastErr
}

// moduleWantedSubdirs lists what actually goes on the initrd, as paths under
// lib/modules/VER/.
//
// Only what the first moments of boot need: virtio (VMs), scsi/ata/nvme/block
// (disks), net (NICs), input and hid (keyboard and mouse), usb and pci (the
// buses those sit on), video and gpu (the installer screen), fs/vfat, fs/fat
// and nls (mounting the FAT partitions), lib and crypto (dependencies such as
// crc32c), plus the modules.* metadata files.
//
// moduleWantedSubdirs - initrd 에 실제로 실을 것들. lib/modules/VER/ 아래
// 경로다.
//
// 부팅 초입에 필요한 것만: virtio (VM), scsi/ata/nvme/block (디스크),
// net (NIC), input+hid (키보드·마우스), usb+pci (그것들이 붙는 버스),
// video+gpu (설치 화면), fs/vfat+fs/fat+nls (FAT 파티션 마운트),
// lib+crypto (crc32c 등 dep), 그리고 modules.* 메타 파일.
var moduleWantedSubdirs = []string{
	"kernel/drivers/virtio/",
	"kernel/drivers/scsi/",
	"kernel/drivers/ata/",
	"kernel/drivers/block/",
	"kernel/drivers/nvme/",
	"kernel/drivers/net/",
	"kernel/drivers/input/",
	"kernel/drivers/hid/",
	"kernel/drivers/usb/",
	"kernel/drivers/pci/",
	// The installer screen is drawn straight onto the framebuffer, so a
	// display driver is needed. efifb/vesafb, which pick up the mode GRUB left
	// set, are usually built into the kernel, but some builds have them as
	// modules, so they go along too. The gpu subtree is the safety net for
	// when even that is missing (bochs, virtio_gpu).
	//
	// 설치 화면을 프레임버퍼에 직접 그리므로 화면 드라이버가 필요하다.
	// GRUB 이 세워 둔 모드를 이어받는 efifb/vesafb 는 보통 커널에 박혀
	// 있지만, 모듈로 빠진 빌드도 있어서 함께 싣는다. gpu 쪽은 그것마저
	// 안 될 때의 안전망 (bochs, virtio_gpu).
	"kernel/drivers/video/",
	"kernel/drivers/gpu/",
	"kernel/fs/vfat/",
	"kernel/fs/fat/",
	"kernel/fs/nls/",
	"kernel/lib/",
	"kernel/crypto/",
	"modules.dep",
	"modules.alias",
	"modules.builtin",
	"modules.builtin.alias.bin",
	"modules.builtin.modinfo",
	"modules.order",
	"modules.symbols",
}

// moduleName takes the module name out of a module file path: directory and
// extensions dropped, hyphens turned into underscores. It normalises what
// modules.dep writes as a path into the name the kernel uses.
//
// moduleName - 모듈 파일 경로에서 모듈 이름만 뽑는다 (디렉터리·확장자 제거,
// 하이픈은 언더스코어로). modules.dep 가 경로로 적는 것을 커널이 쓰는 이름과
// 맞추기 위한 정규화.
func moduleName(path string) string {
	name := path[strings.LastIndex(path, "/")+1:]
	name = strings.TrimSuffix(name, ".gz")
	name = strings.TrimSuffix(name, ".xz")
	name = strings.TrimSuffix(name, ".ko")
	return strings.ReplaceAll(name, "-", "_")
}

// moduleClosure works out, from modules.dep, the set of module names that the
// moduleWantedSubdirs match plus everything they depend on.
//
// Picking by subdirectory alone misses dependencies. sd_mod needs t10_pi from
// `kernel/block/`, sr_mod needs cdrom from `kernel/drivers/cdrom/`, virtio_net
// needs failover from `kernel/net/core/` - all outside the driver directories.
// A missing dependency means the kernel cannot resolve a symbol, that module
// fails to load at all, and a disk or a NIC does not come up.
//
// moduleClosure - moduleWantedSubdirs 에 걸리는 모듈들과 그것들이 요구하는
// 의존 모듈 전부를 modules.dep 에서 구해 이름 집합으로 돌려준다.
//
// 서브디렉터리만 보고 고르면 의존 모듈을 놓친다. sd_mod 는 `kernel/block/` 의
// t10_pi 를, sr_mod 는 `kernel/drivers/cdrom/` 의 cdrom 을, virtio_net 은
// `kernel/net/core/` 의 failover 를 요구하는데 전부 드라이버 디렉터리 밖에
// 있다. 의존 모듈이 빠지면 커널이 심볼을 못 찾아 그 모듈은 로드 자체가
// 실패하고, 디스크나 NIC 가 통째로 안 올라온다.
func moduleClosure(dep []byte) map[string]bool {
	deps := map[string][]string{}
	var seeds []string
	for _, line := range strings.Split(string(dep), "\n") {
		path, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name := moduleName(path)
		for _, d := range strings.Fields(rest) {
			deps[name] = append(deps[name], moduleName(d))
		}
		for _, prefix := range moduleWantedSubdirs {
			if strings.HasPrefix(path, prefix) {
				seeds = append(seeds, name)
				break
			}
		}
	}

	out := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		if out[n] {
			return
		}
		out[n] = true
		for _, d := range deps[n] {
			walk(d)
		}
	}
	for _, s := range seeds {
		walk(s)
	}
	return out
}

// readModulesDep finds modules.dep inside the apk and returns its content.
// readModulesDep - apk 안의 modules.dep 내용을 찾아 돌려준다.
func readModulesDep(apk []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(apk))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("apk 안에서 modules.dep 를 찾지 못함")
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if strings.HasSuffix(hdr.Name, "/modules.dep") {
			return io.ReadAll(io.LimitReader(tr, 8*1024*1024))
		}
	}
}

// extractKernelAndModules pulls vmlinuz-lts and the needed modules out of an
// apk (a gzip tar). The modules are chosen by moduleWantedSubdirs and the
// dependency closure over them (moduleClosure). A .ko.gz or .ko.xz is stored
// decompressed as a raw .ko.
//
// extractKernelAndModules - apk (gzip tar) 에서 vmlinuz-lts 와 필요한 모듈들을
// 뽑는다. 모듈 선택은 moduleWantedSubdirs 와 그 의존성 폐포 (moduleClosure).
// .ko.gz / .ko.xz 는 raw .ko 로 풀어 저장한다.
func extractKernelAndModules(apk []byte) ([]byte, []ModuleFile, string, error) {
	depFile, err := readModulesDep(apk)
	if err != nil {
		return nil, nil, "", err
	}
	closure := moduleClosure(depFile)

	gz, err := gzip.NewReader(bytes.NewReader(apk))
	if err != nil {
		return nil, nil, "", fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var kernel []byte
	var mods []ModuleFile
	var kver string
	verRE := regexp.MustCompile(`^lib/modules/([^/]+)/`)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, "", fmt.Errorf("tar: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		if name == catalog.AlpineKernelVMLinuzPath {
			data, err := io.ReadAll(io.LimitReader(tr, 64*1024*1024))
			if err != nil {
				return nil, nil, "", fmt.Errorf("vmlinuz 읽기: %w", err)
			}
			kernel = data
			continue
		}
		if !strings.HasPrefix(name, "lib/modules/") {
			continue
		}
		if m := verRE.FindStringSubmatch(name); m != nil && kver == "" {
			kver = m[1]
		}
		// Only the kernel/... part matters; filter on the subpath after VER.
		// kernel/... 부분만 관심. VER 뒤 서브패스로 필터.
		var rel string
		if m := verRE.FindStringSubmatchIndex(name); m != nil {
			rel = name[m[1]:] // "kernel/drivers/..." or "modules.dep" etc / "kernel/drivers/..." 또는 "modules.dep" 등
		} else {
			continue
		}
		wanted := strings.HasSuffix(rel, ".ko") ||
			strings.HasSuffix(rel, ".ko.gz") || strings.HasSuffix(rel, ".ko.xz")
		if wanted {
			wanted = closure[moduleName(rel)]
		} else {
			// A metadata file such as modules.dep is taken by name,
			// regardless of the closure.
			//
			// modules.dep 같은 메타파일은 폐포와 무관하게 이름으로 받는다.
			for _, prefix := range moduleWantedSubdirs {
				if rel == prefix {
					wanted = true
					break
				}
			}
		}
		if !wanted {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 32*1024*1024))
		if err != nil {
			return nil, nil, "", fmt.Errorf("%s 읽기: %w", name, err)
		}
		// .ko.gz -> raw .ko. A .ko.xz is skipped (see below).
		// .ko.gz 는 raw .ko 로 푼다. .ko.xz 는 건너뛴다 (아래 참고).
		outName := name
		if strings.HasSuffix(name, ".ko.gz") {
			rd, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				continue
			}
			raw, err := io.ReadAll(io.LimitReader(rd, 32*1024*1024))
			_ = rd.Close()
			if err != nil {
				continue
			}
			data = raw
			outName = strings.TrimSuffix(name, ".gz")
		} else if strings.HasSuffix(name, ".ko.xz") {
			// The standard library has no xz, so this is skipped. Almost
			// every Alpine LTS kernel module is .ko.gz or an uncompressed
			// .ko, so this branch is rare in practice. Add support if that
			// changes.
			//
			// xz 는 표준 라이브러리에 없어 여기선 스킵. 대부분의 Alpine
			// LTS 커널 모듈은 .ko.gz 이거나 무압축 .ko 이므로 실전에서
			// 이 분기는 드물다. 필요해지면 별도 지원을 붙인다.
			continue
		}
		mods = append(mods, ModuleFile{
			Name: outName,
			Mode: 0o644,
			Data: data,
		})
	}
	if kernel == nil {
		return nil, nil, "", fmt.Errorf("apk 안에서 %s 를 찾지 못함", catalog.AlpineKernelVMLinuzPath)
	}
	if kver == "" {
		return kernel, nil, "", fmt.Errorf("모듈 트리 (lib/modules/VER/) 를 찾지 못함")
	}
	return kernel, mods, kver, nil
}

// latestKernelVersion downloads APKINDEX.tar.gz, parses the APKINDEX inside and
// returns the newest V: value for AlpineKernelPackage.
//
// An Alpine repository can hold several records for one package (different
// flavors, say), but linux-lts is a single entry. If several do match, the
// lexicographically largest counts as newest - not exact, but close enough to
// apk's version convention for cases like 6.6.60-r0 against 6.6.60-r1.
//
// latestKernelVersion - APKINDEX.tar.gz 를 받아 안의 APKINDEX 를 파싱해서
// AlpineKernelPackage 의 최신 V: 값을 돌려준다.
//
// Alpine 저장소는 한 패키지에 대해 여러 개 (예: 다른 flavor) 를 담기도
// 하지만, linux-lts 는 단일 항목이다. 여러 개가 잡히면 사전순으로 큰 것을
// 최신으로 친다 - 완전하진 않지만 (6.6.60-r0 vs 6.6.60-r1 처럼) apk 의
// 버전 규약과 대체로 맞는다.
func latestKernelVersion(ctx context.Context, client *http.Client, base string) (string, error) {
	return latestPackageVersion(ctx, client, base, catalog.AlpineKernelPackage)
}

// latestPackageVersion returns the newest V: value for pkg from the APKINDEX.
// latestPackageVersion - APKINDEX 에서 pkg 의 최신 V: 값을 돌려준다.
func latestPackageVersion(ctx context.Context, client *http.Client, base, pkg string) (string, error) {
	idxURL := base + "/APKINDEX.tar.gz"
	body, err := httpGetBytes(ctx, client, idxURL)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", idxURL, err)
	}
	idx, err := readAPKINDEX(body)
	if err != nil {
		return "", err
	}
	var best string
	scanner := bufio.NewScanner(bytes.NewReader(idx))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var curName, curVer string
	commit := func() {
		if curName == pkg && curVer != "" {
			if curVer > best {
				best = curVer
			}
		}
		curName, curVer = "", ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			commit()
			continue
		}
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		key := line[0]
		val := line[2:]
		switch key {
		case 'P':
			curName = val
		case 'V':
			curVer = val
		}
	}
	commit()
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("APKINDEX 스캔: %w", err)
	}
	if best == "" {
		return "", fmt.Errorf("APKINDEX 에 %s 가 없음", pkg)
	}
	return best, nil
}

// readAPKINDEX pulls the single file named "APKINDEX" out of APKINDEX.tar.gz
// and returns its content. The tar also holds a signature (.SIGN.*) and a
// DESCRIPTION, but APKINDEX is the only one wanted.
//
// readAPKINDEX - APKINDEX.tar.gz 안에서 "APKINDEX" 라는 하나의 파일을
// 뽑아 그 내용을 돌려준다. tar 안에는 서명 (.SIGN.*) 과 DESCRIPTION 도
// 있으나 우리가 필요한 건 APKINDEX 한 개다.
func readAPKINDEX(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	// APKINDEX usually sits near the front, so the first match could end it.
	// Its position is not assumed, though - the whole archive is walked.
	//
	// APKINDEX 파일은 대체로 앞쪽에 있어서 첫 매치에서 끝낼 수도 있다.
	// 그러나 위치를 가정하지 않고 전체를 훑는다.
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("아카이브 안에 APKINDEX 파일이 없음")
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if path.Base(hdr.Name) != "APKINDEX" {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 64*1024*1024))
		if err != nil {
			return nil, fmt.Errorf("APKINDEX 읽기: %w", err)
		}
		return data, nil
	}
}

// extractFromAPK reads an apk, which is a gzip tar. Several tar streams can be
// concatenated (a control and a data segment); gzip.Reader has Multistream on
// by default and reads them all in order. The data of the first matching file
// is returned.
//
// wantPath has to match the tar header's Name exactly. Alpine sometimes writes
// a ./ prefix and sometimes does not, so both are tried. Libraries are often in
// an apk as symlinks (liblzma.so.5 -> liblzma.so.5.8.1), and the link has to be
// followed to reach the real content.
//
// extractFromAPK - apk 는 gzip tar. 여러 개의 tar 스트림이 연속으로 붙어
// 있을 수 있다 (control / data segment). gzip.Reader 의 Multistream 이
// 기본으로 켜져 있어 순서대로 다 읽는다. 매칭되는 첫 파일의 데이터를
// 돌려준다.
//
// wantPath 는 tar 헤더의 Name 과 정확히 일치해야 한다. Alpine 은 ./ prefix 를
// 붙이거나 안 붙이거나 하므로 둘 다 시도한다. 라이브러리는 apk 안에 흔히
// 심볼릭 링크로 들어 있어 (liblzma.so.5 -> liblzma.so.5.8.1) 링크를 따라가야
// 실제 내용을 얻는다.
func extractFromAPK(apk []byte, wantPath string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(apk))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	files := map[string][]byte{}
	links := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		switch hdr.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			links[name] = hdr.Linkname
		case tar.TypeReg:
			// vmlinuz runs to a few MB. The cap is 64 MiB - an LTS kernel
			// bigger than that means something is wrong.
			//
			// vmlinuz 는 몇 MB 짜리. 상한을 64 MiB 로 잡는다 - LTS 커널이
			// 이보다 크면 뭔가 이상한 것이다.
			data, err := io.ReadAll(io.LimitReader(tr, 64*1024*1024))
			if err != nil {
				return nil, fmt.Errorf("데이터 읽기 %s: %w", name, err)
			}
			files[name] = data
		}
	}

	cur := wantPath
	for hop := 0; hop < 16; hop++ {
		if data, ok := files[cur]; ok {
			return data, nil
		}
		target, ok := links[cur]
		if !ok {
			break
		}
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(cur), target)
		}
		cur = strings.TrimPrefix(target, "/")
	}
	return nil, fmt.Errorf("apk 안에서 %s 를 찾지 못함", wantPath)
}

// httpGetBytes is one HTTP GET with a short timeout - a dead mirror should be
// left behind quickly.
//
// httpGetBytes - HTTP GET 한 번. 짧은 타임아웃 - 미러가 죽었으면 빨리
// 다음으로 넘어가야 한다.
func httpGetBytes(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// 200 MiB cap; an APKINDEX or a kernel apk fits well inside it.
	// 200 MiB 상한 (APKINDEX 나 커널 apk 는 그 안에 다 들어온다).
	return io.ReadAll(io.LimitReader(resp.Body, 200*1024*1024))
}
