//go:build linux

package main

// Loading the NVIDIA GPU driver (for DVA models) and triggering a TensorRT
// engine rebuild.
//
// NOTHING HERE RUNS ON THE THREE MODELS SUPPORTED TODAY. DS918+, DS3622xs+ and
// SA6400 are none of them DVA, so isDVAModel is always false and the engine
// cache is never cleared; the nvidia modules only ship in a DVA model's driver
// pack, so the load quietly finds nothing. This is a leftover from when the
// loader aimed at every model. It is kept rather than removed, so that adding
// a DVA model later does not mean writing it again.
//
// The problem it solves: on a model that uses a GPU - DVA1622, DVA3221,
// DVA7400 - the DeepLearning package bakes its TensorRT engines (.trt,
// .engine) against the CUDA compute capability it saw the first time. Booting
// through a loader with a different GPU (GTX1650 -> T4 -> RTX A2000) leaves
// that capability mismatched and synodvad goes into a crash loop, with no way
// out short of reinstalling.
//
// Two halves to the answer:
//  1. Load nvidia.ko and nvidia-uvm.ko from the pack so the GPU is visible.
//     The stock kernel carries no NVIDIA driver; the p4 driver pack for DVA
//     models is distributed separately, and without it this step is a no-op.
//  2. On a DVA model, delete the installed system's .trt and .engine caches.
//     synodvad recompiles them against the GPU's capability on its next start
//     and carries on normally from there.
//
// The kind of GPU is not detected at this stage - there is no nvidia-smi in the
// ramdisk. The safe side is taken instead: if the capability could have changed
// since last time, the whole cache goes. A recompile costs a few minutes; a
// crash loop costs the boot entirely.
//
// NVIDIA GPU (DVA 모델용) 드라이버 로드 + TensorRT 엔진 재컴파일 트리거.
//
// 지금 지원하는 세 모델에서는 이 경로가 하나도 돌지 않는다. DS918+,
// DS3622xs+, SA6400 중 DVA 는 없어서 isDVAModel 이 항상 false 이고 엔진
// 캐시는 지워지지 않는다. nvidia 모듈도 DVA 모델 드라이버 팩에만 들어 있어
// 로드는 조용히 빈손으로 끝난다. 로더가 전 모델을 노리던 시절의 잔재다.
// 나중에 DVA 를 붙일 때 다시 쓰지 않으려고 지우지 않고 남겨 둔다.
//
// 풀려던 문제: DVA1622 / DVA3221 / DVA7400 처럼 GPU 를 쓰는 모델은
// DeepLearning 패키지가 TensorRT engine (.trt / .engine) 을 GPU 를 처음 본
// 시점의 CUDA compute capability 로 굽는다. 로더로 부팅해 GPU 종류가 바뀌면
// (예: GTX1650 -> T4 -> RTX A2000) capability 가 어긋나서 synodvad 가
// crash-loop 로 들어간다. 재설치 없이는 안 풀린다.
//
// 해법은 두 갈래다:
//  1. 팩에서 nvidia.ko 와 nvidia-uvm.ko 를 로드해 GPU 가 보이게 한다.
//     stock 커널에는 NVIDIA 드라이버가 없다. DVA 모델용 p4 드라이버 팩은
//     따로 배포되고, 그게 없으면 이 단계는 no-op 이다.
//  2. DVA 모델이면 설치된 시스템의 .trt / .engine 캐시를 지운다. synodvad 가
//     다음 시작 때 GPU capability 에 맞춰 다시 컴파일하고 그 뒤로는 정상으로
//     돈다.
//
// GPU 종류 감지는 이 단계에서 하지 않는다 (nvidia-smi 가 램디스크에 없다).
// 대신 "capability 가 지난번과 달라졌을 가능성이 있으면 캐시를 통째로
// 지운다" 라는 안전한 쪽을 택한다. 재컴파일 비용은 몇 분이지만 crash-loop
// 는 아예 부팅을 못 한다.

import (
	"os"
	"path/filepath"
	"strings"

	"vibeldr/internal/hwscan"
	"vibeldr/internal/kmod"
)

// nvidiaVendor is NVIDIA Corporation's PCI vendor ID.
// nvidiaVendor - NVIDIA Corporation PCI vendor ID.
const nvidiaVendor uint32 = 0x10de

