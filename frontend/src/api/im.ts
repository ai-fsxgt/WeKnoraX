import { del, get, post, put } from '@/utils/request'

export type IMPlatform =
  | 'wecom'
  | 'feishu'
  | 'lark'
  | 'slack'
  | 'telegram'
  | 'dingtalk'
  | 'wechat'
  | 'qqbot'

export interface IMChannelSummary {
  id: string
  platform: IMPlatform
  name: string
  enabled: boolean
  mode: 'webhook' | 'websocket' | 'longpoll'
  output_mode: 'stream' | 'full'
  bot_identity: string
  personal_binding: boolean
  credentials_configured: boolean
  created_by: string
  created_at: string
  updated_at: string
}

export interface IMChannelMutation {
  platform?: IMPlatform
  name?: string
  enabled?: boolean
  mode?: IMChannelSummary['mode']
  output_mode?: IMChannelSummary['output_mode']
  credentials?: Record<string, unknown>
}

export interface BindableIMChannel {
  id: string
  platform: IMPlatform
  name: string
}

export interface IMUserBinding {
  id: string
  im_channel_id: string
  platform: IMPlatform
  channel_name: string
  external_user_id: string
  external_user_name: string
  tenant_id: number
  tenant_name: string
  created_at: string
}

export interface IMBindingRequest {
  id: string
  code?: string
  im_channel_id: string
  platform: IMPlatform
  channel_name: string
  tenant_id: number
  status: 'pending' | 'consumed' | 'cancelled' | 'expired'
  binding_id?: string
  expires_at: string
}

export interface WeChatQRCodeResult {
  qrcode_url: string
  qrcode: string
}

export interface WeChatQRCodeStatus {
  status: 'wait' | 'scaned' | 'confirmed' | 'expired'
  credentials?: {
    bot_token: string
    ilink_bot_id: string
    ilink_user_id: string
  }
}

export function listSystemIMChannels() {
  return get<{ data: IMChannelSummary[] }>('/api/v1/system/admin/im-channels')
}

export function createSystemIMChannel(data: IMChannelMutation) {
  return post<{ data: IMChannelSummary }>('/api/v1/system/admin/im-channels', data)
}

export function updateSystemIMChannel(id: string, data: IMChannelMutation) {
  return put<{ data: IMChannelSummary }>(`/api/v1/system/admin/im-channels/${id}`, data)
}

export function deleteSystemIMChannel(id: string) {
  return del<{ success: boolean }>(`/api/v1/system/admin/im-channels/${id}`)
}

export function toggleSystemIMChannel(id: string) {
  return post<{ data: IMChannelSummary }>(`/api/v1/system/admin/im-channels/${id}/toggle`)
}

export function listBindableIMChannels() {
  return get<{ data: BindableIMChannel[] }>('/api/v1/im/channels')
}

export function createIMBindingRequest(imChannelId: string, tenantId: number) {
  return post<{ data: IMBindingRequest }>('/api/v1/im/binding-requests', {
    im_channel_id: imChannelId,
    tenant_id: tenantId,
  })
}

export function getIMBindingRequest(id: string) {
  return get<{ data: IMBindingRequest }>(`/api/v1/im/binding-requests/${id}`)
}

export function listIMBindings() {
  return get<{ data: IMUserBinding[] }>('/api/v1/im/bindings')
}

export function deleteIMBinding(id: string) {
  return del<{ success: boolean }>(`/api/v1/im/bindings/${id}`)
}

export function getWeChatQRCode() {
  return post<{ data: WeChatQRCodeResult }>('/api/v1/system/admin/im-channels/wechat/qrcode')
}

export function pollWeChatQRCodeStatus(qrcode: string) {
  return post<{ data: WeChatQRCodeStatus }>('/api/v1/system/admin/im-channels/wechat/qrcode/status', { qrcode })
}
