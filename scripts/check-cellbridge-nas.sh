#!/usr/bin/env bash
# 只读诊断 Halo + CellBridge 原生部署。不 apt、不改服务、不发 AT、不启动 ADB、不加载模块。
set -euo pipefail

BRIDGE_NAME=mavo-pcm-bridge.armv7
BRIDGE_HASH=88d47c15e61d1428a59c821fed804c2e6490e82859a085062f21966b58d167fc
APR_NAME=qdc507_aprv3.ko
APR_HASH=3d82d3dec4f1e323201bba87156df9d41438e08314097353f2607f9117211d4a
VOICE_NAME=qdc507_voice.ko
VOICE_HASH=ed3821682d5309969a01c764192c83feff9669c61ef237c69475cd1619cf296c

PACKAGES=(alsa-utils adb pcscd libccid ca-certificates iproute2 sqlite3 curl)
COMMANDS=(arecord aplay adb pcscd ip sqlite3 curl)

usage() {
  cat <<'EOF'
用法：bash scripts/check-cellbridge-nas.sh [--runtime-dir DIR] [--test-root DIR]

只读检查 CPU、Debian 版本、依赖、USB、ALSA、QDC507 文件、systemd 和 /healthz。
不会安装软件包，不会 stop/start 服务，不会打开 ADB，不会发 AT，也不会执行厂商程序。

缺少三个运行文件时，在发布包目录执行：
  ./source/scripts/fetch-qdc507-runtime.sh
  sudo ./install --runtime-dir ./qdc507-runtime
来源是 MaVo 固定提交 0443dfdaf8aec086fd76ba2ee9152fd908114524 的 Resources/ModuleVoice：
  https://github.com/moluncn/mavo
EOF
}

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 2
}

