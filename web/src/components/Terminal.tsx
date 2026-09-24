import { useState, useEffect, useRef, useCallback, forwardRef, useImperativeHandle } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { Wifi, WifiOff, Trash2, Loader2, Maximize2, Minimize2, ExternalLink } from 'lucide-react'
import '@xterm/xterm/css/xterm.css'
import './Terminal.css'

export interface TerminalProps {
  /** WebSocket shell path, e.g. /api/v1/sessions/{id}/shell */
  wsPath: string
  /** Display title */
  title?: string
  /** Substring of the title to highlight (e.g. hostname) */
  titleHighlight?: string
  /** Whether to auto-connect */
  autoConnect?: boolean
  /** Called when connection state changes */
  onConnectionChange?: (connected: boolean) => void
  /** Session ID for the shell connection */
  sessionId?: string
  /** Show open-in-new-tab button */
  showNewTab?: boolean
  /** Called when server pushes a CWD marker message (\x00CWD\x00<dir>) */
  onCWDChange?: (cwd: string) => void
  /** Follow global data-theme (dark/light) for xterm colors. Default false keeps classic dark look */
  followTheme?: boolean
  /**
   * 是否可见。给"切面板要保留会话"的场景用：不可见时只隐藏、**不卸载**。
   *
   * 卸载会触发本组件的清理逻辑关闭 WebSocket，而服务端在 WS 关闭时会
   * `defer CloseShell(sessionID)` —— 那是**真实杀掉靶机上的 shell 进程**，
   * 不是前端假断开。切回来只能重开一个新 shell（历史与运行中的程序全丢）。
   * 所以宿主页面应当常驻挂载、切 tab 时把本属性置 false。
   */
  visible?: boolean
  /**
   * 远端是否自带 tty 回显。
   *
   * - true  ：Linux/macOS 走 PTY + bash -i，readline 自己负责回显与行编辑，
   *           输入必须**裸送**（本地再回显一次会与远端回显叠加成"双份字符"）。
   * - false ：Windows 走 cmd.exe /Q 管道，没有 tty 也不回显，
   *           由前端本地回显 + 行缓冲兜底（否则用户打字看不到任何反馈）。
   */
  remoteEcho?: boolean
}

export interface TerminalHandle {
  /** Inject raw text into the shell input stream (e.g. a cd command) */
  sendText: (text: string) => void
}

// xterm color themes
const XTERM_THEMES: Record<'dark' | 'light', Record<string, string>> = {
  dark: {
    background: '#1a1a2e',
    foreground: '#e0e0e0',
    cursor: '#e94560',
    cursorAccent: '#1a1a2e',
    selectionBackground: '#3a3a5c',
    black: '#1a1a2e',
    red: '#e94560',
    green: '#0f9d58',
    yellow: '#f4b400',
    blue: '#4285f4',
    magenta: '#aa46bb',
    cyan: '#24c1e0',
    white: '#e0e0e0',
    brightBlack: '#4a4a6a',
    brightRed: '#ff6b81',
    brightGreen: '#34c759',
    brightYellow: '#ffd60a',
    brightBlue: '#64b5f6',
    brightMagenta: '#ce93d8',
    brightCyan: '#4dd0e1',
    brightWhite: '#ffffff',
  },
  light: {
    background: '#ffffff',
    foreground: '#1a1a1a',
    cursor: '#6366f1',
    cursorAccent: '#ffffff',
    selectionBackground: '#e0e7ff',
    black: '#000000',
    red: '#dc2626',
    green: '#16a34a',
    yellow: '#d97706',
    blue: '#2563eb',
    magenta: '#9333ea',
    cyan: '#0891b2',
    white: '#e5e5e5',
    brightBlack: '#71717a',
    brightRed: '#ef4444',
    brightGreen: '#22c55e',
    brightYellow: '#f59e0b',
    brightBlue: '#3b82f6',
    brightMagenta: '#a855f7',
    brightCyan: '#06b6d4',
    brightWhite: '#ffffff',
  },
}

