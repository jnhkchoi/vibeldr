package kmod

import (
	"debug/elf"
	"sort"
	"strings"
)

// Dependencies from symbols.
//
// A module's depends= lists the modules it needs, as worked out when it was
// built. That list only holds what was a module in the tree it was built in.
// The driver packs are built against a DSM kernel tree whose .config had some
// of those pieces built in, so a module can need a symbol that the running
// DSM kernel lacks and the pack supplies in a module of its own - and say
// nothing about it. On SA6400, virtio_blk and virtio_scsi use
// blk_mq_virtio_map_queues from blk-mq-virtio, and net_failover uses failover,
// all with an empty depends=, and loading them first fails with an unknown
// symbol.
//
// So the symbols a module leaves undefined are matched against the symbols the
// other modules export (their __ksymtab_ entries), and a module exporting one
// is added to the depends when it is the only one that does.
//
// 기호로 구한 의존성.
//
// 모듈의 depends= 는 빌드될 때 계산한, 필요한 모듈 목록이다. 그 목록에는 빌드한
// 트리에서 모듈이었던 것만 들어간다. 드라이버 팩은 .config 에서 일부가 내장으로
// 된 DSM 커널 트리로 빌드되므로, 모듈이 돌고 있는 DSM 커널에는 없고 팩이 따로
// 모듈로 주는 기호를 쓰면서 아무 말도 하지 않을 수 있다. SA6400 에서 virtio_blk
// 와 virtio_scsi 는 blk-mq-virtio 의 blk_mq_virtio_map_queues 를, net_failover 는
// failover 를 쓰는데 모두 depends= 가 비어 있고, 그것들을 먼저 올리면 알 수 없는
// 기호로 실패한다.
//
// 그래서 모듈이 정의하지 않고 남긴 기호를 다른 모듈이 내보내는 기호(그 __ksymtab_
// 항목)와 대조하고, 그것을 내보내는 모듈이 하나뿐이면 depends 에 더한다.

// ksymtabPrefix marks an exported symbol's table entry.
// ksymtabPrefix - 내보낸 기호의 표 항목 표시.
const ksymtabPrefix = "__ksymtab_"

// readSymbols takes the undefined and the exported symbols out of a module.
// readSymbols - 모듈에서 정의되지 않은 기호와 내보낸 기호를 꺼낸다.
func readSymbols(f *elf.File) (needs, exports []string) {
	syms, err := f.Symbols()
	if err != nil {
		return nil, nil
	}
	for _, s := range syms {
		switch {
		case s.Section == elf.SHN_UNDEF && s.Name != "":
			needs = append(needs, s.Name)
		case strings.HasPrefix(s.Name, ksymtabPrefix):
			exports = append(exports, strings.TrimPrefix(s.Name, ksymtabPrefix))
		}
	}
	return needs, exports
}

// LinkSymbols adds the symbol dependencies to every module in idx and lets go
// of the symbol lists. Call it once the index is complete.
//
// LinkSymbols - idx 의 모든 모듈에 기호 의존성을 더하고 기호 목록은 놓아 준다.
// 색인이 다 찬 뒤에 한 번 부른다.
func (idx Index) LinkSymbols() {
	exporters := map[string][]string{}
	for name, m := range idx {
		for _, s := range m.exports {
			exporters[s] = append(exporters[s], name)
		}
	}
	for name, m := range idx {
		have := map[string]bool{name: true}
		for _, d := range m.Depends {
			have[d] = true
		}
		var added []string
		for _, s := range m.needs {
			by := exporters[s]
			if len(by) != 1 || have[by[0]] {
				continue
			}
			have[by[0]] = true
			added = append(added, by[0])
		}
		sort.Strings(added)
		m.Depends = append(m.Depends, added...)
		m.needs, m.exports = nil, nil
		idx[name] = m
	}
}
