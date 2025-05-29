<template>
  <div class="container mx-auto px-4 py-8">
    <div class="max-w-4xl mx-auto">
      <h1 class="text-3xl font-bold mb-8 bg-gradient-to-r from-purple-600 to-pink-600 bg-clip-text text-transparent">
        LLM Models
      </h1>

      <!-- Add/Edit Form -->
      <Card class="mb-8">
        <CardHeader>
          <CardTitle>{{ editingModel ? 'Edit LLM Model' : 'Add New LLM Model' }}</CardTitle>
          <CardDescription>
            Configure your LLM model settings
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form class="space-y-4" @submit.prevent="handleSubmit">
            <div class="space-y-2">
              <Label for="name">Model Name</Label>
              <Input
                id="name"
                v-model="form.name"
                placeholder="e.g. qwen3-8b"
                :disabled="isSubmitting"
              />
            </div>
            <div class="space-y-2">
              <Label for="base_url">Base URL</Label>
              <Input
                id="base_url"
                v-model="form.base_url"
                placeholder="e.g. http://localhost:1234/v1"
                :disabled="isSubmitting"
              />
            </div>
            <div class="flex items-center space-x-2">
              <Checkbox id="is_active" v-model="form.is_active" :disabled="isSubmitting" />
              <Label for="is_active">Active</Label>
            </div>
            <div class="flex justify-end space-x-2">
              <Button
                v-if="editingModel"
                type="button"
                variant="outline"
                :disabled="isSubmitting"
                @click="cancelEdit"
              >
                Cancel
              </Button>
              <Button type="submit" :disabled="isSubmitting">
                {{ editingModel ? 'Update' : 'Add' }} Model
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <!-- Models List -->
      <div class="space-y-4">
        <h2 class="text-xl font-semibold">Configured Models</h2>
        <div v-if="models.length === 0" class="text-center py-8 text-muted-foreground">
          No LLM models configured yet. Add your first model above.
        </div>
        <div v-else class="grid gap-4">
          <Card v-for="model in models" :key="model.id" class="relative hover:bg-accent/50 transition-colors cursor-pointer" @click="navigateTo(`/llms/${model.slug}`)">
            <CardHeader>
              <div class="flex justify-between items-start">
                <div>
                  <CardTitle class="flex items-center gap-2">
                    {{ model.name }}
                    <Badge v-if="model.is_active" variant="success">Active</Badge>
                    <Badge v-else variant="secondary">Inactive</Badge>
                  </CardTitle>
                  <CardDescription>{{ model.base_url }}</CardDescription>
                </div>
                <div class="flex space-x-2">
                  <Button
                    variant="ghost"
                    size="icon"
                    :disabled="isSubmitting"
                    @click.stop="editModel(model)"
                  >
                    <Pencil class="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    :disabled="isSubmitting"
                    @click.stop="deleteModel(model.id)"
                  >
                    <Trash2 class="h-4 w-4" />
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <div class="text-sm text-muted-foreground">
                Last updated: {{ new Date(model.updated_at).toLocaleString() }}
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

const config = useRuntimeConfig()
const apiBase = config.public.apiBase

const models = ref([])
const isSubmitting = ref(false)
const editingModel = ref(null)

const form = ref({
  name: '',
  base_url: '',
  is_active: true
})

const resetForm = () => {
  form.value = {
    name: '',
    base_url: '',
    is_active: true
  }
  editingModel.value = null
}

const fetchModels = async () => {
  try {
    const response = await fetch(`${apiBase}/api/services/llm-models/`)
    if (!response.ok) throw new Error('Failed to fetch models')
    models.value = await response.json()
  } catch (error) {
    console.error('Error fetching models:', error)
    // You might want to show a toast notification here
  }
}

const handleSubmit = async () => {
  isSubmitting.value = true
  try {
    const url = editingModel.value
      ? `${apiBase}/api/services/llm-models/${editingModel.value.id}/`
      : `${apiBase}/api/services/llm-models/`

    const method = editingModel.value ? 'PUT' : 'POST'

    const response = await fetch(url, {
      method,
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(form.value),
    })

    if (!response.ok) throw new Error('Failed to save model')

    await fetchModels()
    resetForm()
  } catch (error) {
    console.error('Error saving model:', error)
    // You might want to show a toast notification here
  } finally {
    isSubmitting.value = false
  }
}

const editModel = (model) => {
  editingModel.value = model
  form.value = { ...model }
}

const cancelEdit = () => {
  resetForm()
}

const deleteModel = async (id) => {
  if (!confirm('Are you sure you want to delete this model?')) return

  isSubmitting.value = true
  try {
    const response = await fetch(`${apiBase}/api/services/llm-models/${id}/`, {
      method: 'DELETE',
    })

    if (!response.ok) throw new Error('Failed to delete model')

    await fetchModels()
  } catch (error) {
    console.error('Error deleting model:', error)
    // You might want to show a toast notification here
  } finally {
    isSubmitting.value = false
  }
}

onMounted(fetchModels)
</script>