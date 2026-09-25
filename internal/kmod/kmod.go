// Package kmod loads the kernel modules this machine needs.
//
// The DSM ramdisk carries drivers for its own appliance and loads them from a
// hard-coded list in the boot script. Anything not on that list stays on disk
// unloaded. Any machine that is not that appliance runs straight into this:
// igb, ixgbe, i40e, atlantic and r8168 are all present in the ramdisk, none of
// them load, and the installer comes up with no network.
//
// The answer is to do what modprobe does, using only what is already in the
// ramdisk: read each module's alias table, read each device's modalias from
// sysfs, and load what matches. Nothing is downloaded and nothing is built.
// Every module loaded this way is exactly as Synology shipped it, signature
// included.
//
// Package kmod - 이 머신에 필요한 커널 모듈들을 로드.
//
// DSM 램디스크는 자기 어플라이언스용 드라이버를 담고 있고, 부팅 스크립트의
// 하드코딩된 목록으로만 로드한다. 목록에 없는 건 디스크에 있어도 안 뜬다.
// 그 어플라이언스가 아닌 머신은 바로 이걸 겪는다. 램디스크에 igb, ixgbe, i40e,
// atlantic, r8168 이 다 있는데 하나도 안 로드되니 인스톨러가 네트워크 없이 뜬다.
//
// 해법은 modprobe 가 하는 걸 램디스크 안에 이미 있는 것들로 하는 것이다.
// 각 모듈의 alias 표를 읽고, sysfs 에서 각 장치의 modalias 를 읽고, 매칭되는
// 걸 로드한다. 다운로드도 빌드도 없다. 이 경로로 로드되는 모든 모듈은
// 시놀로지가 보낸 그대로, 서명도 그대로다.
package kmod

