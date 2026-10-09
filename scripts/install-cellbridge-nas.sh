#!/usr/bin/env bash
# Debian 13 / Linux x86_64 原生安装 Halo + CellBridge。
# 只在用户执行本脚本后才会安装软件包、写 systemd 或初始化管理员。
# --check-only 只诊断。--test-root 只改隔离目录，不能跳过 SHA-256、架构或厂商哈希。
set -euo pipefail
umask 022

# 与 internal/cellbridge/qdc507/audio.go 的 trustedRuntimeArtifacts 原样一致。
BRIDGE_NAME=mavo-pcm-bridge.armv7
BRIDGE_HASH=88d47c15e61d1428a59c821fed804c2e6490e82859a085062f21966b58d167fc
BRIDGE_MODE=0755
APR_NAME=qdc507_aprv3.ko
APR_HASH=3d82d3dec4f1e323201bba87156df9d41438e08314097353f2607f9117211d4a
APR_MODE=0644
VOICE_NAME=qdc507_voice.ko
VOICE_HASH=ed3821682d5309969a01c764192c83feff9669c61ef237c69475cd1619cf296c
VOICE_MODE=0644

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PACKAGES=(alsa-utils adb pcscd libccid ca-certificates iproute2 sqlite3 curl)

usage() {
  cat <<'EOF'
用法：sudo ./install [--runtime-dir DIR] [--fetch-runtime] [--binary PATH]
      sudo bash source/scripts/install-cellbridge-nas.sh [选项]

安装 Debian 13 / x86_64 上的 Halo 原生服务到 /opt/halo，并启用 systemd 单元
halo-cellbridge.service。Docker compose 不会被启动。

选项：
  --runtime-dir DIR   校验并安装三个 QDC507 厂商文件到 /opt/halo/qdc507
  --fetch-runtime     先执行 fetch-qdc507-runtime.sh，再安装它下载的文件
  --binary PATH       使用这份 halo 二进制，而不是发布包顶层的 halo-linux-amd64
  --check-only        只诊断。不 apt、不改服务、不初始化管理员、不写运行文件
  --test-root DIR     隔离测试根。仍执行 SHA-256、架构、Debian 版本和厂商哈希
  --help              显示本说明

管理员密码只从标准输入读取：终端里交互输入，或由管道传入一行。
密码不会放进参数、环境变量或日志。已有管理员时不会读取、也不会重置密码。
没有运行文件时仍可安装 Halo 和读卡器 IMS，但大疆语音文件未配齐。

取得运行文件：
  ./source/scripts/fetch-qdc507-runtime.sh
  sudo ./install --runtime-dir ./qdc507-runtime
EOF
}

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

CHECK_ONLY=0
FETCH=0
BINARY=""
RUNTIME_DIR=""
TEST_ROOT=""
APT_GET_CMD=apt-get
SYSTEMCTL_CMD=systemctl
CURL_CMD=curl

