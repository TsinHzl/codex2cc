import { useState } from 'react'
import { Check, CheckCheck, Globe, Monitor, Moon, Palette, RotateCcw, Search, SlidersHorizontal, Sparkles, Sun, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import PageHeader from '../components/PageHeader'
import { Button } from '@/components/ui/button'
import { CinematicThemeSwitcher } from '@/components/ui/cinematic-theme-switcher'
import { AppearanceThumbnail, ThemeLivePreview, ThemeThumbnail } from '../components/theme/ThemePreview'
import {
  COLOR_THEMES,
  getThemePreviewSwatch,
  type ColorThemeDef,
  type Theme,
  type ThemeGroup,
  useTheme,
} from '../hooks/useTheme'
import './theme-settings.css'

const MODE_OPTIONS = [
  { id: 'light', icon: Sun, labelKey: 'themeSettings.modeLight' },
  { id: 'dark', icon: Moon, labelKey: 'themeSettings.modeDark' },
  { id: 'system', icon: Monitor, labelKey: 'themeSettings.modeSystem' },
] as const

const GROUP_FILTERS = [
  { id: 'all', labelKey: 'themeSettings.filterAll' },
  { id: 'recommended', labelKey: 'themeSettings.filterRecommended' },
  { id: 'website', labelKey: 'themeSettings.filterWebsite' },
  { id: 'light', labelKey: 'themeSettings.filterLight' },
  { id: 'dark', labelKey: 'themeSettings.filterDark' },
  { id: 'editor', labelKey: 'themeSettings.filterEditor' },
] as const

function matchesGroup(item: ColorThemeDef, group: 'all' | ThemeGroup) {
  return group === 'all' || (group === 'recommended' ? item.recommended : item.group === group)
}

function ThemeStyleCard({ item, active, resolvedMode, onSelect }: {
  item: ColorThemeDef
  active: boolean
  resolvedMode: Theme
  onSelect: () => void
}) {
  const { t } = useTranslation()
  const swatch = getThemePreviewSwatch(item, resolvedMode)

  return (
    <button
      type="button"
      aria-pressed={active}
      aria-label={t('themeSettings.selectTheme', { theme: t(item.nameKey) })}
      aria-describedby={`theme-description-${item.id}`}
      onClick={onSelect}
      className="appearance-theme-card"
      data-theme-id={item.id}
    >
      <div className="appearance-theme-art">
        <ThemeThumbnail swatch={swatch} uiStyle={item.uiStyle} />
        {item.recommended && (
          <span className="appearance-recommendation">
            <Sparkles size={10} aria-hidden="true" />
            {t('themeSettings.recommendedBadge')}
          </span>
        )}
        {active && <span className="appearance-selected-mark"><Check size={14} aria-hidden="true" /></span>}
      </div>
      <span className="appearance-theme-info">
        <span className="appearance-theme-name">
          <span>{t(item.nameKey)}</span>
          <span className="appearance-color-dots" aria-hidden="true">
            {[swatch.primary, swatch.bg, swatch.muted].map((color, index) => <i key={index} style={{ backgroundColor: color }} />)}
          </span>
        </span>
        <span className="appearance-theme-description" id={`theme-description-${item.id}`}>{t(item.descriptionKey)}</span>
        <span className="appearance-theme-meta">
          <span>{t(`themeSettings.group.${item.group}`)}</span>
          <span className={active ? 'appearance-active-label' : ''}>
            {active ? <><Check size={12} aria-hidden="true" />{t('themeSettings.applied')}</> : <><Sun size={11} aria-hidden="true" /><Moon size={11} aria-hidden="true" /><span className="sr-only">{t('themeSettings.bothModes')}</span></>}
          </span>
        </span>
      </span>
    </button>
  )
}

export default function ThemeSettings() {
  const { t } = useTranslation()
  const { theme, themeMode, setThemeMode, colorTheme, setColorTheme, resetTheme } = useTheme()
  const [groupFilter, setGroupFilter] = useState<'all' | ThemeGroup>('all')
  const [search, setSearch] = useState('')

  const activeColorTheme = COLOR_THEMES.find((item) => item.id === colorTheme) ?? COLOR_THEMES[0]
  const activeMode = MODE_OPTIONS.find((item) => item.id === themeMode) ?? MODE_OPTIONS[2]
  const normalizedSearch = search.trim().toLocaleLowerCase()
  const searchedThemes = COLOR_THEMES.filter((item) => (
    [item.id, t(item.nameKey), t(item.descriptionKey), t(`themeSettings.group.${item.group}`)]
      .some((value) => value.toLocaleLowerCase().includes(normalizedSearch))
  ))
  const filteredThemes = searchedThemes.filter((item) => matchesGroup(item, groupFilter))

  const clearFilters = () => {
    setSearch('')
    setGroupFilter('all')
  }

  const handleReset = () => {
    resetTheme()
    clearFilters()
  }

  return (
    <div className="theme-settings-page">
      <PageHeader
        title={t('themeSettings.title')}
        description={t('themeSettings.description')}
        titleAdornment={<span className="appearance-header-badge"><Palette size={12} aria-hidden="true" />{t('themeSettings.themeCount', { count: COLOR_THEMES.length })}</span>}
        actions={(
          <>
            <span className="appearance-save-note"><CheckCheck size={15} aria-hidden="true" />{t('themeSettings.autoSave')}</span>
            <Button variant="outline" size="sm" onClick={handleReset} title={t('themeSettings.resetDesc')}>
              <RotateCcw className="size-3.5" />
              {t('themeSettings.reset')}
            </Button>
          </>
        )}
      />

      <section className="appearance-workspace" aria-label={t('themeSettings.currentAppearance')}>
        <div className="appearance-personalize">
          <div className="appearance-eyebrow"><span />{t('themeSettings.currentAppearance')}</div>
          <div className="appearance-current">
            <span className="appearance-current-icon"><Palette size={24} strokeWidth={1.5} aria-hidden="true" /></span>
            <div>
              <h3>{t(activeColorTheme.nameKey)}</h3>
              <p>{t(activeColorTheme.descriptionKey)}</p>
              {activeColorTheme.uiStyle && (
                <div className="appearance-ui-features" role="group" aria-label={t('themeSettings.uiFeatures')}>
                  <span>{t(`themeSettings.websiteUI.${activeColorTheme.uiStyle}.surface`)}</span>
                  <span>{t(`themeSettings.websiteUI.${activeColorTheme.uiStyle}.controls`)}</span>
                </div>
              )}
            </div>
          </div>
          <div className="appearance-mode-heading">
            <h3 id="appearance-mode-label">{t('themeSettings.modeTitle')}</h3>
            <span>{t('themeSettings.modeHint')}</span>
          </div>
          <div className="appearance-mode-switcher">
            <CinematicThemeSwitcher />
          </div>
          <div className="appearance-modes" role="group" aria-labelledby="appearance-mode-label">
            {MODE_OPTIONS.map(({ id, icon: Icon, labelKey }) => (
              <button
                key={id}
                type="button"
                aria-pressed={themeMode === id}
                onClick={(event) => setThemeMode(id, event)}
                className="appearance-mode"
              >
                <AppearanceThumbnail mode={id} />
                <span><Icon size={13} aria-hidden="true" />{t(labelKey)}</span>
                {themeMode === id && <i><Check size={10} aria-hidden="true" /></i>}
              </button>
            ))}
          </div>
          <p className="appearance-mode-explanation">
            <activeMode.icon size={14} aria-hidden="true" />
            {themeMode === 'system'
              ? t('themeSettings.systemResolved', { mode: t(theme === 'dark' ? 'themeSettings.modeDark' : 'themeSettings.modeLight') })
              : t(themeMode === 'dark' ? 'themeSettings.modeDarkDesc' : 'themeSettings.modeLightDesc')}
          </p>
          <div className="appearance-local-note"><Monitor size={15} aria-hidden="true" /><span>{t('themeSettings.localPreference')}</span></div>
        </div>

        <ThemeLivePreview item={activeColorTheme} mode={theme} />
      </section>

      <section className="appearance-library" aria-labelledby="appearance-library-title">
        <div className="appearance-library-heading">
          <div>
            <h3 id="appearance-library-title">{t('themeSettings.stylesTitle')}<span>{COLOR_THEMES.length}</span></h3>
            <p>{t('themeSettings.stylesDesc')}</p>
          </div>
          <div className="appearance-search">
            <Search size={16} aria-hidden="true" />
            <input
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('themeSettings.searchPlaceholder')}
              aria-label={t('themeSettings.searchLabel')}
            />
            {search && <button type="button" onClick={() => setSearch('')} aria-label={t('themeSettings.clearSearch')}><X size={14} /></button>}
          </div>
        </div>
        <div className="appearance-library-toolbar">
          <div className="appearance-filters" role="group" aria-label={t('themeSettings.filterLabel')}>
            {GROUP_FILTERS.map((filter) => (
              <button key={filter.id} type="button" aria-pressed={groupFilter === filter.id} onClick={() => setGroupFilter(filter.id)}>
                {filter.id === 'recommended' && <Sparkles size={12} aria-hidden="true" />}
                {filter.id === 'website' && <Globe size={12} aria-hidden="true" />}
                {t(filter.labelKey)}
                <span>{searchedThemes.filter((item) => matchesGroup(item, filter.id)).length}</span>
              </button>
            ))}
          </div>
          <span className="appearance-results" role="status">{t('themeSettings.resultsCount', { count: filteredThemes.length })}</span>
        </div>
        {groupFilter === 'website' && (
          <p className="appearance-website-note"><Globe size={15} aria-hidden="true" />{t('themeSettings.websiteHint')}</p>
        )}
        {filteredThemes.length > 0 ? (
          <div className="appearance-theme-grid" role="group" aria-label={t('themeSettings.stylesTitle')}>
            {filteredThemes.map((item) => (
              <ThemeStyleCard key={item.id} item={item} active={item.id === colorTheme} resolvedMode={theme} onSelect={() => setColorTheme(item.id)} />
            ))}
          </div>
        ) : (
          <div className="appearance-empty">
            <span><Search size={24} strokeWidth={1.5} aria-hidden="true" /></span>
            <h4>{t('themeSettings.emptyFilter')}</h4>
            <p>{t('themeSettings.emptyHint')}</p>
            <Button variant="outline" size="sm" onClick={clearFilters}>{t('themeSettings.clearFilters')}</Button>
          </div>
        )}
        <p className="appearance-library-note"><SlidersHorizontal size={13} aria-hidden="true" />{t('themeSettings.libraryHint')}</p>
      </section>
      <span className="sr-only" role="status">{t('themeSettings.currentThemeFull', { theme: t(activeColorTheme.nameKey), mode: t(activeMode.labelKey) })}</span>
    </div>
  )
}
