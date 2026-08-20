#!/usr/bin/env bash
# Install task-id-sanitize into a local CLIProxyAPI (CPA) instance.
set -euo pipefail

PLUGIN_ID="task-id-sanitize"
DEFAULT_REPO="https://github.com/Xell79/task-id-sanitize.git"
DEFAULT_REF="master"
DEFAULT_SERVICE="cli-proxy-api"
DEFAULT_PRIORITY="2"
MIN_GO="1.27"

REPO="${TASK_ID_SANITIZE_REPO:-$DEFAULT_REPO}"
REF="${TASK_ID_SANITIZE_REF:-$DEFAULT_REF}"
SRC=""
CPA_DIR="${CPA_DIR:-}"
CPA_CONFIG="${CPA_CONFIG:-}"
PLUGIN_DIR=""
SERVICE="${CPA_SERVICE:-$DEFAULT_SERVICE}"
PRIORITY="$DEFAULT_PRIORITY"
SO_PATH=""
CLONED_DIR=""
cleanup() {
  # Only remove trees this script cloned; --src/--so paths are the
  # caller's property.
  if [[ -n "$CLONED_DIR" && -d "$CLONED_DIR" ]]; then
    rm -rf "$CLONED_DIR"
  fi
}
trap cleanup EXIT
ENABLE=1
RESTART=1
INSTALL_DEPS=1
RUN_TEST=0
DRY_RUN=0
PRUNE_OLD=0
UPGRADE=0
FORCE=0
SKIP_COPY=0
INSTALLED_VER=""

usage() {
  cat <<'EOF'
Install or upgrade task-id-sanitize into CLIProxyAPI.

Usage:
  install.sh [options]

  curl -fsSL https://raw.githubusercontent.com/Xell79/task-id-sanitize/master/install.sh \
    | sudo bash -s -- [options]

  curl -fsSL https://raw.githubusercontent.com/Xell79/task-id-sanitize/master/install.sh \
    | sudo bash -s -- --upgrade

Options:
  --repo URL          Git clone URL (default: official GitHub repo)
  --ref REF           Git branch, tag, or commit (default: master)
  --src DIR           Use an existing source tree (skip clone)
  --so PATH           Use a prebuilt .so (skip clone/build)
  --cpa-dir DIR       CLIProxyAPI working directory
  --config PATH       Path to config.yaml
  --plugin-dir DIR    Destination directory for the .so
  --service NAME      systemd unit (default: cli-proxy-api)
  --priority N        plugins.configs.task-id-sanitize.priority (default: 2)
  --upgrade           Replace an older install; prune previous .so files
  --force             Allow reinstall of the same version or a downgrade
  --no-enable         Copy the .so but leave the plugin disabled
  --no-restart        Do not restart the CLIProxyAPI service
  --skip-deps         Do not install OS packages / Go
  --test              Run `go test` before installing
  --prune-old         Remove older task-id-sanitize-v*.so from the plugin dir
  --dry-run           Print actions; do not write files or restart
  -h, --help          Show this help

Environment:
  TASK_ID_SANITIZE_REPO   Same as --repo
  TASK_ID_SANITIZE_REF    Same as --ref
  CPA_DIR                 Same as --cpa-dir
  CPA_CONFIG              Same as --config
  CPA_SERVICE             Same as --service
  TASK_ID_SANITIZE_LOG    Plugin log path (runtime, not installer)

Upgrade:
  A newer version copies the new .so, removes previous
  task-id-sanitize-v*.so files, keeps the plugin enabled, and
  restarts CLIProxyAPI so the host hot-loads or reloads the binary.
  The same version is left in place unless --force is set.
  A downgrade also requires --force.
EOF
}

log() { printf '%s\n' "$*"; }
err() { printf 'error: %s\n' "$*" >&2; }
die() { err "$*"; exit 1; }
run() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf 'dry-run:'; printf ' %q' "$@"; printf '\n'
    return 0
  fi
  "$@"
}

need_cmd() { command -v "$1" >/dev/null 2>&1; }

as_root() {
  if [[ "$(id -u)" -eq 0 ]]; then
    "$@"
    return
  fi
  if need_cmd sudo; then
    sudo "$@"
    return
  fi
  die "root or sudo required for: $*"
}

