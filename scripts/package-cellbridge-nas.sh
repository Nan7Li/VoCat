#!/usr/bin/env bash
# 组装可复现的 Halo + CellBridge NAS 发布目录。不上传，不包含厂商运行文件。
set -euo pipefail

usage() {
  cat <<'EOF'
用法：bash scripts/package-cellbridge-nas.sh [--output-dir DIR] [--binary PATH] [--version VER]

默认在仓库 dist/halo-cellbridge-nas-amd64 写出发布目录，并在同级生成同名 tar.gz。
未指定 --binary 时用 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 编译。
source/ 是当前工作区副本，不用 git archive，因此包含尚未提交的脚本。
不包含 .git、node_modules、nas-data、qdc507-runtime 和三个厂商文件。
EOF
}

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
OUTPUT=""
BINARY=""
VERSION=1.1.14-cellbridge

while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --output-dir)
      [[ $# -ge 2 ]] || fail '--output-dir 缺少目录'
      OUTPUT=$2
      shift 2
      ;;
    --binary)
      [[ $# -ge 2 ]] || fail '--binary 缺少路径'
      BINARY=$2
      shift 2
      ;;
    --version)
      [[ $# -ge 2 ]] || fail '--version 缺少版本'
      VERSION=$2
      shift 2
      ;;
    *) fail "未知参数：$1" ;;
  esac
done

[[ -n "$OUTPUT" ]] || OUTPUT=$REPO/dist/halo-cellbridge-nas-amd64
OUTPUT=$(mkdir -p -- "$(dirname -- "$OUTPUT")" && CDPATH= cd -- "$(dirname -- "$OUTPUT")" && pwd)/$(basename -- "$OUTPUT")
[[ ! -e "$OUTPUT" ]] || fail "输出目录已存在：$OUTPUT"

commit=$(git -C "$REPO" rev-parse HEAD)
source_epoch=${SOURCE_DATE_EPOCH:-$(git -C "$REPO" show -s --format=%ct HEAD)}
[[ "$source_epoch" =~ ^[0-9]+$ ]] || fail "SOURCE_DATE_EPOCH 必须是整数"
file_list=$(mktemp /tmp/halo-package-files.XXXXXXXX)
trap 'rm -f -- "$file_list"' EXIT
git -C "$REPO" ls-files -z --cached --others --exclude-standard > "$file_list"
dirty=$(git -C "$REPO" status --porcelain)
if [[ -n "$dirty" ]]; then
  dirty_label=dirty
else
  dirty_label=clean
fi

mkdir -p -- "$OUTPUT/source"
if [[ -n "$BINARY" ]]; then
  [[ -f "$BINARY" && ! -L "$BINARY" ]] || fail "二进制不存在：$BINARY"
  install -m 0755 "$BINARY" "$OUTPUT/halo-linux-amd64"
  built=supplied
else
  build_time=$(date -u -d "@$source_epoch" +%Y-%m-%dT%H:%M:%SZ)
  (
    cd "$REPO"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 \
      go build -trimpath \
      -ldflags "-s -w -X vocat/internal/buildinfo.Version=${VERSION} -X vocat/internal/buildinfo.BuildTime=${build_time}" \
      -o "$OUTPUT/halo-linux-amd64" ./cmd/vocat
  )
  chmod 0755 "$OUTPUT/halo-linux-amd64"
  built=built
fi

(
  cd "$OUTPUT"
  sha256sum halo-linux-amd64 > SHA256SUMS
)

copy_source() {
  local dest=$1
  # Only versioned and nonignored worktree files captured before creating OUTPUT.
  # This excludes ignored credentials and avoids recursing into an in-repo output.
  tar -C "$REPO" --null --no-recursion \
    --exclude '.git' --exclude 'node_modules' --exclude 'nas-data' \
    --exclude 'qdc507-runtime' --exclude 'dist' \
    --exclude 'mavo-pcm-bridge.armv7' --exclude 'qdc507_aprv3.ko' \
    --exclude 'qdc507_voice.ko' --exclude '*.db*' --exclude '*.sqlite*' \
    --exclude '.env' --exclude '.env.*' \
    -cf - -T "$file_list" | tar -C "$dest" -xf -
}

copy_source "$OUTPUT/source"
if find "$OUTPUT/source" \( -name node_modules -o -name .git -o -name nas-data -o -name qdc507-runtime \
  -o -name 'mavo-pcm-bridge.armv7' -o -name 'qdc507_aprv3.ko' -o -name 'qdc507_voice.ko' \) -print | grep -q .; then
  fail '打包结果含有禁止内容'
fi
test ! -d "$OUTPUT/source/node_modules"
test ! -d "$OUTPUT/source/.git"
test ! -d "$OUTPUT/source/qdc507-runtime"
test -f "$OUTPUT/source/scripts/fetch-qdc507-runtime.sh"
test -f "$OUTPUT/source/scripts/install-cellbridge-nas.sh"
test -f "$OUTPUT/source/scripts/check-cellbridge-nas.sh"

cat > "$OUTPUT/install" <<'EOF'
#!/bin/sh
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec /bin/bash "$here/source/scripts/install-cellbridge-nas.sh" "$@"
EOF
chmod 0755 "$OUTPUT/install"

version_line=$("$OUTPUT/halo-linux-amd64" version 2>/dev/null || true)
cat > "$OUTPUT/BUILD-INFO.txt" <<EOF
Halo NAS package
Source commit: $commit
Source state: $dirty_label
Source copy: working tree, not git archive. Uncommitted scripts in this worktree are included.
Binary: $built
Build flags when compiled here: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1
Version argument: $VERSION
Binary version: ${version_line:-unknown}
Vendor runtime: excluded
EOF
if [[ -n "$dirty" ]]; then
  printf 'Dirty paths:\n%s\n' "$dirty" >> "$OUTPUT/BUILD-INFO.txt"
fi

cat > "$OUTPUT/README.md" <<'EOF'
# Halo + CellBridge：Debian 13 / amd64 部署包

适用于 J3160、N5105 以及其他 Linux x86_64。`halo-linux-amd64` 用 `GOAMD64=v1` 构建，网页已嵌入。`source/` 是打包时工作区的源码副本，不是 `git archive`。三个 QDC507 厂商文件不在包内。

先校验：

```sh
sha256sum -c SHA256SUMS
```

安装原生服务并获取运行文件（会先安装下载依赖）：

```sh
sudo ./install --fetch-runtime
```

如已准备好文件，可用 `sudo ./install --runtime-dir ./qdc507-runtime`。单独获取文件可运行 `./source/scripts/fetch-qdc507-runtime.sh`（需 curl 和 CA 证书）。没有运行文件时仍可安装 Halo 和读卡器 IMS，但大疆语音文件未配齐。

程序安装成功、模块 UAC/ADB 就绪、大疆音频验证成功，是三件事。诊断：

```sh
bash source/scripts/check-cellbridge-nas.sh
```

Docker 仍使用 `source/compose.nas.yml`，不要和原生 systemd 服务同时运行。管理员密码只在本机标准输入提供，见 `source/docs/NAS-J3160.zh-CN.md`。
EOF

parent=$(dirname -- "$OUTPUT")
base=$(basename -- "$OUTPUT")
tar --sort=name --mtime="@$source_epoch" --owner=0 --group=0 --numeric-owner \
  -C "$parent" -cf - "$base" | gzip -n > "$parent/$base.tar.gz"
( cd "$parent" && sha256sum "$base.tar.gz" > "$base.tar.gz.sha256" )
printf '已写出 %s\n已写出 %s.tar.gz\n提交 %s（%s）\n' "$OUTPUT" "$parent/$base" "$commit" "$dirty_label"