import (
	"bytes"
	"debug/elf"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// SearchDirs are where DSM keeps its modules. /lib/modules is a symlink to the
// second on some models and a directory of its own on others, so both are read
// and duplicates are resolved by module name.
//
// SearchDirs - DSM 이 모듈을 두는 자리. 모델에 따라 /lib/modules 가 두 번째
// 경로로의 심볼릭 링크이기도 하고 독립 디렉터리이기도 해서, 둘 다 읽고
// 중복은 모듈 이름으로 정리한다.
var SearchDirs = []string{"/lib/modules", "/usr/lib/modules"}

// Module is one .ko file and what it says about itself.
// Module - .ko 파일 하나와 그 파일이 스스로에 대해 밝힌 것.
type Module struct {
	// Name is the module name as the kernel knows it, with underscores.
	// Name - 커널이 아는 모듈 이름. 밑줄 표기다.
	Name string
	Path string
	// Aliases are glob patterns matched against a device's modalias.
	// Aliases - 장치의 modalias 와 대조할 글롭 패턴.
	Aliases []string
	// Depends are module names that must be loaded first.
	// Depends - 먼저 로드되어야 하는 모듈 이름들.
	Depends []string
	// Version is what the driver calls itself, when it says. Vendor drivers
	// carry one - "8.057.00-NAPI" - and the kernel's own mostly do not, which
	// is the difference between a release and whatever was in the tree.
	//
	// Version - 드라이버가 스스로 밝힌 버전. 벤더 드라이버는 대개
	// "8.057.00-NAPI" 처럼 값을 달고 있고 커널 자체 드라이버는 대개 없다.
	// 벤더 릴리스와 트리에 들어 있던 것의 차이가 여기서 드러난다.
	Version string
	// Firmware are the blob names the driver asks the kernel for
	// (MODULE_FIRMWARE), such as "rtl_nic/rtl8168h-2.fw". The name contains a
	// sub-path, and the kernel looks for exactly that path under /lib/firmware.
	//
	// Firmware - 드라이버가 커널에 요청하는 펌웨어 이름 (MODULE_FIRMWARE).
	// "rtl_nic/rtl8168h-2.fw" 처럼 이름에 하위 경로가 들어 있고, 커널이 그
	// 경로 그대로 /lib/firmware 밑에서 찾는다.
	Firmware []string
	// Vermagic is the kernel the module was built for ("4.4.302+ SMP
	// mod_unload"). A module whose first field differs from the running
	// kernel's release is refused by the kernel.
	//
	// Vermagic - 모듈이 빌드된 커널 ("4.4.302+ SMP mod_unload"). 첫 칸이 돌고
	// 있는 커널의 release 와 다르면 커널이 거부한다.
	Vermagic string
}

// normalize converts a module name to the form /proc/modules uses. The kernel
// treats hyphens and underscores as the same character in module names, and
// the two spellings appear interchangeably in file names and depends lists.
//
// normalize - 모듈 이름을 /proc/modules 표기로 맞춘다. 커널은 모듈 이름에서
// 하이픈과 밑줄을 같은 문자로 보고, 파일 이름이나 depends 목록에는 두 표기가
// 섞여 나온다.
func normalize(name string) string {
	return strings.ReplaceAll(strings.TrimSuffix(name, ".ko"), "-", "_")
}

// parseModinfo splits a .modinfo section into its key/value pairs.
//
// The section is a run of NUL-terminated "key=value" strings; a key may appear
// many times, which is how a driver lists dozens of device aliases.
//
// parseModinfo - .modinfo 섹션을 키/값 쌍으로 쪼갠다.
//
// 섹션은 NUL 로 끝나는 "key=value" 문자열의 나열이고, 같은 키가 여러 번
// 나올 수 있다. 드라이버가 장치 alias 를 수십 개씩 늘어놓는 방식이다.
func parseModinfo(data []byte) map[string][]string {
	out := map[string][]string{}
	for _, field := range bytes.Split(data, []byte{0}) {
		if len(field) == 0 {
			continue
		}
		k, v, ok := strings.Cut(string(field), "=")
		if !ok {
			continue
		}
		out[k] = append(out[k], v)
	}
	return out
}

// Read loads one module's metadata.
// Read - 모듈 하나의 메타데이터를 읽는다.
func Read(p string) (Module, error) {
	f, err := elf.Open(p)
	if err != nil {
		return Module{}, fmt.Errorf("kmod: %s: %w", p, err)
	}
	defer f.Close()

	sec := f.Section(".modinfo")
	if sec == nil {
		return Module{}, fmt.Errorf("kmod: %s has no .modinfo section, so it is not a kernel module", p)
	}
	data, err := sec.Data()
	if err != nil {
		return Module{}, fmt.Errorf("kmod: %s: %w", p, err)
	}
	info := parseModinfo(data)

	return moduleFrom(filepath.Base(p), p, info), nil
}

// moduleFrom assembles a Module from a parsed .modinfo section, wherever the
// section came from.
//
// moduleFrom - 파싱된 .modinfo 섹션에서 Module 을 조립한다. 그 섹션이
// 어디서 왔는지는 상관하지 않는다.
func moduleFrom(name, path string, info map[string][]string) Module {
	m := Module{
		Name:     normalize(name),
		Path:     path,
		Aliases:  info["alias"],
		Firmware: info["firmware"],
	}
	if v := info["version"]; len(v) > 0 {
		m.Version = v[0]
	}
	if v := info["vermagic"]; len(v) > 0 {
		m.Vermagic = v[0]
	}
	// depends is a single comma-separated field, and is empty for a module
	// with no dependencies.
	//
	// depends 는 콤마로 구분된 필드 하나이고, 의존이 없는 모듈에서는 비어
	// 있다.
	for _, d := range info["depends"] {
		for _, dep := range strings.Split(d, ",") {
			if dep = strings.TrimSpace(dep); dep != "" {
				m.Depends = append(m.Depends, normalize(dep))
			}
		}
	}
	return m
}

// Index is every module available to be loaded, by name.
// Index - 로드 가능한 모든 모듈을 이름으로 색인한 것.
type Index map[string]Module

// Scan reads every .ko in the given directories, recursively. DSM stacks them
// flat under /lib/modules while Alpine nests them as
// /lib/modules/VER/kernel/drivers/..., and supporting both layouts is what the
// recursive walk is for.
//
// A file that will not parse is skipped rather than failing the scan: one
// malformed module must not stop the machine from finding its network card.
//
// Scan - 주어진 디렉터리 아래 모든 .ko 를 재귀적으로 읽는다. DSM 은
// /lib/modules 밑에 flat 하게 쌓지만 Alpine 은
// /lib/modules/VER/kernel/drivers/... 처럼 중첩한다. 두 배치 다 지원하려고
// 재귀 walk 를 쓴다.
//
// 파싱 안 되는 파일은 스캔을 실패시키지 않고 건너뛴다. 모듈 하나가 깨졌다고
// 머신이 랜카드를 못 찾게 되어서는 안 된다.
func Scan(dirs ...string) (Index, error) {
	idx := Index{}
	var found int
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// One unreadable directory must not stop the other subtrees.
				// 디렉터리 하나가 안 읽혀도 다른 서브트리는 계속 간다.
				if d != nil && d.IsDir() {
					return nil
				}
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".ko") {
				return nil
			}
			found++
			m, err := Read(path)
			if err != nil {
				return nil
			}
			if _, dup := idx[m.Name]; !dup {
				idx[m.Name] = m
			}
			return nil
		})
	}
	if found == 0 {
		return idx, fmt.Errorf("kmod: no .ko files under %s", strings.Join(dirs, " or "))
	}
	return idx, nil
}

