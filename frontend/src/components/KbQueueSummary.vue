<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getRuntimeQueueSummary, type RuntimeQueueSummaryResponse } from '@/api/system'

const { t } = useI18n()
const summary = ref<RuntimeQueueSummaryResponse | null>(null)
const failed = ref(false)
let loading = false
let pollTimer: ReturnType<typeof setInterval> | null = null

async function refresh() {
  if (loading || document.hidden) return
  loading = true
  try {
    summary.value = await getRuntimeQueueSummary()
    failed.value = false
  } catch {
    failed.value = true
  } finally {
    loading = false
  }
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function handleVisibilityChange() {
  stopPolling()
  if (document.hidden) return
  refresh()
  pollTimer = setInterval(refresh, 5000)
}

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibilityChange)
  handleVisibilityChange()
})

onUnmounted(() => {
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  stopPolling()
})
</script>

<template>
  <div class="kb-queue-summary" role="status" aria-live="polite">
    <t-tooltip :content="t('knowledgeBase.queueSummary.description')" placement="bottom">
      <span class="kb-queue-summary__title">
        <t-icon name="queue" size="14px" />
        {{ t('knowledgeBase.queueSummary.title') }}
      </span>
    </t-tooltip>
    <span v-if="failed" class="kb-queue-summary__state kb-queue-summary__state--error">
      {{ t('system.globalSettings.runtime.errors.generic') }}
    </span>
    <span v-else-if="!summary" class="kb-queue-summary__state">
      {{ t('system.globalSettings.runtime.loading') }}
    </span>
    <span v-else-if="!summary.available" class="kb-queue-summary__state">
      {{ t('system.globalSettings.runtime.unavailableTitle') }}
    </span>
    <template v-else>
      <span class="kb-queue-summary__metric">
        {{ t('system.globalSettings.runtime.summary.active') }}
        <strong class="kb-queue-summary__active">{{ summary.active }}</strong>
      </span>
      <span class="kb-queue-summary__metric">
        {{ t('system.globalSettings.runtime.summary.pending') }}
        <strong>{{ summary.pending }}</strong>
      </span>
    </template>
  </div>
</template>

<style scoped lang="less">
.kb-queue-summary {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  margin-left: auto;
  padding: 6px 10px;
  border-radius: 6px;
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 18px;

  &__title,
  &__metric {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }

  &__title {
    padding-right: 12px;
    border-right: 1px solid var(--td-component-stroke);
  }

  &__metric strong {
    color: var(--td-text-color-primary);
    font-size: 14px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  &__metric .kb-queue-summary__active {
    color: var(--td-success-color);
  }

  &__state--error {
    color: var(--td-error-color);
  }
}
</style>
