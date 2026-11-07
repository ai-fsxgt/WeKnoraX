<template>
  <div class="system-user-overview">
    <div class="section-header">
      <h2>{{ t('systemUserOverview.title') }}</h2>
      <p>{{ t('systemUserOverview.description') }}</p>
    </div>

    <t-alert v-if="error" theme="error" :message="error" />
    <template v-else>
      <div v-if="summary" class="summary-grid">
        <div class="summary-card">
          <span class="summary-label">{{ t('systemUserOverview.summary.users') }}</span>
          <strong>{{ formatNumber(summary.total_users) }}</strong>
          <span class="secondary">
            {{ t('systemUserOverview.summary.activeUsers', { count: formatNumber(summary.active_users) }) }}
          </span>
        </div>
        <div class="summary-card">
          <span class="summary-label">{{ t('systemUserOverview.summary.usersWithKnowledgeBases') }}</span>
          <strong>{{ formatNumber(summary.users_with_knowledge_bases) }}</strong>
        </div>
        <div class="summary-card">
          <span class="summary-label">{{ t('systemUserOverview.summary.knowledgeBases') }}</span>
          <strong>{{ formatNumber(summary.knowledge_base_count) }}</strong>
        </div>
        <div class="summary-card">
          <span class="summary-label">{{ t('systemUserOverview.summary.documents') }}</span>
          <strong>{{ formatNumber(summary.document_count) }}</strong>
        </div>
        <div class="summary-card">
          <span class="summary-label">{{ t('systemUserOverview.summary.storage') }}</span>
          <strong>{{ formatCapacity(summary.storage_used) }}</strong>
          <span class="secondary">
            {{ t('systemUserOverview.summary.quota', {
              quota: summary.storage_quota > 0
                ? formatCapacity(summary.storage_quota)
                : t('systemTenants.unlimited'),
            }) }}
          </span>
        </div>
      </div>

      <div class="overview-toolbar">
        <div class="overview-filters">
          <t-input
            v-model="searchInput"
            class="overview-search"
            clearable
            :placeholder="t('systemUserOverview.searchPlaceholder')"
            @enter="applyFilters"
            @clear="applyFilters"
          >
            <template #prefix-icon><t-icon name="search" /></template>
          </t-input>
          <t-select v-model="sortBy" class="overview-sort" :options="sortOptions" />
          <t-checkbox v-model="hasKnowledgeBase">
            {{ t('systemUserOverview.onlyWithKnowledgeBase') }}
          </t-checkbox>
        </div>
        <t-button :loading="loading" @click="applyFilters">{{ t('systemUserOverview.search') }}</t-button>
      </div>

      <div class="overview-table-shell">
        <div class="overview-table-scroll">
          <t-table row-key="id" :data="users" :columns="columns" :loading="loading" hover>
            <template #user="{ row }">
              <div class="identity">
                <span class="username">{{ row.username }}</span>
                <span class="secondary">{{ row.email }}</span>
              </div>
            </template>
            <template #status="{ row }">
              <t-tag :theme="row.is_active ? 'success' : 'default'" variant="light" size="small">
                {{ row.is_active ? t('systemUsers.status.active') : t('systemUsers.status.disabled') }}
              </t-tag>
            </template>
            <template #workspace_count="{ row }">{{ formatNumber(row.workspace_count) }}</template>
            <template #knowledge_base_count="{ row }">{{ formatNumber(row.knowledge_base_count) }}</template>
            <template #document_count="{ row }">{{ formatNumber(row.document_count) }}</template>
            <template #storage_size="{ row }">{{ formatCapacity(row.storage_size) }}</template>
            <template #created_at="{ row }">{{ formatDate(row.created_at) }}</template>
            <template #empty>{{ t('systemUserOverview.empty') }}</template>
          </t-table>
        </div>
        <div v-if="total > 0" class="overview-pager">
          <t-pagination
            v-model="page"
            v-model:page-size="pageSize"
            :total="total"
            :page-size-options="[10, 20, 50]"
            size="small"
            show-jumper
            show-page-size
            @change="loadOverview"
          />
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listSystemUserOverview,
  type SystemUserOverviewItem,
  type SystemUserOverviewSort,
  type SystemUserOverviewSummary,
} from '@/api/system'

