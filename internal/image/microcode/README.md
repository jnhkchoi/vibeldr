# 마이크로코드 블롭 스테이징 디렉터리

여기 두 하위 디렉터리에 담긴 벤더 배포 .bin 파일이 `Builder.WithMicrocode`
경로가 픽업하는 조기 마이크로코드의 원본. 파일은 트리에 커밋하지 않는다
(수 MB 크기 + upstream 이 자주 갱신). 필요한 시점에 아래 명령으로 채운다.

## intel-ucode/

출처: https://github.com/intel/Intel-Linux-Processor-Microcode-Data-Files
(linux-firmware 트리의 `intel-ucode/` 와 동일한 세트).

Intel 저장소의 원본 파일명은 `06-03-02` 처럼 확장자가 없지만, `PackIntelUcode`
는 `*.bin` 만 매칭하므로 저장할 때 `.bin` 을 붙인다:

```bash
mkdir -p internal/image/microcode/intel-ucode
cd internal/image/microcode/intel-ucode
curl -s https://api.github.com/repos/intel/Intel-Linux-Processor-Microcode-Data-Files/contents/intel-ucode \
  | python -c "import json,sys;[print(x['name']) for x in json.load(sys.stdin)]" \
  | tr -d '\r' \
  | while read n; do \
      curl -sSfL "https://raw.githubusercontent.com/intel/Intel-Linux-Processor-Microcode-Data-Files/main/intel-ucode/$n" -o "$n.bin"; \
    done
```

## amd-ucode/

출처: https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/plain/amd-ucode/

파일명이 이미 `microcode_amd*.bin` 이므로 그대로 저장.

```bash
mkdir -p internal/image/microcode/amd-ucode
cd internal/image/microcode/amd-ucode
for f in microcode_amd.bin microcode_amd_fam15h.bin microcode_amd_fam16h.bin \
         microcode_amd_fam17h.bin microcode_amd_fam19h.bin microcode_amd_fam1ah.bin; do
  curl -sSfL "https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/plain/amd-ucode/$f" -o "$f"
done
```

## 동작

두 디렉터리 중 하나라도 비어있으면 (또는 없으면) 해당 벤더의
`{intel,amd}-ucode.img` 는 조용히 스킵되고 `Result.Warnings` 에 이유가 남는다.
빌드 자체는 계속 진행되고, 이 경우 GRUB 은 마이크로코드 없이 DSM 램디스크만
로드한다.
