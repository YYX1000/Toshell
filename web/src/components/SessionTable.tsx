import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { Trash2, Monitor, Network, WifiOff } from 'lucide-react'
import { sessionApi } from '../api'
import { Badge, Empty } from './ui'
import type { BadgeTone } from './ui'
import type { Session } from '../types'

interface SessionTableProps {
  sessions: Session[]
  selectedSession: Session | null
  onSelectSession: (session: Session) => void
  onSessionsChange: (sessions: Session[]) => void
  /** 嵌入模式：不渲染外层 sessions-page 和 page-header */
  embedded?: boolean
  /** 外部搜索过滤词（由父组件管理搜索） */
  searchFilter?: string
  /** 外部状态过滤（'all' 或后端状态值，客户端过滤） */
  statusFilter?: string
  /** 清除搜索/筛选（空态里的「清除筛选」按钮用） */
  onClearFilters?: () => void
}

/** 会话状态 → 统一徽标（会话页表格与会话详情共用同一套文案/配色） */
const STATUS_BADGE: Record<string, { label: string; tone: BadgeTone }> = {
  active: { label: '在线', tone: 'ok' },
  dead: { label: '离线', tone: 'danger' },
  sleep: { label: '休眠', tone: 'warn' },
}

export function sessionStatusBadge(status: string): { label: string; tone: BadgeTone } {
  return STATUS_BADGE[status] || { label: status || '未知', tone: 'default' }
}

/**
 * 心跳相对时间：自己算一个轻量工具函数（不引依赖），每秒随父组件 tick 重渲染。
 * 新鲜 → ok；≥1 分钟 → 默认；≥5 分钟 → warn；≥1 小时 → danger。
 */
export function heartbeatBadge(lastSeen: string): { text: string; tone: BadgeTone } {
  if (!lastSeen) return { text: '—', tone: 'default' }
  const t = new Date(lastSeen).getTime()
  if (isNaN(t)) return { text: '—', tone: 'default' }
  const seconds = Math.floor((Date.now() - t) / 1000)
  if (seconds < 0) return { text: '刚刚', tone: 'ok' }
  if (seconds < 60) return { text: `${seconds}s 前`, tone: 'ok' }
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return { text: `${minutes}m 前`, tone: minutes < 5 ? 'default' : 'warn' }
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return { text: `${hours}h 前`, tone: 'warn' }
  return { text: `${Math.floor(hours / 24)}d 前`, tone: 'danger' }
}

