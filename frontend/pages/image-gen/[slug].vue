<template>
  <div class="container mx-auto px-4 py-8">
    <div class="max-w-4xl mx-auto">
      <div v-if="loading" class="text-center py-8">
        <div class="animate-spin rounded-full h-12 w-12 border-b-2 border-primary mx-auto"/>
        <p class="mt-4 text-muted-foreground">Loading model details...</p>
      </div>
      <div v-else-if="error" class="text-center py-8">
        <p class="text-destructive">{{ error }}</p>
      </div>
      <div v-if="!loading && model" class="space-y-6">
        <div class="flex items-center justify-between">
          <h1 class="text-3xl font-bold bg-gradient-to-r from-purple-600 to-pink-600 bg-clip-text text-transparent">
            {{ model.name }}
          </h1>
          <div class="flex items-center gap-2">
            <Badge variant="outline">{{ model.service_type_display }}</Badge>
            <Badge v-if="model.is_active" variant="success">Active</Badge>
            <Badge v-else variant="secondary">Inactive</Badge>
          </div>
        </div>
        <Card class="mb-8">
          <CardHeader>
            <CardTitle>Generate Image</CardTitle>
            <CardDescription>Use this service to generate an image</CardDescription>
          </CardHeader>
          <CardContent>
            <form class="space-y-4" @submit.prevent="handleSubmit">
              <div class="space-y-2">
                <Label for="mode">Mode</Label>
                <Select v-model="form.mode">
                  <SelectTrigger>
                    <SelectValue placeholder="Select mode" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="base">Base</SelectItem>
                    <SelectItem value="canny">Canny</SelectItem>
                    <SelectItem value="depth">Depth</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="space-y-2">
                <Label for="prompt">Prompt</Label>
                <Input id="prompt" v-model="form.prompt" placeholder="Describe your image..." />
              </div>
              <div class="space-y-2">
                <Label>Ratio</Label>
                <div class="flex flex-wrap gap-4">
                  <RadioGroup v-model="form.ratio" class="flex flex-wrap gap-4">
                    <RadioGroupItem id="ratio-1-1" value="1:1" />
                    <Label for="ratio-1-1">1:1</Label>
                    <RadioGroupItem id="ratio-16-9" value="16:9" />
                    <Label for="ratio-16-9">16:9</Label>
                    <RadioGroupItem id="ratio-9-16" value="9:16" />
                    <Label for="ratio-9-16">9:16</Label>
                    <RadioGroupItem id="ratio-5-4" value="5:4" />
                    <Label for="ratio-5-4">5:4</Label>
                    <RadioGroupItem id="ratio-4-5" value="4:5" />
                    <Label for="ratio-4-5">4:5</Label>
                    <RadioGroupItem id="ratio-3-2" value="3:2" />
                    <Label for="ratio-3-2">3:2</Label>
                    <RadioGroupItem id="ratio-2-3" value="2:3" />
                    <Label for="ratio-2-3">2:3</Label>
                  </RadioGroup>
                </div>
              </div>
              <div class="space-y-2">
                <Label for="cfg_scale">Cfg Scale</Label>
                <Input id="cfg_scale" v-model.number="form.cfg_scale" type="number" min="1.1" max="9" step="0.01" />
              </div>
              <div class="space-y-2">
                <Label for="steps">Steps</Label>
                <Input id="steps" v-model.number="form.steps" type="number" min="5" max="100" step="1" />
              </div>
              <div class="space-y-2">
                <Label for="seed">Seed</Label>
                <Input id="seed" v-model.number="form.seed" type="number" placeholder="0 (random)" />
              </div>
              <div class="flex justify-end">
                <Button type="submit" :disabled="isSubmitting">
                  Generate
                </Button>
              </div>
            </form>
            <div v-if="formError" class="text-destructive mt-2">{{ formError }}</div>
          </CardContent>
        </Card>
        <div class="space-y-4">
          <h2 class="text-xl font-semibold">Previous Requests</h2>
          <div v-if="requestsLoading" class="text-center py-8 text-muted-foreground">
            Loading requests...
          </div>
          <div v-else-if="requests.length === 0" class="text-center py-8 text-muted-foreground">
            No requests yet.
          </div>
          <div v-else class="grid gap-4 grid-cols-1 sm:grid-cols-2 md:grid-cols-3">
            <Card v-for="req in requests" :key="req.id">
              <CardHeader>
                <CardTitle class="flex items-center gap-2">
                  {{ req.payload.prompt }}
                  <Badge v-if="req.status === 'completed'" variant="success">Completed</Badge>
                  <Badge v-else-if="req.status === 'in_progress'" variant="secondary">In Progress</Badge>
                  <Badge v-else-if="req.status === 'failed'" variant="destructive">Failed</Badge>
                  <span class="text-xs text-muted-foreground ml-2">{{ new Date(req.created_at).toLocaleString() }}</span>
                </CardTitle>
                <CardDescription>
                  <span>Mode: {{ req.payload.mode }}</span> | <span>Ratio: {{ req.payload.ratio }}</span>
                </CardDescription>
              </CardHeader>
              <CardContent>
                <div v-if="req.generated_image">
                  <img :src="getImageUrl(req.generated_image)" alt="Generated" class="rounded border w-full max-w-full h-auto" >
                </div>
                <div v-else-if="req.status === 'failed'">
                  <span class="text-destructive">{{ req.error_details }}</span>
                </div>
                <div v-else class="text-muted-foreground">No image yet.</div>
              </CardContent>
            </Card>
          </div>
        </div>
      </div>
      <div v-else-if="!loading && !model" class="text-center py-8">
        <p class="text-muted-foreground">Image generation model not found</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { Card, CardHeader, CardTitle, CardContent, CardDescription, Badge, Button, Input, Label, Select, SelectTrigger, SelectValue, SelectContent, SelectItem, RadioGroup, RadioGroupItem } from '#components'
