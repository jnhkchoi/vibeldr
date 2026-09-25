package kmod

import "testing"

func idxOf(mods ...Module) Index {
	idx := Index{}
	for _, m := range mods {
		idx[m.Name] = m
	}
	return idx
}

func names(mods []Module) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, m.Name)
	}
	return out
}

// The loader's own disk is behind usb_storage, and usb_storage matches no
// device until the disk is there to match. Asking for it by name is what
// opens that door.
//
// 로더 자신의 디스크는 usb_storage 뒤에 있고, usb_storage 는 그 디스크가 있어야
// 매칭된다. 이름으로 요청해야 그 문이 열린다.
func TestByNameLoadsDependenciesFirst(t *testing.T) {
	idx := idxOf(
		Module{Name: "usb_storage", Depends: []string{"usb_common"}},
		Module{Name: "usb_common"},
		Module{Name: "uas", Depends: []string{"usb_storage"}},
		Module{Name: "unrelated"},
	)
	got := names(idx.ByName("uas"))
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	if pos["usb_common"] > pos["usb_storage"] || pos["usb_storage"] > pos["uas"] {
		t.Errorf("wrong order: %v", got)
	}
}

func TestByNameSkipsWhatIsNotThere(t *testing.T) {
	idx := idxOf(Module{Name: "usb_storage"})
	got := names(idx.ByName("usb_storage", "nothing_like_this"))
	if len(got) != 1 || got[0] != "usb_storage" {
		t.Fatalf("got %v", got)
	}
}

// Module names are written both ways and the kernel treats them as the same.
// 모듈 이름은 두 표기로 쓰이고 커널은 둘을 같은 것으로 본다.
func TestByNameAcceptsEitherSpelling(t *testing.T) {
	idx := idxOf(Module{Name: "usb_storage"})
	if got := idx.ByName("usb-storage"); len(got) != 1 {
		t.Fatalf("got %v", names(got))
	}
}
