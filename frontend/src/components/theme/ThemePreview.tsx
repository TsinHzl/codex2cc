import { useState, type CSSProperties } from 'react'
import { Activity, ArrowUpRight, Check, ChevronDown, Circle, Command, KeyRound, LayoutDashboard, MoreHorizontal, Plus, Settings2, SlidersHorizontal, Users, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { getThemePreviewSwatch, type ColorThemeDef, type Theme, type ThemeMode, type ThemePreviewSwatch, type WebsiteThemeStyle } from '../../hooks/useTheme'

const BARS = [28, 43, 35, 55, 42, 66, 52, 70, 57, 76, 65, 90, 73, 86, 68, 96, 82, 92]

export function AppearanceThumbnail({ mode }: { mode: ThemeMode }) {
  return (
    <span className="appearance-mode-art" data-mode={mode} aria-hidden="true">
      <span className="appearance-mode-art-sidebar"><i /><i /><i /></span>
      <span className="appearance-mode-art-body"><i /><span><i /><i /></span><i /></span>
    </span>
  )
}

export function ThemeThumbnail({ swatch, uiStyle }: { swatch: ThemePreviewSwatch; uiStyle?: WebsiteThemeStyle }) {
  const style = {
    '--thumb-primary': swatch.primary,
    '--thumb-bg': swatch.bg,
    '--thumb-surface': swatch.surface,
    '--thumb-muted': swatch.muted,
    '--thumb-sidebar': swatch.sidebar ?? swatch.surface,
  } as CSSProperties

  return (
    <span className="appearance-thumbnail" style={style} data-ui={uiStyle ?? 'default'} aria-hidden="true">
      <span className="appearance-thumb-window">
        <span className="appearance-thumb-sidebar">
          <span className="appearance-thumb-logo"><Command size={10} /></span>
          <i /><i /><i />
          <span className="appearance-thumb-avatar" />
        </span>
        <span className="appearance-thumb-body">
          <span className="appearance-thumb-heading"><i /><i /></span>
          <span className="appearance-thumb-stats"><i /><i /><i /></span>
          <span className="appearance-thumb-chart">
            {BARS.filter((_, index) => index % 2 === 0).map((height, index) => <i key={index} style={{ height: `${height}%` }} />)}
          </span>
          <span className="appearance-thumb-row"><i /><i /></span>
        </span>
      </span>
    </span>
  )
}

function WorkbenchPreview() {
  const { t } = useTranslation()
  return (
    <div className="appearance-demo-workbench" aria-hidden="true">
      <aside className="appearance-demo-sidebar">
        <span className="appearance-demo-brand"><Command size={19} /><b>Codex<span>2API</span></b></span>
        <span className="appearance-demo-nav is-active"><LayoutDashboard size={14} /><span>{t('themeSettings.demo.overview')}</span></span>
        <span className="appearance-demo-nav"><Users size={14} /><span>{t('themeSettings.demo.accounts')}</span></span>
        <span className="appearance-demo-nav"><KeyRound size={14} /><span>{t('themeSettings.demo.keys')}</span></span>
        <span className="appearance-demo-nav"><Activity size={14} /><span>{t('themeSettings.demo.usage')}</span></span>
        <span className="appearance-demo-user"><span>C</span><span>Workspace<small>Personal</small></span></span>
      </aside>
      <div className="appearance-demo-main">
        <div className="appearance-demo-heading">
          <div><span className="appearance-demo-breadcrumb">Workspace / {t('themeSettings.demo.overview')}</span><h4>{t('themeSettings.demo.workspace')}</h4></div>
          <span className="appearance-demo-action"><Plus size={12} />{t('themeSettings.demo.create')}</span>
        </div>
        <div className="appearance-demo-metrics">
          <div><span>{t('themeSettings.demo.requests')}</span><strong>24,680<Zap size={14} /></strong><small><ArrowUpRight size={11} />12.8%</small></div>
          <div><span>{t('themeSettings.demo.successRate')}</span><strong>99.9<em>%</em></strong><small><span className="appearance-status-dot" />{t('themeSettings.demo.healthy')}</small></div>
          <div><span>{t('themeSettings.demo.latency')}</span><strong>128<em>ms</em></strong><small>{t('themeSettings.demo.stable')}</small></div>
        </div>
        <div className="appearance-demo-chart">
          <div><b>{t('themeSettings.demo.traffic')}</b><span>{t('themeSettings.demo.week')}<ChevronDown size={10} /></span></div>
          <div className="appearance-chart-bars">{BARS.map((height, index) => <i key={index} style={{ height: `${height}%`, opacity: 0.35 + index / 28 }} />)}</div>
          <div className="appearance-chart-axis"><span>00:00</span><span>06:00</span><span>12:00</span><span>18:00</span><span>24:00</span></div>
        </div>
        <div className="appearance-demo-request"><span><i /><b>POST</b> /v1/responses</span><span className="appearance-demo-success">200 OK</span><MoreHorizontal size={13} /></div>
      </div>
    </div>
  )
}

function ComponentsPreview() {
  const { t } = useTranslation()
  return (
    <div className="appearance-demo-components" aria-hidden="true">
      <div className="appearance-component-heading"><span><SlidersHorizontal size={16} />{t('themeSettings.demo.componentsTitle')}</span><small>Aa</small></div>
      <div className="appearance-component-grid">
        <div>
          <span className="appearance-component-label">{t('themeSettings.demo.actions')}</span>
          <div className="appearance-component-actions"><span className="appearance-demo-action"><Plus size={12} />{t('themeSettings.demo.primary')}</span><span className="appearance-demo-outline">{t('themeSettings.demo.secondary')}</span></div>
          <span className="appearance-component-label">{t('themeSettings.demo.input')}</span>
          <span className="appearance-demo-input"><KeyRound size={13} /><span>Production API</span><Check size={13} /></span>
          <span className="appearance-component-label">{t('themeSettings.demo.selection')}</span>
          <span className="appearance-demo-input"><span>{t('themeSettings.demo.defaultGroup')}</span><ChevronDown size={13} /></span>
        </div>
        <div>
          <span className="appearance-component-label">{t('themeSettings.demo.status')}</span>
          <div className="appearance-component-badges"><span className="appearance-demo-success">{t('themeSettings.demo.enabled')}</span><span className="appearance-demo-pending">{t('themeSettings.demo.pending')}</span><span className="appearance-demo-error">{t('themeSettings.demo.error')}</span></div>
          <span className="appearance-component-label">{t('themeSettings.demo.controls')}</span>
          <div className="appearance-demo-controls"><span className="appearance-demo-toggle" /><span className="appearance-demo-checkbox"><Check size={11} /></span><Circle size={16} /></div>
          <span className="appearance-component-label">{t('themeSettings.demo.quota')}</span>
          <span className="appearance-demo-progress"><i /></span>
          <span className="appearance-demo-progress-caption">6,800 / 10,000<span>68%</span></span>
        </div>
      </div>
      <div className="appearance-demo-notice"><Check size={15} /><span>{t('themeSettings.demo.notice')}</span></div>
    </div>
  )
}

export function ThemeLivePreview({ item, mode }: { item: ColorThemeDef; mode: Theme }) {
  const { t } = useTranslation()
  const [scene, setScene] = useState<'workbench' | 'components'>('workbench')
  const swatch = getThemePreviewSwatch(item, mode)
  const tokens = [
    { label: 'primary', color: swatch.primary },
    { label: 'background', color: swatch.bg },
    { label: 'surface', color: swatch.surface },
    { label: 'muted', color: swatch.muted },
  ]
  return (
    <div className="appearance-preview">
      <div className="appearance-preview-heading">
        <h3><span />{t('themeSettings.previewTitle')}</h3>
        <div className="appearance-preview-tabs" role="group" aria-label={t('themeSettings.previewScene')}>
          <button type="button" aria-pressed={scene === 'workbench'} onClick={() => setScene('workbench')}><LayoutDashboard size={12} aria-hidden="true" />{t('themeSettings.sceneWorkbench')}</button>
          <button type="button" aria-pressed={scene === 'components'} onClick={() => setScene('components')}><Settings2 size={12} aria-hidden="true" />{t('themeSettings.sceneComponents')}</button>
        </div>
      </div>
      <div className="appearance-preview-stage">
        <div className="appearance-preview-window">
          <div className="appearance-window-chrome"><span aria-hidden="true"><i /><i /><i /></span><span>codex2api / {scene === 'workbench' ? 'workspace' : 'components'}</span><span>{t('themeSettings.demoLabel')}</span></div>
          <div role="img" aria-label={t('themeSettings.previewAccessible', { theme: t(item.nameKey), mode: t(mode === 'dark' ? 'themeSettings.modeDark' : 'themeSettings.modeLight'), scene: t(scene === 'workbench' ? 'themeSettings.sceneWorkbench' : 'themeSettings.sceneComponents') })}>
            {scene === 'workbench' ? <WorkbenchPreview /> : <ComponentsPreview />}
          </div>
        </div>
      </div>
      <div className="appearance-palette" role="group" aria-label={t('themeSettings.paletteLabel')}>
        {tokens.map(({ label, color }) => <span key={label}><i style={{ backgroundColor: color }} />{t(`themeSettings.token.${label}`)}</span>)}
        <span className="appearance-preview-caption">{t('themeSettings.previewDesc')}</span>
      </div>
    </div>
  )
}
