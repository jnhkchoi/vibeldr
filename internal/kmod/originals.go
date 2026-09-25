package kmod

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

// originals.go puts Synology's own modules, taken from the DSM .pat, into a
// driver pack.
//
// The published pack holds only our builds; Synology's modules are theirs and
// are not handed out. During the install the loader has the .pat anyway, so
// it takes the originals from there and merges them in with the same rules
// vibeldr-modules' tools/merge_originals.py applies at build time. Which
// modules the pack keeps was already decided there with the originals in, so
// the result is the pack as built.
//
// Rules:
//  1. A name in both: the original goes in, under our file's name.
//  2. keepOurs keeps ours: modules the loader replaces on purpose with a newer
//     build of ours (atlantic - the stock one refuses cards outside its SubID
//     list, see cmd/vibeldr-init/extradrivers_aquantia.go).
//  3. An original we do not have goes in only if it drives hardware (has a
//     device alias). Synology-internal modules (synobios and the like) are
//     left to DSM - the loader must not load them early.
//  4. Every module an original that goes in depends on goes in as well.
//  5. An original built for another kernel release is never used.
//
// The names that came from Synology are written to OriginalsList. The helper
// in the ramdisk reads it to prefer an original when one of ours claims the
// same device. Merging a pack that already has originals in it gives the same
// pack again.
//
// originals.go - DSM .pat 에서 꺼낸 시놀로지 자신의 모듈을 드라이버 팩에 넣는다.
//
// 공개하는 팩에는 우리 빌드만 있다. 시놀로지 모듈은 시놀로지 것이라 배포하지
// 않는다. 설치 중에는 로더가 어차피 .pat 을 갖고 있으므로 거기서 원본을 꺼내,
// vibeldr-modules 의 tools/merge_originals.py 가 빌드 때 쓰는 것과 같은 규칙으로
// 합친다. 팩에 어떤 모듈을 남길지는 거기서 이미 원본을 넣은 채로 정했으므로,
// 결과는 빌드된 팩 그대로다.
//
// 규칙:
//  1. 양쪽에 다 있는 이름: 원본을 우리 파일 이름으로 넣는다.
//  2. keepOurs 는 우리 것을 남긴다. 로더가 일부러 더 새 우리 빌드로 바꾸는
//     모듈이다 (atlantic - 기본 모듈은 SubID 목록 밖의 카드를 거부한다.
//     cmd/vibeldr-init/extradrivers_aquantia.go 참고).
//  3. 우리에게 없는 원본은 하드웨어를 구동할 때 (장치 alias 가 있을 때) 만
//     넣는다. 시놀로지 내부 모듈 (synobios 등) 은 DSM 에 맡긴다. 로더가 그것들을
//     일찍 로드하면 안 된다.
//  4. 들어가는 원본이 의존하는 모듈도 전부 넣는다.
//  5. 다른 커널 release 로 빌드된 원본은 절대 쓰지 않는다.
//
// 시놀에서 온 이름은 OriginalsList 에 적는다. 램디스크의 헬퍼가 이것을 읽어,
// 우리 모듈이 같은 장치를 잡을 때 원본을 우선한다. 이미 원본이 들어간 팩을 다시
// 합쳐도 같은 팩이 나온다.

// OriginalsList is the pack entry naming the modules that are Synology's own,
// one normalised name per line.
//
// OriginalsList - 시놀 자신의 모듈을 적은 팩 항목. 한 줄에 정규화한 이름 하나.
const OriginalsList = "vibeldr-originals.txt"

// keepOurs are the modules where our build stays even with an original at hand.
// keepOurs - 원본이 있어도 우리 빌드를 남기는 모듈.
var keepOurs = map[string]bool{"atlantic": true}

// deviceAlias matches an alias that names a device, so the module drives
// hardware. fs-, net-pf-, crypto-, char-major- and the like do not.
//
// deviceAlias - 장치를 가리키는 alias (하드웨어 드라이버). fs-, net-pf-,
// crypto-, char-major- 같은 것은 아니다.
var deviceAlias = regexp.MustCompile(`^(pci|usb|virtio|scsi|hid|input|acpi|pnp|dmi|sdio|mdio|serio|vmbus|` +
	`ieee1394|i2c|spi|platform|of|pcmcia|mmc|ssb|bcma|nvme):`)

