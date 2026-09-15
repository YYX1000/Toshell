import { ReactNode, useState } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import {
  LayoutDashboard,
  Users,
  Terminal,
  FileText,
  Settings,
  LogOut,
  Menu,
  X,
  Shell,
  Sun,
  Moon,
  FileCode,
  Network,
  Package,
  Radio,
  Layers,
  Info,
  Sparkles,
  Clock,
  Wifi,
} from 'lucide-react'
import { useAuthStore } from '../stores/auth'
import { useThemeStore } from '../stores/theme'
import { useI18n } from '../i18n'
import { NotificationCenter } from './NotificationCenter'
import './Layout.css'

interface NavItem {
  path: string
  icon: ReactNode
  /** 导航文案 key（i18n） */
  tKey: string
}

interface NavGroup {
  /** 分组标题 key（i18n） */
  tTitle: string
  items: NavItem[]
}

// 导航项用 i18n key，组件内渲染时经 t() 取当前语言文案。
const navGroups: NavGroup[] = [
  {
    tTitle: 'nav.core',
    items: [
      { path: '/', icon: <LayoutDashboard size={20} />, tKey: 'nav.dashboard' },
      { path: '/sessions', icon: <Users size={20} />, tKey: 'nav.sessions' },
      { path: '/builds', icon: <FileCode size={20} />, tKey: 'nav.builds' },
      { path: '/terminal', icon: <Terminal size={20} />, tKey: 'nav.terminal' },
      { path: '/templates', icon: <Layers size={20} />, tKey: 'nav.templates' },
    ],
  },
  {
    tTitle: 'nav.network',
    items: [
      { path: '/tunnels', icon: <Network size={20} />, tKey: 'nav.tunnels' },
      { path: '/listeners', icon: <Radio size={20} />, tKey: 'nav.listeners' },
      { path: '/channels', icon: <Wifi size={20} />, tKey: 'nav.channels' },
      { path: '/plugins', icon: <Package size={20} />, tKey: 'nav.plugins' },
    ],
  },
  {
    tTitle: 'nav.intel',
    items: [
      { path: '/logs', icon: <FileText size={20} />, tKey: 'nav.logs' },
      { path: '/timeline', icon: <Clock size={20} />, tKey: 'nav.timeline' },
      { path: '/copilot', icon: <Sparkles size={20} />, tKey: 'nav.copilot' },
    ],
  },
  {
    tTitle: 'nav.system',
    items: [
      { path: '/settings', icon: <Settings size={20} />, tKey: 'nav.settings' },
      { path: '/about', icon: <Info size={20} />, tKey: 'nav.about' },
    ],
  },
]

export function Layout({ children }: { children: ReactNode }) {
  // 折叠状态持久化：刷新/切页后不再每次都展开（窄屏默认折叠）。
  const [sidebarOpen, setSidebarOpen] = useState<boolean>(() => {
    const saved = localStorage.getItem('toshell.sidebar')
    if (saved === 'collapsed') return false
    if (saved === 'open') return true
    return typeof window !== 'undefined' ? window.innerWidth > 1100 : true
  })
  const location = useLocation()
  const { logout, username } = useAuthStore()
  const { theme, toggleTheme } = useThemeStore()
  const { t, lang, setLang } = useI18n()

  const toggleSidebar = () => {
    setSidebarOpen((v) => {
      localStorage.setItem('toshell.sidebar', v ? 'collapsed' : 'open')
      return !v
    })
  }

  const handleLogout = () => {
    logout()
    window.location.href = '/login'
  }

  // 当前路由对应导航 label（i18n）
  const currentNav = navGroups
    .flatMap((g) => g.items)
    .find((item) => item.path === location.pathname)
  const pageTitle = currentNav ? t(currentNav.tKey) : 'ToShell'

  return (
    <div className="layout">
      {/* 窄屏（<900px）展开侧栏时的遮罩：点一下收起，避免"挡住了看不到主区" */}
      {sidebarOpen && <div className="sidebar-backdrop" onClick={toggleSidebar} />}

      <aside className={`sidebar ${sidebarOpen ? 'open' : 'collapsed'}`}>
        <div className="sidebar-header">
          <div className="logo">
            <Shell size={28} className="logo-icon" />
            {sidebarOpen && <span className="logo-text">ToShell</span>}
          </div>
          <button className="toggle-btn" onClick={toggleSidebar} title={sidebarOpen ? '收起侧栏' : '展开侧栏'}>
            {sidebarOpen ? <X size={18} /> : <Menu size={18} />}
          </button>
        </div>

        <nav className="nav">
          {navGroups.map((group) => (
            <div key={group.tTitle} className="nav-group">
              {sidebarOpen && <div className="nav-group-title">{t(group.tTitle)}</div>}
              {group.items.map((item) => (
                <NavLink
                  key={item.path}
                  to={item.path}
                  // 折叠时只剩图标：用 title 提供悬浮提示，避免"认不出是哪个功能"
                  title={sidebarOpen ? undefined : t(item.tKey)}
                  className={({ isActive }) => `nav-item ${isActive ? 'active' : ''}`}
                  onClick={() => {
                    // 窄屏点完导航自动收起侧栏（否则遮住内容）
                    if (window.innerWidth <= 900) {
                      setSidebarOpen(false)
                      localStorage.setItem('toshell.sidebar', 'collapsed')
                    }
                  }}
                >
                  {item.icon}
                  {sidebarOpen && <span>{t(item.tKey)}</span>}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>

        <div className="sidebar-footer">
          {sidebarOpen && (
            <div className="user-info">
              <div className="avatar">{username?.charAt(0).toUpperCase()}</div>
              <span className="username">{username}</span>
            </div>
          )}
          <button className="logout-btn" onClick={handleLogout} title={sidebarOpen ? undefined : t('common.logout')}>
            <LogOut size={18} />
            {sidebarOpen && <span>{t('common.logout')}</span>}
          </button>
        </div>
      </aside>

      <main className="main-content">
        <header className="topbar">
          {/* 窄屏时侧栏是浮层，顶栏需要一个"汉堡"入口 */}
          <button className="topbar-menu-btn" onClick={toggleSidebar} title="菜单">
            <Menu size={18} />
          </button>
          <div className="page-title">{pageTitle}</div>
          <div className="topbar-actions">
            <NotificationCenter />
            {/* 语言切换 */}
            <button className="lang-toggle" onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')} title={lang === 'zh' ? 'Switch to English' : '切换到中文'}>
              {lang === 'zh' ? 'EN' : '中'}
            </button>
            <button className="theme-toggle" onClick={toggleTheme} title={theme === 'dark' ? t('theme.toLight') : t('theme.toDark')}>
              {theme === 'dark' ? <Sun size={18} /> : <Moon size={18} />}
            </button>
            <div className="status-indicator online" title="控制台与后端的实时连接状态">
              <span className="status-dot" />
              {t('common.online')}
            </div>
          </div>
        </header>
        <div className="content">{children}</div>
      </main>
    </div>
  )
}