maybe_root() {
  local dest=$1
  shift
  if [[ -w "$(dirname "$dest")" ]] && { [[ ! -e "$dest" ]] || [[ -w "$dest" ]]; }; then
    "$@"
    return
  fi
  as_root "$@"
}

version_ge() {
  # version_ge 1.22.2 1.22
  printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1 | grep -qx "$2"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO=$2; shift 2 ;;
    --ref) REF=$2; shift 2 ;;
    --src) SRC=$2; shift 2 ;;
    --so) SO_PATH=$2; shift 2 ;;
    --cpa-dir) CPA_DIR=$2; shift 2 ;;
    --config) CPA_CONFIG=$2; shift 2 ;;
    --plugin-dir) PLUGIN_DIR=$2; shift 2 ;;
    --service) SERVICE=$2; shift 2 ;;
    --priority) PRIORITY=$2; shift 2 ;;
    --no-enable) ENABLE=0; shift ;;
    --no-restart) RESTART=0; shift ;;
    --skip-deps) INSTALL_DEPS=0; shift ;;
    --upgrade) UPGRADE=1; PRUNE_OLD=1; shift ;;
    --force) FORCE=1; shift ;;
    --test) RUN_TEST=1; shift ;;
    --prune-old) PRUNE_OLD=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

GOOS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$GOOS" in
  linux|darwin) ;;
  *) die "unsupported OS: $GOOS (need linux or darwin)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

detect_pkg() {
  if need_cmd apt-get; then echo apt
  elif need_cmd dnf; then echo dnf
  elif need_cmd yum; then echo yum
  elif need_cmd apk; then echo apk
  elif need_cmd pacman; then echo pacman
  else echo none
  fi
}

install_packages() {
  [[ "$INSTALL_DEPS" -eq 1 ]] || return 0
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "dry-run: skip OS package installation"
    return 0
  fi
  local pkg
  pkg=$(detect_pkg)
  log "installing build dependencies ($pkg)"
  case "$pkg" in
    apt)
      as_root apt-get update -y
      as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates curl git python3 gcc libc6-dev make
      ;;
    dnf)
      as_root dnf install -y ca-certificates curl git python3 gcc glibc-devel make
      ;;
    yum)
      as_root yum install -y ca-certificates curl git python3 gcc glibc-devel make
      ;;
    apk)
      as_root apk add --no-cache ca-certificates curl git python3 gcc musl-dev make
      ;;
    pacman)
      as_root pacman -Sy --noconfirm --needed ca-certificates curl git python gcc glibc make
      ;;
    none)
      log "no package manager detected; ensure git, curl, python3, gcc, and Go >= $MIN_GO are installed"
      ;;
  esac
}

go_version_ok() {
  need_cmd go || return 1
  local ver
  ver=$(go version | awk '{print $3}' | sed 's/^go//')
  version_ge "$ver" "$MIN_GO"
}

install_go() {
  if go_version_ok; then
    log "using $(go version)"
    return 0
  fi
  [[ "$INSTALL_DEPS" -eq 1 ]] || die "Go >= $MIN_GO required (found: $(command -v go >/dev/null && go version || echo none))"

  local pkg
  pkg=$(detect_pkg)
  case "$pkg" in
    apt) as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends golang-go || true ;;
    dnf) as_root dnf install -y golang || true ;;
    yum) as_root yum install -y golang || true ;;
    apk) as_root apk add --no-cache go || true ;;
    pacman) as_root pacman -Sy --noconfirm --needed go || true ;;
  esac
  if go_version_ok; then
    log "using $(go version)"
    return 0
  fi

  local GO_DL_VER="1.27.0"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "dry-run: skip Go ${GO_DL_VER} tarball installation"
    go_version_ok && log "using $(go version)" || log "using bundled check: Go >= $MIN_GO required at real install time"
    return 0
  fi
  local tarball="go${GO_DL_VER}.${GOOS}-${GOARCH}.tar.gz"
  local url="https://go.dev/dl/${tarball}"
  local tmp
  tmp=$(mktemp -d)
  log "installing Go ${GO_DL_VER} from go.dev ($GOOS/$GOARCH)"
  curl -fsSL "$url" -o "$tmp/$tarball"
  if [[ "$(id -u)" -eq 0 ]]; then
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmp/$tarball"
    export PATH="/usr/local/go/bin:$PATH"
  else
    mkdir -p "$HOME/.local"
    rm -rf "$HOME/.local/go"
    tar -C "$HOME/.local" -xzf "$tmp/$tarball"
    export PATH="$HOME/.local/go/bin:$PATH"
  fi
  rm -rf "$tmp"
  go_version_ok || die "failed to install Go >= $MIN_GO"
  log "using $(go version)"
}

