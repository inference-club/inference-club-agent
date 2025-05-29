<template>
  <div class="min-h-screen bg-gradient-to-b from-background to-background/80 flex flex-col">
    <header class="sticky top-0 z-50 w-full border-b bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60">
      <div class="container mx-auto px-4 sm:px-6 lg:px-8">
        <div class="flex h-16 items-center justify-between">
          <div class="flex items-center">
            <NuxtLink to="/" class="flex items-center space-x-2">
              <span class="font-bold text-lg sm:text-xl bg-gradient-to-r from-purple-600 to-pink-600 bg-clip-text text-transparent">
                Inference Club Agent
              </span>
            </NuxtLink>
          </div>

          <!-- Mobile menu button -->
          <Button variant="ghost" size="icon" class="md:hidden" @click="isMenuOpen = !isMenuOpen">
            <MenuIcon v-if="!isMenuOpen" class="h-5 w-5" />
            <XIcon v-else class="h-5 w-5" />
          </Button>

          <!-- Desktop navigation -->
          <nav class="hidden md:flex items-center space-x-4 lg:space-x-6">
            <NuxtLink
v-for="item in navItems" :key="item.to"
                      :to="item.to"
                      class="text-sm font-medium transition-colors hover:text-foreground/80 text-foreground/60">
              {{ item.label }}
            </NuxtLink>
            <button
              class="ml-4 px-2 py-1 rounded border border-border bg-background hover:bg-accent transition-colors"
              :aria-label="colorMode.preference === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'"
              @click="colorMode.preference = colorMode.preference === 'dark' ? 'light' : 'dark'"
            >
              <span v-if="colorMode.value === 'dark'">🌙</span>
              <span v-else>☀️</span>
            </button>
          </nav>
        </div>

        <!-- Mobile navigation -->
        <div v-show="isMenuOpen" class="md:hidden py-4 space-y-2">
          <NuxtLink
v-for="item in navItems" :key="item.to"
                    :to="item.to"
                    class="block px-3 py-2 text-sm font-medium transition-colors hover:text-foreground/80 text-foreground/60"
                    @click="isMenuOpen = false">
            {{ item.label }}
          </NuxtLink>
        </div>
      </div>
    </header>
    <main class="flex-1 container mx-auto px-4 sm:px-6 lg:px-8 py-6 sm:py-8">
      <slot />
    </main>
    <SiteFooter />
  </div>
</template>

<script setup>
import { Menu as MenuIcon, X as XIcon } from 'lucide-vue-next'

const isMenuOpen = ref(false)
const colorMode = useColorMode()

const navItems = [
  { label: 'Home', to: '/' },
  { label: 'LLMs', to: '/llms' },
  { label: 'VLMs', to: '/vlms' },
  { label: 'TTS', to: '/tts' },
  { label: 'Image Gen', to: '/image-gen' },
  { label: 'Video Gen', to: '/video-gen' },
  { label: 'Mesh Gen', to: '/mesh-gen' }
]
</script>
