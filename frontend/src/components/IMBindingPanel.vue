<template>
  <div class="im-binding-panel">
    <t-loading :loading="loading" show-overlay>
      <t-tabs v-model="activeTab" class="binding-tabs">
        <t-tab-panel value="create" :label="t('imBinding.createTitle')" :destroy-on-hide="false">
          <section v-if="channels.length" class="binding-create">
            <div class="binding-form">
              <label>{{ t('imBinding.workspace') }}</label>
              <t-select v-model="selectedTenantID" :options="workspaceOptions" />

              <label>{{ t('imBinding.channel') }}</label>
              <t-select v-model="selectedChannelID" :placeholder="t('imBinding.channelPlaceholder')">
                <t-option
                  v-for="channel in availableChannels"
                  :key="channel.id"
                  :value="channel.id"
                  :label="channel.name || platformLabel(channel.platform)"
                  :disabled="channel.binding_mode === 'unified' && channel.is_bound"
                >
                  <div class="channel-option">
                    <img :src="platformLogo(channel.platform)" :alt="platformLabel(channel.platform)" />
                    <span>{{ channel.name || platformLabel(channel.platform) }}</span>
                    <small>{{ platformLabel(channel.platform) }}</small>
                    <small v-if="channel.binding_mode === 'unified'">
                      {{ t(channel.is_bound ? 'imBinding.unifiedBound' : 'imAdmin.unifiedMode') }}
                    </small>
                  </div>
                </t-option>
              </t-select>

              <t-alert v-if="selectedChannel?.binding_mode === 'unified'" theme="info" :message="t('imBinding.unifiedNotice')" />

              <t-button
                theme="primary"
                :loading="generating"
                :disabled="loading || !selectedTenantID || !canBindSelectedChannel"
                @click="generateBindingCode"
              >
                {{ t('imBinding.generate') }}
              </t-button>
            </div>

            <div v-if="pendingRequest?.code" class="binding-code">
              <div class="binding-code__header">
                <div>
                  <h4>{{ t('imBinding.codeTitle') }}</h4>
                  <p>{{ t(bindingCodeHintKey, { channel: pendingRequest.channel_name || platformLabel(pendingRequest.platform) }) }}</p>
                </div>
                <t-tag theme="warning" variant="light">{{ t('imBinding.codeExpires', { time: expiresAtText }) }}</t-tag>
              </div>
              <button type="button" class="code-value" @click="copyCode">
                <code>{{ pendingRequest.code }}</code>
                <t-icon name="file-copy" />
              </button>
              <t-alert theme="info" :message="t(bindingNoticeKey)" />
            </div>

            <t-alert v-else-if="requestState === 'expired'" theme="warning" :message="t('imBinding.expired')" />
          </section>

          <section v-else-if="!loading" class="binding-create no-channels">
            <t-icon name="chat-message" size="46px" />
            <h3>{{ t('imBinding.noChannelsTitle') }}</h3>
            <p>{{ t('imBinding.noChannels') }}</p>
          </section>
        </t-tab-panel>

        <t-tab-panel
          value="bindings"
          :label="`${t('imBinding.currentBindings')} (${bindings.length})`"
          :destroy-on-hide="false"
        >
          <section class="binding-list-section">
            <div v-if="bindings.length" class="binding-list">
              <article v-for="binding in bindings" :key="binding.id" class="binding-card">
                <img :src="platformLogo(binding.platform)" :alt="platformLabel(binding.platform)" />
                <div class="binding-card__copy">
                  <div class="binding-card__title">
                    <strong>{{ binding.channel_name || platformLabel(binding.platform) }}</strong>
                    <span class="platform-name">{{ platformLabel(binding.platform) }}</span>
                    <t-tag size="small" variant="light">
                      {{ t(binding.binding_scope === 'channel' ? 'imAdmin.unifiedMode' : binding.chat_type === 'group' ? 'imBinding.groupBinding' : 'imBinding.privateBinding') }}
                    </t-tag>
                  </div>
                  <p v-if="binding.binding_scope === 'channel'">{{ t('imBinding.unifiedScope') }}</p>
                  <p v-else>
                    {{ t(binding.chat_type === 'group' ? 'imBinding.groupConversation' : 'imBinding.externalAccount') }}：
                    {{ bindingDisplayName(binding) }}
                  </p>
                  <p>{{ t('imBinding.workspaceLabel') }}：{{ binding.tenant_name }}</p>
                </div>
                <t-button size="small" variant="outline" theme="danger" @click="confirmRevoke(binding)">
                  {{ t('imBinding.revoke') }}
                </t-button>
              </article>
            </div>
            <div v-else-if="!loading" class="empty-bindings">
              <t-icon name="user-clear" size="36px" />
              <p>{{ t('imBinding.noBindings') }}</p>
            </div>
          </section>
        </t-tab-panel>
      </t-tabs>
    </t-loading>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { copyWithToast } from '@/utils/clipboard'
