// Package catalog holds the Synology platform, model and serial-rule data.
//
// All of it is embedded in the binary, so the data is versioned along with the
// tool and an offline build always succeeds. An optional remote refresh can
// only ever add to it.
//
// Package catalog - 시놀로지 플랫폼 / 모델 / 시리얼 규칙 카탈로그.
//
// 전부 바이너리에 embed. 데이터가 툴과 함께 버전 관리되니 오프라인 빌드도
// 항상 성공. 원격 리프레시(선택) 는 오직 추가만 가능.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed platforms.json
var platformsJSON []byte

//go:embed serials.json
var serialsJSON []byte

// Platform describes one Synology hardware platform (the "arch" in a .pat URL).
// Platform - 시놀로지 하드웨어 플랫폼 하나 (.pat URL 의 "arch" 부분).
type Platform struct {
	Name string `json:"-"`
	// DT reports whether the platform identifies disks through a device tree.
	// Non-DT platforms need SataPortMap/DiskIdxMap instead.
	//
	// DT - 이 플랫폼이 디스크를 device tree 로 식별하는지. 아니면
	// SataPortMap/DiskIdxMap 이 그 일을 해야 한다.
	DT bool `json:"dt"`
	// Flags are CPU feature flags this platform requires (e.g. "movbe").
	// Flags - 이 플랫폼이 요구하는 CPU 기능 플래그 ("movbe" 등).
	Flags []string `json:"flags"`
	// NoFlags are CPU features that must be masked off on the kernel cmdline.
	// NoFlags - 커널 커맨드라인에서 꺼야 하는 CPU 기능.
	NoFlags []string `json:"noflags"`
	// Kernels maps a DSM product version ("7.4") to a kernel version ("5.10.55").
	// Kernels - DSM 제품 버전("7.4") → 커널 버전("5.10.55") 대응표.
	Kernels map[string]string `json:"kernels"`
	Models  []string          `json:"models"`
}

// KernelMajor returns the leading component of the kernel version for the
// given product version, e.g. 5 for "5.10.55". It returns 0 when unknown.
//
// KernelMajor - 해당 제품 버전의 커널 메이저 번호 ("5.10.55" 면 5).
// 모르는 조합이면 0.
func (p *Platform) KernelMajor(productVer string) int {
	kver, ok := p.Kernels[productVer]
	if !ok {
		return 0
	}
	var major int
	if _, err := fmt.Sscanf(kver, "%d.", &major); err != nil {
		return 0
	}
	return major
}

// NoFlagsContains reports whether the platform lists the given CPU feature as
// one that must be masked off (e.g. "x2apic" -> the nox2apic kernel flag).
//
// NoFlagsContains - 이 플랫폼이 해당 CPU 기능을 꺼야 할 것으로 적어 뒀는지
// ("x2apic" 이면 커널 플래그 nox2apic 로 이어진다).
func (p *Platform) NoFlagsContains(flag string) bool {
	for _, f := range p.NoFlags {
		if strings.EqualFold(f, flag) {
			return true
		}
	}
	return false
}

// SerialRule describes how a valid serial number and MAC address are formed
// for a given model.
//
// SerialRule - 해당 모델에서 올바른 시리얼 번호와 MAC 주소가 어떻게
// 만들어지는지에 대한 규칙.
type SerialRule struct {
	Prefix []string `json:"prefix"`
	Middle []string `json:"middle"`
	// Suffix is either "alpha" or "numeric".
	// Suffix - "alpha" 아니면 "numeric" 둘 중 하나.
	Suffix string `json:"suffix"`
	// MacPre is the 6 hex digit OUI prefix; empty means the Synology default.
	// MacPre - 16진수 6자리 OUI 접두사. 비어 있으면 시놀로지 기본값.
	MacPre string `json:"macpre"`
}

// DefaultMacPrefix is Synology's own OUI, used when a model has no specific one.
// DefaultMacPrefix - 시놀로지 자체 OUI. 모델 전용 값이 없을 때 쓴다.
const DefaultMacPrefix = "001132"

// Catalog is the loaded, indexed view of the embedded data.
// Catalog - embed 된 데이터를 읽어 색인까지 만들어 둔 것.
type Catalog struct {
	platforms map[string]*Platform
	serials   map[string]SerialRule
	// byModel maps an upper-cased model name to its platform name.
	// byModel - 대문자로 맞춘 모델 이름 → 플랫폼 이름.
	byModel map[string]string
}

