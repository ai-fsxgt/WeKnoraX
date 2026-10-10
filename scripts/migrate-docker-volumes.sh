#!/usr/bin/env bash
# 临时迁移工具：将生产编排的现有数据卷复制到编排旁的 data 目录。
# 不修改编排、不启动服务、不删除旧卷。执行前请等待文档解析等任务结束。
set -euo pipefail
export LC_ALL=C

show_help() {
  cat <<'HELP'
用法：在部署目录运行 bash scripts/migrate-docker-volumes.sh [选项]

默认仅预览，不停止容器、不创建目录、不拉取镜像。

选项：
  --execute              正常停止项目容器，复制并校验数据
  --compose-file FILE    编排文件，默认 ./docker-compose.yml
  --project-name NAME    原部署使用了 -p 时，指定同一个项目名
  --helper-image IMAGE   包含 Bash 和 GNU cp/tar/du 的镜像
                         默认 debian:bookworm-slim，可指定镜像代理地址
  -h, --help             显示帮助

命名卷迁到 ./data/<业务卷名>；无业务卷标签的卷按服务名和挂载目录命名。
例如 Redis 的 /data 迁到 ./data/redis-data，Neo4j 的 /logs 迁到 ./data/neo4j-logs。
迁移后将挂载源改为预览显示的目录，容器内路径保持不变。
SELinux 开启时，挂载增加 z；共享目录使用 z，保留已有 ro 设置。
然后按原部署的启动命令启动服务。旧卷保留，验证成功后再自行删除。
失败时服务不会自动启动；原编排、原容器和旧卷仍然保留。
本工具在 Docker 所在的 Linux 主机运行，使用当前用户的 Docker 连接。
HELP
}

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

EXECUTE=false
COMPOSE_FILE=./docker-compose.yml
PROJECT_NAME=
HELPER_IMAGE=debian:bookworm-slim
HELPER_CONTAINER="weknora-volume-migration-$$-$RANDOM"
STOP_ATTEMPTED=false
HELPER_ACTIVE=false

while (($#)); do
  case "$1" in
    --execute)
      EXECUTE=true
      shift
      ;;
    --compose-file|--project-name|--helper-image)
      if (($# < 2)) || [[ -z "$2" ]]; then
        fail "$1 缺少参数"
      fi
      case "$1" in
        --compose-file) COMPOSE_FILE=$2 ;;
        --project-name) PROJECT_NAME=$2 ;;
        --helper-image) HELPER_IMAGE=$2 ;;
      esac
      shift 2
      ;;
    -h|--help)
      show_help
      exit 0
      ;;
    *) fail "未知选项：$1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || fail '请在 Docker 所在的 Linux 主机运行'
((BASH_VERSINFO[0] >= 4)) || fail '需要 Bash 4 或更高版本'
command -v docker >/dev/null || fail '未找到 Docker 命令'
[[ -f "$COMPOSE_FILE" ]] || fail "编排文件不存在：$COMPOSE_FILE"
COMPOSE_DIR=$(cd -- "$(dirname -- "$COMPOSE_FILE")" && pwd -P)
COMPOSE_FILE="$COMPOSE_DIR/$(basename -- "$COMPOSE_FILE")"
DATA_DIR="$COMPOSE_DIR/data"
cd -- "$COMPOSE_DIR"

# 不访问 DockerRootDir：Unix socket 同时覆盖常规安装与 rootless 安装。
if [[ -n ${DOCKER_HOST:-} && -z ${DOCKER_CONTEXT:-} ]]; then
  DOCKER_ENDPOINT=$DOCKER_HOST
else
  DOCKER_ENDPOINT=$(docker context inspect "$(docker context show)" \
    --format '{{(index .Endpoints "docker").Host}}')
