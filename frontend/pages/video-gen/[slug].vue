<template>
  <div class="container mx-auto px-4 py-8">
    <div class="max-w-4xl mx-auto">
      <div class="flex items-center justify-between mb-8">
        <h1 class="text-3xl font-bold bg-gradient-to-r from-purple-600 to-pink-600 bg-clip-text text-transparent">
          {{ service?.name || 'Loading...' }}
        </h1>
        <Button variant="outline" @click="router.push('/video-gen')">
          Back to Services
        </Button>
      </div>

      <!-- Video Generation Form -->
      <Card class="mb-8">
        <CardHeader>
          <CardTitle>Generate Video</CardTitle>
          <CardDescription>
            Configure your video generation parameters
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form class="space-y-4" @submit.prevent="handleSubmit">
            <div class="space-y-2">
              <Label for="prompt">Prompt</Label>
              <Textarea
                id="prompt"
                v-model="form.prompt"
                placeholder="Describe the video you want to generate..."
                :disabled="isSubmitting"
              />
            </div>
            <div class="space-y-2">
              <Label for="negative_prompt">Negative Prompt</Label>
              <Textarea
                id="negative_prompt"
                v-model="form.negative_prompt"
                placeholder="Describe what you don't want in the video..."
                :disabled="isSubmitting"
              />
            </div>
            <div class="space-y-2">
              <Label for="input_image">Input Image (Optional)</Label>
              <div class="flex items-center gap-4">
                <Input
                  id="input_image"
                  type="file"
                  accept="image/*"
                  :disabled="isSubmitting"
                  class="flex-1"
                  @change="handleImageUpload"
                />
                <Button
                  v-if="form.input_image"
                  type="button"
                  variant="outline"
                  size="icon"
                  :disabled="isSubmitting"
                  @click="removeImage"
                >
                  <X class="h-4 w-4" />
                </Button>
              </div>
              <p class="text-sm text-muted-foreground">
                Upload an image to use as a starting point for the video generation
              </p>
              <div v-if="form.input_image" class="mt-2">
                <img
                  :src="form.input_image"
                  alt="Preview"
                  class="max-h-48 rounded-lg object-contain"
                >
              </div>
            </div>
            <div class="grid grid-cols-2 gap-4">
              <div class="space-y-2">
                <Label for="num_frames">Number of Frames</Label>
                <Input
                  id="num_frames"
                  v-model.number="form.num_frames"
                  type="number"
                  :disabled="isSubmitting"
                />
              </div>
              <div class="space-y-2">
                <Label for="seed">Seed</Label>
                <Input
                  id="seed"
                  v-model.number="form.seed"
                  type="number"
                  :disabled="isSubmitting"
                />
              </div>
            </div>
            <div class="grid grid-cols-2 gap-4">
              <div class="space-y-2">
                <Label for="width">Width</Label>
                <Input
                  id="width"
                  v-model.number="form.width"
                  type="number"
                  :disabled="isSubmitting"
                />
              </div>
              <div class="space-y-2">
                <Label for="height">Height</Label>
                <Input
                  id="height"
                  v-model.number="form.height"
                  type="number"
                  :disabled="isSubmitting"
                />
              </div>
            </div>
            <div class="grid grid-cols-2 gap-4">
              <div class="space-y-2">
                <Label for="num_inference_steps">Inference Steps</Label>
                <Input
                  id="num_inference_steps"
                  v-model.number="form.num_inference_steps"
                  type="number"
                  :disabled="isSubmitting"
                />
              </div>
              <div class="space-y-2">
                <Label for="guidance_scale">Guidance Scale</Label>
                <Input
                  id="guidance_scale"
                  v-model.number="form.guidance_scale"
                  type="number"
                  step="0.1"
                  :disabled="isSubmitting"
                />
              </div>
            </div>
            <div class="flex justify-end">
              <Button type="submit" :disabled="isSubmitting">
                {{ isSubmitting ? 'Generating...' : 'Generate Video' }}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <!-- Inference Requests List -->
      <div class="space-y-4">
        <h2 class="text-xl font-semibold">Generation History</h2>
        <div v-if="inferenceRequests.length === 0" class="text-center py-8 text-muted-foreground">
          No video generations yet. Create your first video above.
        </div>
        <div v-else class="grid gap-4">
          <Card v-for="request in inferenceRequests" :key="request.id" class="relative">
            <CardHeader>
              <div class="flex justify-between items-start">
                <div>
                  <CardTitle class="flex items-center gap-2">
                    Request #{{ request.id }}
                    <Badge :variant="getStatusVariant(request.status)">
                      {{ request.status }}
                    </Badge>
                  </CardTitle>
                  <CardDescription>
                    {{ new Date(request.created_at).toLocaleString() }}
                  </CardDescription>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <div class="space-y-4">
                <div class="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <span class="font-medium">Prompt:</span>
                    <p class="text-muted-foreground">{{ request.payload.prompt }}</p>
                  </div>
                  <div>
                    <span class="font-medium">Negative Prompt:</span>
                    <p class="text-muted-foreground">{{ request.payload.negative_prompt }}</p>
                  </div>
                </div>
                <div v-if="request.status === 'completed' && request.generated_video" class="mt-4">
                  <video
                    :src="getVideoUrl(request.generated_video)"
                    controls
                    class="w-full rounded-lg"
                    preload="metadata"
                    @error="(e) => handleVideoError(e, request.generated_video)"
                    @loadeddata="videoError = null"
                  >
                    <source :src="getVideoUrl(request.generated_video)" type="video/mp4">
                    Your browser does not support the video tag.
                  </video>
                  <div v-if="videoError" class="mt-2 text-sm text-destructive">
                    {{ videoError }}
                  </div>
                </div>
                <div v-if="request.error_details" class="mt-4 p-4 bg-destructive/10 rounded-lg">
                  <p class="text-destructive">{{ request.error_details }}</p>
                </div>
              </div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { X } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const config = useRuntimeConfig()
