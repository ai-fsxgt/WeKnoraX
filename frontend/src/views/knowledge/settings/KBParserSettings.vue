<template>
  <div class="kb-parser-settings" :class="{ 'kb-parser-settings--embedded': embedded }">
    <div v-if="!embedded" class="section-header">
      <h2>{{ $t('kbSettings.parser.title') }}</h2>
      <p class="section-description">{{ $t('kbSettings.parser.description') }}</p>
    </div>

    <div v-if="loading" class="loading-inline">
      <t-loading size="small" />
      <span>{{ $t('kbSettings.parser.loading') }}</span>
    </div>

    <t-alert v-else-if="loadError" theme="error" :message="loadError">
      <template #operation>
        <t-button size="small" variant="text" @click="loadEngines(true)">{{ $t('settings.parser.retry') }}</t-button>
      </template>
    </t-alert>

    <div v-else-if="fileTypeGroups.length === 0" class="empty-hint">
      <p>{{ $t('kbSettings.parser.noEngineAvailable') }}</p>
    </div>

    <div v-else class="settings-group" :class="{ 'settings-group--embedded': embedded }">
      <div
        v-for="group in fileTypeGroups"
        :key="group.key"
        class="setting-row"
      >
        <div class="setting-info">
          <label class="group-label">
            <t-icon v-if="!embedded" :name="group.icon" class="group-icon" />
            {{ group.label }}
          </label>
          <div class="ext-tags">
            <span v-for="ext in group.extensions" :key="ext" class="ext-tag">.{{ ext }}</span>
          </div>
        </div>
        <div class="setting-control">
          <div class="parser-control-stack">
            <t-select
              :value="getEngineForGroup(group.extensions) || undefined"
              @change="(val: string) => handleEngineChange(group.extensions, val)"
              :style="embedded ? undefined : { width: '280px' }"
              :class="{ 'parser-engine-select--embedded': embedded }"
              :status="isSystemSelectionUnavailable(group.extensions) || !hasAvailableEngine(group.extensions) ? 'warning' : 'default'"
              :value-display="getSelectedLabel(group.extensions)"
              :placeholder="$t('kbSettings.parser.noEngine')"
              :popup-props="{ overlayInnerStyle: { maxHeight: '240px' } }"
            >
              <t-option-group
                v-if="getEngineOptions(group.extensions).length"
                :divider="getSystemEngineOptions(group.extensions).length > 0"
              >
                <t-option
                  v-for="opt in getEngineOptions(group.extensions)"
                  :key="opt.value"
                  :value="opt.value"
                  :label="opt.selectLabel"
                />
              </t-option-group>
              <t-option-group
                v-if="getSystemEngineOptions(group.extensions).length"
                :divider="getEngineOptions(group.extensions).length > 0"
              >
                <t-option
                  v-for="opt in getSystemEngineOptions(group.extensions)"
                  :key="opt.value"
                  :value="opt.value"
                  :label="opt.selectLabel"
                />
              </t-option-group>
            </t-select>
            <p v-if="isSystemSelectionUnavailable(group.extensions)" class="system-engine-warning">
              <t-icon name="info-circle" />
              {{ $t('systemParser.selectionUnavailable') }}
            </p>
            <t-checkbox
              v-if="group.extensions.includes('xlsx') && getEngineForGroup(group.extensions) === 'builtin'"
              class="xlsx-header-option"
              :checked="getXLSXFirstRowAsHeader(group.extensions)"
              @change="(checked: boolean) => handleXLSXFirstRowAsHeaderChange(group.extensions, checked)"
            >
              {{ $t('kbSettings.parser.xlsxFirstRowAsHeader') }}
            </t-checkbox>
            <div v-if="!hasAvailableEngine(group.extensions)" class="no-engine-warning">
              <a class="go-settings" @click.prevent="goToParserSettings">{{ $t('kbSettings.parser.goConfig') }}</a>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { type ParserEngineInfo } from '@/api/system'
import type { ParserEngineRule } from '@/types/knowledgeProcess'
import { useEditorResourcesStore } from '@/stores/editorResources'
import { useUIStore } from '@/stores/ui'
import { storeToRefs } from 'pinia'

