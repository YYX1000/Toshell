/**
 * UI 基础件（v1.3.5）—— 各页面共用的最小积木。
 *
 * 为什么加这一层：之前每个页面都自己写 `<div style={{...}}>` + `var(--text-dim, #9a9aab)`
 * 这类内联样式与"没有定义只有兜底"的变量，导致深浅主题不一致、间距字号各写各的、
 * 空态/加载态要么没有要么各写一遍。这里把最常用的几类收敛成组件，样式统一走
 * `index.css` 里的 `ui-*` 类与 token。
 *
 * 约定：
 *   - 只做"展示 + 少量状态"，不引入任何第三方依赖，不做数据请求；
 *   - 颜色/间距一律用 token（`--color-*` / `--sp-*`），不再写死十六进制；
 *   - 需要页面自定义宽度/布局时，通过 `style` 传入即可。
 */
import type { CSSProperties, ReactNode } from 'react'
import { useState } from 'react'
import { AlertTriangle, CheckCircle2, ChevronDown, Info, XCircle } from 'lucide-react'

/* ─────────────────────────────── 卡片 / 分组 ─────────────────────────────── */

export function Card({
  title,
  subtitle,
  icon,
  actions,
  children,
  style,
  bodyStyle,
  className,
}: {
  title?: ReactNode
  subtitle?: ReactNode
  icon?: ReactNode
  actions?: ReactNode
  children?: ReactNode
  style?: CSSProperties
  bodyStyle?: CSSProperties
  className?: string
}) {
  return (
    <div className={`ui-card ${className || ''}`} style={style}>
      {(title || actions) && (
        <div className="ui-card-title">
          {icon}
          <span>{title}</span>
          {subtitle && <span className="ui-card-sub">{subtitle}</span>}
          {actions && <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: 8 }}>{actions}</span>}
        </div>
      )}
      <div style={bodyStyle}>{children}</div>
    </div>
  )
}

/** 可折叠分组：用于把塞满一屏的表单/参数按语义分组，默认展开状态可控。 */
export function Section({
  title,
  desc,
  badge,
  defaultOpen = true,
  children,
  style,
}: {
  title: ReactNode
  desc?: ReactNode
  badge?: ReactNode
  defaultOpen?: boolean
  children: ReactNode
  style?: CSSProperties
}) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className="ui-section" style={style}>
      <button type="button" className="ui-section-head" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
        <ChevronDown size={15} className="ui-section-caret" />
        <span>{title}</span>
        {badge && <span className="ui-section-badge">{badge}</span>}
      </button>
      {open && (
        <div className="ui-section-body">
          {desc && <div className="ui-section-desc">{desc}</div>}
          {children}
        </div>
      )}
    </div>
  )
}

/* ─────────────────────────────── 徽标 / 提示 ─────────────────────────────── */

export type BadgeTone = 'default' | 'ok' | 'warn' | 'danger' | 'info' | 'accent'

export function Badge({ tone = 'default', children, style }: { tone?: BadgeTone; children: ReactNode; style?: CSSProperties }) {
  const cls = tone === 'default' ? 'ui-badge' : `ui-badge ui-badge--${tone}`
  return (
    <span className={cls} style={style}>
      {children}
    </span>
  )
}

/** 风险等级：加载器链/高危操作统一用它标注（低=ok、中=warn、高=danger）。 */
export function RiskBadge({ level, note }: { level: '低' | '中' | '高'; note?: string }) {
  const tone: BadgeTone = level === '低' ? 'ok' : level === '中' ? 'warn' : 'danger'
  return (
    <Badge tone={tone} style={note ? { cursor: 'help' } : undefined}>
      <span className="ui-dot" />
      风险{level}
      {note ? <span title={note}>ⓘ</span> : null}
    </Badge>
  )
}

export function Callout({
  tone = 'info',
  title,
  children,
  style,
}: {
  tone?: 'info' | 'warn' | 'danger' | 'ok' | 'default'
  title?: ReactNode
  children?: ReactNode
  style?: CSSProperties
}) {
  const cls = tone === 'default' ? 'ui-callout' : `ui-callout ui-callout--${tone}`
  const Icon = tone === 'warn' ? AlertTriangle : tone === 'danger' ? XCircle : tone === 'ok' ? CheckCircle2 : Info
  return (
    <div className={cls} style={style}>
      <Icon size={15} style={{ flexShrink: 0, marginTop: 2 }} />
      <div style={{ minWidth: 0 }}>
        {title && <div className="ui-callout-title">{title}</div>}
        {children}
      </div>
    </div>
  )
}

/* ─────────────────────────────── 表单字段 ─────────────────────────────── */

export function Field({
  label,
  hint,
  error,
  required,
  children,
  style,
}: {
  label?: ReactNode
  hint?: ReactNode
  error?: ReactNode
  required?: boolean
  children: ReactNode
  style?: CSSProperties
}) {
  return (
    <div className="ui-field" style={style}>
      {label && (
        <label className="ui-field-label">
          {label}
          {required && <span className="ui-req">*</span>}
        </label>
      )}
      {children}
      {error ? <div className="ui-field-error">{error}</div> : hint ? <div className="ui-field-hint">{hint}</div> : null}
    </div>
  )
}

export function Check({
  label,
  hint,
  checked,
  onChange,
  disabled,
  style,
}: {
  label: ReactNode
  hint?: ReactNode
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
  style?: CSSProperties
}) {
  return (
    <div style={style}>
      <label className={`ui-check ${disabled ? 'ui-check--disabled' : ''}`}>
        <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
        <span>{label}</span>
      </label>
      {hint && <div className="ui-field-hint" style={{ marginTop: 2 }}>{hint}</div>}
    </div>
  )
}

/* ─────────────────────────────── 状态展示 ─────────────────────────────── */

export function Empty({ title, desc, action, icon }: { title?: ReactNode; desc?: ReactNode; action?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="ui-empty">
      {icon}
      {title && <div className="ui-empty-title">{title}</div>}
      {desc && <div>{desc}</div>}
      {action}
    </div>
  )
}

export function Skeleton({ width = '100%', height = 14, style }: { width?: number | string; height?: number | string; style?: CSSProperties }) {
  return <div className="ui-skeleton" style={{ width, height, ...style }} />
}

export function SkeletonLines({ lines = 3, style }: { lines?: number; style?: CSSProperties }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, ...style }}>
      {Array.from({ length: lines }).map((_, i) => (
        <Skeleton key={i} height={12} width={i === lines - 1 ? '62%' : '100%'} />
      ))}
    </div>
  )
}

export function Stat({ label, value, tone, style }: { label: ReactNode; value: ReactNode; tone?: 'ok' | 'warn' | 'danger'; style?: CSSProperties }) {
  return (
    <div className="ui-stat" style={style}>
      <span className="ui-stat-label">{label}</span>
      <span className={`ui-stat-value ${tone ? `ui-stat-value--${tone}` : ''}`}>{value}</span>
    </div>
  )
}

export function KeyValue({ items, style }: { items: Array<{ k: ReactNode; v: ReactNode }>; style?: CSSProperties }) {
  return (
    <dl className="ui-kv" style={style}>
      {items.map((it, i) => (
        <div key={i} style={{ display: 'contents' }}>
          <dt>{it.k}</dt>
          <dd>{it.v}</dd>
        </div>
      ))}
    </dl>
  )
}

export function Toolbar({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return (
    <div className="ui-toolbar" style={style}>
      {children}
    </div>
  )
}

export function Code({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return (
    <code className="ui-code" style={style}>
      {children}
    </code>
  )
}