RUNTIME_DIR=""
TEST_ROOT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --runtime-dir)
      [[ $# -ge 2 ]] || fail '--runtime-dir 缺少目录'
      RUNTIME_DIR=$2
      shift 2
      ;;
    --test-root)
      [[ $# -ge 2 ]] || fail '--test-root 缺少目录'
      TEST_ROOT=$2
      shift 2
      ;;
    *) fail "未知参数：$1" ;;
  esac
done

if [[ -n "$TEST_ROOT" ]]; then
  [[ -d "$TEST_ROOT" && ! -L "$TEST_ROOT" ]] || fail '--test-root 必须是已存在的真实目录'
  TEST_ROOT=$(CDPATH= cd -- "$TEST_ROOT" && pwd)
  [[ "$TEST_ROOT" != / ]] || fail '--test-root 不能是 /'
  export PATH="$TEST_ROOT/usr/bin:$TEST_ROOT/bin:$PATH"
fi

OPT_QDC=${TEST_ROOT}/opt/halo/qdc507
UNIT_PATH=${TEST_ROOT}/etc/systemd/system/halo-cellbridge.service
if [[ -n "$TEST_ROOT" && -f "$TEST_ROOT/etc/os-release" ]]; then
  OS_RELEASE=$TEST_ROOT/etc/os-release
else
  OS_RELEASE=/etc/os-release
fi
if [[ -n "$TEST_ROOT" ]]; then
  USB_ROOT=$TEST_ROOT/sys/bus/usb/devices
else
  USB_ROOT=/sys/bus/usb/devices
fi

problems=0
runtime_problems=0
note_problem() { problems=$((problems + 1)); }
note_runtime() { runtime_problems=$((runtime_problems + 1)); note_problem; }

section() { printf '\n## %s\n' "$1"; }

os_value() {
  awk -F= -v key="$1" '$1 == key { gsub(/"/, "", $2); print $2; exit }' "$2"
}

section 'CPU 与系统'
arch=$(uname -m 2>/dev/null || true)
if [[ -z "$arch" ]]; then
  printf 'cpu: unknown\n'
  note_problem
elif [[ "$arch" == x86_64 ]]; then
  printf 'cpu: x86_64 ok\n'
else
  printf 'cpu: %s mismatch（需要 x86_64）\n' "$arch"
  note_problem
fi
if [[ ! -f "$OS_RELEASE" ]]; then
  printf 'os: unknown（没有 os-release）\n'
  note_problem
else
  os_id=$(os_value ID "$OS_RELEASE")
  os_ver=$(os_value VERSION_ID "$OS_RELEASE")
  if [[ "$os_id" == debian && "$os_ver" == 13 ]]; then
    printf 'os: debian 13 ok\n'
  elif [[ -z "$os_id" && -z "$os_ver" ]]; then
    printf 'os: unknown\n'
    note_problem
  else
    printf 'os: %s %s mismatch（需要 Debian 13）\n' "${os_id:-unknown}" "${os_ver:-unknown}"
    note_problem
  fi
fi

section '依赖'
declare -A pkg_state=()
if command -v dpkg-query >/dev/null 2>&1; then
  for pkg in "${PACKAGES[@]}"; do
    status=$(dpkg-query -W -f '${Status}' "$pkg" 2>/dev/null || true)
    if [[ "$status" == "install ok installed" ]]; then
      printf 'pkg %s: present\n' "$pkg"
    elif [[ -z "$status" || "$status" == *not-installed* || "$status" == *config-files* ]]; then
      printf 'pkg %s: missing\n' "$pkg"
      note_problem
    else
      printf 'pkg %s: unknown（%s）\n' "$pkg" "$status"
      note_problem
    fi
  done
else
  for pkg in "${PACKAGES[@]}"; do
    printf 'pkg %s: unknown（没有 dpkg-query）\n' "$pkg"
    note_problem
  done
fi
for cmd in "${COMMANDS[@]}"; do
  if command -v "$cmd" >/dev/null 2>&1; then
    printf 'cmd %s: present\n' "$cmd"
  else
    printf 'cmd %s: missing\n' "$cmd"
    note_problem
  fi
done

section 'USB'
if [[ ! -d "$USB_ROOT" ]]; then
  printf 'usb: unknown（没有 %s）\n' "$USB_ROOT"
  note_problem
else
  usb_count=0
  for dev in "$USB_ROOT"/*; do
    [[ -e "$dev" ]] || continue
    base=$(basename -- "$dev")
    [[ "$base" == *:* || "$base" == usb* ]] && continue
    [[ -f "$dev/idVendor" && -f "$dev/idProduct" ]] || continue
    vendor=$(tr -d '[:space:]' < "$dev/idVendor")
    product=$(tr -d '[:space:]' < "$dev/idProduct")
    manufacturer=""
    product_name=""
    [[ -r "$dev/manufacturer" ]] && manufacturer=$(tr -d '\n' < "$dev/manufacturer")
    [[ -r "$dev/product" ]] && product_name=$(tr -d '\n' < "$dev/product")
    printf 'usb %s: %s:%s %s %s\n' "$base" "$vendor" "$product" "$manufacturer" "$product_name"
    usb_count=$((usb_count + 1))
  done
  if [[ "$usb_count" -eq 0 ]]; then
    printf 'usb: 未发现 USB 设备\n'
    note_problem
  fi
  printf '只列出总线端口和厂商号。不读取序列号，也不打开串口。\n'
fi

section 'ALSA'
print_cards() {
  local tool=$1
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf '%s: unknown（命令 missing）\n' "$tool"
    note_problem
    return
  fi
  local out rc
  set +e
  out=$("$tool" -l 2>&1)
  rc=$?
  set -e
  if [[ "$rc" -ne 0 && -z "$out" ]]; then
    printf '%s: unknown（命令失败）\n' "$tool"
    note_problem
    return
  fi
  printf '%s 实际输出：\n%s\n' "$tool" "$out"
  [[ "$rc" -eq 0 ]] || note_problem
}
print_cards arecord
print_cards aplay
printf '电话页的录音和播放设备必须填上面这块模块对应的声卡。plughw:1,0 只是界面初始占位，不能当作已经识别到模块。\n'

check_runtime_dir() {
  local label=$1 dir=$2
  printf '目录 %s：%s\n' "$label" "$dir"
  if [[ -L "$dir" ]]; then
    printf 'runtime-dir: 拒绝（符号链接）\n'
    note_runtime
    return
  fi
  if [[ ! -e "$dir" ]]; then
    printf 'runtime-dir: missing\n'
    note_runtime
  elif [[ ! -d "$dir" ]]; then
    printf 'runtime-dir: 拒绝（不是目录）\n'
    note_runtime
    return
  fi
  if [[ -d "$dir" ]]; then
    local dir_perm
    dir_perm=$(stat -c '%a' -- "$dir")
    if (( (8#$dir_perm & 8#022) != 0 )); then
      printf 'runtime-dir: 拒绝（对组或其他用户可写）\n'
      note_problem
    fi
  fi
  local name expect src actual perm
  while read -r name expect; do
    src=$dir/$name
    if [[ ! -e "$src" && ! -L "$src" ]]; then
      printf 'runtime %s: missing\n' "$name"
      note_runtime
      continue
    fi
    if [[ -L "$src" || ! -f "$src" ]]; then
      printf 'runtime %s: 拒绝（不是普通文件）\n' "$name"
      note_runtime
      continue
    fi
    if [[ ! -r "$src" ]]; then
      printf 'runtime %s: unknown（无法读取）\n' "$name"
      note_runtime
      continue
    fi
    perm=$(stat -c '%a' -- "$src")
    if (( (8#$perm & 8#022) != 0 )); then
      printf 'runtime %s: 拒绝（对组或其他用户可写）\n' "$name"
      note_runtime
      continue
    fi
    actual=$(sha256sum -- "$src" | awk '{ print $1 }')
    if [[ "$actual" == "$expect" ]]; then
      printf 'runtime %s: ok\n' "$name"
    else
      printf 'runtime %s: 哈希不一致\n  期望 %s\n  实际 %s\n' "$name" "$expect" "$actual"
      note_runtime
    fi
  done <<EOF
$BRIDGE_NAME $BRIDGE_HASH
$APR_NAME $APR_HASH
$VOICE_NAME $VOICE_HASH
EOF
}

section 'QDC507 运行文件'
check_runtime_dir '已安装目录' "$OPT_QDC"
if [[ -n "$RUNTIME_DIR" ]]; then
  check_runtime_dir '指定目录' "$RUNTIME_DIR"
fi
if [[ "$runtime_problems" -eq 0 ]]; then
  printf '三个运行文件哈希一致。这仍不等于模块 UAC/ADB 已就绪，也不等于通话已验证。\n'
else
  cat <<'EOF'
大疆语音文件未配齐或未通过校验，不能把服务说成语音可用。
取得方式是发布包里的下载脚本。它只从 MaVo 这一处固定提交取三个文件并校验，不能改地址，也不会把文件写进 git：
  ./source/scripts/fetch-qdc507-runtime.sh
  sudo ./install --runtime-dir ./qdc507-runtime
来源：https://github.com/moluncn/mavo/tree/0443dfdaf8aec086fd76ba2ee9152fd908114524/Resources/ModuleVoice
EOF
fi

section '服务与健康'
if [[ ! -e "$UNIT_PATH" ]]; then
  printf 'service: missing（没有 halo-cellbridge.service）\n'
  note_problem
elif ! command -v systemctl >/dev/null 2>&1; then
  printf 'service: unknown（没有 systemctl）\n'
  note_problem
else
  set +e
  active=$(systemctl is-active halo-cellbridge.service 2>&1)
  active_rc=$?
  enabled=$(systemctl is-enabled halo-cellbridge.service 2>&1)
  set -e
  if [[ "$active" == *"System has not been booted"* || "$active" == *"Failed to connect"* ]]; then
    printf 'service: unknown（%s）\n' "$active"
    note_problem
  else
    printf 'service active: %s\n' "$active"
    printf 'service enabled: %s\n' "$enabled"
    [[ "$active_rc" -eq 0 ]] || note_problem
  fi
fi

listen=$(sed -n 's/^Environment=VOCAT_ADDR=//p' "$UNIT_PATH" 2>/dev/null | head -n 1 || true)
port=${listen##*:}
[[ "$port" =~ ^[0-9]+$ ]] || port=7575
health_url="http://127.0.0.1:${port}/healthz"
if ! command -v curl >/dev/null 2>&1; then
  printf 'health: unknown（没有 curl）\n'
  note_problem
else
  set +e
  body=$(curl --fail --silent --show-error --max-time 2 "$health_url" 2>&1)
  rc=$?
  set -e
  if [[ "$rc" -eq 0 && "$body" =~ \"status\"[[:space:]]*:[[:space:]]*\"ok\" ]]; then
    printf 'health: ok %s\n%s\n' "$health_url" "$body"
  elif [[ "$rc" -eq 0 ]]; then
    printf 'health: mismatch（HTTP 响应不是 Halo 健康状态）\n'
    note_problem
  elif [[ "$body" == *"Could not resolve"* || "$body" == *"command not found"* ]]; then
    printf 'health: unknown（%s）\n' "$body"
    note_problem
  else
    printf 'health: down %s\n%s\n' "$health_url" "$body"
    note_problem
  fi
fi

section '电话页要填的值'
cat <<EOF
模块运行文件目录：/opt/halo/qdc507
ADB 路径：adb
ADB socket：tcp:127.0.0.1:5038
录音设备和播放设备：使用上面 arecord/aplay 的实际声卡，不要把 plughw:1,0 写成模块已经就绪。
程序安装成功、模块 UAC/ADB 就绪、大疆音频验证成功，是三件不同的事。
模块 root ADB 和双向通话：unknown，本检查不会打开 ADB 或发起通话；需连接模块后在电话页验证。
EOF

section '结论'
if [[ "$problems" -eq 0 ]]; then
  printf '未发现缺失或错误。这仍不等于模块 UAC/ADB 或通话已经验证。\n'
  exit 0
fi
printf '发现 %s 项缺失、不一致或未知。请按上面的 missing、拒绝、哈希不一致和 unknown 逐项处理。\n' "$problems"
printf 'missing 表示已经确认没有；unknown 表示这次检查无法判断。\n'
exit 1
