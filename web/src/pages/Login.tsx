import { useState } from 'react'
import { Loader2, Lock, Shell, User } from 'lucide-react'
import { useAuthStore } from '../stores/auth'
import { authApi } from '../api'
import { useI18n } from '../i18n'
import { Callout, Card, Field } from '../components/ui'
import './Login.css'

export function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const login = useAuthStore((state) => state.login)
  const { t, lang, setLang } = useI18n()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      const response = await authApi.login(username, password)
      if (response.data.token) {
        login(username, response.data.token)
        localStorage.setItem('toshell-token', response.data.token)
        window.location.href = '/dashboard'
      }
    } catch (err: any) {
      if (err.response?.status === 401) {
        setError(t('login.errBadCreds'))
      } else {
        setError(t('login.errConn'))
      }
    } finally {
      setLoading(false)
    }
  }

  // 标题区不暴露用途（不提 C2 / 免杀等字样），沿用原有的简短中英文案风格。
  // 说明：i18n.ts 的 login.subtitle（'C2 命令控制平台'）不在本次改动范围内，
  // 故此处用中性描述，等该键更新后可换回 t('login.subtitle')。
  const subtitle = lang === 'zh' ? '自托管远程管理控制台' : 'Self-hosted remote management console'
  const errorTitle = lang === 'zh' ? '登录失败' : 'Sign-in failed'

  return (
    <div className="login-page">
      <div className="login-bg">
        <div className="login-bg-gradient" />
        <div className="login-bg-grid" />
      </div>

      <div className="login-container">
        <Card className="login-card">
          <div className="login-card-top">
            <button
              className="lang-toggle"
              onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')}
              title={lang === 'zh' ? 'English' : '中文'}
            >
              {lang === 'zh' ? 'EN' : '中'}
            </button>
          </div>

          <div className="login-header">
            <Shell size={44} className="login-logo" />
            <h1>ToShell</h1>
            <p>{subtitle}</p>
          </div>

          <form onSubmit={handleSubmit} className="login-form" aria-busy={loading}>
            <Field label={t('login.username')}>
              <div className="login-input">
                <User size={16} className="login-input-icon" />
                <input
                  type="text"
                  className="ui-input login-input-field"
                  placeholder={t('login.username')}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoComplete="username"
                  disabled={loading}
                  required
                />
              </div>
            </Field>

            <Field label={t('login.password')}>
              <div className="login-input">
                <Lock size={16} className="login-input-icon" />
                <input
                  type="password"
                  className="ui-input login-input-field"
                  placeholder={t('login.password')}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="current-password"
                  disabled={loading}
                  required
                />
              </div>
            </Field>

            {error && (
              <Callout tone="danger" title={errorTitle} style={{ justifyContent: 'center', textAlign: 'center' }}>
                {error}
              </Callout>
            )}

            <button type="submit" className="login-btn" disabled={loading}>
              {loading && <Loader2 size={16} className="login-spin" />}
              {loading ? t('login.submitting') : t('login.submit')}
            </button>
          </form>

          <div className="login-footer">
            <span>{t('login.footer')}</span>
          </div>
        </Card>
      </div>
    </div>
  )
}
