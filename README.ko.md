# vibeldr

*[English README](README.md)*

[![latest release](https://img.shields.io/github/v/release/jnhkchoi/vibeldr)](https://github.com/jnhkchoi/vibeldr/releases/latest)

일반 PC 에서 **시놀로지 DSM 7.4.1-90080** 을 돌리는, 밑바닥부터 만든 x86 부트로더.
**그래픽(프레임버퍼) 설치 마법사**가 있어 — 모델을 고르고 디스크·랜카드를 마우스로
배치하면, DSM 커널을 패치하고 부트 디스크에 써 줍니다.

**[로더 이미지 다운로드](https://github.com/jnhkchoi/vibeldr/releases/latest)**
— `vibeldr-loader.img.gz` (79MB). USB 에 구워서 부팅하면 됩니다. [설치](#설치) 참고.

> **고지.** vibeldr 은 독립 연구 프로젝트입니다. 시놀로지와 **무관**하며 승인·후원받지
> 않았습니다. DSM 자체는 시놀로지의 저작물이라 여기 **포함되지 않습니다** — `.pat` 는
> 사용자가 직접 준비합니다. 본인 소유 하드웨어에서 학습·개인 용도로 쓰세요.

> **이 프로젝트에 대해.** 헤놀로지(Xpenology) 로더의 원리가 궁금해서 파고들다가,
> 대부분 AI 보조(바이브 코딩)로 구현한 것입니다. 작성자가 코드를 유지보수하지 않고
> **버그 수정이나 기능 추가는 대체로 어렵습니다** — 호기심의 산물로 **있는 그대로**
> 공유합니다.

## 지원 모델

| 모델 | 플랫폼 | 커널 | 베이 |
|---|---|---|---|
| DS918+ | apollolake | 4.4.302 | 4 |
| DS3622xs+ | broadwellnk | 4.4.302 | 12 |
| SA6400 | epyc7002 | 5.10.55 | 12 |

모두 DSM **7.4.1-90080**. KVM/QEMU(Proxmox)에서 USB 로더로 부팅해 **설치 → DSM 웹
설정까지 전 과정 검증**했습니다.

## 무엇이 다른가

- **글자 메뉴가 아니라 그래픽 설치 마법사.** `/dev/fb0` 에 사이드바·드롭다운·마우스
  커서로 그리고, 8단계(시작 → 모델 → 부트 디스크 → 저장소 → 네트워크 → 시리얼 →
  확인 → 설치)로 진행합니다.
- **커널 모듈(LKM) 없음.** 런타임에 호출을 가로채는 대신 vibeldr 은:
  - 부팅 전에 **DSM `bzImage` 를 바이트 패치**해 서명 없는 모듈을 받게 만듭니다
    (부트파라미터 잠금 `LOCK OR → AND`, 램디스크 검사 `JZ → JMP`). 오프셋은 매번
    커널 이미지에서 계산 — 하드코딩 주소 없음.
  - DSM 램디스크에 작은 **유저스페이스 헬퍼**를 심어 디스크 베이 매핑, NIC MAC 강제,
    펌웨어 플래셔 무력화, 드라이브 호환성 DB 수정을 합니다.
- **랜카드의 실제 하드웨어 MAC 을 강제**해서 공유기/DHCP 가 고른 MAC 을 봅니다.
- **자동 저장소 매핑**: legacy 플랫폼은 감지된 컨트롤러에서 `SataPortMap`/`DiskIdxMap`
  파생, DT 플랫폼은 device tree 슬롯 사용.
- **DSM 커널은 설치 때 받습니다.** 시놀로지 공식 다운로드 서버에서 받아 기기에서
  풀기 때문에, 로더가 DSM 커널을 싣고 다니지 않습니다.

## 준비물

- 본인 소유 x86-64 머신(또는 VM)
- 로더용 **USB** 디스크(~1GB) — DSM 은 USB / SATA-DOM 만 부트 장치로 인식합니다
- DSM 설치용 데이터 디스크 최소 1대
- 마법사용 화면·키보드·마우스, DSM 용 유선 랜
- 설치 중 대상 기기의 인터넷 연결 — 로더가 DSM 이미지를 시놀로지에서 직접 받고,
  이후 DSM 웹 어시스턴트가 DSM 본체를 설치합니다(온라인, 또는 직접 올린 `.pat` 로)

## 빌드

선택 사항 — 릴리스 이미지가 정확히 이 명령으로 만들어졌으니, 바이너리를 믿는 대신
직접 만들어 쓰셔도 됩니다. Go (리눅스 헬퍼는 자동 빌드, 도구 자체는
Windows/Linux/macOS 에서 실행):

```bash
go run ./cmd/vibeldr bootstrap --no-embed
```

`work/out/bootstrap.img`(모델 무관 로더 이미지)가 나옵니다. 고른 모델의 DSM
`.pat` 과 드라이버·펌웨어 팩([vibeldr-modules](https://github.com/jnhkchoi/vibeldr-modules)
최신 릴리스)을 **설치 때 받으므로**, 설치 단계에서 대상 기기에 인터넷이 필요합니다.
팩에는 우리 빌드만 있고, 시놀로지 자신의 모듈은 설치 중에 `.pat` 에서 꺼내
합칩니다. `--driver-pack <cpio>` 로 팩을 이미지에 미리 실을 수도 있습니다.

## 설치

이미지는 마법사를 `/dev/fb0` 프레임버퍼에 그리므로 대상 기기에 화면이 필요합니다.
각 기기(또는 VM)마다 이미지 **사본**이 하나씩 필요합니다 — 로더가 패치된 커널을 그
이미지에 되씁니다.

### Proxmox / KVM

Proxmox 호스트에서 실행합니다. VM 은 SeaBIOS + q35 에 CPU 종류와 화면은 기본값을 씁니다.
DSM 은 USB / SATA-DOM 만 부트 장치로 인식하므로 로더는 **제거 가능 USB** 로 붙입니다.
설치는 RAM 에서 돌아가므로 메모리는 4GB 를 줍니다 (확인한 크기).

```bash
# 1) 이미지 사본을 ISO 저장소에 풀기 (VM 하나당 하나)
gunzip -c vibeldr-loader.img.gz > /var/lib/vz/template/iso/vibeldr-900.img

# 2) VM 생성 (virtio, e1000e, rtl8139 NIC 확인됨)
qm create 900 --name vibeldr --machine q35 --cores 2 --memory 4096 \
  --net0 virtio=BC:24:11:00:00:01,bridge=vmbr0

# 3) DSM 용 데이터 디스크 (32GB 이상)
qm set 900 --sata0 local-lvm:32

# 4) 로더 이미지를 제거 가능 USB 로 붙이기; bootindex=1 로 가장 먼저 부팅
qm set 900 --args "-device qemu-xhci,id=xhci,addr=0x18 \
  -drive file=/var/lib/vz/template/iso/vibeldr-900.img,if=none,id=drive-usb0,format=raw \
  -device usb-storage,bus=xhci.0,drive=drive-usb0,id=usb0,bootindex=1,removable=on"
qm set 900 --boot 'order=sata0;net0'

# 5) 시작하고 콘솔 열기
qm start 900
```

직접 빌드한 이미지는 `work/out/bootstrap.img` 이고, 1) 대신 그것을 복사하면 됩니다.

### 베어메탈

1. 이미지를 USB 에 굽습니다 (USB 내용 **삭제됨**):
   - Windows: [balenaEtcher](https://etcher.balena.io/) 는 받은 `.img.gz` 를 그대로
     굽습니다. Rufus 를 쓰면 먼저 `.img` 로 풀어야 합니다.
   - Linux/macOS: `gunzip -c vibeldr-loader.img.gz | sudo dd of=/dev/sdX bs=4M status=progress conv=fsync`
     — `/dev/sdX` 를 USB 장치로 바꾸되 **장치 이름을 꼭 다시 확인**하세요.
2. 대상 기기 BIOS/UEFI 에서 **Secure Boot 끄고**, USB 로 부팅하도록 설정합니다
   (USB 항목이 안 보이면 Legacy/CSM 시도).
3. 전원을 켜면 화면에 마법사가 뜹니다. 따라 진행한 뒤, 브라우저에서
   `http://find.synology.com` 또는 `http://<기기 IP>:5000` 으로 DSM 설정을 마칩니다.

## 마법사 진행 (SA6400 예시)

8단계는 모든 모델이 같고, 모델 카드와 베이 수만 다릅니다 (DS918+ 는 4베이, 나머지 12).

| | |
|---|---|
| **1. 시작** — 감지된 디스크/랜카드 확인 | ![시작](docs/img/wizard-1.png) |
| **2. 모델** — 시놀로지 모델 선택 | ![모델](docs/img/wizard-2.png) |
| **3. 부트 디스크** — 로더 디스크 (DSM 저장소로 안 씀) | ![부트디스크](docs/img/wizard-3.png) |
| **4. 저장소** — 디스크를 베이에 배치 | ![저장소](docs/img/wizard-4.png) |
| **5. 네트워크** — 랜카드/MAC (기본은 실제 MAC 유지) | ![네트워크](docs/img/wizard-5.png) |
| **6. 시리얼** — 모델 규칙 시리얼 (자동/직접) | ![시리얼](docs/img/wizard-6.png) |
| **7. 확인** — 요약 검토 | ![확인](docs/img/wizard-7.png) |
| **8. 설치** — DSM 과 드라이버·펌웨어 팩 다운로드, 시놀로지 원본 모듈 합치기, 패치·기록 후 재부팅 | ![설치](docs/img/wizard-8.png) |

끝나면 DSM 으로 재부팅해 웹 UI 에서 설정을 마칩니다:

![DSM 웹](docs/img/dsm-web.png)

## 동작 원리 (요약)

1. GRUB 이 작은 Alpine 커널 + 우리 initrd 를 올리고, 마법사가 프레임버퍼에 뜹니다.
2. 모델을 고르고 디스크/랜카드를 배치하면, 로더가 그 모델의 DSM 이미지를 시놀로지에서
   받아 풀고, `bzImage` 를 패치하고, 유저스페이스 헬퍼를 DSM 램디스크에 심고, 패치된
   커널을 부트 파티션에 쓰고, 드라이버 팩을 준비합니다.
3. 재부팅하면 DSM 설치기가 뜨고, `.pat` 를 업로드해 DSM 웹에서 설정을 마칩니다.

## 크레딧

vibeldr 은 자체 코드지만, 커뮤니티 로더들과 그 연구 위에 서 있습니다. 특히:

- **[RedPill](https://github.com/RedPill-TTG/redpill-lkm)** — DSM 이 부팅/설치 때
  무엇을 검사하는지(서명 강제, 램디스크 검사, 펌웨어 플래시 차단)의 기준. vibeldr 은
  이를 커널 모듈 없이 재현합니다.
- **[Arc](https://github.com/AuxXxilium)** (`AuxXxilium/arc-modules`),
  **[M-shell / TinyCore-RedPill](https://github.com/PeterSuh-Q3)**
  (`PeterSuh-Q3/tcrp-modules`) — 우리에게 없거나 저쪽이 더 나은 드라이버를
  vibeldr-modules 가 그 팩에서 가져온다 (모듈마다 그 README 에 적었다). 그리고
  `SataPortMap`/`DiskIdxMap`·플래셔 처리의 참고.
- **[ARPL / rr](https://github.com/RROrg/rr)** — 자동화 시놀로지 로더의 선행 사례.
- **[007revad](https://github.com/007revad)**
  ([Synology_HDD_db](https://github.com/007revad/Synology_HDD_db),
  [Synology_M2_volume](https://github.com/007revad/Synology_M2_volume)) —
  서드파티 디스크를 DSM 호환성 DB/룰 엔진에 통과시키는 방법의 참고.
  vibeldr 이 이를 자동으로 적용합니다.

XPEnology 커뮤니티 모든 분께 감사합니다.

## 라이선스

작성자가 정할 예정. `LICENSE` 파일이 추가되기 전까지는 모든 권리 유보. (이런 로더는
보통 GPL-3.0 을 씁니다.)