// ─── 剪贴板工具 ───────────────────────────────────────────────────────────
// 部署形态常见的是明文 HTTP（局域网 IP + 非 443 端口），此时浏览器**不给**
// navigator.clipboard（非安全上下文），所以每个 API 都必须有兜底路径。

/** 能否用脚本读剪贴板（粘贴的前提）。HTTP 明文部署下为 false。 */
const canReadClipboard = (): boolean =>
  typeof navigator !== 'undefined' && !!navigator.clipboard?.readText

/** 写剪贴板：优先异步 API，失败回退 execCommand（覆盖非安全上下文）。 */
async function writeClipboard(text: string): Promise<void> {
  if (!text) return
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return
    }
  } catch {
    /* 权限被拒 / 非安全上下文 → 走 execCommand 兜底 */
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.top = '-1000px'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  try {
    document.execCommand('copy')
  } catch {
    /* 极少数浏览器仍拒绝：忽略，用户还能用浏览器右键菜单复制 */
  }
  document.body.removeChild(ta)
}

/** 把点击坐标换算成字符格（列/行）。行是视口行，与 buffer.active.cursorY 同一坐标系。 */
function cellFromPoint(
  term: XTerm,
  clientX: number,
  clientY: number,
): { col: number; row: number } | null {
  const screenEl = term.element?.querySelector('.xterm-screen') as HTMLElement | null
  if (!screenEl || term.cols <= 0 || term.rows <= 0) return null
  const rect = screenEl.getBoundingClientRect()
  const cellW = rect.width / term.cols
  const cellH = rect.height / term.rows
  if (cellW <= 0 || cellH <= 0) return null
  const col = Math.floor((clientX - rect.left) / cellW)
  const row = Math.floor((clientY - rect.top) / cellH)
  if (col < 0 || row < 0 || col >= term.cols || row >= term.rows) return null
  return { col, row }
}