import {
  createIMBindingRequest,
  deleteIMBinding,
  getIMBindingRequest,
  listBindableIMChannels,
  listIMBindings,
  type BindableIMChannel,
  type IMBindingRequest,
  type IMPlatform,
  type IMUserBinding,
} from '@/api/im'
import wecomLogo from '@/assets/img/im/wecom.svg'
import feishuLogo from '@/assets/img/im/feishu.svg'
import larkLogo from '@/assets/img/im/lark.svg'
import slackLogo from '@/assets/img/im/slack.svg'
import telegramLogo from '@/assets/img/im/telegram.svg'
import dingtalkLogo from '@/assets/img/im/dingtalk.svg'
import wechatLogo from '@/assets/img/im/wechat.svg'
import qqbotLogo from '@/assets/img/im/qqbot.png'

const { t } = useI18n()
const authStore = useAuthStore()
const activeTab = ref('create')
const channels = ref<BindableIMChannel[]>([])
const bindings = ref<IMUserBinding[]>([])
const loading = ref(false)
const generating = ref(false)
const selectedTenantID = ref<number | null>(null)
const selectedChannelID = ref('')
const pendingRequest = ref<IMBindingRequest | null>(null)
const requestState = ref<IMBindingRequest['status'] | ''>('')
let pollTimer: ReturnType<typeof setTimeout> | null = null
let polling = false

const logoMap: Record<IMPlatform, string> = {
  wecom: wecomLogo,
  feishu: feishuLogo,
  lark: larkLogo,
  slack: slackLogo,
  telegram: telegramLogo,
  dingtalk: dingtalkLogo,
  wechat: wechatLogo,
  qqbot: qqbotLogo,
}

const workspaceOptions = computed(() => authStore.memberships.map(membership => ({
  value: membership.tenant_id,
  label: membership.tenant_name || String(membership.tenant_id),
})))

const privateBoundChannelIDs = computed(() => new Set(bindings.value
  .filter(binding => binding.binding_scope === 'conversation' && binding.chat_type === 'direct')
  .map(binding => binding.im_channel_id)))
const availableChannels = computed(() => channels.value.filter(
  channel => channel.binding_mode === 'unified' || channel.allow_group_chat || !privateBoundChannelIDs.value.has(channel.id),
))
const selectedChannel = computed(() => availableChannels.value.find(channel => channel.id === selectedChannelID.value))
const canBindSelectedChannel = computed(() => Boolean(selectedChannel.value)
  && !(selectedChannel.value?.binding_mode === 'unified' && selectedChannel.value.is_bound))
const groupBindingAllowed = computed(() => pendingRequest.value?.allow_group_chat === true)
const bindingCodeHintKey = computed(() => !groupBindingAllowed.value ? 'imBinding.codeHint'
  : pendingRequest.value?.binding_mode === 'unified' ? 'imBinding.codeHintUnified' : 'imBinding.codeHintGroup')
