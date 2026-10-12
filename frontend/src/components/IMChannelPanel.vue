<template>
  <div class="im-admin-panel">
    <div class="section-header">
      <div>
        <h2>{{ t('imAdmin.title') }}</h2>
        <p>{{ t('imAdmin.description') }}</p>
        <p class="scope-hint">{{ t('imAdmin.scopeHint') }}</p>
      </div>
      <t-button theme="primary" variant="text" @click="openCreate">
        <template #icon><t-icon name="add" /></template>
        {{ t('agentEditor.im.addChannel') }}
      </t-button>
    </div>

    <t-loading :loading="loading" show-overlay>
      <div v-if="channels.length" class="channel-list">
        <article v-for="channel in channels" :key="channel.id" class="channel-card">
          <div class="channel-main">
            <img
              v-if="platformLogo(channel.platform)"
              :src="platformLogo(channel.platform)"
              :alt="platformLabel(channel.platform)"
              class="platform-logo"
            />
            <t-icon v-else name="chat-message" size="38px" class="platform-logo" />
            <div class="channel-copy">
              <div class="channel-title">
                <h3>{{ channel.name || platformLabel(channel.platform) }}</h3>
                <t-tag :theme="channel.enabled ? 'success' : 'default'" variant="light" size="small">
                  {{ channel.enabled ? t('common.on') : t('common.off') }}
                </t-tag>
              </div>
              <div class="channel-meta">
                <span>{{ platformLabel(channel.platform) }}</span>
                <span>{{ channel.mode }}</span>
                <span>{{ t(channel.binding_mode === 'unified' ? 'imAdmin.unifiedMode' : 'imAdmin.flexibleMode') }}</span>
                <span>{{ channel.output_mode === 'stream' ? t('agentEditor.im.outputStream') : t('agentEditor.im.outputFull') }}</span>
              </div>
              <div class="channel-state">
                <span :class="{ ok: channel.credentials_configured }">
                  <t-icon :name="channel.credentials_configured ? 'check-circle' : 'error-circle'" />
                  {{ channel.credentials_configured ? t('imAdmin.credentialsConfigured') : t('imAdmin.credentialsMissing') }}
                </span>
                <span :class="{ ok: channel.personal_binding }">
                  <t-icon :name="channel.personal_binding ? 'user-checked' : 'info-circle'" />
                  {{ channel.personal_binding ? t('imAdmin.personalBindingReady') : t('imAdmin.personalBindingUnavailable') }}
                </span>
                <span v-if="channel.personal_binding" :class="{ ok: channel.allow_group_chat }">
                  <t-icon name="chat-message" />
                  {{ channel.allow_group_chat ? t('imAdmin.groupChatEnabled') : t('imAdmin.groupChatDisabled') }}
                </span>
              </div>
              <div v-if="channel.mode === 'webhook' && isSupportedPlatform(channel.platform)" class="callback-row">
                <code>{{ callbackURL(channel.id) }}</code>
                <t-button size="small" variant="text" :title="t('common.copy')" @click="copyCallback(channel.id)">
                  <t-icon name="file-copy" />
                </t-button>
              </div>
            </div>
          </div>
          <div class="channel-actions">
            <t-switch
              :value="channel.enabled"
              :loading="togglingId === channel.id"
              :disabled="!isSupportedPlatform(channel.platform)"
              @change="toggleChannel(channel)"
            />
            <t-button
              size="small"
              variant="outline"
              :disabled="!isSupportedPlatform(channel.platform)"
              @click="openEdit(channel)"
            >
              {{ t('common.edit') }}
            </t-button>
            <t-button size="small" variant="outline" theme="danger" @click="confirmDelete(channel)">
              {{ t('common.delete') }}
            </t-button>
          </div>
        </article>
      </div>
      <div v-else-if="!loading" class="empty-state">
        <t-icon name="chat-message" size="42px" />
        <h3>{{ t('agentEditor.im.empty') }}</h3>
        <p>{{ t('imAdmin.description') }}</p>
      </div>
    </t-loading>

    <SettingDrawer
      v-model:visible="drawerVisible"
      :title="editingChannel ? t('agentEditor.im.editChannel') : t('agentEditor.im.addChannel')"
      :description="stepTitles[step]"
      width="620px"
      storage-key="setting-drawer:system-im-channel"
      :confirm-loading="saving"
      :confirm-text="step < 2 ? t('common.next') : t('common.save')"
      @confirm="confirmStep"
      @cancel="closeDrawer"
    >
      <template #headerIcon>
        <img :src="platformLogo(form.platform)" :alt="platformLabel(form.platform)" class="drawer-logo" />
      </template>
      <template v-if="step > 0" #footer-left>
        <t-button variant="outline" @click="step -= 1">{{ t('datasource.back') }}</t-button>
      </template>

      <div class="steps">
        <div v-for="(title, index) in stepTitles" :key="title" :class="['step', { active: step === index, done: step > index }]">
          <span>{{ step > index ? '✓' : index + 1 }}</span>
          {{ title }}
        </div>
      </div>

      <div v-if="step === 0" class="form-stack">
        <label>{{ t('agentEditor.im.platform') }}</label>
        <t-select v-model="form.platform" :disabled="Boolean(editingChannel)" @change="onPlatformChange">
          <t-option v-for="option in platformOptions" :key="option.value" :value="option.value" :label="option.label">
            <div class="platform-option">
              <img :src="option.logo" :alt="option.label" />
              <span>{{ option.label }}</span>
            </div>
          </t-option>
        </t-select>

        <label>{{ t('agentEditor.im.channelName') }}</label>
        <t-input v-model="form.name" :placeholder="platformLabel(form.platform)" :maxlength="128" />

        <template v-if="form.platform !== 'wechat' && form.platform !== 'qqbot'">
          <label>{{ t('imAdmin.bindingMode') }}</label>
          <t-radio-group v-model="form.binding_mode">
            <t-radio value="flexible">{{ t('imAdmin.flexibleMode') }}</t-radio>
            <t-radio value="unified">{{ t('imAdmin.unifiedMode') }}</t-radio>
          </t-radio-group>
          <t-alert theme="info" :message="t(form.binding_mode === 'unified' ? 'imAdmin.unifiedModeHint' : 'imAdmin.flexibleModeHint')" />
        </template>
      </div>

      <div v-else-if="step === 1" class="form-stack">
        <template v-if="form.platform === 'wechat'">
          <t-alert theme="info" :message="t('imAdmin.wechatFixedMode')" />
        </template>
        <template v-else>
          <label>{{ t('agentEditor.im.mode') }}</label>
          <t-radio-group v-model="form.mode" @change="onModeChange">
            <t-radio value="websocket">WebSocket</t-radio>
            <t-radio v-if="form.platform !== 'qqbot'" value="webhook">Webhook</t-radio>
          </t-radio-group>
        </template>

        <label>{{ t('agentEditor.im.outputMode') }}</label>
        <t-radio-group v-model="form.output_mode" :disabled="form.platform === 'wechat'">
          <t-radio value="stream">{{ t('agentEditor.im.outputStream') }}</t-radio>
          <t-radio value="full">{{ t('agentEditor.im.outputFull') }}</t-radio>
        </t-radio-group>

        <div v-if="form.binding_mode === 'flexible' && supportsGroupChat(form.platform, form.mode)" class="group-chat-setting">
          <div>
            <label>{{ t('imAdmin.allowGroupChat') }}</label>
            <p>{{ t('imAdmin.allowGroupChatHint') }}</p>
          </div>
          <t-switch v-model="form.allow_group_chat" />
        </div>

        <t-alert v-if="editingChannel && form.mode === 'webhook'" theme="info">
          <template #message>
            <div class="drawer-callback">
              <span>{{ t('agentEditor.im.callbackUrl') }}</span>
              <code>{{ callbackURL(editingChannel.id) }}</code>
            </div>
          </template>
        </t-alert>
      </div>

      <div v-else class="form-stack">
        <t-alert v-if="editingChannel && editingChannel.credentials_configured" theme="info" :message="t('imAdmin.credentialsPreserved')" />

        <div v-if="form.platform === 'wechat'" class="wechat-binding">
          <t-alert
            v-if="editingChannel?.credentials_configured && !wechatCredentialsReady"
            theme="success"
            :message="t('imAdmin.credentialsConfigured')"
          />
          <t-button :loading="wechatLoading" variant="outline" @click="startWeChatBinding">
            {{ t('agentEditor.im.wechatScanBind') }}
          </t-button>
          <div v-if="wechatQRContent" class="wechat-qr">
            <t-qrcode :value="wechatQRContent" :size="200" />
            <p>{{ wechatStatus === 'scaned' ? t('agentEditor.im.wechatBinding') : t('agentEditor.im.wechatScanning') }}</p>
          </div>
          <t-alert v-if="wechatStatus === 'expired'" theme="warning" :message="t('agentEditor.im.wechatQRExpired')" />
          <t-alert v-if="wechatCredentialsReady" theme="success" :message="t('agentEditor.im.wechatBindSuccess')" />
        </div>

        <template v-else>
          <div
            v-for="field in credentialFields"
            :key="`${editingChannel?.id || 'new'}:${form.platform}:${form.mode}:${field.key}`"
            class="credential-field"
          >
            <label>{{ field.label }}</label>
            <t-input
              v-model="form.credentials[field.key]"
              :name="`im-${editingChannel?.id || 'new'}-${form.platform}-${form.mode}-${field.key}`"
              :type="field.secret ? 'password' : 'text'"
              :placeholder="editingChannel ? t('imAdmin.credentialsPreserved') : ''"
              :autocomplete="field.secret ? 'new-password' : 'off'"
            />
          </div>
        </template>
      </div>
    </SettingDrawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import SettingDrawer from '@/components/settings/SettingDrawer.vue'