parse_systemd() {
  need_cmd systemctl || return 1
  systemctl cat "$SERVICE" >/dev/null 2>&1 || return 1
  local wd exec
  wd=$(systemctl show -p WorkingDirectory --value "$SERVICE" 2>/dev/null || true)
  exec=$(systemctl show -p ExecStart --value "$SERVICE" 2>/dev/null || true)
  if [[ -z "$CPA_DIR" && -n "$wd" && "$wd" != "/" ]]; then
    CPA_DIR=$wd
  fi
  if [[ -z "$CPA_CONFIG" && "$exec" == *"-config"* ]]; then
    CPA_CONFIG=$(printf '%s\n' "$exec" | tr ' ' '\n' | awk 'found{print; exit} $0=="-config"{found=1}')
  fi
}

detect_cpa() {
  parse_systemd || true
  if [[ -z "$CPA_DIR" ]]; then
    local d
    for d in /opt/cli-proxy-api /usr/local/cli-proxy-api "$HOME/cli-proxy-api" "$HOME/CLIProxyAPI" "$PWD"; do
      if [[ -f "$d/config.yaml" ]]; then
        CPA_DIR=$d
        break
      fi
    done
  fi
  [[ -n "$CPA_DIR" ]] || die "cannot find CLIProxyAPI directory (pass --cpa-dir)"
  CPA_DIR=$(cd "$CPA_DIR" && pwd)
  if [[ -z "$CPA_CONFIG" ]]; then
    if [[ -f "$CPA_DIR/config.yaml" ]]; then
      CPA_CONFIG="$CPA_DIR/config.yaml"
    elif [[ -f "$CPA_DIR/config.yml" ]]; then
      CPA_CONFIG="$CPA_DIR/config.yml"
    else
      die "no config.yaml in $CPA_DIR (pass --config)"
    fi
  fi
  [[ -f "$CPA_CONFIG" ]] || die "config not found: $CPA_CONFIG"
}