while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --check-only) CHECK_ONLY=1; shift ;;
    --fetch-runtime) FETCH=1; shift ;;
    --binary)
      [[ $# -ge 2 ]] || fail '--binary 缺少路径'
      BINARY=$2
      shift 2
      ;;
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
    *) fail "未知参数：$1（可用 --help）" ;;
  esac
done

if [[ -n "$TEST_ROOT" ]]; then
  [[ -d "$TEST_ROOT" && ! -L "$TEST_ROOT" ]] || fail '--test-root 必须是已存在的真实目录'
  TEST_ROOT=$(CDPATH= cd -- "$TEST_ROOT" && pwd)
  [[ "$TEST_ROOT" != / ]] || fail '--test-root 不能是 /'
  printf '注意：--test-root %s 只隔离路径。SHA-256、架构、Debian 版本和厂商哈希仍会校验，不能靠环境变量跳过。\n' "$TEST_ROOT"
  export PATH="$TEST_ROOT/usr/bin:$TEST_ROOT/bin:$PATH"
  if [[ "$CHECK_ONLY" -eq 0 ]]; then
    for tool in apt-get systemctl curl; do
      [[ -x "$TEST_ROOT/usr/bin/$tool" && ! -L "$TEST_ROOT/usr/bin/$tool" ]] || fail "隔离安装必须提供模拟命令 $TEST_ROOT/usr/bin/$tool；不会回落到真实系统命令"
    done
    APT_GET_CMD=$TEST_ROOT/usr/bin/apt-get
    SYSTEMCTL_CMD=$TEST_ROOT/usr/bin/systemctl
    CURL_CMD=$TEST_ROOT/usr/bin/curl
  fi
fi

OPT_HALO=${TEST_ROOT}/opt/halo
UNIT_DIR=${TEST_ROOT}/etc/systemd/system
UNIT_PATH=$UNIT_DIR/halo-cellbridge.service
BACKUP_ROOT=${TEST_ROOT}/var/backups/halo-cellbridge
DB_PATH=$OPT_HALO/data/halo.db
BINARY_SNAP=""
RUNTIME_SNAP=""
cleanup_snap() {
  [[ -z "${RUNTIME_SNAP:-}" || ! -d "$RUNTIME_SNAP" ]] || rm -rf -- "$RUNTIME_SNAP"
  [[ -z "${BINARY_SNAP:-}" || ! -d "$BINARY_SNAP" ]] || rm -rf -- "$BINARY_SNAP"
  return 0
}
trap cleanup_snap EXIT

find_binary() {
  if [[ -n "$BINARY" ]]; then
    if [[ ! -f "$BINARY" || -L "$BINARY" ]]; then
      printf '错误：二进制不存在或是符号链接：%s\n' "$BINARY" >&2
      return 1
    fi
    BINARY=$(CDPATH= cd -- "$(dirname -- "$BINARY")" && pwd)/$(basename -- "$BINARY")
    return 0
  fi
  local candidate
  for candidate in "$SCRIPT_DIR/../../halo-linux-amd64" "$SCRIPT_DIR/../halo-linux-amd64"; do
    if [[ -f "$candidate" && ! -L "$candidate" ]]; then
      BINARY=$(CDPATH= cd -- "$(dirname -- "$candidate")" && pwd)/$(basename -- "$candidate")
      return 0
    fi
  done
  printf '错误：找不到 halo-linux-amd64。请在发布包顶层运行，或传入 --binary。\n' >&2
  return 1
}

verify_binary() {
  find_binary || return 1
  local sums dir base expected actual
  dir=$(dirname -- "$BINARY")
  base=$(basename -- "$BINARY")
  sums=$dir/SHA256SUMS
  if [[ ! -f "$sums" || -L "$sums" ]]; then
    printf '错误：缺少 %s，拒绝安装未校验的二进制\n' "$sums" >&2
    return 1
  fi
  expected=$(awk -v name="$base" '$2 == name { print $1; exit }' "$sums")
  if [[ -z "$expected" ]]; then
    printf '错误：SHA256SUMS 没有 %s 这一行\n' "$base" >&2
    return 1
  fi
  if [[ "$CHECK_ONLY" -eq 0 ]]; then
    BINARY_SNAP=$(mktemp -d /tmp/halo-install-binary.XXXXXXXX) || return 1
    chmod 700 "$BINARY_SNAP"
    install -m 0700 -- "$BINARY" "$BINARY_SNAP/halo" || return 1
    BINARY=$BINARY_SNAP/halo
  fi
  actual=$(sha256sum -- "$BINARY" | awk '{ print $1 }')
  if [[ "$actual" != "$expected" ]]; then
    printf '错误：二进制 SHA-256 与 SHA256SUMS 不一致\n' >&2
    return 1
  fi
  if ! perl -e '
    open my $f, "<:raw", $ARGV[0] or die "open: $!\n";
    my $n = read $f, my $h, 20;
    die "ELF 头部过短\n" if !defined($n) || $n < 20;
    my @b = unpack("C20", $h);
    die "不是 ELF\n" if $b[0] != 127 || $b[1] != 69 || $b[2] != 76 || $b[3] != 70;
    die "不是 64 位小端 ELF\n" if $b[4] != 2 || $b[5] != 1;
    my $mach = $b[18] | ($b[19] << 8);
    die "不是 x86_64 ELF\n" if $mach != 62;
  ' "$BINARY"; then
    printf '错误：二进制架构不是 Linux x86_64\n' >&2
    return 1
  fi
  local ver
  if ! ver=$("$BINARY" version); then
    printf '错误：无法执行二进制的 version\n' >&2
    return 1
  fi
  if [[ ! "$ver" =~ ^vocat[[:space:]]+[0-9]+\.[0-9]+\.[0-9]+-cellbridge(-preview\.[0-9]+)?([[:space:]]|$) ]]; then
    printf '错误：不是 Halo CellBridge 集成版，version 输出不匹配\n' >&2
    return 1
  fi
  printf '二进制校验通过：%s\n%s\n' "$BINARY" "$ver"
}

os_value() {
  local key=$1 file=$2
  awk -F= -v key="$key" '$1 == key { gsub(/"/, "", $2); print $2; exit }' "$file"
}

verify_host() {
  if [[ "$(uname -m)" != x86_64 ]]; then
    printf '错误：需要 Linux x86_64，当前是 %s\n' "$(uname -m)" >&2
    return 1
  fi
  local release=${TEST_ROOT}/etc/os-release
  [[ -n "$TEST_ROOT" && -f "$release" ]] || release=/etc/os-release
  if [[ ! -f "$release" ]]; then
    printf '错误：无法读取 os-release，不能确认 Debian 13\n' >&2
    return 1
  fi
  local id version_id
  id=$(os_value ID "$release")
  version_id=$(os_value VERSION_ID "$release")
  if [[ "$id" != debian || "$version_id" != 13 ]]; then
    printf '错误：需要 Debian 13，当前是 %s %s\n' "${id:-unknown}" "${version_id:-unknown}" >&2
    return 1
  fi
}

runtime_names() {
  printf '%s %s %s\n' "$BRIDGE_NAME" "$BRIDGE_HASH" "$BRIDGE_MODE"
  printf '%s %s %s\n' "$APR_NAME" "$APR_HASH" "$APR_MODE"
  printf '%s %s %s\n' "$VOICE_NAME" "$VOICE_HASH" "$VOICE_MODE"
}

snapshot_runtime() {
  local dir=$1
  local snap
  snap=$(mktemp -d)
  chmod 700 "$snap"
  local failed=0
  if [[ -L "$dir" ]]; then
    printf '拒绝：运行时目录是符号链接：%s\n' "$dir" >&2
    rm -rf -- "$snap"
    return 1
  fi
  if [[ ! -d "$dir" ]]; then
    printf 'missing：运行时目录不存在：%s\n' "$dir" >&2
    rm -rf -- "$snap"
    return 1
  fi
  local dir_perm
  dir_perm=$(stat -c '%a' -- "$dir")
  if (( (8#$dir_perm & 8#022) != 0 )); then
    printf '拒绝：运行时目录对组或其他用户可写：%s\n' "$dir" >&2
    rm -rf -- "$snap"
    return 1
  fi
  local name expect mode src dest actual
  while read -r name expect mode; do
    src=$dir/$name
    dest=$snap/$name
    if [[ ! -e "$src" && ! -L "$src" ]]; then
      printf 'missing：%s\n' "$name" >&2
      failed=1
      continue
    fi
    if [[ -L "$src" || ! -f "$src" ]]; then
      printf '拒绝：%s 不是普通文件（符号链接或特殊文件已被拒绝）\n' "$name" >&2
      failed=1
      continue
    fi
    local perm
    perm=$(stat -c '%a' -- "$src")
    if (( (8#$perm & 8#022) != 0 )); then
      printf '拒绝：%s 对组或其他用户可写\n' "$name" >&2
      failed=1
      continue
    fi
    if ! perl -MFcntl -e '
      use Fcntl qw(O_RDONLY O_NOFOLLOW O_WRONLY O_CREAT O_EXCL);
      my ($src, $dest) = @ARGV;
      sysopen(my $in, $src, O_RDONLY | O_NOFOLLOW) or die "open: $!\n";
      my @st = stat($in) or die "stat: $!\n";
      my $mode = $st[2];
      die "not regular\n" if (($mode & 0170000) != 0100000);
      die "writable\n" if ($mode & 022);
      sysopen(my $out, $dest, O_WRONLY | O_CREAT | O_EXCL, 0600) or die "create: $!\n";
      my $buf;
      while (1) {
        my $n = sysread($in, $buf, 1048576);
        die "read: $!\n" unless defined $n;
        last if $n == 0;
        my $off = 0;
        while ($off < length($buf)) {
          my $w = syswrite($out, $buf, length($buf) - $off, $off);
          die "write: $!\n" unless defined $w;
          $off += $w;
        }
      }
      close($out) or die "close: $!\n";
    ' "$src" "$dest"; then
      printf '拒绝：%s 无法按普通文件做私有快照\n' "$name" >&2
      failed=1
      continue
    fi
    actual=$(sha256sum -- "$dest" | awk '{ print $1 }')
    if [[ "$actual" != "$expect" ]]; then
      printf '哈希不一致：%s\n  期望 %s\n  实际 %s\n' "$name" "$expect" "$actual" >&2
      failed=1
    else
      printf '运行文件哈希一致：%s\n' "$name" >&2
    fi
  done < <(runtime_names)
  if [[ "$failed" -ne 0 ]]; then
    rm -rf -- "$snap"
    return 1
  fi
  printf '%s\n' "$snap"
}

publish_runtime() {
  local snap=$1
  mkdir -p -- "$OPT_HALO/qdc507"
  chmod 0755 "$OPT_HALO/qdc507"
  local name expect mode src tmp actual
  while read -r name expect mode; do
    src=$snap/$name
    tmp=$(mktemp -p "$OPT_HALO/qdc507" ".${name}.XXXXXX")
    cp -p -- "$src" "$tmp"
    chmod "$mode" "$tmp"
    actual=$(sha256sum -- "$tmp" | awk '{ print $1 }')
    if [[ "$actual" != "$expect" ]]; then
      rm -f -- "$tmp"
      fail "安装前复核 $name 失败，已留下备份（如有），未声明安装成功"
    fi
    mv -f -- "$tmp" "$OPT_HALO/qdc507/$name"
  done < <(runtime_names)
  rm -rf -- "$snap"
}

docker_conflict() {
  local docker_bin=""
  if [[ -n "$TEST_ROOT" ]]; then
    [[ -x "$TEST_ROOT/usr/bin/docker" ]] || return 1
    docker_bin=$TEST_ROOT/usr/bin/docker
  elif command -v docker >/dev/null 2>&1; then
    docker_bin=docker
  else
    return 1
  fi
  local containers
  containers=$("$docker_bin" ps --filter label=com.docker.compose.project=halo-cellbridge --format '{{.ID}}' 2>/dev/null) || containers=""
  [[ -z "$containers" ]] || return 0
  "$docker_bin" ps --format '{{.Names}}' 2>/dev/null | grep -qx 'halo-cellbridge'
}

default_fetch_dir() {
  local parent
  parent=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
  if [[ -f "$parent/halo-linux-amd64" || -f "$parent/install" ]]; then
    printf '%s\n' "$parent/qdc507-runtime"
  else
    printf '%s\n' "$(pwd)/qdc507-runtime"
  fi
}

if [[ "$CHECK_ONLY" -eq 1 ]]; then
  [[ "$FETCH" -eq 0 ]] || fail '--check-only 不会下载运行文件'
  verify_host || true
  if ! verify_binary; then
    printf '发布包二进制未能校验。下面仍只做本机诊断。\n' >&2
  fi
  check_args=()
  [[ -n "$TEST_ROOT" ]] && check_args+=(--test-root "$TEST_ROOT")
  [[ -n "$RUNTIME_DIR" ]] && check_args+=(--runtime-dir "$RUNTIME_DIR")
  exec "$SCRIPT_DIR/check-cellbridge-nas.sh" "${check_args[@]}"
fi

[[ "$(id -u)" -eq 0 || -n "$TEST_ROOT" ]] || fail '请用 root 执行安装，例如 sudo ./install。诊断请用 --check-only。'

verify_host
verify_binary
unit_src=$SCRIPT_DIR/../deploy/halo-cellbridge.service
[[ -f "$unit_src" && ! -L "$unit_src" ]] || fail '找不到 deploy/halo-cellbridge.service'
for path in "$OPT_HALO" "$OPT_HALO/bin" "$OPT_HALO/data" "$OPT_HALO/qdc507" "$UNIT_PATH" "$BACKUP_ROOT"; do
  [[ ! -L "$path" ]] || fail "安装目标是符号链接，拒绝覆盖：$path"
done
if docker_conflict; then
  fail 'Halo CellBridge Docker 部署正在运行；请先停止它，再安装原生服务。'
fi
if [[ "$FETCH" -eq 1 ]]; then
  "$APT_GET_CMD" update
  "$APT_GET_CMD" install -y "${PACKAGES[@]}"
  fetch_dir=${RUNTIME_DIR:-$(default_fetch_dir)}
  bash "$SCRIPT_DIR/fetch-qdc507-runtime.sh" --output-dir "$fetch_dir"
  RUNTIME_DIR=$fetch_dir
fi

if [[ -n "$RUNTIME_DIR" ]]; then
  RUNTIME_SNAP=$(snapshot_runtime "$RUNTIME_DIR") || fail '运行文件未通过校验。原有 /opt/halo 未修改，未声明安装成功。'
else
  printf '大疆语音文件未配齐：本次未提供 --runtime-dir。Halo 与读卡器 IMS 仍可安装，大疆音频不可用。\n'
fi

if docker_conflict; then
  [[ -z "$RUNTIME_SNAP" ]] || rm -rf -- "$RUNTIME_SNAP"
  fail 'Docker 容器 halo-cellbridge 正在运行。请先在 source 目录执行 docker compose -f compose.nas.yml down，再安装原生服务。不会同时启动两份服务。'
fi

if [[ "$FETCH" -eq 0 ]]; then
  "$APT_GET_CMD" update
  "$APT_GET_CMD" install -y "${PACKAGES[@]}"
fi

if [[ -e "$UNIT_PATH" ]]; then
  "$SYSTEMCTL_CMD" stop halo-cellbridge.service || fail '停止已有 halo-cellbridge.service 失败。数据未替换，未声明安装成功。'
fi

BACKUP_PATH=""
if [[ -d "$OPT_HALO" || -e "$UNIT_PATH" ]]; then
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  mkdir -p -- "$BACKUP_ROOT"
  chmod 0700 "$BACKUP_ROOT"
  BACKUP_PATH=$(mktemp -d -p "$BACKUP_ROOT" "$stamp.XXXXXXXX")
  if [[ -d "$OPT_HALO/data" ]]; then
    cp -a -- "$OPT_HALO/data" "$BACKUP_PATH/data" || fail '备份数据目录失败。现有文件未替换，未声明安装成功。'
  fi
  if [[ -f "$OPT_HALO/bin/halo" && ! -L "$OPT_HALO/bin/halo" ]]; then
    mkdir -p -- "$BACKUP_PATH/bin"
    cp -a -- "$OPT_HALO/bin/halo" "$BACKUP_PATH/bin/halo" || fail '备份二进制失败。现有文件未替换，未声明安装成功。'
  fi
  if [[ -f "$UNIT_PATH" ]]; then
    cp -a -- "$UNIT_PATH" "$BACKUP_PATH/halo-cellbridge.service" || fail '备份服务单元失败。现有文件未替换，未声明安装成功。'
  fi
  cat > "$BACKUP_PATH/RESTORE.txt" <<EOF
这是安装前的 Halo 备份。本安装器不会自动回滚，也不会删除这份备份。
数据目录：data/
二进制：bin/halo
服务单元：halo-cellbridge.service
恢复时先停止 halo-cellbridge.service，再把需要的文件拷回原位置。
EOF
  printf '已备份：%s\n' "$BACKUP_PATH"
fi

mkdir -p -- "$OPT_HALO/bin" "$OPT_HALO/data" "$OPT_HALO/qdc507"
chmod 0755 "$OPT_HALO" "$OPT_HALO/bin" "$OPT_HALO/qdc507"
chmod 0750 "$OPT_HALO/data"

need_admin=0
if [[ ! -e "$DB_PATH" || ( -f "$DB_PATH" && ! -s "$DB_PATH" && ! -L "$DB_PATH" ) ]]; then
  need_admin=1
else
  [[ ! -L "$DB_PATH" && -f "$DB_PATH" ]] || fail '数据库不是普通文件。未初始化管理员，未声明安装成功。'
  admin_count=$(sqlite3 -readonly "$DB_PATH" 'SELECT count(*) FROM admins WHERE id=1;') || fail '无法只读查询管理员。未写入密码，未声明安装成功。'
  admin_count=${admin_count//$'\r'/}
  admin_count=${admin_count//$'\n'/}
  [[ "$admin_count" =~ ^[0-9]+$ ]] || fail "管理员查询结果无法识别。未写入密码，未声明安装成功。"
  if [[ "$admin_count" -ge 1 ]]; then
    printf '已有管理员，跳过初始化，不会读取或重置密码。\n'
  else
    need_admin=1
  fi
fi

if [[ "$need_admin" -eq 1 ]]; then
  password=""
  if [[ -t 0 ]]; then
    read -rsp 'Halo 管理员密码（不会回显）: ' password </dev/tty || fail '无法从终端读取管理员密码。不会写入默认密码。'
    printf '\n' >/dev/tty
  else
    IFS= read -r password || true
  fi
  if [[ -z "$password" ]]; then
    fail '没有读到管理员密码。请在终端交互输入，或向标准输入传入一行密码。不会写入默认密码，未声明安装成功。'
  fi
  printf '%s\n' "$password" | env -u HALO_SETUP_PASSWORD -u VOCAT_BOOTSTRAP_PASSWORD -u VOCAT_ADMIN_PASSWORD \
    "$BINARY" bootstrap-admin --database "$DB_PATH" || {
      password=""
      unset password
      fail "管理员初始化失败。备份：${BACKUP_PATH:-无}。未声明安装成功。"
    }
  password=""
  unset password
  printf '已初始化管理员。\n'
fi

stage=$(mktemp -d -p "$OPT_HALO" '.stage.XXXXXX')
chmod 700 "$stage"
install -m 0755 "$BINARY" "$stage/halo"
mv -f -- "$stage/halo" "$OPT_HALO/bin/halo"
rmdir "$stage" 2>/dev/null || rm -rf -- "$stage"

unit_src=$SCRIPT_DIR/../deploy/halo-cellbridge.service
[[ -f "$unit_src" && ! -L "$unit_src" ]] || fail '找不到 deploy/halo-cellbridge.service。未声明安装成功。'
mkdir -p -- "$UNIT_DIR"
unit_tmp=$(mktemp -p "$UNIT_DIR" '.halo-cellbridge.service.XXXXXX')
install -m 0644 "$unit_src" "$unit_tmp"
mv -f -- "$unit_tmp" "$UNIT_PATH"

if [[ -n "$RUNTIME_SNAP" ]]; then
  publish_runtime "$RUNTIME_SNAP"
  RUNTIME_SNAP=""
fi

"$SYSTEMCTL_CMD" daemon-reload
"$SYSTEMCTL_CMD" enable halo-cellbridge.service
"$SYSTEMCTL_CMD" start halo-cellbridge.service || fail "启动服务失败。备份：${BACKUP_PATH:-无}。未声明安装成功。"

listen=$(sed -n 's/^Environment=VOCAT_ADDR=//p' "$UNIT_PATH" | head -n 1)
port=${listen##*:}
[[ "$port" =~ ^[0-9]+$ ]] || port=7575
health_url="http://127.0.0.1:${port}/healthz"
if ! health_body=$("$CURL_CMD" --fail --silent --show-error --max-time 2 --retry 30 --retry-delay 1 --retry-connrefused --retry-max-time 30 "$health_url"); then
  fail "健康检查失败：$health_url。备份：${BACKUP_PATH:-无}。未声明安装成功。"
fi
[[ "$health_body" =~ \"status\"[[:space:]]*:[[:space:]]*\"ok\" ]] || fail '健康检查失败：响应不是 Halo 的健康状态'
"$SYSTEMCTL_CMD" is-active --quiet halo-cellbridge.service || fail '健康检查失败：Halo 服务未运行'
main_pid=$("$SYSTEMCTL_CMD" show --property=MainPID --value halo-cellbridge.service)
[[ "$main_pid" =~ ^[0-9]+$ && "$main_pid" -gt 0 ]] || fail '健康检查失败：Halo 服务没有有效主进程'
main_exe=$(readlink -f -- "/proc/$main_pid/exe") || fail '健康检查失败：无法确认 Halo 主进程'
[[ "$main_exe" == "$OPT_HALO/bin/halo" ]] || fail '健康检查失败：响应进程不是本次安装的 Halo'

printf '健康检查 %s\n%s\n' "$health_url" "$health_body"
printf '网页：http://127.0.0.1:%s\n' "$port"
if [[ -z "$TEST_ROOT" ]] && command -v ip >/dev/null 2>&1; then
  while read -r addr; do
    [[ -n "$addr" ]] || continue
    printf '网页：http://%s:%s\n' "$addr" "$port"
  done < <(ip -4 -o addr show scope global 2>/dev/null | awk '{ print $4 }' | cut -d/ -f1)
fi
printf '程序安装成功，健康检查已通过。\n'
printf '系统设置 → 自动更新：新安装默认每 6 小时检查融合版并在空闲时安装；已有更新偏好保留。账号、数据库、录音和大疆运行文件保留。\n'
printf '这只表示 Halo 服务在响应，不等于模块 UAC/ADB 已就绪，也不等于大疆音频已经验证。\n'
if [[ -n "$RUNTIME_DIR" ]]; then
  printf '三个 QDC507 运行文件已安装到 /opt/halo/qdc507，SHA-256 与允许列表一致。\n'
else
  printf '大疆语音文件未配齐。请执行 ./source/scripts/fetch-qdc507-runtime.sh 后，再运行 sudo ./install --runtime-dir ./qdc507-runtime。\n'
  printf '当前不能当作大疆语音可用。\n'
fi
printf '电话页运行文件目录填 /opt/halo/qdc507，ADB socket 填 tcp:127.0.0.1:5038。声卡按检查脚本列出的实际设备填写。\n'
printf '请只保留原生服务或 Docker 其中一种。\n'
