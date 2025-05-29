<template>
  <div class="min-h-[calc(100vh-4rem)] flex flex-col">
    <div class="flex-1 flex flex-col items-center justify-center py-8 sm:py-12 px-4 sm:px-6 lg:px-8">
      <div class="w-full max-w-4xl mx-auto text-center space-y-6">
        <h1 class="text-3xl sm:text-4xl md:text-5xl lg:text-6xl font-bold tracking-tighter bg-gradient-to-r from-purple-600 to-pink-600 bg-clip-text text-transparent">
          Welcome to Inference Club
        </h1>
        <p class="text-base sm:text-lg md:text-xl text-muted-foreground max-w-2xl mx-auto">
          Your playground for exploring the cutting edge of AI capabilities. Let's create something amazing together!
        </p>
      </div>

      <div class="w-full max-w-7xl mx-auto mt-12 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 sm:gap-6">
        <Card
v-for="capability in capabilities"
              :key="capability.title"
              class="group relative overflow-hidden transition-all hover:scale-[1.02] hover:shadow-lg h-full flex flex-col">
          <CardHeader class="flex-none">
            <CardTitle class="flex items-center gap-2 text-lg sm:text-xl">
              <component :is="capability.icon" class="h-5 w-5 sm:h-6 sm:w-6" />
              {{ capability.title }}
            </CardTitle>
            <CardDescription class="text-sm sm:text-base">{{ capability.description }}</CardDescription>
          </CardHeader>
          <CardContent class="flex-1">
            <p class="text-sm sm:text-base text-muted-foreground">{{ capability.details }}</p>
          </CardContent>
          <CardFooter class="flex-none pt-4">
            <Button variant="ghost" class="w-full group-hover:bg-primary/10 text-sm sm:text-base">
              Explore {{ capability.title }}
              <ArrowRight class="ml-2 h-4 w-4 transition-transform group-hover:translate-x-1" />
            </Button>
          </CardFooter>
        </Card>
      </div>
    </div>
  </div>
</template>

<script setup>
const capabilities = [
  {
    title: 'LLMs',
    icon: 'Brain',
    description: 'Large Language Models',
    details: 'Generate text, chat, and explore the power of language AI'
  },
  {
    title: 'VLMs',
    icon: 'Eye',
    description: 'Vision Language Models',
    details: 'See and understand images with advanced AI vision'
  },
  {
    title: 'TTS',
    icon: 'Mic',
    description: 'Text to Speech',
    details: 'Transform text into natural-sounding speech'
  },
  {
    title: 'Image Gen',
    icon: 'Image',
    description: 'Image Generation',
    details: 'Create stunning visuals from text descriptions'
  },
  {
    title: 'Video Gen',
    icon: 'Video',
    description: 'Video Generation',
    details: 'Bring your ideas to life with AI-powered video creation'
  },
  {
    title: 'Mesh Gen',
    icon: 'Box',
    description: '3D Mesh Generation',
    details: 'Generate and manipulate 3D models with AI'
  }
]
</script>

<style scoped>
</style>