const { t, locale } = useI18n()
const CAPACITY_UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

const summary = ref<SystemUserOverviewSummary | null>(null)
const users = ref<SystemUserOverviewItem[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const searchInput = ref('')
const searchQuery = ref('')
const sortBy = ref<SystemUserOverviewSort>('created_at')
const hasKnowledgeBase = ref(false)
const loading = ref(false)
const error = ref('')
let loadRequestId = 0

const sortOptions = computed(() => [
  { value: 'created_at', label: t('systemUserOverview.sort.createdAt') },
  { value: 'knowledge_base_count', label: t('systemUserOverview.sort.knowledgeBaseCount') },
  { value: 'storage_size', label: t('systemUserOverview.sort.storageSize') },
])

const columns = computed(() => [
  { colKey: 'user', title: t('systemUserOverview.columns.user'), minWidth: 200 },
  { colKey: 'status', title: t('systemUserOverview.columns.status'), width: 90 },
  { colKey: 'workspace_count', title: t('systemUserOverview.columns.workspaces'), width: 100 },
  { colKey: 'knowledge_base_count', title: t('systemUserOverview.columns.knowledgeBases'), width: 100 },
  { colKey: 'document_count', title: t('systemUserOverview.columns.documents'), width: 100 },
  { colKey: 'storage_size', title: t('systemUserOverview.columns.storage'), width: 120 },
  { colKey: 'created_at', title: t('systemUserOverview.columns.createdAt'), width: 130 },
])

function formatNumber(value: number): string {
  return new Intl.NumberFormat(locale.value).format(value)
}

function formatCapacity(bytes: number): string {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < CAPACITY_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  return `${new Intl.NumberFormat(locale.value, { maximumFractionDigits: 2 }).format(value)} ${CAPACITY_UNITS[unit]}`
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(locale.value, { year: 'numeric', month: '2-digit', day: '2-digit' })
    .format(new Date(value))
}

async function loadOverview() {
  const requestId = ++loadRequestId
  loading.value = true
  error.value = ''
  try {
    const response = await listSystemUserOverview({
      query: searchQuery.value,
      has_knowledge_base: hasKnowledgeBase.value,
      sort_by: sortBy.value,
      page: page.value,
      page_size: pageSize.value,
    })
    if (requestId !== loadRequestId) return
    summary.value = response.summary
    users.value = response.users
    total.value = response.total
  } catch (err: any) {
    if (requestId === loadRequestId) error.value = err.message
  } finally {
    if (requestId === loadRequestId) loading.value = false
  }
}

function applyFilters() {
  searchQuery.value = searchInput.value.trim()
  page.value = 1
  void loadOverview()
}

watch([sortBy, hasKnowledgeBase], applyFilters)

onMounted(loadOverview)
onBeforeUnmount(() => { loadRequestId++ })
</script>

<style scoped lang="less">
.system-user-overview { color: var(--td-text-color-primary); }
.section-header {
  margin-bottom: 24px;
  h2 { margin: 0 0 8px; font-size: 22px; line-height: 1.3; }
  p { margin: 0; color: var(--td-text-color-secondary); line-height: 1.6; }
}
.summary-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}
.summary-card {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 14px 16px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  background: var(--td-bg-color-container);
  strong { font-size: 22px; line-height: 1.2; }
}
.summary-label { color: var(--td-text-color-secondary); font-size: 13px; }
.overview-toolbar, .overview-filters {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.overview-toolbar { justify-content: space-between; margin-bottom: 14px; }
.overview-search { width: 260px; }
.overview-sort { width: 180px; }
.overview-table-shell {
  overflow: hidden;
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  background: var(--td-bg-color-container);
}
.overview-table-scroll { overflow-x: auto; }
.overview-pager {
  display: flex;
  justify-content: flex-end;
  padding: 12px 14px;
  border-top: 1px solid var(--td-component-stroke);
}
.identity { display: flex; flex-direction: column; gap: 3px; overflow-wrap: anywhere; }
.username { font-weight: 500; }
.secondary { color: var(--td-text-color-secondary); font-size: 12px; }
</style>