const config = useRuntimeConfig()
const route = useRoute()
const apiBase = config.public.apiBase
const model = ref(null)
const loading = ref(true)
const error = ref(null)
const formError = ref('')
const isSubmitting = ref(false)
const requests = ref([])
const requestsLoading = ref(true)
const form = ref({
  mode: 'base',
  prompt: '',
  ratio: '1:1',
  cfg_scale: 3.5,
  steps: 50,
  seed: 0,
})
const ratioMap = {
  '1:1': { width: 1024, height: 1024 },
  '16:9': { width: 1344, height: 768 },
  '9:16': { width: 768, height: 1344 },
  '5:4': { width: 1344, height: 1072 },
  '4:5': { width: 1072, height: 1344 },
  '3:2': { width: 1344, height: 896 },
  '2:3': { width: 896, height: 1344 },
}
const handleSubmit = async () => {
  isSubmitting.value = true
  formError.value = ''
  try {
    const { width, height } = ratioMap[form.value.ratio]
    const payload = {
      ...form.value,
      width,
      height,
      service_slug: route.params.slug,
    }
    const response = await fetch(`${apiBase}/api/services/image-gen-infer/`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
    if (!response.ok) throw new Error('Failed to submit request')
    await fetchRequests()
  } catch (e) {
    formError.value = e.message
  } finally {
    isSubmitting.value = false
  }
}
const fetchModel = async () => {
  try {
    const response = await fetch(`${apiBase}/api/services/image-gen-models/${route.params.slug}/`)
    if (!response.ok) {
      const errText = await response.text()
      throw new Error('Failed to fetch model details: ' + errText)
    }
    model.value = await response.json()
  } catch (e) {
    error.value = e.message
    model.value = null
  } finally {
    loading.value = false
  }
}
const fetchRequests = async () => {
  requestsLoading.value = true
  try {
    const response = await fetch(`${apiBase}/api/services/image-gen-models/${route.params.slug}/requests/`)
    if (!response.ok) throw new Error('Failed to fetch requests')
    requests.value = await response.json()
  } finally {
    requestsLoading.value = false
  }
}
const getImageUrl = (path) => {
  if (!path) return ''
  if (path.startsWith('http')) return path
  if (path.startsWith('/media/')) return `${apiBase}${path}`
  return `${apiBase}/media/${path}`
}
onMounted(async () => {
  await fetchModel()
  await fetchRequests()
})
</script>