import { copyWithToast } from '@/utils/clipboard'
import {
  createSystemIMChannel,
  deleteSystemIMChannel,
  getWeChatQRCode,
  listSystemIMChannels,
  pollWeChatQRCodeStatus,
  toggleSystemIMChannel,
  updateSystemIMChannel,
  type IMChannelSummary,
  type IMPlatform,
} from '@/api/im'
import wecomLogo from '@/assets/img/im/wecom.svg'
import feishuLogo from '@/assets/img/im/feishu.svg'
import larkLogo from '@/assets/img/im/lark.svg'
import slackLogo from '@/assets/img/im/slack.svg'
import telegramLogo from '@/assets/img/im/telegram.svg'
import dingtalkLogo from '@/assets/img/im/dingtalk.svg'
import wechatLogo from '@/assets/img/im/wechat.svg'
import qqbotLogo from '@/assets/img/im/qqbot.png'

type CredentialField = { key: string; label: string; secret?: boolean }

const { t } = useI18n()
const channels = ref<IMChannelSummary[]>([])
const loading = ref(false)
const saving = ref(false)
const togglingId = ref('')
const drawerVisible = ref(false)
const editingChannel = ref<IMChannelSummary | null>(null)
const step = ref(0)
const wechatLoading = ref(false)
const wechatQRCode = ref('')
const wechatQRContent = ref('')
const wechatStatus = ref('')
const wechatCredentialsReady = ref(false)
let wechatPollTimer: ReturnType<typeof setTimeout> | null = null
let wechatPolling = false

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

