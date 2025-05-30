<template>
  <div class="container mx-auto px-4 py-8">
    <div class="max-w-2xl mx-auto">
      <div v-if="loading" class="text-center py-8">
        <div class="animate-spin rounded-full h-12 w-12 border-b-2 border-primary mx-auto" />
        <p class="mt-4 text-muted-foreground">Loading TTS service details...</p>
      </div>
      <div v-else-if="error" class="text-center py-8">
        <p class="text-destructive">{{ error }}</p>
      </div>
      <div v-else-if="service" class="space-y-6">
        <Card>
          <CardHeader>
            <CardTitle>{{ service.slug }}</CardTitle>
            <CardDescription>
              <span class="font-mono">ID: {{ service.id }}</span>
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div class="space-y-2">
              <div><span class="font-semibold">URL:</span> <span class="font-mono">{{ service.url }}</span></div>
              <div><span class="font-semibold">Type:</span> <Badge variant="outline">{{ service.type }}</Badge></div>
            </div>
          </CardContent>
        </Card>

        <!-- TTS Request Form -->
        <Card class="mt-8">
          <CardHeader>
            <CardTitle>Generate Speech</CardTitle>
            <CardDescription>Submit a prompt to generate speech audio</CardDescription>
          </CardHeader>
          <CardContent>
            <form class="space-y-6" @submit.prevent="handleSubmit">
              <div class="space-y-2">
                <Label for="conditioningText">Conditioning Speech Text (optional)</Label>
                <Textarea id="conditioningText" v-model="form.conditioningText" placeholder="e.g. [Whispered] This is a secret..." />
              </div>
              <div v-if="form.conditioningText" class="space-y-2">
                <Label for="conditioningAudio">Conditioning Speech Audio File (optional)</Label>
                <Input id="conditioningAudio" type="file" accept="audio/*" @change="handleAudioUpload" />
                <div v-if="form.audioFileName" class="text-xs text-muted-foreground">Selected: {{ form.audioFileName }}</div>
                <Button v-if="form.audioFile" variant="ghost" size="sm" class="mt-1" @click.prevent="removeAudio">Remove</Button>
              </div>
              <div class="space-y-2">
                <Label for="prompt">Prompt</Label>
                <Textarea id="prompt" v-model="form.prompt" placeholder="Enter your prompt..." required />
              </div>

              <!-- Generation Parameters -->
              <Card>
                <CardHeader class="flex flex-row items-center justify-between cursor-pointer" @click="toggleParams">
                  <CardTitle class="text-base">Generation Parameters</CardTitle>
                  <Icon name="lucide:chevron-down" :class="['transition-transform', { 'rotate-180': paramsExpanded }]" size="20" />
                </CardHeader>
                <CardContent v-show="paramsExpanded" class="space-y-4">
                  <div class="space-y-1">
                    <Label>Max New Tokens (Audio Length)</Label>
                    <div class="flex items-center gap-2">
                      <Input v-model.number="form.max_new_tokens" type="number" min="860" max="3072" class="w-32" />
                      <Button type="button" variant="ghost" size="icon" @click="resetParam('max_new_tokens')"><Icon name="lucide:rotate-ccw" size="16" /></Button>
                    </div>
                  </div>
                  <div class="space-y-1">
                    <Label>CFG Scale (Guidance Strength)</Label>
                    <div class="flex items-center gap-2">
                      <Input v-model.number="form.cfg_scale" type="number" min="1" max="5" step="0.1" class="w-32" />
                      <Button type="button" variant="ghost" size="icon" @click="resetParam('cfg_scale')"><Icon name="lucide:rotate-ccw" size="16" /></Button>
                    </div>
                  </div>
                  <div class="space-y-1">
                    <Label>Temperature (Randomness)</Label>
                    <div class="flex items-center gap-2">
                      <Input v-model.number="form.temperature" type="number" min="1" max="1.5" step="0.01" class="w-32" />
                      <Button type="button" variant="ghost" size="icon" @click="resetParam('temperature')"><Icon name="lucide:rotate-ccw" size="16" /></Button>
                    </div>
                  </div>
                  <div class="space-y-1">
                    <Label>Top P (Nucleus Sampling)</Label>
                    <div class="flex items-center gap-2">
                      <Input v-model.number="form.top_p" type="number" min="0.8" max="1" step="0.01" class="w-32" />
                      <Button type="button" variant="ghost" size="icon" @click="resetParam('top_p')"><Icon name="lucide:rotate-ccw" size="16" /></Button>
                    </div>
                  </div>
                  <div class="space-y-1">
                    <Label>CFG Filter Top K</Label>
                    <div class="flex items-center gap-2">
                      <Input v-model.number="form.cfg_filter_top_k" type="number" min="15" max="50" step="1" class="w-32" />
                      <Button type="button" variant="ghost" size="icon" @click="resetParam('cfg_filter_top_k')"><Icon name="lucide:rotate-ccw" size="16" /></Button>
                    </div>
                  </div>
                  <div class="space-y-1">
                    <Label>Speed Factor</Label>
                    <div class="flex items-center gap-2">
                      <Input v-model.number="form.speed_factor" type="number" min="0.8" max="1" step="0.01" class="w-32" />
                      <Button type="button" variant="ghost" size="icon" @click="resetParam('speed_factor')"><Icon name="lucide:rotate-ccw" size="16" /></Button>
                    </div>
                  </div>
                </CardContent>
              </Card>

              <div class="flex justify-end">
                <Button type="submit" :disabled="isSubmitting">
                  Generate
                </Button>
              </div>
              <div v-if="formError" class="text-destructive mt-2">{{ formError }}</div>
            </form>
          </CardContent>
        </Card>

        <!-- TTS Inference Requests List -->
        <div class="space-y-4 mt-8">
          <h2 class="text-xl font-semibold">Previous TTS Requests</h2>
          <div v-if="requestsLoading" class="text-center py-8 text-muted-foreground">
            Loading requests...
          </div>
          <div v-else-if="requestsError" class="text-center py-8 text-destructive">
            {{ requestsError }}
          </div>
          <div v-else-if="ttsRequests.length === 0" class="text-center py-8 text-muted-foreground">
            No TTS requests yet.
          </div>
          <div v-else class="grid gap-4">
            <Card v-for="req in ttsRequests" :key="req.id">
              <CardHeader>
                <CardTitle class="flex items-center gap-2">
                  <span class="prompt-preview" :title="req.payload.text_input">
                    {{ getPromptPreview(req.payload.text_input) }}
                  </span>
                  <Badge v-if="req.status === 'completed'" variant="success">Completed</Badge>
                  <Badge v-else-if="req.status === 'in_progress'" variant="secondary">In Progress</Badge>
                  <Badge v-else-if="req.status === 'failed'" variant="destructive">Failed</Badge>
                  <span class="text-xs text-muted-foreground ml-2" :title="formatFullDate(req.created_at)">
                    {{ formatShortDate(req.created_at) }}
                  </span>
                </CardTitle>
              </CardHeader>
              <CardContent>
                <div v-if="req.speech_output">
                  <audio :src="getAudioUrl(req.speech_output)" controls class="w-full mt-2" />
                </div>
                <div v-else-if="req.status === 'failed'">
                  <span class="text-destructive">{{ req.error_details }}</span>
                </div>
                <div v-else class="text-muted-foreground">No audio yet.</div>
              </CardContent>
            </Card>
          </div>
        </div>
      </div>
      <div v-else class="text-center py-8">
        <p class="text-muted-foreground">TTS service not found.</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, watch } from 'vue'
