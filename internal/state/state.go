// Package state records what was actually built, and from which configuration.
//
// Every stage stores the hash of the config that produced it, so "out of date"
// is decided by comparison rather than by guesswork.
//
// Package state - 어떤 설정으로부터 무엇이 실제로 만들어졌는지 기록.
//
// 각 단계가 자신을 만들어낸 설정의 해시를 함께 기록하니, "낡음" 은 추측이
// 아니라 비교로 판정됨.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Stage is how far the build has got.
// Stage - 빌드가 어디까지 진행됐는지.
type Stage string

const (
	StageEmpty     Stage = "empty"
	StageFetched   Stage = "fetched"
	StageExtracted Stage = "extracted"
	StagePatched   Stage = "patched"
	StageBuilt     Stage = "built"
)

// Order lets the CLI work out which stages are already satisfied.
// Order - 어느 단계가 이미 만족됐는지 CLI 가 추론할 수 있게 하는 순서표.
var Order = []Stage{StageEmpty, StageFetched, StageExtracted, StagePatched, StageBuilt}

func (s Stage) rank() int {
	for i, v := range Order {
		if v == s {
			return i
		}
	}
	return 0
}

// AtLeast reports whether s has got at least as far as want.
// AtLeast - s 가 want 이상 진행됐는지.
func (s Stage) AtLeast(want Stage) bool { return s.rank() >= want.rank() }

// State is what gets persisted to work/state.json.
// State - work/state.json 으로 영속화되는 상태.
type State struct {
	// ConfigHash is the sha256 of the loader.yaml that produced this state.
	// ConfigHash - 이 상태를 만들어낸 loader.yaml 의 sha256.
	ConfigHash string `json:"config_hash"`
	Stage      Stage  `json:"stage"`

	Model      string `json:"model"`
	Platform   string `json:"platform"`
	DSMVersion string `json:"dsm_version"`
	Kernel     string `json:"kernel"`

	// Resolved identity, so a rebuild keeps the same serial and MACs instead of
	// silently handing DSM a new machine identity.
	//
	// 확정된 identity. 재빌드해도 같은 시리얼과 MAC 을 쓰게 해서, DSM 에게
	// 조용히 다른 머신을 건네는 일이 없게 한다.
	Serial string   `json:"serial"`
	MACs   []string `json:"macs"`

	PatPath string `json:"pat_path,omitempty"`
	PatMD5  string `json:"pat_md5,omitempty"`

	// Hashes of the extracted DSM originals. A change here means Synology
	// shipped a different build than the one this state was made from.
	//
	// 추출한 DSM 원본의 해시. 값이 달라졌다면 시놀로지가 이 상태를 만든
	// 때와 다른 빌드를 배포했다는 뜻이다.
	ZImageSHA256  string `json:"zimage_sha256,omitempty"`
	RamdiskSHA256 string `json:"ramdisk_sha256,omitempty"`

	Cmdline   string    `json:"cmdline,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Path is where the state file sits for a given work directory.
// Path - work 디렉터리에 대한 state 파일 위치.
func Path(workDir string) string { return filepath.Join(workDir, "state.json") }

// Load reads the state file. A missing file is not an error: it returns the
// empty state, which is exactly what a first run should see.
//
// Load - state 파일 읽기. 파일이 없어도 에러가 아니고 빈 상태를 돌려줌.
// 첫 실행이면 그게 정확히 봐야 하는 값이다.
func Load(workDir string) (*State, error) {
	data, err := os.ReadFile(Path(workDir))
	if errors.Is(err, os.ErrNotExist) {
		return &State{Stage: StageEmpty}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state.json (delete it to start over): %w", err)
	}
	if s.Stage == "" {
		s.Stage = StageEmpty
	}
	return &s, nil
}

// Save writes the state file atomically, so a run that is interrupted cannot
// leave a half-written file behind for the next run to fail parsing.
//
// Save - state 파일을 atomic 하게 씀. 도중에 끊긴 실행이 반쯤 쓰인 상태를
// 남기지 않게 (다음 실행에서 파싱 실패로 이어지므로).
func (s *State) Save(workDir string) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", workDir, err)
	}
	s.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	data = append(data, '\n')

	final := Path(workDir)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("finalise state: %w", err)
	}
	return nil
}

// HashConfig is the sha256 of the config file's bytes.
// HashConfig - config 파일 바이트의 sha256.
func HashConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Stale reports whether the recorded state came from a different config than
// the one now on disk.
//
// Stale - 기록된 state 가 지금 디스크 위의 config 와 다른 config 로 만들어진
// 것인지 판단.
func (s *State) Stale(configHash string) bool {
	return s.ConfigHash != "" && s.ConfigHash != configHash
}
