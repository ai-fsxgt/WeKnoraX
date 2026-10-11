#!/usr/bin/env bash
# 为 embeddings 表中实际存在的每种向量维度补建 HNSW 部分索引。
# 使用 CREATE INDEX CONCURRENTLY，不锁表、不停服务；不修改编排、不重启服务。
# 升级前先执行，避免迁移在应用启动时同步建索引导致启动长时间阻塞。
set -euo pipefail

show_help() {
  cat <<'HELP'
用法：在部署机器上运行 bash scripts/build-embedding-hnsw-indexes.sh [选项]

默认仅预览：列出 embeddings 表中的向量维度及缺失的索引，不做任何修改。

选项：
  --execute                    创建缺失的索引（先清理上次中断留下的无效索引）
  --container NAME             Postgres 容器名，默认 WeKnora-postgres
  --maintenance-work-mem SIZE  建索引使用的内存，默认 2GB，按数据库容器内存调整
  -h, --help                   显示帮助

建索引可能耗时较长，建议后台执行：
  nohup bash scripts/build-embedding-hnsw-indexes.sh --execute > build-index.log 2>&1 &
HELP
}

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

EXECUTE=false
CONTAINER=WeKnora-postgres
MAINTENANCE_WORK_MEM=2GB
# pgvector halfvec 类型的 HNSW 索引最多支持 4000 维
MAX_HNSW_DIMENSION=4000

while (($#)); do
  case "$1" in
    --execute)
      EXECUTE=true
      shift
      ;;
    --container|--maintenance-work-mem)
      if (($# < 2)) || [[ -z "$2" ]]; then
        fail "$1 缺少参数"
      fi
      case "$1" in
        --container) CONTAINER=$2 ;;
        --maintenance-work-mem) MAINTENANCE_WORK_MEM=$2 ;;
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

[[ $MAINTENANCE_WORK_MEM =~ ^[0-9]+(kB|MB|GB)$ ]] || fail "内存格式不正确：$MAINTENANCE_WORK_MEM（示例：512MB、2GB）"
command -v docker >/dev/null || fail '未找到 Docker 命令'
[[ $(docker inspect --format '{{.State.Running}}' "$CONTAINER" 2>/dev/null) == true ]] || \
  fail "容器未运行：$CONTAINER"

# 使用容器内的 POSTGRES_USER / POSTGRES_DB 连接，无需另外传入账号
run_psql() {
  docker exec -i "$CONTAINER" sh -c \
    'exec psql -X -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"' psql "$@"
}

# 输出与该维度 HNSW 表达式匹配的索引：每行“索引名 是否有效(t/f)”
index_states() {
  run_psql -At -F ' ' -c "
    SELECT c.relname, i.indisvalid
    FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_indexes p ON p.indexname = c.relname
    WHERE p.tablename = 'embeddings' AND p.indexdef LIKE '%halfvec($1)%'"
}

[[ $(run_psql -At -c "SELECT to_regclass('public.embeddings') IS NOT NULL") == t ]] || \
  fail 'embeddings 表不存在'

printf '统计 embeddings 表中的向量维度……\n'
DIMENSIONS=$(run_psql -At -F ' ' -c 'SELECT dimension, count(*) FROM embeddings GROUP BY 1 ORDER BY 1')
if [[ -z $DIMENSIONS ]]; then
  printf 'embeddings 表为空，无需建索引。\n'
  exit 0
fi

declare -a TO_CREATE=()
declare -a TO_DROP=()
declare -a SKIPPED=()
while read -r dimension count; do
  if ((dimension > MAX_HNSW_DIMENSION)); then
    printf '  %s 维（%s 条）：超过 HNSW 上限 %s 维，无法建索引\n' "$dimension" "$count" "$MAX_HNSW_DIMENSION"
    SKIPPED+=("$dimension")
    continue
  fi
  valid_index=
  invalid_indexes=()
  while read -r name valid; do
    [[ -n $name ]] || continue
    if [[ $valid == t ]]; then
      valid_index=$name
    else
      invalid_indexes+=("$name")
    fi
  done <<< "$(index_states "$dimension")"

  if [[ -n $valid_index ]]; then
    printf '  %s 维（%s 条）：已有索引 %s\n' "$dimension" "$count" "$valid_index"
    continue
  fi
  for name in "${invalid_indexes[@]}"; do
    printf '  %s 维（%s 条）：发现无效索引 %s，将先删除\n' "$dimension" "$count" "$name"
    TO_DROP+=("$name")
  done
  printf '  %s 维（%s 条）：缺少索引，将创建 embeddings_embedding_idx_%s\n' "$dimension" "$count" "$dimension"
  TO_CREATE+=("$dimension")
done <<< "$DIMENSIONS"

if ((${#SKIPPED[@]})); then
  printf '\n注意：%s 维无法建 HNSW 索引，向量检索会全表扫描，数据量大时可能超时（错误码 2201）。\n' "${SKIPPED[*]}"
  printf '建议将 embedding 输出维度调整到 %s 以内，并重新向量化。\n' "$MAX_HNSW_DIMENSION"
fi
if ((${#TO_CREATE[@]} == 0)); then
  printf '\n可建索引的维度均已有有效索引，无需处理。\n'
  exit 0
fi
if ! $EXECUTE; then
  printf '\n以上仅为预览。确认后加 --execute 执行。\n'
  exit 0
fi

for name in "${TO_DROP[@]}"; do
  printf '\n删除无效索引 %s……\n' "$name"
  run_psql -c "DROP INDEX CONCURRENTLY IF EXISTS \"$name\""
done

for dimension in "${TO_CREATE[@]}"; do
  index_name="embeddings_embedding_idx_$dimension"
  printf '\n[%s] 开始创建 %s（maintenance_work_mem=%s）……\n' "$(date '+%F %T')" "$index_name" "$MAINTENANCE_WORK_MEM"
  started=$(date +%s)
  # 单进程构建：并行构建依赖 /dev/shm，Docker 默认仅 64MB
  run_psql \
    -c "SET maintenance_work_mem = '$MAINTENANCE_WORK_MEM'" \
    -c 'SET max_parallel_maintenance_workers = 0' \
    -c "CREATE INDEX CONCURRENTLY IF NOT EXISTS $index_name ON embeddings
        USING hnsw ((embedding::halfvec($dimension)) halfvec_cosine_ops)
        WITH (m = 16, ef_construction = 64)
        WHERE (dimension = $dimension)" || \
    fail "创建 $index_name 失败；重新执行本脚本会先清理残留的无效索引"

  result=$(run_psql -At -F ' ' -c "
    SELECT i.indisvalid, pg_size_pretty(pg_relation_size(c.oid))
    FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
    WHERE c.relname = '$index_name'")
  read -r valid size <<< "$result"
  [[ $valid == t ]] || fail "$index_name 创建后无效；重新执行本脚本会先清理并重建"
  printf '[%s] %s 创建完成：大小 %s，耗时 %s 分钟\n' \
    "$(date '+%F %T')" "$index_name" "$size" "$((($(date +%s) - started) / 60))"
done

printf '\n全部完成。升级后迁移检测到索引已存在会直接跳过。\n'