const platformOptions = computed(() => (Object.keys(logoMap) as IMPlatform[]).map(value => ({
  value,
  label: platformLabel(value),
  logo: logoMap[value],
})))

const fieldsByPlatform: Record<IMPlatform, CredentialField[]> = {
  wecom: [],
  feishu: [
    { key: 'app_id', label: 'App ID' },
    { key: 'app_secret', label: 'App Secret', secret: true },
    { key: 'api_base_url', label: 'API Base URL' },
    { key: 'verification_token', label: 'Verification Token', secret: true },
    { key: 'encrypt_key', label: 'Encrypt Key', secret: true },
  ],
  lark: [
    { key: 'app_id', label: 'App ID' },
    { key: 'app_secret', label: 'App Secret', secret: true },
    { key: 'api_base_url', label: 'API Base URL' },
    { key: 'verification_token', label: 'Verification Token', secret: true },
    { key: 'encrypt_key', label: 'Encrypt Key', secret: true },
  ],
  slack: [
    { key: 'bot_token', label: 'Bot Token', secret: true },
    { key: 'app_token', label: 'App Token', secret: true },
    { key: 'signing_secret', label: 'Signing Secret', secret: true },
  ],
  telegram: [
    { key: 'bot_token', label: 'Bot Token', secret: true },
    { key: 'secret_token', label: 'Secret Token', secret: true },
  ],
  dingtalk: [
    { key: 'client_id', label: 'Client ID' },
    { key: 'client_secret', label: 'Client Secret', secret: true },
    { key: 'card_template_id', label: 'Card Template ID' },
  ],
  wechat: [],
  qqbot: [
    { key: 'app_id', label: 'App ID' },
    { key: 'client_secret', label: 'Client Secret', secret: true },
    { key: 'api_base_url', label: 'API Base URL' },
    { key: 'gateway_url', label: 'Gateway URL' },
  ],
}