export type { ParserEngineRule } from '@/types/knowledgeProcess'

const { t } = useI18n()
const editorResources = useEditorResourcesStore()

function getEngineDisplayName(engineName: string): string {
  const key = `kbSettings.parser.engines.${engineName}.name`
  const translated = t(key)
  return translated !== key ? translated : engineName
}

interface EngineOption {
  value: string
  selectLabel: string
  isDefault: boolean
}

function buildOptionLabel(name: string, isDefault: boolean): string {
  const label = getEngineDisplayName(name)
  return isDefault ? `${label} (${t('kbSettings.parser.default')})` : label
}

interface Props {
  parserEngineRules?: ParserEngineRule[]
  /** Compact layout for upload-confirm dialog */
  embedded?: boolean
  allowSystemEngines?: boolean
  /** When set, only show file-type groups matching these extensions */
  relevantExtensions?: string[]
}

const props = withDefaults(defineProps<Props>(), {
  parserEngineRules: () => [],
  embedded: false,
  allowSystemEngines: false,
  relevantExtensions: () => [],
})

const emit = defineEmits<{
  'update:parserEngineRules': [value: ParserEngineRule[]]
}>()

const uiStore = useUIStore()
const localEngineRules = ref<ParserEngineRule[]>([...props.parserEngineRules])
const parserEngines = ref<ParserEngineInfo[]>([])
const systemParserEngines = ref<ParserEngineInfo[]>([])
const loading = ref(true)
const loadError = ref('')
let loadVersion = 0
onUnmounted(() => { loadVersion += 1 })

const allFileTypes = computed(() => {
  const s = new Set<string>()
  for (const engine of [...parserEngines.value, ...systemParserEngines.value]) {
    for (const ft of engine.FileTypes || []) {
      s.add(ft)
    }
  }
  for (const rule of localEngineRules.value) {
    for (const ft of rule.file_types) s.add(ft)
  }
  return s
})

