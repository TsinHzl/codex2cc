import { lazy, Suspense } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import AuthGate from './components/AuthGate'
import Layout from './components/Layout'
import RouteErrorBoundary from './components/RouteErrorBoundary'
import StateShell from './components/StateShell'
import { ToastProvider } from './components/ToastProvider'
import { BrandingProvider } from './branding'
import { VisibleChannelsProvider } from './visibleChannels'
import { ThemeProvider } from './hooks/useTheme'
import Dashboard from './pages/Dashboard'

const Accounts = lazy(() => import('./pages/Accounts'))
const Operations = lazy(() => import('./pages/Operations'))
const OperationsErrors = lazy(() => import('./pages/OperationsErrors'))
const RuntimeStatus = lazy(() => import('./pages/RuntimeStatus'))
const SchedulerBoard = lazy(() => import('./pages/SchedulerBoard'))
const Settings = lazy(() => import('./pages/Settings'))
const Docs = lazy(() => import('./pages/Docs'))
const APIKeys = lazy(() => import('./pages/APIKeys'))
const Usage = lazy(() => import('./pages/Usage'))

export default function App() {
  return (
    <ThemeProvider>
      <BrandingProvider>
        <ToastProvider>
          <RouteErrorBoundary>
            <Suspense fallback={<StateShell variant="page" loading>{null}</StateShell>}>
              <Routes>
                <Route path="/*" element={<AdminApp />} />
              </Routes>
            </Suspense>
          </RouteErrorBoundary>
        </ToastProvider>
      </BrandingProvider>
    </ThemeProvider>
  )
}

function AdminApp() {
  return (
    <AuthGate>
      <VisibleChannelsProvider>
      <Layout>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/accounts" element={<Accounts />} />
          <Route path="/api-keys" element={<APIKeys />} />
          <Route path="/ops" element={<Navigate to="/ops/overview" replace />} />
          <Route path="/ops/overview" element={<Operations />} />
          <Route path="/ops/runtime" element={<RuntimeStatus />} />
          <Route path="/ops/errors" element={<OperationsErrors />} />
          <Route path="/ops/scheduler" element={<SchedulerBoard />} />
          <Route path="/usage" element={<Usage />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="/docs" element={<Docs />} />
          <Route path="/guide" element={<Navigate to="/docs" replace />} />
          <Route path="/api-reference" element={<Navigate to="/docs#model-api" replace />} />
        </Routes>
      </Layout>
      </VisibleChannelsProvider>
    </AuthGate>
  )
}
