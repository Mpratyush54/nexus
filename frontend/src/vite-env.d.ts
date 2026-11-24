/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE: string
  readonly VITE_WS_URL: string
  readonly VITE_APP_VERSION?: string
  readonly VITE_DESKTOP_RELAY?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