const fileTypeGroups = computed(() => {
  const ft = allFileTypes.value
  const groups: { key: string; label: string; icon: string; extensions: string[] }[] = []

  const pdfExts = ['pdf'].filter(e => ft.has(e))
  const officeExts = ['docx', 'doc'].filter(e => ft.has(e))
  const pptExts = ['pptx', 'ppt'].filter(e => ft.has(e))
  const excelExts = ['xlsx', 'xls'].filter(e => ft.has(e))
  const ebookExts = ['epub'].filter(e => ft.has(e))
  const webArchiveExts = ['mhtml'].filter(e => ft.has(e))
  const csvExts = ['csv'].filter(e => ft.has(e))
  const mdExts = ['md', 'markdown'].filter(e => ft.has(e))
  const txtExts = ['txt'].filter(e => ft.has(e))
  const jsonExts = ['json'].filter(e => ft.has(e))
  const imageExts = ['jpg', 'jpeg', 'png', 'gif', 'bmp', 'tiff', 'webp'].filter(e => ft.has(e))
  const audioExts = ['mp3', 'wav', 'm4a', 'flac', 'ogg'].filter(e => ft.has(e))
  const audiovisualExts = [...audioExts]

  if (pdfExts.length) groups.push({ key: 'pdf', label: t('kbSettings.parser.fileTypePdf'), icon: 'file-pdf', extensions: pdfExts })
  if (officeExts.length) groups.push({ key: 'office', label: t('kbSettings.parser.fileTypeWord'), icon: 'file-word', extensions: officeExts })
  if (pptExts.length) groups.push({ key: 'ppt', label: t('kbSettings.parser.fileTypePpt'), icon: 'file-powerpoint', extensions: pptExts })
  if (excelExts.length) groups.push({ key: 'excel', label: t('kbSettings.parser.fileTypeExcel'), icon: 'file-excel', extensions: excelExts })
  if (ebookExts.length) groups.push({ key: 'ebook', label: t('kbSettings.parser.fileTypeEbook'), icon: 'file', extensions: ebookExts })
  if (webArchiveExts.length) groups.push({ key: 'webarchive', label: t('kbSettings.parser.fileTypeWebArchive'), icon: 'file', extensions: webArchiveExts })
  if (csvExts.length) groups.push({ key: 'csv', label: t('kbSettings.parser.fileTypeCsv'), icon: 'file-excel', extensions: csvExts })
  if (mdExts.length) groups.push({ key: 'markdown', label: 'Markdown', icon: 'file-code', extensions: mdExts })
  if (txtExts.length) groups.push({ key: 'text', label: t('kbSettings.parser.fileTypeText'), icon: 'file', extensions: txtExts })
  if (jsonExts.length) groups.push({ key: 'json', label: t('kbSettings.parser.fileTypeJson'), icon: 'file-code', extensions: jsonExts })
  if (imageExts.length) groups.push({ key: 'image', label: t('kbSettings.parser.fileTypeImage'), icon: 'image', extensions: imageExts })
  if (audiovisualExts.length) {
    groups.push({
      key: 'audiovisual',
      label: t('kbSettings.parser.fileTypeAudiovisual'),
      icon: 'sound',
      extensions: audiovisualExts,
    })
  }

  // Keep the UI driven by the backend registry. New parser plugins can expose
  // file types without requiring another frontend release; known families get
  // friendly labels above and everything else gets a compact dynamic row.
  const grouped = new Set(groups.flatMap(group => group.extensions))
  for (const ext of [...ft].filter(ext => !grouped.has(ext) && ext !== 'url').sort()) {
    groups.push({ key: `dynamic-${ext}`, label: ext.toUpperCase(), icon: 'file-code', extensions: [ext] })
  }

  // A system parser may support PNG/JPEG but not every image format. Split
  // these rows by actual capabilities so selecting a group never saves an
  // unsupported extension or hides a valid system option.
  const selectableGroups = props.allowSystemEngines ? groups.flatMap(group => {
    const buckets = new Map<string, string[]>()
    for (const ext of group.extensions) {
      const supported = systemParserEngines.value.filter(engine => engine.FileTypes.includes(ext))
      const rule = localEngineRules.value.find(item => item.file_types.includes(ext))
      const key = JSON.stringify([supported.map(engine => engine.Name), rule?.source || 'tenant', rule?.engine || ''])
      const bucket = buckets.get(key) || []
      bucket.push(ext)
      buckets.set(key, bucket)
    }
    return [...buckets.values()].map(extensions => ({
      ...group,
      key: `${group.key}-${extensions.join('-')}`,
      extensions,
    }))
  }) : groups

  const rel = props.relevantExtensions
  if (!rel?.length) return selectableGroups
  const relSet = new Set(rel)
  return selectableGroups
    .map(group => ({ ...group, extensions: group.extensions.filter(ext => relSet.has(ext)) }))
    .filter(group => group.extensions.length > 0)
})

function getEngineOptions(extensions: string[]): EngineOption[] {
  const raw: { name: string; desc: string; fileTypes: string[]; available: boolean; reason: string }[] = []
  for (const engine of parserEngines.value) {
    const supports = extensions.some(ext => (engine.FileTypes || []).includes(ext))
    if (supports) {
      raw.push({
        name: engine.Name,
        desc: engine.Description || engine.Name,
        fileTypes: engine.FileTypes || [],
        available: engine.Available !== false,
        reason: engine.UnavailableReason || '',
      })
    }
  }
  const defaultName = raw.find(e => e.available)?.name ?? ''
  return raw
    .filter(e => e.available)
    .map(e => ({
      value: e.name,
      selectLabel: buildOptionLabel(e.name, defaultName !== '' && e.name === defaultName),
      isDefault: defaultName !== '' && e.name === defaultName,
    }))
}

function getSystemEngineOptions(extensions: string[]): EngineOption[] {
  if (!props.allowSystemEngines) return []
  return systemParserEngines.value
    .filter(engine => extensions.every(ext => engine.FileTypes.includes(ext)))
    .map(engine => ({
      value: `system:${engine.Name}`,
      selectLabel: t('systemParser.optionLabel', { name: getEngineDisplayName(engine.Name) }),
      isDefault: false,
    }))
}

function isSystemSelectionUnavailable(extensions: string[]): boolean {
  const rule = getRuleForGroup(extensions)
  return rule?.source === 'system'
    && !getSystemEngineOptions(extensions).some(option => option.value === `system:${rule.engine}`)
}

