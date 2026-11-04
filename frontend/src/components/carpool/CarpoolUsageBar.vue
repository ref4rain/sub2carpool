<template>
  <div>
    <div v-if="label || (!compact && hint)" class="mb-1 flex items-center justify-between text-xs">
      <span class="text-gray-600 dark:text-gray-300">{{ label }}</span>
      <span class="text-gray-500 dark:text-dark-400">{{ clamped.toFixed(1) }}%<template v-if="!compact && hint"> · {{ hint }}</template></span>
    </div>
    <div class="h-2 w-full overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
      <div class="h-full rounded-full transition-all" :class="barClass" :style="{ width: `${clamped}%` }"></div>
    </div>
    <div v-if="compact && hint" class="mt-1 text-[11px] text-gray-500 dark:text-dark-400">{{ hint }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  percent: number
  label?: string
  hint?: string
  compact?: boolean
}>()

const clamped = computed(() => Math.min(100, Math.max(0, props.percent || 0)))
const barClass = computed(() => {
  if (clamped.value >= 90) return 'bg-red-500'
  if (clamped.value >= 70) return 'bg-amber-500'
  return 'bg-emerald-500'
})
</script>
