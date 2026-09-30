package kmod

import (
	"reflect"
	"testing"
)

// A module that uses a symbol only one other module exports depends on it,
// even with an empty depends=; a symbol two modules export adds nothing.
//
// 다른 모듈 하나만 내보내는 기호를 쓰는 모듈은 depends= 가 비어 있어도 그 모듈에
// 의존하고, 두 모듈이 내보내는 기호는 아무것도 더하지 않는다.
func TestLinkSymbols(t *testing.T) {
	idx := Index{
		"virtio_scsi":   {Name: "virtio_scsi", Depends: []string{"virtio"}, needs: []string{"blk_mq_virtio_map_queues", "printk", "dup_sym"}},
		"blk_mq_virtio": {Name: "blk_mq_virtio", exports: []string{"blk_mq_virtio_map_queues"}},
		"virtio":        {Name: "virtio"},
		"a":             {Name: "a", exports: []string{"dup_sym"}},
		"b":             {Name: "b", exports: []string{"dup_sym"}},
	}
	idx.LinkSymbols()
	if got := idx["virtio_scsi"].Depends; !reflect.DeepEqual(got, []string{"virtio", "blk_mq_virtio"}) {
		t.Fatalf("depends %v", got)
	}
	if idx["virtio_scsi"].needs != nil || idx["blk_mq_virtio"].exports != nil {
		t.Fatal("symbol lists kept")
	}
	if got := idx.ByName("virtio_scsi"); len(got) != 3 || got[len(got)-1].Name != "virtio_scsi" {
		t.Fatalf("load order %v", names(got))
	}
}
