package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Events queued in the ramdisk stage travel into DSM with the settings and
// reach the webhook from there, in order and ahead of dsm_up; the queue is
// gone afterwards.
//
// 램디스크 단계에서 대기열에 넣은 이벤트는 설정과 함께 DSM 안으로 가고, 거기서
// 순서대로 dsm_up 보다 먼저 웹훅에 닿는다. 그 뒤 대기열은 없다.
func TestNotifyQueueReachesWebhook(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p map[string]any
		if err := json.Unmarshal(body, &p); err != nil {
			t.Errorf("body %q: %v", body, err)
		}
		mu.Lock()
		events = append(events, p["event"].(string)+":"+str(p["stage"]))
		mu.Unlock()
	}))
	defer srv.Close()

	ramdiskRoot, dsmRoot := t.TempDir(), t.TempDir()
	notifyRamdiskRoot, notifyDSMRoot = ramdiskRoot, dsmRoot
	defer func() { notifyRamdiskRoot, notifyDSMRoot = "", "" }()
	cfg, _ := json.Marshal(map[string]any{"webhook_url": srv.URL})
	if err := os.WriteFile(filepath.Join(ramdiskRoot, notifyRamdiskConfig), cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	queueNotify("boot_started", nil)
	queueNotify("error", map[string]string{"stage": "drivers", "message": "x"})
	copyNotify(dsmRoot)
	sendQueuedNotify()
	if err := notify("dsm_up", nil); err != nil {
		t.Fatal(err)
	}

	want := []string{"boot_started:", "error:drivers", "dsm_up:"}
	if len(events) != len(want) {
		t.Fatalf("events %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events %v, want %v", events, want)
		}
	}
	if _, err := os.Stat(dsmRoot + notifyDSMQueue); !os.IsNotExist(err) {
		t.Errorf("queue left behind: %v", err)
	}
}

// Without settings nothing is queued, and a boot without them clears what an
// earlier one copied into DSM.
//
// 설정이 없으면 아무것도 대기열에 넣지 않고, 설정 없는 부팅은 이전 부팅이 DSM 에
// 복사한 것을 지운다.
func TestNotifyWithoutSettings(t *testing.T) {
	ramdiskRoot, dsmRoot := t.TempDir(), t.TempDir()
	notifyRamdiskRoot, notifyDSMRoot = ramdiskRoot, dsmRoot
	defer func() { notifyRamdiskRoot, notifyDSMRoot = "", "" }()
	if err := os.MkdirAll(dsmRoot+notifyDSMDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dsmRoot+notifyDSMConfig, []byte(`{"webhook_url":"http://old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	queueNotify("boot_started", nil)
	if _, err := os.Stat(ramdiskRoot + notifyRamdiskQueue); !os.IsNotExist(err) {
		t.Errorf("queued without settings: %v", err)
	}
	copyNotify(dsmRoot)
	if _, err := os.Stat(dsmRoot + notifyDSMConfig); !os.IsNotExist(err) {
		t.Errorf("old settings kept: %v", err)
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
