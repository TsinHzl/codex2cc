import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { MouseEvent, ReactNode } from 'react'

/** Resolved appearance actually applied to the document. */
export type Theme = 'light' | 'dark'
/** User preference: fixed light/dark, or follow OS. */
export type ThemeMode = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'theme'
const DEFAULT_THEME_MODE: ThemeMode = 'system'

function isThemeMode(value: string | null): value is ThemeMode {
  return value === 'light' || value === 'dark' || value === 'system'
}

function getSystemTheme(): Theme {
  if (typeof window === 'undefined') return 'light'
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function resolveTheme(mode: ThemeMode): Theme {
  return mode === 'system' ? getSystemTheme() : mode
}

function getInitialThemeMode(): ThemeMode {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (isThemeMode(stored)) return stored
    // Legacy: only light/dark was stored — treat as explicit choice.
    if (stored === 'light' || stored === 'dark') return stored
    // Legacy: 旧版颜色主题的持久化键，一次性清除。
    localStorage.removeItem('color-theme')
  } catch {
    /* localStorage unavailable (private mode / blocked) */
  }
  return DEFAULT_THEME_MODE
}

function applyResolvedTheme(nextTheme: Theme) {
  const root = document.documentElement
  // 历史遗留：清除旧版颜色主题持久化到 <html> 上的 theme-* 类（排除切换动画用的 theme-switching）。
  root.className = root.className.replace(/\b(?!theme-switching\b)theme-[a-z0-9-]+\b/g, '').trim()
  if (nextTheme === 'dark') {
    root.classList.add('dark')
  } else {
    root.classList.remove('dark')
  }
}

function persistThemeMode(mode: ThemeMode) {
  try {
    localStorage.setItem(STORAGE_KEY, mode)
  } catch {
    /* localStorage unavailable (private mode / blocked) */
  }
  applyResolvedTheme(resolveTheme(mode))
}

interface ThemeContextValue {
  /** Resolved light/dark currently applied. */
  theme: Theme
  /** User preference including system. */
  themeMode: ThemeMode
  setTheme: (next: Theme, e?: MouseEvent) => void
  setThemeMode: (next: ThemeMode, e?: MouseEvent) => void
  toggle: (e?: MouseEvent) => void
  resetTheme: () => void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

function runThemeTransition(e: MouseEvent | undefined, apply: () => void) {
  const root = document.documentElement
  const x = e?.clientX ?? 40
  const y = e?.clientY ?? window.innerHeight - 40
  const endRadius = Math.hypot(
    Math.max(x, window.innerWidth - x),
    Math.max(y, window.innerHeight - y),
  )

  if (document.startViewTransition) {
    // 冻结元素级颜色过渡：View Transition 的"新画面"是活文档,若放任全页
    // 几千个 200ms transition-colors 同时跑,扫过期间会逐帧重绘导致卡顿。
    root.classList.add('theme-switching')
    const transition = document.startViewTransition(() => {
      apply()
    })
    transition.ready.then(() => {
      root.animate(
        {
          clipPath: [
            `circle(0px at ${x}px ${y}px)`,
            `circle(${endRadius}px at ${x}px ${y}px)`,
          ],
        },
        {
          duration: 400,
          easing: 'ease-out',
          pseudoElement: '::view-transition-new(root)',
        },
      )
    })
    const cleanup = () => root.classList.remove('theme-switching')
    transition.finished.then(cleanup, cleanup)
  } else {
    apply()
  }
}

// Provider：把主题状态提升到全局，避免 Layout 与顶栏切换按钮各持一份导致 UI 不同步。
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [themeMode, setThemeModeState] = useState<ThemeMode>(getInitialThemeMode)
  const [theme, setThemeState] = useState<Theme>(() => resolveTheme(getInitialThemeMode()))
  useEffect(() => {
    persistThemeMode(themeMode)
    setThemeState(resolveTheme(themeMode))
  }, [themeMode])

  // Follow OS when themeMode === 'system'
  useEffect(() => {
    if (themeMode !== 'system') return
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => {
      const next = getSystemTheme()
      applyResolvedTheme(next)
      setThemeState(next)
    }
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [themeMode])

  const setThemeMode = useCallback((nextMode: ThemeMode, e?: MouseEvent) => {
    if (nextMode === themeMode) {
      // Still re-resolve system in case OS flipped while we thought we matched.
      if (nextMode === 'system') {
        const resolved = resolveTheme('system')
        if (resolved !== theme) {
          runThemeTransition(e, () => {
            applyResolvedTheme(resolved)
            setThemeState(resolved)
          })
        }
      }
      return
    }

    const nextResolved = resolveTheme(nextMode)
    const currentResolved = theme
    if (nextResolved === currentResolved) {
      localStorage.setItem(STORAGE_KEY, nextMode)
      setThemeModeState(nextMode)
      return
    }

    runThemeTransition(e, () => {
      localStorage.setItem(STORAGE_KEY, nextMode)
      applyResolvedTheme(nextResolved)
      setThemeModeState(nextMode)
      setThemeState(nextResolved)
    })
  }, [theme, themeMode])

  const setTheme = useCallback((nextTheme: Theme, e?: MouseEvent) => {
    setThemeMode(nextTheme, e)
  }, [setThemeMode])

  const toggle = useCallback((e?: MouseEvent) => {
    const nextTheme: Theme = theme === 'dark' ? 'light' : 'dark'
    setThemeMode(nextTheme, e)
  }, [setThemeMode, theme])

  const resetTheme = useCallback(() => {
    setThemeMode(DEFAULT_THEME_MODE)
  }, [setThemeMode])

  const value = useMemo<ThemeContextValue>(
    () => ({
      theme,
      themeMode,
      setTheme,
      setThemeMode,
      toggle,
      resetTheme,
    }),
    [theme, themeMode, setTheme, setThemeMode, toggle, resetTheme],
  )

  return (
    <ThemeContext.Provider value={value}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext)
  if (!ctx) {
    throw new Error('useTheme must be used within <ThemeProvider>')
  }
  return ctx
}
