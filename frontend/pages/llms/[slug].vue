<template>
  <div class="container mx-auto px-4 py-8">
    <div class="max-w-4xl mx-auto">
      <div v-if="loading" class="text-center py-8">
        <div class="animate-spin rounded-full h-12 w-12 border-b-2 border-primary mx-auto"/>
        <p class="mt-4 text-muted-foreground">Loading LLM details...</p>
      </div>

      <div v-else-if="error" class="text-center py-8">
        <p class="text-destructive">{{ error }}</p>
      </div>

      <div v-else-if="model" class="space-y-6">
        <div class="flex items-center justify-between">
          <h1 class="text-3xl font-bold bg-gradient-to-r from-purple-600 to-pink-600 bg-clip-text text-transparent">
            {{ model.name }}
          </h1>
          <Badge v-if="model.is_active" variant="success">Active</Badge>
          <Badge v-else variant="secondary">Inactive</Badge>
        </div>

        <Card>
          <CardHeader>
            <CardTitle>Model Details</CardTitle>
          </CardHeader>
          <CardContent>
            <dl class="space-y-4">
              <div>
                <dt class="text-sm font-medium text-muted-foreground">Base URL</dt>
                <dd class="mt-1 text-sm">{{ model.base_url }}</dd>
              </div>
              <div>
                <dt class="text-sm font-medium text-muted-foreground">Created</dt>
                <dd class="mt-1 text-sm">{{ new Date(model.created_at).toLocaleString() }}</dd>
              </div>
              <div>
                <dt class="text-sm font-medium text-muted-foreground">Last Updated</dt>
                <dd class="mt-1 text-sm">{{ new Date(model.updated_at).toLocaleString() }}</dd>
              </div>
            </dl>
          </CardContent>
        </Card>
      </div>

      <div v-else class="text-center py-8">
        <p class="text-muted-foreground">LLM model not found</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { Card, CardHeader, CardTitle, CardContent, Badge  } from '#components'

const config = useRuntimeConfig()
const route = useRoute()
const apiBase = config.public.apiBase

const model = ref(null)
const loading = ref(true)
const error = ref(null)

const fetchModel = async () => {
  try {
    const response = await fetch(`${apiBase}/api/services/llm-models/${route.params.slug}/`)
    if (!response.ok) {
      if (response.status === 404) {
        model.value = null
      } else {
        throw new Error('Failed to fetch model details')
      }
    } else {
      model.value = await response.json()
    }
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}

onMounted(fetchModel)
</script>