const apiBase = config.public.apiBase

const service = ref(null)
const inferenceRequests = ref([])
const isSubmitting = ref(false)
const videoError = ref(null)

const form = ref({
  prompt: '',
  negative_prompt: '',
  num_frames: 16,
  width: 512,
  height: 512,
  num_inference_steps: 50,
  guidance_scale: 7.5,
  seed: -1,
  input_image: null,
  input_image_file: null
})

const fetchService = async () => {
  try {
    const response = await fetch(`${apiBase}/api/services/video-gen-services/${route.params.slug}/`)
    if (!response.ok) throw new Error('Failed to fetch service')
    service.value = await response.json()
  } catch (error) {
    console.error('Error fetching service:', error)
  }
}

const fetchInferenceRequests = async () => {
  try {
    const response = await fetch(`${apiBase}/api/services/video-gen-services/${route.params.slug}/requests/`)
    if (!response.ok) throw new Error('Failed to fetch inference requests')
    inferenceRequests.value = await response.json()
  } catch (error) {
    console.error('Error fetching inference requests:', error)
  }
}

const handleImageUpload = (event) => {
  const file = event.target.files[0]
  if (file) {
    form.value.input_image_file = file
    const reader = new FileReader()
    reader.onload = (e) => {
      form.value.input_image = e.target.result
    }
    reader.readAsDataURL(file)
  }
}

const removeImage = () => {
  form.value.input_image = null
  form.value.input_image_file = null
}

const handleSubmit = async () => {
  isSubmitting.value = true
  try {
    const formData = new FormData()
    formData.append('video_gen_service', service.value.id)
    formData.append('prompt', form.value.prompt)
    formData.append('negative_prompt', form.value.negative_prompt)
    formData.append('num_frames', form.value.num_frames)
    formData.append('width', form.value.width)
    formData.append('height', form.value.height)
    formData.append('num_inference_steps', form.value.num_inference_steps)
    formData.append('guidance_scale', form.value.guidance_scale)
    formData.append('seed', form.value.seed)

    if (form.value.input_image_file) {
      formData.append('input_image', form.value.input_image_file)
    }

    const response = await fetch(`${apiBase}/api/services/video-gen-infer/`, {
      method: 'POST',
      body: formData,
    })
    if (!response.ok) throw new Error('Failed to submit video generation request')
    await fetchInferenceRequests()
    // Reset form after successful submission
    form.value = {
      prompt: '',
      negative_prompt: '',
      num_frames: 16,
      width: 512,
      height: 512,
      num_inference_steps: 50,
      guidance_scale: 7.5,
      seed: -1,
      input_image: null,
      input_image_file: null
    }
  } catch (error) {
    console.error('Error submitting video generation request:', error)
  } finally {
    isSubmitting.value = false
  }
}

const getStatusVariant = (status) => {
  switch (status) {
    case 'completed':
      return 'success'
    case 'failed':
      return 'destructive'
    case 'in_progress':
      return 'warning'
    default:
      return 'secondary'
  }
}

const getVideoUrl = (url) => {
  if (!url) return ''
  // If the URL is already absolute, return it as is
  if (url.startsWith('http')) return url
  // Otherwise, prepend the API base URL
  const fullUrl = `${apiBase}${url}`
  console.log('Video URL:', fullUrl) // Debug log
  return fullUrl
}

const handleVideoError = (e, url) => {
  const video = e.target
  const errorDetails = {
    error: video.error,
    networkState: video.networkState,
    readyState: video.readyState,
    src: url,
    fullUrl: getVideoUrl(url)
  }
  console.error('Video error details:', errorDetails)

  let errorMessage = 'Error loading video'
  if (video.error) {
    switch (video.error.code) {
      case 1:
        errorMessage = 'Video loading aborted'
        break
      case 2:
        errorMessage = 'Network error while loading video'
        break
      case 3:
        errorMessage = 'Error decoding video'
        break
      case 4:
        errorMessage = 'Video not supported or corrupted'
        break
    }
  }

  // Only show error if we have a specific error code
  if (video.error && video.error.code !== 0) {
    videoError.value = `${errorMessage}. Please try refreshing the page.`
  } else {
    videoError.value = null
  }
}

onMounted(() => {
  fetchService()
  fetchInferenceRequests()
})
</script>

<style scoped>
</style>