plugin_dir_from_config() {
  local dir
  dir=$(python3 - "$CPA_CONFIG" "$CPA_DIR" <<'PY'
import os, re, sys
cfg, cpa = sys.argv[1], sys.argv[2]
text = open(cfg, encoding="utf-8").read().splitlines()
in_plugins = False
plugins_indent = None
for line in text:
    raw = line.split("#", 1)[0].rstrip()
    if not raw.strip():
        continue
    indent = len(line) - len(line.lstrip(" "))
    if re.match(r"plugins:\s*$", raw):
        in_plugins = True
        plugins_indent = indent
        continue
    if in_plugins:
        if indent <= plugins_indent and not raw.startswith(" "):
            break
        if indent <= plugins_indent and re.match(r"\S", raw):
            break
        m = re.match(r"\s*dir:\s*[\"']?([^\"']+)[\"']?\s*$", raw)
        if m:
            print(m.group(1).strip())
            break
PY
)
  if [[ -z "$dir" ]]; then
    dir="plugins"
  fi
  if [[ "$dir" != /* ]]; then
    dir="$CPA_DIR/$dir"
  fi
  printf '%s/%s/%s\n' "$dir" "$GOOS" "$GOARCH"
}

source_version() {
  local file=$1
  python3 - "$file" <<'PY'
import os, re, sys
path = sys.argv[1]
if not os.path.isfile(path):
    # Unknown (e.g. dry-run without --src/--so) must not masquerade
    # as a real version like 0.0.0.
    print("unknown")
    raise SystemExit(0)
text = open(path, encoding="utf-8").read()
m = re.search(r'pluginVersion\s*=\s*"([^"]+)"', text)
print(m.group(1) if m else "unknown")
PY
}

so_version() {
  local name
  name=$(basename "$1")
  printf '%s\n' "$name" | sed -n "s/^${PLUGIN_ID}-v\\([0-9][0-9.]*\\)\\.so$/\\1/p"
}

installed_version() {
  local dir=$1
  [[ -d "$dir" ]] || return 0
  python3 - "$dir" "$PLUGIN_ID" <<'PY'
import os, re, sys
from functools import cmp_to_key
d, pid = sys.argv[1], sys.argv[2]
pat = re.compile(rf"^{re.escape(pid)}-v([0-9][0-9.]*)\.so$")
vers = []
try:
    names = os.listdir(d)
except FileNotFoundError:
    names = []
for name in names:
    m = pat.match(name)
    if m:
        vers.append(m.group(1))

def cmp(a, b):
    pa, pb = a.split("."), b.split(".")
    n = max(len(pa), len(pb))
    for i in range(n):
        xa = int(pa[i]) if i < len(pa) and pa[i].isdigit() else 0
        xb = int(pb[i]) if i < len(pb) and pb[i].isdigit() else 0
        if xa != xb:
            return (xa > xb) - (xa < xb)
    return 0

if vers:
    print(sorted(vers, key=cmp_to_key(cmp))[-1])
PY
}

cmp_semver() {
  python3 - "$1" "$2" <<'PY'
import sys
a, b = sys.argv[1], sys.argv[2]
def parts(v):
    out = []
    for p in v.split("."):
        out.append(int(p) if p.isdigit() else 0)
    return out
pa, pb = parts(a), parts(b)
n = max(len(pa), len(pb))
pa += [0] * (n - len(pa))
pb += [0] * (n - len(pb))
if pa < pb:
    print("lt")
elif pa > pb:
    print("gt")
else:
    print("eq")
PY
}

prune_old_sos() {
  local keep=$1
  [[ "$PRUNE_OLD" -eq 1 ]] || return 0
  [[ -d "$PLUGIN_DIR" ]] || return 0
  log "pruning older ${PLUGIN_ID}-v*.so (keep $(basename "$keep"))"
  [[ "$DRY_RUN" -eq 1 ]] && return 0
  local f
  for f in "$PLUGIN_DIR"/"${PLUGIN_ID}"-v*.so; do
    [[ -e "$f" ]] || continue
    [[ "$(basename "$f")" == "$(basename "$keep")" ]] && continue
    log "removing $f"
    maybe_root "$f" rm -f "$f"
  done
}

decide_upgrade() {
  local new=$1
  local have
  have=$(installed_version "$PLUGIN_DIR" || true)
  INSTALLED_VER=$have
  if [[ -z "$have" ]]; then
    log "no previous ${PLUGIN_ID} found; installing v${new}"
    return 0
  fi
  local rel
  rel=$(cmp_semver "$new" "$have")
  case "$rel" in
    eq)
      if [[ "$FORCE" -eq 1 ]]; then
        log "reinstalling v${new} (--force)"
        return 0
      fi
      log "already at v${have}; enabling and leaving the binary in place (pass --force to rebuild)"
      SKIP_COPY=1
      return 0
      ;;
    lt)
      if [[ "$FORCE" -eq 1 ]]; then
        log "downgrading v${have} -> v${new} (--force)"
        return 0
      fi
      die "installed v${have} is newer than v${new}; pass --force to downgrade"
      ;;
    gt)
      log "upgrading v${have} -> v${new}"
      PRUNE_OLD=1
      return 0
      ;;
  esac
}

fetch_src() {
  if [[ -n "$SO_PATH" ]]; then
    [[ -f "$SO_PATH" ]] || die "prebuilt .so not found: $SO_PATH"
    return 0
  fi
  if [[ -n "$SRC" ]]; then
    [[ -f "$SRC/plugin.go" ]] || die "--src has no plugin.go: $SRC"
    SRC=$(cd "$SRC" && pwd)
    return 0
  fi
  if [[ -f "${SCRIPT_DIR:-}/plugin.go" ]]; then
    SRC=$SCRIPT_DIR
    return 0
  fi
  local tmp
  tmp=$(mktemp -d)
  log "cloning $REPO ($REF) into $tmp"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    SRC=$tmp
    return 0
  fi
  git clone --depth 1 --branch "$REF" "$REPO" "$tmp/src" 2>/dev/null \
    || git clone "$REPO" "$tmp/src" && git -C "$tmp/src" checkout "$REF"
  SRC="$tmp/src"
  CLONED_DIR=$tmp
}

build_so() {
  if [[ -n "$SO_PATH" ]]; then
    return 0
  fi
  [[ -n "$SRC" ]] || die "no source directory"
  local ver
  ver=$(source_version "$SRC/plugin.go")
  SO_PATH="$SRC/${PLUGIN_ID}-v${ver}.so"
  log "building $SO_PATH"
  if [[ "$RUN_TEST" -eq 1 ]]; then
    log "running tests"
    (cd "$SRC" && CGO_ENABLED=1 go test .)
  fi
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  (cd "$SRC" && CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags='-s -w' -o "$SO_PATH" .)
  [[ -f "$SO_PATH" ]] || die "build produced no .so"
}

enable_plugin_yaml() {
  local cfg=$1
  python3 - "$cfg" "$PLUGIN_ID" "$ENABLE" "$PRIORITY" "$DRY_RUN" <<'PY'
import pathlib, sys

path = pathlib.Path(sys.argv[1])
plugin_id = sys.argv[2]
enable = sys.argv[3] == "1"
priority = sys.argv[4]
dry = sys.argv[5] == "1"
text = path.read_text(encoding="utf-8")
if not text.endswith("\n"):
    text += "\n"
lines = text.splitlines(keepends=True)

def indent_of(line: str) -> int:
    return len(line) - len(line.lstrip(" "))

def is_key(line: str, name: str) -> bool:
    s = line.split("#", 1)[0].rstrip()
    return s.strip() == f"{name}:" or s.strip().startswith(f"{name}:")

def find_block(start_name: str, min_indent: int, begin: int, end: int):
    i = begin
    while i < end:
        raw = lines[i].split("#", 1)[0].rstrip()
        if not raw.strip():
            i += 1
            continue
        ind = indent_of(lines[i])
        if ind < min_indent:
            return None, None, None
        if ind == min_indent and raw.strip().rstrip(":") == start_name:
            j = i + 1
            while j < end:
                if not lines[j].strip() or lines[j].lstrip().startswith("#"):
                    j += 1
                    continue
                if indent_of(lines[j]) <= ind:
                    break
                j += 1
            return i, j, ind
        i += 1
    return None, None, None

def set_child(block_start, block_end, parent_indent, key, value) -> None:
    i = block_start + 1
    child_indent = parent_indent + 2
    while i < block_end:
        if not lines[i].strip() or lines[i].lstrip().startswith("#"):
            i += 1
            continue
        ind = indent_of(lines[i])
        if ind < child_indent:
            break
        raw = lines[i].split("#", 1)[0].rstrip()
        if ind == child_indent and raw.strip().startswith(f"{key}:"):
            lines[i] = f'{" " * child_indent}{key}: {value}\n'
            return
        i += 1
    lines.insert(block_start + 1, f'{" " * child_indent}{key}: {value}\n')

changed = False
p_start, p_end, p_ind = find_block("plugins", 0, 0, len(lines))
if p_start is None:
    lines.append("\nplugins:\n  enabled: true\n  dir: \"plugins\"\n  configs:\n")
    p_start, p_end, p_ind = find_block("plugins", 0, 0, len(lines))
    changed = True

set_child(p_start, p_end, p_ind, "enabled", "true")
p_start, p_end, p_ind = find_block("plugins", 0, 0, len(lines))

c_start, c_end, c_ind = find_block("configs", p_ind + 2, p_start + 1, p_end)
if c_start is None:
    insert_at = p_start + 1
    lines.insert(insert_at, f'{" " * (p_ind + 2)}configs:\n')
    p_start, p_end, p_ind = find_block("plugins", 0, 0, len(lines))
    c_start, c_end, c_ind = find_block("configs", p_ind + 2, p_start + 1, p_end)
    changed = True

pl_start, pl_end, pl_ind = find_block(plugin_id, c_ind + 2, c_start + 1, c_end)
enabled_val = "true" if enable else "false"
if pl_start is None:
    block = (
        f'{" " * (c_ind + 2)}{plugin_id}:\n'
        f'{" " * (c_ind + 4)}enabled: {enabled_val}\n'
        f'{" " * (c_ind + 4)}priority: {priority}\n'
    )
    lines.insert(c_start + 1, block)
    changed = True
else:
    before = "".join(lines[pl_start:pl_end])
    set_child(pl_start, pl_end, pl_ind, "enabled", enabled_val)
    pl_start, pl_end, pl_ind = find_block(plugin_id, c_ind + 2, c_start + 1, len(lines))
    set_child(pl_start, pl_end, pl_ind, "priority", priority)
    after = "".join(lines[pl_start:pl_end])
    if before != after:
        changed = True

new = "".join(lines)
if dry:
    print("dry-run: config would be updated" if new != text or changed else "dry-run: config already correct")
    raise SystemExit(0)
if new != text:
    bak = path.with_suffix(path.suffix + ".bak-task-id-sanitize")
    bak.write_text(text, encoding="utf-8")
    path.write_text(new, encoding="utf-8")
    print(f"updated {path} (backup {bak})")
else:
    print(f"config already has {plugin_id}")
PY
}

restart_service() {
  [[ "$RESTART" -eq 1 ]] || { log "skipping service restart"; return 0; }
  if need_cmd systemctl && systemctl cat "$SERVICE" >/dev/null 2>&1; then
    log "restarting $SERVICE"
    run as_root systemctl restart "$SERVICE"
    return 0
  fi
  log "systemd unit $SERVICE not found; restart CLIProxyAPI yourself"
}

SCRIPT_DIR=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fi

install_packages
detect_cpa
if [[ -z "$PLUGIN_DIR" ]]; then
  PLUGIN_DIR=$(plugin_dir_from_config)
fi
log "CPA dir     : $CPA_DIR"
log "config      : $CPA_CONFIG"
log "plugin dir  : $PLUGIN_DIR"
log "os/arch     : $GOOS/$GOARCH"

if [[ -z "$SO_PATH" ]]; then
  install_go
  fetch_src
  build_so
fi

ver="unknown"
if [[ -n "$SRC" && -f "$SRC/plugin.go" ]]; then
  ver=$(source_version "$SRC/plugin.go")
elif [[ -n "$SO_PATH" ]]; then
  ver=$(so_version "$SO_PATH")
  [[ -n "$ver" ]] || ver="unknown"
fi
[[ "$ver" != "unknown" && -n "$ver" ]] || die "cannot determine plugin version (dry-run of a remote install: pass --src DIR or --so PATH)"
dest="$PLUGIN_DIR/${PLUGIN_ID}-v${ver}.so"
decide_upgrade "$ver"
log "target      : $dest"
if [[ -n "$INSTALLED_VER" ]]; then
  log "installed   : v${INSTALLED_VER}"
fi

if [[ "$SKIP_COPY" -eq 1 ]]; then
  log "skipping binary copy"
else
  log "installing  : $SO_PATH -> $dest"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "dry-run: would mkdir -p $PLUGIN_DIR and copy the .so"
  else
    maybe_root "$PLUGIN_DIR/." mkdir -p "$PLUGIN_DIR"
    maybe_root "$dest" install -m 0755 "$SO_PATH" "$dest"
  fi
fi

prune_old_sos "$dest"
enable_plugin_yaml "$CPA_CONFIG"
if [[ "$SKIP_COPY" -eq 1 && "$FORCE" -eq 0 ]]; then
  log "skipping service restart (already at v${ver}; pass --force to rebuild)"
else
  restart_service
fi
log "done: ${PLUGIN_ID} v${ver} -> $dest"
log "enable=${ENABLE} restart=${RESTART} upgrade=${UPGRADE} force=${FORCE}"