// nvidiaModules is the load order. They depend on one another, so
// loadModuleByName pulls the rest in by itself, but both are tried explicitly.
//
// nvidiaModules - 로드 순서. 서로 depends 관계라 loadModuleByName 이
// 알아서 딸려 로드하지만, 명시적으로 두 개를 시도해 놓는다.
var nvidiaModules = []string{"nvidia", "nvidia_uvm"}

// tensorRTGlobs are where the engine caches live. DeepLearning* varies with the
// SPK's name and version, so it is swept with a glob. Nothing matching means
// nothing to delete - the state before installation, or a non-DVA model.
//
// tensorRTGlobs - 엔진 캐시가 사는 자리들. DeepLearning* 는 SPK 이름과
// 버전에 따라 다양해서 glob 으로 훑는다. 잡히는 파일이 없으면 지울 것도
// 없다 (설치 전 상태나 non-DVA 모델).
var tensorRTGlobs = []string{
	"/var/packages/DeepLearning*/target/models/*.trt",
	"/var/packages/DeepLearning*/target/models/*.engine",
	"/var/packages/DeepLearning*/target/models/**/*.trt",
	"/var/packages/DeepLearning*/target/models/**/*.engine",
	// Surveillance Station's model for deciding DVA is in the same place.
	// Surveillance Station 의 DVA 판별용 모델도 같은 자리에 있다.
	"/var/packages/SurveillanceStation*/target/dva/*.trt",
	"/var/packages/SurveillanceStation*/target/dva/*.engine",
}

// unlockNvidia loads the modules when an NVIDIA GPU is present and, on a DVA
// model, invalidates the engines.
//
// unlockNvidia - NVIDIA GPU 가 있으면 모듈을 로드하고, DVA 이면 엔진을
// 무효화한다.
func unlockNvidia(index kmod.Index, images map[string][]byte, loaded map[string]bool) {
	devs := pciByVendor(scanPCIDevices(hwscan.DefaultSysfs), nvidiaVendor)
	if len(devs) == 0 {
		return
	}
	var addrs []string
	for _, d := range devs {
		addrs = append(addrs, d.sysfsAddress)
	}
	logf("nvidia: found %d GPU(s): %v", len(devs), addrs)

	// Load the pack's modules, skipping quietly when they are not there -
	// only a DVA model's image has them in the pack, and other models' images
	// never carry them.
	//
	// 팩본 모듈을 로드한다. 없으면 조용히 건너뛴다 - DVA 모델 이미지에서만
	// 팩에 들어 있고, 다른 모델 이미지는 애초에 담지 않는다.
	var loadedNames []string
	for _, name := range nvidiaModules {
		if _, ok := index[name]; !ok {
			continue
		}
		ok, err := loadModuleByName(name, index, images, loaded)
		switch {
		case err != nil:
			logf("nvidia: %s: %v", name, err)
		case ok:
			loadedNames = append(loadedNames, name)
		}
	}
	if len(loadedNames) > 0 {
		logf("nvidia: loaded %v", loadedNames)
	}

	// The engine cache is only invalidated on a DVA model. Anywhere else - a
	// non-DVA system that simply has an NVIDIA GPU in it - nothing is touched.
	//
	// DVA 모델일 때만 엔진 캐시를 무효화한다. 아니라면 (예: NVIDIA GPU 를
	// 그냥 꽂아둔 non-DVA 시스템) 손대지 않는다.
	if !isDVAModel() {
		return
	}
	invalidated := invalidateTensorRTEngines(tensorRTGlobs, "")
	if invalidated > 0 {
		logf("nvidia: invalidated %d TensorRT engine file(s); synodvad will recompile", invalidated)
	} else {
		logf("nvidia: no TensorRT engine files present yet")
	}
}

