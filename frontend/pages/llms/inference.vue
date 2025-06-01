<template>
  <div class="min-h-screen bg-background">
    <!-- Header -->
    <div class="border-b">
      <div class="container flex h-16 items-center justify-between py-4">
        <h1 class="text-2xl font-semibold">LLM Playground</h1>
        <div class="flex items-center gap-4">
          <div class="flex items-center gap-2">
            <Label>Model</Label>
            <Select v-model="selectedModel">
              <SelectTrigger class="w-[180px]">
                <SelectValue :placeholder="selectedModel?.name || 'Select a model'" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem v-for="model in models" :key="model.id" :value="model">
                    {{ model.name }}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div class="flex items-center gap-2">
            <Label>Mode</Label>
            <Select v-model="mode">
              <SelectTrigger class="w-[160px]">
                <SelectValue :placeholder="mode === 'completion' ? 'Text Completion' : 'Chat'" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="completion">Text Completion</SelectItem>
                  <SelectItem value="chat">Chat</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <Button variant="outline" @click="showConfigModal = true">
            <Settings class="mr-2 h-4 w-4" />
            Settings
          </Button>
        </div>
      </div>
    </div>

    <!-- Main Content -->
    <div class="container py-8">
      <!-- Mode Content -->
      <div v-if="mode === 'completion'" class="space-y-6">
        <Card>
          <CardHeader>
            <CardTitle>Prompt</CardTitle>
          </CardHeader>
          <CardContent>
            <Textarea
              v-model="completionPrompt"
              rows="6"
              placeholder="Enter your prompt here..."
            />
          </CardContent>
          <CardFooter class="flex justify-end">
            <Button
              :disabled="!completionPrompt"
              @click="submitCompletion"
            >
              Generate
            </Button>
          </CardFooter>
        </Card>

        <Card v-if="completionResult">
          <CardHeader>
            <CardTitle>Response</CardTitle>
          </CardHeader>
          <CardContent>
            <pre class="whitespace-pre-wrap text-sm">{{ completionResult }}</pre>
          </CardContent>
        </Card>
      </div>

      <div v-else class="space-y-6">
        <!-- System Message -->
        <Card>
          <CardHeader>
            <CardTitle>System Message</CardTitle>
            <CardDescription>Set the behavior and context for the assistant.</CardDescription>
          </CardHeader>
          <CardContent>
            <Textarea
              v-model="systemMessage"
              rows="2"
              placeholder="Enter system message..."
            />
          </CardContent>
        </Card>

        <!-- Chat Messages -->
        <div class="space-y-4">
          <Card
            v-for="(message, index) in chatMessages"
            :key="index"
          >
            <CardHeader class="flex flex-row items-center justify-between space-y-0 pb-2">
              <div class="flex items-center space-x-2">
                <Badge
                  :variant="message.role === 'user' ? 'default' : 'secondary'"
                >
                  {{ message.role === 'user' ? 'User' : 'Assistant' }}
                </Badge>
              </div>
              <Button
                v-if="index !== chatMessages.length - 1"
                variant="ghost"
                size="icon"
                @click="removeMessage(index)"
              >
                <X class="h-4 w-4" />
                <span class="sr-only">Remove message</span>
              </Button>
            </CardHeader>
            <CardContent>
              <Textarea
                v-model="message.content"
                rows="3"
                :placeholder="message.role === 'user' ? 'Enter your message...' : 'Assistant response...'"
                :disabled="message.role === 'assistant'"
              />
            </CardContent>
          </Card>
        </div>

        <!-- Chat Actions -->
        <div class="flex justify-between items-center">
          <Button variant="outline" @click="addMessage">
            <Plus class="mr-2 h-4 w-4" />
            Add Message
          </Button>
          <Button
            :disabled="!canSubmitChat"
            @click="submitChat"
          >
            Send
          </Button>
        </div>
      </div>
    </div>

    <!-- Configuration Modal -->
    <Dialog v-model:open="showConfigModal">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Model Settings</DialogTitle>
        </DialogHeader>
        <div class="space-y-4 py-4">
          <div class="space-y-2">
            <Label>Temperature</Label>
            <p class="text-sm text-muted-foreground">
              Controls randomness: 0 is deterministic, 1 is creative.
            </p>
            <Input
              v-model.number="modelConfig.temperature"
              type="number"
              min="0"
              max="2"
              step="0.1"
            />
          </div>
          <div class="space-y-2">
            <Label>Max Tokens</Label>
            <p class="text-sm text-muted-foreground">
              Maximum length of the generated response.
            </p>
            <Input
              v-model.number="modelConfig.max_tokens"
              type="number"
              min="1"
            />
          </div>
        </div>
        <DialogFooter>
          <Button @click="showConfigModal = false">
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { Settings, Plus, X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter } from '@/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Badge } from '@/components/ui/badge'

const runtimeConfig = useRuntimeConfig()
const apiBase = runtimeConfig.public.apiBase

const mode = ref('completion')
const models = ref([])
const selectedModel = ref(null)
const showConfigModal = ref(false)
const completionPrompt = ref('')
const completionResult = ref('')
const systemMessage = ref('')
const chatMessages = ref([])
const modelConfig = ref({
  temperature: 0.7,
  max_tokens: null
})

// Fetch available models
const fetchModels = async () => {
  try {
    const response = await fetch(`${apiBase}/api/services/llm-models/`)
    if (!response.ok) {
      const errorData = await response.json()
      console.error('Error response from API:', errorData)
      throw new Error(errorData.detail || 'Failed to fetch models')
    }
    const data = await response.json()
    console.log('Fetched models:', data)
    models.value = data
    if (models.value.length > 0) {
      selectedModel.value = models.value[0]
    } else {
      console.warn('No models available')
    }
  } catch (error) {
    console.error('Error fetching models:', error)
  }
}

// Add a new message to chat
const addMessage = () => {
  const lastMessage = chatMessages.value[chatMessages.value.length - 1]
  const newRole = !lastMessage || lastMessage.role === 'assistant' ? 'user' : 'assistant'
  chatMessages.value.push({ role: newRole, content: '' })
}

// Remove a message from chat
const removeMessage = (index) => {
  chatMessages.value.splice(index, 1)
}

// Check if chat can be submitted
const canSubmitChat = computed(() => {
  if (chatMessages.value.length === 0) return false
  const lastMessage = chatMessages.value[chatMessages.value.length - 1]
  return lastMessage.role === 'user' && lastMessage.content.trim() !== ''
})

// Submit completion request
const submitCompletion = async () => {
  if (!selectedModel.value || !completionPrompt.value) return

  try {
    const response = await fetch(`${apiBase}/api/inference/llms/v1/`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        model: selectedModel.value.name,
        prompt: completionPrompt.value,
        ...modelConfig.value
      }),
    })

    const data = await response.json()
    completionResult.value = data.choices[0].text
  } catch (error) {
    console.error('Error submitting completion:', error)
  }
}

// Submit chat request
const submitChat = async () => {
  if (!selectedModel.value || !canSubmitChat.value) return

  try {
    const messages = []
    if (systemMessage.value) {
      messages.push({ role: 'system', content: systemMessage.value })
    }
    messages.push(...chatMessages.value)

    const response = await fetch(`${apiBase}/api/inference/llms/v1/`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        model: selectedModel.value.name,
        messages,
        ...modelConfig.value
      }),
    })

    const data = await response.json()
    chatMessages.value.push({
      role: 'assistant',
      content: data.choices[0].message.content
    })
  } catch (error) {
    console.error('Error submitting chat:', error)
  }
}

// Initialize
onMounted(() => {
  fetchModels()
})
</script>