const form = reactive({
  platform: 'wecom' as IMPlatform,
  name: '',
  mode: 'websocket' as IMChannelSummary['mode'],
  output_mode: 'stream' as IMChannelSummary['output_mode'],
  binding_mode: 'flexible' as IMChannelSummary['binding_mode'],
  allow_group_chat: false,
  credentials: {} as Record<string, string>,
})

const stepTitles = computed(() => [
  t('agentEditor.im.stepBasic'),
  t('agentEditor.im.stepConnection'),
  t('agentEditor.im.stepCredentials'),
])

const credentialFields = computed<CredentialField[]>(() => {
  if (form.platform !== 'wecom') return fieldsByPlatform[form.platform]
  return form.mode === 'webhook'
    ? [
        { key: 'corp_id', label: 'Corp ID' },
        { key: 'corp_agent_id', label: 'Agent ID' },
        { key: 'agent_secret', label: 'Agent Secret', secret: true },
        { key: 'token', label: 'Token', secret: true },
        { key: 'encoding_aes_key', label: 'Encoding AES Key', secret: true },
        { key: 'api_base_url', label: 'API Base URL' },
      ]
    : [
        { key: 'bot_id', label: 'Bot ID' },
        { key: 'bot_secret', label: 'Bot Secret', secret: true },
        { key: 'ws_endpoint', label: 'WebSocket Endpoint' },
      ]
})

function isSupportedPlatform(platform: IMChannelSummary['platform']): platform is IMPlatform {
  return Object.prototype.hasOwnProperty.call(logoMap, platform)
}

function platformLogo(platform: IMChannelSummary['platform']) {
  return isSupportedPlatform(platform) ? logoMap[platform] : ''
}

function platformLabel(platform: IMChannelSummary['platform']) {
  return t(`agentEditor.im.${platform}`)
}

function supportsGroupChat(platform: IMPlatform, mode: string) {
  if (platform === 'wechat' || platform === 'qqbot') return false
  return platform !== 'wecom' || mode === 'websocket'
}

function resetForm() {
  stopWeChatPolling()
  Object.assign(form, {
    platform: 'wecom',
    name: '',
    mode: 'websocket',
    output_mode: 'stream',
    binding_mode: 'flexible',
    allow_group_chat: false,
    credentials: {},
  })
  step.value = 0
  wechatQRCode.value = ''
  wechatQRContent.value = ''
  wechatStatus.value = ''
  wechatCredentialsReady.value = false
}

function openCreate() {
  editingChannel.value = null
  resetForm()
  drawerVisible.value = true
}

function openEdit(channel: IMChannelSummary) {
  if (!isSupportedPlatform(channel.platform)) return
  resetForm()
  editingChannel.value = channel
  Object.assign(form, {
    platform: channel.platform,
    name: channel.name,
    mode: channel.mode,
    output_mode: channel.output_mode,
    binding_mode: channel.binding_mode,
    allow_group_chat: channel.allow_group_chat,
    credentials: { ...channel.credential_ids },
  })
  drawerVisible.value = true
}

function closeDrawer() {
  drawerVisible.value = false
  resetForm()
}

function onPlatformChange() {
  form.credentials = {}
  form.binding_mode = 'flexible'
  form.allow_group_chat = false
  if (form.platform === 'wechat') {
    form.mode = 'longpoll'
    form.output_mode = 'full'
  } else {
    form.mode = 'websocket'
    form.output_mode = 'stream'
  }
}

