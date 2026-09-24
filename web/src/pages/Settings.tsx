import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Bell, Package, RefreshCw, Save, Server, User } from 'lucide-react'
import { settingsApi } from '../api'
import { Badge, Callout, Check, Field, Section, Skeleton, Toolbar } from '../components/ui'
import './Settings.css'

interface SettingsData {
  general: Record<string, any>
  listener: Record<string, any>
  implant: Record<string, any>
  /** 载荷构建工具链（mingw gcc / 代码签名）—— 字段名与 server.yaml 的 builder: 段一致 */
  builder: Record<string, any>
  notifications: Record<string, any>
  security: Record<string, any>
  ai: Record<string, any>
  /** 防测绘（控制台前置认证）配置 */
  web: Record<string, any>
  new_password?: string // 仅前端草稿，不随分组提交
}

/** 可分组的配置段（与 /api/v1/settings 的段名一致；general 为只读展示，不在其中） */
type SettingsGroup = 'general' | 'listener' | 'implant' | 'builder' | 'notifications' | 'security' | 'ai' | 'web'

/** 草稿里所有 Record 型分组（不含仅前端使用的 new_password 字符串字段） */
type DraftGroup = 'general' | SettingsGroup

/** 通知平台显示名（后端按 URL 自动识别后回传 platform 字段） */
const PLATFORM_LABEL: Record<string, string> = {
  feishu: '飞书',
  dingtalk: '钉钉',
  wecom: '企业微信',
  slack: 'Slack',
  discord: 'Discord',
  generic: '通用 JSON',
}

/** 分组显示名（保存提示与吸顶条用） */
const GROUP_LABEL: Record<SettingsGroup, string> = {
  general: '通用与服务',
  listener: '监听器与回连',
  implant: '植入端默认参数',
  builder: '载荷构建与签名',
  notifications: '通知 Webhook',
  security: '账户与鉴权',
  ai: 'AI 副驾驶',
  web: '安全与防测绘',
}

/** builder 段的空草稿：后端未回传该段时用它兜底（字段名照抄 server.yaml） */
const BUILDER_DEFAULTS: Record<string, any> = {
  mingw_gcc_path: '',
  sign_enabled: false,
  sign_pfx_path: '',
  sign_pfx_password: '',
  sign_thumbprint: '',
  sign_timestamp_url: '',
  sign_signtool_path: '',
  sign_description: '',
  sign_fail_closed: false,
}

const EMPTY: SettingsData = {
  general: {},
  listener: {},
  implant: {},
  builder: { ...BUILDER_DEFAULTS },
  notifications: {},
  security: {},
  ai: {},
  web: {},
}

/* ───────────────────────────── 分页（左侧导航） ─────────────────────────────
   设置内容按语义拆成 4 个分页，左侧导航由「锚点滚动」改成「分页切换」：
   同一时刻只渲染当前分页的配置段，但 draft / baseline 仍然只有一份（组件级 state），
   所以切页不会丢未保存的改动，底部/顶部保存条始终保存整份配置。 */

type SettingsPageId = 'general' | 'implant' | 'integrations' | 'account'

/** 记住上次停留的分页（URL 里没有 ?page= 时兜底） */
const PAGE_STORAGE_KEY = 'toshell.settings-page'

interface SettingsPageDef {
  id: SettingsPageId
  label: string
  icon: typeof Server
  /** 该分页包含的配置段标题（侧栏用于显示段数，标题行用于内容摘要） */
  sections: string[]
  /** 该分页涉及的配置分组（用于在侧栏上汇总未保存状态） */
  groups: SettingsGroup[]
}

const PAGES: SettingsPageDef[] = [
  {
    id: 'general',
    label: '通用与服务',
    icon: Server,
    sections: ['通用与服务', '监听器与回连', '安全与防测绘（控制台防护）', '日志与审计'],
    groups: ['general', 'listener', 'web'],
  },
  {
    id: 'implant',
    label: '植入端与载荷',
    icon: Package,
    sections: ['植入端默认参数', '载荷构建与签名（builder）'],
    groups: ['implant', 'builder'],
  },
  {
    id: 'integrations',
    label: '集成与通知',
    icon: Bell,
    sections: ['通知 Webhook', 'AI 副驾驶'],
    groups: ['notifications', 'ai'],
  },
  {
    id: 'account',
    label: '账户与鉴权',
    icon: User,
    sections: ['账户与鉴权'],
    groups: ['security'],
  },
]

/** 旧版锚点深链接（/settings#sec-xxx）→ 分页：老链接仍然落到正确的分页 */
const LEGACY_ANCHOR_PAGE: Record<string, SettingsPageId> = {
  'sec-general': 'general',
  'sec-listener': 'general',
  'sec-security': 'general',
  'sec-logs': 'general',
  'sec-implant': 'implant',
  'sec-notify': 'integrations',
  'sec-ai': 'integrations',
  'sec-account': 'account',
}

const isPageId = (v: string | null | undefined): v is SettingsPageId => !!v && PAGES.some((p) => p.id === v)