fi
[[ $DOCKER_ENDPOINT == unix://* ]] || fail '需要本机 Docker Unix socket，不能通过远程连接迁移'
[[ $(docker info --format '{{.OSType}}') == linux ]] || fail '需要 Linux Docker 引擎'

# 与项目现有启动脚本支持的两种 Compose 命令保持一致。
if docker compose version >/dev/null 2>&1; then
  COMPOSE=(docker compose)
elif command -v docker-compose >/dev/null && docker-compose version >/dev/null 2>&1; then
  COMPOSE=(docker-compose)
else
  fail '未找到可用的 Docker Compose'
fi
COMPOSE+=(-f "$COMPOSE_FILE" --profile '*')
if [[ -n "$PROJECT_NAME" ]]; then
  COMPOSE+=(-p "$PROJECT_NAME")
fi
"${COMPOSE[@]}" config --quiet

CONTAINER_LIST=$("${COMPOSE[@]}" ps --all --quiet)
[[ -n "$CONTAINER_LIST" ]] || fail '未找到项目容器；迁移前不要执行 down，使用了 -p 时请指定 --project-name'
mapfile -t CONTAINERS <<< "$CONTAINER_LIST"
PROJECT_NAME=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' \
  "${CONTAINERS[0]}")
[[ -n "$PROJECT_NAME" && $PROJECT_NAME != '<no value>' ]] || fail '无法识别 Compose 项目'

declare -A VOLUMES=()
declare -A VOLUME_KB=()
# 从现有 Docker 卷读取标识，即使编排已经改为绑定挂载也能迁移。
VOLUME_LIST=$(docker volume ls --filter "label=com.docker.compose.project=$PROJECT_NAME" \
  --format '{{.Label "com.docker.compose.volume"}} {{.Name}}')
while read -r volume_key volume_name; do
  [[ -n "$volume_key" && -n "$volume_name" ]] || continue
  [[ $volume_key =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || fail "卷标识不能作为目录名：$volume_key"
  [[ -z ${VOLUMES[$volume_key]:-} ]] || fail "同一卷标识对应多个旧卷：$volume_key"
  VOLUMES[$volume_key]=$volume_name
done <<< "$VOLUME_LIST"
# 镜像 VOLUME 声明产生的匿名卷没有 Compose 卷标签，必须从实际挂载识别。
MOUNTED_VOLUMES=$(docker inspect \
  --format '{{$service := index .Config.Labels "com.docker.compose.service"}}{{range .Mounts}}{{if eq .Type "volume"}}{{printf "%s\t%s\t%s\n" $service .Name .Destination}}{{end}}{{end}}' \
  "${CONTAINERS[@]}")
while IFS=$'\t' read -r container_service mounted_volume mount_path; do
  [[ -n "$mounted_volume" ]] || continue
  found=false
  for volume_key in "${!VOLUMES[@]}"; do
    if [[ ${VOLUMES[$volume_key]} == "$mounted_volume" ]]; then
      found=true
      break
    fi
  done
  if $found; then
    continue
  fi
  mount_directory=${mount_path%/}
  volume_key="$container_service-${mount_directory##*/}"
  [[ -n "$container_service" && -n "$mount_directory" && $volume_key =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || \
    fail "无法确定实际挂载卷的目标目录：$mounted_volume ($container_service:$mount_path)"
  [[ -z ${VOLUMES[$volume_key]:-} ]] || \
    fail "目标 $volume_key 对应多个旧卷：${VOLUMES[$volume_key]} 与 $mounted_volume"
  VOLUMES[$volume_key]=$mounted_volume
  printf '识别实际挂载：%s 的 %s -> ./data/%s（卷：%s）\n' \
    "$container_service" "$mount_path" "$volume_key" "$mounted_volume"
done <<< "$MOUNTED_VOLUMES"
((${#VOLUMES[@]})) || fail '未找到属于本项目的数据卷'
mapfile -t VOLUME_KEYS < <(printf '%s\n' "${!VOLUMES[@]}" | sort)

SELINUX=false
SECURITY_OPTIONS=$(docker info --format '{{json .SecurityOptions}}')
if [[ $SECURITY_OPTIONS == *name=selinux* ]]; then
  SELINUX=true
fi

printf '项目：%s\n编排：%s\n迁移目录：%s\n\n' "$PROJECT_NAME" "$COMPOSE_FILE" "$DATA_DIR"
for volume_key in "${VOLUME_KEYS[@]}"; do
  destination="$DATA_DIR/$volume_key"
  [[ ! -e "$destination" && ! -L "$destination" ]] || fail "目标已存在，不会覆盖：$destination"
  printf '  %s -> ./data/%s\n' "${VOLUMES[$volume_key]}" "$volume_key"
done
if $SELINUX; then
  printf '\n已检测到 SELinux；复制挂载使用 z，修改编排时也需保留此选项。\n'
fi
if ! $EXECUTE; then
  printf '\n以上仅为预览。确认后加 --execute 执行；执行前停止新操作并等待解析任务结束。\n'
  exit 0
fi

cleanup() {
  local status=$?
  trap - EXIT
  if $HELPER_ACTIVE; then
    local remaining
    remaining=$(docker ps --all --quiet --filter "name=^/$HELPER_CONTAINER$")
    if [[ -n "$remaining" ]]; then
      if ! docker rm --force "$remaining" >/dev/null; then
        printf '临时复制容器清理失败，请检查：%s\n' "$HELPER_CONTAINER" >&2
        status=1
      fi
    fi
  fi
  if ((status != 0)) && $STOP_ATTEMPTED; then
    printf '\n迁移未完成，服务未自动启动。原编排和旧卷未修改，请检查错误及目标目录。\n' >&2
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 在停机之前准备镜像、检查工具及所需容量，避免拉取镜像占用停机时间。
if ! docker image inspect "$HELPER_IMAGE" >/dev/null 2>&1; then
  docker pull "$HELPER_IMAGE"
fi
docker run --rm --network none --log-driver none --user 0 "$HELPER_IMAGE" \
  bash -euc 'cp --version >/dev/null; tar --version >/dev/null; du --version >/dev/null'
mkdir -p -- "$DATA_DIR"
total_kb=0
for volume_key in "${VOLUME_KEYS[@]}"; do
  printf '统计容量：%s\n' "$volume_key"
  volume_size=$(docker run --rm --network none --log-driver none --user 0 \
    --mount "type=volume,source=${VOLUMES[$volume_key]},target=/from,readonly,volume-nocopy" \
    "$HELPER_IMAGE" du -sk /from)
  VOLUME_KB[$volume_key]=${volume_size%%[[:space:]]*}
  total_kb=$((total_kb + VOLUME_KB[$volume_key]))
done
available_kb=$(df -Pk "$DATA_DIR" | awk 'NR == 2 {print $4}')
((available_kb >= total_kb)) || fail "数据盘空间不足：预计需要 $total_kb KiB，可用 $available_kb KiB"
printf '预计迁移 %.1f GiB，共 %s 个卷。\n' "$(awk -v kb="$total_kb" 'BEGIN {print kb / 1048576}')" "${#VOLUME_KEYS[@]}"

RUNNING_LIST=$(docker ps --quiet --filter "label=com.docker.compose.project=$PROJECT_NAME")
if [[ -n "$RUNNING_LIST" ]]; then
  mapfile -t RUNNING_CONTAINERS <<< "$RUNNING_LIST"
  printf '\n正在正常停止项目容器（最长等待 120 秒）……\n'
  STOP_ATTEMPTED=true
  docker stop --time 120 "${RUNNING_CONTAINERS[@]}"
  for container_id in "${RUNNING_CONTAINERS[@]}"; do
    exit_code=$(docker inspect --format '{{.State.ExitCode}}' "$container_id")
    [[ $exit_code != 137 ]] || fail "容器被强制终止，暂不复制，请先检查停机状态：$container_id"
  done
fi
for volume_key in "${VOLUME_KEYS[@]}"; do
  writers=$(docker ps --quiet --filter "volume=${VOLUMES[$volume_key]}")
  [[ -z "$writers" ]] || fail "旧卷仍被运行中的容器使用：${VOLUMES[$volume_key]} ($writers)"
done

COPY_SCRIPT=$(cat <<'COPY'
volume_key=$1
total_kb=$2
cp -a --no-preserve=context /from/. /to/ &
copy_pid=$!
while kill -0 "$copy_pid" 2>/dev/null; do
  size=$(du -sk /to)
  copied_kb=${size%%[[:space:]]*}
  percent=$((copied_kb * 100 / (total_kb > 0 ? total_kb : 1)))
  ((percent < 100)) || percent=99
  printf '\r  复制 %s：约 %s%%，已写入 %s / 约 %s MiB' \
    "$volume_key" "$percent" "$((copied_kb / 1024))" "$((total_kb / 1024))"
  sleep 2
done
if wait "$copy_pid"; then
  printf '\r  复制 %s：100%%，完成                                      \n' "$volume_key"
else
  status=$?
  printf '\n复制失败：%s\n' "$volume_key" >&2
  exit "$status"
fi
printf '  校验 %s：开始逐文件检查内容、权限、UID/GID、时间及链接……\n' "$volume_key"
# 流式比较，不将归档落盘；每读取约 100 MiB 显示一次校验进度。
tar --create --sparse --numeric-owner --file=- --directory=/from . |
  tar --compare --file=- --directory=/to --checkpoint=10000 \
    --checkpoint-action='echo=校验进度：%T'
printf '  校验 %s：完成\n' "$volume_key"
COPY
)

completed=0
for volume_key in "${VOLUME_KEYS[@]}"; do
  destination="$DATA_DIR/$volume_key"
  mkdir -- "$destination"
  mount_target="$destination:/to"
  if $SELINUX; then
    mount_target+=:z
  fi
  printf '\n[%s/%s] 迁移 %s\n' "$((completed + 1))" "${#VOLUME_KEYS[@]}" "$volume_key"
  HELPER_ACTIVE=true
  docker run --rm --name "$HELPER_CONTAINER" --network none --log-driver none --user 0 \
    --mount "type=volume,source=${VOLUMES[$volume_key]},target=/from,readonly,volume-nocopy" \
    --volume "$mount_target" "$HELPER_IMAGE" \
    bash -euo pipefail -c "$COPY_SCRIPT" -- "$volume_key" "${VOLUME_KB[$volume_key]}"
  HELPER_ACTIVE=false
  completed=$((completed + 1))
  printf '总体进度：%s/%s 个卷完成（%s%%）\n' "$completed" "${#VOLUME_KEYS[@]}" \
    "$((completed * 100 / ${#VOLUME_KEYS[@]}))"
done

printf '\n数据复制并校验完成。服务保持停止，原编排和旧卷均保留。\n'
printf '请将编排挂载源改成上面对应的 ./data/<卷名>，保留容器内路径和 ro 设置。\n'
if $SELINUX; then
  printf 'SELinux 挂载示例：./data/docreader-tmp:/tmp/docreader:ro,z\n'
fi
printf '修改后按原启动命令启动服务；验证历史数据和新上传后，再单独删除旧卷释放根盘空间。\n'
