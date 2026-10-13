<template>
  <div class="retrieval-settings">
    <!-- 查询扩展（仅普通模式） -->
    <div v-if="showQueryExpansion" class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agent.editor.enableQueryExpansion') }}</label>
        <p class="desc">{{ $t('agentEditor.desc.queryExpansion') }}</p>
      </div>
      <div class="setting-control">
        <t-switch v-model="config.enable_query_expansion" />
      </div>
    </div>

    <!-- 向量召回TopK -->
    <div class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agent.editor.embeddingTopK') }}</label>
        <p class="desc">{{ $t('agentEditor.desc.embeddingTopK') }}</p>
      </div>
      <div class="setting-control">
        <t-input-number v-model="config.embedding_top_k" :min="1" :max="50" theme="column" />
      </div>
    </div>

    <!-- 关键词阈值 -->
    <div class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agent.editor.keywordThreshold') }}</label>
        <p class="desc">{{ $t('agentEditor.desc.keywordThreshold') }}</p>
      </div>
      <div class="setting-control">
        <div class="slider-wrapper">
          <t-slider v-model="config.keyword_threshold" :min="0" :max="1" :step="0.01" />
          <span class="slider-value">{{ config.keyword_threshold?.toFixed(2) }}</span>
        </div>
      </div>
    </div>

    <!-- 向量阈值 -->
    <div class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agent.editor.vectorThreshold') }}</label>
        <p class="desc">{{ $t('agentEditor.desc.vectorThreshold') }}</p>
      </div>
      <div class="setting-control">
        <div class="slider-wrapper">
          <t-slider v-model="config.vector_threshold" :min="0" :max="1" :step="0.01" />
          <span class="slider-value">{{ config.vector_threshold?.toFixed(2) }}</span>
        </div>
      </div>
    </div>

    <!-- 重排TopK（仅在配置了 Rerank 模型时展示） -->
    <div v-if="showRerank" class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agent.editor.rerankTopK') }}</label>
        <p class="desc">{{ $t('agentEditor.desc.rerankTopK') }}</p>
      </div>
      <div class="setting-control">
        <t-input-number v-model="config.rerank_top_k" :min="1" :max="20" theme="column" />
      </div>
    </div>

    <!-- 重排阈值（仅在配置了 Rerank 模型时展示） -->
    <div v-if="showRerank" class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agent.editor.rerankThreshold') }}</label>
        <p class="desc">{{ $t('agentEditor.desc.rerankThreshold') }}</p>
      </div>
      <div class="setting-control">
        <div class="slider-wrapper">
          <t-slider v-model="config.rerank_threshold" :min="-10" :max="10" :step="0.01" />
          <span class="slider-value">{{ config.rerank_threshold?.toFixed(1) }}</span>
        </div>
      </div>
    </div>

    <!-- FAQ 优先策略（关联 FAQ 类型知识库时显示） -->
    <div v-if="showFaq" class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agentEditor.faq.enableLabel') }}</label>
        <p class="desc">{{ $t('agentEditor.faq.enableDesc') }}</p>
      </div>
      <div class="setting-control">
        <t-switch v-model="config.faq_priority_enabled" />
      </div>
    </div>

    <div v-if="showFaq && showDirectAnswer && config.faq_priority_enabled" class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agentEditor.faq.thresholdLabel') }}</label>
        <p class="desc">{{ $t('agentEditor.faq.thresholdDesc') }}</p>
      </div>
      <div class="setting-control">
        <div class="slider-wrapper">
          <t-slider v-model="config.faq_direct_answer_threshold" :min="0.7" :max="1"
            :step="0.05" />
          <span class="slider-value">{{ config.faq_direct_answer_threshold?.toFixed(2)
            }}</span>
        </div>
      </div>
    </div>

    <div v-if="showFaq && config.faq_priority_enabled" class="setting-row">
      <div class="setting-info">
        <label>{{ $t('agentEditor.faq.boostLabel') }}</label>
        <p class="desc">{{ $t('agentEditor.faq.boostDesc') }}</p>
      </div>
      <div class="setting-control">
        <div class="slider-wrapper">
          <t-slider v-model="config.faq_score_boost" :min="1" :max="2" :step="0.1" />
          <span class="slider-value">{{ config.faq_score_boost?.toFixed(1) }}x</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{
  config: {
    embedding_top_k?: number
    keyword_threshold?: number
    vector_threshold?: number
    rerank_top_k?: number
    rerank_threshold?: number
    enable_query_expansion?: boolean
    faq_priority_enabled?: boolean
    faq_direct_answer_threshold?: number
    faq_score_boost?: number
  }
  showQueryExpansion?: boolean
  showRerank?: boolean
  showFaq?: boolean
  showDirectAnswer?: boolean
}>(), {
  showQueryExpansion: true,
  showRerank: true,
  showFaq: true,
  showDirectAnswer: true,
})
</script>

<style lang="less" scoped>
.setting-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 24px;
  padding: 16px 0;
  border-bottom: 1px solid var(--td-component-stroke);
}
.setting-info { flex: 0 0 42%; max-width: 42%; min-width: 0; }
.setting-info label { display: block; margin-bottom: 4px; font-size: 15px; font-weight: 500; color: var(--td-text-color-primary); }
.desc { margin: 0; font-size: 13px; line-height: 1.5; color: var(--td-text-color-secondary); }
.setting-control { display: flex; justify-content: flex-end; flex: 1 1 58%; max-width: 58%; min-width: 0; }
.setting-control :deep(.t-input-number) { width: 120px; }
.slider-wrapper { display: flex; align-items: center; gap: 16px; width: 100%; }
.slider-wrapper :deep(.t-slider) { flex: 1; }
.slider-value { width: 40px; text-align: right; font-family: var(--app-font-family-mono); font-size: 14px; color: var(--td-text-color-primary); }
@media (max-width: 640px) {
  .setting-row { flex-wrap: wrap; gap: 12px; }
  .setting-info { flex-basis: 100%; max-width: 100%; }
  .setting-control { max-width: 100%; }
}
</style>