/** 初始分页：URL ?page= 优先，其次旧锚点 #sec-xxx，其次 localStorage，最后第一页 */
function readInitialPage(fromQuery: string | null): SettingsPageId {
  if (isPageId(fromQuery)) return fromQuery
  const hash = typeof window !== 'undefined' ? window.location.hash.replace(/^#/, '') : ''
  const fromHash = LEGACY_ANCHOR_PAGE[hash]
  if (fromHash) return fromHash
  try {
    const saved = localStorage.getItem(PAGE_STORAGE_KEY)
    if (isPageId(saved)) return saved
  } catch {
    /* localStorage 不可用（隐私模式）时忽略 */
  }
  return PAGES[0].id
}

/**
 * 设置页：真实读写运行时配置（/api/v1/settings），保存后热生效。
 *
 * 布局：左侧分页导航（通用与服务 / 植入端与载荷 / 集成与通知 / 账户与鉴权）
 * + 右侧「吸顶保存条 + 当前分页的语义分组（未选中的分页不渲染）」。
 * 保存是全局的：一次 PUT 只提交有改动的段（后端 SettingsUpdate 支持多段合并提交），
 * 未保存时吸顶条一直显示徽标，离开页面（关闭/刷新）会由 beforeunload 兜底提示；
 * 草稿存在组件级 state 里，切换分页不会重置，也不会漏报其它分页上的改动。
 */
export function Settings() {
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)
  const [rotating, setRotating] = useState(false)
  const [testing, setTesting] = useState(false)
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null)
  const [testResult, setTestResult] = useState<{ ok: boolean; platform?: string; status_code: number; response: string; error?: string } | null>(null)
  /** 后端是否在 settings 接口里放行了 builder 段（GET 回传 + PUT 接收） */
  const [builderSupported, setBuilderSupported] = useState(true)

  // 表单草稿 + 最近一次加载的快照（用于计算"有未保存修改"）
  const [draft, setDraft] = useState<SettingsData>(EMPTY)
  const [baseline, setBaseline] = useState<SettingsData>(EMPTY)

  // ── 分页（左侧导航切换）：URL ?page= 优先，localStorage 兜底 ──
  const [searchParams, setSearchParams] = useSearchParams()
  const [page, setPage] = useState<SettingsPageId>(() => readInitialPage(searchParams.get('page')))
  const contentRef = useRef<HTMLDivElement>(null)

  // 当前分页写回 localStorage：URL 里没有 ?page=（例如直接打开 /settings）时刷新也能停在同页
  useEffect(() => {
    try {
      localStorage.setItem(PAGE_STORAGE_KEY, page)
    } catch {
      /* 忽略：localStorage 不可用 */
    }
  }, [page])

  /** 切换分页：只改 URL / localStorage 与渲染分支，draft / baseline 原样保留 */
  const selectPage = (id: SettingsPageId) => {
    if (id === page) return
    setPage(id)
    try {
      localStorage.setItem(PAGE_STORAGE_KEY, id)
    } catch {
      /* 忽略 */
    }
    const next = new URLSearchParams(searchParams)
    next.set('page', id)
    setSearchParams(next, { replace: true })
    // 切页后回到顶部：滚动容器是 Layout 的 .content，交给浏览器找最近的滚动祖先
    contentRef.current?.scrollIntoView({ block: 'start' })
  }

  const load = async (opts?: { keepBuilder?: Record<string, any> }) => {
    setLoading(true)
    setMsg(null)
    try {
      const res = await settingsApi.get()
      const data: SettingsData = JSON.parse(JSON.stringify(res.data))
      const hasBuilder = !!data.builder && typeof data.builder === 'object'
      setBuilderSupported(hasBuilder)
      if (!hasBuilder) {
        // 后端当前不在 settings 接口里放行 builder 段：不回传也不接收。
        // 保存后重新加载时保留用户刚填的草稿（keepBuilder），避免"填完就被冲掉"。
        data.builder = opts?.keepBuilder ? { ...BUILDER_DEFAULTS, ...opts.keepBuilder } : { ...BUILDER_DEFAULTS }
      }
      setDraft(data)
      const base: SettingsData = JSON.parse(JSON.stringify(data))
      if (!hasBuilder) base.builder = { ...BUILDER_DEFAULTS }
      setBaseline(base)
      setLoaded(true)
    } catch (e) {
      setMsg({ kind: 'err', text: '加载设置失败: ' + (e instanceof Error ? e.message : String(e)) })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
    // 仅首次挂载加载一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const setField = (group: DraftGroup, key: string, value: any) => {
    setDraft((prev) => ({ ...prev, [group]: { ...(prev[group] as Record<string, any>), [key]: value } }))
  }

  /** 有改动的配置段 */
  const dirtyGroups: SettingsGroup[] = (Object.keys(GROUP_LABEL) as SettingsGroup[]).filter(
    (g) => JSON.stringify(draft[g]) !== JSON.stringify(baseline[g]),
  )
  if (draft.new_password && !dirtyGroups.includes('security')) dirtyGroups.push('security')
  const dirty = dirtyGroups.length > 0
  const dirtyLabels = dirtyGroups.map((g) => GROUP_LABEL[g]).join('、')

  // 未保存时关闭/刷新页面给浏览器一个提示（SPA 内部路由切换不会触发，见报告说明）
  const dirtyRef = useRef(false)
  useEffect(() => {
    dirtyRef.current = dirty
  }, [dirty])
  useEffect(() => {
    const handler = (e: BeforeUnloadEvent) => {
      if (!dirtyRef.current) return
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [])

  const confirmDiscard = (): boolean => {
    if (!dirty) return true
    return window.confirm('当前有未保存的修改，继续操作会丢弃这些改动。确定继续？')
  }

  const num = (v: any): number => (typeof v === 'number' ? v : Number(v) || 0)

  /** 保存所有有改动的段：一次 PUT 合并提交（只发有改动的段） */
  const saveAll = async () => {
    if (!dirty) {
      setMsg({ kind: 'ok', text: '没有需要保存的改动' })
      return
    }
    setSaving(true)
    setMsg(null)
    try {
      const payload: Record<string, any> = {}
      for (const g of dirtyGroups) {
        // security / web 需要脱敏字段清洗，放到下面统一处理
        if (g === 'security' || g === 'web') continue
        payload[g] = { ...(draft[g] as Record<string, any>) }
      }
      if (dirtyGroups.includes('security')) {
        // 账户与鉴权：脱敏回显字段绝不回传：api_keys 是掩码展示值（回传会破坏真实密钥）、
        // api_key_count 是只读统计。密钥只能通过「轮换」动作修改。
        const sec = { ...draft.security }
        delete sec.api_keys
        delete sec.api_key_count
        if (draft.new_password) sec.new_password = draft.new_password
        payload.security = sec
      }
      if (dirtyGroups.includes('web')) {
        const web = { ...draft.web }
        delete web.password_set // 只读回显字段，不提交
        delete web.stealth_key_set // 只读回显字段，不提交
        delete web.stealth_entry // 只读回显字段，不提交
        if (draft.web.new_password) web.new_password = draft.web.new_password
        else delete web.new_password // 留空 = 不修改密码
        payload.web = web
      }

      await settingsApi.save(payload)

      const savedLabels = dirtyGroups.map((g) => GROUP_LABEL[g]).join('、')
      const builderWarn = dirtyGroups.includes('builder') && !builderSupported
      setDraft((p) => ({ ...p, new_password: '' }))
      // 重新拉取最新配置；builder 未被后端放行时保留草稿（它仍未持久化）
      await load(builderWarn ? { keepBuilder: { ...draft.builder } } : undefined)
      setMsg({
        kind: 'ok',
        text:
          `✓ 已保存：${savedLabels}` +
          (builderWarn
            ? ' — ⚠ builder 段（构建/签名）当前未被后端 settings 接口接收，改动没有写入配置文件，请在 server.yaml 的 builder: 段手工配置'
            : ''),
      })
    } catch (e: any) {
      const errText = e?.response?.data?.error || (e instanceof Error ? e.message : String(e))
      setMsg({ kind: 'err', text: '保存失败: ' + errText })
    } finally {
      setSaving(false)
    }
  }

  /** 一键轮换 API Key：生成新密钥追加到列表，新密钥仅返回一次 */
  const rotateApiKey = async () => {
    if (!confirmDiscard()) return
    setRotating(true)
    setMsg(null)
    try {
      const res = await settingsApi.save({ security: { rotate_api_key: true } })
      const newKey: string = (res.data as any).new_api_key || ''
      if (newKey) {
        // 展示新密钥（一次性），用户复制保存
        setMsg({ kind: 'ok', text: '✓ 新 API 密钥已生成：' + newKey + '（请立即保存，关闭后不再显示）' })
        // 用浏览器剪贴板辅助（尽力而为）
        try { await navigator.clipboard.writeText(newKey) } catch { /* 忽略 */ }
      } else {
        setMsg({ kind: 'ok', text: '✓ 已轮换' })
      }
      await load()
    } catch (e: any) {
      const errText = e?.response?.data?.error || (e instanceof Error ? e.message : String(e))
      setMsg({ kind: 'err', text: '轮换失败: ' + errText })
    } finally {
      setRotating(false)
    }
  }

  /** 发送测试通知到当前填写的 webhook（不改动已保存配置） */
  const testWebhook = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await settingsApi.testWebhook({
        url: draft.notifications.url || '',
        content: draft.notifications.content || '',
        format: draft.notifications.format || 'auto',
        secret: draft.notifications.secret || '',
      })
      setTestResult(res.data)
    } catch (e: any) {
      const errText = e?.response?.data?.error || (e instanceof Error ? e.message : String(e))
      setTestResult({ ok: false, status_code: 0, response: errText })
    } finally {
      setTesting(false)
    }
  }

  const currentPage = PAGES.find((p) => p.id === page) ?? PAGES[0]

  return (
    <div className="settings-page">
      {/* ── 左侧分页导航：点一项切换一个分页（同一时刻只渲染该分页的配置段） ── */}
      <nav className="settings-sidebar">
        {PAGES.map(({ id, label, icon: Icon, sections, groups }) => (
          <button
            key={id}
            type="button"
            className={`settings-tab ${page === id ? 'active' : ''}`}
            aria-current={page === id ? 'page' : undefined}
            onClick={() => selectPage(id)}
          >
            <Icon size={18} />
            <span className="settings-tab-label">{label}</span>
            {groups.some((g) => dirtyGroups.includes(g)) && <span className="settings-tab-dot" title="有未保存修改" />}
            <span className="settings-tab-count" title={sections.join('、')}>{sections.length}</span>
          </button>
        ))}
      </nav>

      <div className="settings-content" ref={contentRef}>
        {/* ── 吸顶保存条：状态 + 保存 + 重新加载（位置固定，不随内容滚动） ── */}
        <div className="settings-savebar">
          <Toolbar style={{ marginBottom: 0 }}>
            {dirty ? <Badge tone="warn">有未保存修改</Badge> : <Badge tone="ok">配置已同步</Badge>}
            <span className="settings-savebar-hint">
              {loading && !loaded ? '正在加载配置…' : dirty ? `待保存：${dirtyLabels}` : '所有改动已保存'}
            </span>
            <span className="ui-spacer" />
            <button className="save-btn" onClick={saveAll} disabled={saving || loading || !dirty}>
              <Save size={16} /> {saving ? '保存中…' : '保存'}
            </button>
            <button
              className="save-btn ghost"
              onClick={() => { if (confirmDiscard()) load() }}
              disabled={saving || loading}
            >
              <RefreshCw size={16} className={loading ? 'spin' : undefined} /> {loading ? '加载中…' : '重新加载'}
            </button>
          </Toolbar>
          {msg && (
            <div className={`settings-msg ${msg.kind}`}>
              {msg.text}
              <button className="settings-msg-close" onClick={() => setMsg(null)}>×</button>
            </div>
          )}
        </div>

        {loading && !loaded ? (
          <div className="settings-skeleton">
            <Skeleton height={38} />
            <Skeleton height={132} />
            <Skeleton height={132} />
            <Skeleton height={132} />
          </div>
        ) : (
          <>
            {/* ── 分页标题：当前分页包含的配置段一览 ── */}
            <div className="settings-page-head">
              <h2 className="settings-page-title">{currentPage.label}</h2>
              <span className="settings-page-meta">{currentPage.sections.join(' · ')}</span>
            </div>

            {/* ══ 通用与服务（改端口 / 主机后需重启服务端生效） ══ */}
            {page === 'general' && (
              <Section
                title="通用与服务"
                desc="控制台/REST API 的监听地址、心跳超时与写队列参数。"
                badge={<Badge tone="warn">需重启</Badge>}
              >
                <Callout tone="info" title="改动生效方式">
                  <code>api_host</code> / <code>api_port</code> / <code>write_queue_size</code> 需要
                  <b>重启服务端</b>才会生效（保存只是落盘到 server.yaml）；<code>log_level</code> /
                  <code>log_format</code> / <code>heartbeat_timeout</code> 保存后热生效。
                </Callout>
                <div className="settings-grid" style={{ marginTop: 'var(--sp-3)' }}>
                  <Field label="API 监听地址" hint="控制台与 REST API 绑定地址（改后需重启）">
                    <input
                      className="ui-input"
                      value={String(draft.general.api_host ?? '')}
                      onChange={(e) => setField('general', 'api_host', e.target.value)}
                    />
                  </Field>
                  <Field label="API 端口" hint="控制台与 REST API 端口（改后需重启）">
                    <input
                      className="ui-input"
                      type="number"
                      value={String(draft.general.api_port ?? '')}
                      onChange={(e) => setField('general', 'api_port', num(e.target.value))}
                    />
                  </Field>
                  <Field label="心跳超时" hint="超过该时长未收到心跳判定会话离线，如 90s / 2m（热生效）">
                    <input
                      className="ui-input"
                      value={String(draft.general.heartbeat_timeout ?? '')}
                      onChange={(e) => setField('general', 'heartbeat_timeout', e.target.value)}
                    />
                  </Field>
                  <Field label="写队列长度" hint="监听器发送队列上限，满队列会丢包（改后需重启）">
                    <input
                      className="ui-input"
                      type="number"
                      value={String(draft.general.write_queue_size ?? '')}
                      onChange={(e) => setField('general', 'write_queue_size', num(e.target.value))}
                    />
                  </Field>
                </div>
              </Section>
            )}

            {/* ══ 监听器与回连 ══ */}
            {page === 'general' && (
              <Section title="监听器与回连" desc="监听器开关、绑定地址与流量拟态（保存后热生效；改端口需重启监听器）。">
                <div className="settings-grid">
                  <Check
                    label="启用监听器"
                    hint="默认 C2 监听器开关"
                    checked={!!draft.listener.enabled}
                    onChange={(v) => setField('listener', 'enabled', v)}
                  />
                  <Field label="监听主机" hint="绑定地址，0.0.0.0 = 全部网卡">
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.listener.host || ''}
                      onChange={(e) => setField('listener', 'host', e.target.value)}
                      placeholder="0.0.0.0"
                    />
                  </Field>
                  <Field label="监听端口" hint="改动需重启监听器">
                    <input
                      type="number"
                      className="ui-input"
                      value={num(draft.listener.port)}
                      onChange={(e) => setField('listener', 'port', Number(e.target.value))}
                    />
                  </Field>
                  <Field
                    label="公网地址"
                    required
                    hint="服务器公网 IP / 域名：生成的载荷会连接它，务必填目标机可达的地址"
                  >
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.listener.public_host || ''}
                      onChange={(e) => setField('listener', 'public_host', e.target.value)}
                      placeholder="cdn.example.com"
                    />
                  </Field>
                  <Field label="流量拟态模板" hint="HTTP 监听器响应整形（热生效）">
                    <select
                      className="ui-input"
                      value={draft.listener.mimicry_profile || 'cdn'}
                      onChange={(e) => setField('listener', 'mimicry_profile', e.target.value)}
                    >
                      <option value="cdn">cdn（静态资源/CDN）</option>
                      <option value="api">api（REST API）</option>
                      <option value="stream">stream（视频流/m3u8）</option>
                    </select>
                  </Field>
                  <Field label="域前置拟态域名" hint="HTTPS 轮询通道的 TLS SNI + HTTP Host（热生效，示例: cdn.example.com）">
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.listener.front_domain || ''}
                      onChange={(e) => setField('listener', 'front_domain', e.target.value)}
                      placeholder="留空 = 不使用域前置"
                    />
                  </Field>
                  <Field
                    label="监听器伪装网站"
                    hint="HTTP 监听器对探测请求反向代理到该网站（热生效，示例: https://www.example.com）"
                    style={{ gridColumn: '1 / -1' }}
                  >
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.listener.mimicry_site || ''}
                      onChange={(e) => setField('listener', 'mimicry_site', e.target.value)}
                      placeholder="留空 = 使用静态拟态模板"
                    />
                  </Field>
                </div>
                <Callout tone="info" title="植入端加密密钥（encryption_key）不在本页">
                  该密钥由 settings 接口的 listener 段脱敏管理：既不下发也不接收，只能在 server.yaml 的 listener.encryption_key 中配置/更换。
                  更换后所有旧植入端会失联，需要重新生成载荷。
                </Callout>
              </Section>
            )}

            {/* ══ 植入端与载荷构建 ══ */}
            {page === 'implant' && (
              <Section title="植入端默认参数" desc="影响后续构建的默认值（生成载荷时可在构建页覆盖）。">
                <div className="settings-grid">
                  <Field label="心跳间隔(秒)" hint="默认心跳/轮询间隔">
                    <input
                      type="number"
                      className="ui-input"
                      value={num(draft.implant.interval)}
                      onChange={(e) => setField('implant', 'interval', Number(e.target.value))}
                    />
                  </Field>
                  <Field label="抖动(%)" hint="心跳间隔随机抖动 0-100">
                    <input
                      type="number"
                      min={0}
                      max={100}
                      className="ui-input"
                      value={num(draft.implant.jitter)}
                      onChange={(e) => setField('implant', 'jitter', Number(e.target.value))}
                    />
                  </Field>
                  <Field label="重连退避(秒)" hint="断连后的重连基础等待">
                    <input
                      type="number"
                      className="ui-input"
                      value={num(draft.implant.retry_wait)}
                      onChange={(e) => setField('implant', 'retry_wait', Number(e.target.value))}
                    />
                  </Field>
                  <Field label="自杀日期" hint="KillDate (YYYY-MM-DD，留空不启用)">
                    <input
                      className="ui-input"
                      value={draft.implant.kill_date || ''}
                      onChange={(e) => setField('implant', 'kill_date', e.target.value)}
                      placeholder="如 2026-12-31"
                    />
                  </Field>
                  <Field
                    label="启动随机延迟(秒)"
                    hint="植入端启动后随机休眠 [min, max] 秒再连接，打乱「启动即行为」检测（0 关闭）"
                  >
                    <div className="settings-inline-pair">
                      <input
                        type="number"
                        min={0}
                        max={600}
                        className="ui-input"
                        value={draft.implant.startup_delay_min !== undefined ? num(draft.implant.startup_delay_min) : ''}
                        onChange={(e) => setField('implant', 'startup_delay_min', Number(e.target.value))}
                        placeholder="min"
                      />
                      <span className="settings-tilde">~</span>
                      <input
                        type="number"
                        min={0}
                        max={600}
                        className="ui-input"
                        value={draft.implant.startup_delay_max !== undefined ? num(draft.implant.startup_delay_max) : ''}
                        onChange={(e) => setField('implant', 'startup_delay_max', Number(e.target.value))}
                        placeholder="max"
                      />
                    </div>
                  </Field>
                </div>
              </Section>
            )}

            {/* ══ 载荷构建与签名 ══ */}
            {page === 'implant' && (
              <Section
                title="载荷构建与签名（builder）"
                desc="C 植入端编译工具链与 Authenticode 代码签名；字段与 server.yaml 的 builder: 段一一对应。"
                badge={<Badge tone="accent">签名</Badge>}
              >
                <Callout tone="warn" title="未签名的新 PE 可能被直接拒绝执行">
                  未签名的新 PE 在装有 360/电脑管家的主机上会被拒绝执行；自签名证书需在目标机导入受信任的根证书颁发机构。
                  签名不是免杀银弹（杀软仍看信誉与行为），但它是「能不能跑起来」这一层的敲门砖。
                </Callout>
                {!builderSupported && (
                  <Callout tone="danger" title="当前后端的 settings 接口未放行 builder 段" style={{ marginTop: 'var(--sp-3)' }}>
                    该段可以在这里填写，但保存时后端 <code>SettingsUpdate</code> 不含 builder 字段，<b>改动不会写入配置文件</b>
                    （保存后徽标仍会显示未保存）。需要用后端放行 builder 段，或直接编辑 server.yaml 的 <code>builder:</code> 段。
                  </Callout>
                )}
                <div className="settings-grid" style={{ marginTop: 'var(--sp-3)' }}>
                  <Check
                    label="启用代码签名 (sign_enabled)"
                    hint="开启后构建出的 Windows PE 会做 Authenticode 签名"
                    checked={!!draft.builder.sign_enabled}
                    onChange={(v) => setField('builder', 'sign_enabled', v)}
                  />
                  <Field
                    label="mingw-w64 gcc 路径 (mingw_gcc_path)"
                    hint="C 植入端编译用，可填 gcc 可执行文件、所在目录或 PATH 中的命令名；留空自动探测"
                  >
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.builder.mingw_gcc_path || ''}
                      onChange={(e) => setField('builder', 'mingw_gcc_path', e.target.value)}
                      placeholder="如 C:\\msys64\\mingw64\\bin\\gcc.exe（留空自动探测）"
                    />
                  </Field>
                  <Field
                    label="证书文件 pfx 路径 (sign_pfx_path)"
                    hint="与「证书指纹」二选一，pfx 优先；证书密码通过环境变量传给子进程，不出现在命令行"
                  >
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.builder.sign_pfx_path || ''}
                      onChange={(e) => setField('builder', 'sign_pfx_path', e.target.value)}
                      placeholder="如 D:\\keys\\codesign.pfx"
                    />
                  </Field>
                  <Field label="pfx 密码 (sign_pfx_password)" hint="不会回显（后端该字段不参与 JSON 序列化）">
                    <input
                      type="password"
                      className="ui-input"
                      value={draft.builder.sign_pfx_password || ''}
                      onChange={(e) => setField('builder', 'sign_pfx_password', e.target.value)}
                      placeholder="留空 = 不修改"
                    />
                  </Field>
                  <Field
                    label="证书指纹 (sign_thumbprint)"
                    hint="使用本机证书存储 CurrentUser\\My 中该指纹的证书签名（EV/硬件令牌场景）"
                  >
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.builder.sign_thumbprint || ''}
                      onChange={(e) => setField('builder', 'sign_thumbprint', e.target.value)}
                      placeholder="40 位十六进制指纹"
                    />
                  </Field>
                  <Field
                    label="签名时间戳服务器 (sign_timestamp_url)"
                    hint="可选，留空则不打时间戳；内网/离线环境通常不可达，失败不影响签名本身"
                  >
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.builder.sign_timestamp_url || ''}
                      onChange={(e) => setField('builder', 'sign_timestamp_url', e.target.value)}
                      placeholder="http://timestamp.digicert.com"
                    />
                  </Field>
                  <Field label="signtool.exe 路径 (sign_signtool_path)" hint="可选，留空自动探测 PATH 与 Windows SDK 目录（找不到会回退 PowerShell 签名）">
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.builder.sign_signtool_path || ''}
                      onChange={(e) => setField('builder', 'sign_signtool_path', e.target.value)}
                      placeholder="留空 = 自动探测"
                    />
                  </Field>
                  <Field label="签名描述 (sign_description)" hint="写入签名的描述，可选；留空用默认">
                    <input
                      className="ui-input"
                      value={draft.builder.sign_description || ''}
                      onChange={(e) => setField('builder', 'sign_description', e.target.value)}
                      placeholder="留空 = 默认描述"
                    />
                  </Field>
                  <Check
                    label="签名失败即拒绝出包 (sign_fail_closed)"
                    hint="true：签名失败就丢弃该载荷（构建报错）；false：只告警并返回未签名产物"
                    checked={!!draft.builder.sign_fail_closed}
                    onChange={(v) => setField('builder', 'sign_fail_closed', v)}
                  />
                </div>
              </Section>
            )}

            {/* ══ 通知 Webhook ══ */}
            {page === 'integrations' && (
              <Section title="通知 Webhook" desc="会话上线推送钉钉/企业微信/飞书/Slack/Discord（保存后热生效）。">
                <div className="settings-grid">
                  <Check
                    label="新会话通知"
                    hint="会话上线时发送 webhook 通知（热生效）"
                    checked={!!draft.notifications.enabled}
                    onChange={(v) => setField('notifications', 'enabled', v)}
                  />
                  <Check
                    label="仅上线通知"
                    hint="只在会话上线时发送"
                    checked={!!draft.notifications.only_online}
                    onChange={(v) => setField('notifications', 'only_online', v)}
                  />
                  <Field label="Webhook URL" hint="企业微信/钉钉/飞书/Slack 机器人地址" style={{ gridColumn: '1 / -1' }}>
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.notifications.url || ''}
                      onChange={(e) => setField('notifications', 'url', e.target.value)}
                      placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
                    />
                  </Field>
                  <Field
                    label="消息格式"
                    hint="auto 会按 URL 自动识别飞书/钉钉/企业微信/Slack/Discord 并发送对应消息结构；加签 Secret 请在 server.yaml 的 webhook.secret 配置（钉钉走 URL 签名，飞书走 body 内 sign）"
                  >
                    <select
                      className="ui-input"
                      value={draft.notifications.format || 'auto'}
                      onChange={(e) => setField('notifications', 'format', e.target.value)}
                    >
                      <option value="auto">auto（按 URL 自动识别）</option>
                      <option value="dingtalk">dingtalk（钉钉 markdown）</option>
                      <option value="feishu">feishu（飞书 / Lark 文本卡片）</option>
                      <option value="wecom">wecom（企业微信文本）</option>
                      <option value="slack">slack（Slack Incoming Webhook）</option>
                      <option value="discord">discord（Discord Webhook）</option>
                      <option value="generic">generic（通用 JSON，自建接收端）</option>
                    </select>
                  </Field>
                  <Field
                    label="内容模板"
                    hint={'支持 {session_id} {hostname} {username} {os} {arch} {remote_addr} {time}'}
                    style={{ gridColumn: '1 / -1' }}
                  >
                    <textarea
                      value={draft.notifications.content || ''}
                      onChange={(e) => setField('notifications', 'content', e.target.value)}
                      className="ui-input"
                      rows={3}
                      style={{ resize: 'vertical' }}
                      placeholder="新会话上线: {hostname} ({username}@{os}/{arch}) 来自 {remote_addr}"
                    />
                  </Field>
                </div>
                <Toolbar style={{ marginTop: 'var(--sp-3)', marginBottom: 0 }}>
                  <button className="save-btn ghost" onClick={testWebhook} disabled={testing || !draft.notifications.url}>
                    <Bell size={16} /> {testing ? '发送中…' : '发送测试通知'}
                  </button>
                  {!dirty && <span className="settings-muted">改动会随顶部「保存」一起提交</span>}
                </Toolbar>
                {testResult && (
                  <div className={`settings-msg ${testResult.ok ? 'ok' : 'err'}`}>
                    测试结果: {PLATFORM_LABEL[testResult.platform || ''] || testResult.platform || '未知平台'} — HTTP {testResult.status_code}
                    {testResult.ok ? ' ✅ 发送成功' : ` ❌ ${testResult.error || '发送失败'}`}
                    {testResult.response ? ` — ${testResult.response}` : ''}
                    <button className="settings-msg-close" onClick={() => setTestResult(null)}>×</button>
                  </div>
                )}
              </Section>
            )}

            {/* ══ AI 副驾驶 ══ */}
            {page === 'integrations' && (
              <Section title="AI 副驾驶" desc="LLM（OpenAI 兼容 chat/completions）配置，保存后热生效。">
                <div className="settings-grid">
                  <Check
                    label="启用"
                    hint="启用聊天面板（侧边栏「AI 副驾驶」页）"
                    checked={!!draft.ai.enabled}
                    onChange={(v) => setField('ai', 'enabled', v)}
                  />
                  <Field label="模型" hint="模型名，如 deepseek-chat / gpt-4o-mini">
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.ai.model || 'deepseek-chat'}
                      onChange={(e) => setField('ai', 'model', e.target.value)}
                    />
                  </Field>
                  <Field label="API 端点" hint="OpenAI 兼容 chat/completions 端点，如 https://api.deepseek.com/v1">
                    <input
                      className="ui-input ui-input--mono"
                      value={draft.ai.base_url || ''}
                      onChange={(e) => setField('ai', 'base_url', e.target.value)}
                      placeholder="https://api.deepseek.com/v1"
                    />
                  </Field>
                  <Field label="API Key" hint="LLM 服务商密钥（掩码展示，留空不修改）">
                    <input
                      type="password"
                      className="ui-input"
                      value={draft.ai.api_key || ''}
                      onChange={(e) => setField('ai', 'api_key', e.target.value)}
                      placeholder="sk-..."
                    />
                  </Field>
                  <Field label="超时(秒)" hint="单次 LLM 请求超时">
                    <input
                      type="number"
                      min={10}
                      max={300}
                      className="ui-input"
                      value={num(draft.ai.timeout) || 60}
                      onChange={(e) => setField('ai', 'timeout', Number(e.target.value))}
                    />
                  </Field>
                  <Field label="最大工具轮数" hint="模型连续调用工具的上限（防死循环）">
                    <input
                      type="number"
                      min={1}
                      max={30}
                      className="ui-input"
                      value={num(draft.ai.max_turns) || 8}
                      onChange={(e) => setField('ai', 'max_turns', Number(e.target.value))}
                    />
                  </Field>
                  <Field
                    label="权限模式"
                    hint="正常模式：影响会话的操作（命令下发/文件/进程/凭据/截屏/隧道/插件等）执行前需你确认，任务流除外；全自动：直接执行"
                    style={{ gridColumn: '1 / -1' }}
                  >
                    <select
                      className="ui-input"
                      value={draft.ai.consent_mode || 'auto'}
                      onChange={(e) => setField('ai', 'consent_mode', e.target.value)}
                    >
                      <option value="auto">全自动（默认）</option>
                      <option value="normal">正常模式 · 需确认</option>
                    </select>
                  </Field>
                </div>
              </Section>
            )}

            {/* ══ 账户与鉴权 ══ */}
            {page === 'account' && (
              <Section title="账户与鉴权" desc="控制台登录账户、认证方式与 API 密钥。">
                <div className="settings-grid">
                  <Field label="用户名" hint="登录用户名（热生效）">
                    <input
                      className="ui-input"
                      value={draft.security.admin_username || ''}
                      onChange={(e) => setField('security', 'admin_username', e.target.value)}
                    />
                  </Field>
                  <Field label="新密码" hint="修改登录密码（≥8 位，保存后立即生效；留空 = 不修改）">
                    <input
                      type="password"
                      className="ui-input"
                      value={draft.new_password || ''}
                      onChange={(e) => setDraft((p) => ({ ...p, new_password: e.target.value }))}
                      placeholder="输入新密码"
                    />
                  </Field>
                </div>
                <div className="settings-grid settings-grid--checks" style={{ marginTop: 'var(--sp-3)' }}>
                  <Check
                    label="启用认证"
                    hint="Web 控制台登录认证（热生效）"
                    checked={!!draft.security.auth_enabled}
                    onChange={(v) => setField('security', 'auth_enabled', v)}
                  />
                  <Check
                    label="JWT 认证"
                    hint="登录令牌机制"
                    checked={!!draft.security.jwt_enabled}
                    onChange={(v) => setField('security', 'jwt_enabled', v)}
                  />
                  <Check
                    label="API Key 认证"
                    hint="接口密钥机制"
                    checked={!!draft.security.api_key_enabled}
                    onChange={(v) => setField('security', 'api_key_enabled', v)}
                  />
                </div>
                <Callout tone="warn" title="认证开关当前不被 settings 接口接收">
                  auth_enabled / jwt_enabled / api_key_enabled 三个开关只做状态展示：保存接口（<code>SettingsUpdate</code>）没有这三个字段，
                  改动不会生效。要调整请在 server.yaml 的 <code>auth:</code> 段修改后重启服务。
                </Callout>
                <Callout tone="info" title="API 密钥只显示脱敏值" style={{ marginTop: 'var(--sp-3)' }}>
                  密钥用于 <code>X-API-Key</code> 认证。这里只展示首尾各 4 位的脱敏值，<b>绝不回传</b>；
                  轮换会追加一个新密钥（旧密钥仍有效），新密钥仅显示一次，请立即保存。
                </Callout>
                <Field
                  label="API 密钥"
                  hint="留空即表示不修改密钥；新增/删除密钥只能通过「轮换新密钥」动作完成"
                  style={{ marginTop: 'var(--sp-2)' }}
                >
                  <div className="setting-inputs-col">
                    {(draft.security.api_keys as string[] | undefined)?.map((k: string, i: number) => (
                      <code key={i} className="api-key-chip">{k}</code>
                    ))}
                    {!((draft.security.api_keys as string[] | undefined)?.length) && (
                      <span className="settings-muted">暂无 API 密钥</span>
                    )}
                  </div>
                </Field>
                <Toolbar style={{ marginTop: 'var(--sp-2)', marginBottom: 0 }}>
                  <button
                    className="save-btn ghost"
                    onClick={rotateApiKey}
                    disabled={rotating || saving || !draft.security.api_key_enabled}
                  >
                    <RefreshCw size={14} className={rotating ? 'spin' : undefined} /> {rotating ? '轮换中…' : '轮换新密钥'}
                  </button>
                  {!draft.security.api_key_enabled && (
                    <span className="settings-muted">API Key 认证未启用，轮换按钮不可用</span>
                  )}
                </Toolbar>
              </Section>
            )}

            {/* ══ 安全与防测绘 ══ */}
            {page === 'general' && (
              <Section title="安全与防测绘（控制台防护）" desc="前置认证与来源白名单：只作用于控制台与管理 API，植入端回连不受影响。">
                <Callout tone="info" title="不影响植入端">
                  开启后访问控制台需先通过浏览器认证框，阻止 Fofa/Quake/Hunter 等资产测绘引擎抓取并收录本资产；
                  已持有 API Key 的脚本与 AI 调用不受影响；<b>植入端回连不受影响</b>。
                </Callout>
                <div className="settings-grid" style={{ marginTop: 'var(--sp-3)' }}>
                  <Check
                    label="基础认证（Basic Auth）"
                    hint="开启前请先填好用户名与密码（密码至少 8 位），否则会保存失败（防自锁）"
                    checked={!!draft.web?.basic_auth_enabled}
                    onChange={(v) => setField('web', 'basic_auth_enabled', v)}
                  />
                  <Field
                    label="认证用户名"
                    required={!!draft.web?.basic_auth_enabled}
                    hint="浏览器弹框中的用户名"
                  >
                    <input
                      className="ui-input"
                      value={draft.web?.basic_auth_user || ''}
                      onChange={(e) => setField('web', 'basic_auth_user', e.target.value)}
                      placeholder="toshell"
                    />
                  </Field>
                  <Field
                    label="认证密码"
                    required={!!draft.web?.basic_auth_enabled && !draft.web?.password_set}
                    hint={
                      draft.web?.password_set
                        ? '已设置（留空表示不修改；bcrypt 存储，不回显）'
                        : '至少 8 位；保存后以 bcrypt 哈希写入配置'
                    }
                  >
                    <input
                      type="password"
                      className="ui-input"
                      value={draft.web?.new_password || ''}
                      onChange={(e) => setField('web', 'new_password', e.target.value)}
                      placeholder={draft.web?.password_set ? '留空 = 不修改' : '设置密码'}
                    />
                  </Field>
                  <Field
                    label="未认证响应"
                    hint="basic = 返回 401 认证框（推荐：浏览器会弹框，前端照常可用）；disguise = 返回 404（对测绘更隐蔽，但浏览器不弹框，需用 https://用户名:密码@主机/ 才能进入前端）"
                  >
                    <select
                      className="ui-input"
                      value={draft.web?.unauth_mode || 'basic'}
                      onChange={(e) => setField('web', 'unauth_mode', e.target.value)}
                    >
                      <option value="basic">basic（401 认证框，推荐）</option>
                      <option value="disguise">disguise（404 伪装，隐蔽优先）</option>
                    </select>
                  </Field>
                  <Field
                    label="来源白名单（CIDR）"
                    hint="可选：逗号分隔，如 203.0.113.0/24,10.0.0.0/8。非空时仅这些网段可访问控制台（建议同时开启 basic auth 以免自锁）"
                    style={{ gridColumn: '1 / -1' }}
                  >
                    <input
                      className="ui-input ui-input--mono"
                      value={(draft.web?.allow_cidrs || []).join(', ')}
                      onChange={(e) =>
                        setField(
                          'web',
                          'allow_cidrs',
                          e.target.value.split(',').map((s) => s.trim()).filter(Boolean),
                        )
                      }
                      placeholder="留空 = 不限制"
                    />
                  </Field>
                </div>
                {draft.web?.stealth_key_set && (
                  <Callout tone="default" title="隐蔽入口已设置（disguise 模式）" style={{ marginTop: 'var(--sp-2)' }}>
                    入口路径：<code>{draft.web?.stealth_entry || '/__gate?k=<密钥>'}</code>（密钥不回传，丢失需重新生成）。
                  </Callout>
                )}
              </Section>
            )}

            {/* ══ 日志与审计 ══ */}
            {page === 'general' && (
              <Section
                title="日志与审计"
                desc="服务端日志级别与格式（保存后热生效）；登录审计请到「登录日志」页查看。"
                badge={<Badge>热生效</Badge>}
              >
                <div className="settings-grid">
                  <Field label="日志级别" hint="debug / info / warn / error（保存后立即生效）">
                    <input
                      className="ui-input"
                      value={String(draft.general.log_level ?? '')}
                      onChange={(e) => setField('general', 'log_level', e.target.value)}
                      placeholder="info"
                    />
                  </Field>
                  <Field label="日志格式" hint="text / json（保存后立即生效）">
                    <input
                      className="ui-input"
                      value={String(draft.general.log_format ?? '')}
                      onChange={(e) => setField('general', 'log_format', e.target.value)}
                      placeholder="text"
                    />
                  </Field>
                </div>
              </Section>
            )}
          </>
        )}
      </div>
    </div>
  )
}