// Matches reports whether a module claims a device.
//
// Alias patterns are shell globs over the modalias string sysfs exports, for
// example "pci:v00008086d000010C9sv*sd*bc*sc*i*" against
// "pci:v00008086d000010C9sv00008086sd0000A03Cbc02sc00i00".
//
// Matches - 이 모듈이 해당 장치를 담당한다고 하는지.
//
// alias 패턴은 sysfs 가 내놓는 modalias 문자열에 대한 셸 글롭이다. 예를 들어
// "pci:v00008086d000010C9sv*sd*bc*sc*i*" 를
// "pci:v00008086d000010C9sv00008086sd0000A03Cbc02sc00i00" 에 대조한다.
func (m Module) Matches(modalias string) bool {
	for _, pat := range m.Aliases {
		if ok, err := path.Match(pat, modalias); err == nil && ok {
			return true
		}
	}
	return false
}

// Resolve returns the modules needed for a set of device modaliases, in load
// order: a module never appears before something it depends on.
//
// Already-loaded modules are excluded, including from the dependency chain,
// because the kernel rejects a second load of the same module.
//
// Resolve - 주어진 modalias 집합에 필요한 모듈을 로드 순서대로 돌려준다.
// 어떤 모듈도 자기가 의존하는 것보다 먼저 나오지 않는다.
//
// 이미 로드된 모듈은 의존 사슬에서도 빠진다. 커널이 같은 모듈의 두 번째
// 로드를 거부하기 때문이다.
func (idx Index) Resolve(modaliases []string, loaded map[string]bool) []Module {
	return idx.ResolvePreferring(modaliases, loaded, nil)
}

// ResolvePreferring is Resolve with a set of preferred modules: when a
// preferred module and another both match a device, only the preferred one is
// wanted for that device, and if the preferred one is already loaded nothing
// more is. The pack marks Synology's own modules this way - r8169 and
// Synology's r8168 both claim 10ec:8168, and whichever loads first takes the
// card.
//
// ResolvePreferring - 우선 모듈 집합을 받는 Resolve. 우선 모듈과 다른 모듈이 같은
// 장치에 둘 다 맞으면 그 장치에는 우선 모듈만 원하고, 우선 모듈이 이미 올라가
// 있으면 아무것도 더 원하지 않는다. 팩은 시놀 자신의 모듈을 이렇게 표시한다 -
// r8169 와 시놀 r8168 이 둘 다 10ec:8168 을 잡고, 먼저 올라간 쪽이 카드를 가져간다.
func (idx Index) ResolvePreferring(modaliases []string, loaded map[string]bool, prefer map[string]bool) []Module {
	want := map[string]bool{}
	for _, alias := range modaliases {
		var matched []string
		preferred := false
		for name, m := range idx {
			if m.Matches(alias) {
				matched = append(matched, name)
				if prefer[name] {
					preferred = true
				}
			}
		}
		for _, name := range matched {
			if loaded[name] || (preferred && !prefer[name]) {
				continue
			}
			want[name] = true
		}
	}
	var wanted []string
	for name := range want {
		wanted = append(wanted, name)
	}
	// Deterministic order in, deterministic order out. Without this the boot
	// log would differ run to run for no reason.
	//
	// 입력 순서가 결정적이면 출력도 결정적이어야 한다. 이게 없으면 부팅
	// 로그가 이유 없이 실행마다 달라진다.
	sort.Strings(wanted)

	var out []Module
	seen := map[string]bool{}
	var add func(name string, depth int)
	add = func(name string, depth int) {
		// Dependency chains in a ramdisk are two or three deep; anything
		// longer means a cycle, and a cycle must not hang the boot.
		//
		// 램디스크 안의 의존 사슬은 두세 단계다. 그보다 길면 순환이라는
		// 뜻이고, 순환이 부팅을 멈추게 해서는 안 된다.
		if depth > 8 || seen[name] || loaded[name] {
			return
		}
		m, ok := idx[name]
		if !ok {
			return
		}
		seen[name] = true
		for _, d := range m.Depends {
			add(d, depth+1)
		}
		out = append(out, m)
	}
	for _, name := range wanted {
		add(name, 0)
	}
	return out
}