function getSelectedLabel(extensions: string[]): string | undefined {
  const value = getEngineForGroup(extensions)
  const option = [...getEngineOptions(extensions), ...getSystemEngineOptions(extensions)]
    .find(item => item.value === value)
  if (option) return option.selectLabel
  const rule = getRuleForGroup(extensions)
  if (rule?.source === 'system') {
    return t('systemParser.optionLabel', { name: getEngineDisplayName(rule.engine) })
  }
  return undefined
}

function hasAvailableEngine(extensions: string[]): boolean {
  return getEngineOptions(extensions).length > 0 || getSystemEngineOptions(extensions).length > 0
}

function getDefaultEngine(extensions: string[]): string {
  const opts = getEngineOptions(extensions)
  return opts.find(o => o.isDefault)?.value ?? ''
}

function getEngineForGroup(extensions: string[]): string {
  for (const rule of localEngineRules.value) {
    if (rule.file_types.some(ft => extensions.includes(ft))) {
      return rule.source === 'system' ? `system:${rule.engine}` : rule.engine
    }
  }
  return getDefaultEngine(extensions)
}

function handleEngineChange(extensions: string[], engine: string) {
  const currentRule = getRuleForGroup(extensions)
  const otherRules = localEngineRules.value
    .map(rule => ({ ...rule, file_types: rule.file_types.filter(ft => !extensions.includes(ft)) }))
    .filter(rule => rule.file_types.length > 0)
  const systemSelection = engine.startsWith('system:')
  if (engine) {
    otherRules.push({
      file_types: [...extensions],
      engine: systemSelection ? engine.slice('system:'.length) : engine,
      ...(systemSelection ? { source: 'system' as const } : {}),
      ...(currentRule?.xlsx_first_row_as_header !== undefined
        ? { xlsx_first_row_as_header: currentRule.xlsx_first_row_as_header }
        : {}),
    })
  }
  localEngineRules.value = otherRules
  emit('update:parserEngineRules', buildCompleteRules())
}

function getRuleForGroup(extensions: string[]): ParserEngineRule | undefined {
  return localEngineRules.value.find(
    rule => rule.file_types.some(fileType => extensions.includes(fileType))
  )
}

function getXLSXFirstRowAsHeader(extensions: string[]): boolean {
  return getRuleForGroup(extensions)?.xlsx_first_row_as_header === true
}

function handleXLSXFirstRowAsHeaderChange(extensions: string[], checked: boolean) {
  const rules = buildCompleteRules()
  const rule = rules.find(item => item.file_types.some(fileType => extensions.includes(fileType)))
  if (!rule) return

  rule.xlsx_first_row_as_header = checked
  localEngineRules.value = rules
  emit('update:parserEngineRules', rules)
}

function buildCompleteRules(): ParserEngineRule[] {
  const rules = localEngineRules.value.map(rule => ({ ...rule, file_types: [...rule.file_types] }))
  for (const group of fileTypeGroups.value) {
    const missingTypes = group.extensions.filter(ext => !rules.some(rule => rule.file_types.includes(ext)))
    const engine = getDefaultEngine(missingTypes)
    if (missingTypes.length && engine) {
      rules.push({ file_types: missingTypes, engine })
    }
  }
  return rules
}

function goToParserSettings() {
  uiStore.openSettings('parser')
}

