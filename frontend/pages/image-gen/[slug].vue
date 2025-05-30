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
          <div
            class="flex items-center justify-between cursor-pointer px-6 py-4 border-border"
            style="user-select: none;"
            @click="toggleForm"
          >
            <div>
              <div class="text-lg font-semibold">Generate Image</div>
              <div class="text-sm text-muted-foreground">Use this service to generate an image</div>
            </div>
            <Icon name="lucide:chevron-down" :class="['transition-transform', { 'rotate-180': formExpanded }]" size="24" />
          </div>
          <transition name="fade">
            <CardContent v-if="formExpanded">
              <form class="space-y-4" @submit.prevent="handleSubmit">
                <div class="space-y-2">
                  <Label for="mode">Mode</Label>
                  <Select v-model="form.mode">
                    <SelectTrigger>
                      <SelectValue placeholder="Select mode" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="base">Base (Text to Image)</SelectItem>
                      <SelectItem value="canny">Canny (Edge Detection)</SelectItem>
                      <SelectItem value="depth">Depth (Depth Map)</SelectItem>
                    </SelectContent>
                  </Select>
                  <p class="text-sm text-muted-foreground mt-1">
                    Note: For Canny and Depth modes, you'll need to provide an input image.
                  </p>
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
                <div v-if="form.mode === 'canny' || form.mode === 'depth'" class="space-y-2">
                  <Label for="input-image">Input Image (optional)</Label>
                  <input id="input-image" type="file" accept="image/*" class="block w-full text-sm text-muted-foreground file:mr-4 file:py-2 file:px-4 file:rounded file:border-0 file:text-sm file:font-semibold file:bg-primary/10 file:text-primary hover:file:bg-primary/20" @change="handleImageUpload" >
                  <div v-if="form.image" class="mt-2">
                    <img :src="form.image" alt="Input preview" class="max-h-40 rounded border" >
                    <Button variant="ghost" size="sm" class="mt-1" @click="removeImage">Remove</Button>
                  </div>
                </div>
                <div class="flex justify-end">
                  <Button type="submit" :disabled="isSubmitting">
                    Generate
                  </Button>
                </div>
              </form>
              <div v-if="formError" class="text-destructive mt-2">{{ formError }}</div>
            </CardContent>
          </transition>
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
                <div class="flex flex-col gap-1">
                  <span
                    class="prompt-preview"
                    :title="req.payload.prompt"
                  >
                    {{ getPromptPreview(req.payload.prompt) }}
                  </span>
                  <div class="flex items-center justify-between mt-1">
                    <Badge
                      v-if="req.status === 'completed'"
                      variant="success"
                      class="status-badge"
                    >Completed</Badge>
                    <Badge
                      v-else-if="req.status === 'in_progress'"
                      variant="secondary"
                      class="status-badge"
                    >In Progress</Badge>
                    <Badge
                      v-else-if="req.status === 'failed'"
                      variant="destructive"
                      class="status-badge"
                    >Failed</Badge>
                    <span
                      class="date-label"
                      :title="formatFullDate(req.created_at)"
                    >{{ formatShortDate(req.created_at) }}</span>
                  </div>
                  <div class="meta-info">
                    Mode: {{ req.payload.mode }} | Ratio: {{ req.payload.ratio }}
                  </div>
                </div>
              </CardHeader>
              <CardContent>
                <div v-if="req.generated_image">
                  <img :src="getImageUrl(req.generated_image)" alt="Generated" class="card-image" >
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
import { Card, CardHeader, CardContent, Badge, Button, Input, Label, Select, SelectTrigger, SelectValue, SelectContent, SelectItem, RadioGroup, RadioGroupItem, Icon } from '#components'
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
  image: null,
})
const formExpanded = ref(false)
const toggleForm = () => { formExpanded.value = !formExpanded.value }
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
      image: (form.value.mode === 'canny' || form.value.mode === 'depth') ? form.value.image : null,
    }
    const response = await fetch(`${apiBase}/api/services/image-gen-infer/`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
    if (!response.ok) throw new Error('Failed to submit request')
    await fetchRequests()
    form.value.image = null
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
const handleImageUpload = (e) => {
  const file = e.target.files[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = (event) => {
    form.value.image = event.target.result
  }
  reader.readAsDataURL(file)
}
const removeImage = () => {
  form.value.image = null
}
// Helper: prompt preview (first 80 chars, ellipsis if longer)
const getPromptPreview = (prompt) => {
  if (!prompt) return '';
  return prompt.length > 80 ? prompt.slice(0, 80) + '…' : prompt;
};
// Helper: short date (MM/DD/YYYY)
const formatShortDate = (dateStr) => {
  const d = new Date(dateStr);
  return d.toLocaleDateString('en-US');
};
// Helper: full date/time
const formatFullDate = (dateStr) => {
  const d = new Date(dateStr);
  return d.toLocaleString();
};
onMounted(async () => {
  await fetchModel()
  await fetchRequests()
})
</script>

<style scoped>
.prompt-preview {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: 1.1rem;
  font-weight: 600;
  cursor: pointer;
  word-break: break-word;
  line-height: 1.3;
  min-height: 2.6em;
  margin-bottom: 0.1em;
}
.status-badge {
  font-size: 0.85em;
  padding: 0.2em 0.7em;
  border-radius: 0.7em;
  font-weight: 500;
  letter-spacing: 0.02em;
}
.date-label {
  font-size: 0.95em;
  color: #bbb;
  margin-left: auto;
  margin-right: 0.1em;
  white-space: nowrap;
}
.meta-info {
  font-size: 0.95em;
  color: #aaa;
  margin-top: 0.2em;
  font-weight: 400;
}
.card-image {
  width: 100%;
  border-radius: 0.7em;
  margin-top: 0.5em;
  object-fit: cover;
  background: #222;
}
.fade-enter-active, .fade-leave-active {
  transition: max-height 0.3s cubic-bezier(.4,0,.2,1), opacity 0.3s;
}
.fade-enter-from, .fade-leave-to {
  max-height: 0;
  opacity: 0;
}
.fade-enter-to, .fade-leave-from {
  max-height: 1000px;
  opacity: 1;
}
</style>