import { Card, CardHeader, CardTitle, CardContent, CardDescription, Badge, Button, Input, Label, Textarea, Icon } from '#components'
const config = useRuntimeConfig()
const route = useRoute()
const apiBase = config.public.apiBase
const service = ref(null)
const loading = ref(true)
const error = ref(null)
const paramsExpanded = ref(true)
const isSubmitting = ref(false)
const formError = ref('')

const ttsRequests = ref([])
const requestsLoading = ref(true)
const requestsError = ref('')

const defaultParams = {
  max_new_tokens: 3072,
  cfg_scale: 3,
  temperature: 1.3,
  top_p: 0.95,
  cfg_filter_top_k: 30,
  speed_factor: 0.94
}

const form = ref({
  prompt: '',
  conditioningText: '',
  audioFile: null,
  audioFileName: '',
  ...defaultParams
})

const getPromptPreview = (prompt) => {
  if (!prompt) return ''
  return prompt.length > 80 ? prompt.slice(0, 80) + '…' : prompt
}
const formatShortDate = (dateStr) => {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US')
}
const formatFullDate = (dateStr) => {
  const d = new Date(dateStr)
  return d.toLocaleString()
}
const getAudioUrl = (path) => {
  if (!path) return ''
  if (path.startsWith('http')) return path
  if (path.startsWith('/media/')) return `${apiBase}${path}`
  return `${apiBase}/media/${path}`
}