async function loadEngines(force = false) {
  const version = ++loadVersion
  loading.value = true
  loadError.value = ''
  try {
    await editorResources.ensureParserEngines(force)
    if (version !== loadVersion) return
    parserEngines.value = editorResources.parserEngines
    systemParserEngines.value = props.allowSystemEngines ? editorResources.systemParserEngines : []
    ensureCompleteRules()
  } catch (error: any) {
    if (version !== loadVersion) return
    loadError.value = error?.message || t('settings.parser.loadFailed')
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

function ensureCompleteRules() {
  if (!parserEngines.value.length) return
  const complete = buildCompleteRules()
  if (complete.length && complete.length > localEngineRules.value.length) {
    localEngineRules.value = complete
    emit('update:parserEngineRules', complete)
  }
}

onMounted(() => loadEngines(props.allowSystemEngines))

const { showSettingsModal } = storeToRefs(uiStore)
watch(showSettingsModal, (open, wasOpen) => {
  if (wasOpen && !open) {
    loadEngines(true)
  }
})

watch(() => props.parserEngineRules, (v) => {
  localEngineRules.value = v?.length ? [...v] : []
}, { deep: true })
</script>

<style lang="less" scoped>
.kb-parser-settings {
  width: 100%;
}

.section-header {
  margin-bottom: 20px;

  h2 {
    font-size: 20px;
    font-weight: 600;
    color: var(--td-text-color-primary);
    margin: 0 0 6px 0;
  }

  .section-description {
    font-size: 14px;
    color: var(--td-text-color-secondary);
    margin: 0;
    line-height: 1.5;
  }
}

.loading-inline {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 16px 0;
}

.empty-hint {
  padding: 24px 0;
  color: var(--td-text-color-secondary);
}

.settings-group {
  display: flex;
  flex-direction: column;
  gap: 0;
}

.setting-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  padding: 16px 0;
  border-bottom: 1px solid var(--td-component-stroke);

  &:last-child {
    border-bottom: none;
  }
}

.setting-info {
  flex: 0 0 40%;
  max-width: 40%;
  padding-right: 24px;

  .group-label {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .group-icon {
    font-size: 18px;
    color: var(--td-text-color-secondary);
    flex-shrink: 0;
  }

  label {
    font-size: 15px;
    font-weight: 500;
    color: var(--td-text-color-primary);
    display: block;
    margin-bottom: 4px;
  }

  .ext-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 6px;
  }

  .ext-tag {
    display: inline-block;
    font-size: 12px;
    line-height: 1;
    color: var(--td-text-color-secondary);
    background: var(--td-bg-color-secondarycontainer);
    padding: 3px 8px;
    border-radius: 4px;
    font-family: var(--app-font-family-mono);
  }

  .desc {
    font-size: 13px;
    color: var(--td-text-color-secondary);
    margin: 0;
    line-height: 1.5;
  }
}

.setting-control {
  flex: 0 0 55%;
  max-width: 55%;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
}

.parser-control-stack {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 10px;
  width: 280px;
}

.xlsx-header-option {
  align-self: stretch;

  :deep(.t-checkbox) {
    align-items: flex-start;
  }

  :deep(.t-checkbox__label) {
    font-size: 12px;
    line-height: 1.5;
    text-align: left;
    white-space: normal;
  }
}

.no-engine-warning {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--td-warning-color);
  line-height: 1.4;

  .go-settings {
    color: var(--td-brand-color);
    cursor: pointer;
    white-space: nowrap;
    text-decoration: none;

    &:hover {
      text-decoration: underline;
    }
  }
}

.system-engine-warning {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0;
  color: var(--td-warning-color);
  font-size: 12px;
  line-height: 1.5;

  .t-icon {
    flex-shrink: 0;
    margin-top: 2px;
  }
}

// ---- 下拉选项样式 ----
.kb-parser-settings--embedded {
  .settings-group {
    border: 1px solid var(--td-component-stroke);
    border-radius: 8px;
    background: var(--td-bg-color-secondarycontainer, #f8f9fb);
    overflow: hidden;
  }

  .setting-row {
    flex-direction: row;
    align-items: center;
    gap: 16px;
    padding: 10px 14px;
    background: var(--td-bg-color-container, #fff);
    border-bottom: 1px solid var(--td-component-stroke);

    &:last-child {
      border-bottom: none;
    }
  }

  .setting-info {
    flex: 0 0 168px;
    max-width: 168px;
    padding-right: 0;
    display: block;
  }

  .setting-control {
    flex: 1;
    min-width: 0;
    max-width: none;
    align-items: stretch;
  }

  .parser-control-stack {
    width: 100%;
  }

  .group-label {
    font-size: 13px;
    font-weight: 500;
    margin-bottom: 4px;
  }

  .ext-tags {
    margin-top: 0;
    gap: 4px;
  }

  .ext-tag {
    font-size: 11px;
    padding: 2px 6px;
  }

  .parser-engine-select--embedded {
    width: 100%;
  }
}
</style>
