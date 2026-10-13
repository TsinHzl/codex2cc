import { useEffect, useId, useRef, useState } from 'react'
import { Moon, Sun } from 'lucide-react'
import { motion, useReducedMotion } from 'framer-motion'
import { useTranslation } from 'react-i18next'
import { useTheme } from '@/hooks/useTheme'
import { cn } from '@/lib/utils'

interface Particle {
  id: number
  delay: number
  duration: number
}

export type CinematicThemeSwitcherSize = 'default' | 'compact' | 'mini'

const TRACK_WIDTH = 104
const TRACK_HEIGHT = 64
const THUMB_TRAVEL = 46
const SIZE_SCALE: Record<CinematicThemeSwitcherSize, number> = {
  default: 1,
  compact: 44 / TRACK_HEIGHT,
  mini: 32 / TRACK_HEIGHT,
}

export interface CinematicThemeSwitcherProps {
  size?: CinematicThemeSwitcherSize
  className?: string
}

export function CinematicThemeSwitcher({
  size = 'default',
  className,
}: CinematicThemeSwitcherProps) {
  const { t } = useTranslation()
  const { theme, setTheme } = useTheme()
  const reduceMotion = useReducedMotion()
  const reactId = useId().replace(/:/g, '')
  const grainLightId = `grain-light-${reactId}`
  const grainDarkId = `grain-dark-${reactId}`

  const [particles, setParticles] = useState<Particle[]>([])
  const [isAnimating, setIsAnimating] = useState(false)
  const toggleRef = useRef<HTMLButtonElement>(null)
  const particleTimerRef = useRef<number | null>(null)

  const isDark = theme === 'dark'
  const scale = SIZE_SCALE[size]
  const label = isDark ? t('common.switchToLight') : t('common.switchToDark')

  useEffect(() => {
    return () => {
      if (particleTimerRef.current !== null) {
        window.clearTimeout(particleTimerRef.current)
      }
    }
  }, [])

  const generateParticles = () => {
    if (reduceMotion) return

    const nextParticles: Particle[] = []
    for (let i = 0; i < 3; i++) {
      nextParticles.push({
        id: i,
        delay: i * 0.1,
        duration: 0.6 + i * 0.1,
      })
    }

    setParticles(nextParticles)
    setIsAnimating(true)

    if (particleTimerRef.current !== null) {
      window.clearTimeout(particleTimerRef.current)
    }
    particleTimerRef.current = window.setTimeout(() => {
      setIsAnimating(false)
      setParticles([])
      particleTimerRef.current = null
    }, 1000)
  }

  const handleToggle = (event: React.MouseEvent<HTMLButtonElement>) => {
    generateParticles()
    setTheme(isDark ? 'light' : 'dark', event)
  }

  return (
    <div
      className={cn('relative inline-block', className)}
      style={{ width: TRACK_WIDTH * scale, height: TRACK_HEIGHT * scale }}
    >
      <svg className="absolute h-0 w-0" aria-hidden="true">
        <defs>
          <filter id={grainLightId}>
            <feTurbulence type="fractalNoise" baseFrequency="0.9" numOctaves="4" result="noise" />
            <feColorMatrix in="noise" type="saturate" values="0" result="desaturatedNoise" />
            <feComponentTransfer in="desaturatedNoise" result="lightGrain">
              <feFuncA type="linear" slope="0.3" />
            </feComponentTransfer>
            <feBlend in="SourceGraphic" in2="lightGrain" mode="overlay" />
          </filter>
          <filter id={grainDarkId}>
            <feTurbulence type="fractalNoise" baseFrequency="0.9" numOctaves="4" result="noise" />
            <feColorMatrix in="noise" type="saturate" values="0" result="desaturatedNoise" />
            <feComponentTransfer in="desaturatedNoise" result="darkGrain">
              <feFuncA type="linear" slope="0.5" />
            </feComponentTransfer>
            <feBlend in="SourceGraphic" in2="darkGrain" mode="overlay" />
          </filter>
        </defs>
      </svg>

      <div
        className="absolute left-1/2 top-1/2"
        style={{
          width: TRACK_WIDTH,
          height: TRACK_HEIGHT,
          transform: `translate(-50%, -50%) scale(${scale})`,
        }}
      >
        <motion.button
          ref={toggleRef}
          type="button"
          onClick={handleToggle}
          className="relative flex h-[64px] w-[104px] cursor-pointer items-center rounded-full p-[6px] transition-transform duration-300 touch-manipulation focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/70 focus-visible:ring-offset-2 focus-visible:ring-offset-background"
          style={{
            background: isDark
              ? 'radial-gradient(ellipse at top left, #1e293b 0%, #0f172a 40%, #020617 100%)'
              : 'radial-gradient(ellipse at top left, #ffffff 0%, #f1f5f9 40%, #cbd5e1 100%)',
            boxShadow: isDark
              ? `
                inset 5px 5px 12px rgba(0, 0, 0, 0.9),
                inset -5px -5px 12px rgba(71, 85, 105, 0.4),
                inset 8px 8px 16px rgba(0, 0, 0, 0.7),
                inset -8px -8px 16px rgba(100, 116, 139, 0.2),
                inset 0 2px 4px rgba(0, 0, 0, 1),
                inset 0 -2px 4px rgba(71, 85, 105, 0.4),
                inset 0 0 20px rgba(0, 0, 0, 0.6),
                0 1px 1px rgba(255, 255, 255, 0.05),
                0 2px 4px rgba(0, 0, 0, 0.4),
                0 8px 16px rgba(0, 0, 0, 0.4),
                0 16px 32px rgba(0, 0, 0, 0.3),
                0 24px 48px rgba(0, 0, 0, 0.2)
              `
              : `
                inset 5px 5px 12px rgba(148, 163, 184, 0.5),
                inset -5px -5px 12px rgba(255, 255, 255, 1),
                inset 8px 8px 16px rgba(100, 116, 139, 0.3),
                inset -8px -8px 16px rgba(255, 255, 255, 0.9),
                inset 0 2px 4px rgba(148, 163, 184, 0.4),
                inset 0 -2px 4px rgba(255, 255, 255, 1),
                inset 0 0 20px rgba(203, 213, 225, 0.3),
                0 1px 2px rgba(255, 255, 255, 1),
                0 2px 4px rgba(0, 0, 0, 0.1),
                0 8px 16px rgba(0, 0, 0, 0.08),
                0 16px 32px rgba(0, 0, 0, 0.06),
                0 24px 48px rgba(0, 0, 0, 0.04)
              `,
            border: isDark
              ? '2px solid rgba(51, 65, 85, 0.6)'
              : '2px solid rgba(203, 213, 225, 0.6)',
          }}
          title={label}
          aria-label={label}
          role="switch"
          aria-checked={isDark}
          whileTap={reduceMotion ? undefined : { scale: 0.98 }}
        >
          <div
            className="pointer-events-none absolute inset-[3px] rounded-full"
            style={{
              boxShadow: isDark
                ? 'inset 0 2px 6px rgba(0, 0, 0, 0.9), inset 0 -1px 3px rgba(71, 85, 105, 0.3)'
                : 'inset 0 2px 6px rgba(100, 116, 139, 0.4), inset 0 -1px 3px rgba(255, 255, 255, 0.8)',
            }}
          />

          <div
            className="pointer-events-none absolute inset-0 rounded-full"
            style={{
              background: isDark
                ? `
                  radial-gradient(ellipse at top, rgba(71, 85, 105, 0.15) 0%, transparent 50%),
                  linear-gradient(to bottom, rgba(71, 85, 105, 0.2) 0%, transparent 30%, transparent 70%, rgba(0, 0, 0, 0.3) 100%)
                `
                : `
                  radial-gradient(ellipse at top, rgba(255, 255, 255, 0.8) 0%, transparent 50%),
                  linear-gradient(to bottom, rgba(255, 255, 255, 0.7) 0%, transparent 30%, transparent 70%, rgba(148, 163, 184, 0.15) 100%)
                `,
              mixBlendMode: 'overlay',
            }}
          />

          <div
            className="pointer-events-none absolute inset-0 rounded-full"
            style={{
              boxShadow: isDark
                ? 'inset 0 0 15px rgba(0, 0, 0, 0.5)'
                : 'inset 0 0 15px rgba(148, 163, 184, 0.2)',
            }}
          />

          <div className="absolute inset-0 flex items-center justify-between px-4">
            <Sun size={20} className={isDark ? 'text-yellow-100' : 'text-amber-600'} aria-hidden="true" />
            <Moon size={20} className={isDark ? 'text-yellow-100' : 'text-slate-700'} aria-hidden="true" />
          </div>

          <motion.div
            className="relative z-10 flex h-[44px] w-[44px] items-center justify-center overflow-hidden rounded-full"
            style={{
              background: isDark
                ? 'linear-gradient(145deg, #64748b 0%, #475569 50%, #334155 100%)'
                : 'linear-gradient(145deg, #ffffff 0%, #fefefe 50%, #f8fafc 100%)',
              boxShadow: isDark
                ? `
                  inset 2px 2px 4px rgba(100, 116, 139, 0.4),
                  inset -2px -2px 4px rgba(0, 0, 0, 0.8),
                  inset 0 1px 1px rgba(255, 255, 255, 0.15),
                  0 1px 2px rgba(255, 255, 255, 0.1),
                  0 8px 32px rgba(0, 0, 0, 0.6),
                  0 4px 12px rgba(0, 0, 0, 0.5),
                  0 2px 4px rgba(0, 0, 0, 0.4)
                `
                : `
                  inset 2px 2px 4px rgba(203, 213, 225, 0.3),
                  inset -2px -2px 4px rgba(255, 255, 255, 1),
                  inset 0 1px 2px rgba(255, 255, 255, 1),
                  0 1px 2px rgba(255, 255, 255, 1),
                  0 8px 32px rgba(0, 0, 0, 0.18),
                  0 4px 12px rgba(0, 0, 0, 0.12),
                  0 2px 4px rgba(0, 0, 0, 0.08)
                `,
              border: isDark
                ? '2px solid rgba(148, 163, 184, 0.3)'
                : '2px solid rgba(255, 255, 255, 0.9)',
            }}
            animate={{ x: isDark ? THUMB_TRAVEL : 0 }}
            transition={
              reduceMotion
                ? { duration: 0.2 }
                : { type: 'spring', stiffness: 300, damping: 20 }
            }
          >
            <div
              className="pointer-events-none absolute inset-0 rounded-full"
              style={{
                background: 'linear-gradient(to bottom, rgba(255, 255, 255, 0.4) 0%, transparent 40%, rgba(0, 0, 0, 0.1) 100%)',
                mixBlendMode: 'overlay',
              }}
            />

            {isAnimating && particles.map((particle) => (
              <motion.div
                key={particle.id}
                className="pointer-events-none absolute inset-0 flex items-center justify-center"
              >
                <motion.div
                  className="absolute rounded-full"
                  style={{
                    width: 10,
                    height: 10,
                    background: isDark
                      ? 'radial-gradient(circle, rgba(147, 197, 253, 0.5) 0%, rgba(147, 197, 253, 0) 70%)'
                      : 'radial-gradient(circle, rgba(251, 191, 36, 0.7) 0%, rgba(251, 191, 36, 0) 70%)',
                  }}
                  initial={{ scale: 0, opacity: 0 }}
                  animate={{ scale: isDark ? 6 : 8, opacity: [0, 1, 0] }}
                  transition={{
                    duration: isDark ? 0.5 : particle.duration,
                    delay: particle.delay,
                    ease: 'easeOut',
                  }}
                >
                  <div
                    className="absolute inset-0 rounded-full opacity-40"
                    style={{
                      backgroundImage: `url("data:image/svg+xml,%3Csvg viewBox='0 0 200 200' xmlns='http://www.w3.org/2000/svg'%3E%3Cfilter id='noiseFilter'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.9' numOctaves='4' stitchTiles='stitch'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23noiseFilter)'/%3E%3C/svg%3E")`,
                      mixBlendMode: 'overlay',
                    }}
                  />
                </motion.div>
              </motion.div>
            ))}

            <div className="relative z-10">
              {isDark ? (
                <Moon size={20} className="text-yellow-200" aria-hidden="true" />
              ) : (
                <Sun size={20} className="text-amber-500" aria-hidden="true" />
              )}
            </div>
          </motion.div>
        </motion.button>
      </div>
    </div>
  )
}

export default CinematicThemeSwitcher