export function SessionTable({
  sessions,
  selectedSession,
  onSelectSession,
  onSessionsChange,
  embedded: _embedded = false,
  searchFilter = '',
  statusFilter = 'all',
  onClearFilters,
}: SessionTableProps) {
  const navigate = useNavigate()

  const query = (searchFilter || '').trim().toLowerCase()
  // 客户端过滤：主机名 / 用户名 / IP / 进程名 / 会话 ID；状态筛选同样在本地完成
  const filteredSessions = Array.isArray(sessions)
    ? sessions
        .filter((s) => {
          if (statusFilter && statusFilter !== 'all' && (s.status || '') !== statusFilter) return false
          if (!query) return true
          return [s.hostname, s.username, s.remote_addr, s.process_name, s.id]
            .some((v) => (v || '').toLowerCase().includes(query))
        })
        .sort(
          (a, b) => new Date(b.first_seen).getTime() - new Date(a.first_seen).getTime()
        )
    : []

  const [confirmDelete, setConfirmDelete] = useState<Session | null>(null)
  const [deleting, setDeleting] = useState(false)

  // 删除主机：后端会先向植入端推送"exit"任务令其停止运行，再移除会话记录
  const doDelete = async (session: Session) => {
    setDeleting(true)
    try {
      await sessionApi.delete(session.id)
      onSessionsChange(sessions.filter((s) => s.id !== session.id))
      setConfirmDelete(null)
    } catch (error) {
      console.error('Failed to delete session:', error)
    } finally {
      setDeleting(false)
    }
  }

  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingValue, setEditingValue] = useState('')

  const startEdit = (session: Session) => {
    setEditingId(session.id)
    setEditingValue(session.comment || '')
  }

  const saveEdit = async (id: string) => {
    try {
      await sessionApi.update(id, editingValue)
      onSessionsChange(
        sessions.map((s) => (s.id === id ? { ...s, comment: editingValue } : s))
      )
    } catch (error) {
      console.error('Failed to update session comment:', error)
    }
    setEditingId(null)
    setEditingValue('')
  }

  // 每秒触发一次重渲染，让心跳相对时间实时走动
  const [, setTick] = useState(0)
  useEffect(() => {
    const t = setInterval(() => setTick((x) => x + 1), 1000)
    return () => clearInterval(t)
  }, [])

  const tableEl = (
    <div className="sessions-list-panel">
      <table className="sessions-table">
        <thead>
          <tr><th>#</th><th>备注</th><th>状态</th><th>主机名</th><th>用户</th><th>进程ID</th><th>内网IP</th><th>操作系统</th><th>心跳</th><th>操作</th></tr>
        </thead>
        <tbody>
          {filteredSessions.map((session, index) => {
            const st = sessionStatusBadge(session.status || '')
            const hb = heartbeatBadge(session.last_seen)
            const ip = session.remote_addr ? session.remote_addr.split(':')[0] : ''
            return (
              <tr
                key={session.id}
                className={selectedSession?.id === session.id ? 'selected' : ''}
                onClick={() => onSelectSession(session)}
              >
                <td><span className="session-index">{index + 1}</span></td>
                <td>
                  <div className="comment-cell" onClick={(e) => e.stopPropagation()}>
                    {editingId === session.id ? (
                      <input
                        className="comment-input"
                        value={editingValue}
                        onChange={(e) => setEditingValue(e.target.value)}
                        onBlur={() => saveEdit(session.id)}
                        onKeyDown={(e) => { if (e.key === 'Enter') { e.currentTarget.blur() } if (e.key === 'Escape') { setEditingId(null); setEditingValue('') } }}
                        autoFocus
                        placeholder="添加备注"
                      />
                    ) : (
                      <span className="comment-text" title={session.comment || '点击编辑备注'} onClick={() => startEdit(session)}>
                        {session.comment || <span className="comment-placeholder">点击添加</span>}
                      </span>
                    )}
                  </div>
                </td>
                <td>
                  <div className="status-cell">
                    <Badge tone={st.tone}>{st.label}</Badge>
                    {session.listener?.startsWith('relay') && (
                      <span className="relay-badge" title="经中继链回连（Beacon Mesh）">
                        <Badge tone="accent">
                          {session.listener === 'relay' ? '中继' : `中继×${session.listener.slice(5)}`}
                        </Badge>
                      </span>
                    )}
                  </div>
                </td>
                <td>
                  <div className="hostname-cell" title={session.hostname || '-'}>
                    <Monitor size={15} />
                    <span>{session.hostname || '-'}</span>
                  </div>
                </td>
                <td>
                  <div className="user-cell" title={`${session.username || '-'}@${session.domain || '-'}`}>
                    <span className="username">{session.username || '-'}</span>
                    <span className="domain">@{session.domain || '-'}</span>
                  </div>
                </td>
                <td><span className="mono" title={session.pid ? String(session.pid) : undefined}>{session.pid || '-'}</span></td>
                <td><span className="mono" title={ip || undefined}>{ip || '-'}</span></td>
                <td><div className="os-cell" title={`${session.os || '-'} ${session.arch || ''}`.trim()}><span>{session.os || '-'}</span></div></td>
                <td><Badge tone={hb.tone}>{hb.text}</Badge></td>
                <td><div className="actions">
                  <button className="action-btn" title="创建SOCKS5代理" onClick={(e) => { e.stopPropagation(); navigate(`/tunnels?session=${session.id}`) }}><Network size={16} /></button>
                  <button className="action-btn danger" title="删除" onClick={(e) => { e.stopPropagation(); setConfirmDelete(session) }}><Trash2 size={16} /></button>
                </div></td>
              </tr>
            )
          })}
        </tbody>
      </table>
      {filteredSessions.length === 0 && (
        sessions.length === 0 ? (
          <Empty
            icon={<WifiOff size={26} />}
            title="还没有会话"
            desc="植入端首次上线后会自动出现在这里"
          />
        ) : (
          <Empty
            icon={<Network size={26} />}
            title="没有匹配的会话"
            desc="换个关键词，或清除状态筛选后再试"
            action={
              <button type="button" className="sessions-clear-btn" onClick={onClearFilters}>
                清除筛选
              </button>
            }
          />
        )
      )}
    </div>
  )

  return (
    <>
      {tableEl}
      {confirmDelete && (
        <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget && !deleting) setConfirmDelete(null) }}>
          <div className="modal confirm-modal">
            <div className="modal-header">
              <h2>删除主机</h2>
              <button className="close-btn" onClick={() => setConfirmDelete(null)}>×</button>
            </div>
            <div className="modal-body">
              <p className="confirm-text">
                确定要删除主机 <strong>{confirmDelete.hostname || confirmDelete.id}</strong> 吗？
              </p>
              <p className="confirm-warning">
                删除后将发送停止指令，植入端会停止运行，该主机会话记录将被移除，此操作不可恢复。
              </p>
            </div>
            <div className="modal-footer">
              <button className="btn" disabled={deleting} onClick={() => setConfirmDelete(null)}>取消</button>
              <button className="btn btn-danger" disabled={deleting} onClick={() => doDelete(confirmDelete)}>
                {deleting ? '删除中...' : '确认删除'}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