function onModeChange() {
  if (!supportsGroupChat(form.platform, form.mode)) {
    form.allow_group_chat = false
  }
}

function cleanedCredentials() {
  return Object.fromEntries(
    Object.entries(form.credentials)
      .map(([key, value]) => [key, String(value).trim()]),
  )
}

async function confirmStep() {
  if (saving.value) return
  if (step.value < 2) {
    step.value += 1
    return
  }
  if (form.platform === 'wechat' && !editingChannel.value?.credentials_configured && !wechatCredentialsReady.value) {
    MessagePlugin.warning(t('agentEditor.im.wechatScanBind'))
    return
  }

  saving.value = true
  try {
    const payload = {
      name: form.name.trim() || platformLabel(form.platform),
      mode: form.mode,
      output_mode: form.output_mode,
      binding_mode: form.binding_mode,
      allow_group_chat: form.allow_group_chat,
      credentials: cleanedCredentials(),
    }
    if (editingChannel.value) {
      await updateSystemIMChannel(editingChannel.value.id, payload)
      MessagePlugin.success(t('common.updateSuccess'))
    } else {
      await createSystemIMChannel({ ...payload, platform: form.platform })
      MessagePlugin.success(t('common.createSuccess'))
    }
    closeDrawer()
    await loadChannels()
  } catch (error: any) {
    MessagePlugin.error(error?.message === 'binding_mode_in_use'
      ? t('imAdmin.bindingModeInUse')
      : error?.message || t('common.operationFailed'))
  } finally {
    saving.value = false
  }
}

async function loadChannels() {
  loading.value = true
  try {
    const response = await listSystemIMChannels()
    channels.value = response.data || []
  } catch (error: any) {
    channels.value = []
    MessagePlugin.error(error?.message || t('imAdmin.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function toggleChannel(channel: IMChannelSummary) {
  if (!isSupportedPlatform(channel.platform)) return
  togglingId.value = channel.id
  try {
    await toggleSystemIMChannel(channel.id)
    await loadChannels()
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('common.operationFailed'))
  } finally {
    togglingId.value = ''
  }
}

function confirmDelete(channel: IMChannelSummary) {
  const dialog = DialogPlugin.confirm({
    header: t('common.delete'),
    body: t('agentEditor.im.deleteConfirm'),
    confirmBtn: { content: t('common.delete'), theme: 'danger' },
    cancelBtn: t('common.cancel'),
    onConfirm: async () => {
      dialog.destroy()
      try {
        await deleteSystemIMChannel(channel.id)
        MessagePlugin.success(t('common.deleteSuccess'))
        await loadChannels()
      } catch (error: any) {
        MessagePlugin.error(error?.message || t('common.operationFailed'))
      }
    },
    onCancel: () => dialog.destroy(),
  })
}

function callbackURL(channelID: string) {
  return `${window.location.origin}/api/v1/im/callback/${channelID}`
}

async function copyCallback(channelID: string) {
  await copyWithToast(callbackURL(channelID), 'common.copySuccess')
}

async function startWeChatBinding() {
  stopWeChatPolling()
  wechatLoading.value = true
  wechatQRContent.value = ''
  wechatStatus.value = ''
  try {
    const response = await getWeChatQRCode()
    wechatQRCode.value = response.data.qrcode
    wechatQRContent.value = response.data.qrcode_url
    wechatStatus.value = 'wait'
    wechatPolling = true
    void pollWeChatStatus()
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('common.operationFailed'))
  } finally {
    wechatLoading.value = false
  }
}

async function pollWeChatStatus() {
  if (!wechatPolling) return
  try {
    const response = await pollWeChatQRCodeStatus(wechatQRCode.value)
    if (!wechatPolling) return
    wechatStatus.value = response.data.status
    if (response.data.status === 'confirmed' && response.data.credentials) {
      form.credentials = { ...response.data.credentials }
      wechatCredentialsReady.value = true
      stopWeChatPolling()
      MessagePlugin.success(t('agentEditor.im.wechatBindSuccess'))
      return
    }
    if (response.data.status === 'expired') {
      stopWeChatPolling()
      return
    }
  } catch {
    if (!wechatPolling) return
  }
  wechatPollTimer = setTimeout(() => void pollWeChatStatus(), 500)
}

function stopWeChatPolling() {
  wechatPolling = false
  if (wechatPollTimer) {
    clearTimeout(wechatPollTimer)
    wechatPollTimer = null
  }
}

onMounted(loadChannels)
onUnmounted(stopWeChatPolling)
</script>

<style scoped lang="less">
.im-admin-panel {
  min-height: 240px;
}

.section-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 24px;
  margin-bottom: 20px;

  h2,
  p {
    margin: 0;
  }

  h2 {
    margin-bottom: 6px;
    font-size: 18px;
  }

  p {
    color: var(--td-text-color-secondary);
    font-size: 13px;
    line-height: 1.6;
  }

  .scope-hint {
    margin-top: 4px;
  }
}

