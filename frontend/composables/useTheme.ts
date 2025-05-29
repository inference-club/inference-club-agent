import { ref, watch } from 'vue'

const isDark = ref(false)

// Initialize theme from localStorage or system preference
if (import.meta.client) {
  const savedTheme = localStorage.getItem('theme')
  if (savedTheme) {
    isDark.value = savedTheme === 'dark'
  } else {
    isDark.value = window.matchMedia('(prefers-color-scheme: dark)').matches
  }
}

// Watch for theme changes and update document
watch(isDark, (newValue) => {
  if (import.meta.client) {
    document.documentElement.classList.toggle('dark', newValue)
    localStorage.setItem('theme', newValue ? 'dark' : 'light')
  }
}, { immediate: true })

export function useTheme() {
  const toggleTheme = () => {
    isDark.value = !isDark.value
  }

  return {
    isDark,
    toggleTheme
  }
}