// DeviceAliases reads the modalias of every device on every bus in sysfs.
//
// The buses are not a fixed list because loading a driver creates new ones.
// virtio_pci claims a PCI device and a virtio bus appears, and that is where
// the devices the disk and network drivers match on are attached. Reading only
// PCI and USB means those devices are never seen. This is re-read on every
// pass, so a bus that has just appeared is picked up on the next one.
//
// DeviceAliases - sysfs 의 모든 버스에 붙은 모든 장치의 modalias 를 읽는다.
//
// 버스를 고정 목록으로 두지 않는 이유: 드라이버를 로드하면 그 드라이버가
// 새 버스를 만든다. virtio_pci 가 PCI 장치를 잡으면 virtio 버스가 생기고,
// 디스크·네트워크 드라이버가 매칭될 장치는 거기 붙는다. PCI 와 USB 만 읽으면
// 그 장치들은 영영 안 보인다. 매 패스마다 다시 읽으므로 새로 생긴 버스가
// 다음 패스에서 잡힌다.
func DeviceAliases(sysfs string) ([]string, error) {
	buses, err := os.ReadDir(filepath.Join(sysfs, "bus"))
	if err != nil {
		return nil, fmt.Errorf("kmod: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, bus := range buses {
		dir := filepath.Join(sysfs, "bus", bus.Name(), "devices")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(dir, e.Name(), "modalias"))
			if err != nil {
				continue
			}
			s := strings.TrimSpace(string(b))
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("kmod: no device aliases under %s", sysfs)
	}
	return out, nil
}

// Loaded reads the set of modules already in the kernel.
// Loaded - 이미 커널에 올라가 있는 모듈 집합을 읽는다.
func Loaded(procModules string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(procModules)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if name, _, ok := strings.Cut(line, " "); ok && name != "" {
			out[normalize(name)] = true
		}
	}
	return out
}

// ReadBytes reads a module's metadata from an image already in memory.
//
// The loader carries its driver pack as one archive on a partition with no
// filesystem, so the modules are unpacked into memory and never become files.
//
// ReadBytes - 이미 메모리에 있는 이미지에서 모듈 메타데이터를 읽는다.
//
// 로더는 드라이버 팩을 파일시스템 없는 파티션 위의 아카이브 하나로 들고
// 다니므로, 모듈은 메모리에서 풀릴 뿐 파일이 되지 않는다.
func ReadBytes(name string, data []byte) (Module, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return Module{}, fmt.Errorf("kmod: %s: %w", name, err)
	}
	sec := f.Section(".modinfo")
	if sec == nil {
		return Module{}, fmt.Errorf("kmod: %s has no .modinfo section, so it is not a kernel module", name)
	}
	raw, err := sec.Data()
	if err != nil {
		return Module{}, fmt.Errorf("kmod: %s: %w", name, err)
	}
	return moduleFrom(name, "", parseModinfo(raw)), nil
}