export const TerminalComponent = forwardRef<TerminalHandle, TerminalProps>(function TerminalComponent(
  {
    wsPath,
    title,
    autoConnect = false,
    onConnectionChange,
    sessionId,
    showNewTab = false,
    onCWDChange,
    followTheme = false,
    titleHighlight,
    visible = true,
    remoteEcho = false,
  }: TerminalProps,
  ref,
) {
  const [connected, setConnected] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [maximized, setMaximized] = useState(false)
  // 始终使用暗黑主题，不跟随全局亮色
  const [themeMode, setThemeMode] = useState<'dark' | 'light'>('dark')
  const terminalRef = useRef<HTMLDivElement>(null)
  const xtermRef = useRef<XTerm | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const connectedRef = useRef(false)
  const onCWDChangeRef = useRef(onCWDChange)
  const themeModeRef = useRef(themeMode)
  const remoteEchoRef = useRef(remoteEcho)
  const visibleRef = useRef(visible)
  // 管道后端（Windows）的行缓冲；连接建立/断开时清空，避免残留上一轮没提交的半行命令
  const resetInputBufRef = useRef<() => void>(() => {})
  // 状态栏提示的 setter（供 ws.onmessage 里的 NOTICE 标记调用，避免把 connect 的依赖搅动）
  const showNoticeRef = useRef<(text: string, tone?: 'info' | 'warn' | 'error', ms?: number) => void>(() => {})

  useEffect(() => { onCWDChangeRef.current = onCWDChange }, [onCWDChange])
  useEffect(() => { themeModeRef.current = themeMode }, [themeMode])
  useEffect(() => { remoteEchoRef.current = remoteEcho }, [remoteEcho])
  useEffect(() => { visibleRef.current = visible }, [visible])

  // 兜底：wsPath 变了但本组件没被重建（调用方漏了 key）时，绝不能继续沿用旧连接 ——
  // 那会让标题栏显示新主机、实际却在操作老主机的 shell。
  // 这里直接断掉（失败必须是"可见的"），真正该做的是调用方用 key 触发重建。
  const prevWsPathRef = useRef(wsPath)
  useEffect(() => {
    if (prevWsPathRef.current === wsPath) return
    console.warn(
      '[Terminal] wsPath 变化但组件未重建，已断开旧连接：%s -> %s（调用方应给本组件加 key）',
      prevWsPathRef.current, wsPath,
    )
    prevWsPathRef.current = wsPath
    if (wsRef.current) { wsRef.current.close(); wsRef.current = null }
    connectedRef.current = false
    setConnected(false)
    setConnecting(false)
  }, [wsPath])

  // Keep xterm on the classic dark palette regardless of global data-theme
  useEffect(() => {
    const mode: 'dark' | 'light' = 'dark'
    themeModeRef.current = mode
    setThemeMode(mode)
    if (xtermRef.current) xtermRef.current.options.theme = XTERM_THEMES[mode]
  }, [followTheme])

  // Expose sendText so the file browser can drive the shell (e.g. cd into a dir)
  useImperativeHandle(ref, () => ({
    sendText: (text: string) => {
      if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
        wsRef.current.send(text)
      }
    },
  }), [])

  /** 只在终端真正可见且有尺寸时 fit；隐藏（display:none）时测量为 0，会把终端挤成一列。 */
  const safeFit = useCallback(() => {
    const el = terminalRef.current
    if (!el) return
    const rect = el.getBoundingClientRect()
    if (rect.width < 20 || rect.height < 20) return
    try {
      fitAddonRef.current?.fit()
    } catch {
      /* xterm 在极端布局下偶发抛错，忽略即可，下次尺寸变化会重试 */
    }
  }, [])

  // 本地/系统级提示（粘贴失败、链路中断等）一律走状态栏，**不能 terminal.write**：
  // 终端里显示的应当是靶机会话的内容。往终端写会污染回滚缓冲，而且本地 write 会推进
  // xterm 的光标与缓冲区，远端 readline 并不知道，之后远端输出会把这一行覆盖得乱七八糟。
  const [notice, setNotice] = useState<{ text: string; tone: 'info' | 'warn' | 'error' } | null>(null)
  const noticeTimerRef = useRef<number | null>(null)
  const showNotice = useCallback((text: string, tone: 'info' | 'warn' | 'error' = 'warn', ms = 6000) => {
    setNotice({ text, tone })
    if (noticeTimerRef.current) window.clearTimeout(noticeTimerRef.current)
    noticeTimerRef.current = window.setTimeout(() => setNotice(null), ms)
  }, [])
  useEffect(() => () => {
    if (noticeTimerRef.current) window.clearTimeout(noticeTimerRef.current)
  }, [])
  useEffect(() => { showNoticeRef.current = showNotice }, [showNotice])

  /** 粘贴：优先异步 API 读取，读不到就引导用户用浏览器原生粘贴（Ctrl+V）。 */
  const pasteInto = useCallback(async (terminal: XTerm) => {
    // 明文 HTTP（非安全上下文）下没有 navigator.clipboard，只能靠浏览器原生粘贴
    if (!canReadClipboard()) {
      showNotice('当前页面非安全上下文（非 HTTPS/localhost），脚本读不到剪贴板 —— 请用 Ctrl+V 或右键菜单「粘贴」', 'warn')
      return
    }
    try {
      const text = await navigator.clipboard.readText()
      // terminal.paste() 会按需加上 bracketed-paste 包裹，并触发 onData 走正常上行通道
      if (text) terminal.paste(text)
    } catch (err) {
      // 常见于浏览器未授予剪贴板读取权限（NotAllowedError）。原生 Ctrl+V 不受此限制。
      const msg = err instanceof Error ? err.message : String(err)
      showNotice(`读取剪贴板被拒（${msg}）—— 请改用 Ctrl+V 或右键菜单「粘贴」`, 'error')
    }
  }, [showNotice])

  const initTerminal = useCallback(() => {
    if (!terminalRef.current || xtermRef.current) return

    const terminal = new XTerm({
      theme: XTERM_THEMES[themeModeRef.current],
      fontFamily: 'Consolas, "Courier New", monospace',
      fontSize: 14,
      lineHeight: 1.2,
      // 光标：保持标准 1s 闪烁（xterm 内部是 `animation: blink 1s step-end infinite`）。
      // 注意：不要在全局 CSS 里用 `* { animation-duration: 0.01ms }` 去"减少动效"——
      // 那会让这个无限动画变成每秒循环十万次（用户实测"光标闪这么快"），改法见 index.css。
      cursorBlink: true,
      cursorStyle: 'block',
      // 失去焦点时用空心光标，避免多个终端同时闪烁（也更省 repaint）
      cursorInactiveStyle: 'outline',
      scrollback: 10000,
      allowProposedApi: true,
      // Linux shell 输出为 LF（\n），convertEol 让 LF 同时回车，避免每行输出阶梯状错位
      convertEol: true,
      // 右键不选词：右键留给"有选区则复制、无选区则粘贴"（见下面的 contextmenu 处理）
      rightClickSelectsWord: false,
    })

    const fitAddon = new FitAddon()
    terminal.loadAddon(fitAddon)
    xtermRef.current = terminal
    fitAddonRef.current = fitAddon

    terminal.open(terminalRef.current)
    setTimeout(() => { safeFit(); terminal.focus() }, 0)

    terminal.writeln('\x1b[36m═══════════════════════════════════════\x1b[0m')
    terminal.writeln('\x1b[36m  ToShell Interactive Terminal\x1b[0m')
    terminal.writeln('\x1b[36m  点击"连接"按钮开始会话\x1b[0m')
    terminal.writeln('\x1b[36m  复制 Ctrl+Shift+C / 粘贴 Ctrl+Shift+V（或右键）\x1b[0m')
    terminal.writeln('\x1b[36m═══════════════════════════════════════\x1b[0m')
    terminal.writeln('')

    const isOpen = () =>
      connectedRef.current && !!wsRef.current && wsRef.current.readyState === WebSocket.OPEN

    const sendRaw = (text: string) => {
      if (isOpen()) wsRef.current!.send(text)
    }

    // Buffer for accumulating the current command line（仅 remoteEcho=false 的管道后端用）
    let inputBuf = ''
    resetInputBufRef.current = () => { inputBuf = '' }

    terminal.onData((data) => {
      if (!isOpen()) return

      // ── PTY 后端（Linux/macOS bash -i）：远端 readline 自己回显 + 行编辑 ──
      // 输入必须原样透传：方向键、Ctrl+A/E/W/R、Tab 补全、历史都靠它工作。
      // 前端若再本地回显一次，会与远端回显叠加成"双份字符"，光标位置也会与实际不符。
      if (remoteEchoRef.current) {
        wsRef.current!.send(data)
        return
      }

      // ── 管道后端（Windows cmd.exe /Q）：无 tty、无回显，前端本地回显 ──
      // 逐字符走状态机（粘贴会一次送来多字符，所以不能只看首字符）。
      let i = 0
      while (i < data.length) {
        const ch = data[i]
        const code = ch.charCodeAt(0)

        if (code === 0x1b) {
          // 转义序列（方向键/Home/End/功能键）整段裸送，不参与本地回显
          sendRaw(data.slice(i))
          return
        }
        if (code === 13 || code === 10) {
          // 回车：把缓冲整行送出去
          terminal.write('\r\n')
          sendRaw(inputBuf + '\r\n')
          inputBuf = ''
        } else if (code === 127 || code === 8) {
          if (inputBuf.length > 0) {
            inputBuf = inputBuf.slice(0, -1)
            terminal.write('\b \b')
          }
        } else if (code === 3) {
          terminal.write('^C\r\n')
          inputBuf = ''
          sendRaw('\x03')
        } else if (code >= 32) {
          // 可打印字符（含非 ASCII 中文）：本地回显 + 入缓冲
          inputBuf += ch
          terminal.write(ch)
        }
        // 其余控制字符：忽略（不发送也不回显）
        i++
      }
    })

    // ── 鼠标：点击定位光标 ────────────────────────────────────────────────
    // 靶机是 PTY + readline 时，左右方向键就是"移动命令行光标"，所以把点击的列
    // 换算成相对当前光标列的偏移，合成左右方向键发过去即可。
    // 多重保险：只在"点在同一行 / 没有拖拽出选区 / 不是全屏程序 / 远端没接管鼠标"时生效；
    // 方向键在 readline 里会被行首行尾夹住，算错列最多是光标没动，不会破坏命令内容。
    let downX = 0
    let downY = 0
    let downAt = 0
    const onMouseDown = (e: MouseEvent) => {
      if (e.button !== 0) return
      downX = e.clientX
      downY = e.clientY
      downAt = Date.now()
    }
    const onMouseUp = (e: MouseEvent) => {
      if (e.button !== 0) return
      const term = xtermRef.current
      if (!term || !remoteEchoRef.current) return
      if (term.hasSelection()) return // 拖拽选字：把鼠标留给选区
      if (e.shiftKey || e.altKey || e.ctrlKey || e.metaKey) return // 组合键另有含义
      if (Date.now() - downAt > 800) return // 长按
      if (Math.abs(e.clientX - downX) > 4 || Math.abs(e.clientY - downY) > 4) return // 轻微拖拽
      // 全屏程序（vim/top）或远端应用已接管鼠标时不干预
      if (term.buffer.active.type !== 'normal') return
      if (term.modes.mouseTrackingMode !== 'none') return

      const cell = cellFromPoint(term, e.clientX, e.clientY)
      if (!cell) return
      const buf = term.buffer.active
      // 只有点在光标所在行（也就是正在输入的那一行）才当作"定位光标"
      if (cell.row !== buf.cursorY) return

      const delta = cell.col - buf.cursorX
      if (delta === 0) return
      // DECCKM：应用光标键模式下方向键用 SS3（ESC O C/D）而不是 CSI（ESC [ C/D）
      const [left, right] = term.modes.applicationCursorKeysMode
        ? ['\x1bOD', '\x1bOC']
        : ['\x1b[D', '\x1b[C']
      sendRaw(delta < 0 ? left.repeat(-delta) : right.repeat(delta))
    }
    const el = terminal.element
    el?.addEventListener('mousedown', onMouseDown)
    el?.addEventListener('mouseup', onMouseUp)

    // ── 键盘：复制 / 粘贴 ────────────────────────────────────────────────
    // xterm 默认把 Ctrl+C 当 SIGINT 发走，也没有任何复制粘贴快捷键绑定，
    // 在无鼠标环境的浏览器里"选中了却复制不出来"。这里补齐业界通用绑定。
    terminal.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== 'keydown') return true
      const mod = ev.ctrlKey || ev.metaKey

      const isCopyKey =
        (ev.ctrlKey && ev.code === 'Insert') || (mod && ev.shiftKey && ev.code === 'KeyC')
      const isPasteKey =
        (!ev.ctrlKey && ev.shiftKey && ev.code === 'Insert') ||
        (mod && ev.shiftKey && ev.code === 'KeyV')

      // 注意：这些分支必须 preventDefault —— 否则浏览器自己还会执行一次默认动作，
      // 粘贴会变成两次（Chromium 的 Ctrl+Shift+V 就是"粘贴为纯文本"，会再触发一次
      // 原生 paste 事件，xterm 也把它转成一次上行输入）。
      if (isCopyKey) {
        ev.preventDefault()
        const sel = terminal.getSelection()
        if (sel) {
          void writeClipboard(sel)
          terminal.clearSelection()
        }
        return false // 别让 xterm 再把它翻译成控制字符
      }

      if (isPasteKey) {
        ev.preventDefault()
        void pasteInto(terminal)
        return false
      }

      // Ctrl+C 带选区时按现代终端习惯复制而不是发 SIGINT（无选区时保持 ^C 中断）
      if (ev.ctrlKey && !ev.shiftKey && !ev.altKey && ev.code === 'KeyC' && terminal.hasSelection()) {
        ev.preventDefault()
        void writeClipboard(terminal.getSelection())
        terminal.clearSelection()
        return false
      }

      return true
    })

    // ── 鼠标右键：有选区就复制，没选区就粘贴 ──────────────────────────────
    // 明文 HTTP 下脚本读不到剪贴板，此时**不拦截** contextmenu，
    // 让浏览器原生菜单出现——xterm 已把隐藏 textarea 挪到鼠标位置，
    // 菜单里的"粘贴"能正常落进终端。
    const onContextMenu = (e: MouseEvent) => {
      const sel = terminal.getSelection()
      if (sel) {
        e.preventDefault()
        void writeClipboard(sel)
        terminal.clearSelection()
        return
      }
      if (!canReadClipboard()) return // 交给浏览器原生菜单
      e.preventDefault()
      void pasteInto(terminal)
    }
    el?.addEventListener('contextmenu', onContextMenu)

    // 尺寸变化用 ResizeObserver：既覆盖窗口缩放，也覆盖"从隐藏切回可见"
    // （display:none → 有尺寸 也会触发），比只监听 window.resize 更可靠。
    let ro: ResizeObserver | null = null
    if (typeof ResizeObserver !== 'undefined' && terminalRef.current) {
      ro = new ResizeObserver(() => {
        if (!visibleRef.current) return
        safeFit()
      })
      ro.observe(terminalRef.current)
    }
    const handleResize = () => { if (visibleRef.current) safeFit() }
    window.addEventListener('resize', handleResize)

    return () => {
      window.removeEventListener('resize', handleResize)
      ro?.disconnect()
      el?.removeEventListener('mousedown', onMouseDown)
      el?.removeEventListener('mouseup', onMouseUp)
      el?.removeEventListener('contextmenu', onContextMenu)
      if (wsRef.current) { wsRef.current.close(); wsRef.current = null }
    }
  }, [safeFit, pasteInto])

  useEffect(() => { initTerminal() }, [initTerminal])

  const connect = useCallback(() => {
    if (!xtermRef.current) return
    setConnecting(true)

    const token = localStorage.getItem('toshell-token')
    const fullUrl = wsPath.startsWith('ws')
      ? wsPath + `?token=${encodeURIComponent(token || '')}`
      : `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}${wsPath}?token=${encodeURIComponent(token || '')}`

    const ws = new WebSocket(fullUrl)
    wsRef.current = ws

    ws.onopen = () => {
      resetInputBufRef.current()
      setConnected(true)
      connectedRef.current = true
      setConnecting(false)
      xtermRef.current?.focus()
      onConnectionChange?.(true)
    }
    ws.onmessage = (event) => {
      let data: string = event.data
      // CWD marker message from server: \x00CWD\x00<dir> — route to onCWDChange
      if (data.startsWith('\x00CWD\x00')) {
        onCWDChangeRef.current?.(data.slice(5))
        return
      }
      // NOTICE marker: \x00NOTICE\x00<tone>|<text> —— 服务端的系统级提示
      // （如"链路中断，按键已丢弃"）。这类信息属于 UI 外壳，**不能写进终端**，
      // 否则会污染靶机会话的回滚缓冲、并打乱本地光标与远端 readline 的对应关系。
      if (data.startsWith('\x00NOTICE\x00')) {
        const body = data.slice(9)
        const sep = body.indexOf('|')
        const tone = (sep > 0 ? body.slice(0, sep) : 'warn') as 'info' | 'warn' | 'error'
        showNoticeRef.current?.(sep > 0 ? body.slice(sep + 1) : body, tone)
        return
      }
      data = data.replace(/\x1b\]0;[^\x07\x1b]*(?:\x07|\x1b\\)/g, '')
      xtermRef.current?.write(data)
    }
    ws.onerror = () => {
      xtermRef.current?.writeln('\x1b[31m[ 连接错误 ]\x1b[0m')
      setConnected(false); connectedRef.current = false; setConnecting(false)
      onConnectionChange?.(false)
    }
    ws.onclose = (event) => {
      resetInputBufRef.current()
      xtermRef.current?.writeln(`\r\n\x1b[33m[ 断开: code=${event.code} ]\x1b[0m`)
      setConnected(false); connectedRef.current = false; setConnecting(false)
      onConnectionChange?.(false)
    }
  }, [wsPath, onConnectionChange])

  const disconnect = useCallback(() => {
    if (wsRef.current) { wsRef.current.close(); wsRef.current = null }
    setConnected(false); connectedRef.current = false
    onConnectionChange?.(false)
  }, [onConnectionChange])

  const clear = () => xtermRef.current?.clear()
  const toggleMaximize = () => setMaximized((v) => !v)

  // 状态栏标题：titleHighlight 指定的片段（如主机名）单独高亮着色
  const renderTitle = () => {
    if (!title) return 'Terminal'
    if (!titleHighlight || !title.includes(titleHighlight)) return title
    const idx = title.indexOf(titleHighlight)
    return (
      <>
        {title.slice(0, idx)}
        <span className="terminal-title-highlight">{titleHighlight}</span>
        {title.slice(idx + titleHighlight.length)}
      </>
    )
  }

  useEffect(() => {
    if (autoConnect) connect()
    return () => { if (wsRef.current) { wsRef.current.close(); wsRef.current = null } }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Re-fit terminal when maximized toggles
  useEffect(() => {
    if (fitAddonRef.current) {
      setTimeout(() => safeFit(), 50)
    }
  }, [maximized, safeFit])

  // 从隐藏切回可见：重新测量尺寸并抢回焦点（隐藏期间测量为 0，必须重算）
  useEffect(() => {
    if (!visible) return
    const raf = requestAnimationFrame(() => {
      safeFit()
      if (connectedRef.current) xtermRef.current?.focus()
    })
    return () => cancelAnimationFrame(raf)
  }, [visible, safeFit])

  return (
    <div className={`terminal-component ${maximized ? 'terminal-maximized' : ''} ${followTheme ? (themeMode === 'light' ? 'terminal-light' : 'terminal-dark') : ''}`}>
      {/* Status Bar */}
      <div className="terminal-status-bar">
        <span className="terminal-title">{renderTitle()}</span>
        {/* 本地/系统级提示（粘贴失败、链路中断…）显示在这里，不写进终端正文 */}
        {notice && (
          <span className={`terminal-notice terminal-notice-${notice.tone}`} title={notice.text}>
            {notice.text}
          </span>
        )}
        <div className="terminal-actions">
          <span className={`terminal-status ${connected ? 'connected' : 'disconnected'}`}>
            {connecting ? (
              <><Loader2 size={14} className="spin" /> 连接中</>
            ) : connected ? (
              <><Wifi size={14} /> 已连接</>
            ) : (
              <><WifiOff size={14} /> 断开</>
            )}
          </span>
          {!connected ? (
            <button className="term-btn connect-btn" onClick={connect} disabled={connecting}>
              {connecting ? '连接中...' : '连接'}
            </button>
          ) : (
            <button className="term-btn disconnect-btn" onClick={disconnect}>断开</button>
          )}
          <button className="term-btn" onClick={clear} title="清屏">
            <Trash2 size={14} />
          </button>
          {showNewTab && sessionId && (
            <button className="term-btn" onClick={() => window.open(`/shell/${sessionId}`, '_blank')} title="在新标签页打开">
              <ExternalLink size={14} />
            </button>
          )}
          <button className="term-btn" onClick={toggleMaximize} title={maximized ? '还原' : '最大化'}>
            {maximized ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
          </button>
        </div>
      </div>

      {/* Terminal Container */}
      <div className="terminal-container" ref={terminalRef} />
    </div>
  )
})

// Styles in ./Terminal.css - import in your entry point or component usage
