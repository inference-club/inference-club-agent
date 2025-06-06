<template>
  <div class="container mx-auto px-4 py-8">
    <div class="max-w-4xl mx-auto">
      <h1 class="text-3xl font-bold mb-8 bg-gradient-to-r from-purple-600 to-pink-600 bg-clip-text text-transparent">
        Video Generation Services
      </h1>

      <!-- Add/Edit Form -->
      <Card class="mb-8">
        <CardHeader>
          <CardTitle>{{ editingService ? 'Edit Video Generation Service' : 'Add New Video Generation Service' }}</CardTitle>
          <CardDescription>
            Configure your video generation service settings
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form class="space-y-4" @submit.prevent="handleSubmit">
            <div class="space-y-2">
              <Label for="name">Name</Label>
              <Input
                id="name"
                v-model="form.name"
                placeholder="e.g. My Video Service"
                :disabled="isSubmitting"
              />
            </div>
            <div class="space-y-2">
              <Label for="slug">Slug</Label>
              <Input
                id="slug"
                v-model="form.slug"
                placeholder="e.g. my-video-service"
                :disabled="isSubmitting"
              />
            </div>
            <div class="space-y-2">
              <Label for="url">Service URL</Label>
              <Input
                id="url"
                v-model="form.url"
                placeholder="e.g. http://localhost:5000"
                :disabled="isSubmitting"
              />
            </div>
            <div class="space-y-2">
              <Label for="type">Type</Label>
              <Select v-model="form.type" :disabled="isSubmitting">
                <SelectTrigger>
                  <SelectValue placeholder="Select a type" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="HUNYUAN_VIDEO">Hunyuan Video</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="flex justify-end space-x-2">
              <Button
                v-if="editingService"
                type="button"
                variant="outline"
                :disabled="isSubmitting"
                @click="cancelEdit"
              >
                Cancel
              </Button>
              <Button type="submit" :disabled="isSubmitting">
                {{ editingService ? 'Update' : 'Add' }} Service
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <!-- Services List -->
      <div class="space-y-4">
        <h2 class="text-xl font-semibold">Configured Video Generation Services</h2>
        <div v-if="services.length === 0" class="text-center py-8 text-muted-foreground">
          No video generation services configured yet. Add your first service above.
        </div>
        <div v-else class="grid gap-4">
          <Card v-for="service in services" :key="service.id" class="relative hover:bg-accent/50 transition-colors cursor-pointer" @click="navigateTo(`/video-gen/${service.slug}`)">
            <CardHeader>
              <div class="flex justify-between items-start">
                <div>
                  <CardTitle class="flex items-center gap-2">
                    {{ service.name }}
                  </CardTitle>
                  <CardDescription>
                    <div class="flex items-center gap-2">
                      <span>{{ service.url }}</span>
                      <Badge variant="outline">{{ service.type }}</Badge>
                    </div>
                  </CardDescription>
                </div>
                <div class="flex space-x-2">
                  <Button
                    variant="ghost"
                    size="icon"
                    :disabled="isSubmitting"
                    @click.stop="editService(service)"
                  >
                    <Pencil class="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    :disabled="isSubmitting"
                    @click.stop="deleteService(service)"
                  >
                    <Trash2 class="h-4 w-4" />
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <div class="text-sm text-muted-foreground">
                ID: {{ service.id }}
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
import { Pencil, Trash2 } from 'lucide-vue-next'
import { useRouter } from 'vue-router'

const config = useRuntimeConfig()
const apiBase = config.public.apiBase
const router = useRouter()

const services = ref([])
const isSubmitting = ref(false)
const editingService = ref(null)

const form = ref({
  name: '',
  slug: '',
  url: '',
  type: ''
})

const resetForm = () => {
  form.value = {
    name: '',
    slug: '',
    url: '',
    type: ''
  }
  editingService.value = null
}

const fetchServices = async () => {
  try {
    const response = await fetch(`${apiBase}/api/services/video-gen-services/`)
    if (!response.ok) throw new Error('Failed to fetch services')
    services.value = await response.json()
  } catch (error) {
    console.error('Error fetching services:', error)
  }
}

const handleSubmit = async () => {
  isSubmitting.value = true
  try {
    const url = editingService.value
      ? `${apiBase}/api/services/video-gen-services/${editingService.value.slug}/`
      : `${apiBase}/api/services/video-gen-services/`
    const method = editingService.value ? 'PUT' : 'POST'
    const response = await fetch(url, {
      method,
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(form.value),
    })
    if (!response.ok) throw new Error('Failed to save service')
    await fetchServices()
    resetForm()
  } catch (error) {
    console.error('Error saving service:', error)
  } finally {
    isSubmitting.value = false
  }
}

const editService = (service) => {
  editingService.value = service
  form.value = { ...service }
}

const cancelEdit = () => {
  resetForm()
}

const deleteService = async (service) => {
  if (!confirm('Are you sure you want to delete this service?')) return
  isSubmitting.value = true
  try {
    const response = await fetch(`${apiBase}/api/services/video-gen-services/${service.slug}/`, {
      method: 'DELETE',
    })
    if (!response.ok) throw new Error('Failed to delete service')
    await fetchServices()
  } catch (error) {
    console.error('Error deleting service:', error)
  } finally {
    isSubmitting.value = false
  }
}

const navigateTo = (path) => {
  router.push(path)
}

onMounted(fetchServices)
</script>

<style scoped>
</style>