// OriginalsReport says what MergeOriginals did, by normalised module name.
// OriginalsReport - MergeOriginals 가 한 일. 정규화한 모듈 이름으로.
type OriginalsReport struct {
	// Replaced took the place of ours; Added were not in the pack.
	// Replaced 는 우리 것 자리에 들어갔고, Added 는 팩에 없던 것이다.
	Replaced, Added []string
	// Kept stayed ours (keepOurs or another kernel release).
	// Kept 는 우리 것으로 남았다 (keepOurs 이거나 커널 release 가 다름).
	Kept []string
	// WrongRelease are originals built for another kernel release.
	// WrongRelease 는 다른 커널 release 로 빌드된 원본.
	WrongRelease []string
}

// MergeOriginals returns pack with the originals merged in (the rules above)
// and OriginalsList written. pack is left as it is.
//
// MergeOriginals - 원본을 합치고 (위 규칙) OriginalsList 를 쓴 팩을 돌려준다.
// pack 은 건드리지 않는다.
func MergeOriginals(pack, originals Pack) (Pack, OriginalsReport, error) {
	var rep OriginalsReport
	release := PackRelease(pack)
	if release == "" {
		return nil, rep, errors.New("kmod: the pack has no module with a vermagic")
	}

	ours := map[string]string{} // normalised name -> file name in the pack / 정규화 이름 -> 팩 안 파일 이름
	for name := range pack {
		if strings.HasSuffix(name, ".ko") {
			if n := normalize(name); ours[n] == "" {
				ours[n] = name
			}
		}
	}
	type original struct {
		file string
		mod  Module
	}
	orig := map[string]original{}
	for name, data := range originals {
		if !strings.HasSuffix(name, ".ko") {
			continue
		}
		m, err := ReadBytes(baseName(name), data)
		if err != nil {
			continue
		}
		orig[m.Name] = original{file: name, mod: m}
	}
	wrong := map[string]bool{}
	for n, o := range orig {
		if firstField(o.mod.Vermagic) != release {
			wrong[n] = true
			rep.WrongRelease = append(rep.WrongRelease, n)
		}
	}

	out := make(Pack, len(pack)+len(orig)+1)
	for name, data := range pack {
		out[name] = data
	}
	var todo []string
	for _, n := range sortedKeys(orig) {
		o := orig[n]
		if file, have := ours[n]; have {
			if keepOurs[n] || wrong[n] {
				rep.Kept = append(rep.Kept, n)
				continue
			}
			out[file] = originals[o.file]
			rep.Replaced = append(rep.Replaced, n)
			todo = append(todo, o.mod.Depends...)
			continue
		}
		if !wrong[n] && drivesHardware(o.mod) {
			todo = append(todo, n)
		}
	}
	// What the originals that go in depend on goes in too (rule 4). One already
	// in the pack is the original (rule 1) or kept on purpose (rule 2).
	//
	// 들어가는 원본이 의존하는 것도 같이 넣는다 (규칙 4). 이미 팩에 있으면 원본이거나
	// (규칙 1) 일부러 둔 것이다 (규칙 2).
	take := map[string]bool{}
	for len(todo) > 0 {
		n := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		o, ok := orig[n]
		if !ok || take[n] || wrong[n] || ours[n] != "" {
			continue
		}
		take[n] = true
		todo = append(todo, o.mod.Depends...)
	}
	for _, n := range sortedKeys(take) {
		o := orig[n]
		out[baseName(o.file)] = originals[o.file]
		rep.Added = append(rep.Added, n)
	}

	names := append(append([]string{}, rep.Replaced...), rep.Added...)
	sort.Strings(names)
	list := strings.Join(names, "\n")
	if list != "" {
		list += "\n"
	}
	out[OriginalsList] = []byte(list)
	sort.Strings(rep.WrongRelease)
	return out, rep, nil
}

// PackRelease is the kernel release the pack's modules were built for, the
// first field of their vermagic.
//
// PackRelease - 팩 모듈이 빌드된 커널 release. vermagic 의 첫 칸이다.
func PackRelease(p Pack) string {
	for _, name := range p.Names() {
		if !strings.HasSuffix(name, ".ko") {
			continue
		}
		if m, err := ReadBytes(name, p[name]); err == nil && m.Vermagic != "" {
			return firstField(m.Vermagic)
		}
	}
	return ""
}

func drivesHardware(m Module) bool {
	for _, a := range m.Aliases {
		if deviceAlias.MatchString(a) {
			return true
		}
	}
	return false
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