// isDVAModel decides whether the model booting right now is in the DVA line.
//
// In order:
//  1. syno_hw_version=DVA* on /proc/cmdline, the marker from before the
//     installer stage
//  2. "_dva" in the unique field of /etc/synoinfo.conf inside the ramdisk
//
// Finding neither falls back to false, which is the safe side.
//
// TODO: once internal/config gains a dsm_type field, take that value over the
// cmdline or through a carried file and prefer it here. There is no source for
// it at the moment, so this is only a note.
//
// isDVAModel - 지금 부팅 중인 모델이 DVA 계열인지 판정한다.
//
// 확인 순서:
//  1. /proc/cmdline 의 syno_hw_version=DVA* (인스톨러 단계 이전부터 있는 표시)
//  2. 램디스크 안 /etc/synoinfo.conf 의 unique 필드에 "_dva"
//
// 둘 다 못 찾으면 false 로 물러난다 (안전한 쪽).
//
// TODO: internal/config 에 dsm_type 필드가 생기면 그 값을 cmdline 이나 실어
// 나르는 파일로 받아 여기서 우선 쓴다. 지금은 그 소스가 없어 메모만 남긴다.
func isDVAModel() bool {
	if v := cmdlineValue("syno_hw_version", ""); strings.HasPrefix(strings.ToUpper(v), "DVA") {
		return true
	}
	b, err := os.ReadFile("/etc/synoinfo.conf")
	if err != nil {
		return false
	}
	// The form is unique="synology_..._dva3221". Matched in lower case, with
	// or without the quotes.
	//
	// unique="synology_..._dva3221" 형식. quote 유무에 상관없이 소문자로
	// 매칭한다.
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.ToLower(line))
		if !strings.HasPrefix(line, "unique=") {
			continue
		}
		if strings.Contains(line, "_dva") {
			return true
		}
	}
	return false
}

// invalidateTensorRTEngines deletes the files matching the given glob patterns.
//
// root is the filesystem prefix - /tmpRoot after the pivot, say - and an empty
// string searches from the current root. It returns how many files were
// deleted; an individual failure is logged and passed over. No files at all
// giving 0 is normal.
//
// The globs follow filepath.Glob's rules, which do not support `**`. A pattern
// containing it goes through expandGlob, which walks the whole tree below that
// point instead.
//
// invalidateTensorRTEngines - 지정한 glob 패턴들에 걸리는 파일을 지운다.
//
// root 는 파일시스템 prefix (예: pivot 후 /tmpRoot). 빈 문자열이면 현재
// 루트에서 검색한다. 지운 파일 수를 돌려주고, 개별 실패는 로그에 남기고
// 계속한다. 파일이 하나도 없으면 0 이 정상이다.
//
// glob 는 filepath.Glob 규칙을 따르고, 그 규칙은 `**` 를 지원 안 한다.
// `**` 가 든 패턴은 expandGlob 이 그 지점 아래 트리 전체를 walk 해서 대신
// 처리한다.
func invalidateTensorRTEngines(patterns []string, root string) int {
	deleted := 0
	seen := map[string]bool{}
	for _, pat := range patterns {
		full := pat
		if root != "" {
			full = filepath.Join(root, pat)
		}
		matches := expandGlob(full)
		for _, m := range matches {
			if seen[m] {
				continue
			}
			seen[m] = true
			if err := os.Remove(m); err != nil {
				logf("nvidia: remove %s: %v", m, err)
				continue
			}
			deleted++
		}
	}
	return deleted
}

// expandGlob is the thin wrapper that handles `**` instead.
//
// filepath.Glob does not support `**`, so a pattern containing it is narrowed
// with Glob up to that point and then walked below there. Without it, Glob is
// used as it is.
//
// expandGlob - `**` 을 대신 처리하는 얇은 wrapper.
//
// filepath.Glob 은 `**` 을 지원 안 하므로, 패턴에 `**` 가 있으면 앞부분까지
// Glob 로 좁힌 다음 그 밑을 walk 로 이어붙인다. 없으면 그대로 Glob 이다.
func expandGlob(pattern string) []string {
	idx := strings.Index(pattern, "**")
	if idx < 0 {
		out, _ := filepath.Glob(pattern)
		return out
	}
	head := pattern[:idx]
	tail := strings.TrimPrefix(pattern[idx+2:], "/")
	// head usually ends like "*/target/models/"; the trailing slash is dropped
	// so it can be enumerated.
	//
	// head 는 보통 "*/target/models/" 처럼 끝난다. 열거를 위해 뒤 슬래시를
	// 제거한다.
	heads, _ := filepath.Glob(strings.TrimSuffix(head, "/"))
	var out []string
	for _, h := range heads {
		filepath.Walk(h, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			ok, _ := filepath.Match(tail, filepath.Base(p))
			if ok {
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}
