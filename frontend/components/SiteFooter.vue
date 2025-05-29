<template>
  <footer class="border-t bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60">
    <div class="container mx-auto px-4 sm:px-6 lg:px-8 py-12">
      <div class="grid grid-cols-1 md:grid-cols-4 gap-8">
        <!-- Brand and Description -->
        <div class="col-span-1 md:col-span-2 space-y-4">
          <div class="flex items-center space-x-2">
            <span class="font-bold text-xl bg-gradient-to-r from-purple-600 to-pink-600 bg-clip-text text-transparent">
              Inference Club Agent
            </span>
          </div>
          <p class="text-sm text-muted-foreground max-w-md">
            Exploring the cutting edge of AI capabilities. Join us in creating the future of artificial intelligence.
          </p>
          <div class="flex space-x-4">
            <a
href="https://github.com" target="_blank" rel="noopener noreferrer"
               class="text-muted-foreground hover:text-foreground transition-colors">
              <Github class="h-5 w-5" />
            </a>
            <a
href="https://twitter.com" target="_blank" rel="noopener noreferrer"
               class="text-muted-foreground hover:text-foreground transition-colors">
              <Twitter class="h-5 w-5" />
            </a>
            <a
href="https://discord.com" target="_blank" rel="noopener noreferrer"
               class="text-muted-foreground hover:text-foreground transition-colors">
              <MessageSquare class="h-5 w-5" />
            </a>
          </div>
        </div>

        <!-- Quick Links -->
        <div class="space-y-4">
          <h3 class="text-sm font-semibold">Quick Links</h3>
          <ul class="space-y-2">
            <li v-for="item in quickLinks" :key="item.to">
              <NuxtLink
:to="item.to"
                        class="text-sm text-muted-foreground hover:text-foreground transition-colors">
                {{ item.label }}
              </NuxtLink>
            </li>
          </ul>
        </div>

        <!-- Resources -->
        <div class="space-y-4">
          <h3 class="text-sm font-semibold">Resources</h3>
          <ul class="space-y-2">
            <li v-for="item in resources" :key="item.to">
              <NuxtLink
:to="item.to"
                        class="text-sm text-muted-foreground hover:text-foreground transition-colors">
                {{ item.label }}
              </NuxtLink>
            </li>
          </ul>
        </div>
      </div>

      <!-- Bottom Bar -->
      <div class="mt-12 pt-8 border-t">
        <div class="flex flex-col md:flex-row justify-between items-center space-y-4 md:space-y-0">
          <p class="text-sm text-muted-foreground">
            © {{ new Date().getFullYear() }} Inference Club Agent. All rights reserved.
          </p>
          <div class="flex space-x-6">
            <NuxtLink to="/privacy" class="text-sm text-muted-foreground hover:text-foreground transition-colors">
              Privacy Policy
            </NuxtLink>
            <NuxtLink to="/terms" class="text-sm text-muted-foreground hover:text-foreground transition-colors">
              Terms of Service
            </NuxtLink>
          </div>
        </div>
      </div>
    </div>
  </footer>
</template>

<script setup>
defineOptions({
  name: 'SiteFooter'
})

const quickLinks = [
  { label: 'Home', to: '/' },
  { label: 'LLMs', to: '/llms' },
  { label: 'VLMs', to: '/vlms' },
  { label: 'TTS', to: '/tts' },
  { label: 'Image Gen', to: '/image-gen' },
  { label: 'Video Gen', to: '/video-gen' },
  { label: 'Mesh Gen', to: '/mesh-gen' }
]

const resources = [
  { label: 'Documentation', to: '/docs' },
  { label: 'API Reference', to: '/api' },
  { label: 'Blog', to: '/blog' },
  { label: 'Community', to: '/community' },
  { label: 'Support', to: '/support' }
]
</script>