const bindingNoticeKey = computed(() => pendingRequest.value?.binding_mode === 'unified'
  ? 'imBinding.unifiedNotice'
  : groupBindingAllowed.value ? 'imBinding.groupChatNotice' : 'imBinding.privateChatOnly')

const expiresAtText = computed(() => {
  if (!pendingRequest.value) return ''
  return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
    .format(new Date(pendingRequest.value.expires_at))
})

function platformLogo(platform: IMPlatform) {
  return logoMap[platform]
}

function platformLabel(platform: IMPlatform) {
  return t(`agentEditor.im.${platform}`)
}

function bindingDisplayName(binding: IMUserBinding) {
  if (binding.external_user_name) return binding.external_user_name
  if (binding.chat_type === 'direct') return binding.external_user_id
  return binding.external_user_id.slice(-8)
}

function chooseDefaults() {
  const activeTenantID = authStore.effectiveTenantId
  const activeMembership = authStore.memberships.find(item => item.tenant_id === activeTenantID)
  selectedTenantID.value = activeMembership?.tenant_id ?? authStore.memberships[0]?.tenant_id ?? null
  if (!canBindSelectedChannel.value) {
    selectedChannelID.value = availableChannels.value.find(channel => !channel.is_bound)?.id || ''
  }
}

async function loadData() {
  loading.value = true
  try {
    const [channelResponse, bindingResponse] = await Promise.all([
      listBindableIMChannels(),
      listIMBindings(),
    ])
    channels.value = channelResponse.data || []
    bindings.value = bindingResponse.data || []
    chooseDefaults()
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('imBinding.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function generateBindingCode() {
  if (loading.value || generating.value || !selectedTenantID.value || !canBindSelectedChannel.value) return
  stopPolling()
  pendingRequest.value = null
  generating.value = true
  requestState.value = ''
  try {
    const response = await createIMBindingRequest(selectedChannelID.value, selectedTenantID.value)
    pendingRequest.value = response.data
    requestState.value = response.data.status
    polling = true
    schedulePoll(1500)
  } catch (error: any) {
    if (error?.message === 'channel_already_bound') {
      MessagePlugin.error(t('imBinding.unifiedAlreadyBound'))
      await loadData()
    } else {
      MessagePlugin.error(error?.message || t('imBinding.failed'))
    }
  } finally {
    generating.value = false
  }
}

function schedulePoll(delay = 2000) {
  if (!polling) return
  pollTimer = setTimeout(() => void pollBindingRequest(), delay)
}

async function pollBindingRequest() {
  if (!polling || !pendingRequest.value) return
  const requestID = pendingRequest.value.id
  try {
    const response = await getIMBindingRequest(requestID)
    if (!polling || pendingRequest.value?.id !== requestID) return
    requestState.value = response.data.status
    if (response.data.status === 'consumed') {
      stopPolling()
      pendingRequest.value = null
      MessagePlugin.success(t('imBinding.success'))
      await loadData()
      return
    }
    if (response.data.status === 'expired' || response.data.status === 'cancelled') {
      stopPolling()
      pendingRequest.value = null
      if (response.data.status === 'cancelled') await loadData()
      return
    }
  } catch (error: any) {
    if (!polling || pendingRequest.value?.id !== requestID) return
    stopPolling()
    pendingRequest.value = null
    MessagePlugin.error(error?.message || t('imBinding.failed'))
    return
  }
  if (polling && pendingRequest.value?.id === requestID) schedulePoll()
}

function stopPolling() {
  polling = false
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

async function copyCode() {
  if (!pendingRequest.value?.code) return
  await copyWithToast(pendingRequest.value.code, 'common.copySuccess')
}

function confirmRevoke(binding: IMUserBinding) {
  const dialog = DialogPlugin.confirm({
    header: t('imBinding.revoke'),
    body: t(binding.binding_scope === 'channel' ? 'imBinding.revokeUnifiedConfirm' : 'imBinding.revokeConfirm', {
      channel: binding.channel_name || platformLabel(binding.platform),
    }),
    confirmBtn: { content: t('imBinding.revoke'), theme: 'danger' },
    cancelBtn: t('common.cancel'),
    onConfirm: async () => {
      dialog.destroy()
      try {
        await deleteIMBinding(binding.id)
        MessagePlugin.success(t('imBinding.revoked'))
        await loadData()
      } catch (error: any) {
        MessagePlugin.error(error?.message || t('imBinding.failed'))
      }
    },
    onCancel: () => dialog.destroy(),
  })
}

onMounted(loadData)
onUnmounted(stopPolling)
</script>

<style scoped lang="less">
.im-binding-panel {
  min-width: 0;
  min-height: 260px;
}

.binding-tabs {
  min-width: 0;

  :deep(.t-tabs__nav-item) {
    font-size: 13px;
  }

  :deep(.t-tabs__content) {
    padding-top: 18px;
  }
}

.binding-create,
.binding-list-section {
  min-width: 0;
  padding: 20px;
  border: 1px solid var(--td-component-border);
  border-radius: 10px;
  background: var(--td-bg-color-container);
  box-sizing: border-box;
}

.binding-form {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 10px;

  label {
    color: var(--td-text-color-primary);
    font-size: 13px;
    font-weight: 500;
  }

  button {
    margin-top: 4px;
  }
}

.channel-option {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;

  img {
    width: 22px;
    height: 22px;
    flex-shrink: 0;
    object-fit: contain;
  }

  span {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  small {
    flex-shrink: 0;
    margin-left: auto;
    color: var(--td-text-color-placeholder);
  }
}

.binding-code {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 12px;
  margin-top: 20px;
  padding-top: 18px;
  border-top: 1px solid var(--td-component-stroke);
}

.binding-code__header {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;

  > div {
    flex: 1 1 240px;
    min-width: 0;
    overflow-wrap: anywhere;
  }

  h4,
  p {
    margin: 0;
  }

  h4 {
    margin-bottom: 5px;
    font-size: 14px;
  }

  p {
    color: var(--td-text-color-secondary);
    font-size: 12px;
    line-height: 1.6;
  }
}

.code-value {
  display: flex;
  min-width: 0;
  width: 100%;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border: 1px dashed var(--td-brand-color);
  border-radius: 8px;
  color: var(--td-brand-color);
  background: var(--td-brand-color-light);
  box-sizing: border-box;
  cursor: pointer;

  code {
    min-width: 0;
    font-size: 18px;
    font-weight: 600;
    letter-spacing: 1px;
    overflow-wrap: anywhere;
    text-align: left;
  }
}

.binding-list {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 10px;
}

.binding-card {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  padding: 13px;
  border-radius: 8px;
  background: var(--td-bg-color-secondarycontainer);

  > img {
    width: 34px;
    height: 34px;
    flex-shrink: 0;
    object-fit: contain;
  }

  > button {
    flex-shrink: 0;
    margin-left: auto;
  }
}

.binding-card__copy {
  flex: 1 1 240px;
  min-width: 0;

  p {
    margin: 4px 0 0;
    color: var(--td-text-color-secondary);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
}

.binding-card__title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;

  strong {
    min-width: 0;
    font-size: 13px;
    overflow-wrap: anywhere;
  }

  .platform-name {
    color: var(--td-text-color-placeholder);
    font-size: 11px;
  }
}

.empty-bindings,
.no-channels {
  padding: 42px 16px;
  color: var(--td-text-color-secondary);
  text-align: center;

  h3 {
    margin: 12px 0 6px;
    color: var(--td-text-color-primary);
  }

  p {
    margin: 8px 0 0;
  }
}

</style>
