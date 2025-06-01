// https://nuxt.com/docs/api/configuration/nuxt-config
import tailwindcss from '@tailwindcss/vite'
export default defineNuxtConfig({
  compatibilityDate: '2024-11-01',
  devtools: { enabled: true },
  css: ['~/assets/css/tailwind.css'],
  vite: {
    plugins: [
      tailwindcss(),
    ],
  },
  devServer: {
    port: 8081,
    host: '0.0.0.0'
  },
  runtimeConfig: {
    public: {
      apiBase: 'http://localhost:8000',
    }
  },
  // content: {
  //   wsUrl: 'ws://0.0.0.0:4000/ws',
  //   experimental: {
  //     clientDB: true,
  //     stripQueryParameters: true
  //   }
  // },
  modules: [// '@nuxt/content',
  '@nuxt/eslint', '@nuxt/fonts', '@nuxt/icon', '@nuxt/image', '@nuxt/scripts', '@nuxt/test-utils', '@nuxtjs/color-mode', 'shadcn-nuxt', '@nuxtjs/mdc'],
  colorMode: {
    classSuffix: '',
    storageKey: 'theme',
  },
  shadcn: {
    /**
     * Prefix for all the imported component
     */
    prefix: '',
    /**
     * Directory that the component lives in.
     * @default "./components/ui"
     */
    componentDir: './components/ui'
  }
})