type platformsFile struct {
	Platforms map[string]*Platform `json:"platforms"`
}

// Load parses the embedded catalog. It is cheap enough to call once at startup
// and returns an error only if the embedded data itself is corrupt, which would
// be a build-time mistake rather than a runtime condition.
//
// Load - embed 된 카탈로그를 파싱한다. 시작할 때 한 번 부를 만큼 가볍고,
// 오류는 embed 된 데이터 자체가 깨졌을 때만 나온다. 그건 런타임 상황이
// 아니라 빌드 시점의 실수다.
func Load() (*Catalog, error) {
	var pf platformsFile
	if err := json.Unmarshal(platformsJSON, &pf); err != nil {
		return nil, fmt.Errorf("embedded platforms.json: %w", err)
	}
	var serials map[string]SerialRule
	if err := json.Unmarshal(serialsJSON, &serials); err != nil {
		return nil, fmt.Errorf("embedded serials.json: %w", err)
	}

	c := &Catalog{
		platforms: pf.Platforms,
		serials:   serials,
		byModel:   make(map[string]string),
	}
	for name, p := range c.platforms {
		p.Name = name
		for _, m := range p.Models {
			c.byModel[strings.ToUpper(m)] = name
		}
	}
	return c, nil
}

// PlatformNames returns every known platform, sorted.
// PlatformNames - 아는 플랫폼 전부를 정렬해서 돌려준다.
func (c *Catalog) PlatformNames() []string {
	names := make([]string, 0, len(c.platforms))
	for n := range c.platforms {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Platform looks up a platform by name.
// Platform - 이름으로 플랫폼을 찾는다.
func (c *Catalog) Platform(name string) (*Platform, bool) {
	p, ok := c.platforms[strings.ToLower(name)]
	return p, ok
}

// PlatformForModel resolves a model name (case-insensitive) to its platform.
// PlatformForModel - 모델 이름으로 플랫폼을 찾는다 (대소문자 무시).
func (c *Catalog) PlatformForModel(model string) (*Platform, bool) {
	name, ok := c.byModel[strings.ToUpper(strings.TrimSpace(model))]
	if !ok {
		return nil, false
	}
	return c.platforms[name], true
}

// Models returns every known model name, sorted.
// Models - 아는 모델 이름 전부를 정렬해서 돌려준다.
func (c *Catalog) Models() []string {
	models := make([]string, 0, len(c.byModel))
	for _, p := range c.platforms {
		models = append(models, p.Models...)
	}
	sort.Strings(models)
	return models
}

// CuratedModels are the three models the boot TUI offers.
//
// Most of the 96 in the catalog have never been tried here. Rather than let
// somebody pick one and find out the hard way, the list is narrowed to three,
// chosen so that between them they cover the paths that can differ:
//
//   - DS918+ (apollolake, kernel 4.4) - 4 bays, the small end, and the model
//     that carries the i915 transcoding path.
//
//   - DS3622xs+ (broadwellnk, kernel 4.4) - 12 bays on the same kernel, so
//     the bay mapping is exercised at a different width.
//
//   - SA6400 (epyc7002, kernel 5.10) - the other kernel generation, and a
//     device-tree platform, which takes a different storage-mapping path.
//
// Passing on all three means the kernel patch, the ramdisk hooks and the
// module loading work across most combinations. The CLI can still reach the
// whole catalog through Models.
//
// CuratedModels - 부트 TUI 에 노출할 대표 모델 세 개.
//
// 카탈로그의 96 개는 대부분 여기서 시도된 적이 없다. 사용자가 임의로 골라
// 삽질하는 걸 피하려고 세 개로 좁힌다. 셋이 함께 서로 다른 경로를 덮도록
// 골랐다:
//
//   - DS918+ (apollolake, 커널 4.4) - 4 베이, 작은 쪽. i915 트랜스코딩
//     경로를 가진 모델.
//
//   - DS3622xs+ (broadwellnk, 커널 4.4) - 같은 커널에 12 베이. 베이 매핑을
//     다른 폭에서 확인하는 자리.
//
//   - SA6400 (epyc7002, 커널 5.10) - 다른 커널 세대이자 device tree 플랫폼.
//     스토리지 매핑이 아예 다른 길로 간다.
//
// 이 셋을 통과하면 커널 패치·램디스크 훅·모듈 로드가 대부분의 조합에서
// 도는 셈이다. CLI 는 여전히 전체 카탈로그 (Models) 에 접근할 수 있다.
var CuratedModels = []string{"DS918+", "DS3622xs+", "SA6400"}

// modelBays is how many internal disk bays a model admits to having.
//
// The number belongs to the model, not to the motherboard. Put a DS918+ on a
// machine with twelve SATA ports and it still has four bays, so the screen
// that asks which port goes in which bay must draw exactly this many rows.
//
// The source is maxdisks in each model's own /etc/synoinfo.conf, inside its
// ramdisk. To check it again, unpack that model's rd.gz from its .pat (LZMA1
// alone, then cpio) and read synoinfo.conf. Measured on 7.4.1-90080:
//
//	DS918+     maxdisks="4"   internalportcfg="0xf"
//	DS3622xs+  maxdisks="12"  internalportcfg="0xfff"
//	SA6400     maxdisks="12"  (device tree, so no portcfg)
//
// An unknown model gives 0. The caller must not guess at that point but use
// its own default: a wrong bay count throws DiskIdxMap out entirely.
//
// modelBays - 모델이 인정하는 내장 디스크 베이 수.
//
// 이 숫자는 모델이 정하는 것이지 메인보드가 정하는 게 아니다. SATA 포트가
// 12 개인 기계에 DS918+ 를 올려도 베이는 4 개다. 그래서 "어느 포트를 몇 번
// 베이에 놓을지" 를 고르는 화면은 이 수만큼만 줄을 만들어야 한다.
//
// 출처는 각 모델 자신의 램디스크 안 /etc/synoinfo.conf 의 maxdisks 이다.
// 다시 확인하려면 그 모델 .pat 의 rd.gz 를 풀어 (LZMA1 alone + cpio)
// synoinfo.conf 를 보면 된다. 위 표가 7.4.1-90080 기준 실측값이다.
//
// 모르는 모델은 0 을 돌려준다. 호출자는 그때 추측하지 말고 자기 기본값을
// 써야 한다 - 틀린 베이 수는 DiskIdxMap 을 통째로 어긋나게 만든다.
var modelBays = map[string]int{
	"DS918+":    4,
	"DS3622xs+": 12,
	"SA6400":    12,
}

// BaysOf is a model's internal bay count, or 0 when the model is unknown.
// BaysOf - 모델의 내장 베이 수. 모르는 모델이면 0.
func BaysOf(model string) int {
	if n, ok := modelBays[strings.TrimSpace(model)]; ok {
		return n
	}
	for k, v := range modelBays {
		if strings.EqualFold(k, strings.TrimSpace(model)) {
			return v
		}
	}
	return 0
}

// SerialRule returns the serial-number rule for a model. The second result is
// false when the model has no known rule, in which case a generated serial
// cannot be trusted to unlock model-locked DSM features.
//
// SerialRule - 모델의 시리얼 번호 규칙. 두 번째 반환값이 false 면 아는
// 규칙이 없다는 뜻이고, 그때 생성한 시리얼로는 모델 종속 DSM 기능이
// 풀린다고 믿을 수 없다.
func (c *Catalog) SerialRule(model string) (SerialRule, bool) {
	r, ok := c.serials[strings.ToUpper(strings.TrimSpace(model))]
	if ok {
		return r, true
	}
	// serials.json keys preserve the vendor's own casing; fall back to a scan.
	// serials.json 의 키는 벤더 표기를 그대로 쓴다. 못 찾으면 훑어서 찾는다.
	for k, v := range c.serials {
		if strings.EqualFold(k, strings.TrimSpace(model)) {
			return v, true
		}
	}
	return SerialRule{}, false
}

// ProductVersions returns the DSM product versions a platform supports, sorted
// newest first.
//
// ProductVersions - 플랫폼이 지원하는 DSM 제품 버전. 최신순 정렬.
func (c *Catalog) ProductVersions(platform string) []string {
	p, ok := c.Platform(platform)
	if !ok {
		return nil
	}
	vers := make([]string, 0, len(p.Kernels))
	for v := range p.Kernels {
		vers = append(vers, v)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(vers)))
	return vers
}