.channel-list {
  display: grid;
  gap: 12px;
}

.channel-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  padding: 18px;
  border: 1px solid var(--td-component-border);
  border-radius: 10px;
  background: var(--td-bg-color-container);
}

.channel-main,
.channel-title,
.channel-meta,
.channel-state,
.channel-actions,
.callback-row,
.platform-option {
  display: flex;
  align-items: center;
}

.channel-main {
  min-width: 0;
  gap: 14px;
}

.platform-logo,
.drawer-logo {
  width: 38px;
  height: 38px;
  object-fit: contain;
}

.drawer-logo {
  width: 24px;
  height: 24px;
}

.channel-copy {
  min-width: 0;
}

.channel-title {
  gap: 8px;

  h3 {
    margin: 0;
    font-size: 15px;
  }
}

.channel-meta,
.channel-state {
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 7px;
  color: var(--td-text-color-secondary);
  font-size: 12px;
}

.channel-state span {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.channel-state .ok {
  color: var(--td-success-color);
}

.channel-actions {
  flex-shrink: 0;
  gap: 8px;
}

.callback-row {
  gap: 4px;
  margin-top: 8px;

  code {
    max-width: 520px;
    overflow: hidden;
    color: var(--td-text-color-secondary);
    font-size: 11px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}

.empty-state {
  padding: 52px 20px;
  color: var(--td-text-color-secondary);
  text-align: center;

  h3 {
    margin: 12px 0 6px;
    color: var(--td-text-color-primary);
  }

  p {
    margin: 0;
  }
}

.steps {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  margin-bottom: 24px;
}

.step {
  display: flex;
  align-items: center;
  gap: 7px;
  color: var(--td-text-color-placeholder);
  font-size: 12px;

  span {
    display: inline-flex;
    width: 24px;
    height: 24px;
    align-items: center;
    justify-content: center;
    border-radius: 50%;
    background: var(--td-bg-color-secondarycontainer);
  }

  &.active,
  &.done {
    color: var(--td-brand-color);

    span {
      color: #fff;
      background: var(--td-brand-color);
    }
  }
}

.form-stack {
  display: flex;
  flex-direction: column;
  gap: 12px;

  label {
    color: var(--td-text-color-primary);
    font-size: 13px;
    font-weight: 500;
  }
}

.group-chat-setting {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  padding: 12px;
  border-radius: 8px;
  background: var(--td-bg-color-secondarycontainer);

  p {
    margin: 4px 0 0;
    color: var(--td-text-color-secondary);
    font-size: 12px;
    line-height: 1.5;
  }
}

.credential-field {
  display: grid;
  gap: 7px;
}

.platform-option {
  gap: 8px;

  img {
    width: 22px;
    height: 22px;
    object-fit: contain;
  }
}

.drawer-callback {
  display: grid;
  gap: 6px;

  code {
    overflow-wrap: anywhere;
  }
}

.wechat-binding {
  display: grid;
  justify-items: start;
  gap: 14px;
}

.wechat-qr {
  text-align: center;

  p {
    margin: 8px 0 0;
    color: var(--td-text-color-secondary);
    font-size: 12px;
  }
}

@media (max-width: 900px) {
  .channel-card {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
