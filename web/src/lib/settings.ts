// 设置页的 API 客户端。与 Go 后端 internal/api/settings.go 对应。
//
// 约定：后端**永远不下发凭据明文**，只给 secret_set + secret_hint（末 4 位掩码）。
// 保存时 secret 是三态：不传（保留原凭据）/ 空串（清空）/ 有值（设置新值）。
import { request, type AuthKind } from './api.ts';

export interface EndpointView {
  name: string;
  kind: 'source' | 'notify';
  /** page = 页面配置接管；env = 读环境变量 */
  origin: 'page' | 'env';
  base_url: string;
  env_base_url: string;
  auth: AuthKind;
  default_auth: AuthKind;
  secret_set: boolean;
  secret_hint: string;
  secret_origin: 'page' | 'env' | '';
  credential_env: string;
  enabled: boolean;
  interval_secs: number;
  extras?: Record<string, string>;
  /** 配置本身有问题（如密文解不开）时的原因 */
  error?: string;
}

export interface Policy {
  notify_on_sync_failure: boolean;
}

export interface SettingsPayload {
  endpoints: EndpointView[];
  policy: Policy;
  /** 凭据加密密钥来源：env / file / test */
  secret_source: string;
  auth_modes: AuthKind[];
}

export interface EndpointInput {
  base_url: string;
  auth: string;
  /** 不传 = 保留；'' = 清空；其他 = 设置 */
  secret?: string;
  enabled: boolean;
  interval_secs: number;
  extras?: Record<string, string>;
}

export interface ProbeResult {
  name: string;
  url: string;
  ok: boolean;
  status: number;
  duration_ms: number;
  message: string;
  response?: string;
}

export interface NotifyResult {
  channel: string;
  ok: boolean;
  status: number;
  duration_ms: number;
  response?: string;
  error?: string;
}

export function fetchSettings(): Promise<SettingsPayload> {
  return request<SettingsPayload>('GET', '/api/v1/settings');
}

export function saveEndpoint(name: string, input: EndpointInput): Promise<EndpointView> {
  return request<EndpointView>(
    'PUT',
    `/api/v1/settings/endpoints/${encodeURIComponent(name)}`,
    input
  );
}

export function resetEndpoint(name: string): Promise<{ removed: boolean; endpoint: EndpointView }> {
  return request<{ removed: boolean; endpoint: EndpointView }>(
    'DELETE',
    `/api/v1/settings/endpoints/${encodeURIComponent(name)}`
  );
}

/** 连通性探测。可带表单草稿，支持「没保存先测」。 */
export function verifyEndpoint(name: string, draft: EndpointInput): Promise<ProbeResult> {
  return request<ProbeResult>(
    'POST',
    `/api/v1/settings/endpoints/${encodeURIComponent(name)}/verify`,
    draft
  );
}

/** 通知渠道测试发送。失败也是 200，结果在 ok/error 里。 */
export function testNotify(name: string): Promise<NotifyResult> {
  return request<NotifyResult>('POST', `/api/v1/settings/notify/${encodeURIComponent(name)}/test`);
}

export function savePolicy(policy: Policy): Promise<Policy> {
  return request<Policy>('PUT', '/api/v1/settings/policy', policy);
}

export const SECRET_SOURCE_LABELS: Record<string, string> = {
  env: '环境变量 GUANFU_SECRET_KEY',
  file: '数据目录下的密钥文件（0600）',
  test: '测试用临时密钥'
};