const fetchTTSRequests = async () => {
  if (!service.value) return
  requestsLoading.value = true
  requestsError.value = ''
  try {
    const response = await fetch(`${apiBase}/api/inference/?tts_service=${service.value.id}`)
    if (!response.ok) throw new Error('Failed to fetch TTS requests')
    ttsRequests.value = await response.json()
  } catch (e) {
    requestsError.value = e.message
  } finally {
    requestsLoading.value = false
  }
}

const toggleParams = () => {
  paramsExpanded.value = !paramsExpanded.value
}

const resetParam = (key) => {
  form.value[key] = defaultParams[key]
}

const handleAudioUpload = (e) => {
  const file = e.target.files[0]
  if (!file) return
  form.value.audioFile = file
  form.value.audioFileName = file.name
}

const removeAudio = () => {
  form.value.audioFile = null
  form.value.audioFileName = ''
}

watch(() => form.value.conditioningText, (val) => {
  if (!val) removeAudio()
})

const fetchService = async () => {
  try {
    const response = await fetch(`${apiBase}/api/services/tts-services/${route.params.slug}/`)
    if (!response.ok) {
      if (response.status === 404) {
        service.value = null
      } else {
        throw new Error('Failed to fetch TTS service details')
      }
    } else {
      service.value = await response.json()
    }
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}

const handleSubmit = async () => {
  isSubmitting.value = true
  formError.value = ''
  try {
    const text_input = (form.value.conditioningText ? form.value.conditioningText + ' ' : '') + form.value.prompt
    const payload = {
      text_input,
      max_new_tokens: form.value.max_new_tokens,
      cfg_scale: form.value.cfg_scale,
      temperature: form.value.temperature,
      top_p: form.value.top_p,
      cfg_filter_top_k: form.value.cfg_filter_top_k,
      speed_factor: form.value.speed_factor
    }
    const formData = new FormData()
    Object.entries(payload).forEach(([k, v]) => formData.append(k, v))
    if (form.value.audioFile) {
      formData.append('audio_prompt_input', form.value.audioFile)
    }
    formData.append('tts_service', service.value.id)
    formData.append('inference_type', 'tts')
    const response = await fetch(`${apiBase}/api/services/tts-infer/`, {
      method: 'POST',
      body: formData
    })
    if (!response.ok) throw new Error('Failed to submit TTS request')
    await fetchTTSRequests()
    // Optionally: handle response, show success, etc.
  } catch (e) {
    formError.value = e.message
  } finally {
    isSubmitting.value = false
  }
}

onMounted(async () => {
  await fetchService()
  await fetchTTSRequests()
})
watch(service, (val) => { if (val) fetchTTSRequests() })
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
</style>