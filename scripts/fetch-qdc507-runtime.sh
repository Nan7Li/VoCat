#!/usr/bin/env bash
# Download only: never opens USB, ADB, AT or loads kernel modules.
set -euo pipefail
umask 077

usage() {
  cat <<'EOF'
用法：./source/scripts/fetch-qdc507-runtime.sh [--output-dir DIR]
从 MaVo 固定提交下载 QDC507 三个运行文件及来源/许可证资料，逐一校验 SHA-256。
默认输出 ./qdc507-runtime；目标目录必须尚不存在，失败时不会留下半成品。
无需 root；需要 curl、sha256sum。仅下载文件，不配置或操作模块。
下载来源：https://github.com/moluncn/mavo
EOF
}
fail() { printf '错误：%s\n' "$*" >&2; exit 1; }
output=./qdc507-runtime
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --output-dir) (($# >= 2)) || fail '--output-dir 缺少目录'; output=$2; shift 2 ;;
    *) fail "未知参数：$1" ;;
  esac
done
[[ -n "$output" ]] || fail '目录不能为空'
for tool in curl sha256sum mktemp; do command -v "$tool" >/dev/null || fail "缺少 $tool"; done
[[ ! -e "$output" && ! -L "$output" ]] || fail '输出目录已存在；请指定新的目录，现有文件不会覆盖'
parent=$(dirname -- "$output")
name=$(basename -- "$output")
[[ "$name" != . && "$name" != .. ]] || fail '请指定一个新的子目录'
[[ -d "$parent" ]] || fail '输出目录的父目录不存在'
parent=$(cd -- "$parent" && pwd -P)
output="$parent/$name"
stage=$(mktemp -d "$parent/.qdc507-download.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
commit=0443dfdaf8aec086fd76ba2ee9152fd908114524
base="https://raw.githubusercontent.com/moluncn/mavo/$commit"
cat > "$stage/SHA256SUMS" <<'EOF'
3d82d3dec4f1e323201bba87156df9d41438e08314097353f2607f9117211d4a  qdc507_aprv3.ko
ed3821682d5309969a01c764192c83feff9669c61ef237c69475cd1619cf296c  qdc507_voice.ko
88d47c15e61d1428a59c821fed804c2e6490e82859a085062f21966b58d167fc  mavo-pcm-bridge.armv7
af8067302947c01fd9eee72befa54c7e3ef8a48fecde7fd71277f2290b2bf0f7  COPYING-GPL-2.0
fb9d58336bcfdad8938d7833c113a815c2153d9a04564eb73cddabea737f8be2  MODULE-REPORT.md
f4f6c266ced7015d4e61d993a6e31247c26a9e85a8fdf1c6d842c459e1e2970a  manifest.json
e205fa7e06ba1dc86f27c7fcf9dd16da16ed8b2c5c72ee687cb5025870d655cd  THIRD_PARTY_NOTICES.md
EOF
while read -r hash file; do
  path="Resources/ModuleVoice/$file"
  [[ "$file" != THIRD_PARTY_NOTICES.md ]] || path="$file"
  printf '下载并校验：%s\n' "$file"
  curl --fail --silent --show-error --location --retry 2 --connect-timeout 15 --max-time 120 \
    --proto '=https' --proto-redir '=https' "$base/$path" --output "$stage/$file"
  actual=$(sha256sum -- "$stage/$file")
  [[ "${actual%% *}" == "$hash" ]] || fail "$file SHA-256 不匹配，已丢弃下载"
done < "$stage/SHA256SUMS"
cat > "$stage/SOURCE.txt" <<EOF
来源：https://github.com/moluncn/mavo/tree/$commit/Resources/ModuleVoice
固定提交：$commit
模块内核：3.18.44；文件校验与 Halo / CellBridge QDC507 音频代码一致。
许可证与构建资料：COPYING-GPL-2.0、MODULE-REPORT.md、THIRD_PARTY_NOTICES.md。
只完成运行文件准备，尚未配置 USB/ADB、加载驱动或验证通话。
EOF
chmod 600 "$stage"/*
# -T prevents moving into an output directory created concurrently; -n refuses overwrite.
mv -T -n -- "$stage" "$output"
[[ ! -d "$stage" ]] || fail '输出目录被其他进程创建；下载未覆盖该目录'
trap - EXIT
printf '三个运行文件已校验：%s\n下一步：sudo ./install --runtime-dir %q\n' "$output" "$output"
